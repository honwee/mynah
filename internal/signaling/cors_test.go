package signaling

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The routing table must not panic on the OPTIONS patterns registered next to
// the method-less ones, and a preflight must not fall through to the real
// handler (which would try to decode an empty body).
func TestPreflightIsRoutedAheadOfTheHandler(t *testing.T) {
	h := New(nil, "", nil).Handler()

	req := httptest.NewRequest(http.MethodOptions, "/human", nil)
	req.Header.Set("Origin", "https://bi.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204 (body: %s)", rr.Code, rr.Body)
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "https://bi.example.com" {
		t.Errorf("allow-origin = %q, want the request's origin echoed", got)
	}
	if got := rr.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, "Content-Type") {
		t.Errorf("allow-headers = %q, want Content-Type (the SDK posts JSON)", got)
	}
	// Credentials must stay off: cored authenticates by token in the body, and
	// reflecting an arbitrary origin is only safe as long as cookies can't ride.
	if rr.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Error("preflight allows credentials; see cors.go")
	}
}

// A same-origin request (no Origin header) must look exactly like it does today
// — no CORS headers appearing out of nowhere on the visitor page.
func TestSameOriginRequestGetsNoCORSHeaders(t *testing.T) {
	rr := httptest.NewRecorder()
	cors(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })(
		rr, httptest.NewRequest(http.MethodPost, "/human", nil))

	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("allow-origin = %q on a same-origin request", got)
	}
	if got := rr.Header().Get("Vary"); got != "" {
		t.Errorf("Vary = %q on a same-origin request", got)
	}
}

func TestCORSVariesOnOrigin(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/human", nil)
	req.Header.Set("Origin", "https://bi.example.com")
	rr := httptest.NewRecorder()
	cors(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })(rr, req)

	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "https://bi.example.com" {
		t.Errorf("allow-origin = %q", got)
	}
	// Without Vary a shared cache would serve one origin's response to another.
	if got := rr.Header().Get("Vary"); got != "Origin" {
		t.Errorf("Vary = %q, want Origin", got)
	}
}
