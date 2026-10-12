// cored: the realtime digital-human server (data plane + embedded control
// plane). All assembly lives in the public app package; this binary registers
// the full feature set — rerank, voice clone, avatar training, and the
// motion-skeleton library all ship in the open-source build. A distribution
// that wants a narrower build calls app.Main with its own Options instead of
// patching sources (the open-core seam, kept for composability).
package main

import (
	"mynah/app"
	"mynah/core"
	"mynah/internal/avatarbake"
	"mynah/internal/motion"
	"mynah/internal/rerank"
	"mynah/internal/training"
	"mynah/internal/voiceclone"
)

func main() {
	app.Main(app.Options{
		Gate: core.AllFeatures(),
		// MakeReranker is called by app on startup and on every hot config
		// apply; app guarantees url is non-empty. Works with any standard
		// rerank API (jina/cohere/siliconflow/TEI/self-hosted workers/rerank).
		MakeReranker: func(url, model, apiKey string) core.Reranker {
			return rerank.New(url, model, apiKey)
		},
		// The admin-extension seam mounts the avatar-pipeline admin family:
		// stateless voice-clone routes, the avatar-training pipeline, and the
		// driving-motion-skeleton library. Each is an independent delivery
		// unit: drop one from this list (and its frontend script tag) to ship
		// a build without that feature.
		AdminExt: multiAdminExt{voiceclone.New(), training.New(), motion.New(), avatarbake.New()},
	})
}

// multiAdminExt composes several AdminExtensions behind the single-extension
// seam, registering each in turn with the same deps.
type multiAdminExt []core.AdminExtension

func (m multiAdminExt) Register(r core.RouteRegistrar, deps core.AdminDeps) {
	for _, e := range m {
		e.Register(r, deps)
	}
}
