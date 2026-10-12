// Parsing the per-avatar idle/action flags.
//
// Both used to be single-valued: --idle-ivf=<path> installed ONE loop for the
// whole process and --actions=wave=<path> one clip set, so every channel showed
// the same person while silent regardless of the avatar it was bound to. The
// keyed forms below attach each asset to an avatar id:
//
//	--idle-ivf=default=/idle/lin.h264f,bake:leiya_mt=/idle/leiya.h264f
//	--actions=wave@default=/act/lin_wave.h264f,wave@bake:leiya_mt=/act/leiya_wave.h264f
//
// Separators are picked around avatar ids, which contain colons ("bake:leiya_mt",
// "trained:42") but never "=" or "@":
//   - idle entries split on the FIRST "=", so the colons survive;
//   - action entries put the avatar AFTER an "@" rather than before a colon,
//     because "bake:leiya_mt:wave=..." cannot be split unambiguously.
//
// A bare path (no "=") stays valid and lands under "default" — that is the id a
// session gets when nothing is selected, so single-avatar deployments keep
// working untouched.
package app

import (
	"fmt"
	"strings"
)

// idleEntry is one parsed --idle-ivf item.
type idleEntry struct{ Avatar, Path string }

// actionEntry is one parsed --actions item.
type actionEntry struct{ Action, Avatar, Path string }

const defaultAvatarID = "default"

// parseIdleFlag parses --idle-ivf. Empty spec = no idle-local material at all
// (every avatar's idle is then rendered live by its engine).
func parseIdleFlag(spec string) ([]idleEntry, error) {
	var out []idleEntry
	seen := map[string]bool{}
	for _, ent := range splitCSV(spec) {
		avatar, path := defaultAvatarID, ent
		// First "=" only: the left side is an avatar id, which may contain
		// colons but never "=", and paths contain no "=" either.
		if i := strings.Index(ent, "="); i >= 0 {
			avatar, path = ent[:i], ent[i+1:]
		}
		avatar, path = strings.TrimSpace(avatar), strings.TrimSpace(path)
		if avatar == "" || path == "" {
			return nil, fmt.Errorf("--idle-ivf entry %q: want <avatar>=<path> or a bare path", ent)
		}
		if seen[avatar] {
			return nil, fmt.Errorf("--idle-ivf: avatar %q listed twice", avatar)
		}
		seen[avatar] = true
		out = append(out, idleEntry{Avatar: avatar, Path: path})
	}
	return out, nil
}

// parseActionsFlag parses --actions. An entry without "@" belongs to the
// default avatar, matching the bare-path rule for idle assets.
func parseActionsFlag(spec string) ([]actionEntry, error) {
	var out []actionEntry
	seen := map[string]bool{}
	for _, ent := range splitCSV(spec) {
		i := strings.Index(ent, "=")
		if i < 0 {
			return nil, fmt.Errorf("--actions entry %q: want <action>[@<avatar>]=<path>", ent)
		}
		left, path := strings.TrimSpace(ent[:i]), strings.TrimSpace(ent[i+1:])
		action, avatar := left, defaultAvatarID
		if j := strings.Index(left, "@"); j >= 0 {
			action, avatar = strings.TrimSpace(left[:j]), strings.TrimSpace(left[j+1:])
		}
		if action == "" || avatar == "" || path == "" {
			return nil, fmt.Errorf("--actions entry %q: want <action>[@<avatar>]=<path>", ent)
		}
		if key := avatar + "\x00" + action; seen[key] {
			return nil, fmt.Errorf("--actions: %q listed twice for avatar %q", action, avatar)
		} else {
			seen[key] = true
		}
		out = append(out, actionEntry{Action: action, Avatar: avatar, Path: path})
	}
	return out, nil
}

// validateActionAvatars rejects an action clip whose avatar has no idle loop.
// Actions ride the same baked-frame domain as the idle loop and the encoder
// refuses to play one without it, so this is a config error worth stopping the
// boot for — exactly like the old "--actions requires idle-local mode".
func validateActionAvatars(idles []idleEntry, actions []actionEntry) error {
	have := map[string]bool{}
	for _, e := range idles {
		have[e.Avatar] = true
	}
	for _, a := range actions {
		if !have[a.Avatar] {
			return fmt.Errorf("--actions %q is bound to avatar %q, which has no --idle-ivf entry "+
				"(actions need the avatar's baked idle loop)", a.Action, a.Avatar)
		}
	}
	return nil
}
