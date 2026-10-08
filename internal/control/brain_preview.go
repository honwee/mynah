package control

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"time"
)

// handleBrainPreview auditions a Qwen cloud voice: dial a throwaway realtime
// session with the requested voice (voice only applies on the FIRST
// session.update, so a fresh connection per preview is mandatory, not lazy),
// have it read a fixed sample line, and return the reply audio as WAV.
// Costs one short cloud turn per call.
func (s *Server) handleBrainPreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Voice string `json:"voice"`
		Model string `json:"model"`
		Text  string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	cur := s.deps.Config.Current().Brain
	if req.Voice == "" {
		req.Voice = cur.Voice
	}
	if req.Model == "" {
		req.Model = cur.Model
	}
	if req.Text == "" {
		req.Text = "你好，我是你的数字人助手，很高兴认识你。"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	pcm, err := s.deps.QwenPreview(ctx, req.Model, req.Voice, req.Text)
	if err != nil {
		fail(w, http.StatusBadGateway, "qwen preview: "+err.Error())
		return
	}
	if len(pcm) == 0 {
		fail(w, http.StatusBadGateway, "qwen preview: empty audio")
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(wav16kMono(pcm))
}

// wav16kMono wraps 16 kHz mono float32 PCM in a WAV container (s16le).
func wav16kMono(pcm []float32) []byte {
	n := len(pcm)
	b := make([]byte, 44+n*2)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+n*2))
	copy(b[8:], "WAVE")
	copy(b[12:], "fmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1) // PCM
	binary.LittleEndian.PutUint16(b[22:], 1) // mono
	binary.LittleEndian.PutUint32(b[24:], 16000)
	binary.LittleEndian.PutUint32(b[28:], 32000) // byte rate
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(n*2))
	for i, s := range pcm {
		if s > 1 {
			s = 1
		} else if s < -1 {
			s = -1
		}
		binary.LittleEndian.PutUint16(b[44+i*2:], uint16(int16(s*32767)))
	}
	return b
}
