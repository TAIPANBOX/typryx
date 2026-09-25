package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TAIPANBOX/agent-stack-go/event"
	"github.com/TAIPANBOX/typryx/internal/ledger"
)

func writeCalLedger(t *testing.T, dir string, ans []ledger.AnswerRecord, outs []ledger.OutcomeRecord) {
	t.Helper()
	var ansLines, outLines []string
	for _, a := range ans {
		b, err := json.Marshal(a)
		if err != nil {
			t.Fatalf("marshal answer: %v", err)
		}
		ansLines = append(ansLines, string(b))
	}
	for _, o := range outs {
		b, err := json.Marshal(o)
		if err != nil {
			t.Fatalf("marshal outcome: %v", err)
		}
		outLines = append(outLines, string(b))
	}
	if len(ansLines) > 0 {
		if err := os.WriteFile(filepath.Join(dir, "answers.ndjson"), []byte(strings.Join(ansLines, "\n")+"\n"), 0o644); err != nil {
			t.Fatalf("writing answers.ndjson: %v", err)
		}
	}
	if len(outLines) > 0 {
		if err := os.WriteFile(filepath.Join(dir, "outcomes.ndjson"), []byte(strings.Join(outLines, "\n")+"\n"), 0o644); err != nil {
			t.Fatalf("writing outcomes.ndjson: %v", err)
		}
	}
}

func rawBool(t *testing.T, b bool) json.RawMessage {
	t.Helper()
	v, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return v
}

// @test:TestCalibrationRequiresALedgerDirectory
func TestCalibrationRequiresALedgerDirectory(t *testing.T) {
	t.Setenv("TYPRYX_LEDGER_DIR", "")
	var stdout, stderr bytes.Buffer
	code := calibrationCmd(nil, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "--ledger") || !strings.Contains(stderr.String(), "TYPRYX_LEDGER_DIR") {
		t.Errorf("expected the error to name both --ledger and TYPRYX_LEDGER_DIR, got %q", stderr.String())
	}
}

// @test:TestCalibrationFallsBackToTheEnvironmentVariable
func TestCalibrationFallsBackToTheEnvironmentVariable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TYPRYX_LEDGER_DIR", dir)
	var stdout, stderr bytes.Buffer
	code := calibrationCmd(nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0 on an empty ledger, got %d, stderr=%s", code, stderr.String())
	}
}

// @test:TestCalibrationExitsOneWhenAGroupDrifts
func TestCalibrationExitsOneWhenAGroupDrifts(t *testing.T) {
	dir := t.TempDir()
	var ans []ledger.AnswerRecord
	var outs []ledger.OutcomeRecord
	for i := 0; i < 40; i++ {
		id := "a" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		ans = append(ans, ledger.AnswerRecord{
			AnswerID: id, Template: "t", TemplateVersion: "v1", Type: "noul", Backend: "stub", Model: "m1",
			Probabilities: map[string]float64{"true": 0.99, "false": 0.01},
		})
		// always wrong: massively overconfident.
		outs = append(outs, ledger.OutcomeRecord{AnswerID: id, Truth: rawBool(t, false)})
	}
	writeCalLedger(t, dir, ans, outs)

	var stdout, stderr bytes.Buffer
	code := calibrationCmd([]string{"--ledger", dir, "--min-n", "10", "--max-ece", "0.1"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1 for a drifting group, got %d\nstdout=%s\nstderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "drift") {
		t.Errorf("expected the text output to name the drift verdict, got %q", stdout.String())
	}
}

// @test:TestCalibrationJSONIncludesTheBins
func TestCalibrationJSONIncludesTheBins(t *testing.T) {
	dir := t.TempDir()
	writeCalLedger(t, dir, []ledger.AnswerRecord{
		{AnswerID: "a1", Template: "t", TemplateVersion: "v1", Type: "noul", Backend: "stub", Model: "m1",
			Probabilities: map[string]float64{"true": 0.7, "false": 0.3}},
	}, []ledger.OutcomeRecord{
		{AnswerID: "a1", Truth: rawBool(t, true)},
	})
	var stdout, stderr bytes.Buffer
	code := calibrationCmd([]string{"--ledger", dir, "--min-n", "1", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d, stderr=%s", code, stderr.String())
	}
	var parsed struct {
		Groups []struct {
			Bins []struct {
				N int `json:"n"`
			} `json:"bins"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		t.Fatalf("expected valid JSON, got %v: %s", err, stdout.String())
	}
	if len(parsed.Groups) != 1 || len(parsed.Groups[0].Bins) != 10 {
		t.Fatalf("expected 1 group with 10 bins, got %+v", parsed)
	}
}

// @test:TestEmitWithoutAgentIDSkipsAndSaysSo
func TestEmitWithoutAgentIDSkipsAndSaysSo(t *testing.T) {
	dir := t.TempDir()
	var ans []ledger.AnswerRecord
	var outs []ledger.OutcomeRecord
	for i := 0; i < 40; i++ {
		id := "a" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		ans = append(ans, ledger.AnswerRecord{
			AnswerID: id, Template: "t", TemplateVersion: "v1", Type: "noul", Backend: "stub", Model: "m1",
			Probabilities: map[string]float64{"true": 0.99, "false": 0.01},
		})
		outs = append(outs, ledger.OutcomeRecord{AnswerID: id, Truth: rawBool(t, false)})
	}
	writeCalLedger(t, dir, ans, outs)
	emitPath := filepath.Join(t.TempDir(), "events.ndjson")

	var stdout, stderr bytes.Buffer
	code := calibrationCmd([]string{"--ledger", dir, "--min-n", "10", "--max-ece", "0.1", "--emit", emitPath}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "skipped") {
		t.Errorf("expected stderr to say events were skipped, got %q", stderr.String())
	}
	events, err := event.ReadFile(emitPath)
	if err != nil {
		t.Fatalf("reading emit file: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected no events written without --agent-id, got %d", len(events))
	}
}

// @test:TestEmitWithAgentIDWritesTheCalibrationDriftEvent
func TestEmitWithAgentIDWritesTheCalibrationDriftEvent(t *testing.T) {
	dir := t.TempDir()
	var ans []ledger.AnswerRecord
	var outs []ledger.OutcomeRecord
	for i := 0; i < 40; i++ {
		id := "a" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		ans = append(ans, ledger.AnswerRecord{
			AnswerID: id, Template: "t", TemplateVersion: "v1", Type: "noul", Backend: "stub", Model: "m1",
			Probabilities: map[string]float64{"true": 0.99, "false": 0.01},
		})
		outs = append(outs, ledger.OutcomeRecord{AnswerID: id, Truth: rawBool(t, false)})
	}
	writeCalLedger(t, dir, ans, outs)
	emitPath := filepath.Join(t.TempDir(), "events.ndjson")

	var stdout, stderr bytes.Buffer
	code := calibrationCmd([]string{
		"--ledger", dir, "--min-n", "10", "--max-ece", "0.1",
		"--emit", emitPath, "--agent-id", "agent://acme.example/operator",
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d, stderr=%s", code, stderr.String())
	}
	events, err := event.ReadFile(emitPath)
	if err != nil {
		t.Fatalf("reading emit file: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 calibration_drift event, got %d", len(events))
	}
	e := events[0]
	if e.Type != "calibration_drift" || e.Severity != "high" || e.AgentID != "agent://acme.example/operator" {
		t.Errorf("unexpected event: %+v", e)
	}
}

// @test:TestEmitRefusesAMalformedAgentID
func TestEmitRefusesAMalformedAgentID(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := calibrationCmd([]string{
		"--ledger", dir, "--emit", filepath.Join(t.TempDir(), "events.ndjson"), "--agent-id", "not-an-agent-id",
	}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected exit 2 for a malformed --agent-id, got %d", code)
	}
	if !strings.Contains(stderr.String(), "agent://") {
		t.Errorf("expected the error to explain the required agent:// shape, got %q", stderr.String())
	}
}
