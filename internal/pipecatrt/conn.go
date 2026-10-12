package pipecatrt

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"mynah/internal/qwenrt"
)

// Config is one Pipecat brain endpoint: a bot serving Pipecat's
// WebsocketServerTransport with the ProtobufFrameSerializer.
type Config struct {
	URL string // ws://host:port (Pipecat WebsocketServerTransport)
	// ReplyGap is the silence after the last reply audio that ends a reply
	// (Pipecat sends no end-of-bot-speech frame over this transport).
	ReplyGap time.Duration
}

// Conn is one WebSocket to a Pipecat bot. It implements the same event
// surface as qwenrt.Conn (Serve/AppendAudio/Cancel/...) so a session can
// drive either brain through one code path.
type Conn struct {
	cfg  Config
	ws   *websocket.Conn
	wmu  sync.Mutex
	dead atomic.Bool
	id   atomic.Uint64

	lastAct atomic.Int64 // unix nanos of the last frame either way
	closed  chan struct{}
}

// Dial connects to the Pipecat bot.
func Dial(ctx context.Context, cfg Config) (*Conn, error) {
	if cfg.URL == "" {
		return nil, errors.New("pipecat: empty url")
	}
	if cfg.ReplyGap <= 0 {
		cfg.ReplyGap = 700 * time.Millisecond
	}
	d := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	ws, _, err := d.DialContext(ctx, cfg.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("pipecat dial %s: %w", cfg.URL, err)
	}
	c := &Conn{cfg: cfg, ws: ws, closed: make(chan struct{})}
	c.lastAct.Store(time.Now().UnixNano())
	return c, nil
}

func (c *Conn) Alive() bool { return !c.dead.Load() }

// RespIdle is the time since the last frame in either direction.
func (c *Conn) RespIdle() time.Duration {
	return time.Since(time.Unix(0, c.lastAct.Load()))
}

func (c *Conn) Close() {
	if c.dead.CompareAndSwap(false, true) {
		close(c.closed)
		_ = c.ws.Close()
	}
}

func (c *Conn) write(b []byte) error {
	if c.dead.Load() {
		return errors.New("pipecat: connection closed")
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := c.ws.WriteMessage(websocket.BinaryMessage, b); err != nil {
		c.Close()
		return err
	}
	c.lastAct.Store(time.Now().UnixNano())
	return nil
}

// AppendAudio pushes microphone PCM (16 kHz mono float32 LE, as the ASR worker
// hands it to cored) to the bot as InputAudioRawFrame (16-bit).
func (c *Conn) AppendAudio(pcmF32 []byte) error {
	n := len(pcmF32) / 4
	if n == 0 {
		return nil
	}
	s16 := make([]byte, n*2)
	for i := 0; i < n; i++ {
		f := math.Float32frombits(binary.LittleEndian.Uint32(pcmF32[i*4:]))
		v := int32(f * 32767)
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		binary.LittleEndian.PutUint16(s16[i*2:], uint16(int16(v)))
	}
	id := c.id.Add(1)
	return c.write(EncodeAudio(Audio{ID: id, Name: fmt.Sprintf("InputAudioRawFrame#%d", id),
		PCM: s16, SampleRate: 16000, NumChannels: 1}))
}

// Cancel asks the bot to interrupt its current response (Pipecat's
// InterruptionFrame coming in from the transport).
func (c *Conn) Cancel() {
	_ = c.write(EncodeInterruption(c.id.Add(1)))
}

// InjectText delivers typed visitor text as a TranscriptionFrame — the shape
// Pipecat's user aggregators treat as "the user said this", so the bot
// answers it like speech.
func (c *Conn) InjectText(text string) error {
	id := c.id.Add(1)
	return c.write(EncodeText(KindTranscription, Text{ID: id, Name: fmt.Sprintf("TranscriptionFrame#%d", id), Text: text}))
}

// SeedItem / SeedKnowledge are no-ops: conversation memory and knowledge live
// inside the Pipecat pipeline in this mode.
func (c *Conn) SeedItem(role, text string) error { return nil }
func (c *Conn) SeedKnowledge(text string) error  { return nil }

// SpeakVerbatim and the push-to-talk turn helpers are not part of Pipecat's
// transport contract; callers fall back to the local TTS pipeline for
// scripted lines (greetings, mynah_speak).
var ErrUnsupported = errors.New("pipecat brain: not supported over the transport (use the local TTS path)")

func (c *Conn) SpeakVerbatim(string) error { return ErrUnsupported }
func (c *Conn) SpeakVerbatimTurn(context.Context, string, func([]float32), func(string)) (string, error) {
	return "", ErrUnsupported
}
func (c *Conn) Turn(context.Context, string, []byte, func([]float32), func(string)) (string, error) {
	return "", ErrUnsupported
}

// Serve routes the bot's frames onto the qwenrt.ServeEvents callbacks:
//
//	audio          -> OnReplyStart (first after idle) + OnPCM (resampled to 16k f32)
//	text           -> OnReplyDelta
//	transcription  -> OnUserText
//	interruption   -> OnSpeechStart (barge-in: the bot's VAD heard the user)
//	reply end      -> OnReplyDone after ReplyGap of silence
func (c *Conn) Serve(ctx context.Context, cb qwenrt.ServeEvents) error {
	type st struct {
		inReply bool
		text    string
		last    time.Time
	}
	var mu sync.Mutex
	s := st{}
	done := make(chan struct{})
	defer close(done)

	// Reply-end watchdog: no audio for ReplyGap after some audio = reply done.
	go func() {
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				mu.Lock()
				if s.inReply && time.Since(s.last) > c.cfg.ReplyGap {
					s.inReply = false
					txt := s.text
					s.text = ""
					mu.Unlock()
					if cb.OnReplyText != nil {
						cb.OnReplyText(txt)
					}
					if cb.OnReplyDone != nil {
						cb.OnReplyDone(txt)
					}
					continue
				}
				mu.Unlock()
			}
		}
	}()

	go func() {
		select {
		case <-ctx.Done():
			c.Close()
		case <-c.closed:
		}
	}()

	var rs resampler
	for {
		typ, msg, err := c.ws.ReadMessage()
		if err != nil {
			c.Close()
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("pipecat read: %w", err)
		}
		c.lastAct.Store(time.Now().UnixNano())
		if typ != websocket.BinaryMessage {
			continue
		}
		d, err := Decode(msg)
		if err != nil {
			if cb.OnError != nil {
				cb.OnError(err)
			}
			continue
		}
		switch d.Kind {
		case KindAudio:
			a := d.Audio
			if a.SampleRate == 0 || a.NumChannels == 0 || len(a.PCM) == 0 {
				continue
			}
			mu.Lock()
			first := !s.inReply
			s.inReply = true
			s.last = time.Now()
			mu.Unlock()
			if first && cb.OnReplyStart != nil {
				cb.OnReplyStart()
			}
			if cb.OnPCM != nil {
				cb.OnPCM(rs.to16kF32(a.PCM, int(a.SampleRate), int(a.NumChannels)))
			}
		case KindText:
			mu.Lock()
			s.text += d.Text.Text
			mu.Unlock()
			if cb.OnReplyDelta != nil {
				cb.OnReplyDelta(d.Text.Text)
			}
		case KindTranscription:
			if cb.OnUserText != nil {
				cb.OnUserText(d.Text.Text)
			}
		case KindInterruption:
			mu.Lock()
			s.inReply = false
			s.text = ""
			mu.Unlock()
			if cb.OnSpeechStart != nil {
				cb.OnSpeechStart()
			}
		case KindMessage:
			// RTVI / app messages: not needed for lip-sync; log at debug level.
			log.Printf("[pipecat] message: %.120s", d.Data)
		}
	}
}

// resampler converts 16-bit PCM at any rate / channel count to 16 kHz mono
// float32 by linear interpolation, carrying the fractional position across
// calls so chunk boundaries do not click.
type resampler struct {
	pos  float64
	prev float32
	have bool
	rate int
}

func (r *resampler) to16kF32(pcm []byte, rate, ch int) []float32 {
	n := len(pcm) / 2 / ch
	mono := make([]float32, n)
	for i := 0; i < n; i++ {
		var acc int32
		for k := 0; k < ch; k++ {
			acc += int32(int16(binary.LittleEndian.Uint16(pcm[(i*ch+k)*2:])))
		}
		mono[i] = float32(acc) / float32(ch) / 32768
	}
	if rate == 16000 {
		return mono
	}
	if r.rate != rate {
		r.rate, r.pos, r.have = rate, 0, false
	}
	step := float64(rate) / 16000
	out := make([]float32, 0, int(float64(n)/step)+2)
	// source index space includes the carried previous sample at -1
	src := func(i int) float32 {
		if i < 0 {
			if r.have {
				return r.prev
			}
			return mono[0]
		}
		if i >= n {
			return mono[n-1]
		}
		return mono[i]
	}
	p := r.pos - 1 // relative to this chunk, previous sample sits at -1
	for p < float64(n-1) {
		i := int(math.Floor(p))
		f := float32(p - float64(i))
		out = append(out, src(i)*(1-f)+src(i+1)*f)
		p += step
	}
	r.pos = p - float64(n-1) // fractional carry into the next chunk
	r.prev = mono[n-1]
	r.have = true
	return out
}
