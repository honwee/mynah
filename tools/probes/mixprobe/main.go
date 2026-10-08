// mixprobe: verifies MIXED-POOL dispatch — that a channel bound to one engine is
// served by a worker of that engine, and that two such channels can hold
// sessions at the same time.
//
// Why a separate probe: p3probe posts to /offer, which has no channel concept,
// so it always takes the live global avatar and can never exercise an
// engine-bound channel. This one posts to /channel/offer with a slug, which is
// the path a real visitor takes and the only path where the channel's frozen
// avatar (hence its engine) applies.
//
// It is recvonly and silent: no mic, no question, no TTS. The claim under test is
// "which worker got this session", and holding an idle session is enough to
// observe that in GET /api/v1/workers — a spoken round trip is p3probe's job and
// would only add ways for this to fail for unrelated reasons.
package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
)

type result struct {
	slug   string
	frames int64
	err    error
}

func main() {
	base := flag.String("base", "https://127.0.0.1:8443", "cored visitor base URL")
	slugs := flag.String("slugs", "", "comma-separated channel slugs to connect SIMULTANEOUSLY")
	tokens := flag.String("tokens", "", "comma-separated access tokens, aligned with -slugs (empty entries for public channels)")
	hold := flag.Duration("hold", 25*time.Second, "how long to hold each session (cover engine cold start)")
	flag.Parse()

	list := splitCSV(*slugs)
	if len(list) == 0 {
		log.Fatal("-slugs is required")
	}
	tokList := splitCSV(*tokens)

	// All at once, not one after another: sequential connects would pass even on
	// a pool that can only serve one engine at a time.
	var wg sync.WaitGroup
	results := make([]result, len(list))
	for i, slug := range list {
		tok := ""
		if i < len(tokList) {
			tok = tokList[i]
		}
		wg.Add(1)
		go func(i int, slug, tok string) {
			defer wg.Done()
			results[i] = connect(*base, slug, tok, *hold)
		}(i, slug, tok)
	}
	wg.Wait()

	failed := false
	for _, r := range results {
		if r.err != nil {
			failed = true
			log.Printf("FAIL %s: %v", r.slug, r.err)
			continue
		}
		if r.frames == 0 {
			failed = true
			log.Printf("FAIL %s: connected but received 0 video frames", r.slug)
			continue
		}
		log.Printf("PASS %s: %d video packets", r.slug, r.frames)
	}
	if failed {
		log.Fatal("mixprobe: FAILED")
	}
	log.Print("mixprobe: ALL PASS")
}

func connect(base, slug, token string, hold time.Duration) result {
	res := result{slug: slug}
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		res.err = err
		return res
	}
	defer pc.Close()

	for _, kind := range []webrtc.RTPCodecType{webrtc.RTPCodecTypeVideo, webrtc.RTPCodecTypeAudio} {
		if _, err := pc.AddTransceiverFromKind(kind,
			webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
			res.err = err
			return res
		}
	}
	var frames int64
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if tr.Kind() != webrtc.RTPCodecTypeVideo {
			return
		}
		go func() {
			for {
				if _, _, err := tr.ReadRTP(); err != nil {
					return
				}
				atomic.AddInt64(&frames, 1)
			}
		}()
	})
	connected := make(chan struct{}, 1)
	pc.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
		log.Printf("[%s] ice: %s", slug, s)
		if s == webrtc.ICEConnectionStateConnected {
			select {
			case connected <- struct{}{}:
			default:
			}
		}
	})

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		res.err = err
		return res
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		res.err = err
		return res
	}
	// Wait for ICE gathering: the answer path is a single POST, so trickle has
	// nowhere to go.
	<-webrtc.GatheringCompletePromise(pc)

	answer, err := postJSON(base+"/channel/offer", map[string]string{
		"sdp": pc.LocalDescription().SDP, "type": "offer",
		"slug": slug, "token": token,
	})
	if err != nil {
		res.err = err
		return res
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer, SDP: answer,
	}); err != nil {
		res.err = err
		return res
	}
	select {
	case <-connected:
	case <-time.After(30 * time.Second):
		res.err = fmt.Errorf("ICE did not connect in 30s")
		return res
	}
	time.Sleep(hold)
	res.frames = atomic.LoadInt64(&frames)
	return res
}

func postJSON(url string, body map[string]string) (string, error) {
	b, _ := json.Marshal(body)
	// Self-signed cert on the loopback test path; the probe runs on the box.
	cl := &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	resp, err := cl.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var out struct {
		SDP string `json:"sdp"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("bad answer json: %w (%s)", err, raw)
	}
	if out.SDP == "" {
		return "", fmt.Errorf("answer has no sdp: %s", raw)
	}
	return out.SDP, nil
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		out = append(out, strings.TrimSpace(p))
	}
	if len(out) == 1 && out[0] == "" {
		return nil
	}
	return out
}
