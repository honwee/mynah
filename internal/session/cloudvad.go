// Cloud-VAD mode: turn detection delegated to the Qwen realtime server
// (turn_detection = server_vad / smart_turn). The ASR worker degrades to a
// pure Opus->PCM decoder (want_pcm), cored streams the continuous mic
// waveform into the realtime WebSocket, and the SERVER segments utterances,
// transcribes, decides end-of-turn and initiates responses on its own. The
// local turn orchestrator, Smart Turn worker and SenseVoice are all out of
// the loop for these sessions.
//
// Barge-in: the server emits speech_started the moment the user talks; we
// stop local playback and response.cancel immediately (the server does NOT
// auto-cancel; smart_turn's semantic filter keeps fillers from triggering).
package session

import (
	"context"
	"encoding/binary"
	"log"
	"math"
	"strings"
	"time"

	pbasr "mynah/gen/go/proto/asr/v1"
	pb "mynah/gen/go/proto/avatarengine/v1"
	"mynah/internal/llm"
	"mynah/internal/qwenrt"

	"github.com/pion/webrtc/v4"
)

// cloudVADMode reports whether this session hands turn-taking to the server
// (resolved at session create from the console's turn-mode selection).
func (s *Session) cloudVADMode() bool {
	return s.qwenMode() && (s.turnMode == "server_vad" || s.turnMode == "smart_turn")
}

// qwenServe runs the per-connection event loop (spawned by qwenConn right
// after a cloud-VAD dial, so typed injections work before any mic track).
func (s *Session) qwenServe(conn *qwenrt.Conn) {
	seq := uint64(0)
	var playhead time.Time
	err := conn.Serve(s.ctx, qwenServeEvents(s, &seq, &playhead))
	if err != nil && s.ctx.Err() == nil {
		log.Printf("[session %s] qwen serve ended: %v", s.ID, err)
	}
}

// listenMicCloud is the cloud-VAD counterpart of listenMic: mic Opus -> ASR
// worker (decode only) -> qwen AppendAudio. The response side lives in
// qwenServe. Runs for the lifetime of the mic track.
func (s *Session) listenMicCloud(track *webrtc.TrackRemote) {
	stream, err := s.asr.ASR.Session(s.ctx)
	if err != nil {
		log.Printf("[session %s] asr session: %v", s.ID, err)
		return
	}
	if err := stream.Send(&pbasr.ClientFrame{Msg: &pbasr.ClientFrame_Start{
		Start: &pbasr.SessionSpec{SessionId: s.ID, Codec: "opus", WantPcm: true},
	}}); err != nil {
		log.Printf("[session %s] asr start: %v", s.ID, err)
		return
	}

	conn, err := s.qwenConn()
	if err != nil {
		log.Printf("[session %s] qwen dial (cloud-vad): %v", s.ID, err)
		return
	}

	// Keepalive-by-refresh: the server closes any session that generates no
	// response for 180s, and there is no ping that resets that clock. Instead
	// of letting the deadline hit mid-question, proactively rotate the
	// connection at 150s idle (only while she isn't speaking): close it, and
	// the append loop below redials + reseeds history within the next mic
	// chunk (~100ms) — invisible to the user.
	go func() {
		tick := time.NewTicker(10 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-tick.C:
				s.qwen.mu.Lock()
				c := s.qwen.conn
				s.qwen.mu.Unlock()
				if c != nil && c.Alive() && c.RespIdle() > 150*time.Second && !s.speaking.Load() {
					log.Printf("[session %s] qwen idle %.0fs, rotating connection", s.ID, c.RespIdle().Seconds())
					c.Close()
				}
			}
		}
	}()

	// worker -> cored: decoded PCM chunks, forwarded to the server VAD. The
	// cloud side closes idle sessions (no response generated for 180s), so an
	// append failure redials on the spot — qwenConn hands back a fresh
	// connection with a new Serve loop. Server-side conversation context is
	// lost on redial (acceptable; local history still frames the session).
	go func() {
		for {
			sf, err := stream.Recv()
			if err != nil {
				if s.ctx.Err() == nil {
					log.Printf("[session %s] asr recv ended: %v", s.ID, err)
				}
				return
			}
			x, ok := sf.Msg.(*pbasr.ServerFrame_Pcm)
			if !ok {
				continue
			}
			if !conn.Alive() || conn.AppendAudio(x.Pcm.Pcm) != nil {
				if s.ctx.Err() != nil {
					return
				}
				nc, derr := s.qwenConn()
				if derr != nil {
					log.Printf("[session %s] qwen redial: %v", s.ID, derr)
					time.Sleep(2 * time.Second) // don't spin while offline
					continue
				}
				if nc != conn {
					log.Printf("[session %s] qwen reconnected (cloud-vad)", s.ID)
				}
				conn = nc
				_ = conn.AppendAudio(x.Pcm.Pcm)
			}
		}
	}()

	// browser -> worker: RTP Opus payloads verbatim (same as listenMic).
	seq := uint64(0)
	for {
		pkt, _, err := track.ReadRTP()
		if err != nil {
			if s.ctx.Err() == nil {
				log.Printf("[session %s] mic track ended: %v", s.ID, err)
			}
			_ = stream.Send(&pbasr.ClientFrame{Msg: &pbasr.ClientFrame_Close{Close: &pbasr.Close{}}})
			return
		}
		if len(pkt.Payload) == 0 {
			continue
		}
		if err := stream.Send(&pbasr.ClientFrame{Msg: &pbasr.ClientFrame_Audio{
			Audio: &pbasr.AudioPacket{Data: pkt.Payload, Seq: seq},
		}}); err != nil {
			log.Printf("[session %s] asr send: %v", s.ID, err)
			return
		}
		seq++
	}
}

// qwenServeEvents adapts the server-initiated conversation events onto the
// session: barge-in on speech_started, engine audio feed, subtitles, history.
// seq/playhead persist across responses (the engine sees one audio stream).
//
// Subtitles: the transcript deltas all arrive in a burst BEFORE the first
// audio delta (probe-verified), so relaying them live would flash the whole
// reply ahead of speech. Instead they are sliced into punctuation-bounded
// sentences and scheduled along the playback timeline at an estimated
// chars/sec rate (measured per reply, EMA across the session). Every timer
// runs under a per-reply context so a barge-in or a newer reply cancels the
// stale "ing"/"end" emissions.
func qwenServeEvents(s *Session, seq *uint64, playhead *time.Time) qwenrt.ServeEvents {
	var userText string

	// Subtitle pacing state — all mutated on the single Serve goroutine.
	const initialCharRate = 5.0 // runes/sec incl punctuation (longanqian ≈5.2 measured)
	charRate := initialCharRate
	var (
		acc        []rune             // transcript runes not yet sliced into a sentence
		pending    []string           // sentences completed before playback started
		playStart  time.Time          // when this reply's first pcm hit the playhead
		charsSched int                // runes already scheduled (offsets the next sentence)
		totalChars int                // runes in this reply (for the rate measurement)
		emitted    bool               // at least one subtitle scheduled this reply
		replyCtx   context.Context    // cancelled on barge-in or when a newer reply starts
		replyStop  context.CancelFunc = func() {}
	)
	scheduleSent := func(sent string) {
		at := playStart.Add(time.Duration(float64(charsSched)/charRate*float64(time.Second)) + subtitleLag)
		s.scheduleDC(replyCtx, at, "ing", sent)
		charsSched += len([]rune(sent))
		emitted = true
	}
	flushAcc := func(force bool) {
		if len(acc) == 0 || (!force && len(acc) < 6) {
			return
		}
		sent := string(acc)
		acc = acc[:0]
		if strings.TrimSpace(sent) == "" {
			return
		}
		if playStart.IsZero() {
			pending = append(pending, sent)
			return
		}
		scheduleSent(sent)
	}

	return qwenrt.ServeEvents{
		OnSpeechStart: func() {
			// The user started talking. If she's mid-reply this is a barge-in:
			// stop playback AND cancel the cloud response (not automatic).
			if s.speaking.Load() {
				log.Printf("[turn %s] cloud barge-in (speech_started)", s.ID)
				replyStop() // pending subtitles must not outlive the reply
				s.Interrupt()
			}
			s.fsm.emit(EvUserSpeechStart, "")
		},
		OnUserText: func(text string) {
			userText = text
			log.Printf("[session %s] cloud transcript: %q", s.ID, text)
			s.dcEmit("user", text)
		},
		OnReplyStart: func() {
			s.turnStart.Store(time.Now().UnixNano())
			s.turnTTSSeen.Store(false)
			s.turnSpkSeen.Store(false)
			s.turns.Add(1)
			s.speaking.Store(true)
			s.fsm.to(StateThinking, EvAgentThinking, "")
			log.Printf("[turn %s] cloud turn start", s.ID)
			s.dcEmit("start", "")
			*playhead = time.Time{}
			replyStop()
			replyCtx, replyStop = context.WithCancel(s.ctx)
			acc, pending = acc[:0], pending[:0]
			playStart = time.Time{}
			charsSched, totalChars = 0, 0
			emitted = false
		},
		OnPCM: func(out []float32) {
			if s.turnTTSSeen.CompareAndSwap(false, true) {
				log.Printf("[turn %s] qwen first pcm +%dms", s.ID, s.sinceTurnMs())
				s.fsm.to(StateSpeaking, EvAgentSpeechStart, "")
			}
			if playhead.IsZero() {
				*playhead = time.Now()
				// Playback origin known: schedule everything sliced so far.
				playStart = *playhead
				for _, sent := range pending {
					scheduleSent(sent)
				}
				pending = pending[:0]
			}
			*playhead = playhead.Add(pcmDur(len(out)))
			buf := make([]byte, len(out)*4)
			for i, v := range out {
				binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
			}
			_ = s.send(&pb.ClientFrame{Msg: &pb.ClientFrame_Audio{
				Audio: &pb.AudioChunk{PcmF32Le_16K: buf, Seq: *seq},
			}})
			*seq++
		},
		OnReplyDelta: func(delta string) {
			for _, r := range delta {
				acc = append(acc, r)
				totalChars++
				switch r {
				case '。', '！', '？', '；', '：', '，', '.', '!', '?', ';', ',', '\n':
					flushAcc(false)
				}
			}
		},
		OnReplyText: func(text string) {
			flushAcc(true) // tail without a closing punctuation mark
			if !emitted && len(pending) == 0 && text != "" {
				// No deltas came (older event shape) — fall back to one shot.
				s.dcEmit("ing", text)
			}
		},
		OnError: func(err error) {
			log.Printf("[session %s] qwen server error (continuing): %v", s.ID, err)
		},
		OnReplyDone: func(reply string) {
			// Do NOT clear speaking here: the cloud stream completes seconds
			// before the engine finishes PLAYING it, and speaking gates
			// barge-in — clearing early would make her uninterruptible for the
			// tail of every reply. recvLoop's engine SpeechEnd clears it when
			// playback actually ends; a zero-audio reply clears it now.
			if playhead.IsZero() {
				s.speaking.Store(false)
			}
			if reply != "" {
				s.histMu.Lock()
				u := userText
				if u == "" {
					u = "(语音)"
				}
				s.history = append(s.history, llm.Message{Role: "user", Content: u},
					llm.Message{Role: "assistant", Content: reply})
				if len(s.history) > 20 {
					s.history = append([]llm.Message(nil), s.history[len(s.history)-20:]...)
				}
				s.histMu.Unlock()
				log.Printf("[turn %s] cloud reply (%d chars) done +%dms",
					s.ID, len([]rune(reply)), s.sinceTurnMs())
			}
			userText = ""
			if playhead.IsZero() {
				s.dcEmit("end", "")
			} else {
				// Measure this reply's true speech rate for the next one.
				if d := playhead.Sub(playStart); totalChars > 0 && d > time.Second {
					measured := float64(totalChars) / d.Seconds()
					charRate = 0.5*charRate + 0.5*measured
				}
				s.scheduleDC(replyCtx, playhead.Add(subtitleLag), "end", "")
			}
		},
	}
}
