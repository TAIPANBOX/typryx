package backendtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/TAIPANBOX/typryx/internal/backend"
	"github.com/TAIPANBOX/typryx/internal/template"
)

func TestNameDefaultsAndCanBeSet(t *testing.T) {
	b := &Backend{}
	if b.Name() != "backendtest" {
		t.Errorf("expected the default name, got %s", b.Name())
	}
	b.Label = "custom"
	if b.Name() != "custom" {
		t.Errorf("expected custom, got %s", b.Name())
	}
}

func TestModeOKReturnsTheConfiguredAnswer(t *testing.T) {
	want := backend.Answer{Choice: "a"}
	b := &Backend{Mode: ModeOK, Answer: want}
	got, _, err := b.Ask(context.Background(), backend.Question{}, template.Egress{})
	if err != nil || got.Choice != "a" {
		t.Fatalf("expected the configured answer, got %+v, %v", got, err)
	}
}

func TestModeFailReturnsTheConfiguredError(t *testing.T) {
	sentinel := errors.New("boom")
	b := &Backend{Mode: ModeFail, Err: sentinel}
	_, _, err := b.Ask(context.Background(), backend.Question{}, template.Egress{})
	if !errors.Is(err, sentinel) {
		t.Errorf("expected the configured error, got %v", err)
	}
}

func TestModeFailWithNoErrorConfiguredStillFails(t *testing.T) {
	b := &Backend{Mode: ModeFail}
	_, _, err := b.Ask(context.Background(), backend.Question{}, template.Egress{})
	if err == nil {
		t.Fatal("expected a default error")
	}
}

func TestModeHangReturnsWhenTheContextIsDone(t *testing.T) {
	b := &Backend{Mode: ModeHang}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, _, err := b.Ask(ctx, backend.Question{}, template.Egress{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
}

func TestModeEmptyReturnsNoProbabilities(t *testing.T) {
	b := &Backend{Mode: ModeEmpty}
	ans, _, err := b.Ask(context.Background(), backend.Question{}, template.Egress{})
	if err != nil || len(ans.Probabilities) != 0 {
		t.Fatalf("expected an empty-probabilities answer, got %+v, %v", ans, err)
	}
}

func TestAskRecordsEveryCall(t *testing.T) {
	b := &Backend{Mode: ModeOK}
	q := backend.Question{Key: "k"}
	b.Ask(context.Background(), q, template.Egress{})
	b.Ask(context.Background(), q, template.Egress{})
	if len(b.Asked) != 2 {
		t.Fatalf("expected 2 recorded calls, got %d", len(b.Asked))
	}
}
