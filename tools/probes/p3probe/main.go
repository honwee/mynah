// p3probe: headless voice-input verifier. Connects to cored with a sendrecv
// audio transceiver (like the browser with mic on), streams a baked spoken
// question (raw Opus packets file: u32le size + data, 20ms each), and expects
// the avatar to answer: inbound audio/video packet rates must rise after the
// question (ASR -> chat -> TTS -> engine round trip).
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

var vCount, aCount int64

func main() {
	base := flag.String("base", "http://127.0.0.1:8020", "cored base URL")
	pktFile := flag.String("question", "workers/asr/question_long.opuspkts", "baked opus packets file (turn 1, long answer)")
	bargeFile := flag.String("barge", "workers/asr/question.opuspkts", "baked opus packets file (barge-in turn)")
	idleSec := flag.Int("idle", 0, "seconds of silent-mic idling before the question (cloud keepalive test)")
	silenceFile := flag.String("silence", "assets/silence_20ms.opus", "baked 20ms opus silence packet (for -idle)")
	flag.Parse()

	pkts, err := loadPackets(*pktFile)
	if err != nil {
		log.Fatalf("load packets: %v", err)
	}
	bargePkts, err := loadPackets(*bargeFile)
	if err != nil {
		log.Fatalf("load barge packets: %v", err)
	}
	log.Printf("loaded %d + %d opus packets", len(pkts), len(bargePkts))

	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		log.Fatal(err)
	}
	defer pc.Close()

	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		log.Fatal(err)
	}
	// mic track: sendrecv audio like the browser with the mic toggle on
	mic, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 1},
		"mic", "probe")
	if err != nil {
		log.Fatal(err)
	}
	if _, err := pc.AddTransceiverFromTrack(mic,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendrecv}); err != nil {
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
				} else if len(pkt.Payload) > 20 { // ignore silence-sized packets
					atomic.AddInt64(&aCount, 1)
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
		SessionID string `json:"sessionid"`
	}
	if err := json.Unmarshal(answer, &ans); err != nil {
		log.Fatalf("bad answer: %v (%s)", err, string(answer))
	}
	log.Printf("sessionid=%s", ans.SessionID)
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

	// settle, measure idle baseline
	time.Sleep(2 * time.Second)

	// optional idle phase: stream mic silence like a browser with the mic on
	// but nobody talking, long enough to cross the cloud brain's 180s
	// response-idle window (the 150s proactive rotation should fire).
	if *idleSec > 0 {
		sil, err := os.ReadFile(*silenceFile)
		if err != nil {
			log.Fatalf("load silence packet: %v", err)
		}
		log.Printf("idling %ds with silent mic...", *idleSec)
		tick := time.NewTicker(20 * time.Millisecond)
		idleEnd := time.Now().Add(time.Duration(*idleSec) * time.Second)
		for time.Now().Before(idleEnd) {
			<-tick.C
			_ = mic.WriteSample(media.Sample{Data: sil, Duration: 20 * time.Millisecond})
		}
		tick.Stop()
		log.Printf("idle done, asking the question now")
	}
	a0 := atomic.LoadInt64(&aCount)

	// speak the question over the mic track in realtime
	log.Printf("speaking question over mic track...")
	t0 := time.Now()
	tick := time.NewTicker(20 * time.Millisecond)
	for _, p := range pkts {
		<-tick.C
		_ = mic.WriteSample(media.Sample{Data: p, Duration: 20 * time.Millisecond})
	}
	tick.Stop()
	log.Printf("question sent (%.1fs)", time.Since(t0).Seconds())

	// wait for the full round trip: VAD end -> ASR -> LLM -> TTS -> engine
	deadline := time.Now().Add(20 * time.Second)
	var answered bool
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		if atomic.LoadInt64(&aCount)-a0 > 100 { // >2s of real speech audio
			answered = true
			break
		}
	}
	reply := atomic.LoadInt64(&aCount) - a0
	log.Printf("inbound speech audio packets after question: %d (+%.1fs)", reply, time.Since(t0).Seconds())

	// barge-in check: speak again WHILE the avatar is replying — cored must
	// interrupt on VAD speech onset, then answer the new question.
	if answered {
		log.Printf("barge-in: speaking again over the avatar's reply...")
		tick = time.NewTicker(20 * time.Millisecond)
		for _, p := range bargePkts {
			<-tick.C
			_ = mic.WriteSample(media.Sample{Data: p, Duration: 20 * time.Millisecond})
		}
		tick.Stop()
		a1 := atomic.LoadInt64(&aCount)
		dl2 := time.Now().Add(20 * time.Second)
		bargeAnswered := false
		for time.Now().Before(dl2) {
			time.Sleep(500 * time.Millisecond)
			if atomic.LoadInt64(&aCount)-a1 > 100 {
				bargeAnswered = true
				break
			}
		}
		log.Printf("barge-in second answer: %v (%d speech pkts)", bargeAnswered,
			atomic.LoadInt64(&aCount)-a1)
		if !bargeAnswered {
			answered = false
		}
	}

	// keep listening a bit to confirm flow continues
	time.Sleep(3 * time.Second)
	fmt.Printf("\n==== P3.5 VOICE PROBE ====\n")
	fmt.Printf("reply speech packets: %d, video pkts total: %d\n",
		atomic.LoadInt64(&aCount)-a0, atomic.LoadInt64(&vCount))
	if answered {
		fmt.Printf("STATUS: PASS (avatar answered the spoken question)\n")
	} else {
		fmt.Printf("STATUS: FAIL (no spoken reply within 20s)\n")
		os.Exit(1)
	}
}

func loadPackets(path string) ([][]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var pkts [][]byte
	for off := 0; off+4 <= len(raw); {
		n := int(binary.LittleEndian.Uint32(raw[off:]))
		off += 4
		if off+n > len(raw) {
			break
		}
		pkts = append(pkts, raw[off:off+n])
		off += n
	}
	return pkts, nil
}

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
