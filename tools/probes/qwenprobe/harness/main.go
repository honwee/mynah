package main

// Minimal harness for internal/qwenrt.Turn (text inject) — mirrors what the
// session does, run standalone to debug event flow.

import (
	"context"
	"fmt"
	"os"
	"time"

	"mynah/internal/qwenrt"
)

func main() {
	cfg := qwenrt.Config{
		URL:    os.Getenv("QWEN_RT_URL"), // wss://<workspace>.<region>.maas.aliyuncs.com/api-ws/v1/realtime
		APIKey: os.Getenv("QWEN_RT_KEY"),
		Model:  "qwen-audio-3.0-realtime-flash",
		Voice:  "longanqian",
		Instructions: func() string {
			return "你是一个数字人助手，用简短、口语化的中文回答。"
		},
	}
	ctx := context.Background()
	conn, err := qwenrt.Dial(ctx, cfg)
	if err != nil {
		panic(err)
	}
	defer conn.Close()
	t0 := time.Now()
	n := 0
	tr, err := conn.Turn(ctx, "用一句话夸夸今天的天气", nil, func(pcm []float32) {
		if n == 0 {
			fmt.Printf("first pcm +%dms\n", time.Since(t0).Milliseconds())
		}
		n += len(pcm)
	}, nil)
	fmt.Printf("turn done +%dms err=%v samples16k=%d (%.2fs) transcript=%q\n",
		time.Since(t0).Milliseconds(), err, n, float64(n)/16000, tr)
}
