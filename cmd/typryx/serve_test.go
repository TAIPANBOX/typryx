package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/TAIPANBOX/typryx/internal/ledger"
	"github.com/TAIPANBOX/typryx/internal/record"
)

// The serve loop itself: run, runWardryxProxy and serveRuntime, driven
// in-process. A port already held by the test stands in for a listener that
// cannot bind; a SIGINT the test sends to itself stands in for an operator
// stopping the service.

// busyAddr returns an address some other listener already holds.
func busyAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String()
}

// aRegularFile returns a path that exists and is not a directory, so any
// attempt to create a directory there fails.
func aRegularFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// storesEnv is a working stub configuration with every store switched on.
func storesEnv(t *testing.T, addr string) map[string]string {
	dir := t.TempDir()
	return map[string]string{
		"TYPRYX_BACKEND":      "stub",
		"TYPRYX_TEMPLATES":    validTemplatesDirForTest(t),
		"TYPRYX_ADDR":         addr,
		"TYPRYX_EVENTS":       filepath.Join(dir, "events.ndjson"),
		"TYPRYX_LEDGER_DIR":   filepath.Join(dir, "ledger"),
		"TYPRYX_TRAINING_DIR": filepath.Join(dir, "training"),
	}
}

func builtRuntime(t *testing.T, env map[string]string) *runtime {
	t.Helper()
	clearTyprxEnv(t)
	setEnv(t, env)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	rt, err := buildRuntime(cfg, testLogger())
	if err != nil {
		t.Fatalf("buildRuntime: %v", err)
	}
	return rt
}

func TestServeRuntimeReturnsTheListenerFailureAndClosesEveryStore(t *testing.T) {
	rt := builtRuntime(t, storesEnv(t, busyAddr(t)))
	log, buf := bufLogger()

	err := serveRuntime(rt, log)
	if err == nil || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("expected the bind failure back, got %v", err)
	}
	// Each store was closed on the way out: closing it again fails.
	if rt.ledger.Close() == nil {
		t.Error("the ledger was left open")
	}
	if rt.training.Close() == nil {
		t.Error("the training log was left open")
	}
	if rt.journal.Close() == nil {
		t.Error("the journal was left open")
	}
	if strings.Contains(buf.String(), "did not close cleanly") {
		t.Errorf("a clean close logged a failure: %s", buf.String())
	}
}

func TestServeRuntimeLogsEachStoreThatDidNotCloseCleanly(t *testing.T) {
	rt := builtRuntime(t, storesEnv(t, busyAddr(t)))
	_ = rt.ledger.Close()
	_ = rt.training.Close()
	_ = rt.journal.Close()
	log, buf := bufLogger()

	if err := serveRuntime(rt, log); err == nil {
		t.Fatal("expected the bind failure back")
	}
	for _, want := range []string{
		"the ledger did not close cleanly",
		"the training log did not close cleanly",
		"the journal did not close cleanly",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, buf.String())
		}
	}
}

func TestServeRuntimeShutsDownCleanlyOnASignalAndSaysTheRecordIsIncomplete(t *testing.T) {
	env := storesEnv(t, "127.0.0.1:0")
	rt := builtRuntime(t, env)
	// One event with no agent identity: skipped and counted, which the
	// shutdown line must report.
	if got := rt.journal.Answer("", "", record.AnswerData{Template: "t"}); got != record.SkippedNoAgentID {
		t.Fatalf("expected the event to be skipped, got %v", got)
	}

	// While this channel is registered a SIGINT is delivered, never fatal,
	// so a signal that lands before serveRuntime's own Notify is harmless.
	guard := make(chan os.Signal, 64)
	signal.Notify(guard, os.Interrupt)
	defer signal.Stop(guard)

	log, buf := bufLogger()
	done := make(chan error, 1)
	go func() { done <- serveRuntime(rt, log) }()

	deadline := time.After(10 * time.Second)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	var err error
wait:
	for {
		select {
		case err = <-done:
			break wait
		case <-tick.C:
			_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
		case <-deadline:
			t.Fatal("serveRuntime did not return after SIGINT")
		}
	}
	if err != nil {
		t.Fatalf("a signalled shutdown must return nil, got %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "shutting down") {
		t.Errorf("log lacks the shutdown line:\n%s", out)
	}
	if !strings.Contains(out, "the record is incomplete") || !strings.Contains(out, "skipped_no_agent_id=1") {
		t.Errorf("log lacks the incomplete-record warning with its count:\n%s", out)
	}
	if rt.ledger.Close() == nil || rt.training.Close() == nil {
		t.Error("a signalled shutdown left a store open")
	}
}

func TestRunRefusesABadConfigurationAsAConfigError(t *testing.T) {
	clearTyprxEnv(t)
	err := run(testLogger())
	var cfg *configError
	if !errors.As(err, &cfg) || !strings.Contains(err.Error(), "TYPRYX_BACKEND") {
		t.Fatalf("expected a configError naming TYPRYX_BACKEND, got %v", err)
	}
}

func TestRunReturnsAStoreThatCannotBeOpened(t *testing.T) {
	env := storesEnv(t, busyAddr(t))
	env["TYPRYX_LEDGER_DIR"] = aRegularFile(t)
	clearTyprxEnv(t)
	setEnv(t, env)
	err := run(testLogger())
	if err == nil || !strings.Contains(err.Error(), "ledger: creating") {
		t.Fatalf("expected the ledger failure, got %v", err)
	}
	var cfg *configError
	if errors.As(err, &cfg) {
		t.Errorf("a store failure is not a configuration error (exit 1, not 2): %v", err)
	}
}

func TestRunReturnsTheListenerFailure(t *testing.T) {
	clearTyprxEnv(t)
	setEnv(t, storesEnv(t, busyAddr(t)))
	err := run(testLogger())
	if err == nil || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("expected the bind failure, got %v", err)
	}
}

func TestBuildServiceClosesTheLedgerWhenTheTrainingLogCannotOpen(t *testing.T) {
	env := storesEnv(t, "127.0.0.1:0")
	env["TYPRYX_TRAINING_DIR"] = aRegularFile(t)
	clearTyprxEnv(t)
	setEnv(t, env)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	_, rt, err := buildService(cfg, testLogger())
	if err == nil || !strings.Contains(err.Error(), "traininglog: creating") || rt != nil {
		t.Fatalf("expected the training log failure and no runtime, got %v, %v", rt, err)
	}
}

func TestBuildServiceWarnsAboutTornTailsInTheLedgerAndTheTrainingLog(t *testing.T) {
	env := storesEnv(t, "127.0.0.1:0")
	for _, p := range []string{
		filepath.Join(env["TYPRYX_LEDGER_DIR"], "answers.ndjson"),
		filepath.Join(env["TYPRYX_TRAINING_DIR"], "training.ndjson"),
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(`{"answer_id":"half`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	clearTyprxEnv(t)
	setEnv(t, env)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	log, buf := bufLogger()
	_, rt, err := buildService(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rt.journal.Close(); _ = rt.ledger.Close(); _ = rt.training.Close() }()
	out := buf.String()
	if !strings.Contains(out, "the ledger's answer log had a torn last line, skipped") || !strings.Contains(out, "count=1") {
		t.Errorf("log lacks the ledger torn-tail warning with its count:\n%s", out)
	}
	if !strings.Contains(out, "the training log had a torn last line, truncated") || !strings.Contains(out, "bytes=18") {
		t.Errorf("log lacks the training torn-tail warning with its byte count:\n%s", out)
	}
}

func TestRunWardryxProxyRefusesAMissingUpstreamAsAConfigError(t *testing.T) {
	proxyEnv(t, nil)
	t.Setenv("TYPRYX_PROXY_UPSTREAM", "")
	err := runWardryxProxy(testLogger())
	var cfg *configError
	if !errors.As(err, &cfg) || !strings.Contains(err.Error(), "TYPRYX_PROXY_UPSTREAM") {
		t.Fatalf("expected a configError naming TYPRYX_PROXY_UPSTREAM, got %v", err)
	}
}

func TestRunWardryxProxyReturnsAStoreThatCannotBeOpened(t *testing.T) {
	proxyEnv(t, map[string]string{"TYPRYX_LEDGER_DIR": aRegularFile(t)})
	err := runWardryxProxy(testLogger())
	if err == nil || !strings.Contains(err.Error(), "ledger: creating") {
		t.Fatalf("expected the ledger failure, got %v", err)
	}
}

func TestRunWardryxProxyReturnsTheListenerFailure(t *testing.T) {
	proxyEnv(t, map[string]string{"TYPRYX_PROXY_ADDR": busyAddr(t)})
	err := runWardryxProxy(testLogger())
	if err == nil || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("expected the bind failure, got %v", err)
	}
}

// failWriter refuses every write, as a closed stdout would.
type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("stdout closed") }

func TestCalibrationCommandEdges(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		code   int
		errHas string
	}{
		{name: "unknown flag", args: []string{"--nope"}, code: 2, errHas: "flag provided but not defined"},
		{name: "stray argument", args: []string{"--ledger", t.TempDir(), "extra"}, code: 2, errHas: `unexpected argument "extra"`},
		{name: "ledger is not a directory", args: []string{"--ledger", aRegularFile(t)}, code: 2, errHas: "typryx calibration: calibration: reading"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var so, se bytes.Buffer
			if code := calibrationCmd(c.args, &so, &se); code != c.code || !strings.Contains(se.String(), c.errHas) {
				t.Errorf("exit %d (want %d), stderr %q (want %q)", code, c.code, se.String(), c.errHas)
			}
		})
	}
}

func TestCalibrationJSONThatCannotBeWrittenIsAnError(t *testing.T) {
	var se bytes.Buffer
	if code := calibrationCmd([]string{"--ledger", t.TempDir(), "--json"}, failWriter{}, &se); code != 2 || !strings.Contains(se.String(), "encoding JSON: stdout closed") {
		t.Errorf("exit %d, stderr %q", code, se.String())
	}
}

func TestCalibrationMaxBrierJudgesTheBrierScore(t *testing.T) {
	// Every answer is certain and right: Brier 0, inside the bound.
	dir := t.TempDir()
	writeDriftLedger(t, dir, true)
	var so, se bytes.Buffer
	if code := calibrationCmd([]string{"--ledger", dir, "--min-n", "10", "--max-brier", "0.01"}, &so, &se); code != 0 {
		t.Errorf("a perfect group under a Brier bound must pass: exit %d, %s %s", code, so.String(), se.String())
	}
	// Every answer certain and wrong: a Brier score far over 0.5.
	dir2 := t.TempDir()
	writeDriftLedger(t, dir2, false)
	so.Reset()
	if code := calibrationCmd([]string{"--ledger", dir2, "--min-n", "10", "--max-brier", "0.5"}, &so, &se); code != 1 || !strings.Contains(so.String(), "drift") {
		t.Errorf("a wrong group over the Brier bound must drift: exit %d, %s", code, so.String())
	}
}

// writeDriftLedger writes 20 certain "true" answers under model m1, right
// or wrong as asked.
func writeDriftLedger(t *testing.T, dir string, right bool) {
	t.Helper()
	appendNoulGroup(t, dir, "m1", right)
}

// appendNoulGroup appends 20 answers under model, each stating "true" with
// certainty, and an outcome for each that is true when right, false when not.
func appendNoulGroup(t *testing.T, dir, model string, right bool) {
	t.Helper()
	var ans, outs []byte
	for i := 0; i < 20; i++ {
		id := model + "-" + string(rune('a'+i))
		a, err := json.Marshal(ledger.AnswerRecord{
			AnswerID: id, Template: "t", TemplateVersion: "v1", Type: "noul", Backend: "stub", Model: model,
			Probabilities: map[string]float64{"true": 1, "false": 0},
		})
		if err != nil {
			t.Fatal(err)
		}
		o, err := json.Marshal(ledger.OutcomeRecord{AnswerID: id, Truth: rawBool(t, right)})
		if err != nil {
			t.Fatal(err)
		}
		ans = append(append(ans, a...), '\n')
		outs = append(append(outs, o...), '\n')
	}
	for name, b := range map[string][]byte{"answers.ndjson": ans, "outcomes.ndjson": outs} {
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(b); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
}

func TestEmitDriftReportsAnEventFileItCannotOpen(t *testing.T) {
	dir := t.TempDir()
	writeDriftLedger(t, dir, false)
	bad := filepath.Join(aRegularFile(t), "events.ndjson")
	var so, se bytes.Buffer
	code := calibrationCmd([]string{"--ledger", dir, "--min-n", "10", "--max-brier", "0.5", "--emit", bad, "--agent-id", "agent://h/a"}, &so, &se)
	if code != 1 {
		t.Errorf("the verdict still stands when --emit fails: exit %d", code)
	}
	if !strings.Contains(se.String(), "--emit could not open "+bad) {
		t.Errorf("stderr: %q", se.String())
	}
}

func TestEmitDriftWritesOnlyTheGroupsThatDrifted(t *testing.T) {
	dir := t.TempDir()
	writeDriftLedger(t, dir, false)
	// A second, well-calibrated group under another model.
	appendNoulGroup(t, dir, "m2", true)
	events := filepath.Join(t.TempDir(), "events.ndjson")
	var so, se bytes.Buffer
	code := calibrationCmd([]string{"--ledger", dir, "--min-n", "10", "--max-brier", "0.5", "--emit", events, "--agent-id", "agent://h/a"}, &so, &se)
	if code != 1 {
		t.Fatalf("exit %d: %s %s", code, so.String(), se.String())
	}
	if !strings.Contains(se.String(), "wrote 1 calibration_drift event(s)") {
		t.Errorf("exactly the one drifting group must be written, stderr: %q", se.String())
	}
	b, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), "calibration_drift"); n != 1 {
		t.Errorf("expected one calibration_drift event on disk, got %d:\n%s", n, b)
	}
}
