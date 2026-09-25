package backend

import (
	"context"
	"crypto/sha256"
	"strconv"

	"github.com/TAIPANBOX/typryx/internal/template"
)

// Stub is the only backend this phase builds. It is deterministic, spends
// nothing and answers nobody's real question: every answer names
// "backend: stub" and "model: stub-0" so nothing downstream mistakes it for
// a judgement. It exists for tests and demos.
type Stub struct{}

func (Stub) Name() string { return "stub" }

// Ask derives a probability distribution from the SHA-256 of the question's
// key, its instructions and the canonical bytes of what was actually sent,
// so the same question against the same egress always answers the same way
// and a hostile fuzz run is reproducible from its seed.
func (Stub) Ask(_ context.Context, q Question, eg template.Egress) (Answer, Usage, error) {
	h := sha256.Sum256(append([]byte(q.Key+"\x1f"+q.Instructions+"\x1f"), eg.Canonical()...))

	var keys []string
	switch q.Type {
	case template.TypeChoice:
		for _, o := range q.Options {
			keys = append(keys, o.Name)
		}
	case template.TypeScore:
		for i := range q.Levels {
			keys = append(keys, scoreKey(i))
		}
	case template.TypeNoul:
		keys = []string{"true", "false"}
	}
	probs := distribute(h[:], keys)

	ans := Answer{Probabilities: probs, Model: "stub-0"}
	switch q.Type {
	case template.TypeChoice:
		ans.Choice = argmax(keys, probs)
	case template.TypeScore:
		ans.Score = argmaxIndex(keys, probs)
	case template.TypeNoul:
		ans.Yes = probs["true"]
	}
	return ans, Usage{}, nil
}

// distribute turns hash bytes into a probability per key that sums to 1.
// Every weight is hash-byte-plus-one, so it is always strictly positive:
// with two keys (the noul case) that makes both probabilities strictly
// between 0 and 1, never exactly 0 or 1, which is what lets a noul answer's
// Yes read as a genuine probability rather than a certainty the stub never
// has grounds to claim.
func distribute(h []byte, keys []string) map[string]float64 {
	if len(keys) == 0 {
		return nil
	}
	weights := make([]float64, len(keys))
	sum := 0.0
	for i := range keys {
		b := h[i%len(h)]
		// Mix in the index too: two keys that land on the same hash byte
		// (i % len(h) wrapping for more than 32 keys, or two keys sharing a
		// byte value) still get distinguishable weights.
		w := float64(b) + float64(i) + 1
		weights[i] = w
		sum += w
	}
	probs := make(map[string]float64, len(keys))
	for i, k := range keys {
		probs[k] = weights[i] / sum
	}
	return probs
}

func argmax(keys []string, probs map[string]float64) string {
	best := ""
	bestP := -1.0
	for _, k := range keys {
		if probs[k] > bestP {
			bestP = probs[k]
			best = k
		}
	}
	return best
}

func argmaxIndex(keys []string, probs map[string]float64) int {
	best := 0
	bestP := -1.0
	for i, k := range keys {
		if probs[k] > bestP {
			bestP = probs[k]
			best = i
		}
	}
	return best
}

// scoreKey is the probabilities map key for score index i: plain decimal, no
// padding. Its own function so the format is documented in one place,
// matching what internal/service expects when it validates a score answer.
func scoreKey(i int) string {
	return strconv.Itoa(i)
}
