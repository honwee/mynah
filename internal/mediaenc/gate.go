package mediaenc

import (
	"log"
	"sync"
	"time"
)

// Gate is a start barrier shared by the video + audio encoders. Each pace loop
// calls Arrive() once it has its first real sample; Gate releases them all at a
// single instant t0 and returns that shared origin. Pacing both tracks off the
// same t0 (with absolute per-sample deadlines) makes video-frame-0 and
// audio-packet-0 map to the same wall-clock time, so the browser's RTCP-driven
// A/V sync plays lips and voice together.
type Gate struct {
	mu      sync.Mutex
	ready   int
	need    int
	done    chan struct{}
	once    sync.Once
	created time.Time
	t0      time.Time
}

func NewGate(need int) *Gate {
	return &Gate{need: need, done: make(chan struct{}), created: time.Now()}
}

func (g *Gate) fire() {
	g.once.Do(func() {
		g.t0 = time.Now()
		close(g.done)
	})
}

// Arrive blocks until every participant has arrived (or a safety timeout, so one
// stalled track can't hang the other), then returns the shared origin t0. label
// identifies the caller in the timing log.
func (g *Gate) Arrive(label string) time.Time {
	log.Printf("[gate] %s arrived +%dms", label, time.Since(g.created).Milliseconds())
	g.mu.Lock()
	g.ready++
	n := g.ready
	g.mu.Unlock()
	if n >= g.need {
		g.fire()
	}
	select {
	case <-g.done:
	case <-time.After(1500 * time.Millisecond):
		g.fire()
	}
	log.Printf("[gate] %s released (t0 +%dms)", label, time.Since(g.created).Milliseconds())
	return g.t0
}
