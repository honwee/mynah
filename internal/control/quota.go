// Concurrency quota against a MIXED worker pool.
//
// With a single-engine pool the rule was one line: ∑ enabled channels'
// max_concurrent ≤ pool size. That is no longer sufficient. A channel whose
// avatar is a baked directory can ONLY be served by a musetalk worker, so a
// flashhead-heavy pool can satisfy the total while starving it — the publish
// would pass and visitors would meet a 503 instead.
//
// The correct condition for this structure (each engine-bound channel may use
// exactly one engine's workers; each unbound channel may use any) is Hall's
// condition, which here collapses to two inequalities:
//
//	for each engine E:  ∑ max_concurrent of channels bound to E  ≤  workers[E]
//	overall:            ∑ max_concurrent of all enabled channels ≤  ∑ workers
//
// Both must hold. Neither implies the other: two flashhead channels at 1 each
// pass the total against a 2×musetalk pool, and one musetalk channel at 1 passes
// the per-engine check against a pool of exactly one musetalk while a second
// unbound channel busts the total.
package control

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"mynah/core"
	"mynah/internal/avatarcatalog"
	"mynah/internal/avatarengine"
)

// quotaOverride is the not-yet-saved channel a check is being run for.
type quotaOverride struct {
	// ID is the channel being edited; 0 means a channel being created.
	ID int64
	// MaxConcurrent is the allocation it is asking for.
	MaxConcurrent int
	// Avatar is its frozen avatar id, consulted only when ID == 0. For an
	// existing channel the stored avatar is authoritative — the edit paths that
	// touch concurrency never change the avatar.
	Avatar string
}

// channelDemand is one enabled channel's claim on the pool.
type channelDemand struct {
	slug   string
	want   int
	engine string // "" = not bound to any engine (any worker will do)
}

// poolDemand reads the enabled channels' allocations and derives each one's
// engine the same way the channel registry does at dispatch time
// (avatarcatalog.EngineFor), so the quota answers the question sessions will
// actually ask.
func (s *Server) poolDemand(ctx context.Context, override *quotaOverride) ([]channelDemand, error) {
	if s.deps.DB == nil {
		return nil, nil
	}
	rows, err := s.deps.DB.Pool.Query(ctx,
		`SELECT id, slug, max_concurrent, COALESCE(config->>'avatar', '')
		   FROM channels WHERE enabled`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []channelDemand
	replaced := false
	overrideAvatar := ""
	if override != nil {
		overrideAvatar = override.Avatar
	}
	for rows.Next() {
		var id int64
		var slug, avatar string
		var want int
		if err := rows.Scan(&id, &slug, &want, &avatar); err != nil {
			return nil, err
		}
		if override != nil && override.ID != 0 && override.ID == id {
			want = override.MaxConcurrent
			replaced = true
		}
		out = append(out, channelDemand{slug: slug, want: want,
			engine: s.engineForChannel(avatar)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// A channel being created, or an existing one being enabled for the first
	// time, has no enabled row to replace — its demand is additional. In the
	// latter case the caller does not know the avatar (enabling a channel does
	// not change it), so read the stored one rather than treating the channel as
	// unbound, which would let it pass a per-engine check it should fail.
	if override != nil && !replaced {
		if override.ID != 0 && overrideAvatar == "" {
			_ = s.deps.DB.Pool.QueryRow(ctx,
				`SELECT COALESCE(config->>'avatar', '') FROM channels WHERE id = $1`,
				override.ID).Scan(&overrideAvatar)
		}
		out = append(out, channelDemand{slug: "（本频道）", want: override.MaxConcurrent,
			engine: s.engineForChannel(overrideAvatar)})
	}
	return out, nil
}

// engineForChannel is the engine a channel's sessions will demand, for a channel
// whose stored avatar is avatar ("" = never chose one).
//
// The fallback matters: a channel with no avatar of its own does not become
// engine-free at dispatch, it inherits the console's live global selection
// (session.create does exactly this). Counting it as unbound would let it satisfy
// its quota against workers of an engine it can never land on. Only when the
// global selection is itself unattributed — the shipped "default" — is the
// channel genuinely free to use any worker.
func (s *Server) engineForChannel(avatar string) string {
	if e := avatarcatalog.EngineFor(avatar); e != "" {
		return e
	}
	if strings.TrimSpace(avatar) != "" {
		return "" // an avatar was chosen; it just carries no engine intent
	}
	if s.deps.Config == nil {
		return ""
	}
	return avatarcatalog.EngineFor(s.deps.Config.Current().Avatar.Current)
}

// liveComposition counts the DISPATCH pool by engine — what cored can actually
// route to right now, read off each worker's health banner. Workers whose banner
// is unrecognized are counted under "" so they still contribute to the total but
// can never satisfy an engine-bound channel.
//
// This is deliberately the dispatch view rather than the docker view: a
// container that exists but has not finished loading its model cannot serve a
// visitor, and promising concurrency against it is how a publish passes and the
// first visitor gets a 503.
func (s *Server) liveComposition() (map[string]int, int) {
	rep, isReporter := s.deps.Sessions.(core.WorkerReporter)
	if !isReporter {
		return nil, 0
	}
	workers := rep.ListWorkers()
	byEngine := map[string]int{}
	for _, w := range workers {
		byEngine[engineFromDetail(w.Detail)]++
	}
	return byEngine, len(workers)
}

// checkQuotaForComposition refuses a composition that cannot serve the channels
// already published against it. Fail CLOSED: retiring a worker is not undone by
// retrying, unlike a refused publish.
func (s *Server) checkQuotaForComposition(ctx context.Context, workers map[string]int) string {
	if s.deps.DB == nil {
		return ""
	}
	demand, err := s.poolDemand(ctx, nil)
	if err != nil {
		logErr("pool composition quota query", err)
		return "无法核对频道并发配额，出于安全考虑拒绝调整引擎池"
	}
	return poolFeasible(workers, demand)
}

// poolFeasible reports why the composition cannot serve the demand, or "" when
// it can. Pure so both call sites (publish, shrink) share one definition of the
// rule and one wording.
func poolFeasible(workers map[string]int, demand []channelDemand) string {
	total := 0
	for _, n := range workers {
		total += n
	}
	perEngine := map[string]int{}
	sum := 0
	for _, d := range demand {
		sum += d.want
		if d.engine != "" {
			perEngine[d.engine] += d.want
		}
	}

	// Per-engine first: its message names the actual obstacle, whereas the total
	// would report a vaguer "not enough workers" for the same situation.
	engines := make([]string, 0, len(perEngine))
	for e := range perEngine {
		engines = append(engines, e)
	}
	sort.Strings(engines)
	for _, e := range engines {
		need, have := perEngine[e], workers[e]
		if need <= have {
			continue
		}
		label := e
		if eng, found := avatarengine.Get(e); found {
			label = eng.Label
		}
		var bound []string
		for _, d := range demand {
			if d.engine == e {
				bound = append(bound, fmt.Sprintf("%s×%d", d.slug, d.want))
			}
		}
		sort.Strings(bound)
		if have == 0 {
			return fmt.Sprintf("绑定 %s 的频道需要 %d 路并发（%v），但池里没有 %s worker。"+
				"这类频道的形象只有 %s 能渲染，其他引擎的空闲 worker 帮不上。",
				label, need, bound, label, label)
		}
		return fmt.Sprintf("%s 并发不足：绑定它的频道共需 %d 路（%v），池里只有 %d 路。"+
			"这类频道的形象只有 %s 能渲染，加别的引擎不解决。",
			label, need, bound, have, label)
	}

	if sum > total {
		return fmt.Sprintf("并发总额不足：在线频道共需 %d 路，池里共 %d 路 worker（%s）。"+
			"可调低或下线部分频道，或扩池。", sum, total, describeComposition(workers))
	}
	return ""
}

// describeComposition renders {"musetalk":2,"flashhead":1} as a stable string.
func describeComposition(workers map[string]int) string {
	if len(workers) == 0 {
		return "空池"
	}
	names := make([]string, 0, len(workers))
	for e := range workers {
		names = append(names, e)
	}
	sort.Strings(names)
	var parts []string
	for _, e := range names {
		if workers[e] <= 0 {
			continue
		}
		label := e
		if e == "" {
			label = "引擎未识别"
		} else if eng, found := avatarengine.Get(e); found {
			label = eng.Label
		}
		parts = append(parts, fmt.Sprintf("%s %d 路", label, workers[e]))
	}
	if len(parts) == 0 {
		return "空池"
	}
	return joinCN(parts)
}

func joinCN(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " + "
		}
		out += p
	}
	return out
}
