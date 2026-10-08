package control

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"mynah/internal/avatarcatalog"
)

// The avatar list must carry the SAME engine attribution avatarcatalog.EngineFor
// uses for dispatch. It once dropped it for built-in portraits, on the (by then
// false) theory that any engine can speak a cond_image file: the console showed
// no FlashHead-renderable avatar at all, so an operator running a FlashHead
// worker had nothing to bind a channel to — and the pool card next door was
// happily reporting that engine online.
func TestAvatarListCarriesEngineAttribution(t *testing.T) {
	rec := httptest.NewRecorder()
	(&Server{}).handleAvatars(rec, httptest.NewRequest("GET", "/api/v1/avatars", nil))

	var body struct {
		Data []struct {
			ID     string `json:"id"`
			Engine string `json:"engine"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	got := map[string]string{}
	for _, a := range body.Data {
		got[a.ID] = a.Engine
	}
	attributed := 0
	for _, p := range avatarcatalog.Builtins {
		engine, listed := got[p.ID]
		if !listed {
			t.Errorf("built-in %q missing from the avatar list", p.ID)
			continue
		}
		if engine != avatarcatalog.EngineFor(p.ID) {
			t.Errorf("avatar %q engine = %q, want %q (avatarcatalog is the single source)",
				p.ID, engine, avatarcatalog.EngineFor(p.ID))
		}
		if engine != "" {
			attributed++
		}
	}
	// Not just "the field is copied": at least one built-in is engine-bound, so a
	// regression that always emits "" cannot pass by matching an all-empty
	// catalog.
	if attributed == 0 {
		t.Error("no built-in reported an engine; the mixed-pool console has nothing to filter on")
	}
}
