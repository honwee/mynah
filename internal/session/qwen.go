// Qwen-Audio realtime "brain" mode: when the manager carries a qwenrt.Config,
// chat turns bypass the LLM+TTS sentence pipeline and run one speech-to-speech
// exchange against the cloud model instead (internal/qwenrt). The utterance
// AUDIO goes up when the turn stack captured it (paralinguistics reach the
// model); typed chat and coalesced multi-utterance turns inject text. Reply
// PCM arrives already resampled to 16k f32 — the same AudioChunk feed Speak()
// uses, so the avatar engine and every downstream encoder stay untouched.
package session

import (
	"context"
	"encoding/binary"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	pb "mynah/gen/go/proto/avatarengine/v1"
	"mynah/internal/llm"
	"mynah/internal/qwenrt"
)

// SetQwenSource wires the brain-mode resolution (config group "brain") into
// session creation: fn maps a frozen channel-snapshot brain (nil = read the
// live console selection) to a realtime dial config, or nil for local. Called
// once per NEW session and frozen for that session's lifetime, like
// SetTurnModeSource.
func (m *Manager) SetQwenSource(fn func(frozen *BrainConfig) *qwenrt.Config) { m.qwenSource = fn }

// qwenMode reports whether THIS session runs on the Qwen realtime brain
// (frozen at session create — a console PUT applies to the next session).
func (s *Session) qwenMode() bool { return s.qwenCfg != nil }

// qwenState is the per-session lazy connection (one WebSocket = one
// server-side conversation context, kept for the session's lifetime).
type qwenState struct {
	mu   sync.Mutex
	conn *qwenrt.Conn
}

// qwenConn returns the session's live realtime connection, dialing (or
// redialing after a drop — server context is lost, acceptable) on demand.
// The dial carries this session's frozen turn mode: cloud-VAD sessions get a
// server_vad/smart_turn WebSocket, local ones push-to-talk (turn_detection
// is fixed per connection).
func (s *Session) qwenConn() (*qwenrt.Conn, error) {
	s.qwen.mu.Lock()
	defer s.qwen.mu.Unlock()
	if s.qwen.conn != nil && s.qwen.conn.Alive() {
		return s.qwen.conn, nil
	}
	cfg := *s.qwenCfg
	if s.cloudVADMode() {
		cfg.TurnMode = s.turnMode
		cfg.VadThreshold = s.turnLive.VadThreshold
		cfg.VadSilenceMS = s.turnLive.VadSilenceMS
	}
	conn, err := qwenrt.Dial(s.ctx, cfg)
	if err != nil {
		return nil, err
	}
	log.Printf("[session %s] qwen realtime connected (%s, turn=%s)", s.ID, cfg.Model,
		func() string {
			if cfg.TurnMode == "" {
				return "manual"
			}
			return cfg.TurnMode
		}())
	// Redial context restore: the server keeps conversation memory per
	// WebSocket, so a fresh dial starts amnesiac. Replay the local history
	// (probe-verified: user=input_text, assistant=output_text) before any
	// live turn reaches the new connection.
	s.histMu.Lock()
	hist := append([]llm.Message(nil), s.history...)
	s.histMu.Unlock()
	for _, m := range hist {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		if err := conn.SeedItem(m.Role, m.Content); err != nil {
			log.Printf("[session %s] qwen history seed: %v", s.ID, err)
			break
		}
	}
	if len(hist) > 0 {
		log.Printf("[session %s] qwen history seeded (%d messages)", s.ID, len(hist))
	}
	s.qwen.conn = conn
	if s.cloudVADMode() {
		// One Serve loop per connection consumes every server-initiated
		// response — voice (server VAD segments the mic stream) and typed
		// injections alike — so it must run even before a mic track exists.
		go s.qwenServe(conn)
	}
	return conn, nil
}

func (s *Session) qwenCancel() {
	s.qwen.mu.Lock()
	conn := s.qwen.conn
	s.qwen.mu.Unlock()
	if conn != nil && conn.Alive() {
		conn.Cancel()
	}
}

func (s *Session) qwenClose() {
	s.qwen.mu.Lock()
	conn := s.qwen.conn
	s.qwen.conn = nil
	s.qwen.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
}

// qwenSpeak is Speak()'s cloud counterpart: the scripted line is read aloud by
// the realtime model in the session's cloud voice, so a qwen-brain session
// never mixes in the local TTS voice (the channel greeting used to, making the
// first utterance of every cloud session sound like a different person).
//
// Cloud-VAD sessions hand the line to the live Serve loop, which already owns
// every response event (subtitles, FSM, engine feed) — this function only
// submits it. Push-to-talk sessions have no Serve loop, so they run the full
// turn here, mirroring qwenChat's bookkeeping.
func (s *Session) qwenSpeak(text string) {
	conn, err := s.qwenConn()
	if err != nil {
		log.Printf("[session %s] qwen dial (speak): %v", s.ID, err)
		return
	}
	log.Printf("[turn %s] qwen speak: %q", s.ID, text)

	if s.cloudVADMode() {
		if err := conn.SpeakVerbatim(text); err != nil {
			log.Printf("[session %s] qwen speak: %v", s.ID, err)
		}
		return
	}

	s.turnMu.Lock()
	if s.turnCancel != nil {
		s.turnCancel()
	}
	tctx, tcancel := context.WithCancel(s.ctx)
	s.turnCancel = tcancel
	s.turnMu.Unlock()

	s.turnStart.Store(time.Now().UnixNano())
	s.turnTTSSeen.Store(false)
	s.turnSpkSeen.Store(false)
	s.turns.Add(1)
	s.dcEmit("start", "")

	go func() {
		s.speaking.Store(true)
		s.fsm.to(StateThinking, EvAgentThinking, "")
		defer s.speaking.Store(false)

		seq := uint64(0)
		var playhead time.Time
		subTitled := false
		_, err := conn.SpeakVerbatimTurn(tctx, text, func(out []float32) {
			if s.turnTTSSeen.CompareAndSwap(false, true) {
				log.Printf("[turn %s] qwen speak first pcm +%dms", s.ID, s.sinceTurnMs())
				s.fsm.to(StateSpeaking, EvAgentSpeechStart, "")
			}
			if playhead.IsZero() {
				playhead = time.Now()
				// The line is known up front (unlike a reply), so the subtitle
				// can go out with playback instead of waiting for a transcript.
				subTitled = true
				s.scheduleDC(tctx, playhead.Add(subtitleLag), "ing", text)
			}
			playhead = playhead.Add(pcmDur(len(out)))
			buf := make([]byte, len(out)*4)
			for i, v := range out {
				binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
			}
			_ = s.send(&pb.ClientFrame{Msg: &pb.ClientFrame_Audio{
				Audio: &pb.AudioChunk{PcmF32Le_16K: buf, Seq: seq},
			}})
			seq++
		}, nil)
		if err != nil && tctx.Err() == nil {
			log.Printf("[session %s] qwen speak turn: %v", s.ID, err)
		}
		if !subTitled {
			s.dcEmit("end", "")
		} else {
			s.scheduleDC(tctx, playhead.Add(subtitleLag), "end", "")
		}
	}()
}

// seedQwenRAG retrieves knowledge for this turn and seeds it into the cloud
// conversation as a reference item ahead of the model's reply. Same retriever,
// reranker, options and rendering the local brain uses — only the carrier
// differs (the realtime API has no per-turn system message).
//
// Push-to-talk only. Retrieval failure never blocks the turn (retrieveForTurn
// already degrades to nil), and a seed error is logged, not fatal: an
// ungrounded answer beats no answer.
func (s *Session) seedQwenRAG(ctx context.Context, conn *qwenrt.Conn, query string) {
	if strings.TrimSpace(query) == "" {
		return
	}
	msgs := s.ragContext(ctx, query)
	if len(msgs) == 0 {
		return
	}
	if err := conn.SeedKnowledge(msgs[0].Content); err != nil {
		log.Printf("[session %s] qwen rag seed: %v", s.ID, err)
	}
}

// qwenRAGUnavailable reports whether this session has a knowledge base
// configured that CANNOT be used, and logs it once at session start.
//
// Cloud-VAD sessions hand turn-taking to the server, which creates the
// response the instant speech stops — probe-verified to land BEFORE the
// transcript is even delivered, and turn_detection.create_response=false is
// ignored by the server. So there is no window in which cored knows the
// question but generation hasn't started, and nowhere to put retrieved
// context. Switching 对话轮次 to 本地 restores it.
func (s *Session) qwenRAGUnavailable() bool {
	if !s.cloudVADMode() {
		return false
	}
	r, _, opts := s.retrieverConf()
	return r != nil && opts.Enabled
}

// qwenChat runs one realtime turn: utterance audio (pcm != nil) or injected
// text up, streamed reply speech into the engine. Mirrors Chat()'s turn
// bookkeeping (cancel in-flight, FSM, latency probes, subtitles).
//
// In cloud-VAD mode the Serve loop (cloudvad.go) owns all response events, so
// a typed message is only injected — the reply streams back through Serve.
func (s *Session) qwenChat(text string, pcm []byte) {
	if s.cloudVADMode() {
		conn, err := s.qwenConn()
		if err != nil {
			log.Printf("[session %s] qwen dial: %v", s.ID, err)
			return
		}
		if s.speaking.Load() {
			s.Interrupt()
		}
		log.Printf("[turn %s] qwen inject text: %q", s.ID, text)
		if err := conn.InjectText(text); err != nil {
			log.Printf("[session %s] qwen inject: %v", s.ID, err)
		}
		return
	}
	s.turnMu.Lock()
	if s.turnCancel != nil {
		s.turnCancel()
	}
	tctx, tcancel := context.WithCancel(s.ctx)
	s.turnCancel = tcancel
	s.turnMu.Unlock()

	s.turnStart.Store(time.Now().UnixNano())
	s.turnTTSSeen.Store(false)
	s.turnSpkSeen.Store(false)
	s.turns.Add(1)
	log.Printf("[turn %s] qwen start: %q (audio=%dB)", s.ID, text, len(pcm))
	s.dcEmit("start", "")

	go func() {
		s.speaking.Store(true)
		s.fsm.to(StateThinking, EvAgentThinking, "")
		defer s.speaking.Store(false)

		conn, err := s.qwenConn()
		if err != nil {
			log.Printf("[session %s] qwen dial: %v", s.ID, err)
			s.dcEmit("end", "")
			return
		}
		seq := uint64(0)
		var playhead time.Time
		// Knowledge base: retrieve on the user's text and seed it as a
		// reference item before the turn. Only possible here — see
		// qwenRAGUnavailable for why cloud-VAD sessions can't.
		s.seedQwenRAG(tctx, conn, text)
		reply, err := conn.Turn(tctx, text, pcm, func(out []float32) {
			if s.turnTTSSeen.CompareAndSwap(false, true) {
				log.Printf("[turn %s] qwen first pcm +%dms", s.ID, s.sinceTurnMs())
				s.fsm.to(StateSpeaking, EvAgentSpeechStart, "")
			}
			if playhead.IsZero() {
				playhead = time.Now()
			}
			playhead = playhead.Add(pcmDur(len(out)))
			buf := make([]byte, len(out)*4)
			for i, v := range out {
				binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
			}
			_ = s.send(&pb.ClientFrame{Msg: &pb.ClientFrame_Audio{
				Audio: &pb.AudioChunk{PcmF32Le_16K: buf, Seq: seq},
			}})
			seq++
		}, func(transcript string) {
			// Full reply text lands near the end of the audio stream; show it as
			// one subtitle for the remainder of playback.
			s.dcEmit("ing", transcript)
		})
		if err != nil && tctx.Err() == nil {
			log.Printf("[session %s] qwen turn: %v", s.ID, err)
		}
		if reply != "" {
			s.histMu.Lock()
			s.history = append(s.history, llm.Message{Role: "user", Content: text},
				llm.Message{Role: "assistant", Content: reply})
			if len(s.history) > 20 {
				s.history = append([]llm.Message(nil), s.history[len(s.history)-20:]...)
			}
			s.histMu.Unlock()
			log.Printf("[turn %s] qwen reply (%d chars) done +%dms",
				s.ID, len([]rune(reply)), s.sinceTurnMs())
		}
		if playhead.IsZero() {
			s.dcEmit("end", "")
		} else {
			s.scheduleDC(tctx, playhead.Add(subtitleLag), "end", "")
		}
	}()
}
