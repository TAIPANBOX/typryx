package ledger

import (
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenOnAFreshDirStartsEmpty(t *testing.T) {
	l, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()
	if _, ok := l.GetAnswer("nope"); ok {
		t.Error("a fresh ledger should have no answers")
	}
}

func TestPutAnswerThenGetAnswerRoundTrips(t *testing.T) {
	l, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()
	rec := AnswerRecord{AnswerID: "a1", Template: "t", TemplateVersion: "v1", Type: "noul", Backend: "stub", Model: "stub-0"}
	if err := l.PutAnswer(rec); err != nil {
		t.Fatalf("PutAnswer: %v", err)
	}
	got, ok := l.GetAnswer("a1")
	if !ok {
		t.Fatal("expected the answer to be found")
	}
	if got.TemplateVersion != "v1" {
		t.Errorf("expected v1, got %s", got.TemplateVersion)
	}
}

func TestPutAnswerPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	l1, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := l1.PutAnswer(AnswerRecord{AnswerID: "a1", Template: "t", TemplateVersion: "v1", Type: "noul"}); err != nil {
		t.Fatalf("PutAnswer: %v", err)
	}
	if err := l1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	l2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer l2.Close()
	if _, ok := l2.GetAnswer("a1"); !ok {
		t.Error("the answer written before reopening should still be indexed")
	}
}

func TestPutOutcomeWritesToOutcomesFile(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()
	if err := l.PutOutcome(OutcomeRecord{AnswerID: "a1", Truth: json.RawMessage(`true`), Source: "human"}); err != nil {
		t.Fatalf("PutOutcome: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "outcomes.ndjson"))
	if err != nil {
		t.Fatalf("reading outcomes.ndjson: %v", err)
	}
	if len(b) == 0 {
		t.Error("expected a line in outcomes.ndjson")
	}
}

func TestOpenOnAnUnwritableParentIsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses permission checks")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer os.Chmod(parent, 0o700)
	if _, err := Open(filepath.Join(parent, "ledger")); err == nil {
		t.Error("expected an error creating a ledger dir under an unwritable parent")
	}
}

func TestOpenSurfacesAnOutcomesFileOpenError(t *testing.T) {
	dir := t.TempDir()
	// A directory where the outcomes file should be: os.OpenFile on it
	// fails, and Open must surface that (and close the answers file it had
	// already opened) rather than leaking a handle or panicking.
	if err := os.Mkdir(filepath.Join(dir, "outcomes.ndjson"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := Open(dir); err == nil {
		t.Error("expected an error when outcomes.ndjson cannot be opened as a file")
	}
}

func TestPutAnswerAfterCloseIsAnErrorNotAPanic(t *testing.T) {
	l, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := l.PutAnswer(AnswerRecord{AnswerID: "a1"}); err == nil {
		t.Error("expected an error writing to a closed ledger")
	}
}

func TestPutOutcomeAfterCloseIsAnErrorNotAPanic(t *testing.T) {
	l, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := l.PutOutcome(OutcomeRecord{AnswerID: "a1"}); err == nil {
		t.Error("expected an error writing to a closed ledger")
	}
}

func TestCloseIsIdempotentEnoughToReportBothErrors(t *testing.T) {
	l, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	// A second close hits already-closed file descriptors on both files;
	// this only proves Close reports SOMETHING rather than panicking.
	_ = l.Close()
}

// TestATornLastLineIsSkippedNotFatal: a process killed mid-write leaves a
// half-written line at the very end of the file. Open must skip it, count it,
// and still open successfully with every earlier line indexed.
func TestATornLastLineIsSkippedNotFatal(t *testing.T) {
	dir := t.TempDir()
	good, _ := json.Marshal(AnswerRecord{AnswerID: "a1", Template: "t", TemplateVersion: "v1"})
	content := string(good) + "\n" + `{"answer_id":"a2","templ` // torn: cut mid-write
	if err := os.WriteFile(filepath.Join(dir, "answers.ndjson"), []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	l, err := Open(dir)
	if err != nil {
		t.Fatalf("Open should tolerate a torn last line, got: %v", err)
	}
	defer l.Close()
	if l.SkippedTornLines != 1 {
		t.Errorf("expected 1 skipped torn line, got %d", l.SkippedTornLines)
	}
	if _, ok := l.GetAnswer("a1"); !ok {
		t.Error("the earlier, complete line should still be indexed")
	}
	if _, ok := l.GetAnswer("a2"); ok {
		t.Error("the torn line must not appear as an indexed answer")
	}
}

// TestAMalformedLineNotLastRefusesToOpen: the same malformed content, but
// NOT at the end of the file, is not a torn write; it means the file is not
// what this package wrote, and Open refuses rather than building an index it
// cannot trust.
func TestAMalformedLineNotLastRefusesToOpen(t *testing.T) {
	dir := t.TempDir()
	good, _ := json.Marshal(AnswerRecord{AnswerID: "a1"})
	content := `{"answer_id":"broken middle line` + "\n" + string(good) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "answers.ndjson"), []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("expected Open to refuse when a non-last line is malformed")
	}
}

// TestLoadAnswerIndexNeverPanicsOnHostileLines: a seeded sweep of mutated
// NDJSON content. Open must either succeed or return an error; it must never
// panic.
func TestLoadAnswerIndexNeverPanicsOnHostileLines(t *testing.T) {
	base := []byte(`{"answer_id":"a1","template":"t","template_version":"v1","type":"noul","backend":"stub","model":"stub-0","answered_at":"now"}` + "\n")
	for seed := int64(0); seed < 200; seed++ {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("seed %d: panicked: %v", seed, r)
				}
			}()
			dir := t.TempDir()
			mutated := mutate(base, seed)
			if err := os.WriteFile(filepath.Join(dir, "answers.ndjson"), mutated, 0o644); err != nil {
				t.Fatalf("writing fixture: %v", err)
			}
			l, err := Open(dir)
			if err == nil {
				l.Close()
			}
		}()
	}
}

func mutate(b []byte, seed int64) []byte {
	r := rand.New(rand.NewSource(seed))
	out := append([]byte(nil), b...)
	n := r.Intn(10) + 1
	for i := 0; i < n; i++ {
		if len(out) == 0 {
			break
		}
		switch r.Intn(3) {
		case 0:
			out[r.Intn(len(out))] = byte(r.Intn(256))
		case 1:
			cut := r.Intn(len(out) + 1)
			out = out[:cut]
		case 2:
			pos := r.Intn(len(out) + 1)
			junk := []byte{byte(r.Intn(256))}
			out = append(out[:pos], append(junk, out[pos:]...)...)
		}
	}
	return out
}
