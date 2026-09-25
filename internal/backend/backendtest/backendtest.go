// Package backendtest is a controllable backend.Backend for tests outside
// package backend itself (internal/service, internal/api, internal/mcp): one
// that can fail, hang past a caller's deadline, or answer with no
// probabilities at all, so the service package's refusal-to-guess behaviour
// (invariant 1) can be exercised without a real model.
//
// It lives in its own package rather than a _test.go file in internal/backend
// because Go does not let another package's tests import a package's own test
// file.
package backendtest

import (
	"context"
	"errors"

	"github.com/TAIPANBOX/typryx/internal/backend"
	"github.com/TAIPANBOX/typryx/internal/template"
)

// Mode selects how Ask behaves.
type Mode int

const (
	// ModeOK returns Answer as given.
	ModeOK Mode = iota
	// ModeFail returns Err (or a default error if Err is nil).
	ModeFail
	// ModeHang blocks until ctx is done and returns ctx.Err().
	ModeHang
	// ModeEmpty returns an Answer with no Probabilities at all.
	ModeEmpty
)

// Backend is a controllable backend.Backend for tests.
type Backend struct {
	Mode   Mode
	Label  string // Name(), defaults to "backendtest"
	Answer backend.Answer
	Usage  backend.Usage
	Err    error
	// Asked records every Question this backend was given, so a test can
	// assert on exactly what reached it (invariant 2).
	Asked []AskedCall
}

// AskedCall is one recorded call to Ask.
type AskedCall struct {
	Question backend.Question
	Egress   template.Egress
}

func (b *Backend) Name() string {
	if b.Label != "" {
		return b.Label
	}
	return "backendtest"
}

func (b *Backend) Ask(ctx context.Context, q backend.Question, eg template.Egress) (backend.Answer, backend.Usage, error) {
	b.Asked = append(b.Asked, AskedCall{Question: q, Egress: eg})
	switch b.Mode {
	case ModeFail:
		if b.Err != nil {
			return backend.Answer{}, backend.Usage{}, b.Err
		}
		return backend.Answer{}, backend.Usage{}, errors.New("backendtest: forced failure")
	case ModeHang:
		<-ctx.Done()
		return backend.Answer{}, backend.Usage{}, ctx.Err()
	case ModeEmpty:
		return backend.Answer{Model: b.Name()}, backend.Usage{}, nil
	default:
		return b.Answer, b.Usage, nil
	}
}
