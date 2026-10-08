// One-shot container runs: create → start → stream output → wait → remove.
//
// This is how cored runs GPU batch work (today: MuseTalk avatar baking) without
// keeping a service alive for it. A bake is minutes of GPU time that happens a
// handful of times in a deployment's life; a long-running worker for it would
// hold VRAM around the clock and need its own health, restart and pool logic.
//
// SECURITY: same rule as pool.go — a name that did not exist before cannot be
// looked up in a registry, so it must match a STRICT PATTERN. One-shot names
// live in their own namespace (mynah-oneshot-<token>) that provably
// cannot collide with a service or a pool worker.
package supervisor

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// oneshotNameRE is anchored and narrow: the caller supplies only a short
// alphanumeric token (a job id), never a full container name.
var oneshotNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

// OneshotName builds the container name for a one-shot run identified by token.
func OneshotName(token string) string { return "mynah-oneshot-" + token }

func validateOneshotName(name string) error {
	tok, ok := strings.CutPrefix(name, "mynah-oneshot-")
	if !ok || !oneshotNameRE.MatchString(tok) {
		return fmt.Errorf("container name %q is not a one-shot run name", name)
	}
	return nil
}

// RunSpec describes a single batch container.
type RunSpec struct {
	Name  string   // must satisfy validateOneshotName; use OneshotName()
	Image string   //
	Cmd   []string // args to the image ENTRYPOINT (or to Entrypoint, if set)
	// Entrypoint overrides the image's own. The bake image's entrypoint is
	// python; the frame-extraction step in the same image needs ffmpeg.
	Entrypoint []string
	Env        []string // "K=V"
	Binds      []string // "/host:/container" or ":ro"
	WorkingDir string   //
	// GPUIndex is the CDI device to attach, or -1 for a CPU-only run.
	GPUIndex int
}

// RunOnce runs spec to completion and returns the container's exit code.
//
// onLine receives each line the container writes (stdout and stderr merged, in
// arrival order). It is called from this goroutine, so a slow callback slows
// log consumption — keep it to parsing and a mutex-guarded store.
//
// The container is removed on every path, including ctx cancellation, so a
// canceled or crashed job never leaves a name behind that would collide with a
// retry of the same id.
func (d *Docker) RunOnce(ctx context.Context, spec RunSpec, onLine func(string)) (int, error) {
	if err := validateOneshotName(spec.Name); err != nil {
		return -1, err
	}
	if spec.Image == "" || len(spec.Cmd) == 0 {
		return -1, fmt.Errorf("run spec for %q needs an image and a command", spec.Name)
	}

	// A leftover from an interrupted previous attempt would make create fail
	// with a name conflict. Removal is idempotent.
	_ = d.removeContainer(context.WithoutCancel(ctx), spec.Name)

	body := dockerCreateBody{Image: spec.Image, Cmd: spec.Cmd, Env: spec.Env,
		Entrypoint: spec.Entrypoint, WorkingDir: spec.WorkingDir}
	body.HostConfig.Binds = spec.Binds
	// host networking for consistency with every other Mynah container;
	// a bake makes no network calls, but this keeps one networking story.
	body.HostConfig.NetworkMode = "host"
	// NOT unless-stopped: a batch job that exits nonzero must stay exited. A
	// restart policy here would turn a bad input into an infinite GPU loop.
	body.HostConfig.RestartPolicy.Name = "no"
	if spec.GPUIndex >= 0 {
		body.HostConfig.DeviceRequests = []deviceRequest{{
			Driver:       "cdi",
			DeviceIDs:    []string{fmt.Sprintf("nvidia.com/gpu=%d", spec.GPUIndex)},
			Capabilities: [][]string{{"gpu"}},
		}}
	}

	var created struct {
		ID string `json:"Id"`
	}
	if err := d.doJSON(ctx, http.MethodPost,
		"/containers/create?name="+url.QueryEscape(spec.Name), body, &created); err != nil {
		return -1, fmt.Errorf("create %s: %w", spec.Name, err)
	}
	// WithoutCancel: cleanup must run even when ctx is already canceled, which
	// is exactly the case where a stray container would otherwise be left.
	defer func() { _ = d.removeContainer(context.WithoutCancel(ctx), spec.Name) }()

	if err := d.startByID(ctx, created.ID); err != nil {
		return -1, fmt.Errorf("start %s: %w", spec.Name, err)
	}

	// Following the log stream doubles as waiting: docker closes it when the
	// container exits. Errors here are not fatal to the run — the exit code is
	// read from the container afterwards either way.
	if err := d.followLogs(ctx, created.ID, onLine); err != nil && ctx.Err() == nil {
		onLine("[log stream ended: " + err.Error() + "]")
	}

	return d.waitExit(ctx, created.ID)
}

// removeContainer deletes a container by name with no pattern check. PRIVATE
// on purpose: every caller must validate the name in its own namespace first.
func (d *Docker) removeContainer(ctx context.Context, name string) error {
	err := d.do(ctx, http.MethodDelete,
		fmt.Sprintf("/containers/%s?force=1&v=0", url.PathEscape(name)), nil)
	if err != nil && strings.Contains(err.Error(), "404") {
		return nil
	}
	return err
}

// followLogs streams the container's output, calling onLine per line.
//
// The stream is docker's multiplexed format (8-byte header per frame, since the
// container has no TTY), so it must be demuxed rather than read as plain text.
func (d *Docker) followLogs(ctx context.Context, id string, onLine func(string)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://docker/containers/"+url.PathEscape(id)+"/logs?follow=1&stdout=1&stderr=1", nil)
	if err != nil {
		return err
	}
	// d.http has a timeout sized for stop's grace period; a bake runs for
	// minutes to tens of minutes, so this stream needs a client with none.
	// ctx remains the deadline.
	resp, err := d.stream.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("logs: docker api %s", resp.Status)
	}

	br := bufio.NewReaderSize(resp.Body, 64*1024)
	var hdr [8]byte
	for {
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil // container exited
			}
			return err
		}
		n := binary.BigEndian.Uint32(hdr[4:])
		// A frame is one write() from the container, so it is small in practice.
		// Cap it anyway: an unbounded length from the socket must not become an
		// unbounded allocation.
		if n > 1<<20 {
			return fmt.Errorf("log frame too large (%d bytes)", n)
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(br, buf); err != nil {
			return err
		}
		for _, line := range strings.Split(strings.TrimRight(string(buf), "\n"), "\n") {
			// tqdm and friends redraw with \r; keep only the last redraw so a
			// progress bar cannot arrive as one enormous line.
			if i := strings.LastIndexByte(line, '\r'); i >= 0 {
				line = line[i+1:]
			}
			if line = strings.TrimSpace(line); line != "" {
				onLine(line)
			}
		}
	}
}

// waitExit reads the container's exit code once it has stopped.
func (d *Docker) waitExit(ctx context.Context, id string) (int, error) {
	// The log stream normally ends at exit, but a stream error can get here
	// early; /wait blocks until the container is actually gone.
	var res struct {
		StatusCode int `json:"StatusCode"`
	}
	wctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := d.doJSON(wctx, http.MethodPost,
		"/containers/"+url.PathEscape(id)+"/wait", nil, &res); err != nil {
		return -1, fmt.Errorf("wait: %w", err)
	}
	return res.StatusCode, nil
}
