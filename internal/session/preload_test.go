package session

import (
	"errors"
	"testing"

	pb "mynah/gen/go/proto/avatarengine/v1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A worker whose engine has no Preload method is warm, not broken. FlashHead's
// server predates the RPC; treating its Unimplemented as a failure made every
// FlashHead bind report 「形象预热失败，首个访客可能等待较久」 even though a portrait
// loads inline in milliseconds. A Preload that RUNS and fails is still cold.
func TestPreloadOutcomeUnimplementedIsWarm(t *testing.T) {
	r := preloadOutcome("1:9415", nil, status.Error(codes.Unimplemented, "Method not implemented!"))
	if !r.Ready {
		t.Errorf("Unimplemented must count as ready, got %+v", r)
	}
	if r.Detail == "" {
		t.Error("no detail: the operator cannot tell this apart from a real load")
	}
	if r := preloadOutcome("1:9414", nil, errors.New("connection refused")); r.Ready {
		t.Error("an unreachable worker must not count as warm")
	}
	if r := preloadOutcome("1:9414", &pb.PreloadReply{Ready: false, Detail: "cannot load avatar"}, nil); r.Ready {
		t.Error("a Preload that ran and refused must not count as warm")
	}
	if r := preloadOutcome("1:9414", &pb.PreloadReply{Ready: true, LoadMs: 15000}, nil); !r.Ready || r.LoadMS != 15000 {
		t.Errorf("successful load mangled: %+v", r)
	}
}

// The preload broadcast must be scoped to the engine that can render the avatar.
// It used to hit the whole pool, which was correct while a pool was single-engine
// and became a permanent false alarm the moment two engines ran side by side:
// MuseTalk cannot load a portrait and FlashHead cannot load a bake directory, so
// "all workers ready" was unreachable and every bind warned the operator about a
// pool that was in fact correctly warmed.
func TestPreloadBroadcastIsScopedToEngine(t *testing.T) {
	m := &Manager{workers: []*Worker{
		{Addr: "1:9414", Engine: "musetalk"},
		{Addr: "2:9417", Engine: "musetalk"},
		{Addr: "3:9415", Engine: "flashhead"},
	}}

	for _, tc := range []struct {
		engine string
		want   []string
	}{
		{"musetalk", []string{"1:9414", "2:9417"}},
		{"flashhead", []string{"3:9415"}},
		// Engine-agnostic material (the "default" built-in): dispatch may send it
		// anywhere, so warm everywhere.
		{"", []string{"1:9414", "2:9417", "3:9415"}},
		// An engine that is not in the pool asks nobody — and PreloadSummary must
		// call that out rather than report success, see below.
		{"wav2lipls", nil},
	} {
		got := m.workersForEngine(tc.engine)
		if len(got) != len(tc.want) {
			t.Errorf("engine %q: got %d workers, want %d", tc.engine, len(got), len(tc.want))
			continue
		}
		for i, w := range got {
			if w.Addr != tc.want[i] {
				t.Errorf("engine %q: worker %d = %s, want %s", tc.engine, i, w.Addr, tc.want[i])
			}
		}
	}
}

// A busy worker still gets warmed: it will be free for the next session, and an
// avatar resident on only the idle half of the pool works intermittently.
func TestPreloadIncludesBusyWorkers(t *testing.T) {
	m := &Manager{workers: []*Worker{
		{Addr: "1:9414", Engine: "musetalk", busy: "sess-1"},
		{Addr: "2:9417", Engine: "musetalk"},
	}}
	if got := len(m.workersForEngine("musetalk")); got != 2 {
		t.Errorf("got %d workers, want 2 (busy workers need the avatar for their next session)", got)
	}
}

// An empty broadcast is NOT success. ready == total == 0 satisfied the
// "everything ready" branch, so binding an avatar whose engine is not deployed
// reported a clean warm-up for a channel no worker can serve.
func TestPreloadSummaryEmptyBroadcastWarns(t *testing.T) {
	ready, total, reason := PreloadSummary(nil)
	if ready != 0 || total != 0 {
		t.Errorf("got %d/%d, want 0/0", ready, total)
	}
	if reason == "" {
		t.Error("no reason for an empty broadcast; the console would show a green tick")
	}
}

func TestPreloadSummaryPartialAndFull(t *testing.T) {
	if _, _, reason := PreloadSummary([]PreloadResult{{Ready: true}, {Ready: true}}); reason != "" {
		t.Errorf("all ready should be silent, got %q", reason)
	}
	_, _, reason := PreloadSummary([]PreloadResult{{Ready: true}, {Detail: "boom"}})
	if reason == "" {
		t.Error("partial success must warn: sessions can land on the cold worker")
	}
	if _, _, reason := PreloadSummary([]PreloadResult{{Detail: "boom"}}); reason == "" {
		t.Error("total failure must warn")
	}
}
