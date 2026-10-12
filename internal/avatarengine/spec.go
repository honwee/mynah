// Building a worker container spec for a given engine.
//
// Every pool worker this server creates is built from a declaration here, not
// cloned from a running container. Cloning (supervisor.SpecFromReference) was
// the original approach and carried the assumption "the pool already runs the
// engine you want more of" — which a mixed pool breaks by definition: the engine
// being added is precisely the one with nothing running to copy, and its image,
// arguments and mounts all differ. So each engine's material has to be declared,
// and this file turns a declaration into a container spec.
//
// Material is validated before anything is created. A half-configured change
// that produces containers which crash-loop is worse than a refusal: the pool
// would drain to zero while the operator works out what is missing.
package avatarengine

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Material is the per-engine deployment input: where its model weights, engine
// repo and speaking material live ON THE HOST. Paths are bind-mounted at the
// same path inside the container, because the worker receives them as CLI args.
type Material struct {
	// ModelsDir / RepoDir: musetalk needs both (weights + the importable
	// upstream package). flashhead needs only its own dir.
	ModelsDir string `json:"models_dir,omitempty"`
	RepoDir   string `json:"repo_dir,omitempty"`
	// AvatarDir is a baked avatar directory (musetalk / wav2lipls).
	AvatarDir string `json:"avatar_dir,omitempty"`
	// AvatarImage is a single portrait (flashhead).
	AvatarImage string `json:"avatar_image,omitempty"`
	// Ckpt is the model checkpoint (wav2lipls).
	Ckpt string `json:"ckpt,omitempty"`
	// GPU is the CDI device index workers of this engine run on.
	GPU int `json:"gpu"`
	// WorkersDir / GenDir are the repo paths bind-mounted for live python +
	// generated protobuf. Same for every engine.
	WorkersDir string `json:"workers_dir,omitempty"`
	GenDir     string `json:"gen_dir,omitempty"`
	CacheVolume string `json:"cache_volume,omitempty"`
}

// Validate checks that everything this engine needs is declared AND present on
// disk. Returns a human-readable reason on failure — the console shows it
// verbatim, because "which path is missing" is the only useful answer.
//
// These are HOST paths, and this runs wherever cored runs. When cored is itself
// containerized, the material has to be bind-mounted into it (read-only) at the
// same paths, or every engine looks unready and no pool can be composed. See
// MATERIAL_ROOT in deploy/compose/docker-compose.full.yml.
//
// statFn is injectable for tests; nil uses the real filesystem.
func (e Engine) ValidateMaterial(m Material, statFn func(string) error) string {
	if statFn == nil {
		statFn = func(p string) error {
			_, err := os.Stat(p)
			return err
		}
	}
	type req struct {
		label string
		path  string
		dir   bool
	}
	var reqs []req
	switch e.Name {
	case "musetalk":
		reqs = []req{
			{"模型目录 (models_dir)", m.ModelsDir, true},
			{"MuseTalk 仓库 (repo_dir)", m.RepoDir, true},
			{"烘焙素材目录 (avatar_dir)", m.AvatarDir, true},
		}
	case "flashhead":
		reqs = []req{
			{"FlashHead 目录 (repo_dir)", m.RepoDir, true},
			{"肖像图 (avatar_image)", m.AvatarImage, false},
		}
	case "wav2lipls":
		reqs = []req{
			{"烘焙素材目录 (avatar_dir)", m.AvatarDir, true},
			{"模型权重 (ckpt)", m.Ckpt, false},
		}
	default:
		return fmt.Sprintf("未知引擎 %q", e.Name)
	}
	var missing []string
	for _, r := range reqs {
		if strings.TrimSpace(r.path) == "" {
			missing = append(missing, r.label+"：未配置")
			continue
		}
		if err := statFn(r.path); err != nil {
			missing = append(missing, fmt.Sprintf("%s：%s 不存在", r.label, r.path))
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return "素材未就绪 — " + strings.Join(missing, "；")
	}
	return ""
}

// WorkerSpec is the engine-agnostic description of a worker container, handed
// to the supervisor to materialize. Kept separate from supervisor.ContainerSpec
// so this package does not depend on the docker layer.
type WorkerSpec struct {
	Image    string
	Cmd      []string
	Env      []string
	Binds    []string
	GPUIndex int
}

// BuildWorkerSpec turns engine + material + port into a runnable container
// description. Mounts use identical host and container paths so the CLI args
// stay valid inside the container.
//
// This is deliberately PURE: it does not touch the filesystem. Material
// readiness is a separate precheck (ValidateMaterial) that callers run first —
// they need its reason string for the error response anyway, and keeping the
// two apart makes spec construction testable without staging real directories.
func (e Engine) BuildWorkerSpec(m Material, port int) (WorkerSpec, error) {
	spec := WorkerSpec{
		Image:    e.Image,
		GPUIndex: m.GPU,
		Env: []string{
			"PERSONALIVE_GEN=/app/gen/python",
			// CDI maps the requested device to index 0 inside the container.
			"CUDA_VISIBLE_DEVICES=0",
			"XDG_CACHE_HOME=/cache",
			"TMPDIR=/cache/tmp",
		},
	}
	same := func(p string) string { return p + ":" + p + ":ro" }
	if m.WorkersDir != "" {
		spec.Binds = append(spec.Binds, m.WorkersDir+":/app/workers")
		// ...and again at the HOST path, read-only. /app/workers alone is what
		// broke built-in portraits: cored hands a worker an absolute path under
		// this tree as cond_image, and that same string is also read by processes
		// with a different view of the filesystem — the idle-bake worker runs
		// bare on the host, where /app does not exist. Binding the tree at its
		// own path means "the portrait lives here" is one fact rather than three
		// translations. Costs nothing: same inode, read-only.
		if m.WorkersDir != "/app/workers" {
			spec.Binds = append(spec.Binds, same(m.WorkersDir))
		}
	}
	if m.GenDir != "" {
		spec.Binds = append(spec.Binds, m.GenDir+":/app/gen:ro")
	}
	if m.CacheVolume != "" {
		spec.Binds = append(spec.Binds, m.CacheVolume+":/cache")
	}

	switch e.Name {
	case "musetalk":
		spec.Cmd = []string{e.Script,
			fmt.Sprintf("--port=%d", port),
			"--models-dir=" + m.ModelsDir,
			"--musetalk-repo=" + m.RepoDir,
			"--avatar-dir=" + m.AvatarDir,
		}
		spec.Binds = append(spec.Binds, same(m.RepoDir), same(m.ModelsDir), same(m.AvatarDir))
	case "flashhead":
		spec.Cmd = []string{e.Script,
			fmt.Sprintf("--port=%d", port),
			"--avatar=" + m.AvatarImage,
		}
		spec.Env = append(spec.Env, "FLASHHEAD_DIR="+m.RepoDir,
			// Compile off: ~14s cold start vs ~139s, and steady-state throughput
			// is still comfortably realtime.
			"FLASHHEAD_COMPILE_MODEL=0", "FLASHHEAD_COMPILE_VAE=0")
		spec.Binds = append(spec.Binds, same(m.RepoDir))
		if dir := filepath.Dir(m.AvatarImage); dir != "" && !strings.HasPrefix(m.AvatarImage, m.RepoDir) {
			spec.Binds = append(spec.Binds, same(dir))
		}
	case "wav2lipls":
		spec.Cmd = []string{e.Script,
			fmt.Sprintf("--port=%d", port),
			"--avatar-dir=" + m.AvatarDir,
			"--ckpt=" + m.Ckpt,
		}
		spec.Binds = append(spec.Binds, same(m.AvatarDir), same(filepath.Dir(m.Ckpt)))
	default:
		return WorkerSpec{}, fmt.Errorf("未知引擎 %q", e.Name)
	}
	return spec, nil
}
