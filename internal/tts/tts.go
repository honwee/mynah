// Package tts is cored's HTTP client for the existing OpenAI-compatible TTS
// service (vllm-omni qwen3-tts at :8091). It streams int16 PCM @24k and
// resamples to mono float32 @16k for the AvatarEngine.
//
// (P2 keeps TTS as an HTTP dependency; a later phase turns it into a gRPC
// worker behind the same streaming contract.)
package tts

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const (
	srcRate = 24000 // qwen3-tts native output
	dstRate = 16000 // AvatarEngine / wav2vec input
)

// Client calls POST {ServerURL}/v1/audio/speech.
type Client struct {
	ServerURL    string
	Voice        string
	Language     string
	Speed        float64
	Instructions string // optional style steer ("更沉稳"); empty = omitted
	TaskType     string
	Seed         int // 0 = let the model randomize per utterance; >0 = reproducible
	HTTP         *http.Client
}

// New returns a client with omnitts-compatible defaults.
func New(serverURL, voice string, seed int) *Client {
	if voice == "" {
		voice = "vivian"
	}
	return &Client{
		ServerURL: serverURL,
		Voice:     voice,
		Language:  "Auto",
		Speed:     1.0,
		TaskType:  "CustomVoice",
		Seed:      seed,
		HTTP:      &http.Client{},
	}
}

type speechReq struct {
	Input          string  `json:"input"`
	Voice          string  `json:"voice"`
	ResponseFormat string  `json:"response_format"`
	Speed          float64 `json:"speed"`
	Language       string  `json:"language"`
	TaskType       string  `json:"task_type"`
	Instructions   string  `json:"instructions,omitempty"` // omitted when empty
	Seed           int     `json:"seed,omitempty"`         // omitted when 0 → TTS samples freely
	Stream         bool    `json:"stream"`
}

// Stream synthesizes text and invokes onPCM with mono float32 @16k chunks as
// they arrive. It returns when the response ends or ctx is cancelled.
func (c *Client) Stream(ctx context.Context, text string, onPCM func([]float32)) error {
	body, _ := json.Marshal(speechReq{
		Input: text, Voice: c.Voice, ResponseFormat: "pcm",
		Speed: c.Speed, Language: c.Language, TaskType: c.TaskType,
		Instructions: c.Instructions, Seed: c.Seed, Stream: true,
	})
	url := c.ServerURL + "/v1/audio/speech"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("tts status %d: %s", resp.StatusCode, string(b))
	}

	rs := &resampler{ratio: float64(srcRate) / float64(dstRate)} // input samples per output sample
	var carry byte
	haveCarry := false
	buf := make([]byte, 16384)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			raw := buf[:n]
			if haveCarry {
				raw = append([]byte{carry}, raw...)
				haveCarry = false
			}
			if len(raw)%2 == 1 {
				carry = raw[len(raw)-1]
				haveCarry = true
				raw = raw[:len(raw)-1]
			}
			in := make([]float32, len(raw)/2)
			for i := range in {
				s := int16(binary.LittleEndian.Uint16(raw[i*2:]))
				in[i] = float32(s) / 32767.0
			}
			if out := rs.process(in); len(out) > 0 {
				onPCM(out)
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}

// resampler is a stateful linear resampler (24k -> 16k) that interpolates across
// chunk boundaries. step = inRate/outRate input samples per output sample.
type resampler struct {
	ratio   float64
	pending []float32
	frac    float64 // fractional input index into pending
}

func (r *resampler) process(in []float32) []float32 {
	r.pending = append(r.pending, in...)
	var out []float32
	for {
		i := int(r.frac)
		if i+1 >= len(r.pending) {
			break
		}
		t := r.frac - float64(i)
		out = append(out, r.pending[i]*float32(1-t)+r.pending[i+1]*float32(t))
		r.frac += r.ratio
	}
	// Drop fully-consumed input, keep the tail for boundary interpolation.
	drop := int(r.frac)
	if drop > 0 {
		if drop > len(r.pending) {
			drop = len(r.pending)
		}
		r.pending = append([]float32(nil), r.pending[drop:]...)
		r.frac -= float64(drop)
	}
	return out
}
