package session

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
	"unicode"

	"mynah/core"
)

// TurnConfig holds the semantic turn-taking knobs (LiveKit calls these the
// endpointing/min-interruption settings). Zero values are filled by
// withDefaults.
type TurnConfig struct {
	GraceMS           int      // hold window after a low-EOT utterance before committing what we have
	MinInterruptMS    int      // mic speech must sustain this long to barge-in (filters short backchannels)
	PredictTimeoutMS  int      // turn-detector RPC timeout
	Language          string   // STT language hint passed to the detector ("zh")
	HistoryDepth      int      // DEPRECATED, no-op: the audio turn detector judges the waveform, not chat history
	InterruptMinChars int      // while she speaks, an utterance must have >= this many chars to interrupt
	Backchannels      []string // ack words that never form a turn nor barge-in
	InterruptWords    []string // explicit "stop talking" intents that bypass the ack/length gates

	// ExtraInterruptWords/ExtraBackchannels are appended to the built-in lists
	// (operators add words without restating the defaults). Set via the
	// --turn-interrupt-words / --turn-backchannels flags.
	ExtraInterruptWords []string
	ExtraBackchannels   []string
}

func (c TurnConfig) withDefaults() TurnConfig {
	if c.GraceMS == 0 {
		c.GraceMS = 700
	}
	if c.MinInterruptMS == 0 {
		c.MinInterruptMS = -1 // <=0 disables acoustic barge-in (default): interruption is
		// decided on transcript CONTENT, so a drawn-out "嗯/对" never stops her.
	}
	if c.PredictTimeoutMS == 0 {
		c.PredictTimeoutMS = 1000
	}
	if c.Language == "" {
		c.Language = "zh"
	}
	if c.HistoryDepth == 0 {
		c.HistoryDepth = 6
	}
	if c.InterruptMinChars == 0 {
		c.InterruptMinChars = 4 // <4-char utterances (好好的/对对对) never interrupt mid-reply
	}
	if c.Backchannels == nil {
		c.Backchannels = defaultBackchannels
	}
	c.Backchannels = append(append([]string{}, c.Backchannels...), c.ExtraBackchannels...)
	if c.InterruptWords == nil {
		c.InterruptWords = defaultInterruptWords
	}
	c.InterruptWords = append(append([]string{}, c.InterruptWords...), c.ExtraInterruptWords...)
	return c
}

// defaultBackchannels are acknowledgements that should never stop her nor
// warrant an answer. Includes the way SenseVoice (lang=auto) mis-hears a
// Chinese "嗯": "Yeah"/"应"/"응"/"うん" etc.
var defaultBackchannels = []string{
	"嗯", "嗯嗯", "嗯哼", "恩", "哦", "哦哦", "噢", "噢噢", "啊", "唉", "诶",
	"对", "对对", "对对对", "是", "是的", "好", "好的", "行", "可以", "嗯呢",
	"ok", "okay", "yeah", "yep", "yes", "um", "uh", "huh", "hmm", "mm", "mhm",
	"应", "응", "네", "うん", "はい",
}

// turnOrchestrator decides, per ASR utterance, whether to barge-in over the
// avatar and whether the user has finished their turn — replacing the legacy
// "every transcript answers immediately, every speech onset interrupts" path.
//
// It is driven entirely from the single ASR-recv goroutine (onVad/onTranscript
// are never concurrent with each other); only the two timers fire on their own
// goroutines, so pending/timer state is guarded by mu. The detector RPC runs
// off-lock so it never stalls the recv goroutine's media-adjacent work.
type turnOrchestrator struct {
	s   *Session
	td  core.TurnDetector
	cfg TurnConfig

	mu       sync.Mutex
	pending  []string          // utterances accumulated while EOT is uncertain
	pcm      map[uint64][]byte // per-utterance audio (from the ASR worker) awaiting its transcript
	heldPCM  []byte            // audio of a lone held utterance, so a grace flush still sends 原声
	grace    *time.Timer       // commits pending when it fires
	interr   *time.Timer       // barge-in arm
	inSpeech bool              // mic VAD currently inside an utterance
}

func newTurnOrchestrator(s *Session, td core.TurnDetector, cfg TurnConfig) *turnOrchestrator {
	return &turnOrchestrator{s: s, td: td, cfg: cfg.withDefaults()}
}

// onVad handles a mic VAD edge. Speech onset while the avatar talks arms a
// min-duration timer; only if the speech sustains past it do we barge-in, so a
// quick "嗯" never cuts her off. An utterance that ends before the timer
// disarms it.
func (o *turnOrchestrator) onVad(speech bool, utt uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if speech {
		o.inSpeech = true
		// Acoustic barge-in is opt-in (MinInterruptMS > 0). Disabled by default
		// because sound alone can't tell a backchannel ("嗯") from a real
		// interruption — that's decided on the transcript in onTranscript. With
		// no headphones the avatar's own voice also leaks into the mic, so a
		// duration-only trigger would have her cut herself off.
		if o.cfg.MinInterruptMS > 0 && o.s.speaking.Load() {
			if o.interr != nil {
				o.interr.Stop()
			}
			o.interr = time.AfterFunc(time.Duration(o.cfg.MinInterruptMS)*time.Millisecond, func() {
				o.mu.Lock()
				still := o.inSpeech
				o.mu.Unlock()
				if still && o.s.speaking.Load() {
					log.Printf("[turn %s] barge-in (utt=%d, sustained >%dms)", o.s.ID, utt, o.cfg.MinInterruptMS)
					o.s.Interrupt()
				}
			})
		}
	} else {
		o.inSpeech = false
		if o.interr != nil {
			o.interr.Stop()
			o.interr = nil
		}
	}
}

// onUtteranceAudio stashes the just-segmented utterance's PCM (16k mono f32 LE,
// from the ASR worker — emitted just BEFORE its transcript) so onTranscript can
// hand it to the audio turn detector. Runs on the same ASR-recv goroutine as
// onTranscript, so the stash-then-read is ordered; o.mu only guards against the
// timers.
func (o *turnOrchestrator) onUtteranceAudio(utt uint64, pcm []byte) {
	o.mu.Lock()
	if o.pcm == nil {
		o.pcm = make(map[uint64][]byte, 2)
	}
	o.pcm[utt] = pcm
	for k := range o.pcm { // bound memory: drop anything older than the previous utterance
		if k+1 < utt {
			delete(o.pcm, k)
		}
	}
	o.mu.Unlock()
}

// onTranscript handles a final per-utterance transcript: drop pure
// backchannels, else accumulate and ask the detector (on this utterance's
// audio) whether the user is done. EOT -> commit now; not-EOT -> hold for grace
// and coalesce the next utterance.
func (o *turnOrchestrator) onTranscript(text string, utt uint64) {
	o.mu.Lock()
	o.stopGrace()
	if isNoise(text) {
		o.mu.Unlock()
		log.Printf("[turn %s] noise %q ignored", o.s.ID, text)
		return
	}
	// Acks ("嗯/对/好的") and ultra-short ASR fragments ("阿唔"/"네"/"Sure")
	// never drive a turn — they neither interrupt her nor get answered — UNLESS
	// she just asked a question, in which case a short "好的" is a real answer.
	// This is LiveKit's split: backchannels suppress interruptions; the model +
	// conversational context own real turn-taking. (Checked regardless of
	// speaking state: idle gaps between her short replies were leaking acks
	// through as fresh turns.)
	// An explicit interrupt-intent word ("停"/"打断"/"别说") bypasses the ack +
	// min-length gates so even a 2-char "打断" cuts her off immediately. The one
	// small reverse wordlist — backchannels never contain these.
	if !o.hasInterruptIntent(text) {
		if o.isAckish(text) {
			// Acks ("嗯/对/好/好的/嗯嗯") never drive a turn — never interrupt her,
			// never get answered. (No "but she asked a question" exception: the
			// flirty persona ends nearly every line with a question, which made that
			// exception leak every ack through. A lone "好" answer is dropped — fine
			// for a companion; the user can say more.)
			o.mu.Unlock()
			log.Printf("[turn %s] ack %q ignored", o.s.ID, text)
			return
		}
		if o.s.speaking.Load() && runeLen(text) < o.cfg.InterruptMinChars {
			// Mid-reply, only a SUBSTANTIAL utterance interrupts her. Short
			// non-ack fragments ASR can't pin to the wordlist ("好好的"/"对对对")
			// stay below the bar, so they never cut her off. Real barge-in = a
			// fuller sentence.
			o.mu.Unlock()
			log.Printf("[turn %s] short %q during reply, not interrupting", o.s.ID, text)
			return
		}
	}
	o.pending = append(o.pending, text)
	pcm := o.pcm[utt]
	delete(o.pcm, utt)
	single := len(o.pending) == 1
	o.mu.Unlock()

	eot, prob := o.predict(pcm)

	o.mu.Lock()
	if eot {
		joined := strings.Join(o.pending, "，")
		o.pending = nil
		o.heldPCM = nil
		o.mu.Unlock()
		log.Printf("[turn %s] EOT (p=%.2f) commit: %q", o.s.ID, prob, joined)
		// A clean single-utterance turn keeps its audio (the qwen realtime brain
		// hears tone, not just words); coalesced turns fall back to joined text.
		if single {
			o.commit(joined, pcm)
		} else {
			o.commit(joined, nil)
		}
		return
	}
	if single {
		o.heldPCM = pcm // grace may flush this lone utterance — keep its 原声
	} else {
		o.heldPCM = nil
	}
	log.Printf("[turn %s] hold (p=%.2f), grace %dms: %q", o.s.ID, prob, o.cfg.GraceMS, strings.Join(o.pending, "，"))
	o.grace = time.AfterFunc(time.Duration(o.cfg.GraceMS)*time.Millisecond, o.flushGrace)
	o.mu.Unlock()
}

// flushGrace commits whatever has accumulated when the hold window expires.
func (o *turnOrchestrator) flushGrace() {
	o.mu.Lock()
	if len(o.pending) == 0 {
		o.mu.Unlock()
		return
	}
	joined := strings.Join(o.pending, "，")
	pcm := o.heldPCM
	o.pending = nil
	o.heldPCM = nil
	o.mu.Unlock()
	log.Printf("[turn %s] grace expired, commit: %q", o.s.ID, joined)
	o.commit(joined, pcm)
}

// commit answers a finished turn. A clean barge-in flush precedes the chat
// when the avatar is still mid-reply (the EOT path may fire without onVad
// having armed). pcm (optional) is the single utterance's audio for the qwen
// realtime brain; nil = text goes up.
func (o *turnOrchestrator) commit(text string, pcm []byte) {
	if o.s.speaking.Load() {
		o.s.Interrupt()
	}
	o.s.fsm.emit(EvTurnCommitted, text)
	o.s.ChatVoice(text, pcm)
}

func (o *turnOrchestrator) stopGrace() {
	if o.grace != nil {
		o.grace.Stop()
		o.grace = nil
	}
}

// predict asks the turn detector on the utterance's audio; any error (timeout,
// or missing audio) falls back to EOT=true so a detector outage degrades to the
// legacy answer-immediately behavior rather than hanging the turn.
func (o *turnOrchestrator) predict(pcm []byte) (eot bool, prob float32) {
	if len(pcm) == 0 {
		log.Printf("[turn %s] no utterance audio; committing", o.s.ID)
		return true, 1
	}
	ctx, cancel := context.WithTimeout(o.s.ctx, time.Duration(o.cfg.PredictTimeoutMS)*time.Millisecond)
	defer cancel()
	p, e, err := o.td.Predict(ctx, pcm, o.cfg.Language)
	if err != nil {
		log.Printf("[turn %s] turn detector error: %v (committing)", o.s.ID, err)
		return true, 1
	}
	return e, p
}

// isBackchannel reports whether the utterance is purely acknowledgements —
// every token (split on ASR's comma/space) is in the backchannel set. So
// "嗯", "嗯，对", "对对 好的" all count, but "嗯，那你说说" does not.
// ackRunes are characters that on their own make up acknowledgements/filler.
// An utterance composed ENTIRELY of these (any length) is a backchannel —
// catches repeated strings ("嗯嗯嗯嗯对是") the wordlist + length gate miss.
// Deliberately excludes particles that begin real words/questions (的呢吧吗).
const ackRunes = "嗯唔恩呃唉诶哦噢喔啊呀对是好行嗨哈"

// defaultInterruptWords are explicit "stop talking" intents that should cut her
// off even when short — the deliberate counterpart to the ack filter.
// Backchannels never contain these, so a substring match is safe. Operators can
// append more via --turn-interrupt-words.
var defaultInterruptWords = []string{"打断", "停一下", "停下", "别说", "别讲", "等一下", "等等", "闭嘴", "安静", "停"}

func (o *turnOrchestrator) hasInterruptIntent(text string) bool {
	for _, w := range o.cfg.InterruptWords {
		if strings.Contains(text, w) {
			return true
		}
	}
	return false
}

// isAckish reports whether the utterance is an acknowledgement, an ultra-short
// (≤2 char) fragment, or all-filler — the things that should never, on their
// own, drive a turn. Covers the non-lexical sounds ASR mangles ("阿唔"/"唔") and
// repeated acks ("对对对") that no wordlist enumerates. The length/filler
// heuristics only apply while she is speaking: idle, a short "你好"/"嗨" is a
// real opener and deserves an answer (only wordlist acks stay muted there).
func (o *turnOrchestrator) isAckish(text string) bool {
	if o.isBackchannel(text) {
		return true
	}
	if !o.s.speaking.Load() {
		return false
	}
	strip := strings.TrimRight(strings.TrimSpace(text), "。，！？、,.!?… ")
	if len([]rune(strip)) <= 2 {
		return true
	}
	return isAllAckUnits(text)
}

// ackUnits are multi-char dismissive fillers ("好了"/"够了"/"行了"). Combined
// with ackRunes (single-char acks) in a greedy consume, isAllAckUnits catches
// run-on repeats with no separators ("好了好了好了", "嗯嗯对是好了") that the
// wordlist (whole-token) and charset (single-rune) checks miss. User chose to
// treat these as acks; "stop" intent is served by interruptWords instead.
var ackUnits = []string{"好了", "够了", "行了", "过了", "算了", "得了", "好的", "是的", "可以", "嗯嗯", "对对", "哦哦", "好吧"}

func isAllAckUnits(text string) bool {
	var b strings.Builder
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		}
	}
	rs := []rune(b.String())
	if len(rs) == 0 {
		return false
	}
	for i := 0; i < len(rs); {
		matched := false
		for _, u := range ackUnits {
			ur := []rune(u)
			if i+len(ur) <= len(rs) && string(rs[i:i+len(ur)]) == u {
				i += len(ur)
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		if strings.ContainsRune(ackRunes, rs[i]) {
			i++
			continue
		}
		return false
	}
	return true
}

// lastAssistantAskedQuestion reports whether her most recent reply ended as a
// question — the case where a following short "好的"/"对" IS a real answer and
// must be processed rather than dropped as a backchannel.
func (o *turnOrchestrator) lastAssistantAskedQuestion() bool {
	o.s.histMu.Lock()
	defer o.s.histMu.Unlock()
	for i := len(o.s.history) - 1; i >= 0; i-- {
		if o.s.history[i].Role != "assistant" {
			continue
		}
		t := strings.TrimRight(strings.TrimSpace(o.s.history[i].Content), "。！. \n")
		if t == "" {
			return false
		}
		if strings.ContainsAny(t, "?？") {
			return true
		}
		r := []rune(t)
		switch r[len(r)-1] {
		case '吗', '呢', '吧', '么':
			return true
		}
		return false
	}
	return false
}

func (o *turnOrchestrator) isBackchannel(text string) bool {
	// Split on ANY punctuation or whitespace (Chinese 。，！？、… and ASCII
	// .,!?) so "Okay." / "嗯，对" tokenize to bare words.
	toks := strings.FieldsFunc(text, func(r rune) bool {
		return unicode.IsPunct(r) || unicode.IsSpace(r)
	})
	if len(toks) == 0 {
		return false
	}
	for _, tok := range toks {
		hit := false
		for _, b := range o.cfg.Backchannels {
			if strings.EqualFold(tok, b) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

// isNoise reports whether the transcript carries no words at all (empty or
// punctuation-only, e.g. a bare "." from a mic blip).
func isNoise(text string) bool {
	return runeLen(text) == 0
}

// runeLen counts substantive characters (letters/digits), ignoring punctuation
// and whitespace — "how much was actually said".
func runeLen(text string) int {
	n := 0
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			n++
		}
	}
	return n
}
