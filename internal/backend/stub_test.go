package backend

import (
	"context"
	"math"
	"testing"

	"github.com/TAIPANBOX/typryx/internal/template"
)

func TestStubName(t *testing.T) {
	if (Stub{}).Name() != "stub" {
		t.Errorf("expected stub, got %s", (Stub{}).Name())
	}
}

func sumProbs(m map[string]float64) float64 {
	s := 0.0
	for _, v := range m {
		s += v
	}
	return s
}

func TestStubChoiceProbabilitiesSumToOneAndNameEveryOption(t *testing.T) {
	q := Question{
		Key: "t", Type: template.TypeChoice, Instructions: "pick one",
		Options: []Option{{Name: "a"}, {Name: "b"}, {Name: "c"}},
	}
	ans, usage, err := Stub{}.Ask(context.Background(), q, template.Egress{})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(ans.Probabilities) != 3 {
		t.Fatalf("expected 3 probabilities, got %d", len(ans.Probabilities))
	}
	if math.Abs(sumProbs(ans.Probabilities)-1) > 1e-9 {
		t.Errorf("probabilities do not sum to 1: %v", ans.Probabilities)
	}
	if _, ok := ans.Probabilities[ans.Choice]; !ok {
		t.Errorf("Choice %q is not one of the named options", ans.Choice)
	}
	if ans.Model != "stub-0" {
		t.Errorf("expected model stub-0, got %s", ans.Model)
	}
	if usage.CostUSD != 0 {
		t.Errorf("the stub must never report a cost, got %v", usage.CostUSD)
	}
}

func TestStubScoreKeysAreIndices(t *testing.T) {
	q := Question{Key: "t", Type: template.TypeScore, Instructions: "rate it", Levels: []string{"a", "b", "c", "d"}}
	ans, _, err := Stub{}.Ask(context.Background(), q, template.Egress{})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	for _, k := range []string{"0", "1", "2", "3"} {
		if _, ok := ans.Probabilities[k]; !ok {
			t.Errorf("missing probability key %q", k)
		}
	}
	if ans.Score < 0 || ans.Score > 3 {
		t.Errorf("score %d out of range", ans.Score)
	}
}

func TestStubNoulYesIsStrictlyBetweenZeroAndOne(t *testing.T) {
	q := Question{Key: "t", Type: template.TypeNoul, Instructions: "is it true"}
	ans, _, err := Stub{}.Ask(context.Background(), q, template.Egress{})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if ans.Yes <= 0 || ans.Yes >= 1 {
		t.Errorf("expected Yes strictly between 0 and 1, got %v", ans.Yes)
	}
	if math.Abs(ans.Probabilities["true"]+ans.Probabilities["false"]-1) > 1e-9 {
		t.Errorf("true/false probabilities do not sum to 1: %v", ans.Probabilities)
	}
	if ans.Probabilities["true"] != ans.Yes {
		t.Errorf("Yes must equal Probabilities[true]")
	}
}

func TestStubIsDeterministic(t *testing.T) {
	q := Question{Key: "t", Type: template.TypeNoul, Instructions: "is it true"}
	a1, _, _ := Stub{}.Ask(context.Background(), q, template.Egress{})
	a2, _, _ := Stub{}.Ask(context.Background(), q, template.Egress{})
	if a1.Yes != a2.Yes {
		t.Errorf("the same question answered twice gave different results: %v vs %v", a1.Yes, a2.Yes)
	}
}

func TestStubAnswerChangesWithEgressContent(t *testing.T) {
	tpl := template.Template{ID: "t", Type: template.TypeNoul, Instructions: "is it true", Fields: []string{"a"}}
	eg1, _, _ := template.Filter(tpl, []byte(`{"a":"one"}`))
	eg2, _, _ := template.Filter(tpl, []byte(`{"a":"two"}`))
	q := Question{Key: "t", Type: template.TypeNoul, Instructions: "is it true"}
	a1, _, _ := Stub{}.Ask(context.Background(), q, eg1)
	a2, _, _ := Stub{}.Ask(context.Background(), q, eg2)
	if a1.Yes == a2.Yes {
		t.Error("two different egress payloads produced the exact same probability; the stub is not reading its input")
	}
}
