// Probe: can a cloud-VAD session defer generation so cored gets a window to
// inject retrieved knowledge? Tests turn_detection.create_response=false —
// if honoured, the server still segments + transcribes but does NOT generate,
// letting us retrieve on the transcript and drive response.create ourselves
// (that is the only place RAG context can enter a cloud-VAD turn).
//
// Feeds a real WAV (16k mono) as mic audio so the server VAD has something to
// segment. Reports the event order and whether an unsolicited response.created
// appeared.
package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	url := flag.String("url", "", "realtime ws url")
	key := flag.String("key", os.Getenv("QWEN_RT_KEY"), "api key")
	model := flag.String("model", "qwen-audio-3.0-realtime-flash", "model")
	wav := flag.String("wav", "", "16k mono s16le WAV to feed as mic audio")
	createResp := flag.Bool("create-response", false, "turn_detection.create_response")
	flag.Parse()

	raw, err := os.ReadFile(*wav)
	if err != nil {
		log.Fatalf("read wav: %v", err)
	}
	if len(raw) < 44 {
		log.Fatalf("wav too short")
	}
	pcm := raw[44:] // assume canonical 44-byte header, 16k mono s16le
	log.Printf("mic audio: %d bytes (%.2fs @16k s16)", len(pcm), float64(len(pcm))/2/16000)

	d := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	hdr := http.Header{"Authorization": {"Bearer " + *key}}
	ws, _, err := d.Dial(*url+"?model="+*model, hdr)
	if err != nil {
		log.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	send := func(v any) {
		b, _ := json.Marshal(v)
		if err := ws.WriteMessage(websocket.TextMessage, b); err != nil {
			log.Fatalf("send: %v", err)
		}
	}

	send(map[string]any{"type": "session.update", "session": map[string]any{
		"modalities": []string{"text", "audio"},
		"voice":      "longanlingxi",
		"turn_detection": map[string]any{
			"type":            "smart_turn",
			"create_response": *createResp,
		},
		"instructions": "你是林夏，一个亲切的数字人助手。回答简短口语化。",
	}})

	type ev struct {
		Type       string `json:"type"`
		Transcript string `json:"transcript"`
		Response   struct {
			ID string `json:"id"`
		} `json:"response"`
		Session struct {
			TurnDetection map[string]any `json:"turn_detection"`
		} `json:"session"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	read := func(to time.Duration) (ev, []byte, error) {
		ws.SetReadDeadline(time.Now().Add(to))
		_, data, err := ws.ReadMessage()
		var e ev
		if err == nil {
			_ = json.Unmarshal(data, &e)
		}
		return e, data, err
	}

	for {
		e, rawmsg, err := read(15 * time.Second)
		if err != nil {
			log.Fatalf("handshake: %v", err)
		}
		if e.Type == "session.updated" {
			log.Printf("session.updated; server echoed turn_detection=%v", e.Session.TurnDetection)
			if cr, ok := e.Session.TurnDetection["create_response"]; ok {
				log.Printf(">>> server ACKED create_response=%v (requested %v)", cr, *createResp)
			} else {
				log.Printf(">>> server did NOT echo create_response — may be ignoring it; raw: %s", rawmsg)
			}
			break
		}
		if e.Type == "error" {
			log.Fatalf("handshake error: %s (%s)", e.Error.Message, e.Error.Code)
		}
	}

	// Stream the mic audio in 100ms chunks, like AppendAudio does.
	go func() {
		const chunk = 3200
		for i := 0; i < len(pcm); i += chunk {
			end := min(i+chunk, len(pcm))
			send(map[string]any{
				"type":  "input_audio_buffer.append",
				"audio": base64.StdEncoding.EncodeToString(pcm[i:end]),
			})
			time.Sleep(100 * time.Millisecond)
		}
		log.Printf("mic audio fully streamed")
	}()

	var (
		gotTranscript  string
		gotRespCreated bool
		tSpeechEnd     time.Time
	)
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		e, rawmsg, err := read(20 * time.Second)
		if err != nil {
			log.Printf("read ended: %v", err)
			break
		}
		switch e.Type {
		case "input_audio_buffer.speech_started":
			log.Printf("event: speech_started")
		case "input_audio_buffer.speech_stopped":
			tSpeechEnd = time.Now()
			log.Printf("event: speech_stopped")
		case "conversation.item.input_audio_transcription.completed":
			gotTranscript = e.Transcript
			log.Printf("event: TRANSCRIPT %q", e.Transcript)
		case "response.created":
			gotRespCreated = true
			d := ""
			if !tSpeechEnd.IsZero() {
				d = time.Since(tSpeechEnd).Round(time.Millisecond).String() + " after speech_stopped"
			}
			log.Printf("event: response.created id=%s %s", e.Response.ID, d)
		case "response.done":
			log.Printf("event: response.done")
			goto verdict
		case "error":
			log.Printf("event: ERROR %s (%s) raw=%s", e.Error.Message, e.Error.Code, rawmsg)
		}
		if gotTranscript != "" && !gotRespCreated && !tSpeechEnd.IsZero() &&
			time.Since(tSpeechEnd) > 6*time.Second {
			goto verdict
		}
	}

verdict:
	log.Printf("--- transcript=%q respCreated=%v ---", gotTranscript, gotRespCreated)
	if *createResp {
		log.Printf("(baseline run: response.created expected)")
		return
	}
	if gotTranscript != "" && !gotRespCreated {
		log.Printf("VERDICT: create_response=false HONOURED — cored can retrieve then drive response.create (RAG possible)")
	} else if gotRespCreated {
		log.Printf("VERDICT: create_response=false IGNORED — server generated anyway (no RAG window in cloud-VAD)")
	} else {
		log.Printf("VERDICT: inconclusive — no transcript arrived")
	}
}
