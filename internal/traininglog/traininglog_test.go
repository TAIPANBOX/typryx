package traininglog_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/TAIPANBOX/typryx/internal/traininglog"
)

func sampleLine(id string) traininglog.Line {
	return traininglog.Line{
		AnswerID: id, AnsweredAt: "2026-09-30T12:00:00Z", Template: "eval.outcome_met",
		TemplateVersion: "v1", Type: "noul", State: json.RawMessage(`{"task":"t"}`),
		Backend: "stub", Model: "stub-0",
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if len(b) == 0 {
		return nil
	}
	if b[len(b)-1] != '\n' {
		t.Fatalf("%s does not end in a newline: a torn or unterminated line", path)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

var wantKeys = []string{"answer_id", "answered_at", "backend", "model", "state", "template", "template_version", "type"}

// @test:TestTheTrainingDirIsPrivateAndTheFileIsPrivate
func TestTheTrainingDirIsPrivateAndTheFileIsPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "training")
	l, err := traininglog.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()
	if err := l.Put(sampleLine("a1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("the training directory is %o; it holds customer data and must be 0700", got)
	}
	fi, err := os.Stat(filepath.Join(dir, traininglog.FileName))
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("training.ndjson is %o; it holds customer data and must be 0600", got)
	}
}

// @test:TestAnExistingTrainingFileIsNarrowedToPrivate
//
// A training.ndjson that already exists with wider permissions (copied in,
// restored from a backup, created by an older build) is narrowed to 0600 when
// the log opens, not trusted as found: it holds a customer's own data.
func TestAnExistingTrainingFileIsNarrowedToPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "training")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, traininglog.FileName)
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := traininglog.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("an existing training.ndjson stayed %o after Open; it must be narrowed to 0600", got)
	}
}

// @test:TestATrainingLineHasNoFieldThatCouldHoldABackendAnswer
func TestATrainingLineHasNoFieldThatCouldHoldABackendAnswer(t *testing.T) {
	var got []string
	rt := reflect.TypeOf(traininglog.Line{})
	for i := 0; i < rt.NumField(); i++ {
		tag := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			t.Fatalf("field %s has no json name, so what is written is not what is declared", rt.Field(i).Name)
		}
		got = append(got, tag)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, wantKeys) {
		t.Fatalf("a training line declares %v; the only allowed set is %v. Adding a field is a decision "+
			"about what customer data and what model output reaches disk, not a refactor.", got, wantKeys)
	}

	// And what is actually written is that set, nothing added on the way out.
	dir := t.TempDir()
	l, err := traininglog.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()
	if err := l.Put(sampleLine("a1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	var m map[string]json.RawMessage
	lines := readLines(t, filepath.Join(dir, traininglog.FileName))
	if err := json.Unmarshal([]byte(lines[0]), &m); err != nil {
		t.Fatalf("the written line is not JSON: %v", err)
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, wantKeys) {
		t.Fatalf("the written line has keys %v, want %v", keys, wantKeys)
	}
}

// @test:TestThePackagesCannotSeeABackendAnswer
//
// Structural half of "a Jev answer can never become a training label through
// this package": the package neither imports internal/backend (where
// backend.Answer and its probabilities live) nor internal/ledger (whose
// AnswerRecord carries them), so nothing here can even name one.
func TestThePackagesCannotSeeABackendAnswer(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	deps := string(out)
	if !strings.Contains(deps, "github.com/TAIPANBOX/typryx/internal/traininglog") {
		t.Fatalf("go list did not list the package itself, so this measured nothing:\n%s", deps)
	}
	for _, banned := range []string{"internal/backend", "internal/ledger", "internal/service", "internal/calibration"} {
		if strings.Contains(deps, "/typryx/"+banned) {
			t.Errorf("internal/traininglog depends on %s, which would let a backend's answer or probabilities reach the training log or its export", banned)
		}
	}
}

func TestEveryPutIsOneCompleteLineAndSurvivesAReopen(t *testing.T) {
	dir := t.TempDir()
	l, err := traininglog.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	first := sampleLine("a1")
	// A newline inside a string is escaped by JSON; it must never become a
	// real line break in the file.
	first.State = json.RawMessage("{\"task\" :\n \"two\\nlines\"}")
	if err := l.Put(first); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	l, err = traininglog.Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer l.Close()
	if err := l.Put(sampleLine("a2")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	lines := readLines(t, filepath.Join(dir, traininglog.FileName))
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), lines)
	}
	for _, ln := range lines {
		if !json.Valid([]byte(ln)) {
			t.Errorf("line is not valid JSON: %q", ln)
		}
	}
}

// @test:TestATornTrainingTailIsTruncatedBeforeTheNextWrite
func TestATornTrainingTailIsTruncatedBeforeTheNextWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, traininglog.FileName)
	good, _ := json.Marshal(sampleLine("a1"))
	if err := os.WriteFile(path, append(append([]byte{}, good...), []byte("\n{\"answer_id\":\"a2\",\"answ")...), 0o600); err != nil {
		t.Fatalf("seeding the file: %v", err)
	}
	l, err := traininglog.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()
	if l.TornBytes() == 0 {
		t.Error("Open truncated a torn tail and reported 0 bytes dropped")
	}
	if err := l.Put(sampleLine("a3")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	lines := readLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("expected the good line and the new one, got %d lines: %q", len(lines), lines)
	}
	for _, ln := range lines {
		var got traininglog.Line
		if err := json.Unmarshal([]byte(ln), &got); err != nil {
			t.Errorf("a line is not valid JSON, so the new write merged into the fragment: %q", ln)
		}
	}
	if !bytes.Contains([]byte(lines[1]), []byte(`"a3"`)) {
		t.Errorf("the second line should be the newly written answer a3, got %q", lines[1])
	}
}

func TestAPutAfterCloseReportsTheFailure(t *testing.T) {
	l, err := traininglog.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := l.Put(sampleLine("a1")); err == nil {
		t.Error("Put on a closed log succeeded; a write that did not happen must be reported")
	}
}

func TestOpenRefusesADirThatIsAFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := traininglog.Open(f); err == nil {
		t.Error("Open on a path that is a regular file succeeded")
	}
}
