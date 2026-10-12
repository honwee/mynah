// Preloading an avatar across the worker pool.
//
// Binding an avatar to a channel is an ADMIN action; a visitor arriving is not.
// Loading a bake costs ~15s and the worker only reports Ready afterwards, so
// whoever triggers the load waits on a blank screen. Doing it here — at bind
// time — moves that cost to the person who expects it.
//
// It is broadcast to every worker that can RENDER the avatar rather than one,
// because dispatch picks whichever such worker is free: an avatar resident on
// only some of them would work intermittently, which is far harder to diagnose
// than a clean failure. "Every worker that can render it" used to mean the whole
// pool — under a mixed pool it means the ones running the avatar's engine, since
// MuseTalk handed a portrait (or FlashHead handed a bake directory) cannot load
// it and would drag the summary to a permanent partial failure.
package session

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	pb "mynah/gen/go/proto/avatarengine/v1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PreloadResult reports one worker's outcome.
type PreloadResult struct {
	Addr    string `json:"addr"`
	Ready   bool   `json:"ready"`
	Already bool   `json:"already"`
	LoadMS  int64  `json:"load_ms"`
	Detail  string `json:"detail"`
}

// PreloadAvatar asks every worker that can render avatar to make it resident.
//
// avatar is the worker-visible cond_image value (a bake directory or portrait
// path). engine scopes the broadcast the same way dispatch scopes worker
// selection (acquireWorker); "" means engine-agnostic and asks the whole pool.
// Returns per-worker results; the caller decides whether a partial success is
// acceptable. A worker whose engine does not implement the RPC at all counts as
// ready (nothing to warm — see below), while a Preload that runs and fails
// counts as cold.
func (m *Manager) PreloadAvatar(ctx context.Context, avatar, engine string) (ready, total int, reason string) {
	return PreloadSummary(m.preloadAvatarDetailed(ctx, avatar, engine))
}

// preloadAvatarDetailed is the per-worker view PreloadAvatar summarizes.
func (m *Manager) preloadAvatarDetailed(ctx context.Context, avatar, engine string) []PreloadResult {
	workers := m.workersForEngine(engine)

	results := make([]PreloadResult, len(workers))
	var wg sync.WaitGroup
	for i, w := range workers {
		wg.Add(1)
		go func(i int, w *Worker) {
			defer wg.Done()
			// Generous: a cold bake decode is ~15s and several may queue behind
			// each other on one worker.
			cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
			defer cancel()
			rep, err := w.Client.AE.Preload(cctx, &pb.PreloadRequest{Avatar: avatar})
			results[i] = preloadOutcome(w.Addr, rep, err)
		}(i, w)
	}
	wg.Wait()

	okN := 0
	for _, r := range results {
		if r.Ready {
			okN++
		}
	}
	log.Printf("preload avatar %q (engine %q): %d/%d workers ready", avatar, engine, okN, len(results))
	return results
}

// preloadOutcome turns one worker's RPC result into a PreloadResult.
//
// A worker that has no Preload METHOD is counted ready. FlashHead's server
// predates the RPC and loads its portrait inline in milliseconds, so there is no
// first-visitor stall to move — which is the only thing preloading buys.
// Reporting it as 「没有 worker 能加载该形象」 made every FlashHead bind warn about
// a pool that was in fact ready. Only Unimplemented gets this pass: a Preload
// that runs and fails, or a worker that is unreachable, still counts as cold.
func preloadOutcome(addr string, rep *pb.PreloadReply, err error) PreloadResult {
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			return PreloadResult{Addr: addr, Ready: true, Already: true,
				Detail: "该引擎无需预热（worker 未实现 Preload）"}
		}
		return PreloadResult{Addr: addr, Detail: err.Error()}
	}
	return PreloadResult{
		Addr: addr, Ready: rep.Ready, Already: rep.Already,
		LoadMS: rep.LoadMs, Detail: rep.Detail,
	}
}

// workersForEngine is the preload broadcast set: every worker that could be
// chosen to render this avatar.
//
// Same engine predicate as acquireWorker, minus busy/draining — preloading is a
// load, not a session, so a worker mid-session still needs the avatar resident
// for its NEXT one. engine == "" means engine-agnostic material and asks
// everyone, which is what dispatch does for such an avatar too.
func (m *Manager) workersForEngine(engine string) []*Worker {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Worker
	for _, w := range m.workers {
		if engine == "" || w.Engine == engine {
			out = append(out, w)
		}
	}
	return out
}

// PreloadSummary condenses per-worker results into something an operator can
// act on: how many workers can now serve the avatar, and why the rest cannot.
func PreloadSummary(results []PreloadResult) (ready int, total int, reason string) {
	total = len(results)
	// No worker was even asked: the avatar's engine is not in the pool. Distinct
	// from failure and NOT success — the ready==total branch below would call an
	// empty broadcast fully warmed, which is how "0/0 workers ready" turns into a
	// silent green tick on a channel no worker can serve.
	if total == 0 {
		return 0, 0, "池里没有能渲染该形象的 worker（该引擎未部署）"
	}
	var firstErr string
	for _, r := range results {
		if r.Ready {
			ready++
			continue
		}
		if firstErr == "" && r.Detail != "" {
			firstErr = r.Detail
		}
	}
	if ready == total {
		return ready, total, ""
	}
	if ready == 0 {
		return ready, total, fmt.Sprintf("没有 worker 能加载该形象：%s", firstErr)
	}
	return ready, total, fmt.Sprintf(
		"只有 %d/%d 个 worker 加载成功；未成功的会话会落到没有该形象的 worker 上（%s）",
		ready, total, firstErr)
}
