// Package supervisor gives the control plane a way to observe and control the
// local runtime components (TTS, ASR, avatar workers) without knowing how they
// are actually run.
//
// The interface is deliberately orchestrator-shaped — List/Start/Stop/Restart
// over named services — so the Docker implementation here can be swapped for a
// Kubernetes one (Deployments/Pods) without touching the HTTP handlers or the
// console. That is the migration path: single host today, cluster later.
//
// cored already runs as root on the host, so the Docker implementation talks to
// /var/run/docker.sock directly. Nothing extra is granted by this — but note
// that the socket IS full host control, so every mutating call goes through the
// name allowlist in Registry, never through a caller-supplied string.
package supervisor

import (
	"context"
	"time"
)

// State is a service's coarse lifecycle, normalized across backends.
type State string

const (
	StateRunning  State = "running" // up and (if a health probe exists) healthy
	StateStarting State = "starting"
	StateStopped  State = "stopped"
	StateFailed   State = "failed"
	StateMissing  State = "missing" // never created / image absent
	StateUnknown  State = "unknown"
)

// Service is one managed component as the console sees it.
type Service struct {
	// Name is the stable id used in API paths (e.g. "tts", "asr", "musetalk").
	Name string `json:"name"`
	// Label is the human-facing name shown in the console.
	Label string `json:"label"`
	// Description explains what breaks when this is stopped.
	Description string `json:"description"`
	State       State  `json:"state"`
	// Detail carries backend specifics (image tag, exit code, health string).
	Detail string `json:"detail,omitempty"`
	// StartedAt is zero when not running.
	StartedAt time.Time `json:"started_at,omitzero"`
	// Port is the service's listen port, for the console's health hint.
	Port int `json:"port,omitempty"`
	// GPU is the CDI device index this service occupies, -1 when it needs none.
	// Reflects ACTUAL runtime: an ASR started with --decode-only reports -1
	// because it loads no model.
	GPU int `json:"gpu"`
	// Mode is the runtime variant read off the container's own argv, e.g.
	// "decode-only" for an ASR serving as a bare Opus decoder. Empty = the
	// service's default mode. Read from the container rather than from config
	// so the console shows what is actually running, not what someone intended.
	Mode string `json:"mode,omitempty"`
	// Controllable is false for services the console must not stop (cored
	// itself would cut the branch it sits on).
	Controllable bool `json:"controllable"`
	// ColdStart is a rough time-to-ready, so the console can warn before a
	// restart that costs minutes (TTS pays model load + torch.compile).
	ColdStartSeconds int `json:"cold_start_seconds,omitempty"`
	// HostPID is the container's main process id in the HOST namespace, or 0
	// when not running. It is what ties a service to its VRAM claim in
	// nvidia-smi, which reports host pids — without it the capacity planner
	// cannot tell "memory the pool already holds" (reclaimable on resize) from
	// "memory someone else holds" (not reclaimable).
	HostPID int `json:"host_pid,omitempty"`
	// HostPIDs is EVERY process in the container, in the host namespace. The main
	// pid is not enough for attribution: vLLM's server forks the worker that
	// actually opens CUDA, so matching only HostPID reports the TTS as holding no
	// VRAM while it holds 11GB — and, worse, counts that 11GB as memory nobody
	// owns. Empty when the container is not running or docker could not be asked.
	HostPIDs []int `json:"host_pids,omitempty"`
	// Engine is the avatarengine catalog id this service runs, empty for
	// services that are not avatar workers. Fixed services declare it in the
	// registry; dynamic pool slots carry it in their container name. The mixed
	// pool counts workers per engine off this field — never off the name.
	Engine string `json:"engine,omitempty"`
}

// Supervisor observes and controls the local component set.
type Supervisor interface {
	// List reports every known service, running or not.
	List(ctx context.Context) ([]Service, error)
	// Start/Stop/Restart act on one service by Name. Implementations must
	// reject names that are not in their own registry rather than passing
	// them through to the backend.
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string) error
	Restart(ctx context.Context, name string) error
}
