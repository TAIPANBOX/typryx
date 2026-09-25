package service_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/TAIPANBOX/agent-stack-go/event"
	"github.com/TAIPANBOX/typryx/internal/backend"
	"github.com/TAIPANBOX/typryx/internal/ledger"
	"github.com/TAIPANBOX/typryx/internal/record"
	"github.com/TAIPANBOX/typryx/internal/service"
	"github.com/TAIPANBOX/typryx/internal/template"
)

// loadOneTemplate writes tmpl as a *.json file in a fresh temp dir and
// returns the loaded registry.
func loadOneTemplate(t *testing.T, tmpl template.Template) *template.Registry {
	t.Helper()
	dir := t.TempDir()
	b, err := json.Marshal(tmpl)
	if err != nil {
		t.Fatalf("marshaling fixture template: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, tmpl.ID+".json"), b, 0o644); err != nil {
		t.Fatalf("writing fixture template: %v", err)
	}
	reg, loadErrs, err := template.LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(loadErrs) != 0 {
		t.Fatalf("fixture template failed to load: %v", loadErrs)
	}
	return reg
}

// testDeps bundles what a test typically wants to inspect after calling Ask.
type testDeps struct {
	Service     *service.Service
	JournalPath string
	Ledger      *ledger.Ledger
}

func newService(t *testing.T, tmpl template.Template, back backend.Backend) *testDeps {
	t.Helper()
	reg := loadOneTemplate(t, tmpl)
	dir := t.TempDir()
	journalPath := filepath.Join(dir, "events.ndjson")
	j, err := record.Open(journalPath)
	if err != nil {
		t.Fatalf("record.Open: %v", err)
	}
	t.Cleanup(func() { j.Close() })

	svc := service.New()
	svc.Templates = reg
	svc.Backend = back
	svc.Cap = service.NewCap(1000)
	svc.Journal = j

	return &testDeps{Service: svc, JournalPath: journalPath}
}

func newServiceWithLedger(t *testing.T, tmpl template.Template, back backend.Backend) *testDeps {
	t.Helper()
	d := newService(t, tmpl, back)
	l, err := ledger.Open(t.TempDir())
	if err != nil {
		t.Fatalf("ledger.Open: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	d.Service.Ledger = l
	d.Ledger = l
	return d
}

// readEvents reads every event.Event from a journal file written during a
// test. Malformed lines are surfaced as a test failure rather than silently
// skipped, since a test reading its own fixture should never produce one.
func readEvents(t *testing.T, path string) []event.Event {
	t.Helper()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	events, err := event.ReadFile(path)
	if err != nil {
		t.Fatalf("reading journal %s: %v", path, err)
	}
	return events
}
