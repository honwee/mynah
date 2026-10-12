// Audition harness for qwenrt.Preview: dial with a voice, read a line, dump WAV.
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"log"
	"os"
	"time"

	"mynah/internal/qwenrt"
)

func main() {
	url := flag.String("url", "", "realtime ws url")
	key := flag.String("key", os.Getenv("QWEN_RT_KEY"), "api key")
	model := flag.String("model", "qwen-audio-3.0-realtime-flash", "model")
	voice := flag.String("voice", "longanlingxin", "voice to audition")
	text := flag.String("text", "你好，我是你的数字人助手，很高兴认识你。", "sample line")
	out := flag.String("out", "/tmp/qwen_voice_preview.wav", "wav output")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	t0 := time.Now()
	pcm, err := qwenrt.Preview(ctx, qwenrt.Config{URL: *url, APIKey: *key, Model: *model, Voice: *voice}, *text)
	if err != nil {
		log.Fatalf("preview: %v", err)
	}
	log.Printf("voice=%s got %d samples (%.2fs audio) in %.2fs", *voice, len(pcm), float64(len(pcm))/16000, time.Since(t0).Seconds())
	b := make([]byte, 44+len(pcm)*2)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+len(pcm)*2))
	copy(b[8:], "WAVE")
	copy(b[12:], "fmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], 16000)
	binary.LittleEndian.PutUint32(b[28:], 32000)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(len(pcm)*2))
	for i, s := range pcm {
		if s > 1 {
			s = 1
		} else if s < -1 {
			s = -1
		}
		binary.LittleEndian.PutUint16(b[44+i*2:], uint16(int16(s*32767)))
	}
	if err := os.WriteFile(*out, b, 0644); err != nil {
		log.Fatal(err)
	}
	log.Printf("wrote %s", *out)
}
