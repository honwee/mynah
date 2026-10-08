// Declarative pool composition: the caller states what the pool should look
// like ({"musetalk": 2, "flashhead": 1}) and this file works out how to get
// there.
//
// Why declarative rather than three imperative endpoints: resize ("one more
// worker"), engine switch ("replace everything") and mix ("one of each") are the
// same operation seen from three angles, and keeping them as separate code paths
// is how the musetalk-hardcoded prefix counting got in. Both older endpoints are
// now thin wrappers that compute a `want` map and call in here.
//
// Dispatch already handles a mixed pool: cored parses each worker's engine from
// its health banner and acquireWorker filters on it. What was missing was the
// composition entry point and per-engine accounting — not the routing.
package control

import (
	"context"
	"fmt"
	"sort"

	"mynah/internal/avatarengine"
	"mynah/internal/supervisor"
)

// poolWorker is one avatar worker in the pool, as the reconciler sees it.
type poolWorker struct {
	// Name is the service id ("musetalk2", "flashhead-3").
	Name   string
	Engine string
	// Slot is >0 for a dynamic slot this server created and may delete, and 0
	// for a compose-managed worker, which may only be stopped — compose owns it,
	// and removing it would make `docker compose up -d` the only way back.
	Slot    int
	Running bool
}

func (w poolWorker) dynamic() bool { return w.Slot > 0 }

// currentComposition reads the live pool out of a supervisor listing.
//
// Membership is decided by supervisor.EngineOfService, the single source of
// "which engine does this worker run" — NOT by a name prefix. The prefix version
// counted a hypothetical "musetalkX" as a pool member and missed every
// flashhead worker, which is exactly the bug a mixed pool cannot afford.
//
// The result is sorted by name so a plan built from it is reproducible (docker
// returns containers in arbitrary order).
func currentComposition(list []supervisor.Service) []poolWorker {
	var out []poolWorker
	for _, svc := range list {
		engine := supervisor.EngineOfService(svc.Name)
		if engine == "" {
			continue
		}
		w := poolWorker{Name: svc.Name, Engine: engine,
			Running: svc.State == supervisor.StateRunning || svc.State == supervisor.StateStarting}
		if _, slot, err := supervisor.ParsePoolSlot("mynah-" + svc.Name); err == nil {
			w.Slot = slot
		}
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// countByEngine is the composition of a worker set, running workers only.
func countByEngine(inv []poolWorker) map[string]int {
	n := map[string]int{}
	for _, w := range inv {
		if w.Running {
			n[w.Engine]++
		}
	}
	return n
}

type stepAction string

const (
	// stepStart restarts an existing stopped worker — no container is created.
	stepStart stepAction = "start"
	// stepCreate materializes a new dynamic slot.
	stepCreate stepAction = "create"
	// stepRetire removes a dynamic slot or stops a compose-managed one.
	stepRetire stepAction = "retire"
)

// step is one docker-level action. Worker is the target for start/retire; for
// create only Engine is meaningful, because the slot and port are allocated at
// execution time against live state.
type step struct {
	Action stepAction
	Engine string
	Worker poolWorker
}

// planComposition is the pure core: inventory + target → ordered steps.
//
// Two policies live here, both of which cost real money to get wrong:
//
// REUSE BEFORE CREATE. A stopped worker of the wanted engine is started rather
// than a new slot created. Without this, every trip through "flashhead 1 → 0 →
// 1" would leave the two compose musetalk containers idle and accrete dynamic
// slots until MaxPoolSlots was exhausted.
//
// RETIRE BEFORE ADD, except when it would empty the pool. Interleaving keeps the
// peak worker count at max(current, want) instead of current+want, which matters
// because the VRAM check validates the TARGET composition — a swap that briefly
// runs old and new together can OOM a live worker. The one case where an add has
// to go first is a single-worker pool, where retiring first would drop the pool
// to zero and cut visitors off for a cold start. That case is safe to overshoot
// by construction: peak is two workers, and a pool holding one worker has room
// for a second or the current one would not have fit either.
func planComposition(inv []poolWorker, want map[string]int) []step {
	byEngine := map[string][]poolWorker{}
	engines := map[string]bool{}
	for _, w := range inv {
		byEngine[w.Engine] = append(byEngine[w.Engine], w)
		engines[w.Engine] = true
	}
	for e, n := range want {
		if n > 0 {
			engines[e] = true
		}
	}
	names := make([]string, 0, len(engines))
	for e := range engines {
		names = append(names, e)
	}
	sort.Strings(names)

	var adds, retires []step
	running := 0
	for _, e := range names {
		var up, down []poolWorker
		for _, w := range byEngine[e] {
			if w.Running {
				up = append(up, w)
			} else {
				down = append(down, w)
			}
		}
		running += len(up)

		// Retire dynamic slots first, highest slot first; compose-managed workers
		// last, since stopping one is the change an operator is least likely to
		// have intended and the hardest for this server to undo.
		sort.Slice(up, func(i, j int) bool {
			if up[i].dynamic() != up[j].dynamic() {
				return up[i].dynamic()
			}
			return up[i].Slot > up[j].Slot
		})
		// Reuse compose-managed workers first (they exist whether or not the pool
		// wants them, so leaving one stopped is pure waste), then low slots.
		sort.Slice(down, func(i, j int) bool {
			if down[i].dynamic() != down[j].dynamic() {
				return down[j].dynamic()
			}
			return down[i].Slot < down[j].Slot
		})

		switch n := want[e]; {
		case n > len(up):
			need := n - len(up)
			for i := 0; i < need; i++ {
				if i < len(down) {
					adds = append(adds, step{stepStart, e, down[i]})
				} else {
					adds = append(adds, step{stepCreate, e, poolWorker{Engine: e}})
				}
			}
		case n < len(up):
			for i := 0; i < len(up)-n; i++ {
				retires = append(retires, step{stepRetire, e, up[i]})
			}
		}
	}

	var out []step
	ai, ri := 0, 0
	for ai < len(adds) || ri < len(retires) {
		// Retiring is allowed whenever it does not leave the pool empty with an
		// add still outstanding.
		if ri < len(retires) && (running > 1 || ai >= len(adds)) {
			out = append(out, retires[ri])
			ri++
			running--
			continue
		}
		out = append(out, adds[ai])
		ai++
		running++
	}
	return out
}

// applyComposition executes a plan against docker, allocating a slot and port
// for each created worker and honouring the placement's GPU choice.
//
// Ports and slots are allocated by SCANNING what is taken, never as base+n: the
// compose workers sit on 9414 and 9417, so slot arithmetic would hand a new
// worker a port musetalk2 already binds.
func (s *Server) applyComposition(ctx context.Context, steps []step,
	list []supervisor.Service, place avatarengine.Placement) (map[string]any, error) {

	d, castOK := s.deps.Supervisor.(*supervisor.Docker)
	if !castOK {
		return nil, fmt.Errorf("当前 supervisor 实现不支持调整引擎池")
	}

	takenPorts := map[int]bool{}
	usedSlots := map[int]bool{}
	for _, svc := range list {
		if svc.Port > 0 {
			takenPorts[svc.Port] = true
		}
		if _, n, err := supervisor.ParsePoolSlot("mynah-" + svc.Name); err == nil {
			usedSlots[n] = true
		}
	}

	// nth counts workers per engine as they are created, so each one can be
	// matched to its own placement decision (the planner may spread an engine's
	// workers across cards).
	nth := map[string]int{}
	var created, started, retired []string

	for _, st := range steps {
		switch st.Action {
		case stepStart:
			if err := d.Start(ctx, st.Worker.Name); err != nil {
				return nil, fmt.Errorf("启动 %s 失败：%w（已完成：新建 %v，启动 %v，退役 %v）",
					st.Worker.Name, err, created, started, retired)
			}
			started = append(started, st.Worker.Name)

		case stepCreate:
			eng, found := avatarengine.Get(st.Engine)
			if !found {
				return nil, fmt.Errorf("引擎目录缺少 %s", st.Engine)
			}
			mat, okMat := s.engineMaterial(st.Engine)
			if !okMat {
				return nil, fmt.Errorf("%s 的素材路径未配置（需要在启动参数里声明）", eng.Label)
			}
			if reason := eng.ValidateMaterial(mat, nil); reason != "" {
				return nil, fmt.Errorf("%s：%s", eng.Label, reason)
			}
			slot, err := freePoolSlot(usedSlots)
			if err != nil {
				return nil, err
			}
			port, err := freePoolPort(takenPorts)
			if err != nil {
				return nil, err
			}
			// The planner already decided which card this worker fits on; without
			// this override every worker would land on the single --pool-gpu
			// default and the other card's headroom would be unreachable.
			if g := place.GPUFor(st.Engine, nth[st.Engine]); g >= 0 {
				mat.GPU = g
			}
			nth[st.Engine]++
			ws, err := eng.BuildWorkerSpec(mat, port)
			if err != nil {
				return nil, err
			}
			name := supervisor.PoolName(st.Engine, slot)
			if err := d.Ensure(ctx, supervisor.ContainerSpec{
				Name: name, Image: ws.Image, Cmd: ws.Cmd, Env: ws.Env,
				Binds: ws.Binds, GPUIndex: ws.GPUIndex,
			}); err != nil {
				return nil, fmt.Errorf("创建 %s 失败：%w（已完成：新建 %v，启动 %v，退役 %v）",
					name, err, created, started, retired)
			}
			usedSlots[slot] = true
			takenPorts[port] = true
			created = append(created, name)

		case stepRetire:
			if err := s.retireWorker(ctx, d, supervisor.Service{Name: st.Worker.Name}); err != nil {
				return nil, fmt.Errorf("退役 %s 失败：%w（已完成：新建 %v，启动 %v，退役 %v）",
					st.Worker.Name, err, created, started, retired)
			}
			retired = append(retired, st.Worker.Name)
		}
	}

	return map[string]any{
		"created": created, "started": started, "retired": retired,
		"changed": len(steps) > 0,
	}, nil
}
