package control

import (
	"net/http/httptest"
	"testing"

	"mynah/core"
)

type stubSessions struct{}

func (stubSessions) ListSessions() []core.SessionInfo            { return nil }
func (stubSessions) GetSession(string) (*core.SessionInfo, bool) { return nil, false }
func (stubSessions) KickSession(string) bool                     { return false }

// The bind-time warmup route must actually be MOUNTED. registerAvatarPreload
// shipped twice with no caller: the handler, the port, the frontend button and
// the toast all existed, and every avatar bind reported 「形象预热失败，首个访客
// 可能等待较久」 because the POST 404'd. A handler nothing routes to is invisible
// to every other kind of test, so assert on the mux.
func TestAvatarPreloadRouteIsMounted(t *testing.T) {
	s := New(Deps{
		Sessions:            stubSessions{},
		ResolveAvatarSource: func(string) string { return "/tmp/x.jpg" },
	})
	req := httptest.NewRequest("POST", "/api/v1/avatars/preload", nil)
	if _, pattern := s.mux.Handler(req); pattern == "" {
		t.Fatal("POST /api/v1/avatars/preload is not routed; the console's warmup call 404s")
	}
}

// ...and stays unmounted when it would panic: the handler dereferences the
// resolver hook.
func TestAvatarPreloadRouteAbsentWithoutResolver(t *testing.T) {
	s := New(Deps{Sessions: stubSessions{}})
	req := httptest.NewRequest("POST", "/api/v1/avatars/preload", nil)
	if _, pattern := s.mux.Handler(req); pattern != "" {
		t.Fatalf("route mounted without ResolveAvatarSource (pattern %q); the handler would nil-deref", pattern)
	}
}
