// Headless e2e probe for the qwen-brain cored: pion offer -> /human chat ->
// verify reply speech reaches the browser leg. Speech detection uses cored's
// own heuristic: worker Opus silence packets are ~3B, real speech is bigger.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/pion/webrtc/v4"
)

const base = "http://127.0.0.1:8046"

func main() {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		panic(err)
	}
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		panic(err)
	}
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		panic(err)
	}

	type dcEvent struct {
		Status string `json:"status"`
		Text   string `json:"text"`
	}
	dcCh := make(chan dcEvent, 64)
	dc, _ := pc.CreateDataChannel("chat", nil)
	dc.OnMessage(func(m webrtc.DataChannelMessage) {
		var ev dcEvent
		if json.Unmarshal(m.Data, &ev) == nil && ev.Status != "" {
			select {
			case dcCh <- ev:
			default:
			}
		}
	})

	vframes := make(chan int, 1)
	speechPkts := make(chan int, 1)
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		go func() {
			nv, nsp := 0, 0
			for {
				pkt, _, err := track.ReadRTP()
				if err != nil {
					return
				}
				if track.Kind() == webrtc.RTPCodecTypeVideo {
					nv++
					select {
					case <-vframes:
					default:
					}
					vframes <- nv
				} else if len(pkt.Payload) > 20 { // silence ≈3B, speech >>20B
					nsp++
					select {
					case <-speechPkts:
					default:
					}
					speechPkts <- nsp
				}
			}
		}()
	})

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		panic(err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		panic(err)
	}
	<-gathered

	body, _ := json.Marshal(map[string]string{
		"sdp": pc.LocalDescription().SDP, "type": "offer"})
	resp, err := http.Post(base+"/offer", "application/json", bytes.NewReader(body))
	if err != nil {
		panic(err)
	}
	rb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var ans struct {
		SDP       string `json:"sdp"`
		Type      string `json:"type"`
		SessionID string `json:"sessionid"`
	}
	if err := json.Unmarshal(rb, &ans); err != nil {
		fmt.Println("offer failed:", resp.Status, string(rb[:min(len(rb), 200)]))
		os.Exit(1)
	}
	fmt.Println("session", ans.SessionID)
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{
		SDP: ans.SDP, Type: webrtc.SDPTypeAnswer}); err != nil {
		panic(err)
	}

	time.Sleep(3 * time.Second)
	baseV, baseSp := lastOr(vframes, 0), lastOr(speechPkts, 0)
	fmt.Printf("pre-chat: vframes=%d speech_pkts=%d\n", baseV, baseSp)

	t0 := time.Now()
	body, _ = json.Marshal(map[string]any{
		"sessionid": ans.SessionID, "type": "chat", "interrupt": true,
		"text": "用一句话夸夸今天的天气"})
	resp, err = http.Post(base+"/human", "application/json", bytes.NewReader(body))
	if err != nil {
		panic(err)
	}
	rb, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	fmt.Println("chat sent:", resp.Status, string(rb))

	deadline := time.After(25 * time.Second)
	firstSpeech := time.Duration(0)
	var transcript string
loop:
	for {
		select {
		case ev := <-dcCh:
			fmt.Printf("dc: %+v (+%dms)\n", ev, time.Since(t0).Milliseconds())
			if ev.Status == "ing" && ev.Text != "" {
				transcript = ev.Text
			}
			if ev.Status == "end" && transcript != "" {
				break loop
			}
		case <-deadline:
			break loop
		default:
			if firstSpeech == 0 {
				if n := lastOr(speechPkts, 0); n > baseSp+10 {
					firstSpeech = time.Since(t0)
					fmt.Printf("SPEECH on audio track +%dms\n", firstSpeech.Milliseconds())
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	nv, nsp := lastOr(vframes, 0), lastOr(speechPkts, 0)
	fmt.Printf("post: vframes=%d (+%d) speech_pkts=%d (+%d)\n", nv, nv-baseV, nsp, nsp-baseSp)
	fmt.Println("transcript:", transcript)
	if firstSpeech > 0 && nv > baseV+100 && transcript != "" {
		fmt.Println("E2E: PASS")
	} else {
		fmt.Println("E2E: FAIL")
	}
	pc.Close()
}

func lastOr(ch chan int, def int) int {
	select {
	case v := <-ch:
		return v
	default:
		return def
	}
}
