package control

import (
	"mynah/core"

	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// HealthTargets lists every dependency cored was assembled with; empty
// fields are reported as "disabled" rather than probed.
type HealthTargets struct {
	AvatarAddr string // gRPC worker(s), TCP dial check; comma list = pool, ok when all up
	AsrAddr    string // gRPC worker, TCP dial check
	TTSURL     string // HTTP base
	LLMURL     string // OpenAI-compatible base (also covers ollama)
	OllamaURL  string // embedding endpoint (RAG); often same as LLMURL
}

type probeResult struct {
	Status string `json:"status"` // ok | down | disabled
	// No omitempty: local probes legitimately measure 0ms, and dropping the
	// field made the console render "undefined ms".
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

// probeAvatar reports the avatar engine's health.
//
// With a static --worker list the addresses are known up front and TCP-dialed.
// With --worker-pool they are NOT: membership is discovered at runtime, so
// there is no fixed list to dial and the flag's unused default points at a port
// nothing listens on. Probing that reported the engine as down while a healthy
// pool was serving traffic — the dashboard contradicted its own worker list.
//
// So in pool mode the truth is the pool itself: any member means the engine can
// serve; an empty pool means it cannot.
func (s *Server) probeAvatar(ctx context.Context, addr string) probeResult {
	if addr != "" {
		return probeTCPMulti(ctx, addr)
	}
	rep, isReporter := s.deps.Sessions.(core.WorkerReporter)
	if !isReporter {
		return probeResult{Status: "disabled"}
	}
	n := len(rep.ListWorkers())
	if n == 0 {
		return probeResult{Status: "down",
			Error: "引擎池为空：没有 worker 在线（新 worker 加载模型约需 50 秒）"}
	}
	return probeResult{Status: "ok"}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	t := s.deps.Health
	checks := map[string]func() probeResult{
		"avatar": func() probeResult { return s.probeAvatar(ctx, t.AvatarAddr) },
		"asr":    func() probeResult { return probeTCP(ctx, t.AsrAddr) },
		"tts":    func() probeResult { return probeHTTP(ctx, t.TTSURL) },
		"llm":    func() probeResult { return probeHTTP(ctx, t.LLMURL) },
		"ollama": func() probeResult { return probeHTTP(ctx, t.OllamaURL) },
		"postgres": func() probeResult {
			if s.deps.DB == nil {
				return probeResult{Status: "disabled"}
			}
			start := time.Now()
			if err := s.deps.DB.Pool.Ping(ctx); err != nil {
				return probeResult{Status: "down", Error: err.Error()}
			}
			return probeResult{Status: "ok", LatencyMS: time.Since(start).Milliseconds()}
		},
	}

	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		out = make(map[string]probeResult, len(checks))
	)
	for name, probe := range checks {
		wg.Add(1)
		go func(name string, probe func() probeResult) {
			defer wg.Done()
			res := probe()
			mu.Lock()
			out[name] = res
			mu.Unlock()
		}(name, probe)
	}
	wg.Wait()

	overall := "ok"
	for _, res := range out {
		if res.Status == "down" {
			overall = "degraded"
			break
		}
	}
	ok(w, map[string]any{"status": overall, "components": out})
}

func probeTCP(ctx context.Context, addr string) probeResult {
	if addr == "" {
		return probeResult{Status: "disabled"}
	}
	start := time.Now()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return probeResult{Status: "down", Error: err.Error()}
	}
	conn.Close()
	return probeResult{Status: "ok", LatencyMS: time.Since(start).Milliseconds()}
}

// probeTCPMulti probes a comma-separated address pool (the multi-worker
// --worker flag): ok only when every member answers, so a half-dead pool
// still shows as degraded.
func probeTCPMulti(ctx context.Context, addrs string) probeResult {
	if addrs == "" {
		return probeResult{Status: "disabled"}
	}
	var worst probeResult
	for _, addr := range strings.Split(addrs, ",") {
		if addr = strings.TrimSpace(addr); addr == "" {
			continue
		}
		res := probeTCP(ctx, addr)
		if res.Status == "down" {
			res.Error = addr + ": " + res.Error
			return res
		}
		worst = res
	}
	return worst
}

func probeHTTP(ctx context.Context, base string) probeResult {
	if base == "" {
		return probeResult{Status: "disabled"}
	}
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base, nil)
	if err != nil {
		return probeResult{Status: "down", Error: err.Error()}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return probeResult{Status: "down", Error: err.Error()}
	}
	resp.Body.Close()
	// Any HTTP response (even 404 on the bare base URL) proves the service
	// is up; we only care about reachability here.
	if resp.StatusCode >= 500 {
		return probeResult{Status: "down", Error: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}
	return probeResult{Status: "ok", LatencyMS: time.Since(start).Milliseconds()}
}
