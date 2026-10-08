package core

// Feature names answered by FeatureGate / reported by GET /api/v1/features.
// The list is fixed per API version so the console can render capability
// switches without guessing.
//
// Since the full-open-source merge every feature ships in the one build and
// defaults ON; the gate survives as a deployment-level switch (a distribution
// or operator can still assemble app.Main with a narrower gate to hide
// capabilities it doesn't provision workers for).
const (
	FeatureRerank   = "rerank"
	FeatureTraining = "training"
	// FeatureMotion is the driving-motion-skeleton library (动作骨架). Split
	// from FeatureTraining so a build can ship it independently — same
	// capability family (avatar pipeline), separate release unit.
	FeatureMotion = "motion"
)

// Features is the canonical order for /features responses.
var Features = []string{FeatureRerank, FeatureTraining, FeatureMotion}

// AllFeatures is the default gate: everything on (the open-source build ships
// every capability).
func AllFeatures() StaticGate {
	g := StaticGate{}
	for _, f := range Features {
		g[f] = true
	}
	return g
}

// StaticGate is a FeatureGate over a fixed set.
type StaticGate map[string]bool

func (g StaticGate) Enabled(feature string) bool { return g[feature] }
