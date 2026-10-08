package mediaenc

import (
	"sync"
	"time"

	"github.com/pion/webrtc/v4/pkg/media"
)

const opusFrameDur = 20 * time.Millisecond

// AudioEncoder paces pre-encoded Opus packets (fed via WriteOpus, produced by
// the worker's PyAV libopus) out to a pion audio track at 20ms on absolute
// deadlines anchored to the shared Gate origin — same t0 as video, so the two
// stay lip-synced. On underrun it emits a captured silence packet so the audio
// RTP clock keeps advancing in lockstep with video.
type AudioEncoder struct {
	track   sampleWriter
	gate    *Gate
	samples chan []byte
	stop    chan struct{}
	once    sync.Once

	silMu   sync.Mutex
	silence []byte
}

// NewAudioEncoder starts the Opus pacing goroutine. Packets arrive already
// encoded (worker-side libopus), so there is no encoder subprocess. silence,
// if non-nil, is a baked 20ms Opus silence packet: the pacer then starts
// immediately (idle-local mode — the worker sends no audio while idle) instead
// of waiting for the first worker packet.
func NewAudioEncoder(track sampleWriter, logTag string, gate *Gate, silence []byte) (*AudioEncoder, error) {
	e := &AudioEncoder{
		track: track, gate: gate,
		samples: make(chan []byte, 400), stop: make(chan struct{}),
	}
	if len(silence) > 0 {
		e.silence = append([]byte(nil), silence...)
	}
	go e.paceLoop()
	return e, nil
}

// WriteOpus enqueues one Opus packet for pacing.
func (e *AudioEncoder) WriteOpus(pkt []byte) error {
	b := append([]byte(nil), pkt...) // copy: the gRPC message buffer is reused
	e.setSilence(b)
	select {
	case e.samples <- b:
	case <-e.stop:
	}
	return nil
}

func (e *AudioEncoder) getSilence() []byte {
	e.silMu.Lock()
	defer e.silMu.Unlock()
	return e.silence
}

func (e *AudioEncoder) setSilence(p []byte) {
	e.silMu.Lock()
	defer e.silMu.Unlock()
	// Capture a tiny (silence) packet — the session opens idle, so the first
	// packets are silence; keep the smallest seen as the underrun-fill frame.
	if len(p) > 0 && (e.silence == nil || len(p) < len(e.silence)) {
		e.silence = append([]byte(nil), p...)
	}
}

func (e *AudioEncoder) sleepUntil(t time.Time) bool {
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

func (e *AudioEncoder) paceLoop() {
	dur := opusFrameDur
	first := e.getSilence()
	if first == nil {
		// no baked silence: wait for the first worker packet (legacy mode)
		select {
		case f, ok := <-e.samples:
			if !ok {
				return
			}
			first = f
		case <-e.stop:
			return
		}
	}
	t0 := e.gate.Arrive("audio")
	_ = e.track.WriteSample(media.Sample{Data: first, Duration: dur})
	for m := 1; ; m++ {
		if !e.sleepUntil(t0.Add(time.Duration(m) * dur)) {
			return
		}
		select {
		case d, ok := <-e.samples:
			if !ok {
				return
			}
			_ = e.track.WriteSample(media.Sample{Data: d, Duration: dur})
		default:
			if sil := e.getSilence(); sil != nil {
				_ = e.track.WriteSample(media.Sample{Data: sil, Duration: dur})
			}
		}
	}
}

// Flush drops all queued packets (barge-in); the pacer fills with silence.
func (e *AudioEncoder) Flush() {
	for {
		select {
		case <-e.samples:
		default:
			return
		}
	}
}

// Close stops the pacing goroutine.
func (e *AudioEncoder) Close() {
	e.once.Do(func() { close(e.stop) })
}
