package avatarengine

import (
	"fmt"
	"strings"
	"testing"
)

// fakeStat reports the given paths as present and everything else as missing.
func fakeStat(present ...string) func(string) error {
	set := map[string]bool{}
	for _, p := range present {
		set[p] = true
	}
	return func(p string) error {
		if set[p] {
			return nil
		}
		return fmt.Errorf("no such file")
	}
}

// A switch must be refused BEFORE any container is created when material is
// missing. Draining the pool and then discovering the new engine cannot start
// is far worse than a refusal.
func TestValidateMaterialNamesTheMissingPath(t *testing.T) {
	mt, _ := Get("musetalk")
	m := Material{ModelsDir: "/m", RepoDir: "/r", AvatarDir: "/a"}

	if reason := mt.ValidateMaterial(m, fakeStat("/m", "/r", "/a")); reason != "" {
		t.Fatalf("complete material rejected: %s", reason)
	}

	reason := mt.ValidateMaterial(m, fakeStat("/m", "/r")) // avatar dir absent
	if reason == "" {
		t.Fatal("missing avatar dir accepted")
	}
	if !strings.Contains(reason, "/a") {
		t.Errorf("reason should name the missing path, got: %s", reason)
	}
}

func TestValidateMaterialRejectsUnconfigured(t *testing.T) {
	mt, _ := Get("musetalk")
	reason := mt.ValidateMaterial(Material{}, fakeStat())
	if reason == "" {
		t.Fatal("empty material accepted")
	}
	for _, want := range []string{"models_dir", "repo_dir", "avatar_dir"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason should mention %s, got: %s", want, reason)
		}
	}
}

// Engines need different material: flashhead takes a portrait image and no
// baked directory, so applying musetalk's requirements to it would wrongly
// block a valid configuration.
func TestMaterialRequirementsDifferPerEngine(t *testing.T) {
	fh, _ := Get("flashhead")
	m := Material{RepoDir: "/fh", AvatarImage: "/fh/examples/p.jpg"}
	if reason := fh.ValidateMaterial(m, fakeStat("/fh", "/fh/examples/p.jpg")); reason != "" {
		t.Fatalf("valid flashhead material rejected: %s", reason)
	}
	// The same material is NOT enough for musetalk.
	mt, _ := Get("musetalk")
	if reason := mt.ValidateMaterial(m, fakeStat("/fh", "/fh/examples/p.jpg")); reason == "" {
		t.Error("musetalk accepted flashhead-shaped material")
	}
}

// The port must reach the worker's argv, or every slot would bind the same
// port and only the first would start.
func TestBuildWorkerSpecCarriesPortAndPaths(t *testing.T) {
	mt, _ := Get("musetalk")
	m := Material{
		ModelsDir: "/m", RepoDir: "/r", AvatarDir: "/a", GPU: 1,
		WorkersDir: "/w", GenDir: "/g",
	}
	spec, err := mt.BuildWorkerSpec(m, 9418)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(spec.Cmd, " ")
	for _, want := range []string{"--port=9418", "--models-dir=/m", "--musetalk-repo=/r", "--avatar-dir=/a"} {
		if !strings.Contains(joined, want) {
			t.Errorf("cmd missing %q: %v", want, spec.Cmd)
		}
	}
	if spec.GPUIndex != 1 {
		t.Errorf("GPUIndex = %d, want 1", spec.GPUIndex)
	}
	// Model paths must be mounted at the SAME path, since they are passed as
	// CLI args that the container has to be able to resolve.
	bindJoined := strings.Join(spec.Binds, " ")
	for _, want := range []string{"/m:/m:ro", "/r:/r:ro", "/a:/a:ro"} {
		if !strings.Contains(bindJoined, want) {
			t.Errorf("binds missing identical-path mount %q: %v", want, spec.Binds)
		}
	}
}

func TestBuildWorkerSpecRefusesUnknownEngine(t *testing.T) {
	unknown := Engine{Name: "nope", Image: "i", Script: "s"}
	if _, err := unknown.BuildWorkerSpec(Material{}, 9418); err == nil {
		t.Error("built a spec for an unknown engine")
	}
}

func TestFlashheadSpecUsesPortraitNotBakeDir(t *testing.T) {
	fh, _ := Get("flashhead")
	m := Material{RepoDir: "/fh", AvatarImage: "/fh/p.jpg", GPU: 0}
	spec, err := fh.BuildWorkerSpec(m, 9419)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(spec.Cmd, " ")
	if !strings.Contains(joined, "--avatar=/fh/p.jpg") {
		t.Errorf("flashhead cmd should take --avatar, got %v", spec.Cmd)
	}
	if strings.Contains(joined, "--avatar-dir") {
		t.Errorf("flashhead must not be given a bake dir: %v", spec.Cmd)
	}
	if !strings.Contains(strings.Join(spec.Env, " "), "FLASHHEAD_DIR=/fh") {
		t.Errorf("flashhead needs FLASHHEAD_DIR in env, got %v", spec.Env)
	}
}

// The workers tree is mounted twice: at /app/workers (what the worker's own code
// expects) AND at its host path. Built-in portraits live under it, and cored
// hands their absolute path to the worker as cond_image — a path that is also
// read by the idle-bake worker, which runs bare on the host. With only
// /app/workers, one of the two always got a path it could not resolve.
func TestWorkersTreeIsMountedAtBothPaths(t *testing.T) {
	for _, name := range []string{"musetalk", "flashhead"} {
		e, found := Get(name)
		if !found {
			t.Fatalf("engine %q missing from the catalog", name)
		}
		m := Material{
			ModelsDir: "/m", RepoDir: "/r", AvatarDir: "/a",
			AvatarImage: "/r/p.jpg",
			WorkersDir:  "/root/repo/workers", GenDir: "/root/repo/gen",
		}
		spec, err := e.BuildWorkerSpec(m, 9418)
		if err != nil {
			t.Fatal(err)
		}
		binds := strings.Join(spec.Binds, " ")
		for _, want := range []string{
			"/root/repo/workers:/app/workers",
			"/root/repo/workers:/root/repo/workers:ro",
		} {
			if !strings.Contains(binds, want) {
				t.Errorf("%s binds missing %q: %v", name, want, spec.Binds)
			}
		}
	}
}

// Degenerate case: when the workers dir already IS /app/workers, the host-path
// mount would be a duplicate target and docker refuses the container outright.
func TestWorkersTreeNotMountedTwiceOnItself(t *testing.T) {
	mt, _ := Get("musetalk")
	spec, err := mt.BuildWorkerSpec(Material{
		ModelsDir: "/m", RepoDir: "/r", AvatarDir: "/a",
		WorkersDir: "/app/workers",
	}, 9418)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, b := range spec.Binds {
		if strings.HasPrefix(b, "/app/workers:") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("workers dir mounted %d times, want 1: %v", n, spec.Binds)
	}
}
