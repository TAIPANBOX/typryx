package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TAIPANBOX/typryx/internal/traininglog"
)

// A credential bound to something that is not an agent:// identity would
// have that value written as agent_id on every event it produces, so the
// service refuses to start, naming the identity and never the credential.
func TestLoadConfigRefusesACredentialBoundToAMalformedIdentity(t *testing.T) {
	for _, id := range []string{"not-an-agent", "agent://", "http://h/a"} {
		t.Run(id, func(t *testing.T) {
			clearTyprxEnv(t)
			setEnv(t, map[string]string{
				"TYPRYX_BACKEND":   "stub",
				"TYPRYX_TEMPLATES": validTemplatesDirForTest(t),
				"TYPRYX_KEYS":      "good-secret=agent://h/a,bad-secret-value=" + id,
			})
			_, err := loadConfig()
			var cfg *configError
			if !errors.As(err, &cfg) {
				t.Fatalf("expected a configError, got %v", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, "TYPRYX_KEYS") || !strings.Contains(msg, `"`+id+`"`) {
				t.Errorf("the refusal must name the variable and the identity: %s", msg)
			}
			if strings.Contains(msg, "bad-secret-value") || strings.Contains(msg, "good-secret") {
				t.Errorf("the refusal must never print a credential: %s", msg)
			}
		})
	}

	// A well-formed binding is accepted.
	clearTyprxEnv(t)
	setEnv(t, map[string]string{
		"TYPRYX_BACKEND": "stub", "TYPRYX_TEMPLATES": validTemplatesDirForTest(t),
		"TYPRYX_KEYS": "k1=agent://h/a",
	})
	if _, err := loadConfig(); err != nil {
		t.Fatalf("a well-formed identity was refused: %v", err)
	}
}

func TestLoadConfigRefusesAMalformedPriceNamingTheVariable(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		v    string
	}{
		{"jev output", jevWorkingEnv(t, ""), "TYPRYX_JEV_PRICE_PER_MTOK_OUTPUT"},
		{"openai input", openAIWorkingEnv(t, ""), "TYPRYX_OPENAI_PRICE_PER_MTOK_INPUT"},
		{"openai output", openAIWorkingEnv(t, ""), "TYPRYX_OPENAI_PRICE_PER_MTOK_OUTPUT"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clearTyprxEnv(t)
			c.env[c.v] = "1.5usd"
			setEnv(t, c.env)
			_, err := loadConfig()
			var cfg *configError
			if !errors.As(err, &cfg) || !strings.Contains(err.Error(), c.v) {
				t.Fatalf("expected a configError naming %s, got %v", c.v, err)
			}
		})
	}
}

func TestConnectRefusesAnUnknownFlag(t *testing.T) {
	var so, se bytes.Buffer
	if code := connectCmd([]string{"claude-code", "--nope"}, &so, &se); code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
	if so.Len() != 0 || !strings.Contains(se.String(), "flag provided but not defined: -nope") {
		t.Errorf("stdout %q, stderr %q", so.String(), se.String())
	}
}

func TestExportOutThatCannotBeOpenedIsAUsageError(t *testing.T) {
	clearTyprxEnv(t)
	tdir, ldir := writeExportFixture(t)
	out := filepath.Join(t.TempDir(), "missing-dir", "rows.jsonl")
	var so, se bytes.Buffer
	if code := exportCmd([]string{"--training", "--training-dir", tdir, "--ledger", ldir, "--out", out}, &so, &se); code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
	if !strings.Contains(se.String(), "typryx export: opening --out:") {
		t.Errorf("stderr %q", se.String())
	}
	if _, err := os.Stat(filepath.Dir(out)); !os.IsNotExist(err) {
		t.Errorf("the export must not create directories for --out: %v", err)
	}
}

func TestPrintExportCountsSaysWhenThereIsNothing(t *testing.T) {
	var b bytes.Buffer
	printExportCounts(&b, traininglog.ExportReport{MalformedLines: 2, TornLines: 1, OrphanTruth: 3})
	want := "no training lines\nmalformed_lines=2 torn_lines=1 orphan_truth=3\n"
	if b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
}
