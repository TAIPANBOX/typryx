package calibration_test

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/TAIPANBOX/typryx/internal/calibration"
	"github.com/TAIPANBOX/typryx/internal/ledger"
)

func sha256OfFile(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return sha256.Sum256(b)
}

// @test:TestCalibrationNeverModifiesTheLedgerItReads
//
// A running typryx may still be appending to these files while calibration
// reads them; unlike internal/ledger.Open, which truncates a torn tail on
// disk at startup, this package must never write to either file, not even to
// clean up a torn tail it found. Both files here carry a torn last line
// (the shape a crash, or a concurrent writer caught mid-append, leaves), and
// their bytes must be bit-for-bit identical before and after Run.
func TestCalibrationNeverModifiesTheLedgerItReads(t *testing.T) {
	dir := t.TempDir()
	goodAns, _ := json.Marshal(ledger.AnswerRecord{
		AnswerID: "a1", Template: "t", TemplateVersion: "v1", Type: "noul", Backend: "stub", Model: "m1",
		Probabilities: map[string]float64{"true": 0.6, "false": 0.4},
	})
	answersContent := string(goodAns) + "\n" + `{"answer_id":"a2","templ` // torn, no trailing newline
	if err := os.WriteFile(filepath.Join(dir, "answers.ndjson"), []byte(answersContent), 0o644); err != nil {
		t.Fatalf("writing answers fixture: %v", err)
	}
	goodOut, _ := json.Marshal(ledger.OutcomeRecord{AnswerID: "a1", Truth: json.RawMessage(`true`)})
	outcomesContent := string(goodOut) + "\n" + `{"answer_id":"a2","tr` // also torn
	if err := os.WriteFile(filepath.Join(dir, "outcomes.ndjson"), []byte(outcomesContent), 0o644); err != nil {
		t.Fatalf("writing outcomes fixture: %v", err)
	}

	answersPath := filepath.Join(dir, "answers.ndjson")
	outcomesPath := filepath.Join(dir, "outcomes.ndjson")
	beforeA := sha256OfFile(t, answersPath)
	beforeO := sha256OfFile(t, outcomesPath)

	report, err := calibration.Run(calibration.Options{LedgerDir: dir, MinN: 1})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Counters.TornLines != 2 {
		t.Errorf("expected both files' torn tails counted, got torn_lines=%d", report.Counters.TornLines)
	}

	afterA := sha256OfFile(t, answersPath)
	afterO := sha256OfFile(t, outcomesPath)
	if beforeA != afterA {
		t.Error("answers.ndjson's bytes changed after a read-only calibration run")
	}
	if beforeO != afterO {
		t.Error("outcomes.ndjson's bytes changed after a read-only calibration run")
	}
}

// @test:TestAMalformedLineAnywhereIsSkippedAndCountedNotFatal
//
// Unlike internal/ledger.Open (which refuses to open over a malformed
// non-last line), calibration must still produce a report: a malformed line
// is skipped and counted, never fatal.
func TestAMalformedLineAnywhereIsSkippedAndCountedNotFatal(t *testing.T) {
	dir := t.TempDir()
	good, _ := json.Marshal(ledger.AnswerRecord{
		AnswerID: "a1", Template: "t", TemplateVersion: "v1", Type: "noul", Backend: "stub", Model: "m1",
		Probabilities: map[string]float64{"true": 0.6, "false": 0.4},
	})
	content := `{"answer_id":"broken middle line` + "\n" + string(good) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "answers.ndjson"), []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	outGood, _ := json.Marshal(ledger.OutcomeRecord{AnswerID: "a1", Truth: json.RawMessage(`true`)})
	if err := os.WriteFile(filepath.Join(dir, "outcomes.ndjson"), []byte(string(outGood)+"\n"), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	report, err := calibration.Run(calibration.Options{LedgerDir: dir, MinN: 1})
	if err != nil {
		t.Fatalf("Run must not refuse over a malformed line, got: %v", err)
	}
	if report.Counters.MalformedLines != 1 {
		t.Errorf("expected 1 malformed line counted, got %d", report.Counters.MalformedLines)
	}
	if len(report.Groups) != 1 {
		t.Fatalf("expected the well-formed line to still be scored, got %d groups", len(report.Groups))
	}
}

// @test:TestAMissingLedgerDirectoryIsNotAnErrorItIsAnEmptyReport
func TestAMissingLedgerDirectoryIsNotAnErrorItIsAnEmptyReport(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist-yet")
	report, err := calibration.Run(calibration.Options{LedgerDir: dir, MinN: 1})
	if err != nil {
		t.Fatalf("expected no error for a ledger directory with no files yet, got: %v", err)
	}
	if len(report.Groups) != 0 {
		t.Errorf("expected 0 groups, got %d", len(report.Groups))
	}
}
