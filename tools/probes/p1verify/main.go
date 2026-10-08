// cored (P1): a minimal driver/verifier for the AvatarEngine gRPC contract.
// Streams a wav as realtime audio into a worker, receives video frames,
// writes them as PNGs, and reports first-frame latency / fps / interrupt behavior.
// This is NOT the full cored — it exercises the seam end-to-end, offline (no WebRTC).
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	pb "mynah/gen/go/proto/avatarengine/v1"
	"mynah/internal/engineclient"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}

// decodeWav uses ffmpeg to turn any audio file into mono float32le @16k.
func decodeWav(path string) []float32 {
	cmd := exec.Command("ffmpeg", "-v", "error", "-i", path,
		"-f", "f32le", "-ar", "16000", "-ac", "1", "-")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	must(cmd.Run())
	raw := out.Bytes()
	n := len(raw) / 4
	pcm := make([]float32, n)
	for i := 0; i < n; i++ {
		pcm[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4 : i*4+4]))
	}
	return pcm
}

func f32leBytes(s []float32) []byte {
	b := make([]byte, len(s)*4)
	for i, v := range s {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(v))
	}
	return b
}

func writePNG(dir string, n int, v *pb.VideoFrame) {
	w, h := int(v.Width), int(v.Height)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	src := v.Data // rgb24
	for i := 0; i < w*h; i++ {
		img.Pix[i*4+0] = src[i*3+0]
		img.Pix[i*4+1] = src[i*3+1]
		img.Pix[i*4+2] = src[i*3+2]
		img.Pix[i*4+3] = 255
	}
	f, err := os.Create(filepath.Join(dir, fmt.Sprintf("f%05d.png", n)))
	must(err)
	defer f.Close()
	must(png.Encode(f, img))
}

func main() {
	addr := flag.String("addr", "127.0.0.1:9401", "worker address")
	wav := flag.String("wav", "examples/podcast_16k.wav", "input audio")
	out := flag.String("out", "_out", "frame output dir")
	doInterrupt := flag.Bool("interrupt", false, "fire one interrupt at the midpoint")
	chunkMs := flag.Int("chunk_ms", 500, "audio chunk size in ms (realtime paced)")
	flag.Parse()

	must(os.MkdirAll(*out, 0o755))
	pcm := decodeWav(*wav)
	fmt.Printf("audio: %d samples (%.1fs @16k)\n", len(pcm), float64(len(pcm))/16000)

	cli, err := engineclient.Dial(*addr)
	must(err)
	defer cli.Close()

	h, err := cli.AE.Health(context.Background(), &pb.HealthRequest{})
	must(err)
	fmt.Printf("health: ready=%v detail=%q\n", h.Ready, h.Detail)

	stream, err := cli.AE.Session(context.Background())
	must(err)

	chunk := 16000 * *chunkMs / 1000
	mid := len(pcm) / 2

	// sender goroutine
	go func() {
		must(stream.Send(&pb.ClientFrame{Msg: &pb.ClientFrame_Start{
			Start: &pb.SessionSpec{SessionId: "p1", UseFaceCrop: true}}}))
		var seq uint64
		for i := 0; i < len(pcm); i += chunk {
			j := i + chunk
			if j > len(pcm) {
				j = len(pcm)
			}
			_ = stream.Send(&pb.ClientFrame{Msg: &pb.ClientFrame_Audio{
				Audio: &pb.AudioChunk{PcmF32Le_16K: f32leBytes(pcm[i:j]), Seq: seq}}})
			seq++
			if *doInterrupt && i <= mid && i+chunk > mid {
				time.Sleep(300 * time.Millisecond)
				_ = stream.Send(&pb.ClientFrame{Msg: &pb.ClientFrame_Interrupt{Interrupt: &pb.Interrupt{}}})
				fmt.Println(">>> sent INTERRUPT")
			}
			time.Sleep(time.Duration(*chunkMs) * time.Millisecond) // realtime
		}
		_ = stream.Send(&pb.ClientFrame{Msg: &pb.ClientFrame_Close{Close: &pb.Close{}}})
		_ = stream.CloseSend()
	}()

	// receiver
	start := time.Now()
	var firstFrame time.Duration
	frames := 0
	gens := map[uint64]int{}
	for {
		sf, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Println("recv err:", err)
			break
		}
		switch m := sf.Msg.(type) {
		case *pb.ServerFrame_Ready:
			fmt.Printf("READY %dx%d @%dfps sr=%d\n", m.Ready.Width, m.Ready.Height, m.Ready.Fps, m.Ready.SampleRate)
		case *pb.ServerFrame_Video:
			if frames == 0 {
				firstFrame = time.Since(start)
			}
			writePNG(*out, frames, m.Video)
			gens[m.Video.Gen]++
			frames++
		case *pb.ServerFrame_Stats:
			fmt.Printf("STATS server_frames=%d rtf=%.2f\n", m.Stats.Frames, m.Stats.Rtf)
		case *pb.ServerFrame_Error:
			fmt.Println("ENGINE ERROR:", m.Error.Message)
		}
	}
	dur := time.Since(start)
	fmt.Printf("DONE frames=%d firstFrame=%dms wall=%.1fs fps=%.1f gens=%v out=%s\n",
		frames, firstFrame.Milliseconds(), dur.Seconds(), float64(frames)/dur.Seconds(), gens, *out)
}
