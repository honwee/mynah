package pipecatrt

import (
	"context"
	"encoding/binary"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"mynah/internal/qwenrt"
)

func TestWireRoundTrip(t *testing.T) {
	a := Audio{ID: 7, Name: "InputAudioRawFrame#7", PCM: []byte{1, 2, 3, 4}, SampleRate: 16000, NumChannels: 1}
	d, err := Decode(EncodeAudio(a))
	if err != nil || d.Kind != KindAudio || d.Audio.SampleRate != 16000 || string(d.Audio.PCM) != "\x01\x02\x03\x04" || d.Audio.ID != 7 {
		t.Fatalf("audio: %+v %v", d, err)
	}
	d, err = Decode(EncodeText(KindTranscription, Text{ID: 1, Text: "你好"}))
	if err != nil || d.Kind != KindTranscription || d.Text.Text != "你好" {
		t.Fatalf("transcription: %+v %v", d, err)
	}
	d, err = Decode(EncodeInterruption(3))
	if err != nil || d.Kind != KindInterruption || !d.Interrupt {
		t.Fatalf("interruption: %+v %v", d, err)
	}
}

func TestResampler24kTo16k(t *testing.T) {
	var r resampler
	// 1 s of 24 kHz sine in two chunks -> ~16000 samples total, continuous
	n := 24000
	pcm := make([]byte, n*2)
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(int16(10000*math.Sin(2*math.Pi*440*float64(i)/24000))))
	}
	out := append(r.to16kF32(pcm[:n], 24000, 1), r.to16kF32(pcm[n:], 24000, 1)...)
	if len(out) < 15990 || len(out) > 16010 {
		t.Fatalf("got %d samples", len(out))
	}
	// no discontinuity at the chunk seam: neighbouring samples differ by less than the sine slope allows
	maxStep := 0.0
	for i := 1; i < len(out); i++ {
		if d := math.Abs(float64(out[i] - out[i-1])); d > maxStep {
			maxStep = d
		}
	}
	if maxStep > 0.06 { // 440 Hz at 16 kHz: max slope ~ 2*pi*440/16000*0.305 ≈ 0.053
		t.Fatalf("seam discontinuity: max step %.3f", maxStep)
	}
}

// fakePipecat is a WebsocketServerTransport+ProtobufFrameSerializer bot: on
// any audio it sends an interruption, a transcription, two text deltas and
// 24 kHz TTS audio, then goes quiet (reply end by gap).
func fakePipecat(t *testing.T) (*httptest.Server, *sync.WaitGroup, *[]string) {
	var up websocket.Upgrader
	var wg sync.WaitGroup
	got := []string{}
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		wg.Add(1)
		defer wg.Done()
		replied := false
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				return
			}
			d, err := Decode(msg)
			if err != nil {
				t.Errorf("bad client frame: %v", err)
				return
			}
			mu.Lock()
			switch d.Kind {
			case KindAudio:
				got = append(got, "audio")
			case KindTranscription:
				got = append(got, "transcription:"+d.Text.Text)
			case KindInterruption:
				got = append(got, "interruption")
			}
			mu.Unlock()
			if d.Kind == KindAudio && !replied {
				replied = true
				_ = ws.WriteMessage(websocket.BinaryMessage, EncodeInterruption(1))
				_ = ws.WriteMessage(websocket.BinaryMessage, EncodeText(KindTranscription, Text{Text: "我回来啦"}))
				_ = ws.WriteMessage(websocket.BinaryMessage, EncodeText(KindText, Text{Text: "欢迎"}))
				_ = ws.WriteMessage(websocket.BinaryMessage, EncodeText(KindText, Text{Text: "回家。"}))
				pcm := make([]byte, 2400*2) // 100 ms @ 24 kHz
				for i := 0; i < 2400; i++ {
					binary.LittleEndian.PutUint16(pcm[i*2:], uint16(int16(8000*math.Sin(2*math.Pi*300*float64(i)/24000))))
				}
				for k := 0; k < 5; k++ {
					_ = ws.WriteMessage(websocket.BinaryMessage, EncodeAudio(Audio{PCM: pcm, SampleRate: 24000, NumChannels: 1}))
				}
			}
		}
	}))
	return srv, &wg, &got
}

func TestServeMapsPipecatFramesOntoEvents(t *testing.T) {
	srv, _, got := fakePipecat(t)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := Dial(ctx, Config{URL: "ws" + strings.TrimPrefix(srv.URL, "http"), ReplyGap: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	events := []string{}
	pcmN := 0
	add := func(s string) { mu.Lock(); events = append(events, s); mu.Unlock() }
	go func() {
		_ = c.Serve(ctx, qwenrt.ServeEvents{
			OnSpeechStart: func() { add("speech_start") },
			OnUserText:    func(s string) { add("user:" + s) },
			OnReplyStart:  func() { add("reply_start") },
			OnReplyDelta:  func(s string) { add("delta:" + s) },
			OnPCM:         func(p []float32) { mu.Lock(); pcmN += len(p); mu.Unlock() },
			OnReplyDone:   func(s string) { add("done:" + s) },
		})
	}()
	// 20 ms of mic audio
	mic := make([]byte, 320*4)
	if err := c.AppendAudio(mic); err != nil {
		t.Fatal(err)
	}
	_ = c.InjectText("你好")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		doneSeen := len(events) > 0 && strings.HasPrefix(events[len(events)-1], "done:")
		mu.Unlock()
		if doneSeen {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	c.Cancel()
	time.Sleep(50 * time.Millisecond)
	c.Close()
	mu.Lock()
	defer mu.Unlock()
	want := []string{"speech_start", "user:我回来啦", "delta:欢迎", "delta:回家。", "reply_start", "done:欢迎回家。"}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("events=%v", events)
	}
	if pcmN < 7900 || pcmN > 8100 { // 500 ms @ 16 kHz
		t.Fatalf("pcm samples %d", pcmN)
	}
	if strings.Join(*got, ",") != "audio,transcription:你好,interruption" {
		t.Fatalf("bot got %v", *got)
	}
}
