// p2probe: a headless pion WebRTC client that verifies cored's media path end
// to end (no browser). It connects, counts inbound video/audio RTP during idle,
// triggers a /human echo turn, and confirms frames keep flowing while speaking.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
)

var vCount, aCount, vBytes, aBytes int64

func main() {
	base := flag.String("base", "http://127.0.0.1:8020", "cored base URL")
	text := flag.String("text", "你好，我是 Mynah 数字人，很高兴见到你。", "echo text")
	flag.Parse()

	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		log.Fatal(err)
	}
	defer pc.Close()

	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		log.Fatal(err)
	}
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		log.Fatal(err)
	}

	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		log.Printf("ontrack: %s %s", tr.Kind().String(), tr.Codec().MimeType)
		go func() {
			for {
				pkt, _, err := tr.ReadRTP()
				if err != nil {
					return
				}
				if tr.Kind() == webrtc.RTPCodecTypeVideo {
					atomic.AddInt64(&vCount, 1)
					atomic.AddInt64(&vBytes, int64(len(pkt.Payload)))
				} else {
					atomic.AddInt64(&aCount, 1)
					atomic.AddInt64(&aBytes, int64(len(pkt.Payload)))
				}
			}
		}()
	})

	connected := make(chan struct{}, 1)
	pc.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
		log.Printf("ice: %s", s)
		if s == webrtc.ICEConnectionStateConnected {
			select {
			case connected <- struct{}{}:
			default:
			}
		}
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

	answer := postJSON(*base+"/offer", map[string]string{
		"sdp": pc.LocalDescription().SDP, "type": "offer",
	})
	var ans struct {
		SDP       string `json:"sdp"`
		Type      string `json:"type"`
		SessionID string `json:"sessionid"`
	}
	if err := json.Unmarshal(answer, &ans); err != nil {
		log.Fatalf("bad answer: %v (%s)", err, string(answer))
	}
	log.Printf("got answer, sessionid=%s", ans.SessionID)
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer, SDP: ans.SDP,
	}); err != nil {
		log.Fatal(err)
	}

	select {
	case <-connected:
	case <-time.After(15 * time.Second):
		log.Fatal("ICE did not connect within 15s")
	}

	// Phase 1: idle (~3s) — expect continuous video (living standby).
	v0, a0 := snap()
	time.Sleep(3 * time.Second)
	v1, a1 := snap()
	log.Printf("IDLE 3s:      video=%d  audio=%d", v1-v0, a1-a0)

	// Phase 2: speak.
	log.Printf("POST /human echo: %q", *text)
	_ = postJSON(*base+"/human", map[string]any{
		"text": *text, "type": "echo", "interrupt": false, "sessionid": ans.SessionID,
	})
	time.Sleep(6 * time.Second)
	v2, a2 := snap()
	log.Printf("SPEAK 6s:     video=%d  audio=%d", v2-v1, a2-a1)

	// Phase 3: interrupt, settle back to idle.
	_ = postJSON(*base+"/interrupt_talk", map[string]any{"sessionid": ans.SessionID})
	time.Sleep(2 * time.Second)
	v3, a3 := snap()
	log.Printf("AFTER-INT 2s: video=%d  audio=%d", v3-v2, a3-a2)

	fmt.Printf("\n==== P2 PROBE RESULT ====\n")
	fmt.Printf("idle  video pkts/s ~ %.1f   audio pkts/s ~ %.1f\n", float64(v1-v0)/3.0, float64(a1-a0)/3.0)
	fmt.Printf("speak video pkts/s ~ %.1f   audio pkts/s ~ %.1f\n", float64(v2-v1)/6.0, float64(a2-a1)/6.0)
	ok := (v1-v0) > 30 && (v2-v1) > 60 && (a2-a1) > 60
	if ok {
		fmt.Printf("STATUS: PASS (idle video flowing + speak a/v flowing)\n")
	} else {
		fmt.Printf("STATUS: FAIL (insufficient packets)\n")
	}
}

func snap() (int64, int64) { return atomic.LoadInt64(&vCount), atomic.LoadInt64(&aCount) }

func postJSON(url string, body any) []byte {
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		log.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return out
}
