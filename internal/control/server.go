// Package control is the admin/control-plane HTTP server (admind embedded in
// cored, on its own listener). All endpoints live under /api/v1 and require a
// Bearer JWT except /auth/login. Responses follow {code:0,data}/{code,msg}
// with semantic HTTP status codes.
package control

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"mynah/core"
	"mynah/internal/avatarengine"
	"mynah/internal/mtbake"
	"mynah/internal/channel"
	"mynah/internal/config"
	"mynah/internal/knowledge"
	"mynah/internal/store"
	"mynah/internal/supervisor"
)

// Deps is everything the control plane is allowed to touch. Runtime access
// goes through the core interfaces only (the EE seam).
type Deps struct {
	Auth     core.AuthProvider
	DB       *store.Store
	Config   *config.Manager
	Ingester *knowledge.Ingester
	Sessions core.SessionAdmin    // may be nil until wired (P4-6)
	Reranker func() core.Reranker // live reranker getter (rerank config applies hot); nil func or nil return = vector order only
	Channels *channel.Registry    // published-channel registry; nil = publish disabled (no --db)
	// Supervisor exposes local component lifecycle (TTS/ASR/avatar workers) to
	// the console. nil = the services panel and its routes don't exist, which
	// is the right default on a host without a reachable Docker socket.
	Supervisor supervisor.Supervisor
	// EngineMaterial declares, per engine id, where that engine's weights /
	// repo / speaking material live on the HOST. Needed only for switching to
	// an engine that is not currently running: scaling an existing pool clones
	// a live worker instead. An engine absent here cannot be switched to, and
	// the console says so rather than creating containers that crash-loop.
	EngineMaterial map[string]avatarengine.Material
	// RescanAvatars re-reads the bake directory and refreshes the catalog.
	// Without it a newly finished bake stays invisible until cored restarts,
	// which is the single worst friction in the manual bake workflow. nil =
	// the catalog is whatever was scanned at startup.
	RescanAvatars func()
	// AvatarBaker turns an uploaded video into a MuseTalk avatar directory.
	// nil when the deployment has no Docker socket or no bake material
	// configured — the console then hides the upload panel instead of offering
	// a button that cannot work.
	AvatarBaker *mtbake.Runner
	// Visitor listen addresses (the actual runtime flags, not the config group
	// which may not reflect them). Used to build channel share links pointing at
	// the public visitor port. VisitorTLS wins when set (secure origin).
	VisitorListen    string
	VisitorTLSListen string
	Gate     core.FeatureGate     // answers /features; nil = everything off
	WebDir   string               // serve this dir on the admin listener (console build); "" = API only
	Health   HealthTargets
	// EngineDetail is the avatar worker Health detail (e.g. "wav2lipLS avatar
	// engine (face_size=384)"), surfaced on /features so the console can gate
	// engine-specific tuning UI (the wav2lipLS mouth knobs).
	EngineDetail string
	// AdminExt mounts the avatar-pipeline admin routes (voice clone, training,
	// motion, idle bake) through the RouteRegistrar seam. nil = those routes
	// don't exist.
	AdminExt core.AdminExtension
	// ResolveAvatarSource + ApplyIdle are runtime hooks handed through to the
	// AdminExtension (the idle-bake extension needs both); nil is fine.
	ResolveAvatarSource func(id string) string
	ApplyIdle           func(avatarID, assetPath string) error
	// QwenPreview auditions a Qwen cloud voice (throwaway realtime session
	// reading a sample line -> 16k mono f32 PCM). nil = no cloud credentials,
	// the preview endpoint is not mounted.
	QwenPreview func(ctx context.Context, model, voice, text string) ([]float32, error)
}

// reranker resolves the live reranker client (nil = no second stage).
func (s *Server) reranker() core.Reranker {
	if s.deps.Reranker == nil {
		return nil
	}
	return s.deps.Reranker()
}

type Server struct {
	deps Deps
	mux  *http.ServeMux
}

func New(deps Deps) *Server {
	s := &Server{deps: deps, mux: http.NewServeMux()}

	s.public("POST /api/v1/auth/login", s.handleLogin)
	s.private("GET /api/v1/auth/me", s.handleMe)
	s.private("POST /api/v1/auth/password", s.handlePassword)
	s.private("GET /api/v1/health", s.handleHealth)
	s.private("GET /api/v1/features", s.handleFeatures)
	if deps.Config != nil {
		s.registerConfig()
		s.private("GET /api/v1/tts/voices", s.handleVoices)
		s.private("GET /api/v1/avatars", s.handleAvatars)
		s.private("POST /api/v1/config/llm/test", s.handleLLMTest)
		if deps.QwenPreview != nil {
			s.private("POST /api/v1/config/brain/preview", s.handleBrainPreview)
		}
	}
	if deps.Ingester != nil {
		s.registerKB()
	}
	if deps.AvatarBaker != nil && deps.DB != nil {
		mtbake.NewHandlers(deps.DB.Pool, deps.AvatarBaker).Register(s.private)
	}
	if deps.Channels != nil && deps.Config != nil {
		s.registerChannels()
	}
	if deps.Supervisor != nil && deps.Config != nil {
		// Local-component lifecycle (start/stop/restart). Absent when there is
		// no Docker socket — better no panel than a panel of dead buttons.
		s.registerServices()
		s.registerCapacity()
		s.registerPool()
		s.registerEngineSwitch()
	}
	if deps.Sessions != nil {
		s.registerSessions()
		// Bind-time avatar warmup. Needs BOTH the pool (Sessions) and the id ->
		// worker-path resolver, so it is mounted here rather than next to
		// GET /api/v1/avatars: that route is static and survives a nil resolver,
		// this one would dereference it. It went unmounted for its first two
		// releases — registerAvatarPreload existed but nothing called it, so the
		// console's warmup POST 404'd and every bind reported 「形象预热失败」.
		if deps.ResolveAvatarSource != nil {
			s.registerAvatarPreload()
		}
		s.private("POST /api/v1/playground/chat", s.handlePlayground)
		s.private("POST /api/v1/playground/chat/stream", s.handlePlaygroundStream)
		s.private("POST /api/v1/playground/avatar/offer", s.handleAvatarOffer)
		s.private("POST /api/v1/playground/avatar/human", s.handleAvatarHuman)
		s.private("POST /api/v1/playground/avatar/interrupt", s.handleAvatarInterrupt)
		s.private("GET /api/v1/playground/avatar/actions", s.handleAvatarActions)
		s.private("POST /api/v1/playground/avatar/action", s.handleAvatarAction)
	}
	if deps.AdminExt != nil && deps.Config != nil {
		gate := deps.Gate
		if gate == nil {
			gate = core.StaticGate{}
		}
		if gate.Enabled(core.FeatureTraining) || gate.Enabled(core.FeatureMotion) {
			// The avatar-pipeline admin family (voice clone, training, motion).
			// Each sub-extension self-guards (skips its routes when its own
			// feature is off / DB is nil), so mounting the bundle when ANY of
			// the family is enabled is safe and keeps per-feature delivery a
			// build-composition choice rather than a gate change here.
			// DB may be nil (no --db): hand the extension a nil DBConn rather
			// than an interface wrapping a nil pool, so it can degrade cleanly.
			var db core.DBConn
			if s.deps.DB != nil {
				db = s.deps.DB.Pool
			}
			deps.AdminExt.Register(s, core.AdminDeps{
				TTSBase:             func() string { return s.deps.Config.Current().TTS.BaseURL },
				DB:                  db,
				Ctx:                 context.Background(),
				ResolveAvatarSource: deps.ResolveAvatarSource,
				ApplyIdle:           deps.ApplyIdle,
			})
		}
	}
	if deps.WebDir != "" {
		s.registerWeb(deps.WebDir)
	}

	return s
}

// registerWeb serves the console's static build on the admin listener (the
// same-origin deployment: no CORS, no dev proxy). API misses keep their JSON
// 404 instead of falling through to the file server.
func (s *Server) registerWeb(dir string) {
	files := http.FileServer(http.Dir(dir))
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			fail(w, http.StatusNotFound, "no such endpoint: "+r.Method+" "+r.URL.Path)
			return
		}
		files.ServeHTTP(w, r)
	})
}

func (s *Server) Handler() http.Handler { return s.mux }

// Private and Public satisfy core.RouteRegistrar so an AdminExtension (EE) can
// mount routes without importing this package. They delegate to the unexported
// helpers used by the OSS routes.
func (s *Server) Private(pattern string, h http.HandlerFunc) { s.private(pattern, h) }
func (s *Server) Public(pattern string, h http.HandlerFunc)  { s.public(pattern, h) }

func (s *Server) public(pattern string, h http.HandlerFunc) {
	s.mux.HandleFunc(pattern, h)
}

// private wraps a handler with Bearer-JWT verification; the identity is
// stored on the request context.
func (s *Server) private(pattern string, h http.HandlerFunc) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(raw, "Bearer ")
		if !ok || token == "" {
			fail(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		ident, err := s.deps.Auth.Verify(r.Context(), token)
		if err != nil {
			fail(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		h(w, r.WithContext(withIdentity(r.Context(), ident)))
	})
}

type identityKey struct{}

func withIdentity(ctx context.Context, id *core.Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

func identity(r *http.Request) *core.Identity {
	id, _ := r.Context().Value(identityKey{}).(*core.Identity)
	return id
}

// ─── response helpers ───────────────────────────────────────

func ok(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}

func fail(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"code": status, "msg": msg})
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		fail(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func logErr(where string, err error) {
	if err != nil {
		log.Printf("control: %s: %v", where, err)
	}
}
