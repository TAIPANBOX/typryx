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
	"encoding/json"
	"log/slog"

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
	// NoulCriteriaGiven is true when the template explicitly set noul
	// criteria (NoulTrueDesc/NoulFalseDesc came from the template's own
	// criteria object, not from the zero value). A backend that must tell
	// "the template said nothing" apart from "the template said the empty
	// string" (the jev backend sends no criteria field at all in the first
	// case, an explicit object in the second) reads this instead of
	// inferring it from NoulTrueDesc/NoulFalseDesc being empty.
	NoulCriteriaGiven bool
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

// logHTTPFailure writes the one operator line for a 4xx or 5xx answer from a
// backend server. It says whether the server refused the call (4xx: something
// about the call, such as a tokenfuse gateway in front of the model asking for
// `x-fuse-run-id` with `metering_required`) or failed it (5xx), and carries the
// server's machine code when the body is JSON of the shape
// {"error":{"type":"..."}} or {"error":{"code":"..."}}. Only a code made of
// lowercase letters, digits and underscores, at most 64 bytes, is logged; the
// message text never is, because it may echo anything that was sent.
func logHTTPFailure(logger *slog.Logger, prefix string, status int, body []byte) {
	msg := prefix + ": server failed the call"
	if status < 500 {
		msg = prefix + ": server refused the call"
	}
	if code := machineCode(body); code != "" {
		logger.Warn(msg, "status", status, "error_type", code)
		return
	}
	logger.Warn(msg, "status", status)
}

// machineCode returns error.type, else error.code, from a JSON error body when
// it is a plain machine code, and "" otherwise.
func machineCode(body []byte) string {
	var parsed struct {
		Error struct {
			Type any `json:"type"`
			Code any `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return ""
	}
	for _, v := range []any{parsed.Error.Type, parsed.Error.Code} {
		if s, ok := v.(string); ok && isMachineCode(s) {
			return s
		}
	}
	return ""
}

func isMachineCode(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}
