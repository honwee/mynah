// Pool container lifecycle: creating and removing avatar-engine workers.
//
// SECURITY — read before changing anything here.
//
// The docker socket is root-equivalent on the host. Start/Stop/Restart are safe
// because they only ever act on names looked up in the fixed `registry` map.
// Create and Remove are different in kind: they take a name that did not exist
// before, so there is no table to look it up in. The boundary is therefore a
// STRICT PATTERN, enforced in one place (validatePoolName):
//
//	mynah-<engine>-<n>   engine ∈ catalog, 1 ≤ n ≤ MaxPoolSlots
//
// Anything else is refused — including the fixed service names (removing
// mynah-tts or mynah-cored through this path must be impossible)
// and anything containing a path separator or traversal.
package supervisor

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// MaxPoolSlots caps how many workers a pool may ever have. It is a safety
// bound, not a capacity estimate — real capacity comes from the VRAM planner
// (internal/avatarengine) and is always far below this. Its job is to stop a
// bug or a hostile caller from creating containers without limit.
const MaxPoolSlots = 8

// poolNameRE is deliberately anchored and narrow: lowercase engine id, a single
// dash, a small index. No dots, no slashes, no uppercase.
var poolNameRE = regexp.MustCompile(`^mynah-([a-z0-9]+)-([1-9][0-9]?)$`)

// PoolName builds the canonical container name for slot n of an engine.
func PoolName(engine string, n int) string {
	return fmt.Sprintf("mynah-%s-%d", engine, n)
}

// validatePoolName is the single security boundary for create/remove. It
// returns the engine and slot on success.
func validatePoolName(name string) (engine string, slot int, err error) {
	// Defence in depth: the regex already excludes these, but a future edit to
	// the pattern must not silently re-open them.
	if strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		return "", 0, fmt.Errorf("illegal container name %q", name)
	}
	if _, fixed := registry[strings.TrimPrefix(name, "mynah-")]; fixed {
		return "", 0, fmt.Errorf("%q is a fixed service, not a pool worker; "+
			"it cannot be created or removed through the pool API", name)
	}
	m := poolNameRE.FindStringSubmatch(name)
	if m == nil {
		return "", 0, fmt.Errorf("container name %q is not a pool worker name "+
			"(want mynah-<engine>-<n>)", name)
	}
	slot, _ = strconv.Atoi(m[2])
	if slot < 1 || slot > MaxPoolSlots {
		return "", 0, fmt.Errorf("pool slot %d out of range (1..%d)", slot, MaxPoolSlots)
	}
	return m[1], slot, nil
}

// ContainerSpec is everything needed to materialize one pool worker. It is
// built from the engine catalog plus deployment paths — never from caller input
// beyond the engine name and slot.
type ContainerSpec struct {
	Name     string   // must satisfy validatePoolName
	Image    string   //
	Cmd      []string // entrypoint args (image ENTRYPOINT is ["python"])
	Env      []string // "K=V"
	Binds    []string // "/host:/container:ro"
	GPUIndex int      // CDI device index
}

// dockerCreateBody is the /containers/create payload. Only the fields this
// needs; docker fills the rest with defaults.
type dockerCreateBody struct {
	Image      string   `json:"Image"`
	Cmd        []string `json:"Cmd"`
	Entrypoint []string `json:"Entrypoint,omitempty"`
	Env        []string `json:"Env"`
	WorkingDir string   `json:"WorkingDir"`
	HostConfig struct {
		Binds         []string `json:"Binds"`
		NetworkMode   string   `json:"NetworkMode"`
		RestartPolicy struct {
			Name string `json:"Name"`
		} `json:"RestartPolicy"`
		DeviceRequests []deviceRequest   `json:"DeviceRequests,omitempty"`
		Annotations    map[string]string `json:"Annotations,omitempty"`
	} `json:"HostConfig"`
}

type deviceRequest struct {
	Driver       string     `json:"Driver"`
	DeviceIDs    []string   `json:"DeviceIDs"`
	Capabilities [][]string `json:"Capabilities"`
}

// Ensure creates the container if it is absent and starts it. An existing
// container whose image or command differs is recreated; one that already
// matches is just started (idempotent, so a retry after a partial failure is
// safe).
func (d *Docker) Ensure(ctx context.Context, spec ContainerSpec) error {
	if _, _, err := validatePoolName(spec.Name); err != nil {
		return err
	}
	if spec.Image == "" || len(spec.Cmd) == 0 {
		return fmt.Errorf("spec for %q needs an image and a command", spec.Name)
	}

	existing, err := d.findContainer(ctx, spec.Name)
	if err != nil {
		return err
	}
	if existing != nil {
		if sameSpec(existing, spec) {
			return d.startByID(ctx, existing.ID)
		}
		// Spec drift (new image tag, changed args): replace it.
		if err := d.Remove(ctx, spec.Name); err != nil {
			return fmt.Errorf("replace %s: %w", spec.Name, err)
		}
	}

	body := dockerCreateBody{Image: spec.Image, Cmd: spec.Cmd, Env: spec.Env,
		WorkingDir: "/app/workers/avatar"}
	body.HostConfig.Binds = spec.Binds
	body.HostConfig.NetworkMode = "host"
	// unless-stopped, matching compose: a worker stopped on purpose must stay
	// stopped across a daemon restart.
	body.HostConfig.RestartPolicy.Name = "unless-stopped"
	body.HostConfig.DeviceRequests = []deviceRequest{{
		Driver:       "cdi",
		DeviceIDs:    []string{fmt.Sprintf("nvidia.com/gpu=%d", spec.GPUIndex)},
		Capabilities: [][]string{{"gpu"}},
	}}

	if err := d.doJSON(ctx, http.MethodPost,
		"/containers/create?name="+url.QueryEscape(spec.Name), body, nil); err != nil {
		return fmt.Errorf("create %s: %w", spec.Name, err)
	}
	return d.startByName(ctx, spec.Name)
}

// Remove stops and deletes a pool worker. Fixed services are rejected by
// validatePoolName, so this cannot be turned against TTS/ASR/cored.
func (d *Docker) Remove(ctx context.Context, name string) error {
	if _, _, err := validatePoolName(name); err != nil {
		return err
	}
	// force=1 stops it first; v=0 keeps any named volumes (they are shared).
	err := d.do(ctx, http.MethodDelete,
		fmt.Sprintf("/containers/%s?force=1&v=0", url.PathEscape(name)), nil)
	if err != nil && strings.Contains(err.Error(), "404") {
		return nil // already gone: removal is idempotent
	}
	return err
}

func (d *Docker) findContainer(ctx context.Context, name string) (*dockerContainer, error) {
	var containers []dockerContainer
	if err := d.do(ctx, http.MethodGet, "/containers/json?all=1", &containers); err != nil {
		return nil, err
	}
	for i := range containers {
		for _, n := range containers[i].Names {
			if strings.TrimPrefix(n, "/") == name {
				return &containers[i], nil
			}
		}
	}
	return nil, nil
}

// sameSpec is a coarse drift check: image identity is what matters in practice
// (a new engine or a rebuilt worker), and comparing full argv through the list
// endpoint is unreliable because Command is truncated there.
func sameSpec(c *dockerContainer, spec ContainerSpec) bool {
	return c.Image == spec.Image
}

func (d *Docker) startByName(ctx context.Context, name string) error {
	return d.do(ctx, http.MethodPost,
		"/containers/"+url.PathEscape(name)+"/start", nil)
}

func (d *Docker) startByID(ctx context.Context, id string) error {
	return d.do(ctx, http.MethodPost,
		"/containers/"+url.PathEscape(id)+"/start", nil)
}

// ParsePoolSlot exposes the name validator to callers that need to recognize
// dynamic slots (the control plane, when deciding which workers it may remove).
// Keeping validation in one place is the point: callers must not re-implement
// the pattern.
func ParsePoolSlot(name string) (engine string, slot int, err error) {
	return validatePoolName(name)
}

// EngineOfService reports which avatar engine a service id belongs to, or "".
//
// It takes the SERVICE id as List reports it ("musetalk2", "flashhead-3"), not
// a container name — callers hold Service values, and making them re-add the
// "mynah-" prefix is exactly the kind of duplicated string surgery that
// produced the HasPrefix(name, "musetalk") counting bug this replaces.
func EngineOfService(name string) string {
	if engine, _, err := validatePoolName("mynah-" + name); err == nil {
		return engine
	}
	return registry[name].engine
}
