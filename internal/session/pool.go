// Dynamic worker pool: cored discovers avatar-engine workers by probing a port
// range instead of being handed a fixed address list at startup.
//
// Why this exists: --worker is parsed once at boot, so growing or shrinking the
// pool (or switching engines) meant restarting cored and dropping every live
// session. With a port range, the supervisor can create/remove worker
// containers and cored converges on its own.
//
// Three rules shape the reconciler, each of them a way this can go wrong:
//
//  1. NEVER evict a busy worker. A worker whose health probe fails while it is
//     serving a session is marked draining, not removed: it keeps its session
//     until the visitor leaves. Dropping it would kill a live call to tidy up
//     bookkeeping.
//  2. Require consecutive failures before evicting. A single missed probe
//     (worker mid-GC, socket hiccup) must not shrink the pool — that would
//     silently reduce the concurrency quota.
//  3. Re-dial rather than reuse on recovery. A gRPC client whose target died
//     can wedge; when an address comes back it gets a fresh connection and the
//     old one is closed.
package session

import (
	"context"
	"fmt"
	"log"
	"time"

	pb "mynah/gen/go/proto/avatarengine/v1"
	"mynah/internal/engineclient"
)

// PoolSpec is the address range cored watches for workers.
type PoolSpec struct {
	Host      string
	PortStart int
	PortEnd   int
	// ProbeInterval is how often every candidate address is checked. 5s keeps
	// scale-up responsive without turning health probes into load: the whole
	// range is at most a handful of addresses.
	ProbeInterval time.Duration
	// FailuresBeforeEvict guards against transient blips (see rule 2).
	FailuresBeforeEvict int
}

// Addrs expands the spec into candidate addresses.
func (p PoolSpec) Addrs() []string {
	var out []string
	for port := p.PortStart; port <= p.PortEnd; port++ {
		out = append(out, fmt.Sprintf("%s:%d", p.Host, port))
	}
	return out
}

func (p PoolSpec) withDefaults() PoolSpec {
	if p.ProbeInterval <= 0 {
		p.ProbeInterval = 5 * time.Second
	}
	if p.FailuresBeforeEvict <= 0 {
		p.FailuresBeforeEvict = 3
	}
	return p
}

// StartPoolReconciler runs the discovery loop until ctx ends. Safe to call once
// per Manager.
func (m *Manager) StartPoolReconciler(ctx context.Context, spec PoolSpec) {
	spec = spec.withDefaults()
	log.Printf("worker pool: watching %s:%d-%d every %s (evict after %d consecutive failures)",
		spec.Host, spec.PortStart, spec.PortEnd, spec.ProbeInterval, spec.FailuresBeforeEvict)
	go func() {
		fails := map[string]int{}
		t := time.NewTicker(spec.ProbeInterval)
		defer t.Stop()
		m.reconcileOnce(ctx, spec, fails) // converge immediately, don't wait a tick
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.reconcileOnce(ctx, spec, fails)
			}
		}
	}()
}

// reconcileOnce brings the live pool in line with what is actually answering.
func (m *Manager) reconcileOnce(ctx context.Context, spec PoolSpec, fails map[string]int) {
	for _, addr := range spec.Addrs() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		known := m.workerByAddr(addr)
		detail, err := probeHealth(ctx, known, addr)

		if err == nil {
			fails[addr] = 0
			if known == nil {
				m.addDiscoveredWorker(addr, detail)
			} else {
				m.clearDraining(addr)
			}
			continue
		}

		if known == nil {
			continue // absent and still absent: nothing to do
		}
		fails[addr]++
		if fails[addr] < spec.FailuresBeforeEvict {
			continue
		}
		m.evictOrDrain(addr, err)
	}
}

// probeHealth checks an address, dialing a throwaway client when the worker is
// not already in the pool.
func probeHealth(ctx context.Context, known *Worker, addr string) (string, error) {
	cli := knownClient(known)
	if cli == nil {
		var err error
		cli, err = engineclient.Dial(addr)
		if err != nil {
			return "", err
		}
		defer cli.Close()
	}
	pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rep, err := cli.AE.Health(pctx, &pb.HealthRequest{})
	if err != nil {
		return "", err
	}
	if !rep.Ready {
		return "", fmt.Errorf("worker not ready")
	}
	return rep.Detail, nil
}

func knownClient(w *Worker) *engineclient.Client {
	if w == nil {
		return nil
	}
	return w.Client
}

func (m *Manager) workerByAddr(addr string) *Worker {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, w := range m.workers {
		if w.Addr == addr {
			return w
		}
	}
	return nil
}

// addDiscoveredWorker joins a newly-answering address to the pool. A fresh
// client is dialed here rather than reusing the probe's (see rule 3).
func (m *Manager) addDiscoveredWorker(addr, detail string) {
	cli, err := engineclient.Dial(addr)
	if err != nil {
		log.Printf("worker pool: %s answered health but dial failed: %v", addr, err)
		return
	}
	m.mu.Lock()
	for _, w := range m.workers { // lost a race with another reconcile
		if w.Addr == addr {
			m.mu.Unlock()
			cli.Close()
			return
		}
	}
	m.workers = append(m.workers, NewWorker(addr, detail, cli))
	n := len(m.workers)
	m.mu.Unlock()
	log.Printf("worker pool: + %s (%s) — pool size now %d", addr, detail, n)
}

// evictOrDrain removes a dead worker, unless it is mid-session: killing a live
// call to tidy the pool is never the right trade (rule 1).
func (m *Manager) evictOrDrain(addr string, cause error) {
	m.mu.Lock()
	idx := -1
	for i, w := range m.workers {
		if w.Addr == addr {
			idx = i
			break
		}
	}
	if idx < 0 {
		m.mu.Unlock()
		return
	}
	w := m.workers[idx]
	if w.busy != "" {
		if !w.draining {
			w.draining = true
			log.Printf("worker pool: %s unhealthy but serving session %s — draining, "+
				"will remove when the session ends (%v)", addr, w.busy, cause)
		}
		m.mu.Unlock()
		return
	}
	m.workers = append(m.workers[:idx], m.workers[idx+1:]...)
	n := len(m.workers)
	m.mu.Unlock()

	w.Client.Close()
	log.Printf("worker pool: − %s (%v) — pool size now %d", addr, cause, n)
}

func (m *Manager) clearDraining(addr string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, w := range m.workers {
		if w.Addr == addr && w.draining {
			w.draining = false
			log.Printf("worker pool: %s healthy again, no longer draining", addr)
		}
	}
}

// reapDrained removes drained workers once their session is gone. Called from
// releaseWorker, which already holds m.mu.
func (m *Manager) reapDrainedLocked() {
	kept := m.workers[:0]
	for _, w := range m.workers {
		if w.draining && w.busy == "" {
			w.Client.Close()
			log.Printf("worker pool: − %s (drained)", w.Addr)
			continue
		}
		kept = append(kept, w)
	}
	m.workers = kept
}
