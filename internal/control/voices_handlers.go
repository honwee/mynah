package control

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// handleVoices proxies the TTS service's voice list (GET {tts}/v1/audio/voices)
// so the console can offer a dropdown instead of a free-text voice field
// (admin-console PRD §8). The TTS base URL follows the live config, so it
// works right after a hot apply. Response shape matches the console contract:
// [{id, name}]. The console degrades to a free-text field on any failure.
func (s *Server) handleVoices(w http.ResponseWriter, r *http.Request) {
	base := s.deps.Config.Current().TTS.BaseURL
	if base == "" {
		fail(w, http.StatusServiceUnavailable, "tts service not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/audio/voices", nil)
	if err != nil {
		fail(w, http.StatusBadGateway, "tts voices: "+err.Error())
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fail(w, http.StatusBadGateway, "tts service unreachable: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fail(w, http.StatusBadGateway, "tts voices: upstream status "+resp.Status)
		return
	}
	var body struct {
		Voices         []string          `json:"voices"`
		UploadedVoices []json.RawMessage `json:"uploaded_voices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		fail(w, http.StatusBadGateway, "tts voices: bad upstream body: "+err.Error())
		return
	}
	type voice struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	// uploaded_voices is [] when empty but an array of {name,...} objects once
	// custom voices exist; its names also appear in the top-level voices list.
	// Build a set of custom names so we can mark (not duplicate) them.
	custom := make(map[string]bool, len(body.UploadedVoices))
	for _, raw := range body.UploadedVoices {
		if n := voiceName(raw); n != "" {
			custom[n] = true
		}
	}
	out := make([]voice, 0, len(body.Voices))
	for _, v := range body.Voices {
		if custom[v] {
			out = append(out, voice{ID: v, Name: v + "（自定义）"})
		} else {
			out = append(out, voice{ID: v, Name: v})
		}
	}
	ok(w, out)
}

// voiceName extracts a voice name from one uploaded_voices entry, tolerating
// both a bare string and a {name,...} object (the TTS service returns objects;
// older/other builds may return strings).
func voiceName(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &o) == nil {
		return o.Name
	}
	return ""
}
