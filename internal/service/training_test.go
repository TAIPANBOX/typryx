package service_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/typryx/internal/backend"
	"github.com/TAIPANBOX/typryx/internal/backend/backendtest"
	"github.com/TAIPANBOX/typryx/internal/service"
	"github.com/TAIPANBOX/typryx/internal/template"
	"github.com/TAIPANBOX/typryx/internal/traininglog"
)

// trainingService is a service with a training log on and a backend whose
// distribution is distinctive enough to find in a file: a probability of
// 0.7123 can be grepped for, and cannot be there by coincidence.
func trainingService(t *testing.T, tmpl template.Template, tb backend.Backend) (*testDeps, string) {
	t.Helper()
	d := newService(t, tmpl, tb)
	dir := filepath.Join(t.TempDir(), "training")
	log, err := traininglog.Open(dir)
	if err != nil {
		t.Fatalf("traininglog.Open: %v", err)
	}
	t.Cleanup(func() { log.Close() })
	d.Service.Training = log
	return d, filepath.Join(dir, traininglog.FileName)
}

func okBackend() *backendtest.Backend {
	return &backendtest.Backend{Mode: backendtest.ModeOK, Answer: backend.Answer{
		Probabilities: map[string]float64{"true": 0.7123, "false": 0.2877}, Model: "test-0",
	}}
}

func trainingLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the training log: %v", err)
	}
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// @test:TestAnAnsweredAskIsWrittenToTheTrainingLog
func TestAnAnsweredAskIsWrittenToTheTrainingLog(t *testing.T) {
	d, path := trainingService(t, noulTemplate("task"), okBackend())
	before := time.Now().UTC().Add(-time.Second)
	res, refusal := d.Service.Ask(context.Background(), service.Caller{AgentID: "agent://a.example/b"},
		service.AskRequest{Template: "eval.outcome_met", State: json.RawMessage(`{"task":"t"}`)})
	if refusal != nil || res.Unanswered {
		t.Fatalf("expected an answer, got %+v / %+v", res, refusal)
	}
	lines := trainingLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("expected one training line for one answered ask, got %d", len(lines))
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("the line is not JSON: %v", err)
	}
	str := func(k string) string {
		var s string
		if err := json.Unmarshal(got[k], &s); err != nil {
			t.Fatalf("%s is not a string: %s", k, got[k])
		}
		return s
	}
	if str("answer_id") != res.AnswerID || str("template") != "eval.outcome_met" ||
		str("template_version") != res.TemplateVersion || str("type") != "noul" ||
		str("backend") != "backendtest" || str("model") != "test-0" {
		t.Errorf("line does not describe the answer it was written for: %s (result %+v)", lines[0], res)
	}
	at, err := time.Parse(time.RFC3339Nano, str("answered_at"))
	if err != nil || at.Before(before) {
		t.Errorf("answered_at %q is not a recent RFC3339 time (%v)", str("answered_at"), err)
	}
	if string(got["state"]) != `{"task":"t"}` {
		t.Errorf("state = %s", got["state"])
	}
}

// @test:TestOnlyEgressedFieldsReachTheTrainingLog
//
// The state below carries a field the template does not name. It is held
// back from the backend (invariant 2) and must be held back from disk too:
// the training log is the egress filter's second reader, not a way around it.
func TestOnlyEgressedFieldsReachTheTrainingLog(t *testing.T) {
	tb := okBackend()
	d, path := trainingService(t, noulTemplate("task"), tb)
	res, refusal := d.Service.Ask(context.Background(), service.Caller{AgentID: "agent://a.example/b"},
		service.AskRequest{Template: "eval.outcome_met",
			State: json.RawMessage(`{"task":"t","user_email":"held-back-SENTINEL@example.test","note":"also-held-back-SENTINEL"}`)})
	if refusal != nil || res.Unanswered {
		t.Fatalf("expected an answer, got %+v / %+v", res, refusal)
	}
	if res.HeldBackFields != 2 {
		t.Fatalf("the fixture was meant to hold back 2 fields, held back %d", res.HeldBackFields)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "SENTINEL") || strings.Contains(string(raw), "user_email") {
		t.Fatalf("a field the template does not name reached training.ndjson: %s", raw)
	}
	var line struct {
		State map[string]json.RawMessage `json:"state"`
	}
	if err := json.Unmarshal(raw, &line); err != nil {
		t.Fatal(err)
	}
	if len(line.State) != 1 || string(line.State["task"]) != `"t"` {
		t.Errorf("the state on disk should be exactly the one named field, got %v", line.State)
	}
	// What the backend was handed and what reached disk are the same bytes.
	if got, want := string(tb.Asked[0].Egress.Canonical()), string(mustState(t, raw)); got != want {
		t.Errorf("the training state %s differs from what the backend was handed, %s", want, got)
	}
}

func mustState(t *testing.T, line []byte) json.RawMessage {
	t.Helper()
	var l struct {
		State json.RawMessage `json:"state"`
	}
	if err := json.Unmarshal(line, &l); err != nil {
		t.Fatal(err)
	}
	return l.State
}

// @test:TestTheTrainingLogNeverCarriesABackendAnswerOrProbabilities
func TestTheTrainingLogNeverCarriesABackendAnswerOrProbabilities(t *testing.T) {
	cases := []struct {
		name string
		tmpl template.Template
		tb   *backendtest.Backend
		req  service.AskRequest
	}{
		{"noul", noulTemplate("task"), okBackend(),
			service.AskRequest{Template: "eval.outcome_met", State: json.RawMessage(`{"task":"t"}`)}},
		{"choice", choiceTemplate(), &backendtest.Backend{Mode: backendtest.ModeOK, Answer: backend.Answer{
			Probabilities: map[string]float64{"cheap": 0.1111, "default": 0.2222, "hard": 0.6667}, Model: "test-0"}},
			service.AskRequest{Template: "request.complexity", State: json.RawMessage(`{"prompt":"p"}`)}},
	}
	want := []string{"answer_id", "answered_at", "backend", "model", "state", "template", "template_version", "type"}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, path := trainingService(t, c.tmpl, c.tb)
			res, refusal := d.Service.Ask(context.Background(), service.Caller{AgentID: "agent://a.example/b"}, c.req)
			if refusal != nil || res.Unanswered {
				t.Fatalf("expected an answer, got %+v / %+v", res, refusal)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var keys map[string]json.RawMessage
			if err := json.Unmarshal(raw, &keys); err != nil {
				t.Fatal(err)
			}
			var got []string
			for k := range keys {
				got = append(got, k)
			}
			sort.Strings(got)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("keys on disk = %v, want exactly %v", got, want)
			}
			for _, v := range c.tb.Answer.Probabilities {
				if s := strings.TrimRight(strings.TrimRight(formatFloat(v), "0"), "."); strings.Contains(string(raw), s) {
					t.Errorf("the probability %s appears in the training line: %s", s, raw)
				}
			}
			if strings.Contains(string(raw), "probabilit") {
				t.Errorf("the word probabilities appears in the training line: %s", raw)
			}
			if res.Answer != nil {
				if b, err := json.Marshal(res.Answer); err == nil && strings.Contains(string(raw), `"answer":`+string(b)) {
					t.Errorf("the served answer is in the training line: %s", raw)
				}
			}
		})
	}
}

func formatFloat(v float64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// @test:TestUnansweredAndRefusedAsksAreNotWrittenToTheTrainingLog
func TestUnansweredAndRefusedAsksAreNotWrittenToTheTrainingLog(t *testing.T) {
	good := json.RawMessage(`{"task":"t"}`)
	type tc struct {
		name string
		tb   backend.Backend
		req  service.AskRequest
		prep func(*service.Service)
	}
	cases := []tc{
		{"backend error", &backendtest.Backend{Mode: backendtest.ModeFail}, service.AskRequest{Template: "eval.outcome_met", State: good}, nil},
		{"no probabilities", &backendtest.Backend{Mode: backendtest.ModeEmpty}, service.AskRequest{Template: "eval.outcome_met", State: good}, nil},
		{"bad probabilities", &backendtest.Backend{Mode: backendtest.ModeOK, Answer: backend.Answer{
			Probabilities: map[string]float64{"true": 0.9, "false": 0.9}, Model: "m"}},
			service.AskRequest{Template: "eval.outcome_met", State: good}, nil},
		{"unknown template", okBackend(), service.AskRequest{Template: "nope", State: good}, nil},
		{"state is not an object", okBackend(), service.AskRequest{Template: "eval.outcome_met", State: json.RawMessage(`[1]`)}, nil},
		{"freeform switched off", okBackend(), service.AskRequest{State: good,
			Question: &service.FreeformQuestion{Type: "noul", Instructions: "is it"}}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, path := trainingService(t, noulTemplate("task"), c.tb)
			if c.prep != nil {
				c.prep(d.Service)
			}
			_, _ = d.Service.Ask(context.Background(), service.Caller{AgentID: "agent://a.example/b"}, c.req)
			if lines := trainingLines(t, path); len(lines) != 0 {
				t.Errorf("a %s ask wrote %d training line(s): %v", c.name, len(lines), lines)
			}
		})
	}
}

func TestACapRefusedAskIsNotWrittenToTheTrainingLog(t *testing.T) {
	d, path := trainingService(t, noulTemplate("task"), okBackend())
	d.Service.Cap = service.NewCap(1)
	req := service.AskRequest{Template: "eval.outcome_met", State: json.RawMessage(`{"task":"t"}`)}
	if _, r := d.Service.Ask(context.Background(), service.Caller{AgentID: "agent://a.example/b"}, req); r != nil {
		t.Fatalf("the first ask, inside the cap, was refused: %+v", r)
	}
	if _, r := d.Service.Ask(context.Background(), service.Caller{AgentID: "agent://a.example/b"}, req); r == nil || r.Code != "over_hourly_cap" {
		t.Fatalf("expected the second ask to be refused over the cap, got %+v", r)
	}
	if lines := trainingLines(t, path); len(lines) != 1 {
		t.Errorf("expected exactly the one answered ask in the log, got %d lines", len(lines))
	}
}

// @test:TestAFreeformAskIsNotWrittenToTheTrainingLog
//
// A freeform question has no template, so no `fields` allowlist and no
// version to train a model against: its egress is the whole state, sent
// because an operator switched freeform on knowingly. The training log is
// for templated questions only.
func TestAFreeformAskIsNotWrittenToTheTrainingLog(t *testing.T) {
	d, path := trainingService(t, noulTemplate("task"), okBackend())
	d.Service.AllowFreeform = true
	res, refusal := d.Service.Ask(context.Background(), service.Caller{AgentID: "agent://a.example/b"},
		service.AskRequest{State: json.RawMessage(`{"anything":"whole-state-SENTINEL"}`),
			Question: &service.FreeformQuestion{Type: "noul", Instructions: "is it fine"}})
	if refusal != nil || res.Unanswered {
		t.Fatalf("expected the freeform ask to be answered, got %+v / %+v", res, refusal)
	}
	raw, _ := os.ReadFile(path)
	if len(raw) != 0 {
		t.Errorf("a freeform ask reached the training log: %s", raw)
	}
}

// @test:TestATrainingLogWriteFailureNeverTurnsAnAnswerIntoARefusal
func TestATrainingLogWriteFailureNeverTurnsAnAnswerIntoARefusal(t *testing.T) {
	d, _ := trainingService(t, noulTemplate("task"), okBackend())
	if err := d.Service.Training.Close(); err != nil {
		t.Fatal(err)
	}
	res, refusal := d.Service.Ask(context.Background(), service.Caller{AgentID: "agent://a.example/b"},
		service.AskRequest{Template: "eval.outcome_met", State: json.RawMessage(`{"task":"t"}`)})
	if refusal != nil || res.Unanswered {
		t.Fatalf("a training log that cannot be written must not change the answer, got %+v / %+v", res, refusal)
	}
	if got := d.Service.TrainingFailures(); got != 1 {
		t.Errorf("TrainingFailures = %d, want 1: a lost training line must be counted, not dropped silently", got)
	}
}

func TestWithNoTrainingLogAnAnswerIsServedAndNothingIsCounted(t *testing.T) {
	d := newService(t, noulTemplate("task"), okBackend())
	if d.Service.Training != nil {
		t.Fatal("a service built without a training log has one")
	}
	res, refusal := d.Service.Ask(context.Background(), service.Caller{AgentID: "agent://a.example/b"},
		service.AskRequest{Template: "eval.outcome_met", State: json.RawMessage(`{"task":"t"}`)})
	if refusal != nil || res.Unanswered {
		t.Fatalf("got %+v / %+v", res, refusal)
	}
	if d.Service.TrainingFailures() != 0 {
		t.Error("TrainingFailures moved with no training log configured")
	}
}
