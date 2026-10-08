package control

import (
	"net/http"
	"sort"
	"strings"

	"mynah/internal/avatarengine"
	"mynah/internal/gpu"
	"mynah/internal/supervisor"
)

// registerServices mounts the local-component lifecycle routes. Only called
// when a Supervisor is wired (no Docker socket = no panel, rather than a panel
// whose buttons all fail).
func (s *Server) registerServices() {
	s.private("GET /api/v1/services", s.handleServiceList)
	s.private("POST /api/v1/services/{name}/start", s.handleServiceAction)
	s.private("POST /api/v1/services/{name}/stop", s.handleServiceAction)
	s.private("POST /api/v1/services/{name}/restart", s.handleServiceAction)
}

// serviceNeed reports whether a service is actually used by the CURRENT
// brain/turn selection, so the console can say "stopped but not needed" instead
// of showing a scary red dot — and, more importantly, warn before someone stops
// something that only LOOKS idle.
//
// mode is the service's live runtime variant (see supervisor.Service.Mode);
// it changes the advice materially for ASR, so it is an input here rather than
// something the UI tacks on afterwards.
func serviceNeed(name, mode, brainMode, turnMode, engine string) (needed bool, note string) {
	cloudBrain := brainMode == "qwen"
	cloudTurn := turnMode == "server_vad" || turnMode == "smart_turn"
	switch name {
	case "tts":
		if cloudBrain {
			return false, "当前云端大脑模式下对话不经过它；但控制台音色列表、声音复刻仍需要它，" +
				"且切回本地大脑前必须先启动（冷启动约 2.5 分钟）"
		}
		return true, "本地大脑模式下每一句话都经过它"
	case "asr":
		if mode == "decode-only" {
			if !(cloudBrain && cloudTurn) {
				// Misconfiguration: this worker will reject every session.
				return true, "⚠ 配置冲突：当前轮次模式需要语音识别，但本服务以精简模式启动" +
					"（未加载识别模型），会话会被直接拒绝。请清空 ASR_EXTRA_ARGS 后重启本服务。"
			}
			return true, "已是精简模式：不加载识别模型、不占显存，仅做 Opus 解码。" +
				"仍不能停——浏览器发来的是 Opus，云端接口要裸 PCM，全栈只有它做这步转换。" +
				"切回本地判轮次前需清空 ASR_EXTRA_ARGS 并重启本服务。"
		}
		if cloudBrain && cloudTurn {
			return true, "云端判停下识别模型确实不跑，但这个进程还兼着 Opus 解码——" +
				"浏览器发来的是 Opus，云端接口要的是裸 PCM，全栈只有它做这一步。" +
				"所以现在停掉数字人会听不见（画面正常、就是不回答）。" +
				"想省这 1.4GB 显存：给它加 --decode-only（ASR_EXTRA_ARGS）后重启。"
		}
		return true, "语音输入必需"
	case "musetalk", "musetalk2":
		return true, "数字人口型引擎，停掉会降低并发上限"
	}
	// Dynamic pool slots (name is "<engine>-<slot>") are engines too. They have
	// no fixed name to match, so key off the engine attribution instead.
	if engine != "" {
		return true, "数字人口型引擎，停掉会降低该引擎的并发上限"
	}
	return true, ""
}

func (s *Server) handleServiceList(w http.ResponseWriter, r *http.Request) {
	list, err := s.deps.Supervisor.List(r.Context())
	if err != nil {
		fail(w, http.StatusBadGateway, "list services: "+err.Error())
		return
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })

	cfg := s.deps.Config.Current()
	brainMode, turnMode := cfg.Brain.Mode, cfg.Turn.Mode
	if brainMode == "" {
		brainMode = "local"
	}
	if turnMode == "" {
		turnMode = "local"
	}

	out := make([]map[string]any, 0, len(list))
	// One nvidia-smi read for the whole list: the console's question about an
	// engine worker is "is it actually holding memory", and the pool card's
	// per-device totals cannot answer it per worker.
	snap := gpu.Read(r.Context())
	for _, svc := range list {
		needed, note := serviceNeed(svc.Name, svc.Mode, brainMode, turnMode, svc.Engine)
		row := map[string]any{
			"name": svc.Name, "label": svc.Label, "description": svc.Description,
			"state": svc.State, "detail": svc.Detail, "port": svc.Port,
			"gpu": svc.GPU, "controllable": svc.Controllable,
			"mode":               svc.Mode,
			"cold_start_seconds": svc.ColdStartSeconds,
			"started_at":         svc.StartedAt,
			"needed":             needed,
			"note":               note,
			"engine":             svc.Engine,
		}
		if svc.Engine != "" {
			if e, found := avatarengine.Get(svc.Engine); found {
				row["engine_label"] = e.Label
			} else {
				row["engine_label"] = svc.Engine
			}
		}
		// Absent deliberately when there is no reading, rather than 0: see
		// Snapshot.ForPIDs. A worker mid-model-load must not look free.
		// Every process in the container counts, not just the main one — the TTS
		// holds its 11GB in a forked vLLM worker.
		pids := map[int]bool{}
		if svc.HostPID > 0 {
			pids[svc.HostPID] = true
		}
		for _, p := range svc.HostPIDs {
			pids[p] = true
		}
		if len(pids) > 0 {
			if used, device, found := snap.ForPIDs(pids); found {
				row["vram_mb"] = used
				if device >= 0 {
					// The card the process is REALLY on beats the one the
					// container was configured with.
					row["gpu"] = device
				}
			}
		}
		out = append(out, row)
	}
	ok(w, map[string]any{
		"services": out, "brain_mode": brainMode, "turn_mode": turnMode,
	})
}

func (s *Server) handleServiceAction(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var (
		err    error
		action string
	)
	switch {
	case strings.HasSuffix(r.URL.Path, "/start"):
		action, err = "start", s.deps.Supervisor.Start(r.Context(), name)
	case strings.HasSuffix(r.URL.Path, "/stop"):
		action, err = "stop", s.deps.Supervisor.Stop(r.Context(), name)
	case strings.HasSuffix(r.URL.Path, "/restart"):
		action, err = "restart", s.deps.Supervisor.Restart(r.Context(), name)
	default:
		fail(w, http.StatusNotFound, "unknown action")
		return
	}
	if err != nil {
		// An unknown/uncontrollable name is a client error, not a backend one.
		code := http.StatusBadGateway
		if strings.Contains(err.Error(), "unknown service") ||
			strings.Contains(err.Error(), "not controllable") {
			code = http.StatusBadRequest
		}
		fail(w, code, action+" "+name+": "+err.Error())
		return
	}
	ok(w, map[string]any{"name": name, "action": action, "accepted": true})
}

// compile-time check that the wired type satisfies the interface.
var _ supervisor.Supervisor = (*supervisor.Docker)(nil)
