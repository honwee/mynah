// Package mediaenc paces media samples out to WebRTC tracks on a Gate-shared
// clock (see gate.go) so the audio + video tracks stay lip-synced.
//
// Both media are encoded on the worker side (VP8 / Opus via PyAV) and arrive
// already-compressed, so cored's media path is a pure forwarder/pacer with NO
// ffmpeg. With an IdleLoop configured (idle-local mode), cored replays a baked
// VP8 idle animation itself during silence — the worker emits nothing while
// idle, so an idle session costs zero GPU and zero gRPC traffic.
package mediaenc

import (
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4/pkg/media"
)

// sampleWriter is satisfied by *webrtc.TrackLocalStaticSample.
type sampleWriter interface {
	WriteSample(media.Sample) error
}

// VideoEncoder paces pre-encoded VP8 frames (fed via WriteFrame) out to a pion
// video track at fps, on absolute deadlines anchored to the shared Gate origin.
// With idle != nil it runs a two-state machine: replay the local idle loop
// until worker speech frames arrive, forward those until the speech-end
// sentinel, then cut back to the idle loop at a keyframe.
type VideoEncoder struct {
	fps     int
	track   sampleWriter
	gate    *Gate
	tag     string
	codec   string // "vp8" or "h264" — selects keyframe detection
	idle    *IdleLoop
	samples chan []byte // worker frames; nil entry = speech-end sentinel
	stop    chan struct{}
	once    sync.Once
	kfGate  atomic.Bool // drop speech P-frames until a keyframe (set by Flush too)
	kfReq   atomic.Bool // viewer asked for a keyframe (PLI): idle re-enters at one

	// One-shot action clip (动作编排) queued by PlayAction; the idle pacer picks
	// it up on its next idle tick, plays it through once, then re-enters the
	// idle loop at a keyframe. Depth-1 queue: a newer request replaces an
	// unstarted one. Idle-local mode only.
	actMu      sync.Mutex
	pendingAct *IdleLoop

	inN, inBytes, outN atomic.Int64
}

// NewVideoEncoder starts the pacing goroutine. idle may be nil (worker
// streams continuously, including its own idle frames). codec is "vp8" or
// "h264" and must match both the track and the worker's encoder.
func NewVideoEncoder(track sampleWriter, fps int, logTag string, gate *Gate, idle *IdleLoop, codec string) (*VideoEncoder, error) {
	e := &VideoEncoder{
		fps: fps, track: track, gate: gate, tag: logTag, idle: idle, codec: codec,
		samples: make(chan []byte, 300), stop: make(chan struct{}),
	}
	go e.paceLoop()
	go e.statLoop()
	return e, nil
}

// WriteFrame enqueues one VP8 frame (a complete encoded frame) for pacing.
func (e *VideoEncoder) WriteFrame(vp8 []byte) error {
	if len(vp8) == 0 {
		return nil // nil/empty is reserved for the speech-end sentinel
	}
	e.inN.Add(1)
	e.inBytes.Add(int64(len(vp8)))
	b := append([]byte(nil), vp8...) // copy: the gRPC message buffer is reused
	select {
	case e.samples <- b:
	case <-e.stop:
	}
	return nil
}

// SpeechEnd enqueues the in-band end-of-turn sentinel: frames queued before it
// still play out, then the pacer cuts back to the local idle loop.
func (e *VideoEncoder) SpeechEnd() {
	if e.idle == nil {
		return
	}
	select {
	case e.samples <- nil:
	case <-e.stop:
	}
}

// PlayAction queues a baked one-shot action clip (same container/codec as the
// idle loop; frame 0 keyframe). The pacer starts it at the next idle tick and
// returns to the idle loop when it ends. Speech takes priority: worker frames
// interrupt the action mid-clip, and an action queued while speaking starts
// after the turn ends. Only valid in idle-local mode.
func (e *VideoEncoder) PlayAction(clip *IdleLoop) error {
	if e.idle == nil {
		return errors.New("idle-local mode is off")
	}
	if clip.Codec != e.codec {
		return errors.New("action codec " + clip.Codec + " != track codec " + e.codec)
	}
	e.actMu.Lock()
	e.pendingAct = clip
	e.actMu.Unlock()
	return nil
}

// takeAction pops the pending action clip, if any.
func (e *VideoEncoder) takeAction() *IdleLoop {
	e.actMu.Lock()
	defer e.actMu.Unlock()
	a := e.pendingAct
	e.pendingAct = nil
	return a
}

func (e *VideoEncoder) statLoop() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-e.stop:
			return
		case <-t.C:
			in := e.inN.Swap(0)
			ib := e.inBytes.Swap(0)
			out := e.outN.Swap(0)
			if in > 0 || out > 0 {
				avg := int64(0)
				if in > 0 {
					avg = ib / in
				}
				log.Printf("[vid %s] in=%dfps avg=%dB out=%dfps qlen=%d",
					e.tag, in, avg, out, len(e.samples))
			}
		}
	}
}

// sleepUntil waits until t (absolute), returning false if stopped first.
func (e *VideoEncoder) sleepUntil(t time.Time) bool {
	d := time.Until(t)
	if d <= 0 {
		select {
		case <-e.stop:
			return false
		default:
			return true
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-e.stop:
		return false
	case <-timer.C:
		return true
	}
}

func isVP8Keyframe(fr []byte) bool { return len(fr) > 0 && fr[0]&0x01 == 0 }

// isH264Keyframe scans Annex-B NAL units for an IDR slice (type 5).
func isH264Keyframe(fr []byte) bool {
	for i := 0; i+3 < len(fr); i++ {
		if fr[i] == 0 && fr[i+1] == 0 && fr[i+2] == 1 {
			if fr[i+3]&0x1F == 5 {
				return true
			}
			i += 2
		}
	}
	return false
}

func (e *VideoEncoder) isKeyframe(fr []byte) bool {
	if e.codec == "h264" {
		return isH264Keyframe(fr)
	}
	return isVP8Keyframe(fr)
}

func (e *VideoEncoder) paceLoop() {
	if e.idle != nil {
		e.paceIdleLocal()
		return
	}
	// Worker-streams-everything mode (no local idle assets): wait for the
	// first frame, then forward on the grid; skip ticks on underrun.
	dur := time.Second / time.Duration(e.fps)
	var first []byte
	select {
	case f, ok := <-e.samples:
		if !ok {
			return
		}
		first = f
	case <-e.stop:
		return
	}
	t0 := e.gate.Arrive("video")
	_ = e.track.WriteSample(media.Sample{Data: first, Duration: dur})
	e.outN.Add(1)
	dropped := uint16(0)
	for n := 1; ; n++ {
		if !e.sleepUntil(t0.Add(time.Duration(n) * dur)) {
			return
		}
		select {
		case d, ok := <-e.samples:
			if !ok {
				return
			}
			if d == nil {
				continue // stray sentinel
			}
			// A VP8 frame is an inter-frame delta: never re-send one (the
			// decoder would apply the delta twice and smear). On underrun we
			// skip ticks and advance the RTP clock via PrevDroppedPackets so
			// the track stays locked to the shared gate t0.
			_ = e.track.WriteSample(media.Sample{Data: d, Duration: dur, PrevDroppedPackets: dropped})
			dropped = 0
			e.outN.Add(1)
		default:
			if dropped < 65535 {
				dropped++
			}
		}
	}
}

// paceIdleLocal: idle-local mode. The track starts on the idle loop right away
// (no waiting on the worker), so Arrive() fires immediately and the audio
// pacer locks to the same t0.
func (e *VideoEncoder) paceIdleLocal() {
	dur := time.Second / time.Duration(e.fps)
	t0 := e.gate.Arrive("video")
	speaking := false
	e.kfGate.Store(true) // drop speech P-frames until a keyframe restores references
	cur := e.idle.NewCursor()
	dropped := uint16(0)
	// One-shot action playback state: act != nil while a clip is playing,
	// actPos walks it linearly (frame 0 is a keyframe; every frame after is a
	// delta on the clip's own reference chain, so it can't be entered midway
	// or resumed after speech — abort and restart instead).
	var act *IdleLoop
	actPos := 0

	for n := 0; ; n++ {
		if n > 0 && !e.sleepUntil(t0.Add(time.Duration(n)*dur)) {
			return
		}
		var out []byte
	drain:
		// Take at most one playable worker frame per tick; sentinel and
		// keyframe-gated frames are consumed without occupying the tick.
		for {
			select {
			case d, ok := <-e.samples:
				if !ok {
					return
				}
				if d == nil { // speech turn over -> cut to idle at a keyframe
					if speaking {
						speaking = false
						e.kfGate.Store(true)
						cur.Reenter()
					}
					continue
				}
				if e.kfGate.Load() && !e.isKeyframe(d) {
					continue // orphaned delta (pre-keyframe / post-flush)
				}
				e.kfGate.Store(false)
				speaking = true
				out = d
			default:
			}
			break drain
		}

		if out != nil && act != nil {
			act, actPos = nil, 0 // speech preempts the action clip mid-play
		}

		switch {
		case out != nil: // worker speech frame
			_ = e.track.WriteSample(media.Sample{Data: out, Duration: dur, PrevDroppedPackets: dropped})
			dropped = 0
			e.outN.Add(1)
		case speaking:
			// mid-speech underrun: never splice idle frames into the speech
			// reference chain — skip the tick, advance the RTP clock
			if dropped < 65535 {
				dropped++
			}
		default: // idle: an action clip if one is queued/playing, else the baked loop
			if e.kfReq.Swap(false) {
				// viewer lost packets: restart its references at a keyframe now
				act, actPos = nil, 0
				cur.Reenter()
			}
			if act == nil {
				if a := e.takeAction(); a != nil {
					act, actPos = a, 0
				}
			}
			var fr []byte
			if act != nil {
				fr = act.Frames[actPos]
				actPos++
				if actPos >= len(act.Frames) { // clip done -> idle at a keyframe
					act, actPos = nil, 0
					cur.Reenter()
				}
			} else {
				fr = cur.Next()
			}
			_ = e.track.WriteSample(media.Sample{Data: fr, Duration: dur, PrevDroppedPackets: dropped})
			dropped = 0
			e.outN.Add(1)
		}
	}
}

// RequestKeyframe answers a viewer PLI on the replay path: the idle pacer
// re-enters the baked loop at the next keyframe instead of letting the
// viewer's broken reference chain run until the loop happens to wrap. Speech
// frames come from the worker, which gets its own keyframe request.
func (e *VideoEncoder) RequestKeyframe() { e.kfReq.Store(true) }

// Flush drops all queued frames (barge-in) and arms the keyframe gate so
// old-turn P-frames still in flight from the worker are dropped too — the next
// played speech frame is the keyframe the worker forces per turn.
func (e *VideoEncoder) Flush() {
	e.kfGate.Store(true)
	for {
		select {
		case <-e.samples:
		default:
			return
		}
	}
}

// Close stops the pacing goroutine.
func (e *VideoEncoder) Close() {
	e.once.Do(func() { close(e.stop) })
}
