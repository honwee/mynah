// Package avatarengine is the catalog of interchangeable avatar (lip-sync)
// engines and the capacity math that decides how many workers fit on a GPU.
//
// Every engine here speaks the same proto/avatarengine/v1 contract, so cored
// treats them as equivalent — the differences that matter are resource profile
// and what source material they need.
//
// The VRAM numbers are MEASURED, never estimated. Two hard-won rules behind
// that, both measured on an RTX 4090 host on 2026-07-29:
//
//  1. Use the steady-state figure, not the post-load one. MuseTalk reads
//     5618 MiB once its models are loaded and 7632 MiB after its FIRST
//     inference (activations + cuDNN workspaces enter the allocator pool and
//     never leave). Sizing a pool off the load-time number over-provisions by
//     ~2GB per worker.
//  2. The figure is tied to the OUTPUT RESOLUTION, which comes from the baked
//     avatar / portrait — not from the engine alone. A 512x512 engine and a
//     720x1280 one are not comparable. Each entry records what it was measured
//     at; re-measure when the avatar's resolution changes materially.
package avatarengine

import (
	"fmt"
	"strings"
)

// Engine describes one interchangeable lip-sync backend.
type Engine struct {
	Name  string // stable id used in config + API
	Label string // console-facing
	// Script is the worker entrypoint inside the image (working dir
	// /app/workers/avatar).
	Script string
	Image  string

	// VRAMSteadyMB is the per-worker steady-state VRAM. Zero = NOT MEASURED,
	// which makes the engine unselectable (see Catalog.Selectable) rather than
	// letting a guess drive capacity planning.
	VRAMSteadyMB int
	// VRAMMeasuredAt records the conditions, because the number is only
	// meaningful with them.
	VRAMMeasuredAt string
	// ColdStartSec is time from container start to gRPC READY.
	ColdStartSec int
	// NeedsBake: the engine drives a pre-baked avatar directory (latents,
	// coords, masks, frames) rather than a single portrait image. Switching to
	// such an engine requires that material to exist first.
	NeedsBake bool
	Notes     string
}

// catalog is the registry of known engines.
var catalog = []Engine{
	{
		Name: "musetalk", Label: "MuseTalk 1.5",
		Script: "musetalk_server.py", Image: "mynah/musetalk:prod",
		VRAMSteadyMB: 7700,
		VRAMMeasuredAt: "2026-07-29 RTX 4090 reference host, 720x1280 baked avatar (leiya_mt, 484 frames): " +
			"5618MiB loaded -> 7632MiB after first inference, flat across further sessions; " +
			"a 24h prod worker read 7652MiB",
		ColdStartSec: 50,
		NeedsBake:    true,
		Notes:        "当前生产引擎。单步 UNet latent inpainting，无 torch.compile，冷启动快。",
	},
	{
		Name: "flashhead", Label: "SoulX-FlashHead 1.3B",
		Script: "server.py", Image: "mynah/flashhead:prod",
		VRAMSteadyMB: 6600,
		VRAMMeasuredAt: "2026-07-29 RTX 4090 reference host, 512x512 单张肖像 (linxia.jpg), compile 关闭: " +
			"6421MiB loaded -> 6464MiB after 3 sessions. 注意分辨率低于 musetalk 的 720x1280，" +
			"换成同等分辨率素材需重测",
		ColdStartSec: 90,
		NeedsBake:    false,
		Notes: "扩散式，只需一张肖像图，不需要烘焙。开启 torch.compile 可提吞吐但冷启动涨到 ~140s。" +
			"实测显存增长极小（+43MiB），与 musetalk 首次推理 +2GB 的形态不同。",
	},
	{
		Name: "wav2lipls", Label: "wav2lipLS",
		Script: "wav2lipls_server.py", Image: "mynah/avatar:prod",
		VRAMSteadyMB:   2000,
		VRAMMeasuredAt: "2026-10-08 RTX 4090 reference host, 720x1280 baked avatar (242 frames), face 384, HuBERT large: +1958MiB at READY after warmup (batch=16)",
		ColdStartSec:   30,
		NeedsBake:      true,
		Notes:          "最省显存的一档（约 2GB/路），6GB 显卡可跑；画质低于 MuseTalk / FlashHead。需要烘焙素材目录 + ckpt + HuBERT。",
	},
}

// All returns every known engine, measured or not (the console shows
// unmeasured ones as unavailable with the reason).
func All() []Engine { return append([]Engine(nil), catalog...) }

// Get looks up an engine by name.
func Get(name string) (Engine, bool) {
	for _, e := range catalog {
		if e.Name == name {
			return e, true
		}
	}
	return Engine{}, false
}

// Selectable reports whether an engine may be chosen. An engine without a
// measured VRAM profile is refused: capacity planning would have to guess, and
// guessing here means either OOM or wasted GPU.
func (e Engine) Selectable() (bool, string) {
	if e.VRAMSteadyMB <= 0 {
		return false, "显存画像未实测，无法参与容量计算（用 workers/avatar/vram_probe.py 实测后填入目录）"
	}
	if e.Image == "" || e.Script == "" {
		return false, "镜像或启动脚本未配置"
	}
	return true, ""
}

// GPU is one device's live memory picture, in MiB.
type GPU struct {
	Index   int
	TotalMB int
	// UsedByOthersMB is memory held by processes that are NOT pool workers
	// (TTS, ASR, someone else's job). Must be measured live: treating the
	// pool's own current usage as unavailable would make the pool able to
	// shrink but never grow.
	UsedByOthersMB int
}

// ReserveMB is the headroom kept free on every GPU. Not optional: the
// steady-state figures are averages over a session, while a peak can briefly
// exceed them, and fragmentation makes the last few hundred MB unusable in
// practice. max(2048, 10% of the card).
func ReserveMB(totalMB int) int {
	r := totalMB / 10
	if r < 2048 {
		r = 2048
	}
	return r
}

// Capacity is how many workers of one engine fit, and the arithmetic behind it
// — the console shows the working, not just the verdict.
type Capacity struct {
	Engine      string `json:"engine"`
	PerGPU      []int  `json:"per_gpu"` // slots on each GPU, index-aligned with the input
	Total       int    `json:"total"`   // sum
	Explain     string `json:"explain"` // human-readable arithmetic
	PerWorkerMB int    `json:"per_worker_mb"`
}

// Plan computes how many workers of e fit across gpus. A worker never spans
// GPUs, so this floors per device and sums — NOT floor(sum/per), which would
// promise a slot that no single card can host.
func Plan(e Engine, gpus []GPU) (Capacity, error) {
	if ok, why := e.Selectable(); !ok {
		return Capacity{}, fmt.Errorf("engine %q unusable: %s", e.Name, why)
	}
	cap := Capacity{Engine: e.Name, PerWorkerMB: e.VRAMSteadyMB}
	explain := ""
	for _, g := range gpus {
		reserve := ReserveMB(g.TotalMB)
		avail := g.TotalMB - g.UsedByOthersMB - reserve
		n := 0
		if avail > 0 {
			n = avail / e.VRAMSteadyMB
		}
		cap.PerGPU = append(cap.PerGPU, n)
		cap.Total += n
		explain += fmt.Sprintf("GPU%d: (%dMiB 总量 − %dMiB 其他占用 − %dMiB 安全余量) ÷ %dMiB/worker = %d；",
			g.Index, g.TotalMB, g.UsedByOthersMB, reserve, e.VRAMSteadyMB, n)
	}
	cap.Explain = explain
	return cap, nil
}

// Slot is one planned worker and the card it lands on.
type Slot struct {
	Engine string `json:"engine"`
	GPU    int    `json:"gpu"`
	VRAMMB int    `json:"vram_mb"`
}

// Placement is the answer to "can I run this composition, and where".
type Placement struct {
	Want     map[string]int `json:"want"`     // engine -> worker count requested
	Slots    []Slot         `json:"slots"`    // one entry per placed worker
	Feasible bool           `json:"feasible"` // every requested worker got a card
	Reason   string         `json:"reason"`   // why not, when !Feasible
	Explain  string         `json:"explain"`  // the arithmetic, per card
}

// PlanMix places a mixed composition across gpus. Unlike Plan, which answers
// "how many of one engine fit", this answers "does this specific mix fit and on
// which cards" — the question a mixed pool actually has to ask.
//
// A worker cannot span GPUs, so this is bin packing. First-fit-decreasing (the
// biggest workers placed first) is used deliberately over anything cleverer:
// with at most MaxPoolSlots workers over two cards, FFD is optimal in practice,
// and — more importantly — its result is explainable in one line to the operator
// who has to decide what to shut down. A packing the operator can't follow is
// worse than one slot short.
//
// GPUs are consumed in the order given, so pass them index-ascending for stable,
// reproducible placement.
func PlanMix(want map[string]int, gpus []GPU) Placement {
	p := Placement{Want: map[string]int{}}

	// Expand the request into individual workers, biggest first.
	var pending []Slot
	for name, n := range want {
		if n <= 0 {
			continue
		}
		p.Want[name] = n
		e, ok := Get(name)
		if !ok {
			p.Reason = fmt.Sprintf("未知引擎 %q", name)
			return p
		}
		if sel, why := e.Selectable(); !sel {
			p.Reason = fmt.Sprintf("引擎 %s 不可用：%s", e.Label, why)
			return p
		}
		for i := 0; i < n; i++ {
			pending = append(pending, Slot{Engine: name, GPU: -1, VRAMMB: e.VRAMSteadyMB})
		}
	}
	// Descending by size. Stable insertion sort: the list is tiny and this keeps
	// same-size engines in a deterministic order.
	for i := 1; i < len(pending); i++ {
		for j := i; j > 0 && pending[j].VRAMMB > pending[j-1].VRAMMB; j-- {
			pending[j], pending[j-1] = pending[j-1], pending[j]
		}
	}

	// Remaining budget per card, after other tenants and the safety reserve.
	free := make([]int, len(gpus))
	for i, g := range gpus {
		free[i] = g.TotalMB - g.UsedByOthersMB - ReserveMB(g.TotalMB)
		if free[i] < 0 {
			free[i] = 0
		}
	}

	placed := 0
	var unplaced []Slot
	for _, s := range pending {
		fit := -1
		for i := range gpus {
			if free[i] >= s.VRAMMB {
				fit = i
				break
			}
		}
		if fit < 0 {
			unplaced = append(unplaced, s)
			continue
		}
		free[fit] -= s.VRAMMB
		s.GPU = gpus[fit].Index
		p.Slots = append(p.Slots, s)
		placed++
	}

	explain := ""
	for i, g := range gpus {
		var on []string
		for _, s := range p.Slots {
			if s.GPU == g.Index {
				on = append(on, s.Engine)
			}
		}
		what := "空闲"
		if len(on) > 0 {
			what = strings.Join(on, " + ")
		}
		explain += fmt.Sprintf("GPU%d: %dMiB 可用（%dMiB 总量 − %dMiB 其他占用 − %dMiB 安全余量）→ %s，剩 %dMiB；",
			g.Index, g.TotalMB-g.UsedByOthersMB-ReserveMB(g.TotalMB), g.TotalMB, g.UsedByOthersMB,
			ReserveMB(g.TotalMB), what, free[i])
	}
	p.Explain = explain

	if len(unplaced) > 0 {
		var need []string
		for _, s := range unplaced {
			need = append(need, fmt.Sprintf("%s(%dMiB)", s.Engine, s.VRAMMB))
		}
		p.Reason = fmt.Sprintf("显存放不下：%s 无处安放（worker 不能跨卡）。%s",
			strings.Join(need, "、"), explain)
		return p
	}
	p.Feasible = true
	return p
}

// Count is how many workers of one engine the placement holds.
func (p Placement) Count(engine string) int {
	n := 0
	for _, s := range p.Slots {
		if s.Engine == engine {
			n++
		}
	}
	return n
}

// GPUFor returns the card assigned to the i-th worker of engine, or -1.
func (p Placement) GPUFor(engine string, nth int) int {
	seen := 0
	for _, s := range p.Slots {
		if s.Engine != engine {
			continue
		}
		if seen == nth {
			return s.GPU
		}
		seen++
	}
	return -1
}
