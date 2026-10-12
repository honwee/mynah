package instinct

import (
	"context"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

// Thresholds are the gate's minimum probabilities, tuned from open-annie's
// production values: a clip needs a confident pick; a clip that the reply
// text itself announces ("let me wave") additionally needs the commit question
// to agree; partial (still-streaming) text needs more confidence than final.
type Thresholds struct {
	ClipMinP     float64 // choice confidence to play a clip (default 0.6)
	CommitMinP   float64 // noul "is the reply committing to an action" (0.6)
	PartialMinP  float64 // extra bar while text is still streaming (0.8)
	ReactionMinP float64 // speaking phase: a reaction to the line itself (0.75)
}

// Options configure one session's engine.
type Options struct {
	Provider    Provider      // primary (jev or rules)
	Fallback    Provider      // used when Provider errors; nil = none
	Timeout     time.Duration // per decision (default 1500ms)
	MinInterval time.Duration // between asks within a turn (default 450ms)
	MinNewRunes int           // new text required before asking again (8)
	MaxPerTurn  int           // asks per turn (4)
	Cooldown    time.Duration // between played clips (4s)
	Thresholds  Thresholds
	// Clips the avatar can play right now, id -> one-line description used as
	// the option's criteria (what the clip looks like / when it fits).
	Clips map[string]string
	// Act plays a clip; return false when it could not play right now (e.g.
	// the avatar is mid-speech). The engine then holds the clip and plays it
	// when the turn ends (Idle), so a reaction to a line lands right after
	// the line — the natural place for it on a video avatar, whose speech
	// frames cannot be blended with a baked clip.
	Act func(id string) bool
	// HoldFor bounds how long a held clip stays valid (default 8s).
	HoldFor time.Duration
	// Observe receives every decision for logging / UI (may be nil).
	Observe func(d Decision)
	LogTag  string
}

// Decision is one gated outcome, for logs and the console.
type Decision struct {
	At       time.Time         `json:"at"`
	Phase    string            `json:"phase"`    // "listening" | "speaking"
	Provider string            `json:"provider"` // which provider answered
	State    string            `json:"state"`    // text the question was asked about (trimmed)
	Answers  map[string]Answer `json:"answers"`
	Clip     string            `json:"clip,omitempty"` // what we played or held ("" = nothing)
	When     string            `json:"when,omitempty"` // "now" | "after_speech"
	Reason   string            `json:"reason"`         // why played / why not
	Latency  time.Duration     `json:"latency_ms"`
	Err      string            `json:"error,omitempty"`
}

// Engine is per session. Feed it text and phase changes; it asks, gates and
// acts in the background. All methods are safe for concurrent use and never
// block the caller.
type Engine struct {
	o Options

	mu        sync.Mutex
	phase     string // "idle" | "listening" | "speaking"
	userText  strings.Builder
	agentText strings.Builder
	askedAt   time.Time
	askedLen  int
	asks      int
	inflight  bool
	lastClip  time.Time
	pendAsk   bool // an ask was wanted while one was in flight
	pendFinal bool
	held      string // clip waiting for the turn to end
	heldAt    time.Time
	closed    bool
	cancel    context.CancelFunc
	ctx       context.Context
	decisions []Decision
}

func New(o Options) *Engine {
	if o.Timeout <= 0 {
		o.Timeout = 1500 * time.Millisecond
	}
	if o.MinInterval <= 0 {
		o.MinInterval = 450 * time.Millisecond
	}
	if o.MinNewRunes <= 0 {
		o.MinNewRunes = 8
	}
	if o.MaxPerTurn <= 0 {
		o.MaxPerTurn = 4
	}
	if o.Cooldown <= 0 {
		o.Cooldown = 4 * time.Second
	}
	if o.Thresholds.ClipMinP <= 0 {
		o.Thresholds.ClipMinP = 0.6
	}
	if o.Thresholds.CommitMinP <= 0 {
		o.Thresholds.CommitMinP = 0.6
	}
	if o.Thresholds.PartialMinP <= 0 {
		o.Thresholds.PartialMinP = 0.8
	}
	if o.Thresholds.ReactionMinP <= 0 {
		o.Thresholds.ReactionMinP = 0.75
	}
	if o.HoldFor <= 0 {
		// A reaction decided early in a long line should still land at its end;
		// replies run 5-15 s, so hold well past that.
		o.HoldFor = 20 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Engine{o: o, phase: "idle", ctx: ctx, cancel: cancel}
}

func (e *Engine) Close() {
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
	e.cancel()
}

// Recent returns the last n decisions (newest last).
func (e *Engine) Recent(n int) []Decision {
	e.mu.Lock()
	defer e.mu.Unlock()
	if n > len(e.decisions) {
		n = len(e.decisions)
	}
	out := make([]Decision, n)
	copy(out, e.decisions[len(e.decisions)-n:])
	return out
}

// UserSpeechStart: the visitor started talking — a new listening turn.
func (e *Engine) UserSpeechStart() { e.turn("listening") }

// UserText feeds visitor transcript (partial or final). final=true flushes a
// decision even below the new-rune bar.
func (e *Engine) UserText(text string, final bool) {
	e.mu.Lock()
	if e.phase != "listening" {
		e.phase = "listening"
		e.resetTurnLocked()
	}
	e.userText.Reset()
	e.userText.WriteString(text)
	e.mu.Unlock()
	e.maybeAsk(final)
}

// AgentSpeechStart: the avatar begins its reply — a new speaking turn.
func (e *Engine) AgentSpeechStart() { e.turn("speaking") }

// AgentText feeds the reply text as it streams (delta appended).
func (e *Engine) AgentText(delta string, final bool) {
	e.mu.Lock()
	if e.phase != "speaking" {
		e.phase = "speaking"
		e.resetTurnLocked()
	}
	e.agentText.WriteString(delta)
	e.mu.Unlock()
	e.maybeAsk(final)
}

// Idle: the turn ended (agent speech end / interrupted). A clip held during
// speech plays now, while the picture is back on the idle loop.
func (e *Engine) Idle() {
	e.turn("idle")
	e.mu.Lock()
	clip, at := e.held, e.heldAt
	e.held = ""
	e.mu.Unlock()
	if clip == "" || e.o.Act == nil {
		return
	}
	if time.Since(at) > e.o.HoldFor {
		if e.o.LogTag != "" {
			log.Printf("[instinct %s] held clip %q expired (%.1fs)", e.o.LogTag, clip, time.Since(at).Seconds())
		}
		return
	}
	if e.o.Act(clip) {
		e.mu.Lock()
		e.lastClip = time.Now()
		e.mu.Unlock()
		if e.o.LogTag != "" {
			log.Printf("[instinct %s] held clip %q played after speech (%.1fs after decision)", e.o.LogTag, clip, time.Since(at).Seconds())
		}
	} else if e.o.LogTag != "" {
		log.Printf("[instinct %s] held clip %q could not play", e.o.LogTag, clip)
	}
}

func (e *Engine) turn(phase string) {
	e.mu.Lock()
	e.phase = phase
	e.resetTurnLocked()
	e.mu.Unlock()
}

func (e *Engine) resetTurnLocked() {
	e.userText.Reset()
	e.agentText.Reset()
	e.askedAt = time.Time{}
	e.askedLen = 0
	e.asks = 0
}

// maybeAsk applies the pacer and, when due, runs one decision in the background.
func (e *Engine) maybeAsk(final bool) {
	e.mu.Lock()
	if e.closed || e.o.Provider == nil || len(e.o.Clips) == 0 || e.phase == "idle" {
		e.mu.Unlock()
		return
	}
	if e.inflight {
		// Do not drop a final utterance that lands mid-decision: re-ask when
		// the in-flight decision returns.
		e.pendAsk = true
		e.pendFinal = e.pendFinal || final
		e.mu.Unlock()
		return
	}
	text := e.userText.String()
	if e.phase == "speaking" {
		text = e.agentText.String()
	}
	n := len([]rune(text))
	now := time.Now()
	if e.asks >= e.o.MaxPerTurn || (!final && n-e.askedLen < e.o.MinNewRunes) ||
		(!e.askedAt.IsZero() && now.Sub(e.askedAt) < e.o.MinInterval) || strings.TrimSpace(text) == "" {
		e.mu.Unlock()
		return
	}
	if now.Sub(e.lastClip) < e.o.Cooldown {
		e.mu.Unlock()
		return
	}
	e.inflight = true
	e.askedAt = now
	e.askedLen = n
	e.asks++
	phase := e.phase
	e.mu.Unlock()
	go e.decide(phase, text, final)
}

func (e *Engine) questions(phase string) (map[string]Question, string) {
	opts := map[string]string{}
	for id, desc := range e.o.Clips {
		opts[id] = desc
	}
	opts["none"] = "不做任何动作，保持自然待机"
	q := map[string]Question{
		"clip": {Type: "choice", Criteria: opts,
			Instructions: "数字人此刻最合适的身体反应。只在非常贴切时选动作，不确定就选 none。"},
	}
	situ := "访客正在说话，数字人在听。"
	if phase == "speaking" {
		situ = "数字人正要说出下面这段话。"
		q["commit"] = Question{Type: "noul", Instructions: "这段话是否明确承诺/描述了数字人正在做一个肢体动作（如挥手、点头、摇头）？"}
		q["reaction"] = Question{Type: "choice", Criteria: opts,
			Instructions: "说完这句话之后，数字人最自然的即时反应/表情是哪一个？只在情绪明确时选，不确定就选 none。"}
	}
	return q, situ
}

func (e *Engine) decide(phase, text string, final bool) {
	qs, situ := e.questions(phase)
	state := situ + "\n文本：" + lastRunes(text, 200)
	t0 := time.Now()
	ctx, cancel := context.WithTimeout(e.ctx, e.o.Timeout)
	defer cancel()
	prov := e.o.Provider
	ans, err := prov.Decide(ctx, state, qs)
	if err != nil && e.o.Fallback != nil {
		if e.o.LogTag != "" {
			log.Printf("[instinct %s] %s failed (%v) -> %s", e.o.LogTag, prov.Name(), err, e.o.Fallback.Name())
		}
		prov = e.o.Fallback
		ans, err = prov.Decide(context.Background(), state, qs)
	}
	d := Decision{At: t0, Phase: phase, Provider: prov.Name(), State: lastRunes(text, 60), Answers: ans, Latency: time.Since(t0)}
	if err != nil {
		d.Err = err.Error()
		d.Reason = "provider error"
	} else {
		d.Clip, d.Reason = e.gate(phase, final, ans)
	}
	if d.Clip != "" && e.o.Act != nil {
		if phase == "speaking" || !e.o.Act(d.Clip) {
			// Mid-speech (or the act refused): hold it for the end of the turn.
			d.When = "after_speech"
			e.mu.Lock()
			e.held, e.heldAt = d.Clip, time.Now()
			e.mu.Unlock()
		} else {
			d.When = "now"
		}
	}
	e.mu.Lock()
	e.inflight = false
	if d.When == "now" {
		e.lastClip = time.Now()
	}
	e.decisions = append(e.decisions, d)
	if len(e.decisions) > 50 {
		e.decisions = e.decisions[len(e.decisions)-50:]
	}
	again, againFinal := e.pendAsk, e.pendFinal
	e.pendAsk, e.pendFinal = false, false
	e.mu.Unlock()
	if again {
		defer e.maybeAsk(againFinal)
	}
	if e.o.LogTag != "" {
		log.Printf("[instinct %s] %s/%s %s -> clip=%q %s (%s) %dms", e.o.LogTag, prov.Name(), phase,
			shortJSON(ans), d.Clip, d.When, d.Reason, d.Latency.Milliseconds())
	}
	if e.o.Observe != nil {
		e.o.Observe(d)
	}
}

// gate turns answers into at most one clip id, or "" with a reason.
//
// Listening: the "clip" pick plays when confident. Speaking: a "clip" pick
// needs the reply to commit to it ("let me wave"); otherwise the "reaction"
// pick (how she reacts to her own line) plays after the line when confident.
func (e *Engine) gate(phase string, final bool, ans map[string]Answer) (string, string) {
	pick := func(name string, min float64) (string, float64, string) {
		a, ok := ans[name]
		if !ok || a.Choice == "" {
			return "", 0, "no opinion"
		}
		if a.Choice == "none" {
			return "", 0, "none"
		}
		if _, has := e.o.Clips[a.Choice]; !has {
			return "", 0, "unknown clip " + a.Choice
		}
		p := choiceConf(a)
		if !final && e.o.Thresholds.PartialMinP > min {
			min = e.o.Thresholds.PartialMinP
		}
		if p < min {
			return "", p, "below threshold"
		}
		return a.Choice, p, "ok"
	}
	clip, _, why := pick("clip", e.o.Thresholds.ClipMinP)
	if phase != "speaking" {
		return clip, why
	}
	if clip != "" {
		if c, ok := ans["commit"]; ok && c.Noul >= e.o.Thresholds.CommitMinP {
			return clip, "committed gesture"
		}
	}
	if r, _, rwhy := pick("reaction", e.o.Thresholds.ReactionMinP); r != "" {
		return r, "reaction"
	} else if rwhy != "none" && rwhy != "no opinion" {
		return "", "reaction " + rwhy
	}
	if clip != "" {
		return "", "reply does not commit to a gesture"
	}
	return "", why
}

func choiceConf(a Answer) float64 {
	if a.Confidence > 0 {
		return a.Confidence
	}
	if a.Probabilities != nil {
		return a.Probabilities[a.Choice]
	}
	return 0
}

func lastRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

func shortJSON(ans map[string]Answer) string {
	keys := make([]string, 0, len(ans))
	for k := range ans {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(' ')
		}
		a := ans[k]
		switch a.Type {
		case "noul":
			b.WriteString(k + "=" + trimF(a.Noul))
		case "score":
			b.WriteString(k + "=" + trimF(a.Score))
		default:
			b.WriteString(k + "=" + a.Choice + ":" + trimF(choiceConf(a)))
		}
	}
	return b.String()
}

func trimF(f float64) string {
	s := strings.TrimRight(strings.TrimRight(fmtFloat(f), "0"), ".")
	if s == "" {
		return "0"
	}
	return s
}
