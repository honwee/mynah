// Docker implementation of Supervisor: manages the Mynah containers via
// the Engine API over /var/run/docker.sock.
//
// No Docker SDK dependency — the three endpoints needed (list/start/stop) are
// trivial over plain HTTP with a unix dialer, and the SDK would pull a large
// transitive tree into a binary that otherwise has none of it.
//
// SECURITY: the docker socket is equivalent to root on the host. Service names
// from HTTP callers are looked up in the registry below and never interpolated
// into a request path, so an arbitrary container cannot be reached through this.
package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"mynah/internal/avatarengine"
)

// DefaultSocket is the Engine API socket cored talks to.
const DefaultSocket = "/var/run/docker.sock"

// spec is the static description of one managed container.
type spec struct {
	container    string // docker container name (--name)
	label        string
	description  string
	port         int
	gpu          int
	coldStart    int
	controllable bool
	// engine ties a fixed service to an avatarengine catalog id, empty for
	// services that are not avatar workers (TTS, ASR). This is the ONLY place a
	// fixed worker's engine is declared: a mixed pool has to count workers per
	// engine, and inferring it from the container name (HasPrefix "musetalk")
	// silently breaks the moment a compose worker is renamed or a second engine
	// gets a fixed slot.
	engine string
}

// registry is the allowlist: the ONLY containers this supervisor will touch.
// Keys are the stable API ids. Adding a service here is the deliberate act of
// making it console-controllable.
var registry = map[string]spec{
	"tts": {
		container: "mynah-tts",
		label:     "语音合成 (TTS)",
		description: "本地语音合成。云端大脑模式下的对话不经过它，但控制台音色列表、" +
			"声音复刻、以及本地大脑模式的全部说话都依赖它。冷启动约 2.5 分钟。",
		port: 8091, gpu: 0, coldStart: 150, controllable: true,
	},
	"asr": {
		container: "mynah-asr",
		label:     "语音识别 (ASR)",
		description: "语音识别与断句。注意：云端判停模式下识别模型虽然不跑，但它仍是" +
			"全栈唯一的 Opus 解码器——停掉麦克风音频就进不来，数字人听不见。",
		port: 9402, gpu: 1, coldStart: 45, controllable: true,
	},
	// The engine's own name is in the label, not just the prose: on a mixed pool
	// two different engines both have a "#1", and a card titled "数字人引擎 #1"
	// next to another card titled "数字人引擎 #1" tells the operator nothing about
	// which of them serves which channel.
	"musetalk": {
		container:   "mynah-musetalk",
		label:       "数字人引擎 · MuseTalk 1.5 #1",
		description: "MuseTalk 1.5 口型引擎（需要烘焙形象，720×1280 半身），单会话。停掉后 MuseTalk 并发上限降低。",
		port:        9414, gpu: 1, coldStart: 50, controllable: true, engine: "musetalk",
	},
	"musetalk2": {
		container:   "mynah-musetalk2",
		label:       "数字人引擎 · MuseTalk 1.5 #2",
		description: "MuseTalk 1.5 口型引擎（需要烘焙形象，720×1280 半身），单会话。停掉后 MuseTalk 并发上限降低。",
		port:        9417, gpu: 1, coldStart: 50, controllable: true, engine: "musetalk",
	},
}

// Docker implements Supervisor against the Engine API.
type Docker struct {
	http *http.Client
	// stream is for endpoints that block for the lifetime of a job (log
	// follow): the same client cannot be used, because its timeout is sized for
	// a container stop and would sever a bake's output mid-run.
	stream *http.Client
}

// stopGraceSeconds is how long docker waits after SIGTERM before SIGKILL.
// These are model servers holding GPU memory; give them a real chance to exit.
const stopGraceSeconds = 30

// NewDocker returns a supervisor talking to the Engine API on socket. It does
// not dial — a Docker-less host simply reports every service as unknown rather
// than failing cored's startup.
func NewDocker(socket string) *Docker {
	if socket == "" {
		socket = DefaultSocket
	}
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}
	return &Docker{
		http: &http.Client{
			// Must exceed stopGraceSeconds with room to spare: /stop blocks for the
			// whole grace period, so a client timeout equal to it reports a bogus
			// failure for a stop that is actually proceeding normally.
			Timeout:   (stopGraceSeconds + 30) * time.Second,
			Transport: &http.Transport{DialContext: dial},
		},
		// No timeout: the caller's context is the deadline. See the field doc.
		stream: &http.Client{Transport: &http.Transport{DialContext: dial}},
	}
}

// dockerContainer is the subset of /containers/json this needs.
type dockerContainer struct {
	Names   []string `json:"Names"`
	Image   string   `json:"Image"`
	State   string   `json:"State"`
	Status  string   `json:"Status"`
	Created int64    `json:"Created"`
	Command string   `json:"Command"`
	ID      string   `json:"Id"`
}

// inspect returns the runtime mode, host pid and listen port for a container,
// all read off the container itself. /containers/json truncates Command and
// omits the pid, so this needs the inspect endpoint. Failure is non-fatal.
func (d *Docker) inspect(ctx context.Context, name, containerID string) (mode string, hostPID, port, gpu int) {
	gpu = -1
	if containerID == "" {
		return "", 0, 0, gpu
	}
	var detail struct {
		Args  []string `json:"Args"`
		State struct {
			Pid int `json:"Pid"`
		} `json:"State"`
		Config struct {
			Cmd []string `json:"Cmd"`
		} `json:"Config"`
		HostConfig struct {
			DeviceRequests []struct {
				DeviceIDs []string `json:"DeviceIDs"`
			} `json:"DeviceRequests"`
		} `json:"HostConfig"`
	}
	if err := d.do(ctx, http.MethodGet,
		"/containers/"+url.PathEscape(containerID)+"/json", &detail); err != nil {
		return "", 0, 0, gpu
	}
	hostPID = detail.State.Pid
	// Which card this container was actually given, rather than which card
	// someone intended it to have: a dynamic pool slot has no registry entry to
	// read a gpu index from, and reporting -1 for it renders in the console as
	// "不占用 GPU" — false for a worker holding 6.6GB.
	if idx, found := deviceRequestGPU(detail.HostConfig.DeviceRequests); found {
		gpu = idx
	}
	for _, argv := range [][]string{detail.Args, detail.Config.Cmd} {
		for _, a := range argv {
			if name == "asr" && strings.Contains(a, "--decode-only") {
				mode = "decode-only"
			}
			// Dynamic pool slots have no registry entry to read their port from,
			// but the port is in their argv (BuildWorkerSpec emits --port=N).
			// It matters beyond display: the capacity planner identifies pool
			// workers by port, and a worker it cannot identify has its VRAM
			// counted as someone else's — which makes the pool unable to grow.
			if rest, isPort := strings.CutPrefix(a, "--port="); isPort {
				if n, err := strconv.Atoi(strings.TrimSpace(rest)); err == nil {
					port = n
				}
			}
		}
	}
	return mode, hostPID, port, gpu
}

// deviceRequestGPU pulls the CDI index out of "nvidia.com/gpu=N". found is false
// when the container asked for no GPU at all, which is a different statement
// from "GPU 0" — see gpuFromDeviceRequests, whose caller wants the 0 default.
func deviceRequestGPU(reqs []struct {
	DeviceIDs []string `json:"DeviceIDs"`
}) (int, bool) {
	for _, r := range reqs {
		for _, id := range r.DeviceIDs {
			if _, after, isGPU := strings.Cut(id, "nvidia.com/gpu="); isGPU {
				if n, err := strconv.Atoi(strings.TrimSpace(after)); err == nil {
					return n, true
				}
			}
		}
	}
	return 0, false
}

// engineLabel is the console-facing name of an engine, for labels the supervisor
// composes itself. Unknown ids fall back to the raw id rather than "" so a
// hand-created container still reads as something.
func engineLabel(engine string) string {
	if e, found := avatarengine.Get(engine); found {
		return e.Label
	}
	return engine
}

// engineDescription is the one-line "what material does this need" note, so a
// dynamically created worker explains itself as well as a registry one does.
func engineDescription(engine string) string {
	switch engine {
	case "musetalk":
		return "MuseTalk 1.5 口型引擎（需要烘焙形象，720×1280 半身），单会话。"
	case "flashhead":
		return "SoulX-FlashHead 1.3B 口型引擎（单张肖像即可，512×512 头肩），单会话。"
	}
	return ""
}

// containerPIDs lists every process in a container, in the HOST pid namespace —
// which is what docker top reports and what nvidia-smi's pid column is. Needed
// because the process holding the CUDA context is often a CHILD of the
// container's main process (vLLM forks its worker), so the main pid alone
// attributes that memory to nobody.
//
// Errors are swallowed: a stopped container has no process list, and a failed
// top must degrade to "cannot attribute" rather than break the service list.
func (d *Docker) containerPIDs(ctx context.Context, containerID string) []int {
	if containerID == "" {
		return nil
	}
	var top struct {
		Titles    []string   `json:"Titles"`
		Processes [][]string `json:"Processes"`
	}
	if err := d.do(ctx, http.MethodGet,
		"/containers/"+url.PathEscape(containerID)+"/top?ps_args="+url.QueryEscape("-eo pid"),
		&top); err != nil {
		return nil
	}
	col := 0
	for i, t := range top.Titles {
		if strings.EqualFold(strings.TrimSpace(t), "PID") {
			col = i
			break
		}
	}
	var pids []int
	for _, row := range top.Processes {
		if col >= len(row) {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimSpace(row[col])); err == nil && n > 0 {
			pids = append(pids, n)
		}
	}
	return pids
}

func (d *Docker) do(ctx context.Context, method, path string, out any) error {
	return d.doJSON(ctx, method, path, nil, out)
}

// doJSON issues a request with an optional JSON body. body == nil sends none,
// which is what every non-create endpoint wants.
func (d *Docker) doJSON(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	// The host part is ignored for a unix socket, but net/http needs one.
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return fmt.Errorf("docker api: %w", err)
	}
	defer resp.Body.Close()
	// 304 = already in the requested state (start on running / stop on stopped).
	if resp.StatusCode == http.StatusNotModified {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return fmt.Errorf("docker api %s: %s", resp.Status, e.Message)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (d *Docker) List(ctx context.Context) ([]Service, error) {
	var containers []dockerContainer
	// all=1 so stopped containers are visible too (a stopped service must still
	// appear in the console, with a start button).
	err := d.do(ctx, http.MethodGet, "/containers/json?all=1", &containers)

	byName := map[string]dockerContainer{}
	if err == nil {
		for _, c := range containers {
			for _, n := range c.Names {
				byName[strings.TrimPrefix(n, "/")] = c
			}
		}
	}

	out := make([]Service, 0, len(registry))
	for name, sp := range registry {
		svc := Service{
			Name: name, Label: sp.label, Description: sp.description,
			Port: sp.port, GPU: sp.gpu, Controllable: sp.controllable,
			ColdStartSeconds: sp.coldStart, State: StateUnknown, Engine: sp.engine,
		}
		if err != nil {
			// Docker unreachable: report unknown rather than pretending stopped,
			// so the console doesn't offer a "start" that can't work.
			svc.Detail = "docker 不可达: " + err.Error()
			out = append(out, svc)
			continue
		}
		c, found := byName[sp.container]
		if !found {
			svc.State = StateMissing
			// Reachable when someone `docker rm`s a service, or when a
			// container was created with `docker run --rm` (stopping such a
			// container destroys it, so there is nothing left to start).
			// Everything here is compose-created precisely so that stop/start
			// is reversible — recreating is a compose job, not an API call.
			svc.Detail = "容器不存在。用 docker compose up -d " + name + " 重建（compose 管理的服务停止后应当保留，出现此状态多半是被手动 rm 了）"
			out = append(out, svc)
			continue
		}
		svc.Detail = c.Status
		svc.StartedAt = time.Unix(c.Created, 0)
		svc.Mode, svc.HostPID, _, _ = d.inspect(ctx, name, c.ID)
		if c.State == "running" {
			svc.HostPIDs = d.containerPIDs(ctx, c.ID)
		}
		if svc.Mode == "decode-only" {
			// No model loaded, so it holds no VRAM — report that rather than
			// the slot it would occupy in full mode.
			svc.GPU = -1
		}
		switch c.State {
		case "running":
			// Docker's health, when the image declares a healthcheck, rides in
			// Status as "(healthy)"/"(health: starting)".
			if strings.Contains(c.Status, "health: starting") {
				svc.State = StateStarting
			} else {
				svc.State = StateRunning
			}
		case "created", "restarting":
			svc.State = StateStarting
		case "exited", "dead":
			svc.State = StateStopped
			// Distinguish "operator stopped it" from "it crashed". A python
			// model server that doesn't trap SIGTERM gets SIGKILLed at the end
			// of the stop grace, so 137 (128+SIGKILL) and 143 (128+SIGTERM) are
			// the NORMAL outcome of a deliberate stop — showing those as
			// 异常退出 would cry wolf on every single stop.
			if !strings.Contains(c.Status, "Exited (0)") &&
				!strings.Contains(c.Status, "Exited (137)") &&
				!strings.Contains(c.Status, "Exited (143)") {
				svc.State = StateFailed
			}
		case "paused":
			svc.State = StateStopped
		}
		out = append(out, svc)
	}

	// Dynamic pool slots are not in the fixed registry — they are created at
	// runtime — so discover them from the container names themselves. Without
	// this the console would show a pool it cannot see, and a shrink would find
	// nothing to remove.
	if err == nil {
		for name, c := range byName {
			engine, slot, perr := validatePoolName(name)
			if perr != nil {
				continue
			}
			svc := Service{
				Name: fmt.Sprintf("%s-%d", engine, slot),
				// Name the engine: a dynamic slot's "#1" collides with the fixed
				// registry's "#1", and on a mixed pool the engine is the whole
				// point of the distinction (which channels can this serve).
				Label:        fmt.Sprintf("数字人引擎 · %s #%d（动态）", engineLabel(engine), slot),
				Description:  engineDescription(engine) + "由控制台组合池创建，可随组合调整被回收。",
				Controllable: true,
				GPU:          -1,
				State:        StateUnknown,
				Detail:       c.Status,
				StartedAt:    time.Unix(c.Created, 0),
				Engine:       engine,
			}
			svc.Mode, svc.HostPID, svc.Port, svc.GPU = d.inspect(ctx, svc.Name, c.ID)
			if c.State == "running" {
				svc.HostPIDs = d.containerPIDs(ctx, c.ID)
			}
			switch c.State {
			case "running":
				svc.State = StateRunning
			case "created", "restarting":
				svc.State = StateStarting
			case "exited", "dead", "paused":
				svc.State = StateStopped
			}
			out = append(out, svc)
		}
	}
	return out, nil
}

// resolve maps an API id to its container name, rejecting anything not in the
// registry. This is the allowlist boundary — callers never reach docker with a
// name of their own choosing.
func resolve(name string) (spec, error) {
	sp, ok := registry[name]
	if !ok {
		return spec{}, fmt.Errorf("unknown service %q", name)
	}
	if !sp.controllable {
		return spec{}, fmt.Errorf("service %q is not controllable", name)
	}
	return sp, nil
}

func (d *Docker) Start(ctx context.Context, name string) error {
	sp, err := resolve(name)
	if err != nil {
		return err
	}
	return d.do(ctx, http.MethodPost,
		"/containers/"+url.PathEscape(sp.container)+"/start", nil)
}

func (d *Docker) Stop(ctx context.Context, name string) error {
	sp, err := resolve(name)
	if err != nil {
		return err
	}
	return d.do(ctx, http.MethodPost,
		fmt.Sprintf("/containers/%s/stop?t=%d",
			url.PathEscape(sp.container), stopGraceSeconds), nil)
}

func (d *Docker) Restart(ctx context.Context, name string) error {
	sp, err := resolve(name)
	if err != nil {
		return err
	}
	return d.do(ctx, http.MethodPost,
		fmt.Sprintf("/containers/%s/restart?t=%d",
			url.PathEscape(sp.container), stopGraceSeconds), nil)
}
