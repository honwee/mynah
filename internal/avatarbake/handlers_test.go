package avatarbake

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An idle bake animates ONE portrait image (LivePortrait). A baked avatar
// resolves to a DIRECTORY of pre-encoded frames — widening the shared resolver
// to cover bakes (so session dispatch and preload could handle them) also made
// that directory reach this handler, where it passed os.Stat and would have
// failed deep inside the worker instead of here.
func TestCreateRejectsNonPortraitSource(t *testing.T) {
	dir := t.TempDir()
	bakeDir := filepath.Join(dir, "leiya_mt")
	if err := os.Mkdir(bakeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	portrait := filepath.Join(dir, "f.jpg")
	if err := os.WriteFile(portrait, []byte("jpg"), 0o644); err != nil {
		t.Fatal(err)
	}

	e := &Extension{resolveSource: func(id string) string {
		if id == "bake:leiya_mt" {
			return bakeDir
		}
		return portrait
	}}

	// db is never reached: every path check runs before the first query.
	rec := httptest.NewRecorder()
	e.create(nil)(rec, httptest.NewRequest("POST", "/api/v1/avatars/bake",
		strings.NewReader(`{"avatar_id":"bake:leiya_mt"}`)))
	if rec.Code != 400 {
		t.Fatalf("baked avatar as bake source: got %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "source portrait") {
		t.Errorf("unhelpful reason: %s", rec.Body.String())
	}

	// A missing source is still its own (different) 400.
	rec = httptest.NewRecorder()
	e2 := &Extension{resolveSource: func(string) string { return filepath.Join(dir, "gone.jpg") }}
	e2.create(nil)(rec, httptest.NewRequest("POST", "/api/v1/avatars/bake", strings.NewReader(`{"avatar_id":"f"}`)))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "not found on server") {
		t.Errorf("missing source: %d %s", rec.Code, rec.Body.String())
	}
}
