package instinct

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestJevRequestShapeAndBreaker(t *testing.T) {
	var got jevReq
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/systemone" || r.Header.Get("Authorization") != "Bearer k1" {
			t.Errorf("bad request %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		if calls >= 2 {
			w.WriteHeader(451)
			_, _ = w.Write([]byte(`{"title":"Typesafe is not available in your region.","status":451}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"answers": map[string]any{
			"clip":   map[string]any{"type": "choice", "choice": "nod", "confidence": 0.83, "probabilities": map[string]float64{"nod": 0.83, "none": 0.17}},
			"commit": map[string]any{"type": "noul", "noul": 0.12}}})
	}))
	defer srv.Close()
	j, err := NewJev(JevConfig{BaseURL: srv.URL, APIKey: "k1", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ans, err := j.Decide(context.Background(), "state", map[string]Question{"clip": {Type: "choice", Criteria: map[string]string{"nod": "x"}}})
	if err != nil || ans["clip"].Choice != "nod" || ans["clip"].Confidence != 0.83 || ans["commit"].Noul != 0.12 {
		t.Fatalf("ans=%v err=%v", ans, err)
	}
	if got.Model != "jev-latest" || got.Questions["clip"].Type != "choice" {
		t.Fatalf("request shape: %+v", got)
	}
	// three failures trip the breaker; the fourth call must fail fast without HTTP
	for i := 0; i < 3; i++ {
		if _, err := j.Decide(context.Background(), "s", nil); err == nil {
			t.Fatal("expected 451 error")
		}
	}
	before := calls
	if _, err := j.Decide(context.Background(), "s", nil); err == nil || calls != before {
		t.Fatalf("breaker did not open: err=%v calls=%d", err, calls-before)
	}
}
