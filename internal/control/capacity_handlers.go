package control

import (
	"net/http"
	"sort"
	"strings"

	"mynah/core"
	"mynah/internal/avatarengine"
	"mynah/internal/gpu"
)

// registerCapacity mounts the read-only avatar-pool capacity view: how much
// VRAM is free, what each engine costs, what the pool is currently composed of,
// and how the published channels' concurrency is allocated across it.
//
// Read-only: composing the pool is POST /api/v1/avatar/pool. This endpoint is
// what the console reads before and after, so every number it reports is one the
// mutating path also enforces — the capacity view and the 409 must never
// disagree about the arithmetic.
func (s *Server) registerCapacity() {
	s.private("GET /api/v1/avatar/capacity", s.handleAvatarCapacity)
}

func (s *Server) handleAvatarCapacity(w http.ResponseWriter, r *http.Request) {
	snap := gpu.Read(r.Context())

	// Which pids belong to the pool (their memory is reclaimable on resize), and
	// what the pool is composed of. Membership comes from the engine attribution,
	// never from the container name: a name prefix check would have missed every
	// flashhead worker and counted its VRAM against the pool's own budget.
	poolPIDs := map[int]bool{}
	poolRunning := 0
	composition := map[string]int{}
	if s.deps.Supervisor != nil {
		if list, err := s.deps.Supervisor.List(r.Context()); err == nil {
			inv := currentComposition(list)
			composition = countByEngine(inv)
			for _, wkr := range inv {
				if wkr.Running {
					poolRunning++
				}
			}
			member := map[string]bool{}
			for _, wkr := range inv {
				member[wkr.Name] = true
			}
			for _, svc := range list {
				if !member[svc.Name] {
					continue
				}
				// Main pid AND its children: memory held by a forked worker is
				// just as reclaimable, and leaving it out counts the pool's own
				// VRAM as someone else's — which makes the pool unable to grow.
				if svc.HostPID > 0 {
					poolPIDs[svc.HostPID] = true
				}
				for _, p := range svc.HostPIDs {
					poolPIDs[p] = true
				}
			}
		}
	}

	// Build the per-device picture the planner consumes: everything NOT held by
	// the pool counts as occupied.
	var gpus []avatarengine.GPU
	devices := make([]map[string]any, 0, len(snap.Devices))
	for _, d := range snap.Devices {
		byPool := snap.UsedByMB(d.Index, poolPIDs)
		others := d.UsedMB - byPool
		if others < 0 {
			others = 0
		}
		gpus = append(gpus, avatarengine.GPU{
			Index: d.Index, TotalMB: d.TotalMB, UsedByOthersMB: others,
		})
		devices = append(devices, map[string]any{
			"index": d.Index, "name": d.Name,
			"total_mb": d.TotalMB, "used_mb": d.UsedMB, "free_mb": d.FreeMB,
			"used_by_pool_mb":   byPool,
			"used_by_others_mb": others,
			"reserve_mb":        avatarengine.ReserveMB(d.TotalMB),
		})
	}

	// Capacity for every catalog engine, so the console can show what a switch
	// would buy before the switch itself exists.
	engines := make([]map[string]any, 0)
	for _, e := range avatarengine.All() {
		row := map[string]any{
			"name": e.Name, "label": e.Label,
			"vram_steady_mb": e.VRAMSteadyMB,
			"measured_at":    e.VRAMMeasuredAt,
			"cold_start_sec": e.ColdStartSec,
			"needs_bake":     e.NeedsBake,
			"notes":          e.Notes,
		}
		sel, why := e.Selectable()
		row["selectable"] = sel
		row["unavailable_reason"] = why
		if sel && snap.Available && len(gpus) > 0 {
			if plan, err := avatarengine.Plan(e, gpus); err == nil {
				row["max_workers"] = plan.Total
				row["per_gpu"] = plan.PerGPU
				row["explain"] = plan.Explain
			}
		}
		engines = append(engines, row)
	}

	// The dispatch view: which engines cored can actually route to. Differs from
	// `composition` above (the docker view) while a worker is still loading its
	// model — the console needs both to explain "created but not yet serving".
	var poolEngines []string
	poolMixed := false
	live := map[string]int{}
	liveTotal := 0
	if rep, isReporter := s.deps.Sessions.(core.WorkerReporter); isReporter {
		poolEngines, poolMixed = poolEngineMix(rep.ListWorkers())
		live, liveTotal = s.liveComposition()
	}

	// Per-engine quota: how much concurrency the published channels have claimed
	// from each engine, against how many workers of it are dispatchable. This is
	// the arithmetic that decides whether a publish is accepted, so showing it is
	// what turns a 409 into something an operator can act on.
	perEngine := make([]map[string]any, 0)
	unbound := []string{}
	allocated := 0
	if demand, err := s.poolDemand(r.Context(), nil); err == nil {
		byEngine := map[string]int{}
		chans := map[string][]string{}
		for _, d := range demand {
			allocated += d.want
			if d.engine == "" {
				unbound = append(unbound, d.slug)
				continue
			}
			byEngine[d.engine] += d.want
			chans[d.engine] = append(chans[d.engine], d.slug)
		}
		// Every engine that either has workers or has demand, so an over-committed
		// engine with zero workers is visible rather than simply absent.
		seen := map[string]bool{}
		var names []string
		for e := range byEngine {
			if !seen[e] {
				seen[e], names = true, append(names, e)
			}
		}
		for e := range live {
			if e != "" && !seen[e] {
				seen[e], names = true, append(names, e)
			}
		}
		sort.Strings(names)
		sort.Strings(unbound)
		for _, e := range names {
			label := e
			if eng, found := avatarengine.Get(e); found {
				label = eng.Label
			}
			sort.Strings(chans[e])
			perEngine = append(perEngine, map[string]any{
				"engine": e, "label": label,
				"workers":   live[e],
				"allocated": byEngine[e],
				"channels":  chans[e],
				"over":      byEngine[e] > live[e],
			})
		}
	}

	// current_engine keeps the single-engine contract working for callers that
	// still ask "which engine is the pool", and is empty on a mixed pool — where
	// the honest answer is `composition`, not one name.
	currentEngine := ""
	if len(composition) == 1 {
		for e := range composition {
			currentEngine = e
		}
	}

	ok(w, map[string]any{
		"composition":      composition,
		"describe":         describeComposition(composition),
		"pool_engines":     poolEngines,
		"pool_mixed":       poolMixed,
		"unbound_channels": unbound,
		"per_engine_quota": perEngine,
		"gpu_available":    snap.Available,
		"gpu_reason":       snap.Reason,
		"devices":          devices,
		"engines":          engines,
		"current_engine":   currentEngine,
		"pool_running":     poolRunning,
		"pool_dispatch":    liveTotal,
		"quota_allocated":  allocated,
		"quota_total":      liveTotal,
	})
}

// poolEngineMix reports the engines actually present in the dispatch pool.
//
// A mixed pool is a supported state, not a fault: acquireWorker filters on the
// worker's engine, so a channel whose avatar binds it to one engine only ever
// lands on a worker that can render that avatar.
//
// What remains genuinely unsafe is a channel with NO avatar bound: its demand
// may land on any engine, and different engines usually drive different material
// (a baked 720x1280 avatar vs a 512x512 portrait), so two visitors of that
// channel would see DIFFERENT PEOPLE. The console therefore warns off
// `unbound_channels`, and treats this list as information.
func poolEngineMix(workers []core.WorkerInfo) (engines []string, mixed bool) {
	seen := map[string]bool{}
	for _, w := range workers {
		e := engineFromDetail(w.Detail)
		if e == "" || seen[e] {
			continue
		}
		seen[e] = true
		engines = append(engines, e)
	}
	sort.Strings(engines)
	return engines, len(engines) > 1
}

// engineFromDetail maps a worker's Health banner to an engine id. The banners
// are set by each worker ("musetalk 1.5 avatar engine", "flashhead avatar
// engine", "wav2lipLS avatar engine (face_size=384)").
func engineFromDetail(detail string) string {
	d := strings.ToLower(detail)
	switch {
	case strings.Contains(d, "musetalk"):
		return "musetalk"
	case strings.Contains(d, "flashhead"):
		return "flashhead"
	case strings.Contains(d, "wav2lip"):
		return "wav2lipls"
	}
	return ""
}
