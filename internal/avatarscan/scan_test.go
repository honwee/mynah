package avatarscan

import (
	"os"
	"path/filepath"
	"testing"

	"mynah/internal/avatarcatalog"
)

// stageBake writes the artifacts ScanBakes checks for.
func stageBake(t *testing.T, root, name string, complete bool) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "full_imgs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "full_imgs", "00000000.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	markers := []string{"coords.pkl", "mask_coords.pkl", "latents.pt"}
	if !complete {
		markers = markers[:1] // an aborted bake: some artifacts never written
	}
	for _, m := range markers {
		if err := os.WriteFile(filepath.Join(dir, m), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A bake is recognized by CONTENT, not by being a directory: an aborted bake
// would otherwise be offered as a selectable avatar and fail at session start.
func TestScanBakesIgnoresIncompleteBakes(t *testing.T) {
	root := t.TempDir()
	stageBake(t, root, "good", true)
	stageBake(t, root, "aborted", false)
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := ScanBakes(root)
	if len(got) != 1 {
		t.Fatalf("got %d bakes, want only the complete one: %+v", len(got), got)
	}
	if got[0].Name != "good" {
		t.Errorf("found %q, want good", got[0].Name)
	}
	if got[0].Engine != "musetalk" {
		t.Errorf("engine = %q, want musetalk — binding depends on it", got[0].Engine)
	}
	if got[0].ID != "bake:good" {
		t.Errorf("id = %q, want the bake: prefix", got[0].ID)
	}
}

// The reason rescan exists: baking happens outside cored, so a bake finished
// after startup must become visible without a restart.
func TestRescanPicksUpABakeCreatedAfterStartup(t *testing.T) {
	root := t.TempDir()
	stageBake(t, root, "first", true)

	avatarcatalog.SetBaked(ScanBakes(root)) // "startup"
	t.Cleanup(func() { avatarcatalog.SetBaked(nil) })
	if n := len(avatarcatalog.BakedAvatars()); n != 1 {
		t.Fatalf("startup scan found %d, want 1", n)
	}

	stageBake(t, root, "second", true) // baked while cored runs

	avatarcatalog.SetBaked(ScanBakes(root)) // rescan
	got := avatarcatalog.BakedAvatars()
	if len(got) != 2 {
		t.Fatalf("after rescan got %d avatars, want 2 — a new bake stayed invisible", len(got))
	}
	if avatarcatalog.ResolveAny("bake:second", "") == "" {
		t.Error("the new bake does not resolve; a channel could not bind to it")
	}
}

// A missing directory must not be an error — it just means no baked avatars.
func TestScanBakesToleratesMissingRoot(t *testing.T) {
	if got := ScanBakes(filepath.Join(t.TempDir(), "nope")); got != nil {
		t.Errorf("missing root returned %+v, want nil", got)
	}
	if got := ScanBakes(""); got != nil {
		t.Errorf("empty root returned %+v, want nil", got)
	}
}
