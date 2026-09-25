package calibration

import (
	"encoding/json"
	"testing"
)

// White-box tests of the unexported helpers, in package calibration itself
// (unlike calibration_test.go, which only sees the public API): the branches
// here are edge cases (an unrecognized answer type, a truth that does not
// even parse as its type's shape, a confidence outside [0,1]) that the
// external, ledger-file-based tests exercise unevenly.

func TestTruthKeyForEveryType(t *testing.T) {
	cases := []struct {
		name       string
		answerType string
		truth      json.RawMessage
		wantKey    string
		wantOK     bool
	}{
		{"choice", "choice", json.RawMessage(`"cheap"`), "cheap", true},
		{"choice malformed", "choice", json.RawMessage(`123`), "", false},
		{"score", "score", json.RawMessage(`2`), "2", true},
		{"score malformed", "score", json.RawMessage(`"two"`), "", false},
		{"noul true", "noul", json.RawMessage(`true`), "true", true},
		{"noul false", "noul", json.RawMessage(`false`), "false", true},
		{"noul malformed", "noul", json.RawMessage(`"yes"`), "", false},
		{"unknown type", "mystery", json.RawMessage(`"x"`), "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			key, ok := truthKeyFor(c.answerType, c.truth)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if ok && key != c.wantKey {
				t.Errorf("key = %q, want %q", key, c.wantKey)
			}
		})
	}
}

func TestBinIndexClampsOutOfRangeConfidence(t *testing.T) {
	cases := []struct {
		confidence float64
		want       int
	}{
		{-1.0, 0},
		{0, 0},
		{0.05, 0},
		{0.15, 1},
		{0.95, 9},
		{1.0, 9},
		{1.5, 9},
	}
	for _, c := range cases {
		if got := binIndex(c.confidence); got != c.want {
			t.Errorf("binIndex(%v) = %d, want %d", c.confidence, got, c.want)
		}
	}
}
