// Audition harness for the qwen-brain greeting path: verify a scripted line
// (channel greeting) is read verbatim by the cloud model in the session voice,
// on BOTH turn modes, without leaking into subsequent conversation.
//
//	-mode cloud  cloud-VAD session (prod: turn=smart_turn) -> Conn.SpeakVerbatim
//	-mode ptt    push-to-talk session (turn=local) -> Conn.SpeakVerbatimTurn,
//	             followed by a normal chat turn to prove no instruction leak
//
// Costs one short cloud turn per run (two in ptt mode).
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"time"

	"mynah/internal/qwenrt"
)

const persona = "你是林夏，一个亲切的数字人助手。回答简短口语化。"

func main() {
	url := flag.String("url", "", "realtime ws url")
	key := flag.String("key", os.Getenv("QWEN_RT_KEY"), "api key")
	model := flag.String("model", "qwen-audio-3.0-realtime-flash", "model")
	voice := flag.String("voice", "longanlingxi", "voice")
	greet := flag.String("greet", "你好呀，我是林夏，很高兴见到你。", "greeting line")
	mode := flag.String("mode", "cloud", "cloud (cloud-VAD) | ptt (push-to-talk)")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := qwenrt.Config{
		URL: *url, APIKey: *key, Model: *model, Voice: *voice,
		Instructions: func() string { return persona },
	}
	if *mode == "cloud" {
		cfg.TurnMode = "smart_turn"
	}
	conn, err := qwenrt.Dial(ctx, cfg)
	if err != nil {
		log.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	log.Printf("dialed mode=%s cloudVAD=%v voice=%s", *mode, cfg.CloudVAD(), *voice)

	if *mode == "cloud" {
		runCloud(ctx, conn, *greet)
		return
	}
	runPTT(ctx, conn, *greet)
}

// runCloud drives the production shape: the greeting is submitted to a live
// Serve loop, which owns every response event exactly as it does for a
// server-initiated reply.
func runCloud(ctx context.Context, conn *qwenrt.Conn, greet string) {
	samples, replies := 0, 0
	transcript := ""
	var userTexts []string
	done := make(chan struct{})

	go func() {
		err := conn.Serve(ctx, qwenrt.ServeEvents{
			OnUserText: func(t string) {
				log.Printf("event: user transcript %q  <-- unexpected for a greeting", t)
				userTexts = append(userTexts, t)
			},
			OnPCM:        func(p []float32) { samples += len(p) },
			OnReplyStart: func() { replies++ },
			OnReplyText:  func(t string) { transcript = t },
			OnError:      func(e error) { log.Printf("event: server error %v", e) },
			OnReplyDone: func(r string) {
				if r != "" {
					transcript = r
				}
				close(done)
			},
		})
		if err != nil && ctx.Err() == nil {
			log.Printf("serve ended: %v", err)
		}
	}()

	time.Sleep(300 * time.Millisecond) // let Serve attach
	t0 := time.Now()
	if err := conn.SpeakVerbatim(greet); err != nil {
		log.Fatalf("SpeakVerbatim: %v", err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		log.Fatalf("timed out (samples=%d transcript=%q)", samples, transcript)
	}

	log.Printf("greeting: %.2fs audio in %.2fs wall, transcript=%q",
		float64(samples)/16000, time.Since(t0).Seconds(), transcript)
	ok := true
	if samples == 0 {
		log.Printf("FAIL: no audio reached OnPCM")
		ok = false
	}
	if transcript != greet {
		log.Printf("FAIL: transcript != greeting (model paraphrased or answered)")
		ok = false
	}
	if len(userTexts) > 0 {
		log.Printf("WARN: a user transcript fired for the greeting: %v", userTexts)
	}
	if replies != 1 {
		log.Printf("WARN: expected exactly 1 reply, got %d", replies)
	}
	finish(ok, "greeting spoken verbatim in cloud voice via Serve loop")
}

// runPTT drives the push-to-talk shape and then asks a real question on the
// same connection: if the per-response instructions override had leaked into
// session state, the model would parrot the question instead of answering it.
func runPTT(ctx context.Context, conn *qwenrt.Conn, greet string) {
	ok := true

	n1 := 0
	t0 := time.Now()
	tr1, err := conn.SpeakVerbatimTurn(ctx, greet, func(p []float32) { n1 += len(p) }, nil)
	if err != nil {
		log.Fatalf("SpeakVerbatimTurn: %v", err)
	}
	log.Printf("greeting: %.2fs audio in %.2fs wall, transcript=%q",
		float64(n1)/16000, time.Since(t0).Seconds(), tr1)
	if n1 == 0 || tr1 != greet {
		log.Printf("FAIL: greeting not read verbatim")
		ok = false
	}

	const q = "你会做什么？"
	n2 := 0
	t1 := time.Now()
	tr2, err := conn.Turn(ctx, q, nil, func(p []float32) { n2 += len(p) }, nil)
	if err != nil {
		log.Fatalf("follow-up Turn: %v", err)
	}
	log.Printf("follow-up: %.2fs audio in %.2fs wall, reply=%q",
		float64(n2)/16000, time.Since(t1).Seconds(), tr2)
	if n2 == 0 {
		log.Printf("FAIL: no audio on follow-up turn")
		ok = false
	}
	if tr2 == q {
		log.Printf("FAIL: model PARROTED the question — verbatim instructions leaked into session")
		ok = false
	}
	finish(ok, "greeting verbatim, follow-up answered normally (no leak)")
}

func finish(ok bool, msg string) {
	if ok {
		log.Printf("STATUS: PASS — %s", msg)
		return
	}
	log.Printf("STATUS: FAIL")
	os.Exit(1)
}
