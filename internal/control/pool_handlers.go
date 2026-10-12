package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"mynah/internal/avatarengine"
	"mynah/internal/gpu"
	"mynah/internal/supervisor"
)

// registerPool mounts avatar-pool resizing. Separate from the read-only
// capacity view because this one mutates: it creates and destroys containers.
func (s *Server) registerPool() {
	s.private("POST /api/v1/avatar/pool", s.handlePoolCompose)
	s.private("POST /api/v1/avatar/pool/size", s.handlePoolResize)
}

// poolPortBase / poolPortEnd bound the ports dynamic slots may use. They must
// match the --worker-pool range cored watches.
//
// Ports are NOT slot+base: the compose-managed workers sit on 9414 and 9417,
// which is not contiguous, so a naive base+n-1 would hand slot 4 the port
// musetalk2 already owns and the new container would fail to bind. Free ports
// are therefore allocated by scanning, and taken ports are learned from the
// fixed registry rather than assumed.
const (
	poolPortBase = 9414
	poolPortEnd  = 9421
)

// freePoolPort returns the lowest port in the range not already claimed by a
// known worker (fixed or dynamic).
func freePoolPort(taken map[int]bool) (int, error) {
	for p := poolPortBase; p <= poolPortEnd; p++ {
		if !taken[p] {
			return p, nil
		}
	}
	return 0, fmt.Errorf("端口段 %d-%d 已用尽", poolPortBase, poolPortEnd)
}

// freePoolSlot returns the lowest unused slot number.
func freePoolSlot(used map[int]bool) (int, error) {
	for n := 1; n <= supervisor.MaxPoolSlots; n++ {
		if !used[n] {
			return n, nil
		}
	}
	return 0, fmt.Errorf("槽位已用尽（上限 %d）", supervisor.MaxPoolSlots)
}

type poolResizeReq struct {
	Size int `json:"size"`
}

type poolComposeReq struct {
	// Workers is the target composition: engine id → worker count. Absent
	// engines mean zero, so this is the whole picture, not a delta.
	Workers map[string]int `json:"workers"`
}

// handlePoolCompose sets the pool to an exact composition.
//
// This is the primary pool endpoint; resize and engine-switch are expressed in
// terms of it. Mixed compositions are a supported state, not a transient one:
// cored parses each worker's engine from its health banner and routes
// engine-bound channels only to workers that can serve them.
func (s *Server) handlePoolCompose(w http.ResponseWriter, r *http.Request) {
	var req poolComposeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	want, ferr := normalizeComposition(req.Workers)
	if ferr != "" {
		fail(w, http.StatusBadRequest, ferr)
		return
	}
	s.reconcilePool(w, r, want)
}

// normalizeComposition validates a requested composition and drops zero entries
// (a zero count is a normal way for the console to say "none of this engine").
func normalizeComposition(workers map[string]int) (map[string]int, string) {
	want := map[string]int{}
	total := 0
	names := make([]string, 0, len(workers))
	for e := range workers {
		names = append(names, e)
	}
	sort.Strings(names)
	for _, e := range names {
		n := workers[e]
		if n < 0 {
			return nil, fmt.Sprintf("引擎 %s 的路数不能为负", e)
		}
		if n == 0 {
			continue
		}
		eng, found := avatarengine.Get(e)
		if !found {
			return nil, "未知引擎: " + e
		}
		if sel, why := eng.Selectable(); !sel {
			return nil, eng.Label + " 不可选：" + why
		}
		want[e] = n
		total += n
	}
	if total > supervisor.MaxPoolSlots {
		return nil, fmt.Sprintf("池总路数上限为 %d，请求 %d", supervisor.MaxPoolSlots, total)
	}
	return want, ""
}

// handlePoolResize keeps the old single-number contract working: it changes the
// count of the engine the pool is already running, without changing which engine
// that is. Ambiguous on a mixed pool, so it refuses there and points at the
// composition endpoint rather than guessing which engine the operator meant.
func (s *Server) handlePoolResize(w http.ResponseWriter, r *http.Request) {
	var req poolResizeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	if req.Size < 1 {
		fail(w, http.StatusBadRequest, "池大小至少为 1")
		return
	}
	if req.Size > supervisor.MaxPoolSlots {
		fail(w, http.StatusBadRequest,
			fmt.Sprintf("池大小上限为 %d", supervisor.MaxPoolSlots))
		return
	}
	list, err := s.deps.Supervisor.List(r.Context())
	if err != nil {
		fail(w, http.StatusBadGateway, "list services: "+err.Error())
		return
	}
	cur := countByEngine(currentComposition(list))
	switch len(cur) {
	case 0:
		fail(w, http.StatusConflict,
			"池里没有运行中的 worker，无法判断要扩哪个引擎。请用组合接口指定引擎。")
	case 1:
		for e := range cur {
			s.reconcilePool(w, r, map[string]int{e: req.Size})
		}
	default:
		fail(w, http.StatusConflict, fmt.Sprintf(
			"当前是混合池（%s），「池大小」一个数字说不清要改哪个引擎。请按引擎分别设置路数。",
			describeComposition(cur)))
	}
}

// reconcilePool is the one path that mutates the pool. Guards, in order, each
// one refusing BEFORE anything is created or destroyed:
//
//  1. material  — an engine whose paths are missing would crash-loop
//  2. VRAM      — over-committing OOMs a worker mid-call (growth only; a shrink
//     needs no budget, and failing closed on an unreadable GPU
//     would block the very action that frees memory)
//  3. quota     — the new composition must still serve every published channel,
//     per engine and in total
//
// All three answer with the arithmetic, because "why can't I set this" is the
// question an operator actually has.
func (s *Server) reconcilePool(w http.ResponseWriter, r *http.Request, want map[string]int) {
	list, err := s.deps.Supervisor.List(r.Context())
	if err != nil {
		fail(w, http.StatusBadGateway, "list services: "+err.Error())
		return
	}
	inv := currentComposition(list)
	cur := countByEngine(inv)

	growing := false
	for e, n := range want {
		if n > cur[e] {
			growing = true
			// Material is checked here rather than only in applyComposition so a
			// misconfigured engine costs a 409 instead of a half-applied plan.
			eng, _ := avatarengine.Get(e)
			mat, okMat := s.engineMaterial(e)
			if !okMat {
				fail(w, http.StatusConflict, fmt.Sprintf(
					"%s 的素材路径未配置（需要在启动参数里声明）。配置好再加这个引擎，"+
						"否则新 worker 起不来。", eng.Label))
				return
			}
			if reason := eng.ValidateMaterial(mat, nil); reason != "" {
				fail(w, http.StatusConflict, eng.Label+"："+reason)
				return
			}
		}
	}

	var place avatarengine.Placement
	if growing {
		gpus, reason := s.gpuBudget(r.Context(), list)
		if reason != "" {
			fail(w, http.StatusConflict, reason)
			return
		}
		place = avatarengine.PlanMix(want, gpus)
		if !place.Feasible {
			fail(w, http.StatusConflict, place.Reason)
			return
		}
	}

	if msg := s.checkQuotaForComposition(r.Context(), want); msg != "" {
		fail(w, http.StatusConflict, msg)
		return
	}

	steps := planComposition(inv, want)
	if len(steps) == 0 {
		ok(w, map[string]any{"composition": want, "changed": false,
			"describe": describeComposition(want)})
		return
	}
	report, err := s.applyComposition(r.Context(), steps, list, place)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	report["composition"] = want
	report["describe"] = describeComposition(want)
	if place.Explain != "" {
		report["placement"] = place.Explain
	}
	report["note"] = "worker 已下发；cored 会在几秒内探测到并更新池（新 worker 需加载模型才会开始派发）"
	ok(w, report)
}

// gpuBudget reads live VRAM and attributes it, returning the planner's view of
// each device. The pool's OWN usage is excluded from "used by others" because
// it is reclaimable when the pool is resized or replaced — counting it would
// make the planner conclude the pool can never grow.
func (s *Server) gpuBudget(ctx context.Context, list []supervisor.Service) ([]avatarengine.GPU, string) {
	snap := gpu.Read(ctx)
	if !snap.Available {
		// Without GPU visibility the planner is blind. Fail closed: creating an
		// unbudgeted worker is how a live call gets OOM-killed.
		return nil, "无法读取显存信息，出于安全考虑拒绝操作：" + snap.Reason
	}
	poolPIDs := map[int]bool{}
	for _, svc := range list {
		if !(svc.Port >= poolPortBase && svc.Port <= poolPortEnd) {
			continue
		}
		// Children too: a CUDA context opened by a forked process is still the
		// pool's own, reclaimable memory (see capacity_handlers).
		if svc.HostPID > 0 {
			poolPIDs[svc.HostPID] = true
		}
		for _, p := range svc.HostPIDs {
			poolPIDs[p] = true
		}
	}
	var gpus []avatarengine.GPU
	for _, d := range snap.Devices {
		others := d.UsedMB - snap.UsedByMB(d.Index, poolPIDs)
		if others < 0 {
			others = 0
		}
		gpus = append(gpus, avatarengine.GPU{Index: d.Index, TotalMB: d.TotalMB, UsedByOthersMB: others})
	}
	return gpus, ""
}
