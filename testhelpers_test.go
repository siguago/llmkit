package llmkit

import "math/rand/v2"

// newTestRand returns a deterministically seeded generator so jitter-dependent
// assertions don't flake.
func newTestRand() *rand.Rand {
	return rand.New(rand.NewPCG(1, 2))
}
