// Package qwenrt is a minimal client for the Qwen-Audio realtime
// speech-to-speech API (Alibaba Model Studio, OpenAI-Realtime-style WebSocket
// events). cored uses it in push-to-talk mode as an alternative "brain": the
// ASR worker still segments mic audio and the turn orchestrator still decides
// when a turn is committed, but the committed utterance AUDIO (not the
// transcript) goes to the model, which answers with 24k PCM speech + text in
// one shot — replacing the LLM+TTS sentence pipeline. The reply audio is
// resampled to 16k mono f32, the exact format the AvatarEngine AudioChunk
// contract expects, so every avatar backend (wav2lipLS / FlashHead) works
// unchanged.
//
// One Conn per session; the server keeps the conversation context (up to 50
// turns) for the lifetime of the WebSocket, so reconnecting loses history.
package qwenrt

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"encoding/json"

	"github.com/gorilla/websocket"
)

// Config identifies the endpoint and session parameters. Instructions is read
// at dial time so a persona edit in the config center applies to the next
// session without a restart.
type Config struct {
	URL          string // wss://<workspace>.<region>.maas.aliyuncs.com/api-ws/v1/realtime
	APIKey       string
	Model        string // e.g. qwen-audio-3.0-realtime-flash
	Voice        string // system voice (longanqian...) or a voice-clone id
	Instructions func() string

	// TurnMode selects who detects end-of-turn. ""/"manual" = push-to-talk
	// (cored's local ASR + turn stack commits whole utterances via Turn).
	// "server_vad" (acoustic) / "smart_turn" (acoustic+semantic) hand
	// segmentation to the server: the client streams mic audio continuously
	// (AppendAudio) and consumes server-initiated responses (ServeVAD).
	// turn_detection is fixed per WebSocket — switching modes = redial.
	TurnMode string
	// server_vad tuning; zero = server defaults (threshold 0.5, silence 800ms).
	// smart_turn ignores both (the model decides semantically).
	VadThreshold float64
	VadSilenceMS int
}

// CloudVAD reports whether cfg delegates turn detection to the server.
func (c Config) CloudVAD() bool {
	return c.TurnMode == "server_vad" || c.TurnMode == "smart_turn"
}

// event is the subset of server events the turn loop consumes.
type event struct {
	Type       string `json:"type"`
	Delta      string `json:"delta"`
	Transcript string `json:"transcript"`
	ResponseID string `json:"response_id"`
	Response   struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"response"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Conn is one live realtime session (one WebSocket = one conversation).
type Conn struct {
	ws     *websocket.Conn
	wmu    sync.Mutex // serialize writes (turn goroutine + Cancel)
	events chan event
	dead   chan struct{}
	once   sync.Once

	// Cloud-VAD serve state: the id of the server-initiated response currently
	// streaming, and the id whose remaining deltas must be dropped after a
	// local barge-in (Cancel is async — the server keeps streaming until its
	// response.done lands, and that stale audio must not reach the engine).
	smu     sync.Mutex
	curID   string
	dropID  string
	serving bool // a Serve loop owns this connection (cloud-VAD mode)

	// AppendAudio accumulator: mic chunks arrive per 20ms Opus packet; batch
	// to ~100ms per WS message (the documented sweet spot).
	amu  sync.Mutex
	abuf []byte

	// Response-idle tracking: the server closes a session that generates no
	// response for 180s (response_idle_timeout). born + lastResp let the
	// session proactively refresh the connection before that deadline.
	born     time.Time
	lastResp atomic.Int64 // unix nanos of the latest response.created
}

// RespIdle reports how long the connection has gone without the server
// generating a response (from dial if none yet).
func (c *Conn) RespIdle() time.Duration {
	t := c.born
	if n := c.lastResp.Load(); n > 0 {
		t = time.Unix(0, n)
	}
	return time.Since(t)
}

// Dial connects and sends the initial session.update. Default is push-to-talk
// (no server VAD — cored's own ASR/turn stack owns segmentation and barge-in);
// with cfg.TurnMode server_vad/smart_turn the server segments and initiates
// responses itself (drive the connection with AppendAudio + ServeVAD).
func Dial(ctx context.Context, cfg Config) (*Conn, error) {
	voice := cfg.Voice
	if voice == "" {
		voice = "longanqian"
	}
	d := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	hdr := http.Header{"Authorization": {"Bearer " + cfg.APIKey}}
	ws, _, err := d.DialContext(ctx, cfg.URL+"?model="+cfg.Model, hdr)
	if err != nil {
		return nil, fmt.Errorf("qwen realtime dial: %w", err)
	}
	c := &Conn{ws: ws, events: make(chan event, 512), dead: make(chan struct{}), born: time.Now()}
	var turnDet any // nil = manual push-to-talk
	switch cfg.TurnMode {
	case "server_vad":
		td := map[string]any{"type": "server_vad"}
		if cfg.VadThreshold > 0 {
			td["threshold"] = cfg.VadThreshold
		}
		if cfg.VadSilenceMS > 0 {
			td["silence_duration_ms"] = cfg.VadSilenceMS
		}
		turnDet = td
	case "smart_turn":
		turnDet = map[string]any{"type": "smart_turn"}
	}
	sess := map[string]any{
		"modalities":     []string{"text", "audio"},
		"voice":          voice,
		"turn_detection": turnDet,
	}
	if cfg.Instructions != nil {
		if instr := cfg.Instructions(); instr != "" {
			sess["instructions"] = instr
		}
	}
	if err := c.send(map[string]any{"type": "session.update", "session": sess}); err != nil {
		ws.Close()
		return nil, err
	}
	// The server silently drops items/responses submitted before the session
	// config lands — wait for session.updated (probe-verified) before use.
	ws.SetReadDeadline(time.Now().Add(15 * time.Second))
	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			ws.Close()
			return nil, fmt.Errorf("qwen realtime handshake: %w", err)
		}
		var ev event
		if json.Unmarshal(data, &ev) != nil {
			continue
		}
		if ev.Type == "session.updated" {
			break
		}
		if ev.Type == "error" {
			ws.Close()
			return nil, fmt.Errorf("qwen realtime handshake: %s (%s)", ev.Error.Message, ev.Error.Code)
		}
	}
	ws.SetReadDeadline(time.Time{})
	go c.readLoop()
	return c, nil
}

func (c *Conn) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.ws.WriteMessage(websocket.TextMessage, b)
}

func (c *Conn) readLoop() {
	defer c.once.Do(func() { close(c.dead) })
	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			return
		}
		var ev event
		if json.Unmarshal(data, &ev) != nil {
			continue
		}
		if ev.Type == "response.created" {
			c.lastResp.Store(time.Now().UnixNano())
		}
		select {
		case c.events <- ev:
		default: // idle overflow (post-cancel stragglers); turns drain first
		}
	}
}

// Alive reports whether the WebSocket is still up.
func (c *Conn) Alive() bool {
	select {
	case <-c.dead:
		return false
	default:
		return true
	}
}

// Cancel aborts the in-flight response (barge-in). Safe from any goroutine.
// In cloud-VAD mode it also marks the current server response's remaining
// deltas for dropping — cancellation is async and the server keeps streaming
// until its response.done lands. serving tracks whether a Serve loop has ever
// run: with one, curID=="" means "no active response" and the wire message is
// skipped (the server errors on cancelling nothing); push-to-talk mode has no
// response tracking, so it always sends.
func (c *Conn) Cancel() {
	c.smu.Lock()
	if c.serving && c.curID == "" {
		c.smu.Unlock()
		return
	}
	if c.curID != "" {
		c.dropID = c.curID
	}
	c.smu.Unlock()
	_ = c.send(map[string]any{"type": "response.cancel"})
}

// Close tears down the WebSocket (the server drops the conversation context).
func (c *Conn) Close() { _ = c.ws.Close() }

// Preview auditions a voice: dial a throwaway session pinned to that voice
// (voice only binds on the first session.update — an existing conn can never
// switch), instruct the model to read the sample line verbatim, and collect
// the reply as 16k mono f32 PCM. The connection is closed before returning.
func Preview(ctx context.Context, cfg Config, text string) ([]float32, error) {
	cfg.TurnMode = "" // push-to-talk: we drive the single turn ourselves
	// 聊天腔而非念稿腔：realtime 没有语速参数，语速跟着模型对「说话情境」的
	// 判断走。纯朗读指令会触发播音腔（比实际对话偏慢/偏正式），试听就会和
	// 数字人实际听感对不上，所以强调日常聊天的语气语速。
	cfg.Instructions = func() string {
		return "你是一个语音试听工具。用户发来什么文字，你就用日常聊天的自然语气和语速把它说出来，" +
			"就像在和朋友随口说话，不要念稿腔、不要播音腔。内容一字不差，不要回应、不要添加或省略任何字。"
	}
	conn, err := Dial(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	var pcm []float32
	_, err = conn.Turn(ctx, text, nil, func(out []float32) {
		pcm = append(pcm, out...)
	}, nil)
	if err != nil && len(pcm) == 0 {
		return nil, err
	}
	return pcm, nil
}

// AppendAudio streams mic audio into the server-side VAD (cloud-VAD modes
// only). pcmF32 is f32le mono 16k; batched to ~100ms per WS message.
func (c *Conn) AppendAudio(pcmF32 []byte) error {
	const batch = 3200 // 100ms of 16k s16le
	c.amu.Lock()
	c.abuf = append(c.abuf, f32leToS16le(pcmF32)...)
	var flush []byte
	if len(c.abuf) >= batch {
		flush = c.abuf
		c.abuf = nil
	}
	c.amu.Unlock()
	if flush == nil {
		return nil
	}
	return c.send(map[string]any{
		"type":  "input_audio_buffer.append",
		"audio": base64.StdEncoding.EncodeToString(flush),
	})
}

// ServeEvents are the callbacks a cloud-VAD session hooks. All fire on the
// single Serve goroutine (no internal concurrency to guard against).
type ServeEvents struct {
	OnSpeechStart func()          // server VAD: user started talking (barge-in hook)
	OnUserText    func(string)    // input transcription of the user's speech
	OnPCM         func([]float32) // reply speech, resampled to 16k f32 (AudioChunk-ready)
	OnReplyStart  func()          // a server-initiated response began
	OnReplyDelta  func(string)    // incremental reply transcript (word-sized pieces; the full text arrives in a burst BEFORE the first audio delta)
	OnReplyText   func(string)    // full reply transcript (once, near stream end)
	OnReplyDone   func(string)    // response finished (or was cancelled); arg = transcript
	OnError       func(error)     // non-fatal server error (e.g. a cancel that raced a finished response); the loop continues
}

// Serve runs the cloud-VAD event loop until ctx ends or the connection dies.
// The server initiates responses on its own (turn_detection segments the
// AppendAudio stream); this loop routes their events, dropping deltas of a
// response cancelled by a local barge-in (see Cancel).
func (c *Conn) Serve(ctx context.Context, cb ServeEvents) error {
	c.smu.Lock()
	c.serving = true
	c.smu.Unlock()
	var rs *resampler
	transcript := ""
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.dead:
			return errors.New("qwen realtime connection lost")
		case ev := <-c.events:
			switch ev.Type {
			case "input_audio_buffer.speech_started":
				if cb.OnSpeechStart != nil {
					cb.OnSpeechStart()
				}
			case "conversation.item.input_audio_transcription.completed":
				if cb.OnUserText != nil && ev.Transcript != "" {
					cb.OnUserText(ev.Transcript)
				}
			case "response.created":
				c.smu.Lock()
				c.curID = ev.Response.ID
				c.smu.Unlock()
				rs = newResampler24to16()
				transcript = ""
				if cb.OnReplyStart != nil {
					cb.OnReplyStart()
				}
			case "response.audio.delta":
				if c.dropped(ev.ResponseID) || rs == nil {
					continue
				}
				raw, err := base64.StdEncoding.DecodeString(ev.Delta)
				if err != nil || len(raw) < 2 {
					continue
				}
				if out := rs.process(s16leToF32(raw)); len(out) > 0 && cb.OnPCM != nil {
					cb.OnPCM(out)
				}
			case "response.audio_transcript.delta":
				if c.dropped(ev.ResponseID) {
					continue
				}
				if cb.OnReplyDelta != nil && ev.Delta != "" {
					cb.OnReplyDelta(ev.Delta)
				}
			case "response.audio_transcript.done":
				if c.dropped(ev.ResponseID) {
					continue
				}
				transcript = ev.Transcript
				if cb.OnReplyText != nil {
					cb.OnReplyText(ev.Transcript)
				}
			case "response.done":
				c.smu.Lock()
				done := ev.Response.ID
				wasDropped := done != "" && done == c.dropID
				if done == "" || done == c.curID {
					c.curID = ""
				}
				if wasDropped {
					c.dropID = ""
				}
				c.smu.Unlock()
				rs = nil
				if !wasDropped && cb.OnReplyDone != nil {
					cb.OnReplyDone(transcript)
				}
			case "error":
				// Server errors here are usually benign races (a barge-in
				// cancel landing after response.done -> "no active response").
				// The connection itself is fine — report and keep serving;
				// c.dead covers real connection loss.
				if cb.OnError != nil {
					cb.OnError(fmt.Errorf("qwen realtime: %s (%s)", ev.Error.Message, ev.Error.Code))
				}
			}
		}
	}
}

// dropped reports whether a response-scoped event belongs to a response
// cancelled by a local barge-in. An untagged event (no response_id) is
// attributed to the currently-streaming response.
func (c *Conn) dropped(responseID string) bool {
	c.smu.Lock()
	defer c.smu.Unlock()
	if c.dropID == "" {
		return false
	}
	if responseID != "" {
		return responseID == c.dropID
	}
	return c.curID == c.dropID
}

// SeedItem injects one prior-history message into a fresh connection's
// conversation (redial context restore). role is "user" or "assistant"; the
// server demands input_text for user items and output_text for assistant
// items (probe-verified). Seed BEFORE any live turn — items are appended in
// send order.
func (c *Conn) SeedItem(role, text string) error {
	ctype := "input_text"
	if role == "assistant" {
		ctype = "output_text"
	}
	return c.send(map[string]any{
		"type": "conversation.item.create",
		"item": map[string]any{
			"type": "message", "role": role,
			"content": []map[string]any{{"type": ctype, "text": text}},
		},
	})
}

// readVerbatimInstr wraps a line in the "read this, don't answer it" framing
// used for scripted speech (channel greeting). session.instructions bind on
// the first session.update only, so switching the model from "answer the user"
// to "read this line" mid-session is only possible per-response.
func readVerbatimInstr(text string) string {
	return "把下面这句话用自然的口语语气一字不差地说出来，" +
		"不要添加、省略或回应任何内容：" + text
}

// SpeakVerbatim makes the model read text aloud in its own voice instead of
// answering — the cloud counterpart of Speak()'s local TTS, used for the
// channel greeting so a qwen-brain session never mixes in the local voice.
//
// Two probe-verified constraints shape this: (1) the server refuses
// response.create while the conversation holds no user message, so a minimal
// placeholder item is seeded first; (2) a per-response instructions override
// is honoured (session-level instructions are immutable after the first
// session.update). Cloud-VAD callers must run this on a connection whose Serve
// loop is live — the reply events flow through it exactly like any other
// server-initiated response.
func (c *Conn) SpeakVerbatim(text string) error {
	if err := c.SeedItem("user", greetSeedPlaceholder); err != nil {
		return err
	}
	return c.send(map[string]any{
		"type": "response.create",
		"response": map[string]any{
			"modalities":   []string{"audio", "text"},
			"instructions": readVerbatimInstr(text),
		},
	})
}

// greetSeedPlaceholder is the throwaway user item that unblocks the first
// response.create. It reads as a stage direction so the model doesn't treat it
// as a question if it ever surfaces in context.
const greetSeedPlaceholder = "（开场）"

// SeedKnowledge injects retrieved knowledge as a user item just before a turn.
// The realtime API has no per-turn system message and session.instructions are
// immutable after the first session.update, so a conversation item is the only
// carrier. Content is framed as reference material (see renderRAG) rather than
// as something the user said.
//
// Push-to-talk only: in cloud-VAD mode the server creates the response the
// instant speech stops — before the transcript is even delivered — so there is
// no window between "know the question" and "generation starts".
func (c *Conn) SeedKnowledge(text string) error {
	return c.SeedItem("user", text)
}

// InjectText submits a typed user message and asks for a response (cloud-VAD
// mode only: the running Serve loop consumes the response events, exactly as
// if the server had initiated it).
func (c *Conn) InjectText(text string) error {
	if err := c.send(map[string]any{
		"type": "conversation.item.create",
		"item": map[string]any{
			"type": "message", "role": "user",
			"content": []map[string]any{{"type": "input_text", "text": text}},
		},
	}); err != nil {
		return err
	}
	return c.send(map[string]any{
		"type":     "response.create",
		"response": map[string]any{"modalities": []string{"audio", "text"}},
	})
}

// Turn runs one push-to-talk exchange. pcmF32 is the user utterance as f32le
// mono 16k (the ASR worker's UtteranceAudio bytes); empty = inject text as an
// input_text item instead (typed chat, or voice without utterance audio).
// onPCM receives reply speech resampled to f32 mono 16k — AudioChunk-ready.
// onTranscript fires once with the full reply text. Returns the transcript.
func (c *Conn) Turn(ctx context.Context, text string, pcmF32 []byte,
	onPCM func([]float32), onTranscript func(string)) (string, error) {
	c.drainStale()

	if len(pcmF32) >= 8 {
		s16 := f32leToS16le(pcmF32)
		const chunk = 32000 // 1s of 16k s16 per append message
		for i := 0; i < len(s16); i += chunk {
			end := min(i+chunk, len(s16))
			if err := c.send(map[string]any{
				"type":  "input_audio_buffer.append",
				"audio": base64.StdEncoding.EncodeToString(s16[i:end]),
			}); err != nil {
				return "", err
			}
		}
		if err := c.send(map[string]any{"type": "input_audio_buffer.commit"}); err != nil {
			return "", err
		}
	} else {
		if err := c.send(map[string]any{
			"type": "conversation.item.create",
			"item": map[string]any{
				"type": "message", "role": "user",
				"content": []map[string]any{{"type": "input_text", "text": text}},
			},
		}); err != nil {
			return "", err
		}
	}
	if err := c.send(map[string]any{
		"type":     "response.create",
		"response": map[string]any{"modalities": []string{"audio", "text"}},
	}); err != nil {
		return "", err
	}
	return c.awaitResponse(ctx, onPCM, onTranscript)
}

// SpeakVerbatimTurn is SpeakVerbatim for push-to-talk sessions: it drives the
// scripted line AND collects the reply itself (no Serve loop exists in this
// mode). Same probe-verified shape — placeholder user item, then a
// response.create carrying the read-verbatim instructions override.
func (c *Conn) SpeakVerbatimTurn(ctx context.Context, text string,
	onPCM func([]float32), onTranscript func(string)) (string, error) {
	c.drainStale()
	if err := c.SeedItem("user", greetSeedPlaceholder); err != nil {
		return "", err
	}
	if err := c.send(map[string]any{
		"type": "response.create",
		"response": map[string]any{
			"modalities":   []string{"audio", "text"},
			"instructions": readVerbatimInstr(text),
		},
	}); err != nil {
		return "", err
	}
	return c.awaitResponse(ctx, onPCM, onTranscript)
}

// drainStale drops events a cancelled previous turn left behind.
func (c *Conn) drainStale() {
	for {
		select {
		case <-c.events:
			continue
		default:
		}
		return
	}
}

// awaitResponse consumes one response's events (audio + transcript) to
// completion, returning its transcript. Shared by Turn and SpeakVerbatimTurn.
func (c *Conn) awaitResponse(ctx context.Context,
	onPCM func([]float32), onTranscript func(string)) (string, error) {
	rs := newResampler24to16()
	myID := ""
	transcript := ""
	deadline := time.NewTimer(60 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return transcript, ctx.Err()
		case <-c.dead:
			return transcript, errors.New("qwen realtime connection lost")
		case <-deadline.C:
			return transcript, errors.New("qwen realtime turn timeout")
		case ev := <-c.events:
			// Until our own response.created arrives, every response-scoped
			// event is a straggler from the previous (cancelled) response —
			// its done/transcript/audio must not be mistaken for ours.
			switch ev.Type {
			case "response.created":
				myID = ev.Response.ID
			case "response.audio.delta":
				if myID == "" || (ev.ResponseID != "" && ev.ResponseID != myID) {
					continue
				}
				raw, err := base64.StdEncoding.DecodeString(ev.Delta)
				if err != nil || len(raw) < 2 {
					continue
				}
				if out := rs.process(s16leToF32(raw)); len(out) > 0 && onPCM != nil {
					onPCM(out)
				}
			case "response.audio_transcript.done":
				if myID == "" || (ev.ResponseID != "" && ev.ResponseID != myID) {
					continue
				}
				transcript = ev.Transcript
				if onTranscript != nil {
					onTranscript(ev.Transcript)
				}
			case "response.done":
				if myID == "" || (ev.Response.ID != "" && ev.Response.ID != myID) {
					continue
				}
				return transcript, nil
			case "error":
				return transcript, fmt.Errorf("qwen realtime: %s (%s)", ev.Error.Message, ev.Error.Code)
			}
		}
	}
}

func f32leToS16le(b []byte) []byte {
	n := len(b) / 4
	out := make([]byte, n*2)
	for i := 0; i < n; i++ {
		f := math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
		v := f * 32767
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		binary.LittleEndian.PutUint16(out[i*2:], uint16(int16(v)))
	}
	return out
}

func s16leToF32(b []byte) []float32 {
	n := len(b) / 2
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		out[i] = float32(int16(binary.LittleEndian.Uint16(b[i*2:]))) / 32767.0
	}
	return out
}

// resampler is a stateful 24k->16k downsampler: a windowed-sinc FIR low-pass
// (cutoff ~7kHz) removes content above the 16k Nyquist BEFORE the linear
// interpolation — without it, 8-12kHz sibilant energy aliases down into the
// 4-8kHz band the avatar engine's mel features read, smearing mouth shapes.
// Both stages carry state across chunk boundaries.
type resampler struct {
	ratio   float64
	pending []float32
	frac    float64
	fir     []float32 // low-pass taps
	hist    []float32 // last len(fir)-1 raw input samples
}

func newResampler24to16() *resampler {
	return &resampler{ratio: 24000.0 / 16000.0, fir: firLowpass(33, 7000.0/24000.0)}
}

// firLowpass builds a Hamming-windowed sinc low-pass, fc = cutoff/sampleRate,
// normalized to unity DC gain.
func firLowpass(taps int, fc float64) []float32 {
	h := make([]float32, taps)
	m := float64(taps - 1)
	var sum float64
	for i := range h {
		x := float64(i) - m/2
		s := 2 * fc
		if x != 0 {
			s = math.Sin(2*math.Pi*fc*x) / (math.Pi * x)
		}
		v := s * (0.54 - 0.46*math.Cos(2*math.Pi*float64(i)/m))
		h[i] = float32(v)
		sum += v
	}
	for i := range h {
		h[i] = float32(float64(h[i]) / sum)
	}
	return h
}

func (r *resampler) filter(in []float32) []float32 {
	n := len(r.fir) - 1
	buf := make([]float32, 0, len(r.hist)+len(in))
	buf = append(append(buf, r.hist...), in...)
	var out []float32
	for i := n; i < len(buf); i++ {
		var acc float32
		for j, c := range r.fir {
			acc += c * buf[i-j]
		}
		out = append(out, acc)
	}
	if len(buf) >= n {
		r.hist = append(r.hist[:0], buf[len(buf)-n:]...)
	} else {
		r.hist = buf
	}
	return out
}

func (r *resampler) process(in []float32) []float32 {
	if r.fir != nil {
		in = r.filter(in)
	}
	r.pending = append(r.pending, in...)
	var out []float32
	for {
		i := int(r.frac)
		if i+1 >= len(r.pending) {
			break
		}
		t := r.frac - float64(i)
		out = append(out, r.pending[i]*float32(1-t)+r.pending[i+1]*float32(t))
		r.frac += r.ratio
	}
	drop := int(r.frac)
	if drop > 0 {
		if drop > len(r.pending) {
			drop = len(r.pending)
		}
		r.pending = append([]float32(nil), r.pending[drop:]...)
		r.frac -= float64(drop)
	}
	return out
}
