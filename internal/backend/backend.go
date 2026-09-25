// Package backend defines the one interface every model backend implements,
// and the "stub" backend that is the only one built in this phase.
//
// A backend's Ask method takes a template.Egress, never a map or raw bytes.
// That is deliberate and it is how invariant 2 (only a template's named
// fields ever reach a backend) is held by the type system rather than by a
// promise: template.Egress is constructible only inside package template, so
// there is no way to hand a backend state that skipped the filter. See
// template.Egress's own doc comment.
package backend

import (
	"context"

	"github.com/TAIPANBOX/typryx/internal/template"
)

// Option is one named choice a "choice" question offers.
type Option struct {
	Name        string
	Description string
}

// Question is what typryx asks a backend, assembled from a template (or, in
// freeform mode, from the caller's own question).
type Question struct {
	// Key identifies the question for a deterministic backend (the stub) and
	// for the record; it is the template id, or "freeform" for a
	// freeform question.
	Key          string
	Type         template.Type
	Instructions string
	// Options is set for Type == choice, sorted by Name.
	Options []Option
	// Levels is set for Type == score; array position is the score.
	Levels []string
	// NoulTrueDesc and NoulFalseDesc are a noul question's criteria
	// descriptions, set for Type == noul when the template gave any (empty
	// otherwise; a backend that needs some text falls back to its own
	// default, e.g. "yes"/"no").
	NoulTrueDesc  string
	NoulFalseDesc string
}

// Answer is what a backend answered.
//
// Choice, Score and Yes are filled according to Type: Choice and
// Probabilities' keys name an option for a choice question; Score is an
// index into Levels for a score question and Probabilities' keys are
// "0".."n-1"; Yes is the probability of true for a noul question and
// Probabilities is {"true": Yes, "false": 1-Yes}.
//
// A backend may return an empty or missing Probabilities map, NaN values, a
// sum that is not 1, or a map missing an option: typryx's service package
// validates every one of these before trusting an answer, never the backend
// itself, because invariant 1 (never invent an answer) has to hold even
// against a backend that lies or breaks.
//
// Choice, Score and Yes are ADVISORY ONLY and are never read by
// internal/service: the service derives the served answer from
// Probabilities itself (argmax over the template's option keys for choice,
// argmax index for score, Probabilities["true"] for noul), because a
// backend that returns a valid distribution alongside a disagreeing Choice,
// Score or Yes must never have the disagreement served. They remain here so
// a backend can report what it believes it answered, for logging or a
// future backend implementation's own convenience, but nothing in this
// repository trusts them.
type Answer struct {
	Choice        string
	Score         int
	Yes           float64
	Probabilities map[string]float64
	Model         string
}

// Usage is what asking cost. The stub backend never spends anything and
// always reports zero.
type Usage struct {
	InputTokens  int
	OutputTokens int
	CostUSD      float64
}

// Backend answers one typed question.
//
// Ask must respect ctx: a backend that ignores its deadline turns typryx's
// own timeout into a hang, which is exactly the silent-guess failure mode
// invariant 1 exists to close off from the other direction.
type Backend interface {
	Name() string
	Ask(ctx context.Context, q Question, egress template.Egress) (Answer, Usage, error)
}
