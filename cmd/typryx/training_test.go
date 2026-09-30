package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TAIPANBOX/typryx/internal/traininglog"
)

func trainingEnv(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{"TYPRYX_BACKEND": "stub", "TYPRYX_TEMPLATES": validTemplatesDirForTest(t)}
}

func bufLogger() (*slog.Logger, *bytes.Buffer) {
	var b bytes.Buffer
	return slog.New(slog.NewTextHandler(&b, nil)), &b
}

// @test:TestTheTrainingLogIsOffUnlessADirectoryIsNamed
func TestTheTrainingLogIsOffUnlessADirectoryIsNamed(t *testing.T) {
	clearTyprxEnv(t)
	setEnv(t, trainingEnv(t))
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.trainingDir != "" {
		t.Fatalf("with TYPRYX_TRAINING_DIR unset the training dir is %q: the log must be off by default", cfg.trainingDir)
	}

	// Building the runtime in an empty working directory must leave it empty:
	// no training dir, no file, nothing.
	cwd := t.TempDir()
	t.Chdir(cwd)
	log, out := bufLogger()
	rt, err := buildRuntime(cfg, log)
	if err != nil {
		t.Fatalf("buildRuntime: %v", err)
	}
	defer rt.journal.Close()
	if rt.training != nil {
		t.Error("a training log is open with TYPRYX_TRAINING_DIR unset")
	}
	entries, err := os.ReadDir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("building a runtime with the training log off created %d entries in the working directory", len(entries))
	}
	if !strings.Contains(out.String(), "training_log=off") {
		t.Errorf("the boot line must say the training log is off, got: %s", out.String())
	}
}

func TestLoadConfigReadsTheTrainingDir(t *testing.T) {
	clearTyprxEnv(t)
	env := trainingEnv(t)
	env["TYPRYX_TRAINING_DIR"] = "/some/where"
	setEnv(t, env)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.trainingDir != "/some/where" {
		t.Errorf("trainingDir = %q", cfg.trainingDir)
	}
}

// @test:TestTheBootLineSaysWhereTheTrainingLogIsAndNeverWhatIsInIt
func TestTheBootLineSaysWhereTheTrainingLogIsAndNeverWhatIsInIt(t *testing.T) {
	clearTyprxEnv(t)
	dir := filepath.Join(t.TempDir(), "train")
	// A pre-existing line: the boot log must not echo anything from the file.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, traininglog.FileName),
		[]byte(`{"answer_id":"x","state":{"secret":"BOOT-SENTINEL"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := trainingEnv(t)
	env["TYPRYX_TRAINING_DIR"] = dir
	env["TYPRYX_LEDGER_DIR"] = filepath.Join(t.TempDir(), "ledger")
	setEnv(t, env)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	log, out := bufLogger()
	rt, err := buildRuntime(cfg, log)
	if err != nil {
		t.Fatalf("buildRuntime: %v", err)
	}
	defer rt.journal.Close()
	defer rt.ledger.Close()
	defer rt.training.Close()
	if !strings.Contains(out.String(), "training_log="+dir) {
		t.Errorf("the boot line must name the training directory, got: %s", out.String())
	}
	if strings.Contains(out.String(), "BOOT-SENTINEL") {
		t.Errorf("the boot log echoed the contents of the training log: %s", out.String())
	}
	if strings.Contains(out.String(), "WARN") {
		t.Errorf("with a ledger configured there is nothing to warn about, got: %s", out.String())
	}
}

func TestATrainingLogWithNoLedgerWarnsThatNothingCanBeExported(t *testing.T) {
	clearTyprxEnv(t)
	env := trainingEnv(t)
	env["TYPRYX_TRAINING_DIR"] = filepath.Join(t.TempDir(), "train")
	setEnv(t, env)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	log, out := bufLogger()
	rt, err := buildRuntime(cfg, log)
	if err != nil {
		t.Fatalf("buildRuntime: %v", err)
	}
	defer rt.journal.Close()
	defer rt.training.Close()
	if !strings.Contains(out.String(), "TYPRYX_LEDGER_DIR") || !strings.Contains(out.String(), "WARN") {
		t.Errorf("expected a warning naming TYPRYX_LEDGER_DIR, got: %s", out.String())
	}
}

func TestTrainingState(t *testing.T) {
	if got := trainingState(""); got != "off" {
		t.Errorf("trainingState(\"\") = %q", got)
	}
	if got := trainingState("/x/y"); got != "/x/y" {
		t.Errorf("trainingState(/x/y) = %q", got)
	}
}

// --- typryx export ----------------------------------------------------------

func writeExportFixture(t *testing.T) (trainingDir, ledgerDir string) {
	t.Helper()
	trainingDir, ledgerDir = t.TempDir(), t.TempDir()
	tl := `{"answer_id":"a1","answered_at":"2026-09-30T12:00:00Z","template":"t","template_version":"v1","type":"noul","state":{"x":1},"backend":"jev","model":"m"}` + "\n" +
		`{"answer_id":"a2","answered_at":"2026-09-30T12:00:00Z","template":"t","template_version":"v1","type":"noul","state":{"x":2},"backend":"jev","model":"m"}` + "\n"
	ol := `{"answer_id":"a1","template":"t","template_version":"v1","backend":"jev","model":"m","truth":true,"source":"human","recorded_at":"x"}` + "\n"
	if err := os.WriteFile(filepath.Join(trainingDir, traininglog.FileName), []byte(tl), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ledgerDir, "outcomes.ndjson"), []byte(ol), 0o600); err != nil {
		t.Fatal(err)
	}
	return
}

// @test:TestExportWritesRowsToStdoutAndCountsToStderr
func TestExportWritesRowsToStdoutAndCountsToStderr(t *testing.T) {
	clearTyprxEnv(t)
	tdir, ldir := writeExportFixture(t)
	var stdout, stderr bytes.Buffer
	code := exportCmd([]string{"--training", "--training-dir", tdir, "--ledger", ldir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr.String())
	}
	rows := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(rows) != 1 {
		t.Fatalf("expected one row on stdout, got %q", stdout.String())
	}
	var row map[string]json.RawMessage
	if err := json.Unmarshal([]byte(rows[0]), &row); err != nil {
		t.Fatal(err)
	}
	if string(row["label"]) != "true" || string(row["state"]) != `{"x":1}` {
		t.Errorf("row = %s", rows[0])
	}
	if !strings.Contains(stderr.String(), "t exported=1 skipped_no_truth=1") {
		t.Errorf("stderr must carry the per-template counts, got: %s", stderr.String())
	}
	if strings.Contains(stdout.String(), "exported=") {
		t.Errorf("the counts leaked into the JSONL on stdout: %s", stdout.String())
	}
}

func TestExportOutWritesAPrivateFileAndRefusesToWriteInsideAnInput(t *testing.T) {
	clearTyprxEnv(t)
	tdir, ldir := writeExportFixture(t)
	out := filepath.Join(t.TempDir(), "rows.jsonl")
	var stdout, stderr bytes.Buffer
	if code := exportCmd([]string{"--training", "--training-dir", tdir, "--ledger", ldir, "--out", out}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	fi, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("the export file is %o; it holds customer data and must be 0600", fi.Mode().Perm())
	}
	if stdout.Len() != 0 {
		t.Errorf("with --out nothing goes to stdout, got %q", stdout.String())
	}
	b, _ := os.ReadFile(out)
	if !strings.Contains(string(b), `"label":true`) {
		t.Errorf("export file = %s", b)
	}

	for _, inside := range []string{filepath.Join(tdir, "rows.jsonl"), filepath.Join(ldir, "rows.jsonl"), filepath.Join(ldir, "sub", "..", "rows.jsonl")} {
		var so, se bytes.Buffer
		if code := exportCmd([]string{"--training", "--training-dir", tdir, "--ledger", ldir, "--out", inside}, &so, &se); code != 2 {
			t.Errorf("--out %s inside an input directory: exit %d, want 2 (the export is read-only on both)", inside, code)
		}
		if _, err := os.Stat(inside); err == nil {
			t.Errorf("--out %s was written although it is inside an input directory", inside)
		}
	}
}

func TestExportUsageErrors(t *testing.T) {
	clearTyprxEnv(t)
	tdir, ldir := writeExportFixture(t)
	cases := []struct {
		name string
		args []string
	}{
		{"no mode flag", []string{"--training-dir", tdir, "--ledger", ldir}},
		{"no training dir", []string{"--training", "--ledger", ldir}},
		{"no ledger dir", []string{"--training", "--training-dir", tdir}},
		{"missing training dir", []string{"--training", "--training-dir", filepath.Join(tdir, "nope"), "--ledger", ldir}},
		{"missing ledger dir", []string{"--training", "--training-dir", tdir, "--ledger", filepath.Join(ldir, "nope")}},
		{"stray argument", []string{"--training", "--training-dir", tdir, "--ledger", ldir, "extra"}},
		{"unknown flag", []string{"--training", "--bogus"}},
	}
	for _, c := range cases {
		var so, se bytes.Buffer
		if code := exportCmd(c.args, &so, &se); code != 2 {
			t.Errorf("%s: exit %d, want 2 (stderr %q)", c.name, code, se.String())
		}
		if so.Len() != 0 {
			t.Errorf("%s: wrote to stdout: %q", c.name, so.String())
		}
	}
}

func TestExportReadsTheDirectoriesFromTheEnvironmentWhenNoFlagNamesThem(t *testing.T) {
	clearTyprxEnv(t)
	tdir, ldir := writeExportFixture(t)
	setEnv(t, map[string]string{"TYPRYX_TRAINING_DIR": tdir, "TYPRYX_LEDGER_DIR": ldir})
	var so, se bytes.Buffer
	if code := exportCmd([]string{"--training"}, &so, &se); code != 0 {
		t.Fatalf("exit %d: %s", code, se.String())
	}
	if !strings.Contains(so.String(), `"label":true`) {
		t.Errorf("stdout = %s", so.String())
	}
}
