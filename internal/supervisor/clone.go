// Deriving a new pool worker's spec from an existing one.
//
// The alternative — restating image, mounts, env and model paths as cored
// flags — duplicates deployment truth that already lives in compose, and the
// two copies drift the first time someone edits one of them. A running worker
// IS the deployment config, so a new slot is built by cloning it and changing
// only the name and the port.
//
// The cost is that scaling up needs at least one worker to copy from — and that
// cost is why the pool no longer uses this. A mixed pool adds the engine that is
// NOT running, so there is nothing to clone; pool workers are now built from
// avatarengine.BuildWorkerSpec declarations instead. This is kept as a general
// docker utility, not as part of the pool path: if you find yourself reaching
// for it to create a worker, you are re-introducing the assumption that the pool
// runs a single engine.
package supervisor

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// portArgRE matches the --port=N argument this needs to rewrite.
var portArgRE = regexp.MustCompile(`^--port=\d+$`)

// SpecFromReference clones refContainer's runtime config into a spec for a new
// pool worker called name, serving on port.
//
// Only the port argument is rewritten; image, binds, env and every model path
// come across verbatim, so a cloned worker is byte-identical to its template
// apart from where it listens.
func (d *Docker) SpecFromReference(ctx context.Context, refContainer, name string, port int) (ContainerSpec, error) {
	if _, _, err := validatePoolName(name); err != nil {
		return ContainerSpec{}, err
	}
	var detail struct {
		Config struct {
			Image string   `json:"Image"`
			Cmd   []string `json:"Cmd"`
			Env   []string `json:"Env"`
		} `json:"Config"`
		HostConfig struct {
			Binds          []string `json:"Binds"`
			DeviceRequests []struct {
				DeviceIDs []string `json:"DeviceIDs"`
			} `json:"DeviceRequests"`
		} `json:"HostConfig"`
	}
	if err := d.do(ctx, http.MethodGet,
		"/containers/"+url.PathEscape(refContainer)+"/json", &detail); err != nil {
		return ContainerSpec{}, fmt.Errorf("read template container %s: %w", refContainer, err)
	}
	if detail.Config.Image == "" || len(detail.Config.Cmd) == 0 {
		return ContainerSpec{}, fmt.Errorf("template container %s has no image/command", refContainer)
	}

	cmd := make([]string, 0, len(detail.Config.Cmd))
	rewrote := false
	for _, a := range detail.Config.Cmd {
		if portArgRE.MatchString(a) {
			cmd = append(cmd, fmt.Sprintf("--port=%d", port))
			rewrote = true
			continue
		}
		cmd = append(cmd, a)
	}
	if !rewrote {
		// Without a --port to rewrite the clone would bind the template's port
		// and collide. Refuse rather than create a container that cannot start.
		return ContainerSpec{}, fmt.Errorf(
			"template container %s has no --port=N argument to rewrite; cannot derive a distinct worker",
			refContainer)
	}

	spec := ContainerSpec{
		Name:  name,
		Image: detail.Config.Image,
		Cmd:   cmd,
		Binds: detail.HostConfig.Binds,
		// Env from inspect includes image-baked vars (PATH, PYTHON_VERSION...).
		// Passing them back is harmless — they are what the template runs with.
		Env:      detail.Config.Env,
		GPUIndex: gpuFromDeviceRequests(detail.HostConfig.DeviceRequests),
	}
	return spec, nil
}

// gpuFromDeviceRequests pulls the CDI index out of "nvidia.com/gpu=N".
// Defaults to 0 when the template declares none.
func gpuFromDeviceRequests(reqs []struct {
	DeviceIDs []string `json:"DeviceIDs"`
}) int {
	for _, r := range reqs {
		for _, id := range r.DeviceIDs {
			if _, after, ok := strings.Cut(id, "nvidia.com/gpu="); ok {
				n := 0
				if _, err := fmt.Sscanf(after, "%d", &n); err == nil {
					return n
				}
			}
		}
	}
	return 0
}
