// Verify Session.Speak routes to the cloud brain in qwen mode: open a WebRTC
// session against a running cored, fire /human with type=echo, and confirm
// reply speech arrives. The routing itself is asserted from cored's log
// ("qwen speak" vs "tts first pcm") — run this next to `journalctl -f`.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/pion/webrtc/v4"
)

func main() {
	base := flag.String("base", "http://127.0.0.1:8020", "cored base url")
	text := flag.String("text", "你好呀，我是林夏，很高兴见到你。", "line to echo")
	flag.Parse()

	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		log.Fatal(err)
	}
	defer pc.Close()
	for _, k := range []webrtc.RTPCodecType{webrtc.RTPCodecTypeVideo, webrtc.RTPCodecTypeAudio} {
		if _, err := pc.AddTransceiverFromKind(k,
			webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
			log.Fatal(err)
		}
	}
	dc, _ := pc.CreateDataChannel("chat", nil)
	type dcEvent struct {
		Status string `json:"status"`
		Text   string `json:"text"`
	}
	subs := make(chan dcEvent, 64)
	dc.OnMessage(func(m webrtc.DataChannelMessage) {
		var ev dcEvent
		if json.Unmarshal(m.Data, &ev) == nil && ev.Status != "" {
			select {
			case subs <- ev:
			default:
			}
		}
	})

	speech := make(chan int, 1)
	pc.OnTrack(func(t *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if t.Kind() != webrtc.RTPCodecTypeAudio {
			go func() {
				for {
					if _, _, err := t.ReadRTP(); err != nil {
						return
					}
				}
			}()
			return
		}
		go func() {
			n := 0
			for {
				pkt, _, err := t.ReadRTP()
				if err != nil {
					return
				}
				if len(pkt.Payload) > 20 { // silence ≈3B
					n++
					select {
					case <-speech:
					default:
					}
					speech <- n
				}
			}
		}()
	})

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		log.Fatal(err)
	}
	gather := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		log.Fatal(err)
	}
	<-gather

	body, _ := json.Marshal(map[string]string{
		"sdp": pc.LocalDescription().SDP, "type": "offer",
	})
	resp, err := http.Post(*base+"/offer", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var ans struct {
		SDP       string `json:"sdp"`
		Type      string `json:"type"`
		SessionID string `json:"sessionid"`
	}
	if err := json.Unmarshal(raw, &ans); err != nil {
		log.Fatalf("offer: %s", raw)
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer, SDP: ans.SDP,
	}); err != nil {
		log.Fatal(err)
	}
	log.Printf("session %s connected", ans.SessionID)
	time.Sleep(3 * time.Second) // let the engine reach Ready

	hb, _ := json.Marshal(map[string]any{
		"text": *text, "type": "echo", "sessionid": ans.SessionID,
	})
	t0 := time.Now()
	hr, err := http.Post(*base+"/human", "application/json", bytes.NewReader(hb))
	if err != nil {
		log.Fatal(err)
	}
	hraw, _ := io.ReadAll(hr.Body)
	hr.Body.Close()
	log.Printf("/human echo sent: %s", bytes.TrimSpace(hraw))

	deadline := time.After(30 * time.Second)
	n := 0
	for {
		select {
		case ev := <-subs:
			log.Printf("subtitle: status=%s text=%q (+%.1fs)", ev.Status, ev.Text, time.Since(t0).Seconds())
		case n = <-speech:
			if n == 1 {
				log.Printf("first speech packet +%.1fs", time.Since(t0).Seconds())
			}
		case <-deadline:
			fmt.Println()
			if n > 0 {
				log.Printf("STATUS: PASS — echo produced %d speech packets", n)
			} else {
				log.Printf("STATUS: FAIL — no speech")
			}
			return
		}
	}
}
