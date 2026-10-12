package mtbake

import (
	"os"
	"path/filepath"
	"testing"
)

// The name becomes BOTH a filesystem path component and a catalog id, so
// anything that could escape the bakes directory has to be refused here — this
// is the only validation between an HTTP form field and a bind-mounted path.
func TestValidateNameRejectsPathEscapes(t *testing.T) {
	for _, bad := range []string{
		"", "a", "../evil", "a/b", "a\\b", ".hidden", "-lead", "1digit",
		"UPPER", "with space", "semi;colon", "dollar$sign",
		"thisnameiswaytoolongtobeavalidavatardirectoryname",
	} {
		if err := ValidateName(bad); err == nil {
			t.Errorf("ValidateName(%q) accepted a name it must refuse", bad)
		}
	}
	for _, good := range []string{"leiya_mt", "lxgray", "a1", "x-y_z9"} {
		if err := ValidateName(good); err != nil {
			t.Errorf("ValidateName(%q) = %v, want accepted", good, err)
		}
	}
}

func stageOutput(t *testing.T, dir string, frames int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "full_imgs"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < frames; i++ {
		p := filepath.Join(dir, "full_imgs", string(rune('a'+i))+".png")
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPublishReplacesAnExistingAvatar(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "leiya")
	stageOutput(t, dest, 1) // the avatar already in production
	stage := filepath.Join(root, ".staging-7")
	stageOutput(t, stage, 3) // the rebake

	n, err := publish(stage, dest)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if n != 3 {
		t.Errorf("reported %d frames, want 3", n)
	}
	got, _ := filepath.Glob(filepath.Join(dest, "full_imgs", "*.png"))
	if len(got) != 3 {
		t.Errorf("destination has %d frames, want the 3 new ones", len(got))
	}
	// The staging dir must be consumed, not left behind: it lives INSIDE the
	// bakes dir, and a leftover would accumulate a full frame library per bake.
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Error("staging directory survived publish")
	}
	if _, err := os.Stat(dest + ".replaced"); !os.IsNotExist(err) {
		t.Error("the replaced-avatar backup was not cleaned up")
	}
}

// An empty bake must not replace a working avatar. This is the failure mode
// that matters: the bake script can exit 0 having detected no faces, and
// silently swapping a live appearance for an empty directory would take a
// channel down.
func TestPublishRefusesEmptyOutputAndKeepsTheOldAvatar(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "leiya")
	stageOutput(t, dest, 2)
	stage := filepath.Join(root, ".staging-8")
	if err := os.MkdirAll(filepath.Join(stage, "full_imgs"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := publish(stage, dest); err == nil {
		t.Fatal("publish accepted an empty bake")
	}
	got, _ := filepath.Glob(filepath.Join(dest, "full_imgs", "*.png"))
	if len(got) != 2 {
		t.Errorf("the existing avatar has %d frames, want its original 2", len(got))
	}
}

// Baking into a name that does not exist yet is the common case.
func TestPublishCreatesANewAvatar(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, ".staging-9")
	stageOutput(t, stage, 2)
	if _, err := publish(stage, filepath.Join(root, "brand_new")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "brand_new", "full_imgs")); err != nil {
		t.Errorf("new avatar not in place: %v", err)
	}
}

// A failed bake's message has to reach the console, because the alternative is
// the operator ssh-ing in to read container logs — the thing this pipeline
// exists to avoid.
func TestDescribeSurfacesTheLastOutputLine(t *testing.T) {
	got := describe(nil, 1, "loading models\nTraceback...\nRuntimeError: CUDA out of memory")
	if want := "退出码 1：RuntimeError: CUDA out of memory"; got != want {
		t.Errorf("describe = %q, want %q", got, want)
	}
	if got := describe(nil, 137, ""); got != "退出码 137" {
		t.Errorf("describe with no output = %q", got)
	}
}

// The usable bbox_shift window is per-clip and printed exactly once, mixed into
// the bake's output. Getting this pattern wrong silently costs the operator the
// only guidance they have for a re-bake, so pin the real line verbatim.
func TestBBoxRangeIsParsedFromTheBakeOutput(t *testing.T) {
	for _, tc := range []struct{ line, want string }{
		{"bbox_shift adjust range: [-12~17], current: 0", "-12~17"},
		{"bbox_shift adjust range: [-3~4], current: -5", "-3~4"},
		// Upstream prints the lower bound with an explicit sign; a clip whose
		// landmarks give a negative upper bound must still parse.
		{"bbox_shift adjust range: [-20~-2], current: 0", "-20~-2"},
	} {
		m := bboxRangeRE.FindStringSubmatch(tc.line)
		if m == nil {
			t.Errorf("no match for %q", tc.line)
			continue
		}
		if m[1] != tc.want {
			t.Errorf("parsed %q from %q, want %q", m[1], tc.line, tc.want)
		}
	}
	// Progress lines and tracebacks share the stream; neither may match.
	for _, other := range []string{"PROGRESS 45", "250 input frames", ""} {
		if bboxRangeRE.MatchString(other) {
			t.Errorf("bboxRangeRE matched unrelated line %q", other)
		}
	}
}
