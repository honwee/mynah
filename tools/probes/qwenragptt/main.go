// Probe: does injected knowledge actually reach the cloud model on a
// push-to-talk turn? Asks a question the model cannot know, first WITHOUT
// context (expect a hedge/refusal) then WITH a knowledge item seeded ahead of
// the turn (expect the exact fact). This is the shape RAG must take in
// brain=qwen + turn=local sessions — cloud-VAD has no injection window
// (create_response=false is ignored; see qwenragprobe).
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"strings"
	"time"

	"mynah/internal/qwenrt"
)

const (
	question = "你们公司的退货政策是几天？"
	// A fact the model cannot possibly know — if it says this number, the
	// injected knowledge was used.
	fact     = "本公司退货政策为自签收之日起 17 天内无理由退货，生鲜类商品除外。"
	needle   = "17"
	kbPrefix = "以下是与用户问题相关的知识库内容，回答时优先依据这些内容；与问题无关时忽略：\n1. "
)

func main() {
	url := flag.String("url", "", "realtime ws url")
	key := flag.String("key", os.Getenv("QWEN_RT_KEY"), "api key")
	model := flag.String("model", "qwen-audio-3.0-realtime-flash", "model")
	voice := flag.String("voice", "longanlingxi", "voice")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	dial := func() *qwenrt.Conn {
		c, err := qwenrt.Dial(ctx, qwenrt.Config{
			URL: *url, APIKey: *key, Model: *model, Voice: *voice,
			Instructions: func() string { return "你是林夏，一个亲切的客服助手。回答简短口语化。" },
		})
		if err != nil {
			log.Fatalf("dial: %v", err)
		}
		return c
	}

	// --- control: no knowledge injected ---
	c1 := dial()
	r1, err := c1.Turn(ctx, question, nil, func([]float32) {}, nil)
	if err != nil {
		log.Fatalf("control turn: %v", err)
	}
	c1.Close()
	log.Printf("WITHOUT knowledge: %q", r1)

	// --- test: knowledge seeded as a system-ish user item before the turn ---
	c2 := dial()
	if err := c2.SeedKnowledge(kbPrefix + fact); err != nil {
		log.Fatalf("SeedKnowledge: %v", err)
	}
	r2, err := c2.Turn(ctx, question, nil, func([]float32) {}, nil)
	if err != nil {
		log.Fatalf("rag turn: %v", err)
	}
	c2.Close()
	log.Printf("WITH knowledge:    %q", r2)

	usedControl := strings.Contains(r1, needle)
	usedRAG := strings.Contains(r2, needle)
	log.Printf("--- control mentions %q: %v | rag mentions %q: %v ---",
		needle, usedControl, needle, usedRAG)
	if usedRAG && !usedControl {
		log.Printf("STATUS: PASS — injected knowledge reaches and steers the cloud model")
		return
	}
	if usedControl {
		log.Printf("STATUS: INCONCLUSIVE — control also said %q (pick a rarer fact)", needle)
	} else {
		log.Printf("STATUS: FAIL — injected knowledge did NOT steer the reply")
	}
	os.Exit(1)
}
