// Discovering baked avatars on disk.
//
// avatarcatalog stays I/O-free (both the session manager and the control plane
// import it, and a leaf package with no filesystem access is easy to reason
// about), so the scan lives here and its result is registered into the catalog.
//
// A bake is recognized by its contents rather than its name: the files a
// MuseTalk worker actually needs. That way a half-finished or aborted bake
// directory is not offered as a selectable avatar.
package avatarscan

import (
	"os"
	"path/filepath"
	"sort"

	"mynah/internal/avatarcatalog"
)

// musetalkMarkers are the artifacts load_avatar_data() requires. All must be
// present for the directory to be a usable bake.
var musetalkMarkers = []string{"coords.pkl", "mask_coords.pkl", "latents.pt"}

// ScanBakes walks one level under root and returns the directories that look
// like finished MuseTalk bakes. A missing root is not an error — it just means
// no baked avatars are available.
func ScanBakes(root string) []avatarcatalog.Baked {
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []avatarcatalog.Baked
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if !isMuseTalkBake(dir) {
			continue
		}
		out = append(out, avatarcatalog.Baked{
			ID:     avatarcatalog.BakedPrefix + e.Name(),
			Name:   e.Name(),
			Engine: "musetalk",
			Path:   dir,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func isMuseTalkBake(dir string) bool {
	for _, m := range musetalkMarkers {
		if _, err := os.Stat(filepath.Join(dir, m)); err != nil {
			return false
		}
	}
	// full_imgs must exist and be non-empty; latents alone cannot render.
	imgs, err := os.ReadDir(filepath.Join(dir, "full_imgs"))
	return err == nil && len(imgs) > 0
}
