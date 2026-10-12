// Package voiceclone is the EE admin extension for zero-shot voice cloning.
// It mounts three authenticated routes that proxy the TTS service's voice
// management API (the same service cored already lists voices from):
//
//	POST   /api/v1/tts/voices          multipart upload -> {tts}/v1/audio/voices
//	DELETE /api/v1/tts/voices/{name}   delete a custom voice (built-ins protected)
//	POST   /api/v1/tts/voices/preview  synth a sample -> audio/wav for audition
//
// Cloned voices live on the TTS side; once uploaded they surface in the
// existing GET /api/v1/tts/voices list (marked "（自定义）") and in the config
// center voice dropdown. No DB, migration, or async job is involved — the TTS
// upload is synchronous.
package voiceclone

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"mynah/core"
)

const (
	maxUploadBytes = 20 << 20 // 20MB reference audio cap (per clip)
	maxClips       = 5        // at most this many reference clips per upload
	uploadTimeout  = 60 * time.Second
	previewTimeout = 60 * time.Second
	previewSeed    = 42 // fixed seed so auditions of the same voice+text are reproducible
)

// nameRe bounds voice names to a filesystem/URL-safe slug so they can't break
// the TTS path or collide with control characters.
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,39}$`)

// Extension implements core.AdminExtension.
type Extension struct {
	http    *http.Client
	ffmpeg  string // resolved ffmpeg path; "" disables transcoding (forward as-is)
	asrHTTP string // SenseVoice one-shot transcribe base, e.g. http://127.0.0.1:9404
}

// New builds the voice-clone admin extension.
func New() *Extension {
	asr := os.Getenv("PL_ASR_HTTP")
	if asr == "" {
		asr = "http://127.0.0.1:9404"
	}
	return &Extension{http: &http.Client{}, ffmpeg: findFFmpeg(), asrHTTP: strings.TrimRight(asr, "/")}
}

// findFFmpeg locates an ffmpeg binary for upload transcoding. Empty result =
// transcoding disabled (uploads forwarded unchanged).
func findFFmpeg() string {
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	for _, p := range []string{"/usr/bin/ffmpeg", "/usr/local/bin/ffmpeg", "/opt/homebrew/bin/ffmpeg"} {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

// transcodeToWAV converts arbitrary input audio to a mono PCM WAV via ffmpeg so
// the TTS decoder (libsndfile, which rejects m4a/aac/webm) accepts it. Both
// input and output use temp files: MP4/m4a stores its moov atom at the end and
// cannot be demuxed from a non-seekable pipe (that yields a 0-sample WAV), and
// a temp output file keeps the WAV header seekable/valid. Returns (nil,false)
// when ffmpeg is unavailable or produces no audio — the caller then forwards
// the original bytes.
func (e *Extension) transcodeToWAV(ctx context.Context, in []byte) ([]byte, bool) {
	if e.ffmpeg == "" {
		return nil, false
	}
	inTmp, err := os.CreateTemp("", "vc-in-*")
	if err != nil {
		return nil, false
	}
	defer os.Remove(inTmp.Name())
	if _, err := inTmp.Write(in); err != nil {
		inTmp.Close()
		return nil, false
	}
	inTmp.Close()
	outTmp, err := os.CreateTemp("", "vc-out-*.wav")
	if err != nil {
		return nil, false
	}
	defer os.Remove(outTmp.Name())
	outTmp.Close()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.ffmpeg, "-hide_banner", "-loglevel", "error",
		"-y", "-i", inTmp.Name(), "-ac", "1", outTmp.Name())
	if err := cmd.Run(); err != nil {
		return nil, false
	}
	out, err := os.ReadFile(outTmp.Name())
	if err != nil || len(out) <= 44 { // <=44 bytes = WAV header only, no samples
		return nil, false
	}
	return out, true
}

// transcodePCM16k decodes input audio to 16kHz mono signed-16-bit little-endian
// PCM (headerless) for the ASR worker. Input goes to a temp file (m4a needs a
// seekable source); raw PCM has no header so stdout is fine. Returns
// (nil,false) on failure.
func (e *Extension) transcodePCM16k(ctx context.Context, in []byte) ([]byte, bool) {
	if e.ffmpeg == "" {
		return nil, false
	}
	inTmp, err := os.CreateTemp("", "vc-asr-*")
	if err != nil {
		return nil, false
	}
	defer os.Remove(inTmp.Name())
	if _, err := inTmp.Write(in); err != nil {
		inTmp.Close()
		return nil, false
	}
	inTmp.Close()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.ffmpeg, "-hide_banner", "-loglevel", "error",
		"-i", inTmp.Name(), "-ac", "1", "-ar", "16000", "-f", "s16le", "pipe:1")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil || out.Len() == 0 {
		return nil, false
	}
	return out.Bytes(), true
}

// concatToWAV joins reference clips into a single mono WAV so the cloned voice
// is enrolled from a longer, timbre-richer reference (one short take
// under-specifies the speaker for ICL). Each input is decoded and normalized to
// mono/24kHz before concatenation so clips of different formats/rates join
// cleanly. Returns (nil,false) on failure — the caller then falls back to the
// single-clip path.
func (e *Extension) concatToWAV(ctx context.Context, ins [][]byte) ([]byte, bool) {
	if e.ffmpeg == "" || len(ins) < 2 {
		return nil, false
	}
	var tmps []string
	defer func() {
		for _, p := range tmps {
			os.Remove(p)
		}
	}()
	args := []string{"-hide_banner", "-loglevel", "error", "-y"}
	for _, in := range ins {
		t, err := os.CreateTemp("", "vc-c-*")
		if err != nil {
			return nil, false
		}
		tmps = append(tmps, t.Name())
		if _, err := t.Write(in); err != nil {
			t.Close()
			return nil, false
		}
		t.Close()
		args = append(args, "-i", t.Name())
	}
	outTmp, err := os.CreateTemp("", "vc-cat-*.wav")
	if err != nil {
		return nil, false
	}
	tmps = append(tmps, outTmp.Name())
	outTmp.Close()
	// Build the filter graph: normalize every input, then concat them in order.
	var fb strings.Builder
	for i := range ins {
		fmt.Fprintf(&fb, "[%d:a]aformat=sample_rates=24000:channel_layouts=mono[a%d];", i, i)
	}
	for i := range ins {
		fmt.Fprintf(&fb, "[a%d]", i)
	}
	fmt.Fprintf(&fb, "concat=n=%d:v=0:a=1[a]", len(ins))
	args = append(args, "-filter_complex", fb.String(), "-map", "[a]", outTmp.Name())
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, e.ffmpeg, args...).Run(); err != nil {
		return nil, false
	}
	out, err := os.ReadFile(outTmp.Name())
	if err != nil || len(out) <= 44 {
		return nil, false
	}
	return out, true
}

// Register mounts the routes. deps.TTSBase returns the live TTS base URL;
// voice cloning is stateless (no DB), so deps.DB/Ctx are unused.
func (e *Extension) Register(r core.RouteRegistrar, deps core.AdminDeps) {
	ttsBase := deps.TTSBase
	r.Private("POST /api/v1/tts/voices", e.upload(ttsBase))
	r.Private("DELETE /api/v1/tts/voices/{name}", e.delete(ttsBase))
	r.Private("POST /api/v1/tts/voices/preview", e.preview(ttsBase))
	r.Private("POST /api/v1/tts/voices/transcribe", e.transcribe())
}

// transcribe runs the uploaded reference audio through the SenseVoice ASR
// worker so the console can pre-fill the ICL reference text (which is what
// makes a clone actually resemble the speaker). Audio is decoded to 16k mono
// PCM here and posted to the worker's one-shot HTTP endpoint.
func (e *Extension) transcribe() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if e.asrHTTP == "" || e.ffmpeg == "" {
			fail(w, http.StatusServiceUnavailable, "transcription not available (asr/ffmpeg missing)")
			return
		}
		if err := req.ParseMultipartForm(maxUploadBytes); err != nil {
			fail(w, http.StatusBadRequest, "multipart parse: "+err.Error())
			return
		}
		file, hdr, err := req.FormFile("audio_sample")
		if err != nil {
			fail(w, http.StatusBadRequest, `missing "audio_sample" file field`)
			return
		}
		defer file.Close()
		if hdr.Size > maxUploadBytes {
			fail(w, http.StatusRequestEntityTooLarge, "audio sample exceeds 20MB")
			return
		}
		raw, err := io.ReadAll(io.LimitReader(file, maxUploadBytes+1))
		if err != nil || len(raw) > maxUploadBytes {
			fail(w, http.StatusRequestEntityTooLarge, "audio sample exceeds 20MB")
			return
		}
		pcm, decoded := e.transcodePCM16k(req.Context(), raw)
		if !decoded {
			fail(w, http.StatusBadRequest, "could not decode audio")
			return
		}
		ctx, cancel := context.WithTimeout(req.Context(), 60*time.Second)
		defer cancel()
		areq, err := http.NewRequestWithContext(ctx, http.MethodPost, e.asrHTTP+"/transcribe", bytes.NewReader(pcm))
		if err != nil {
			fail(w, http.StatusBadGateway, "asr: "+err.Error())
			return
		}
		areq.Header.Set("Content-Type", "application/octet-stream")
		resp, err := e.http.Do(areq)
		if err != nil {
			fail(w, http.StatusBadGateway, "asr service unreachable: "+err.Error())
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode != http.StatusOK {
			fail(w, http.StatusBadGateway, "asr: "+upstreamMsg(resp.StatusCode, body))
			return
		}
		var out struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			fail(w, http.StatusBadGateway, "asr: bad response: "+err.Error())
			return
		}
		ok(w, map[string]any{"text": strings.TrimSpace(out.Text)})
	}
}

// upload streams a multipart voice upload to the TTS service. It re-encodes the
// form (rather than proxying the raw body) so it can validate every field and
// reject bad requests before they ever reach the GPU.
func (e *Extension) upload(ttsBase func() string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		base := ttsBase()
		if base == "" {
			fail(w, http.StatusServiceUnavailable, "tts service not configured")
			return
		}
		if err := req.ParseMultipartForm(maxUploadBytes); err != nil {
			fail(w, http.StatusBadRequest, "multipart parse: "+err.Error())
			return
		}
		name := strings.TrimSpace(req.FormValue("name"))
		if !nameRe.MatchString(name) {
			fail(w, http.StatusBadRequest, "name must be 2-40 chars of a-z 0-9 _ -")
			return
		}
		consent := strings.TrimSpace(req.FormValue("consent"))
		if consent == "" {
			fail(w, http.StatusBadRequest, "consent is required: confirm you are authorized to clone this voice")
			return
		}
		// One or more reference clips arrive under the same "audio_sample" field;
		// multiple short takes give the model broader timbre coverage than one
		// short sample. Read them all (in order), capped for safety.
		fhs := req.MultipartForm.File["audio_sample"]
		if len(fhs) == 0 {
			fail(w, http.StatusBadRequest, `missing "audio_sample" file field`)
			return
		}
		if len(fhs) > maxClips {
			fail(w, http.StatusBadRequest, fmt.Sprintf("at most %d reference clips", maxClips))
			return
		}
		var clips [][]byte
		for _, fh := range fhs {
			if fh.Size > maxUploadBytes {
				fail(w, http.StatusRequestEntityTooLarge, "an audio sample exceeds 20MB")
				return
			}
			f, err := fh.Open()
			if err != nil {
				fail(w, http.StatusBadRequest, "cannot read audio sample")
				return
			}
			b, err := io.ReadAll(io.LimitReader(f, maxUploadBytes+1))
			f.Close()
			if err != nil || len(b) > maxUploadBytes {
				fail(w, http.StatusRequestEntityTooLarge, "an audio sample exceeds 20MB")
				return
			}
			if len(b) > 0 {
				clips = append(clips, b)
			}
		}
		if len(clips) == 0 {
			fail(w, http.StatusBadRequest, "empty audio sample")
			return
		}

		// The TTS decoder (libsndfile) only reads wav/mp3/ogg/flac — phone
		// recordings (m4a/aac) and browser captures (webm/opus) fail. Transcode
		// to mono WAV with ffmpeg so any common format works; if ffmpeg is
		// missing or chokes, forward the original bytes unchanged. Multiple clips
		// are concatenated into one longer reference first.
		var raw []byte
		uploadName := "audio.wav"
		if len(clips) >= 2 {
			if merged, ok := e.concatToWAV(req.Context(), clips); ok {
				raw = merged
			} else if wav, ok := e.transcodeToWAV(req.Context(), clips[0]); ok {
				raw = wav
			} else {
				raw, uploadName = clips[0], fhs[0].Filename
			}
		} else if wav, ok := e.transcodeToWAV(req.Context(), clips[0]); ok {
			raw = wav
		} else {
			raw, uploadName = clips[0], fhs[0].Filename
		}

		// Re-encode the validated form for the upstream request.
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		part, err := mw.CreateFormFile("audio_sample", uploadName)
		if err != nil {
			fail(w, http.StatusInternalServerError, "encode upload: "+err.Error())
			return
		}
		if _, err := part.Write(raw); err != nil {
			fail(w, http.StatusInternalServerError, "encode upload: "+err.Error())
			return
		}
		mw.WriteField("name", name)
		mw.WriteField("consent", consent)
		if v := strings.TrimSpace(req.FormValue("ref_text")); v != "" {
			mw.WriteField("ref_text", v)
		}
		if v := strings.TrimSpace(req.FormValue("speaker_description")); v != "" {
			mw.WriteField("speaker_description", v)
		}
		mw.Close()

		ctx, cancel := context.WithTimeout(req.Context(), uploadTimeout)
		defer cancel()
		ureq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/audio/voices", &buf)
		if err != nil {
			fail(w, http.StatusBadGateway, "tts upload: "+err.Error())
			return
		}
		ureq.Header.Set("Content-Type", mw.FormDataContentType())
		resp, err := e.http.Do(ureq)
		if err != nil {
			fail(w, http.StatusBadGateway, "tts service unreachable: "+err.Error())
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		// The TTS service returns HTTP 200 even for soft failures (e.g. an
		// undecodable audio file), wrapping the real error in the body as
		// {"error":{message,code}}. Trust the body, not just the status.
		if rejected, status, msg := ttsRejected(resp.StatusCode, body); rejected {
			fail(w, status, "tts upload rejected: "+msg)
			return
		}
		ok(w, map[string]any{"name": name})
	}
}

// delete removes a custom voice. Built-in voices (those not in uploaded_voices)
// are protected: deleting one returns 403 rather than touching the TTS service.
func (e *Extension) delete(ttsBase func() string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		base := ttsBase()
		if base == "" {
			fail(w, http.StatusServiceUnavailable, "tts service not configured")
			return
		}
		name := req.PathValue("name")
		if !nameRe.MatchString(name) {
			fail(w, http.StatusBadRequest, "invalid voice name")
			return
		}
		uploaded, err := e.uploadedVoices(req.Context(), base)
		if err != nil {
			fail(w, http.StatusBadGateway, err.Error())
			return
		}
		if !contains(uploaded, name) {
			fail(w, http.StatusForbidden, "only custom (uploaded) voices can be deleted")
			return
		}
		ctx, cancel := context.WithTimeout(req.Context(), 10*time.Second)
		defer cancel()
		dreq, err := http.NewRequestWithContext(ctx, http.MethodDelete, base+"/v1/audio/voices/"+name, nil)
		if err != nil {
			fail(w, http.StatusBadGateway, "tts delete: "+err.Error())
			return
		}
		resp, err := e.http.Do(dreq)
		if err != nil {
			fail(w, http.StatusBadGateway, "tts service unreachable: "+err.Error())
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
			fail(w, http.StatusBadGateway, "tts delete rejected: "+upstreamMsg(resp.StatusCode, body))
			return
		}
		ok(w, map[string]any{"deleted": name})
	}
}

type previewReq struct {
	Text         string   `json:"text"`
	Voice        string   `json:"voice"`
	Seed         *int     `json:"seed"`         // nil -> previewSeed; "换一版" passes a varied seed
	Instructions string   `json:"instructions"` // optional style steer, e.g. "更沉稳"
	Speed        *float64 `json:"speed"`        // nil -> 1.0; clamped to [0.5, 2.0]
}

// preview synthesizes a short sample with the given voice and streams it back
// as audio/wav for the console's <audio> element.
func (e *Extension) preview(ttsBase func() string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		base := ttsBase()
		if base == "" {
			fail(w, http.StatusServiceUnavailable, "tts service not configured")
			return
		}
		var in previewReq
		if err := json.NewDecoder(io.LimitReader(req.Body, 1<<16)).Decode(&in); err != nil {
			fail(w, http.StatusBadRequest, "bad json: "+err.Error())
			return
		}
		in.Text = strings.TrimSpace(in.Text)
		in.Voice = strings.TrimSpace(in.Voice)
		if in.Text == "" || in.Voice == "" {
			fail(w, http.StatusBadRequest, "text and voice are required")
			return
		}
		if len([]rune(in.Text)) > 200 {
			fail(w, http.StatusBadRequest, "preview text must be <= 200 chars")
			return
		}
		seed := previewSeed
		if in.Seed != nil {
			seed = *in.Seed
		}
		speed := 1.0
		if in.Speed != nil {
			speed = *in.Speed
			if speed < 0.5 {
				speed = 0.5
			} else if speed > 2.0 {
				speed = 2.0
			}
		}
		body := map[string]any{
			"input":           in.Text,
			"voice":           in.Voice,
			"response_format": "wav",
			// Qwen3-TTS samples a different rendition every call unless a seed
			// is fixed; pin it so auditioning the same voice+text is stable
			// (otherwise the timbre appears to "change every time"). "换一版"
			// passes a different seed to hear an alternate rendition.
			"seed":  seed,
			"speed": speed,
		}
		// NB: do NOT send x_vector_only_mode here — it's only valid for the
		// "Base" task and the TTS rejects it for CustomVoice. ICL high-fidelity
		// is already the default for a custom voice enrolled with ref_text.
		if s := strings.TrimSpace(in.Instructions); s != "" {
			body["instructions"] = s
		}
		payload, _ := json.Marshal(body)
		ctx, cancel := context.WithTimeout(req.Context(), previewTimeout)
		defer cancel()
		sreq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/audio/speech", bytes.NewReader(payload))
		if err != nil {
			fail(w, http.StatusBadGateway, "tts speech: "+err.Error())
			return
		}
		sreq.Header.Set("Content-Type", "application/json")
		resp, err := e.http.Do(sreq)
		if err != nil {
			fail(w, http.StatusBadGateway, "tts service unreachable: "+err.Error())
			return
		}
		defer resp.Body.Close()
		// The TTS service wraps errors in a 200 JSON body, so a non-audio
		// content-type means failure even at HTTP 200 — never stream that to
		// the browser as audio.
		ct := resp.Header.Get("Content-Type")
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(ct, "audio/") {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			_, status, msg := ttsRejected(resp.StatusCode, body)
			if status == 0 {
				status = http.StatusBadGateway
			}
			if msg == "" {
				msg = upstreamMsg(resp.StatusCode, body)
			}
			fail(w, status, "tts speech rejected: "+msg)
			return
		}
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		io.Copy(w, resp.Body)
	}
}

// uploadedVoices fetches the TTS service's custom-voice list.
func (e *Extension) uploadedVoices(ctx context.Context, base string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/audio/voices", nil)
	if err != nil {
		return nil, fmt.Errorf("tts voices: %w", err)
	}
	resp, err := e.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tts service unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tts voices: upstream status %d", resp.StatusCode)
	}
	var body struct {
		UploadedVoices []json.RawMessage `json:"uploaded_voices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("tts voices: bad upstream body: %w", err)
	}
	// uploaded_voices is [] when empty but an array of {name,...} objects once
	// custom voices exist; tolerate a bare-string element too.
	names := make([]string, 0, len(body.UploadedVoices))
	for _, raw := range body.UploadedVoices {
		if n := voiceName(raw); n != "" {
			names = append(names, n)
		}
	}
	return names, nil
}

// voiceName extracts a name from one uploaded_voices entry, tolerating both a
// bare string and a {name,...} object.
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

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// ttsRejected inspects a TTS JSON response that may carry an error despite a
// 200 status (the service wraps failures as {"error":{message,code}} or
// {"success":false}). Returns (true, mappedStatus, message) when the call
// failed. An inner 4xx code maps to 400 (the client's audio/params were the
// problem); anything else maps to 502 (upstream fault).
func ttsRejected(httpStatus int, body []byte) (bool, int, string) {
	var rb struct {
		Error *struct {
			Message string `json:"message"`
			Code    int    `json:"code"`
		} `json:"error"`
		Success *bool           `json:"success"`
		Voice   json.RawMessage `json:"voice"`
	}
	_ = json.Unmarshal(body, &rb)
	if rb.Error != nil {
		status := http.StatusBadGateway
		if rb.Error.Code >= 400 && rb.Error.Code < 500 {
			status = http.StatusBadRequest
		}
		msg := rb.Error.Message
		if msg == "" {
			msg = upstreamMsg(httpStatus, body)
		}
		return true, status, msg
	}
	if rb.Success != nil && !*rb.Success {
		return true, http.StatusBadGateway, upstreamMsg(httpStatus, body)
	}
	if httpStatus != http.StatusOK && httpStatus != http.StatusCreated {
		return true, http.StatusBadGateway, upstreamMsg(httpStatus, body)
	}
	return false, 0, ""
}

// upstreamMsg renders a compact upstream error, preferring a JSON detail/message
// field when present, else the raw body, capped for safety.
func upstreamMsg(status int, body []byte) string {
	var j struct {
		Detail  any    `json:"detail"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(body, &j) == nil {
		if j.Message != "" {
			return j.Message
		}
		if j.Error != "" {
			return j.Error
		}
		if j.Detail != nil {
			return fmt.Sprintf("%v", j.Detail)
		}
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 300 {
		s = s[:300]
	}
	if s == "" {
		return fmt.Sprintf("HTTP %d", status)
	}
	return s
}

// ok/fail mirror the control plane's {code,data}/{code,msg} response contract
// so the console's shared client handles them uniformly.
func ok(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}

func fail(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"code": status, "msg": msg})
}
