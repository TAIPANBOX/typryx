package template

import (
	"os"
	"path/filepath"
	"testing"
)

// examplesDir is the starter catalog shipped in this repository and baked
// into the Docker image (Dockerfile's `COPY examples/templates
// /etc/typryx/templates`). scripts/templates-load.sh is the CI/pre-push gate
// that proves the same two properties these two tests prove; these tests are
// what scripts/features-are-bound.sh's binding needs, since a shell gate
// names no Go test of its own.
func examplesDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "..", "examples", "templates")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("examples/templates not found relative to internal/template: %v", err)
	}
	return dir
}

// @test:TestEveryStarterTemplateLoads
func TestEveryStarterTemplateLoads(t *testing.T) {
	reg, loadErrs, err := LoadDir(examplesDir(t))
	if err != nil {
		t.Fatalf("reading examples/templates: %v", err)
	}
	if len(loadErrs) != 0 {
		t.Fatalf("examples/templates has %d template(s) that fail to load: %v", len(loadErrs), loadErrs)
	}

	// Sorted, because Registry.List() always returns templates sorted by ID.
	want := []string{"eval.answer_quality", "eval.outcome_met", "request.complexity", "triage.anomaly_class"}
	var got []string
	for _, tpl := range reg.List() {
		got = append(got, tpl.ID)
		if tpl.Version() == "" {
			t.Errorf("%s has an empty version", tpl.ID)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("examples/templates loaded %d template(s) %v, want %d: %v", len(got), got, len(want), want)
	}
	for i, id := range want {
		if got[i] != id {
			t.Errorf("examples/templates: want %q at position %d, got %q (full list %v)", id, i, got[i], got)
		}
	}
}

// identifyingFieldDenylist mirrors scripts/templates-load.sh's own list: the
// starter catalog ships the minimum a judge needs, and none of these names a
// person or a customer.
var identifyingFieldDenylist = map[string]bool{
	"email": true, "user_email": true, "name": true, "phone": true,
	"iban": true, "card": true, "api_key": true, "token": true,
	"password": true, "ssn": true, "address": true,
}

// @test:TestNoStarterTemplateNamesAnIdentifyingField
func TestNoStarterTemplateNamesAnIdentifyingField(t *testing.T) {
	reg, loadErrs, err := LoadDir(examplesDir(t))
	if err != nil {
		t.Fatalf("reading examples/templates: %v", err)
	}
	if len(loadErrs) != 0 {
		t.Fatalf("examples/templates has %d template(s) that fail to load: %v", len(loadErrs), loadErrs)
	}
	for _, tpl := range reg.List() {
		for _, f := range tpl.Fields {
			if identifyingFieldDenylist[f] {
				t.Errorf("%s names %q in fields, which identifies a person or a customer", tpl.ID, f)
			}
		}
	}
}
