// toolprobe: a headless pion client that verifies the ability contract end to
// end against a running cored (no browser). It plays the part of a host page —
// declares two abilities over the chat DataChannel, asks a question, and checks
// what actually comes back:
//
//   - an ACTION call must arrive while the avatar is still speaking, not after
//     the turn ends (that simultaneity is the whole demo);
//   - a BLOCKING call must be preceded by a spoken filler, and the number the
//     probe returns must show up in the final answer.
//
// Both checks are about ordering, which is exactly what unit tests can't see.
//
//	go run ./tools/probes/toolprobe -base http://127.0.0.1:8020
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

// abilities is what a BI page would declare. cored must treat every field below
// as opaque — if anything in here needs a matching change in cored, the design
// has leaked.
var abilities = []map[string]any{
	{
		"name":        "goto_page",
		"description": "打开大屏上的指定页面，并可按区域筛选",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"page": map[string]any{"type": "string",
					"enum":        []string{"overview", "sales", "supply", "finance"},
					"description": "要打开的页面"},
				"region": map[string]any{"type": "string", "description": "区域，如 华东"},
			},
			"required": []string{"page"},
		},
	},
	{
		"name":        "query_metric",
		"description": "查询某个业务指标的当前数值",
		"blocking":    true,
		"filler":      "我查一下。",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"metric": map[string]any{"type": "string", "description": "指标名"},
				"region": map[string]any{"type": "string"},
			},
			"required": []string{"metric"},
		},
	},
}

// magicValue goes back as the blocking call's result. Finding it in the spoken
// text is the proof the result actually reached the model, rather than the
// model inventing a plausible number.
const magicValue = 3186

// blockingResult is what the probe hands back for a blocking call, and needle is
// what the final answer must contain to prove the result reached the model
// rather than the model inventing a plausible number. -result overrides both.
var blockingResult any = map[string]any{"value": magicValue, "unit": "万元", "yoy": "+12.4%"}
var needle = fmt.Sprint(magicValue)

// verbose dumps every raw tool_call frame — the fastest way to tell a cored bug
// apart from a probe-side misreading.
var verbose bool

type event struct {
	at   time.Duration
	kind string // "say" | "tool" | "user"
	text string
}

type recorder struct {
	mu     sync.Mutex
	t0     time.Time
	events []event
	said   strings.Builder
}

func (r *recorder) add(kind, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event{time.Since(r.t0), kind, text})
	if kind == "say" {
		r.said.WriteString(text)
	}
}

func main() {
	base := flag.String("base", "http://127.0.0.1:8020", "cored base URL")
	ask := flag.String("ask", "帮我打开华东区的销售页面，顺便告诉我销售额是多少",
		"the question to ask (should provoke both abilities)")
	wait := flag.Duration("wait", 25*time.Second, "how long to watch the turn")
	// A published channel freezes its own brain, so -slug is how you point the
	// probe at a brain=local channel on a deployment whose global brain is qwen
	// (tool calling only exists on the local LLM path).
	slug := flag.String("slug", "", "published channel to join (empty = raw /offer)")
	token := flag.String("token", "", "channel access token, if access_mode=token")
	// The regression case: with no abilities declared the turn must take the
	// pre-tool code path byte for byte. Same probe, same question, so the two
	// runs are directly comparable.
	bare := flag.Bool("no-abilities", false, "declare nothing (regression: plain turn must be unchanged)")
	// A channel with a domains whitelist rejects a request that presents no
	// Origin at all, which is exactly what a headless probe sends by default.
	origin := flag.String("origin", "", "Origin header to present (needed for a domain-restricted channel)")
	// A real host page declares its own abilities. Point these at a JSON file to
	// probe an actual integration (e.g. the BI demo) instead of the built-ins.
	abilFile := flag.String("abilities", "", "JSON file with the abilities array (default: the built-in BI-ish pair)")
	actionName := flag.String("action", "goto_page", "name of the action-kind ability the checks look for")
	blockName := flag.String("blocking", "query_metric", "name of the blocking ability the checks look for")
	resultJSON := flag.String("result", "", "JSON object to return for the blocking call (default: the magic value)")
	// With a structured -result there is no single token to look for, so the
	// caller names one (a number from the result that the answer should quote).
	needleFlag := flag.String("needle", "", "string the final answer must contain (default: the magic value)")
	flag.BoolVar(&verbose, "v", false, "dump raw tool_call frames")
	flag.Parse()

	if *abilFile != "" {
		b, err := os.ReadFile(*abilFile)
		if err != nil {
			log.Fatalf("-abilities: %v", err)
		}
		// Decoding into the non-empty built-in slice would MERGE element-wise —
		// encoding/json reuses existing maps and only adds keys — so the file's
		// second ability would silently inherit the built-in second one's
		// blocking/filler. Cost me an afternoon of blaming cored. Start empty.
		abilities = nil
		if err := json.Unmarshal(b, &abilities); err != nil {
			log.Fatalf("-abilities: %v", err)
		}
	}
	if *resultJSON != "" {
		if err := json.Unmarshal([]byte(*resultJSON), &blockingResult); err != nil {
			log.Fatalf("-result: %v", err)
		}
		needle = ""
	}
	if *needleFlag != "" {
		needle = *needleFlag
	}
	if needle == "" {
		log.Printf("no -needle given with -result: skipping the did-the-result-" +
			"reach-the-answer check (a whole JSON object never appears in speech)")
	}

	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		log.Fatal(err)
	}
	defer pc.Close()

	for _, kind := range []webrtc.RTPCodecType{webrtc.RTPCodecTypeVideo, webrtc.RTPCodecTypeAudio} {
		if _, err := pc.AddTransceiverFromKind(kind, webrtc.RTPTransceiverInit{
			Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
			log.Fatal(err)
		}
	}

	rec := &recorder{t0: time.Now()}
	dcOpen := make(chan struct{})
	dc, err := pc.CreateDataChannel("chat", nil)
	if err != nil {
		log.Fatal(err)
	}
	dc.OnOpen(func() {
		defer close(dcOpen)
		if *bare {
			log.Printf("declared NO abilities (regression run)")
			return
		}
		b, _ := json.Marshal(map[string]any{"type": "abilities", "abilities": abilities})
		_ = dc.SendText(string(b))
		names := make([]string, 0, len(abilities))
		for _, a := range abilities {
			names = append(names, fmt.Sprint(a["name"]))
		}
		log.Printf("declared abilities: %s", strings.Join(names, ", "))
	})
	dc.OnMessage(func(m webrtc.DataChannelMessage) { onMessage(dc, rec, m.Data) })

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		log.Fatal(err)
	}
	gather := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		log.Fatal(err)
	}
	<-gather

	offerURL, offerBody := *base+"/offer", map[string]string{
		"sdp": pc.LocalDescription().SDP, "type": "offer"}
	if *slug != "" {
		offerURL = *base + "/channel/offer"
		offerBody["slug"], offerBody["token"] = *slug, *token
	}
	probeOrigin = *origin
	raw := postJSON(offerURL, offerBody)
	var ans struct {
		SDP       string `json:"sdp"`
		SessionID string `json:"sessionid"`
	}
	if err := json.Unmarshal(raw, &ans); err != nil {
		log.Fatalf("bad answer: %v (%s)", err, raw)
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer, SDP: ans.SDP}); err != nil {
		log.Fatal(err)
	}
	log.Printf("connected, session=%s", ans.SessionID)

	select {
	case <-dcOpen:
	case <-time.After(15 * time.Second):
		log.Fatal("datachannel never opened")
	}

	rec.mu.Lock()
	rec.t0 = time.Now() // clock starts at the question, so offsets read naturally
	rec.mu.Unlock()
	log.Printf("asking: %q", *ask)
	postJSON(*base+"/human", map[string]any{
		"text": *ask, "type": "chat", "interrupt": true, "sessionid": ans.SessionID})

	time.Sleep(*wait)
	report(rec, *bare, *actionName, *blockName)
}

func onMessage(dc *webrtc.DataChannel, rec *recorder, data []byte) {
	if string(data) == "pong" {
		return
	}
	var m struct {
		Type      string          `json:"type"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Blocking  bool            `json:"blocking"`
		Arguments json.RawMessage `json:"arguments"`
		Status    string          `json:"status"`
		Text      string          `json:"text"`
	}
	if json.Unmarshal(data, &m) != nil {
		return
	}

	if m.Type == "tool_call" && verbose {
		log.Printf("raw tool_call: %s", data)
	}
	switch {
	case m.Type == "tool_call":
		rec.add("tool", fmt.Sprintf("%s%s %s", m.Name,
			map[bool]string{true: " [blocking]", false: ""}[m.Blocking], m.Arguments))
		if !m.Blocking {
			return
		}
		// Answer with a deliberate delay: an instant reply would hide whether
		// the filler is really covering the wait.
		go func(id string) {
			time.Sleep(1200 * time.Millisecond)
			b, _ := json.Marshal(map[string]any{"type": "tool_result", "id": id,
				"result": blockingResult})
			_ = dc.SendText(string(b))
			rec.add("tool", "-> result "+needle)
		}(m.ID)
	case m.Status == "ing":
		rec.add("say", m.Text)
	case m.Status == "user":
		rec.add("user", m.Text)
	}
}

func report(rec *recorder, bare bool, actionName, blockName string) {
	rec.mu.Lock()
	defer rec.mu.Unlock()

	fmt.Printf("\n==== TIMELINE ====\n")
	for _, e := range rec.events {
		fmt.Printf("%7.2fs  %-5s %s\n", e.at.Seconds(), e.kind, e.text)
	}

	said := rec.said.String()
	var action, blocking, resulted *event
	var firstSay, lastSay *event
	for i := range rec.events {
		e := &rec.events[i]
		switch {
		case e.kind == "tool" && strings.HasPrefix(e.text, actionName) && action == nil:
			action = e
		case e.kind == "tool" && strings.HasPrefix(e.text, blockName) && blocking == nil:
			blocking = e
		case e.kind == "tool" && strings.HasPrefix(e.text, "-> result") && resulted == nil:
			resulted = e
		case e.kind == "say":
			if firstSay == nil {
				firstSay = e
			}
			lastSay = e
		}
	}

	fmt.Printf("\n==== CHECKS ====\n")
	pass := true
	check := func(ok bool, label, detail string) {
		if !ok {
			pass = false
		}
		fmt.Printf("[%s] %s%s\n", map[bool]string{true: "PASS", false: "FAIL"}[ok], label, detail)
	}

	// Regression run: nothing was declared, so the turn must be an ordinary one
	// — it speaks, and not one byte of the tool protocol shows up.
	if bare {
		check(firstSay != nil, "plain turn still speaks",
			func() string {
				if firstSay == nil {
					return " — silence"
				}
				return fmt.Sprintf(" — first at %.2fs: %q", firstSay.at.Seconds(), firstSay.text)
			}())
		check(action == nil && blocking == nil, "no tool traffic without abilities", "")
		fmt.Printf("\nSTATUS: %s\n", map[bool]string{true: "PASS", false: "FAIL"}[pass])
		return
	}

	check(action != nil, "action call dispatched ("+actionName+")", "")
	// The point of fire-and-forget: the page changes mid-sentence, not after.
	if action != nil && lastSay != nil {
		check(action.at < lastSay.at, "action landed while still speaking",
			fmt.Sprintf(" — call at %.2fs, last speech at %.2fs", action.at.Seconds(), lastSay.at.Seconds()))
	}
	check(blocking != nil, "blocking call dispatched ("+blockName+")", "")
	// A blocking call with no filler in front of it is a frozen avatar.
	if blocking != nil {
		var filler *event
		for i := range rec.events {
			e := &rec.events[i]
			if e.kind == "say" && e.at > blocking.at {
				filler = e
				break
			}
		}
		check(filler != nil, "filler spoken while waiting",
			func() string {
				if filler == nil {
					return " — nothing spoken after the call: silence"
				}
				return fmt.Sprintf(" — %q at %.2fs", filler.text, filler.at.Seconds())
			}())
	}
	if needle != "" {
		check(strings.Contains(said, needle),
			"tool result reached the answer",
			fmt.Sprintf(" — looking for %s in: %q", needle, said))
	}

	fmt.Printf("\nSTATUS: %s\n", map[bool]string{true: "PASS", false: "FAIL"}[pass])
}

// probeOrigin is echoed as the Origin header on every request once -origin is set.
var probeOrigin string

func postJSON(url string, body any) []byte {
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		log.Fatalf("POST %s: %v", url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if probeOrigin != "" {
		req.Header.Set("Origin", probeOrigin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return out
}
