package record_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/TAIPANBOX/agent-stack-go/event"
	"github.com/TAIPANBOX/typryx/internal/record"
)

func TestOpenWithEmptyPathIsDisabled(t *testing.T) {
	j, err := record.Open("")
	if err != nil {
		t.Fatalf("Open(\"\"): %v", err)
	}
	if outcome := j.Answer("a", "r", record.AnswerData{}); outcome != record.Disabled {
		t.Errorf("expected Disabled, got %v", outcome)
	}
	if err := j.Close(); err != nil {
		t.Errorf("Close on a disabled journal should be a no-op: %v", err)
	}
}

func TestAnswerWrittenWithAnAgentIsWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.ndjson")
	j, err := record.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer j.Close()
	outcome := j.Answer("agent://acme.example/bot", "run1", record.AnswerData{
		Template: "t", TemplateVersion: "v1", Backend: "stub", Model: "stub-0",
		StateSHA384: "sha384:x", HeldBackFields: 2, AnswerID: "a1", Answer: "x",
		Probabilities: map[string]float64{"a": 1},
	})
	if outcome != record.Written {
		t.Fatalf("expected Written, got %v", outcome)
	}
	events, err := event.ReadFile(path)
	if err != nil {
		t.Fatalf("reading events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	e := events[0]
	if e.Type != record.TypeAnswer || e.Schema != record.Schema || e.Source != record.Source {
		t.Errorf("unexpected envelope: %+v", e)
	}
	if e.Severity != "info" {
		t.Errorf("expected info severity for an answer, got %s", e.Severity)
	}
	if e.Data["template"] != "t" || e.Data["answer_id"] != "a1" {
		t.Errorf("unexpected data: %+v", e.Data)
	}
}

func TestUnansweredIsMediumSeverity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.ndjson")
	j, _ := record.Open(path)
	defer j.Close()
	j.Unanswered("agent://acme.example/bot", "r", record.UnansweredData{Template: "t", AnswerID: "a1", Reason: "timeout"})
	events, _ := event.ReadFile(path)
	if len(events) != 1 || events[0].Type != record.TypeUnanswered || events[0].Severity != "medium" {
		t.Fatalf("unexpected events: %+v", events)
	}
	if events[0].Data["reason"] != "timeout" {
		t.Errorf("expected reason timeout, got %v", events[0].Data["reason"])
	}
}

func TestRefusedIsHighSeverity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.ndjson")
	j, _ := record.Open(path)
	defer j.Close()
	j.Refused("agent://acme.example/bot", "r", record.RefusedData{Reason: "unknown_template"})
	events, _ := event.ReadFile(path)
	if len(events) != 1 || events[0].Type != record.TypeRefused || events[0].Severity != "high" {
		t.Fatalf("unexpected events: %+v", events)
	}
}

func TestAnEmptyAgentIDIsSkippedAndCounted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.ndjson")
	j, _ := record.Open(path)
	defer j.Close()
	outcome := j.Answer("", "r", record.AnswerData{})
	if outcome != record.SkippedNoAgentID {
		t.Fatalf("expected SkippedNoAgentID, got %v", outcome)
	}
	skipped, failed := j.Counts()
	if skipped != 1 || failed != 0 {
		t.Errorf("expected skipped=1 failed=0, got skipped=%d failed=%d", skipped, failed)
	}
	events, _ := event.ReadFile(path)
	if len(events) != 0 {
		t.Errorf("expected no events written for a skipped emit, got %d", len(events))
	}
}

func TestWhitespaceOnlyAgentIDIsAlsoSkipped(t *testing.T) {
	j, _ := record.Open(filepath.Join(t.TempDir(), "events.ndjson"))
	defer j.Close()
	if outcome := j.Answer("   ", "r", record.AnswerData{}); outcome != record.SkippedNoAgentID {
		t.Errorf("expected SkippedNoAgentID for whitespace-only agent id, got %v", outcome)
	}
}

func TestWriteFailedIsCountedWhenTheJournalCannotBeAppended(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.ndjson")
	j, err := record.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// The underlying file is now closed; a further write must fail rather
	// than panic, and the failure is counted rather than surfaced as a
	// refusal to the caller (fail-open, documented in the package doc).
	outcome := j.Answer("agent://acme.example/bot", "r", record.AnswerData{})
	if outcome != record.WriteFailed {
		t.Fatalf("expected WriteFailed once the journal file is closed, got %v", outcome)
	}
	_, failed := j.Counts()
	if failed != 1 {
		t.Errorf("expected 1 write failure counted, got %d", failed)
	}
}

func TestOpenOnAnUnwritableDirectoryIsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses permission checks")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer os.Chmod(dir, 0o700)
	if _, err := record.Open(filepath.Join(dir, "sub", "events.ndjson")); err == nil {
		t.Error("expected an error opening a journal under an unwritable directory")
	}
}
