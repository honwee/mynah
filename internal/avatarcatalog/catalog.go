// Package avatarcatalog is the OSS registry of built-in digital-human
// portraits. It maps the avatar id stored in config.avatar.current to a
// portrait file the avatar worker loads as its speaking source (SessionSpec
// .cond_image). It is a pure data + resolver leaf package (no I/O, no other
// internal deps) so both the session manager and the control plane can import
// it without an import cycle.
package avatarcatalog

import (
	"path"
	"strings"
	"sync"
)

// Preset is a built-in, packaged portrait the console can offer as a selectable
// digital-human appearance.
type Preset struct {
	ID   string // logical id stored in config.avatar.current, e.g. "builtin:f"
	Name string // console display label
	File string // basename under the avatar-assets dir; "" = no override (worker keeps its boot --avatar)
	// Engine is which engine can actually render this portrait, or "" for
	// "any". A single JPEG is FlashHead-shaped material: MuseTalk wants a baked
	// directory and, handed a file path, logs "unknown avatar" and keeps the
	// face it booted with — so on a mixed pool an unattributed portrait means
	// the visitor meets whichever person the dispatcher happened to pick.
	Engine string
}

// Builtins is the OSS catalog. "default" maps to the bundled default portrait
// (the same file the worker boots on via --avatar), so selecting it explicitly
// switches a worker BACK to the default face even after another preset loaded
// it (the worker process keeps its loaded avatar across sessions). Add new
// built-ins here and drop the matching portrait into workers/avatar/assets/.
//
// "default" alone carries NO engine. It is also the shipped fallback for
// config.avatar.current, so on the many deployments that never picked a face it
// means "whatever this worker boots with" rather than an intent — binding it to
// flashhead would refuse every session on a musetalk-only pool. Any portrait an
// operator picks DELIBERATELY is attributed, because there the wrong engine
// silently shows the wrong person.
var Builtins = []Preset{
	{ID: "default", Name: "默认", File: "mynah-default.jpg"},
	{ID: "f", Name: "F", File: "mynah-f.jpg", Engine: "flashhead"},
	// Nova: a second FlashHead preset, shipped as a HALF-BODY presenter. The
	// cond image is the 720x1280 canvas (nova.halfbody.png); the worker renders
	// the 512 face and pastes it back at the bbox recorded in nova.bbox.json
	// using nova.blend.npz, so speaking frames and the baked idle share one
	// geometry. See workers/avatar/halfbody.py.
	{ID: "nova", Name: "Nova", File: "nova.halfbody.png", Engine: "flashhead"},
	// Luna: same half-body FlashHead recipe, night-time indoor look (dark wall,
	// warm lamp). Engine cond is luna.cond.png (loosened crop, see
	// halfbody.py recrop); paste-back via luna.blend.npz.
	{ID: "luna", Name: "Luna", File: "luna.halfbody.png", Engine: "flashhead"},
}

// Resolve maps a stored avatar id to a worker-readable cond_image path.
// assetsDir is the avatar-assets base dir as the worker sees it (e.g.
// /app/workers/avatar/assets). It returns "" — meaning "no override, keep the
// worker's currently-loaded avatar" — only for the empty id and for ids it does
// not know (unknown ids and "trained:<jobId>" likenesses, which the EE overlay
// resolves, not here). Known built-ins (including "default") resolve to their
// packaged portrait so the catalog is fully bidirectional. Returned paths
// always use forward slashes (container paths), even when cored runs on a
// non-Linux dev host.
func Resolve(id, assetsDir string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	for _, p := range Builtins {
		if p.File != "" && p.ID == id {
			return path.Join(assetsDir, p.File)
		}
	}
	return "" // trained:* and unknown ids: no override (EE/fallback territory)
}

// ---- baked avatars ----------------------------------------------------------
//
// Builtins above are single portrait FILES, which is what FlashHead speaks.
// MuseTalk and wav2lipLS instead consume a baked DIRECTORY (frames, masks,
// latents). Both end up in SessionSpec.cond_image — the worker knows which
// shape it wants — but they are discovered differently, so baked avatars are
// registered at startup from a filesystem scan rather than compiled in.
//
// Each entry carries its engine because the two are not interchangeable: a
// MuseTalk bake cannot be spoken by FlashHead and vice versa. The console shows
// the engine next to each avatar so an operator cannot bind a channel to an
// avatar its pool cannot render.

// Baked is one discovered avatar directory.
type Baked struct {
	ID     string `json:"id"`     // stable id, e.g. "bake:leiya_mt"
	Name   string `json:"name"`   // display label (the directory name)
	Engine string `json:"engine"` // which engine can speak it
	Path   string `json:"path"`   // worker-visible directory path
}

// BakedPrefix marks ids that resolve to a baked directory.
const BakedPrefix = "bake:"

// bakedMu guards bakedRegistry. Needed because the set is now RESCANNED at
// runtime (a freshly baked avatar must appear without restarting cored), while
// ResolveAny reads it on every session start — writer and readers are
// concurrent, which the startup-only version never had to handle.
var (
	bakedMu       sync.RWMutex
	bakedRegistry []Baked
)

// SetBaked replaces the discovered set. Called at startup and on each rescan;
// the package stays I/O-free by taking the result of a scan done by the caller.
func SetBaked(list []Baked) {
	bakedMu.Lock()
	bakedRegistry = append([]Baked(nil), list...)
	bakedMu.Unlock()
}

// BakedAvatars returns the discovered baked avatars.
func BakedAvatars() []Baked {
	bakedMu.RLock()
	defer bakedMu.RUnlock()
	return append([]Baked(nil), bakedRegistry...)
}

// ResolveAny maps any avatar id to the cond_image value the worker expects:
// a portrait path for built-ins, a bake directory for "bake:*". Returns "" for
// ids it does not know, which the worker reads as "keep your current avatar".
func ResolveAny(id, assetsDir string) string {
	id = strings.TrimSpace(id)
	if after, isBake := strings.CutPrefix(id, BakedPrefix); isBake {
		bakedMu.RLock()
		defer bakedMu.RUnlock()
		for _, b := range bakedRegistry {
			if b.ID == id || b.Name == after {
				return b.Path
			}
		}
		return ""
	}
	return Resolve(id, assetsDir)
}

// EngineFor reports which engine an avatar id needs, "" for ids that carry no
// engine intent (unknown/trained ids, and "default" — see Builtins).
//
// This is the single source of engine attribution: the channel registry derives
// a channel's engine from it, the concurrency quota counts per engine with it,
// and session dispatch filters the worker pool by it. On a mixed pool an
// unattributed avatar is not an error but a coin flip — a MuseTalk worker handed
// a portrait file logs "unknown avatar" and keeps its own face — so anything
// whose engine IS knowable must be attributed here rather than at any one of the
// three call sites.
func EngineFor(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	for _, p := range Builtins {
		if p.ID == id {
			return p.Engine
		}
	}
	bakedMu.RLock()
	defer bakedMu.RUnlock()
	for _, b := range bakedRegistry {
		if b.ID == id {
			return b.Engine
		}
	}
	return ""
}
