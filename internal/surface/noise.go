package surface

import (
	"math"
	"math/rand"
)

// Noiser injects the transient ε term. Real recall isn't deterministic, and an
// occasional surprising resurfacing is a feature — but tests need reproducible
// output, so noise is injected rather than drawn from a package global.
type Noiser interface {
	Noise(path string) float64
}

type noiser struct {
	scale    float64
	gaussian bool
	rng      *rand.Rand
}

// NewNoiser returns a Noiser drawing from a seeded stream. scale <= 0 yields a
// zero-noise (fully deterministic) Noiser — pass NewNoiser(0, 0, false) in tests
// that assert exact base+spread activations. Default is logistic, the noise
// distribution ACT-R actually specifies (retrieval probability becomes a softmax
// over activations); set gaussian for the handoff's literal "Gaussian" wording.
func NewNoiser(scale float64, seed int64, gaussian bool) Noiser {
	return &noiser{scale: scale, gaussian: gaussian, rng: rand.New(rand.NewSource(seed))}
}

// Noise draws one ε sample. The path argument is part of the interface so a
// future implementation could key noise by note, but this one draws from a
// positional stream — reproducibility depends on the caller iterating candidates
// in a stable (path-sorted) order, as Rank's contract requires.
func (n *noiser) Noise(string) float64 {
	if n.scale <= 0 {
		return 0
	}
	if n.gaussian {
		return n.rng.NormFloat64() * n.scale
	}
	u := n.rng.Float64()
	for u <= 0 || u >= 1 { // avoid ln(0)/div-by-zero at the CDF tails
		u = n.rng.Float64()
	}
	return n.scale * math.Log(u/(1-u)) // Logistic(0, scale) via inverse CDF
}
