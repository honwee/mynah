package instinct

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// JevConfig is what the Jev provider needs. BaseURL defaults to TypeSafe's
// endpoint; ProxyURL (optional) routes the HTTPS calls through an egress proxy
// — api.typesafe.ai answers 451 "not available in your region" from mainland
// China, so a deployment there needs one.
type JevConfig struct {
	BaseURL  string
	APIKey   string
	Model    string // "jev-latest"
	ProxyURL string
	Timeout  time.Duration
	RPM      int // per-key budget (TypeSafe: 1200/min); 0 = no local limit
}

// Jev calls POST {BaseURL}/v1/systemone. It carries a circuit breaker (3
// consecutive failures open it for 30 s) and a token bucket for the RPM
// budget; when open or empty it fails fast so the caller falls back to rules.
type Jev struct {
	cfg    JevConfig
	client *http.Client

	mu        sync.Mutex
	fails     int
	openUntil time.Time
	tokens    float64
	lastFill  time.Time
}

const (
	breakerTrip = 3
	breakerHold = 30 * time.Second
)

func NewJev(cfg JevConfig) (*Jev, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("jev: api key missing")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.typesafe.ai"
	}
	if cfg.Model == "" {
		cfg.Model = "jev-latest"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 1500 * time.Millisecond
	}
	tr := &http.Transport{ForceAttemptHTTP2: true, MaxIdleConns: 4, IdleConnTimeout: 90 * time.Second}
	if cfg.ProxyURL != "" {
		pu, err := url.Parse(cfg.ProxyURL)
		if err != nil {
			return nil, fmt.Errorf("jev: bad proxy url: %w", err)
		}
		tr.Proxy = http.ProxyURL(pu)
	}
	return &Jev{cfg: cfg, client: &http.Client{Transport: tr, Timeout: cfg.Timeout},
		tokens: float64(cfg.RPM), lastFill: time.Now()}, nil
}

func (j *Jev) Name() string { return "jev" }

// Warm opens the TLS/HTTP2 connection ahead of the first decision (a cold
// handshake through a proxy can eat the whole 1.5 s budget). Fire-and-forget.
func (j *Jev) Warm(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, j.cfg.BaseURL+"/", nil)
	if err != nil {
		return
	}
	resp, err := j.client.Do(req)
	if err == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
	}
}

// admit applies breaker + token bucket; returns a reason when the call must
// not be attempted.
func (j *Jev) admit(now time.Time) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if now.Before(j.openUntil) {
		return fmt.Errorf("jev: circuit open for %s", j.openUntil.Sub(now).Round(time.Second))
	}
	if j.cfg.RPM > 0 {
		j.tokens += now.Sub(j.lastFill).Minutes() * float64(j.cfg.RPM)
		if j.tokens > float64(j.cfg.RPM) {
			j.tokens = float64(j.cfg.RPM)
		}
		j.lastFill = now
		if j.tokens < 1 {
			return errors.New("jev: rpm budget exhausted")
		}
		j.tokens--
	}
	return nil
}

func (j *Jev) record(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err == nil {
		j.fails = 0
		return
	}
	j.fails++
	if j.fails >= breakerTrip {
		j.openUntil = time.Now().Add(breakerHold)
		j.fails = 0
	}
}

type jevReq struct {
	Model     string              `json:"model"`
	State     string              `json:"state"`
	Questions map[string]Question `json:"questions"`
}

type jevResp struct {
	Answers map[string]Answer `json:"answers"`
	Title   string            `json:"title,omitempty"`
	Status  int               `json:"status,omitempty"`
	Error   any               `json:"error,omitempty"`
}

func (j *Jev) Decide(ctx context.Context, state string, questions map[string]Question) (map[string]Answer, error) {
	if err := j.admit(time.Now()); err != nil {
		return nil, err
	}
	body, _ := json.Marshal(jevReq{Model: j.cfg.Model, State: state, Questions: questions})
	ctx, cancel := context.WithTimeout(ctx, j.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, j.cfg.BaseURL+"/v1/systemone", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+j.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := j.client.Do(req)
	if err != nil {
		j.record(err)
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusTooManyRequests {
		err := errors.New("jev: 429 rate limited")
		j.record(err)
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		var jr jevResp
		_ = json.Unmarshal(raw, &jr)
		msg := jr.Title
		if msg == "" {
			msg = string(raw)
			if len(msg) > 160 {
				msg = msg[:160]
			}
		}
		err := fmt.Errorf("jev: http %d: %s", resp.StatusCode, msg)
		j.record(err)
		return nil, err
	}
	var jr jevResp
	if err := json.Unmarshal(raw, &jr); err != nil {
		j.record(err)
		return nil, fmt.Errorf("jev: bad json: %w", err)
	}
	j.record(nil)
	if jr.Answers == nil {
		jr.Answers = map[string]Answer{}
	}
	return jr.Answers, nil
}
