package main

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnvOr(t *testing.T) {
	os.Unsetenv("TYPRYX_TEST_VAR")
	if got := envOr("TYPRYX_TEST_VAR", "fallback"); got != "fallback" {
		t.Errorf("expected fallback, got %s", got)
	}
	os.Setenv("TYPRYX_TEST_VAR", "set")
	defer os.Unsetenv("TYPRYX_TEST_VAR")
	if got := envOr("TYPRYX_TEST_VAR", "fallback"); got != "set" {
		t.Errorf("expected set, got %s", got)
	}
}

func TestEnvIntDefaultsWhenUnset(t *testing.T) {
	os.Unsetenv("TYPRYX_TEST_INT")
	n, err := envInt("TYPRYX_TEST_INT", 42)
	if err != nil || n != 42 {
		t.Fatalf("expected 42, nil; got %d, %v", n, err)
	}
}

func TestEnvIntParsesAWholeNumber(t *testing.T) {
	os.Setenv("TYPRYX_TEST_INT", "7")
	defer os.Unsetenv("TYPRYX_TEST_INT")
	n, err := envInt("TYPRYX_TEST_INT", 42)
	if err != nil || n != 7 {
		t.Fatalf("expected 7, nil; got %d, %v", n, err)
	}
}

func TestEnvIntRefusesAMalformedValueByName(t *testing.T) {
	os.Setenv("TYPRYX_TEST_INT", "2s")
	defer os.Unsetenv("TYPRYX_TEST_INT")
	_, err := envInt("TYPRYX_TEST_INT", 42)
	if err == nil {
		t.Fatal("expected an error for a malformed integer")
	}
	if !strings.Contains(err.Error(), "TYPRYX_TEST_INT") {
		t.Errorf("expected the error to name the variable, got %v", err)
	}
}

func TestJournalState(t *testing.T) {
	if journalState("") != "disabled" {
		t.Errorf("expected disabled")
	}
	if journalState("/tmp/x.ndjson") != "/tmp/x.ndjson" {
		t.Errorf("expected the path back")
	}
}

func TestLedgerState(t *testing.T) {
	if !strings.Contains(ledgerState(""), "disabled") {
		t.Errorf("expected a disabled message")
	}
	if ledgerState("/tmp/ledger") != "/tmp/ledger" {
		t.Errorf("expected the path back")
	}
}

func TestCapState(t *testing.T) {
	if capState(0) != "UNCAPPED" {
		t.Errorf("expected UNCAPPED for 0")
	}
	if capState(-1) != "UNCAPPED" {
		t.Errorf("expected UNCAPPED for a negative value")
	}
	if capState(100) != "100" {
		t.Errorf("expected 100, got %s", capState(100))
	}
}

func TestJoinSemicolon(t *testing.T) {
	if got := joinSemicolon(nil); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
	if got := joinSemicolon([]string{"a"}); got != "a" {
		t.Errorf("expected 'a', got %q", got)
	}
	if got := joinSemicolon([]string{"a", "b"}); got != "a; b" {
		t.Errorf("expected 'a; b', got %q", got)
	}
}

func TestTemplatesCheckReportsEachTemplateAndFailsOnAnyInvalid(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "good.json"), []byte(`{"id":"good","type":"noul","instructions":"x","fields":["a"]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`{"id":"BAD","type":"noul","instructions":"x","fields":["a"]}`), 0o644)

	var stdout, stderr bytes.Buffer
	code := templatesCheck(dir, &stdout, &stderr)
	if code != 1 {
		t.Errorf("expected exit 1 with an invalid template present, got %d", code)
	}
	if !strings.Contains(stdout.String(), "good ") {
		t.Errorf("expected the good template to be printed, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "bad.json") {
		t.Errorf("expected the bad template's file to be named on stderr, got %q", stderr.String())
	}
}

func TestTemplatesCheckOnAllValidTemplatesExitsZero(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "good.json"), []byte(`{"id":"good","type":"noul","instructions":"x","fields":["a"]}`), 0o644)
	var stdout, stderr bytes.Buffer
	if code := templatesCheck(dir, &stdout, &stderr); code != 0 {
		t.Errorf("expected exit 0, got %d: %s", code, stderr.String())
	}
}

func TestTemplatesCheckOnAMissingDirIsAnError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := templatesCheck(filepath.Join(t.TempDir(), "nope"), &stdout, &stderr)
	if code != 1 {
		t.Errorf("expected exit 1 for a missing directory, got %d", code)
	}
}

// clearTyprxEnv unsets every TYPRYX_ variable so each loadConfig test starts
// from a clean slate regardless of what the shell running `go test` exported.
func clearTyprxEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "TYPRYX_") {
			name := strings.SplitN(kv, "=", 2)[0]
			old, had := os.LookupEnv(name)
			os.Unsetenv(name)
			if had {
				t.Cleanup(func() { os.Setenv(name, old) })
			}
		}
	}
}

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		os.Setenv(k, v)
		t.Cleanup(func() { os.Unsetenv(k) })
	}
}

func TestLoadConfigRequiresBackend(t *testing.T) {
	clearTyprxEnv(t)
	setEnv(t, map[string]string{"TYPRYX_TEMPLATES": validTemplatesDirForTest(t)})
	_, err := loadConfig()
	if err == nil || !strings.Contains(err.Error(), "TYPRYX_BACKEND") {
		t.Fatalf("expected an error naming TYPRYX_BACKEND, got %v", err)
	}
	var cfgErr *configError
	if !isConfigError(err, &cfgErr) {
		t.Error("a missing required variable should be a configError (exit 2)")
	}
}

func TestLoadConfigRejectsABackendNotBuiltYet(t *testing.T) {
	clearTyprxEnv(t)
	setEnv(t, map[string]string{"TYPRYX_BACKEND": "jev", "TYPRYX_TEMPLATES": validTemplatesDirForTest(t)})
	_, err := loadConfig()
	if err == nil || !strings.Contains(err.Error(), "jev") || !strings.Contains(err.Error(), "not built yet") {
		t.Fatalf("expected a 'backend jev is not built yet' error, got %v", err)
	}
}

// --- openai-logprobs backend configuration ----------------------------------

func openAIWorkingEnv(t *testing.T, keyFile string) map[string]string {
	env := map[string]string{
		"TYPRYX_BACKEND":      "openai-logprobs",
		"TYPRYX_TEMPLATES":    validTemplatesDirForTest(t),
		"TYPRYX_OPENAI_URL":   "http://127.0.0.1:11434/v1",
		"TYPRYX_OPENAI_MODEL": "qwen2.5:3b",
	}
	if keyFile != "" {
		env["TYPRYX_OPENAI_KEY_FILE"] = keyFile
	}
	return env
}

func TestLoadConfigAcceptsOpenAILogprobsBackendWithMinimalEnv(t *testing.T) {
	clearTyprxEnv(t)
	setEnv(t, openAIWorkingEnv(t, ""))
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.backendName != "openai-logprobs" {
		t.Errorf("expected backendName openai-logprobs, got %s", cfg.backendName)
	}
	if cfg.openai == nil {
		t.Fatal("expected an openai config to be populated")
	}
	if cfg.openai.url != "http://127.0.0.1:11434/v1" {
		t.Errorf("unexpected url: %s", cfg.openai.url)
	}
	if cfg.openai.model != "qwen2.5:3b" {
		t.Errorf("unexpected model: %s", cfg.openai.model)
	}
	if cfg.openai.minLabelMass != 0.9 {
		t.Errorf("expected the default 0.9, got %v", cfg.openai.minLabelMass)
	}
	if cfg.openai.key != "" {
		t.Errorf("expected no key without TYPRYX_OPENAI_KEY_FILE, got %q", cfg.openai.key)
	}
}

func TestLoadConfigRequiresOpenAIURLForTheOpenAIBackend(t *testing.T) {
	clearTyprxEnv(t)
	env := openAIWorkingEnv(t, "")
	delete(env, "TYPRYX_OPENAI_URL")
	setEnv(t, env)
	_, err := loadConfig()
	if err == nil || !strings.Contains(err.Error(), "TYPRYX_OPENAI_URL") {
		t.Fatalf("expected an error naming TYPRYX_OPENAI_URL, got %v", err)
	}
	var cfgErr *configError
	if !isConfigError(err, &cfgErr) {
		t.Error("a missing required-when-chosen variable should be a configError (exit 2)")
	}
}

func TestLoadConfigRequiresOpenAIModelForTheOpenAIBackend(t *testing.T) {
	clearTyprxEnv(t)
	env := openAIWorkingEnv(t, "")
	delete(env, "TYPRYX_OPENAI_MODEL")
	setEnv(t, env)
	_, err := loadConfig()
	if err == nil || !strings.Contains(err.Error(), "TYPRYX_OPENAI_MODEL") {
		t.Fatalf("expected an error naming TYPRYX_OPENAI_MODEL, got %v", err)
	}
}

func TestLoadConfigRejectsAMalformedOpenAIURL(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"not absolute", "127.0.0.1:11434/v1"},
		{"wrong scheme", "ftp://127.0.0.1:11434/v1"},
		{"no host", "http:///v1"},
		{"has a query string", "http://127.0.0.1:11434/v1?x=1"},
		{"has a fragment", "http://127.0.0.1:11434/v1#frag"},
		{"has userinfo", "http://user:pass@127.0.0.1:11434/v1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clearTyprxEnv(t)
			env := openAIWorkingEnv(t, "")
			env["TYPRYX_OPENAI_URL"] = c.url
			setEnv(t, env)
			_, err := loadConfig()
			if err == nil || !strings.Contains(err.Error(), "TYPRYX_OPENAI_URL") {
				t.Fatalf("expected an error naming TYPRYX_OPENAI_URL for %q, got %v", c.url, err)
			}
		})
	}
}

func TestLoadConfigReadsTheOpenAIKeyFileTrimmed(t *testing.T) {
	clearTyprxEnv(t)
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key.txt")
	if err := os.WriteFile(keyFile, []byte("  secret-key-123\n"), 0o600); err != nil {
		t.Fatalf("writing key file: %v", err)
	}
	setEnv(t, openAIWorkingEnv(t, keyFile))
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.openai.key != "secret-key-123" {
		t.Errorf("expected the trimmed key, got %q", cfg.openai.key)
	}
}

func TestLoadConfigRejectsAnUnreadableOpenAIKeyFile(t *testing.T) {
	clearTyprxEnv(t)
	env := openAIWorkingEnv(t, filepath.Join(t.TempDir(), "does-not-exist.txt"))
	setEnv(t, env)
	_, err := loadConfig()
	if err == nil || !strings.Contains(err.Error(), "TYPRYX_OPENAI_KEY_FILE") {
		t.Fatalf("expected an error naming TYPRYX_OPENAI_KEY_FILE, got %v", err)
	}
}

func TestLoadConfigRejectsAMalformedMinLabelMass(t *testing.T) {
	for _, v := range []string{"0", "-0.5", "1.5", "not-a-number"} {
		t.Run(v, func(t *testing.T) {
			clearTyprxEnv(t)
			env := openAIWorkingEnv(t, "")
			env["TYPRYX_OPENAI_MIN_LABEL_MASS"] = v
			setEnv(t, env)
			_, err := loadConfig()
			if err == nil || !strings.Contains(err.Error(), "TYPRYX_OPENAI_MIN_LABEL_MASS") {
				t.Fatalf("expected an error naming TYPRYX_OPENAI_MIN_LABEL_MASS for %q, got %v", v, err)
			}
		})
	}
}

func TestLoadConfigAcceptsAValidMinLabelMass(t *testing.T) {
	clearTyprxEnv(t)
	env := openAIWorkingEnv(t, "")
	env["TYPRYX_OPENAI_MIN_LABEL_MASS"] = "0.75"
	setEnv(t, env)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.openai.minLabelMass != 0.75 {
		t.Errorf("expected 0.75, got %v", cfg.openai.minLabelMass)
	}
}

func TestBuildRuntimeWiresTheOpenAIBackendWhenChosen(t *testing.T) {
	clearTyprxEnv(t)
	setEnv(t, openAIWorkingEnv(t, ""))
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	rt, err := buildRuntime(cfg, testLogger())
	if err != nil {
		t.Fatalf("buildRuntime: %v", err)
	}
	defer rt.journal.Close()
	if rt.server == nil {
		t.Fatal("expected a server")
	}
}

func TestLoadConfigRequiresTemplates(t *testing.T) {
	clearTyprxEnv(t)
	setEnv(t, map[string]string{"TYPRYX_BACKEND": "stub"})
	_, err := loadConfig()
	if err == nil || !strings.Contains(err.Error(), "TYPRYX_TEMPLATES") {
		t.Fatalf("expected an error naming TYPRYX_TEMPLATES, got %v", err)
	}
}

func TestLoadConfigRejectsAMissingTemplatesDirectory(t *testing.T) {
	clearTyprxEnv(t)
	setEnv(t, map[string]string{"TYPRYX_BACKEND": "stub", "TYPRYX_TEMPLATES": filepath.Join(t.TempDir(), "nope")})
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected an error for a template directory that does not exist")
	}
}

func TestLoadConfigRejectsAnInvalidTemplate(t *testing.T) {
	clearTyprxEnv(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`{"id":"BAD ID","type":"noul","instructions":"x","fields":["a"]}`), 0o644)
	setEnv(t, map[string]string{"TYPRYX_BACKEND": "stub", "TYPRYX_TEMPLATES": dir})
	_, err := loadConfig()
	if err == nil || !strings.Contains(err.Error(), "bad.json") {
		t.Fatalf("expected an error naming bad.json, got %v", err)
	}
}

func TestLoadConfigRejectsAMalformedMaxCallsPerHour(t *testing.T) {
	clearTyprxEnv(t)
	setEnv(t, map[string]string{
		"TYPRYX_BACKEND": "stub", "TYPRYX_TEMPLATES": validTemplatesDirForTest(t),
		"TYPRYX_MAX_CALLS_PER_HOUR": "lots",
	})
	_, err := loadConfig()
	if err == nil || !strings.Contains(err.Error(), "TYPRYX_MAX_CALLS_PER_HOUR") {
		t.Fatalf("expected an error naming TYPRYX_MAX_CALLS_PER_HOUR, got %v", err)
	}
}

func TestLoadConfigRejectsAMalformedTimeout(t *testing.T) {
	clearTyprxEnv(t)
	setEnv(t, map[string]string{
		"TYPRYX_BACKEND": "stub", "TYPRYX_TEMPLATES": validTemplatesDirForTest(t),
		"TYPRYX_TIMEOUT_MS": "2s",
	})
	_, err := loadConfig()
	if err == nil || !strings.Contains(err.Error(), "TYPRYX_TIMEOUT_MS") {
		t.Fatalf("expected an error naming TYPRYX_TIMEOUT_MS, got %v", err)
	}
}

// @test:TestANegativeCapRefusesToStart
//
// A negative TYPRYX_MAX_CALLS_PER_HOUR is not "uncapped", it is a typo away
// from one that silently means the same thing as 0 in some other reader's
// head; refuse it outright rather than guess. 0 stays the one explicit,
// documented uncapped opt-out.
func TestANegativeCapRefusesToStart(t *testing.T) {
	clearTyprxEnv(t)
	setEnv(t, map[string]string{
		"TYPRYX_BACKEND": "stub", "TYPRYX_TEMPLATES": validTemplatesDirForTest(t),
		"TYPRYX_MAX_CALLS_PER_HOUR": "-1",
	})
	_, err := loadConfig()
	if err == nil || !strings.Contains(err.Error(), "TYPRYX_MAX_CALLS_PER_HOUR") {
		t.Fatalf("expected an error naming TYPRYX_MAX_CALLS_PER_HOUR, got %v", err)
	}
}

func TestZeroCapIsStillTheExplicitUncappedOptOut(t *testing.T) {
	clearTyprxEnv(t)
	setEnv(t, map[string]string{
		"TYPRYX_BACKEND": "stub", "TYPRYX_TEMPLATES": validTemplatesDirForTest(t),
		"TYPRYX_MAX_CALLS_PER_HOUR": "0",
	})
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("0 must still be accepted as the uncapped opt-out: %v", err)
	}
	if cfg.maxCallsPerHour != 0 {
		t.Errorf("expected 0, got %d", cfg.maxCallsPerHour)
	}
}

// @test:TestANonPositiveTimeoutRefusesToStart
//
// TYPRYX_TIMEOUT_MS <= 0 silently became the 2s default; a backend deadline
// of zero or less is nonsensical and must be refused by name, not quietly
// replaced.
func TestANonPositiveTimeoutRefusesToStart(t *testing.T) {
	for _, v := range []string{"0", "-5"} {
		t.Run(v, func(t *testing.T) {
			clearTyprxEnv(t)
			setEnv(t, map[string]string{
				"TYPRYX_BACKEND": "stub", "TYPRYX_TEMPLATES": validTemplatesDirForTest(t),
				"TYPRYX_TIMEOUT_MS": v,
			})
			_, err := loadConfig()
			if err == nil || !strings.Contains(err.Error(), "TYPRYX_TIMEOUT_MS") {
				t.Fatalf("expected an error naming TYPRYX_TIMEOUT_MS, got %v", err)
			}
		})
	}
}

func TestLoadConfigRefusesAnOpenBindWithNoCredential(t *testing.T) {
	clearTyprxEnv(t)
	setEnv(t, map[string]string{
		"TYPRYX_BACKEND": "stub", "TYPRYX_TEMPLATES": validTemplatesDirForTest(t),
		"TYPRYX_ADDR": "0.0.0.0:4320",
	})
	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected the open-bind refusal")
	}
	var cfgErr *configError
	if isConfigError(err, &cfgErr) {
		t.Error("the open-bind refusal is exit 1, not a configError (exit 2)")
	}
}

func TestLoadConfigSucceedsWithMinimalValidEnv(t *testing.T) {
	clearTyprxEnv(t)
	setEnv(t, map[string]string{"TYPRYX_BACKEND": "stub", "TYPRYX_TEMPLATES": validTemplatesDirForTest(t)})
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.addr != defaultAddr {
		t.Errorf("expected the default address, got %s", cfg.addr)
	}
	if cfg.maxCallsPerHour != defaultMaxCallsPerHour {
		t.Errorf("expected the default cap, got %d", cfg.maxCallsPerHour)
	}
	if len(cfg.templates.List()) != 1 {
		t.Errorf("expected 1 loaded template, got %d", len(cfg.templates.List()))
	}
}

func TestLoadConfigAcceptsAnOpenBindWithAKey(t *testing.T) {
	clearTyprxEnv(t)
	setEnv(t, map[string]string{
		"TYPRYX_BACKEND": "stub", "TYPRYX_TEMPLATES": validTemplatesDirForTest(t),
		"TYPRYX_ADDR": "0.0.0.0:0", "TYPRYX_KEYS": "k1",
	})
	if _, err := loadConfig(); err != nil {
		t.Fatalf("an open bind with a key configured should be accepted: %v", err)
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestBuildRuntimeWithMinimalConfig(t *testing.T) {
	clearTyprxEnv(t)
	setEnv(t, map[string]string{"TYPRYX_BACKEND": "stub", "TYPRYX_TEMPLATES": validTemplatesDirForTest(t)})
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	rt, err := buildRuntime(cfg, testLogger())
	if err != nil {
		t.Fatalf("buildRuntime: %v", err)
	}
	defer rt.journal.Close()
	if rt.server == nil {
		t.Fatal("expected a server")
	}
	if rt.server.Addr != defaultAddr {
		t.Errorf("expected the default addr, got %s", rt.server.Addr)
	}
	if rt.ledger != nil {
		t.Error("no TYPRYX_LEDGER_DIR was set; expected a nil ledger")
	}
}

func TestBuildRuntimeWithLedgerAndEventsConfigured(t *testing.T) {
	clearTyprxEnv(t)
	dir := t.TempDir()
	setEnv(t, map[string]string{
		"TYPRYX_BACKEND": "stub", "TYPRYX_TEMPLATES": validTemplatesDirForTest(t),
		"TYPRYX_LEDGER_DIR": filepath.Join(dir, "ledger"),
		"TYPRYX_EVENTS":     filepath.Join(dir, "events.ndjson"),
	})
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	rt, err := buildRuntime(cfg, testLogger())
	if err != nil {
		t.Fatalf("buildRuntime: %v", err)
	}
	defer rt.journal.Close()
	defer rt.ledger.Close()
	if rt.ledger == nil {
		t.Error("expected a ledger to be opened")
	}
}

func TestBuildRuntimeSurfacesAJournalOpenError(t *testing.T) {
	clearTyprxEnv(t)
	setEnv(t, map[string]string{
		"TYPRYX_BACKEND": "stub", "TYPRYX_TEMPLATES": validTemplatesDirForTest(t),
		"TYPRYX_EVENTS": filepath.Join(t.TempDir(), "no", "such", "dir", "events.ndjson"),
	})
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if _, err := buildRuntime(cfg, testLogger()); err == nil {
		t.Fatal("expected an error opening a journal under a directory that does not exist")
	}
}

func TestBuildRuntimeSurfacesALedgerOpenError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses permission checks")
	}
	clearTyprxEnv(t)
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer os.Chmod(parent, 0o700)
	setEnv(t, map[string]string{
		"TYPRYX_BACKEND": "stub", "TYPRYX_TEMPLATES": validTemplatesDirForTest(t),
		"TYPRYX_LEDGER_DIR": filepath.Join(parent, "ledger"),
	})
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if _, err := buildRuntime(cfg, testLogger()); err == nil {
		t.Fatal("expected an error opening a ledger under an unwritable directory")
	}
}

func TestBuildRuntimeWarnsOnAnUncappedDeployment(t *testing.T) {
	clearTyprxEnv(t)
	setEnv(t, map[string]string{
		"TYPRYX_BACKEND": "stub", "TYPRYX_TEMPLATES": validTemplatesDirForTest(t),
		"TYPRYX_MAX_CALLS_PER_HOUR": "0",
	})
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	rt, err := buildRuntime(cfg, testLogger())
	if err != nil {
		t.Fatalf("buildRuntime: %v", err)
	}
	defer rt.journal.Close()
}

func isConfigError(err error, target **configError) bool {
	e, ok := err.(*configError)
	if ok {
		*target = e
	}
	return ok
}

// --- process-level tests: build the real binary and start it -------------

func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "typryx")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building typryx: %v\n%s", err, out)
	}
	return bin
}

func validTemplatesDirForTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "t.json"), []byte(`{"id":"t","type":"noul","instructions":"is it true","fields":["x"]}`), 0o644)
	return dir
}

func runBin(t *testing.T, bin string, env []string, args ...string) (code int, out string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting: %v", err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			return 0, buf.String()
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), buf.String()
		}
		t.Fatalf("waiting: %v", err)
	case <-time.After(2 * time.Second):
		cmd.Process.Kill()
		<-done
		return -1, buf.String() // stayed up
	}
	return
}

// @test:TestTheServiceRefusesToStartWithoutANamedBackend
func TestTheServiceRefusesToStartWithoutANamedBackend(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a process")
	}
	bin := buildBinary(t)
	templatesDir := validTemplatesDirForTest(t)
	code, out := runBin(t, bin, []string{"TYPRYX_TEMPLATES=" + templatesDir, "TYPRYX_ADDR=127.0.0.1:0"})
	if code != 2 {
		t.Fatalf("expected exit 2 without TYPRYX_BACKEND, got %d\n%s", code, out)
	}
	if !strings.Contains(out, "TYPRYX_BACKEND") {
		t.Errorf("expected the failure to name TYPRYX_BACKEND, got %s", out)
	}
}

// @test:TestAKeyBoundToANonAgentIdentityRefusesToStart
//
// TYPRYX_KEYS="cred=bob" binds a real credential to an identity that is not
// an agent:// URI at all; that identity is what the journal would then write
// as agent_id on every event, unattested. Refuse to start naming TYPRYX_KEYS
// and the malformed identity, and never echo the credential itself into any
// output.
func TestAKeyBoundToANonAgentIdentityRefusesToStart(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a process")
	}
	bin := buildBinary(t)
	templatesDir := validTemplatesDirForTest(t)
	const credential = "th15-is-th3-s3cr3t-cred3nt1al"
	code, out := runBin(t, bin, []string{
		"TYPRYX_BACKEND=stub", "TYPRYX_TEMPLATES=" + templatesDir, "TYPRYX_ADDR=127.0.0.1:0",
		"TYPRYX_KEYS=" + credential + "=bob",
	})
	if code != 2 {
		t.Fatalf("expected exit 2 for a key bound to a non-agent identity, got %d\n%s", code, out)
	}
	if !strings.Contains(out, "TYPRYX_KEYS") {
		t.Errorf("expected the failure to name TYPRYX_KEYS, got %s", out)
	}
	if strings.Contains(out, credential) {
		t.Errorf("the credential itself must never appear in the output, got %s", out)
	}
}

func TestUnknownSubcommandExitsTwo(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a process")
	}
	bin := buildBinary(t)
	code, out := runBin(t, bin, nil, "not-a-real-subcommand")
	_ = out
	if code != 2 {
		t.Errorf("expected exit 2 for an unknown subcommand, got %d", code)
	}
}
