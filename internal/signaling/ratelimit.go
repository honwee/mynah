package signaling

import (
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

// ratelimit is a small per-IP token-bucket guard for the visitor endpoints.
// Every /offer costs GPU-backed session setup, so an unauthenticated public
// deployment must not accept unbounded request volume. Defaults are generous
// for legitimate use (a visitor posts one offer and a few /human turns per
// minute) and tunable via flags; --visitor-rate-limit=0 disables the guard.
//
// This protects the REQUEST path only. The deeper resource bound — how many
// concurrent sessions the avatar worker serves — is enforced by the worker
// itself (one session at a time) and by channel max_concurrent. Operators
// exposing cored directly to the internet should still front it with their
// own gateway; see SECURITY.md.
type ratelimit struct {
	perMin int // sustained tokens per minute per IP
	burst  int
	mu     sync.Mutex
	bkts   map[string]*bucket
	sweep  time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newRatelimit(perMin int) *ratelimit {
	if perMin <= 0 {
		return nil
	}
	return &ratelimit{perMin: perMin, burst: perMin, bkts: map[string]*bucket{}, sweep: time.Now()}
}

// allow reports whether one request from addr may proceed.
func (r *ratelimit) allow(addr string) bool {
	ip, _, err := net.SplitHostPort(addr)
	if err != nil {
		ip = addr
	}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	// Amortized sweep: drop buckets idle > 10 min so the map stays bounded.
	if now.Sub(r.sweep) > 10*time.Minute {
		for k, b := range r.bkts {
			if now.Sub(b.last) > 10*time.Minute {
				delete(r.bkts, k)
			}
		}
		r.sweep = now
	}
	b := r.bkts[ip]
	if b == nil {
		b = &bucket{tokens: float64(r.burst), last: now}
		r.bkts[ip] = b
	}
	b.tokens += now.Sub(b.last).Minutes() * float64(r.perMin)
	if b.tokens > float64(r.burst) {
		b.tokens = float64(r.burst)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// guard wraps h with the per-IP limit (429 on exceed). nil receiver = no-op.
func (r *ratelimit) guard(h http.HandlerFunc) http.HandlerFunc {
	if r == nil {
		return h
	}
	return func(w http.ResponseWriter, req *http.Request) {
		if !r.allow(req.RemoteAddr) {
			log.Printf("signaling: rate-limited %s %s from %s", req.Method, req.URL.Path, req.RemoteAddr)
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		h(w, req)
	}
}
