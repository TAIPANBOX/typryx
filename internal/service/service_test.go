package service_test

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/TAIPANBOX/typryx/internal/backend"
	"github.com/TAIPANBOX/typryx/internal/backend/backendtest"
	"github.com/TAIPANBOX/typryx/internal/ledger"
	"github.com/TAIPANBOX/typryx/internal/service"
	"github.com/TAIPANBOX/typryx/internal/template"
)

func choiceTemplate() template.Template {
	return template.Template{
		ID:           "request.complexity",
		Type:         template.TypeChoice,
		Instructions: "classify the request",
		Criteria:     json.RawMessage(`{"cheap":"d1","default":"d2","hard":"d3"}`),
		Fields:       []string{"prompt"},
	}
}

func noulTemplate(fields ...string) template.Template {
	return template.Template{
		ID:           "eval.outcome_met",
		Type:         template.TypeNoul,
		Instructions: "did it work",
		Fields:       fields,
	}
}

// @test:TestAChoiceQuestionIsAnsweredWithAProbabilityForEveryOption
func TestAChoiceQuestionIsAnsweredWithAProbabilityForEveryOption(t *testing.T) {
	d := newService(t, choiceTemplate(), backend.Stub{})
	result, refusal := d.Service.Ask(context.Background(),
		service.Caller{AgentID: "agent://acme.example/bot"},
		service.AskRequest{Template: "request.complexity", State: json.RawMessage(`{"prompt":"hi there"}`)})
	if refusal != nil {
		t.Fatalf("expected an answer, got a refusal: %+v", refusal)
	}
	if result.Unanswered {
		t.Fatalf("expected an answer, got unanswered: %s", result.Reason)
	}
	choice, ok := result.Answer.(string)
	if !ok || choice == "" {
		t.Fatalf("expected Answer to name one class, got %#v", result.Answer)
	}
	if len(result.Probabilities) != 3 {
		t.Fatalf("expected a probability for every one of 3 classes, got %d", len(result.Probabilities))
	}
	if _, ok := result.Probabilities[choice]; !ok {
		t.Errorf("the named class %q has no probability", choice)
	}
	sum := 0.0
	for _, p := range result.Probabilities {
		sum += p
	}
	if math.Abs(sum-1) > 1e-9 {
		t.Errorf("probabilities sum to %v, not 1", sum)
	}
	if result.Backend != "stub" {
		t.Errorf("expected backend stub, got %s", result.Backend)
	}
	if result.Model == "" {
		t.Error("expected a model version to be named")
	}
}

// @test:TestTheAnswerIsDerivedFromTheProbabilitiesNotTakenFromTheBackend
//
// A backend's own Choice/Score/Yes fields are advisory only and must never
// be trusted: the service derives the answer from the probability
// distribution itself, the same distribution it already validated. A
// backend that returns a nonsensical Choice, an out-of-range Score, or a Yes
// that disagrees with its own probabilities must never have that leak into
// the served answer.
func TestTheAnswerIsDerivedFromTheProbabilitiesNotTakenFromTheBackend(t *testing.T) {
	t.Run("choice", func(t *testing.T) {
		tb := &backendtest.Backend{Mode: backendtest.ModeOK, Answer: backend.Answer{
			Choice:        "banana", // not one of the template's options at all
			Probabilities: map[string]float64{"cheap": 0.1, "default": 0.2, "hard": 0.7},
			Model:         "test-0",
		}}
		d := newService(t, choiceTemplate(), tb)
		result, refusal := d.Service.Ask(context.Background(),
			service.Caller{AgentID: "a"},
			service.AskRequest{Template: "request.complexity", State: json.RawMessage(`{"prompt":"x"}`)})
		if refusal != nil {
			t.Fatalf("unexpected refusal: %+v", refusal)
		}
		if result.Answer != "hard" {
			t.Errorf("expected the derived answer 'hard' (the argmax of the probabilities), got %#v (the backend's claimed Choice was 'banana')", result.Answer)
		}
	})

	t.Run("score", func(t *testing.T) {
		tb := &backendtest.Backend{Mode: backendtest.ModeOK, Answer: backend.Answer{
			Score:         7, // out of range for a 4-level template
			Probabilities: map[string]float64{"0": 0.6, "1": 0.2, "2": 0.1, "3": 0.1},
			Model:         "test-0",
		}}
		tmpl := template.Template{ID: "s", Type: template.TypeScore, Instructions: "x",
			Criteria: json.RawMessage(`["l0","l1","l2","l3"]`), Fields: []string{"x"}}
		d := newService(t, tmpl, tb)
		result, refusal := d.Service.Ask(context.Background(),
			service.Caller{AgentID: "a"},
			service.AskRequest{Template: "s", State: json.RawMessage(`{"x":1}`)})
		if refusal != nil {
			t.Fatalf("unexpected refusal: %+v", refusal)
		}
		if result.Answer != 0 {
			t.Errorf("expected the derived answer 0 (the argmax index), got %#v (the backend's claimed Score was 7)", result.Answer)
		}
	})

	t.Run("noul", func(t *testing.T) {
		tb := &backendtest.Backend{Mode: backendtest.ModeOK, Answer: backend.Answer{
			Yes:           0.99, // disagrees with its own probabilities below
			Probabilities: map[string]float64{"true": 0.2, "false": 0.8},
			Model:         "test-0",
		}}
		d := newService(t, noulTemplate("task"), tb)
		result, refusal := d.Service.Ask(context.Background(),
			service.Caller{AgentID: "a"},
			service.AskRequest{Template: "eval.outcome_met", State: json.RawMessage(`{"task":"t"}`)})
		if refusal != nil {
			t.Fatalf("unexpected refusal: %+v", refusal)
		}
		if result.Answer != 0.2 {
			t.Errorf("expected the derived answer 0.2 (Probabilities[true]), got %#v (the backend's claimed Yes was 0.99)", result.Answer)
		}
	})
}

// @test:TestOnlyTheFieldsATemplateNamesReachTheBackend
func TestOnlyTheFieldsATemplateNamesReachTheBackend(t *testing.T) {
	tb := &backendtest.Backend{Mode: backendtest.ModeOK, Answer: backend.Answer{
		Yes: 0.5, Probabilities: map[string]float64{"true": 0.5, "false": 0.5}, Model: "test-0",
	}}
	d := newService(t, noulTemplate("task", "final_answer"), tb)
	result, refusal := d.Service.Ask(context.Background(),
		service.Caller{AgentID: "agent://acme.example/bot"},
		service.AskRequest{Template: "eval.outcome_met",
			State: json.RawMessage(`{"task":"t","final_answer":"a","secret1":1,"secret2":2,"secret3":3}`)})
	if refusal != nil {
		t.Fatalf("unexpected refusal: %+v", refusal)
	}
	if len(tb.Asked) != 1 {
		t.Fatalf("expected exactly one call to the backend, got %d", len(tb.Asked))
	}
	var kept map[string]json.RawMessage
	if err := json.Unmarshal(tb.Asked[0].Egress.Canonical(), &kept); err != nil {
		t.Fatalf("egress did not decode: %v", err)
	}
	if len(kept) != 2 {
		t.Fatalf("expected exactly 2 fields to reach the backend, got %d: %v", len(kept), kept)
	}
	for _, secret := range []string{"secret1", "secret2", "secret3"} {
		if _, ok := kept[secret]; ok {
			t.Errorf("field %q reached the backend and it was not named by the template", secret)
		}
	}
	if result.HeldBackFields != 3 {
		t.Errorf("expected the answer to say 3 fields were held back, got %d", result.HeldBackFields)
	}
}

// @test:TestAFailingBackendGivesUnansweredAndNeverAGuess
func TestAFailingBackendGivesUnansweredAndNeverAGuess(t *testing.T) {
	cases := []struct {
		name       string
		backend    backend.Backend
		wantReason string
		timeout    time.Duration
	}{
		{"a backend that errors", &backendtest.Backend{Mode: backendtest.ModeFail}, "backend_error", time.Second},
		{"a backend that hangs past the deadline", &backendtest.Backend{Mode: backendtest.ModeHang}, "timeout", 10 * time.Millisecond},
		{"a backend with no probabilities", &backendtest.Backend{Mode: backendtest.ModeEmpty}, "no_probabilities", time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := newService(t, noulTemplate("task"), c.backend)
			d.Service.Timeout = c.timeout
			result, refusal := d.Service.Ask(context.Background(),
				service.Caller{AgentID: "agent://acme.example/bot"},
				service.AskRequest{Template: "eval.outcome_met", State: json.RawMessage(`{"task":"t"}`)})
			if refusal != nil {
				t.Fatalf("expected an unanswered RESULT, not a refusal: %+v", refusal)
			}
			if !result.Unanswered {
				t.Fatal("expected Unanswered to be true")
			}
			if result.Reason != c.wantReason {
				t.Errorf("expected reason %q, got %q", c.wantReason, result.Reason)
			}
			if result.Probabilities != nil {
				t.Errorf("an unanswered result must carry no probabilities at all, got %v", result.Probabilities)
			}
			if result.Answer != nil {
				t.Errorf("an unanswered result must carry no answer at all, got %#v", result.Answer)
			}
			b, err := json.Marshal(result)
			if err != nil {
				t.Fatalf("marshaling the result: %v", err)
			}
			var m map[string]any
			json.Unmarshal(b, &m)
			if _, ok := m["probabilities"]; ok {
				t.Error("the JSON wire shape must omit probabilities entirely when unanswered")
			}
			if _, ok := m["answer"]; ok {
				t.Error("the JSON wire shape must omit answer entirely when unanswered")
			}
		})
	}
}

// @test:TestABackendsNamedReasonReachesTheCallerAndTheRecord
//
// A backend that fails with a *backend.UnansweredError carries its own,
// more specific reason (e.g. the openai-logprobs backend's
// label_mass_too_low) than the generic backend_error every other error
// gets; that reason must reach both the caller's Result and the journal's
// typed_unanswered event, unchanged.
func TestABackendsNamedReasonReachesTheCallerAndTheRecord(t *testing.T) {
	tb := &backendtest.Backend{Mode: backendtest.ModeFail, Err: &backend.UnansweredError{Reason: "label_mass_too_low"}}
	d := newService(t, noulTemplate("task"), tb)
	result, refusal := d.Service.Ask(context.Background(),
		service.Caller{AgentID: "agent://acme.example/bot"},
		service.AskRequest{Template: "eval.outcome_met", State: json.RawMessage(`{"task":"t"}`)})
	if refusal != nil {
		t.Fatalf("expected an unanswered result, not a refusal: %+v", refusal)
	}
	if !result.Unanswered || result.Reason != "label_mass_too_low" {
		t.Fatalf("expected reason %q, got unanswered=%v reason=%q", "label_mass_too_low", result.Unanswered, result.Reason)
	}
	events := readEvents(t, d.JournalPath)
	found := false
	for _, e := range events {
		if e.Type == "typed_unanswered" {
			found = true
			if e.Data["reason"] != "label_mass_too_low" {
				t.Errorf("expected the journal's reason to be label_mass_too_low, got %v", e.Data["reason"])
			}
		}
	}
	if !found {
		t.Fatal("expected one typed_unanswered event on the journal")
	}
}

// TestATimeoutStillWinsOverABackendsNamedReason: the backend/service
// precedence documented in internal/service.ask must not change just
// because a backend can now name its own reason: a deadline firing is still
// "timeout", even if the backend's own error happens to also be an
// UnansweredError (a backend racing its own deadline against ctx's could do
// this).
func TestATimeoutStillWinsOverABackendsNamedReason(t *testing.T) {
	tb := &backendtest.Backend{Mode: backendtest.ModeHang}
	d := newService(t, noulTemplate("task"), tb)
	d.Service.Timeout = 10 * time.Millisecond
	result, refusal := d.Service.Ask(context.Background(),
		service.Caller{AgentID: "agent://acme.example/bot"},
		service.AskRequest{Template: "eval.outcome_met", State: json.RawMessage(`{"task":"t"}`)})
	if refusal != nil {
		t.Fatalf("expected an unanswered result, not a refusal: %+v", refusal)
	}
	if result.Reason != "timeout" {
		t.Errorf("expected timeout to win regardless, got %q", result.Reason)
	}
}

// @test:TestACallerThatGoesAwayIsRecordedAsCanceledNotTimeout
//
// bctx (the backend's own deadline context) wraps the caller's ctx: if the
// CALLER disconnects, bctx.Err() reports context.Canceled just as surely as
// it would report context.DeadlineExceeded if only our own timeout fired.
// Collapsing both into "timeout" tells an operator the backend was slow,
// when the truth is the other end hung up; that is a different fact to log,
// alert on, or bill.
func TestACallerThatGoesAwayIsRecordedAsCanceledNotTimeout(t *testing.T) {
	tb := &backendtest.Backend{Mode: backendtest.ModeHang}
	d := newService(t, noulTemplate("task"), tb)
	d.Service.Timeout = time.Second // long enough that only the caller's own cancel fires first

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	result, refusal := d.Service.Ask(ctx, service.Caller{AgentID: "a"},
		service.AskRequest{Template: "eval.outcome_met", State: json.RawMessage(`{"task":"t"}`)})
	if refusal != nil {
		t.Fatalf("expected an unanswered RESULT, not a refusal: %+v", refusal)
	}
	if !result.Unanswered || result.Reason != "canceled" {
		t.Errorf("expected unanswered/canceled, got unanswered=%v reason=%q", result.Unanswered, result.Reason)
	}
}

// TestBadProbabilitiesAreUnansweredNotGuessed covers the shapes of a bad
// probability distribution that are not "empty": present, but wrong.
func TestBadProbabilitiesAreUnansweredNotGuessed(t *testing.T) {
	cases := []struct {
		name  string
		probs map[string]float64
	}{
		{"sums to 0.9, not 1", map[string]float64{"true": 0.4, "false": 0.5}},
		{"contains NaN", map[string]float64{"true": math.NaN(), "false": 0.5}},
		{"misses an option", map[string]float64{"true": 1.0}},
		{"a value over 1", map[string]float64{"true": 1.5, "false": -0.5}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb := &backendtest.Backend{Mode: backendtest.ModeOK, Answer: backend.Answer{Probabilities: c.probs, Model: "test-0"}}
			d := newService(t, noulTemplate("task"), tb)
			result, refusal := d.Service.Ask(context.Background(),
				service.Caller{AgentID: "agent://acme.example/bot"},
				service.AskRequest{Template: "eval.outcome_met", State: json.RawMessage(`{"task":"t"}`)})
			if refusal != nil {
				t.Fatalf("unexpected refusal: %+v", refusal)
			}
			if !result.Unanswered || result.Reason != "bad_probabilities" {
				t.Errorf("expected unanswered/bad_probabilities, got unanswered=%v reason=%q", result.Unanswered, result.Reason)
			}
		})
	}
}

// @test:TestAProbabilityForAnOptionTheTemplateDoesNotHaveIsRefused
//
// The expected key set (the template's option names, or "0".."n-1" for
// score, or {"true","false"} for noul) must match EXACTLY: an extra key a
// backend invented must not reach the caller, even when the keys the
// template actually names sum to a valid distribution on their own.
func TestAProbabilityForAnOptionTheTemplateDoesNotHaveIsRefused(t *testing.T) {
	tb := &backendtest.Backend{Mode: backendtest.ModeOK, Answer: backend.Answer{
		Probabilities: map[string]float64{"true": 0.5, "false": 0.5, "maybe": 0.3},
		Model:         "test-0",
	}}
	d := newService(t, noulTemplate("task"), tb)
	result, refusal := d.Service.Ask(context.Background(),
		service.Caller{AgentID: "a"},
		service.AskRequest{Template: "eval.outcome_met", State: json.RawMessage(`{"task":"t"}`)})
	if refusal != nil {
		t.Fatalf("unexpected refusal: %+v", refusal)
	}
	if !result.Unanswered || result.Reason != "bad_probabilities" {
		t.Errorf("expected unanswered/bad_probabilities for an extra key, got unanswered=%v reason=%q", result.Unanswered, result.Reason)
	}
}

// @test:TestTheHourlyCapRefusesTheCallAfterTheLimit
func TestTheHourlyCapRefusesTheCallAfterTheLimit(t *testing.T) {
	tb := &backendtest.Backend{Mode: backendtest.ModeOK, Answer: backend.Answer{
		Yes: 0.5, Probabilities: map[string]float64{"true": 0.5, "false": 0.5}, Model: "test-0",
	}}
	d := newService(t, noulTemplate("task"), tb)
	d.Service.Cap = service.NewCap(1)

	req := service.AskRequest{Template: "eval.outcome_met", State: json.RawMessage(`{"task":"t"}`)}
	caller := service.Caller{AgentID: "agent://acme.example/bot"}

	_, refusal := d.Service.Ask(context.Background(), caller, req)
	if refusal != nil {
		t.Fatalf("the first call, within the cap, must succeed: %+v", refusal)
	}
	_, refusal = d.Service.Ask(context.Background(), caller, req)
	if refusal == nil {
		t.Fatal("the second call, over the cap, must be refused")
	}
	if refusal.Code != "over_hourly_cap" || refusal.HTTPStatus != 429 {
		t.Errorf("expected over_hourly_cap/429, got %s/%d", refusal.Code, refusal.HTTPStatus)
	}
	if len(tb.Asked) != 1 {
		t.Errorf("the refused call must never reach the backend; backend was asked %d times", len(tb.Asked))
	}
}

// TestCapWindowRollsOverAfterAnHour uses an injected clock (via NewCap's
// internal now field is not exported, so this drives the cap through its
// public surface with a fixed limit and two real Asks separated by
// manipulating the Service's own clock is not available externally; this
// test instead proves the cap does NOT block a second call once enough of
// its window has been simulated by asking through a cap built with a small
// limit and confirming behaviour resets are covered by the exported
// constructor's documented contract). See internal cap_test.go for the
// white-box rollover test with an injected clock.
// TestRefusedCallsDoNotCountAgainstTheCap: a call refused before the cap step
// (an unknown template, here) must never consume the hourly budget. Spending
// the whole cap on calls that were refused anyway, before any backend was
// asked, would let an attacker exhaust a deployment's real capacity for free.
func TestRefusedCallsDoNotCountAgainstTheCap(t *testing.T) {
	tb := &backendtest.Backend{Mode: backendtest.ModeOK, Answer: backend.Answer{
		Yes: 0.5, Probabilities: map[string]float64{"true": 0.5, "false": 0.5}, Model: "test-0",
	}}
	d := newService(t, noulTemplate("task"), tb)
	d.Service.Cap = service.NewCap(1)
	caller := service.Caller{AgentID: "agent://acme.example/bot"}

	for i := 0; i < 5; i++ {
		_, refusal := d.Service.Ask(context.Background(), caller,
			service.AskRequest{Template: "no-such-template", State: json.RawMessage(`{}`)})
		if refusal == nil || refusal.Code != "unknown_template" {
			t.Fatalf("call %d: expected unknown_template, got %+v", i, refusal)
		}
	}
	// The cap's one slot must still be free: none of the refused calls above
	// should have spent it.
	_, refusal := d.Service.Ask(context.Background(), caller,
		service.AskRequest{Template: "eval.outcome_met", State: json.RawMessage(`{"task":"t"}`)})
	if refusal != nil {
		t.Fatalf("the cap's slot was consumed by calls that never reached it: %+v", refusal)
	}
}

func TestCapDisabledAtZeroNeverRefuses(t *testing.T) {
	tb := &backendtest.Backend{Mode: backendtest.ModeOK, Answer: backend.Answer{
		Yes: 0.5, Probabilities: map[string]float64{"true": 0.5, "false": 0.5}, Model: "test-0",
	}}
	d := newService(t, noulTemplate("task"), tb)
	d.Service.Cap = service.NewCap(0)
	req := service.AskRequest{Template: "eval.outcome_met", State: json.RawMessage(`{"task":"t"}`)}
	caller := service.Caller{AgentID: "agent://acme.example/bot"}
	for i := 0; i < 5; i++ {
		if _, refusal := d.Service.Ask(context.Background(), caller, req); refusal != nil {
			t.Fatalf("call %d: a cap of 0 must never refuse, got %+v", i, refusal)
		}
	}
}

// @test:TestEveryAnswerAndRefusalIsRecordedWithoutTheState
func TestEveryAnswerAndRefusalIsRecordedWithoutTheState(t *testing.T) {
	tb := &backendtest.Backend{Mode: backendtest.ModeOK, Answer: backend.Answer{
		Yes: 0.5, Probabilities: map[string]float64{"true": 0.5, "false": 0.5}, Model: "test-0",
	}}
	d := newService(t, noulTemplate("task"), tb)
	caller := service.Caller{AgentID: "agent://acme.example/bot"}

	secretState := `{"task":"the secret sauce recipe is XYZ"}`
	_, refusal := d.Service.Ask(context.Background(), caller,
		service.AskRequest{Template: "eval.outcome_met", State: json.RawMessage(secretState)})
	if refusal != nil {
		t.Fatalf("unexpected refusal: %+v", refusal)
	}
	_, refusal = d.Service.Ask(context.Background(), caller,
		service.AskRequest{Template: "unknown-template-id", State: json.RawMessage(`{}`)})
	if refusal == nil {
		t.Fatal("expected the second ask to be refused (unknown template)")
	}

	events := readEvents(t, d.JournalPath)
	if len(events) != 2 {
		t.Fatalf("expected 2 journal lines, got %d", len(events))
	}
	answerEvent, refusedEvent := events[0], events[1]
	if answerEvent.Type != "typed_answer" {
		t.Errorf("expected the first event to be typed_answer, got %s", answerEvent.Type)
	}
	if refusedEvent.Type != "typed_refused" {
		t.Errorf("expected the second event to be typed_refused, got %s", refusedEvent.Type)
	}
	for _, e := range events {
		if e.AgentID != "agent://acme.example/bot" {
			t.Errorf("event %s: expected the caller's agent id, got %q", e.Type, e.AgentID)
		}
		b, _ := json.Marshal(e.Data)
		if containsSecret(string(b)) {
			t.Errorf("event %s carries the raw state: %s", e.Type, b)
		}
	}
	if _, ok := answerEvent.Data["state_sha384"]; !ok {
		t.Error("the answer event should carry a hash of the egressed state")
	}
	if _, ok := answerEvent.Data["state"]; ok {
		t.Error("the answer event must never carry a field literally named state")
	}
}

func containsSecret(s string) bool {
	return len(s) > 0 && (jsonContains(s, "secret sauce") || jsonContains(s, "XYZ"))
}

func jsonContains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

// @test:TestAnEventWithNoAgentIsSkippedAndCounted
func TestAnEventWithNoAgentIsSkippedAndCounted(t *testing.T) {
	tb := &backendtest.Backend{Mode: backendtest.ModeOK, Answer: backend.Answer{
		Yes: 0.5, Probabilities: map[string]float64{"true": 0.5, "false": 0.5}, Model: "test-0",
	}}
	d := newService(t, noulTemplate("task"), tb)

	result, refusal := d.Service.Ask(context.Background(), service.Caller{AgentID: ""},
		service.AskRequest{Template: "eval.outcome_met", State: json.RawMessage(`{"task":"t"}`)})
	if refusal != nil {
		t.Fatalf("a credential naming nobody must still get an answer: %+v", refusal)
	}
	if result.Unanswered {
		t.Fatal("expected an answer, not unanswered")
	}
	skipped, _ := d.Service.Journal.Counts()
	if skipped != 1 {
		t.Errorf("expected 1 skipped-no-agent event, got %d", skipped)
	}
	events := readEvents(t, d.JournalPath)
	for _, e := range events {
		if e.AgentID == "" {
			t.Error("no event should ever be written with a blank or fabricated agent id")
		}
	}
	if len(events) != 0 {
		t.Errorf("expected zero events written (the only one was skipped), got %d", len(events))
	}
}

// @test:TestAnOutcomeIsScoredAgainstTheVersionItWasAskedUnder
func TestAnOutcomeIsScoredAgainstTheVersionItWasAskedUnder(t *testing.T) {
	tmplV1 := noulTemplate("task")
	tb := &backendtest.Backend{Mode: backendtest.ModeOK, Answer: backend.Answer{
		Yes: 0.5, Probabilities: map[string]float64{"true": 0.5, "false": 0.5}, Model: "test-0",
	}}
	d := newServiceWithLedger(t, tmplV1, tb)
	v1 := tmplV1.Version()

	result, refusal := d.Service.Ask(context.Background(), service.Caller{AgentID: "agent://acme.example/bot"},
		service.AskRequest{Template: "eval.outcome_met", State: json.RawMessage(`{"task":"t"}`)})
	if refusal != nil {
		t.Fatalf("unexpected refusal: %+v", refusal)
	}
	if result.TemplateVersion != v1 {
		t.Fatalf("expected the answer to carry version %s, got %s", v1, result.TemplateVersion)
	}

	// The template changes: same id, different instructions, so a different
	// registry now reports a different version. The service's own Templates
	// registry is REPLACED with one holding the new version, in place, so
	// that if Outcome ever consulted the live registry instead of the
	// ledger's own record of what the answer was given under, this test
	// would actually observe it report v2. Leaving the old registry in
	// place (as a first draft of this test did) would let that mistake
	// through silently, because both would then agree on v1 by accident.
	tmplV2 := tmplV1
	tmplV2.Instructions = "a completely different question now"
	if tmplV2.Version() == v1 {
		t.Fatal("test setup bug: the changed template must produce a different version")
	}
	d.Service.Templates = loadOneTemplate(t, tmplV2)

	outcome, refusal := d.Service.Outcome(service.Caller{AgentID: "agent://acme.example/bot"},
		service.OutcomeRequest{AnswerID: result.AnswerID, Truth: json.RawMessage(`true`), Source: "human"})
	if refusal != nil {
		t.Fatalf("unexpected refusal recording the outcome: %+v", refusal)
	}
	if outcome.TemplateVersion != v1 {
		t.Errorf("the outcome must be scored against %s (the version the answer was given under), got %s", v1, outcome.TemplateVersion)
	}
}

func TestOutcomeOnAnUnknownAnswerIsRefused(t *testing.T) {
	d := newServiceWithLedger(t, noulTemplate("task"), backend.Stub{})
	_, refusal := d.Service.Outcome(service.Caller{AgentID: "agent://acme.example/bot"},
		service.OutcomeRequest{AnswerID: "nope", Truth: json.RawMessage(`true`), Source: "human"})
	if refusal == nil || refusal.Code != "unknown_answer" || refusal.HTTPStatus != 404 {
		t.Fatalf("expected unknown_answer/404, got %+v", refusal)
	}
}

func TestOutcomeWithNoLedgerConfiguredRefuses(t *testing.T) {
	d := newService(t, noulTemplate("task"), backend.Stub{})
	_, refusal := d.Service.Outcome(service.Caller{AgentID: "agent://acme.example/bot"},
		service.OutcomeRequest{AnswerID: "a1", Truth: json.RawMessage(`true`), Source: "human"})
	if refusal == nil || refusal.Code != "no_ledger" || refusal.HTTPStatus != 503 {
		t.Fatalf("expected no_ledger/503, got %+v", refusal)
	}
}

func TestOutcomeValidatesTruthAgainstTheAnswerType(t *testing.T) {
	tb := &backendtest.Backend{Mode: backendtest.ModeOK, Answer: backend.Answer{
		Choice: "a", Probabilities: map[string]float64{"a": 0.6, "b": 0.4}, Model: "test-0",
	}}
	tmpl := template.Template{ID: "c", Type: template.TypeChoice, Instructions: "x",
		Criteria: json.RawMessage(`{"a":"d","b":"d"}`), Fields: []string{"x"}}
	d := newServiceWithLedger(t, tmpl, tb)
	result, refusal := d.Service.Ask(context.Background(), service.Caller{AgentID: "agent://acme.example/bot"},
		service.AskRequest{Template: "c", State: json.RawMessage(`{"x":1}`)})
	if refusal != nil {
		t.Fatalf("unexpected refusal: %+v", refusal)
	}
	_, refusal = d.Service.Outcome(service.Caller{AgentID: "agent://acme.example/bot"},
		service.OutcomeRequest{AnswerID: result.AnswerID, Truth: json.RawMessage(`"not-an-option"`), Source: "human"})
	if refusal == nil || refusal.Code != "bad_truth" || refusal.HTTPStatus != 400 {
		t.Fatalf("expected bad_truth/400 for an option that was never offered, got %+v", refusal)
	}
	_, refusal = d.Service.Outcome(service.Caller{AgentID: "agent://acme.example/bot"},
		service.OutcomeRequest{AnswerID: result.AnswerID, Truth: json.RawMessage(`"a"`), Source: "human"})
	if refusal != nil {
		t.Fatalf("a truth matching a real option should be accepted: %+v", refusal)
	}
}

// @test:TestAFreeFormQuestionIsRefusedUnlessSwitchedOn
func TestAFreeFormQuestionIsRefusedUnlessSwitchedOn(t *testing.T) {
	tb := &backendtest.Backend{Mode: backendtest.ModeOK, Answer: backend.Answer{
		Yes: 0.5, Probabilities: map[string]float64{"true": 0.5, "false": 0.5}, Model: "test-0",
	}}
	d := newService(t, noulTemplate("task"), tb)
	req := service.AskRequest{
		State: json.RawMessage(`{"anything":"goes"}`),
		Question: &service.FreeformQuestion{
			Type: "noul", Instructions: "is this true",
		},
	}
	result, refusal := d.Service.Ask(context.Background(), service.Caller{AgentID: "agent://acme.example/bot"}, req)
	if refusal == nil {
		t.Fatal("expected a refusal with freeform switched off")
	}
	if refusal.Code != "freeform_disabled" || refusal.HTTPStatus != 403 {
		t.Errorf("expected freeform_disabled/403, got %s/%d", refusal.Code, refusal.HTTPStatus)
	}
	if len(tb.Asked) != 0 {
		t.Error("nothing must leave the box for a refused freeform question")
	}
	if result.AnswerID != "" || result.Template != "" || result.Answer != nil {
		t.Errorf("a refusal should carry a zero Result, got %+v", result)
	}

	d.Service.AllowFreeform = true
	result, refusal = d.Service.Ask(context.Background(), service.Caller{AgentID: "agent://acme.example/bot"}, req)
	if refusal != nil {
		t.Fatalf("with freeform on, the same question should be answered: %+v", refusal)
	}
	if result.Unanswered {
		t.Fatalf("expected an answer, got unanswered: %s", result.Reason)
	}
	if result.Template != "freeform" {
		t.Errorf("expected the template id 'freeform', got %s", result.Template)
	}
	if len(tb.Asked) != 1 {
		t.Fatalf("expected exactly one backend call, got %d", len(tb.Asked))
	}
	var kept map[string]json.RawMessage
	json.Unmarshal(tb.Asked[0].Egress.Canonical(), &kept)
	if _, ok := kept["anything"]; !ok {
		t.Error("a freeform question sends the whole state; the field did not reach the backend")
	}
	if result.HeldBackFields != 0 {
		t.Errorf("a freeform question holds nothing back, got %d", result.HeldBackFields)
	}
}

func TestFreeformRejectsAnUnknownQuestionType(t *testing.T) {
	d := newService(t, noulTemplate("task"), backend.Stub{})
	d.Service.AllowFreeform = true
	_, refusal := d.Service.Ask(context.Background(), service.Caller{AgentID: "agent://acme.example/bot"},
		service.AskRequest{State: json.RawMessage(`{}`), Question: &service.FreeformQuestion{Type: "essay", Instructions: "x"}})
	if refusal == nil || refusal.Code != "bad_question" {
		t.Fatalf("expected bad_question for an unknown freeform type, got %+v", refusal)
	}
}

func TestUnknownTemplateIsRefused(t *testing.T) {
	d := newService(t, noulTemplate("task"), backend.Stub{})
	_, refusal := d.Service.Ask(context.Background(), service.Caller{AgentID: "a"},
		service.AskRequest{Template: "nope", State: json.RawMessage(`{}`)})
	if refusal == nil || refusal.Code != "unknown_template" || refusal.HTTPStatus != 404 {
		t.Fatalf("expected unknown_template/404, got %+v", refusal)
	}
}

func TestBadStateIsRefused(t *testing.T) {
	d := newService(t, noulTemplate("task"), backend.Stub{})
	_, refusal := d.Service.Ask(context.Background(), service.Caller{AgentID: "a"},
		service.AskRequest{Template: "eval.outcome_met", State: json.RawMessage(`[1,2,3]`)})
	if refusal == nil || refusal.Code != "bad_state" || refusal.HTTPStatus != 400 {
		t.Fatalf("expected bad_state/400, got %+v", refusal)
	}
}

// TestAServiceBuiltWithoutNewStillWorks exercises the fallback branches of
// clock() and newID(): a Service built as a plain struct literal (its
// unexported now/randomID seams are left nil) must still answer using the
// real clock and real randomness, not panic or misbehave.
func TestAServiceBuiltWithoutNewStillWorks(t *testing.T) {
	d := newService(t, noulTemplate("task"), backend.Stub{})
	bare := &service.Service{
		Templates: d.Service.Templates,
		Backend:   d.Service.Backend,
		Journal:   d.Service.Journal,
	}
	result, refusal := bare.Ask(context.Background(), service.Caller{AgentID: "a"},
		service.AskRequest{Template: "eval.outcome_met", State: json.RawMessage(`{"task":"t"}`)})
	if refusal != nil {
		t.Fatalf("unexpected refusal: %+v", refusal)
	}
	if result.AnswerID == "" {
		t.Error("expected a real random answer id even without New()")
	}
}

// TestANilCapNeverRefuses: a Service with no Cap set (nil) admits every call,
// which is what lets a deployment set TYPRYX_MAX_CALLS_PER_HOUR=0 (uncapped)
// without the service package itself needing a special case beyond "no cap
// configured".
func TestANilCapNeverRefuses(t *testing.T) {
	d := newService(t, noulTemplate("task"), backend.Stub{})
	d.Service.Cap = nil
	_, refusal := d.Service.Ask(context.Background(), service.Caller{AgentID: "a"},
		service.AskRequest{Template: "eval.outcome_met", State: json.RawMessage(`{"task":"t"}`)})
	if refusal != nil {
		t.Fatalf("a nil cap must never refuse: %+v", refusal)
	}
}

func TestFreeformBadStateIsRefused(t *testing.T) {
	d := newService(t, noulTemplate("task"), backend.Stub{})
	d.Service.AllowFreeform = true
	_, refusal := d.Service.Ask(context.Background(), service.Caller{AgentID: "a"},
		service.AskRequest{
			State:    json.RawMessage(`[1,2,3]`),
			Question: &service.FreeformQuestion{Type: "noul", Instructions: "is it true"},
		})
	if refusal == nil || refusal.Code != "bad_state" {
		t.Fatalf("expected bad_state for a non-object freeform state, got %+v", refusal)
	}
}

// @test:TestABadFreeformQuestionIsRefusedBeforeTheCapAndRecorded
//
// A freeform question with bad criteria or instructions must be refused
// BEFORE the hourly cap is taken (a caller sending malformed freeform
// questions must not be able to burn the deployment's real capacity for
// free) and the refusal must still reach the journal, exactly like any
// other refusal.
func TestABadFreeformQuestionIsRefusedBeforeTheCapAndRecorded(t *testing.T) {
	d := newService(t, noulTemplate("task"), backend.Stub{})
	d.Service.AllowFreeform = true
	d.Service.Cap = service.NewCap(1)
	caller := service.Caller{AgentID: "a"}

	_, refusal := d.Service.Ask(context.Background(), caller,
		service.AskRequest{
			State: json.RawMessage(`{}`),
			Question: &service.FreeformQuestion{
				Type: "choice", Instructions: "pick one", Criteria: json.RawMessage(`["not", "an", "object"]`),
			},
		})
	if refusal == nil || refusal.Code != "bad_question" || refusal.HTTPStatus != 400 {
		t.Fatalf("expected bad_question/400, got %+v", refusal)
	}

	// The cap's one slot must still be free: the bad question above must
	// never have reached cap.take().
	_, refusal = d.Service.Ask(context.Background(), caller,
		service.AskRequest{Template: "eval.outcome_met", State: json.RawMessage(`{"task":"t"}`)})
	if refusal != nil {
		t.Fatalf("the cap's slot was consumed by a bad freeform question that never reached it: %+v", refusal)
	}

	events := readEvents(t, d.JournalPath)
	found := false
	for _, e := range events {
		if e.Type == "typed_refused" && e.Data["reason"] == "bad_question" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a typed_refused event with reason bad_question, got %+v", events)
	}
}

func TestFreeformWithMalformedChoiceCriteriaIsRefused(t *testing.T) {
	d := newService(t, noulTemplate("task"), backend.Stub{})
	d.Service.AllowFreeform = true
	_, refusal := d.Service.Ask(context.Background(), service.Caller{AgentID: "a"},
		service.AskRequest{
			State: json.RawMessage(`{}`),
			Question: &service.FreeformQuestion{
				Type: "choice", Instructions: "pick one", Criteria: json.RawMessage(`["not", "an", "object"]`),
			},
		})
	if refusal == nil || refusal.Code != "bad_question" {
		t.Fatalf("expected bad_question for malformed freeform choice criteria, got %+v", refusal)
	}
}

func TestFreeformWithMalformedScoreCriteriaIsRefused(t *testing.T) {
	d := newService(t, noulTemplate("task"), backend.Stub{})
	d.Service.AllowFreeform = true
	_, refusal := d.Service.Ask(context.Background(), service.Caller{AgentID: "a"},
		service.AskRequest{
			State: json.RawMessage(`{}`),
			Question: &service.FreeformQuestion{
				Type: "score", Instructions: "rate it", Criteria: json.RawMessage(`{"not":"an array"}`),
			},
		})
	if refusal == nil || refusal.Code != "bad_question" {
		t.Fatalf("expected bad_question for malformed freeform score criteria, got %+v", refusal)
	}
}

func TestOutcomeSurfacesALedgerWriteFailure(t *testing.T) {
	tb := &backendtest.Backend{Mode: backendtest.ModeOK, Answer: backend.Answer{
		Yes: 0.5, Probabilities: map[string]float64{"true": 0.5, "false": 0.5}, Model: "test-0",
	}}
	d := newServiceWithLedger(t, noulTemplate("task"), tb)
	result, refusal := d.Service.Ask(context.Background(), service.Caller{AgentID: "a"},
		service.AskRequest{Template: "eval.outcome_met", State: json.RawMessage(`{"task":"t"}`)})
	if refusal != nil {
		t.Fatalf("unexpected refusal: %+v", refusal)
	}
	// Close the ledger's files out from under it: the in-memory index still
	// answers GetAnswer, but the outcomes file can no longer be written to,
	// which is the ledger_write_failed path.
	d.Ledger.Close()
	_, refusal = d.Service.Outcome(service.Caller{AgentID: "a"},
		service.OutcomeRequest{AnswerID: result.AnswerID, Truth: json.RawMessage(`true`), Source: "human"})
	if refusal == nil || refusal.Code != "ledger_write_failed" || refusal.HTTPStatus != 500 {
		t.Fatalf("expected ledger_write_failed/500 once the ledger file is closed, got %+v", refusal)
	}
}

// @test:TestASecondOutcomeForTheSameAnswerIsRefused
//
// A truth is counted once. A second POST /v1/outcome for an answer that
// already has one must be refused, and the refusal must hold even after the
// ledger has been closed and reopened (the index that remembers "this answer
// already has an outcome" has to survive a restart, not just live in memory
// for the current process).
func TestASecondOutcomeForTheSameAnswerIsRefused(t *testing.T) {
	tb := &backendtest.Backend{Mode: backendtest.ModeOK, Answer: backend.Answer{
		Probabilities: map[string]float64{"true": 0.5, "false": 0.5}, Model: "test-0",
	}}
	ledgerDir := t.TempDir()
	led, err := ledger.Open(ledgerDir)
	if err != nil {
		t.Fatalf("ledger.Open: %v", err)
	}
	d := newService(t, noulTemplate("task"), tb)
	d.Service.Ledger = led

	result, refusal := d.Service.Ask(context.Background(), service.Caller{AgentID: "a"},
		service.AskRequest{Template: "eval.outcome_met", State: json.RawMessage(`{"task":"t"}`)})
	if refusal != nil {
		t.Fatalf("unexpected refusal: %+v", refusal)
	}

	_, refusal = d.Service.Outcome(service.Caller{AgentID: "a"},
		service.OutcomeRequest{AnswerID: result.AnswerID, Truth: json.RawMessage(`true`), Source: "human"})
	if refusal != nil {
		t.Fatalf("the first outcome should be accepted: %+v", refusal)
	}

	_, refusal = d.Service.Outcome(service.Caller{AgentID: "a"},
		service.OutcomeRequest{AnswerID: result.AnswerID, Truth: json.RawMessage(`false`), Source: "human"})
	if refusal == nil || refusal.Code != "outcome_exists" || refusal.HTTPStatus != 409 {
		t.Fatalf("expected outcome_exists/409 for a second outcome, got %+v", refusal)
	}

	// Across a restart: close and reopen the ledger, rebuild the service on
	// top of the reopened one, and confirm a second outcome is still refused.
	if err := led.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	led2, err := ledger.Open(ledgerDir)
	if err != nil {
		t.Fatalf("reopening the ledger: %v", err)
	}
	defer led2.Close()
	d.Service.Ledger = led2
	_, refusal = d.Service.Outcome(service.Caller{AgentID: "a"},
		service.OutcomeRequest{AnswerID: result.AnswerID, Truth: json.RawMessage(`false`), Source: "human"})
	if refusal == nil || refusal.Code != "outcome_exists" {
		t.Fatalf("expected outcome_exists to survive a restart, got %+v", refusal)
	}
}

func TestOutcomeRejectsAScoreTruthOfTheWrongTypeOrOutOfRange(t *testing.T) {
	tb := &backendtest.Backend{Mode: backendtest.ModeOK, Answer: backend.Answer{
		Score: 1, Probabilities: map[string]float64{"0": 0.5, "1": 0.5}, Model: "test-0",
	}}
	tmpl := template.Template{ID: "s", Type: template.TypeScore, Instructions: "x",
		Criteria: json.RawMessage(`["l0","l1"]`), Fields: []string{"x"}}
	d := newServiceWithLedger(t, tmpl, tb)
	result, refusal := d.Service.Ask(context.Background(), service.Caller{AgentID: "a"},
		service.AskRequest{Template: "s", State: json.RawMessage(`{"x":1}`)})
	if refusal != nil {
		t.Fatalf("unexpected refusal: %+v", refusal)
	}
	for _, truth := range []string{`"not a number"`, `5`, `-1`} {
		_, refusal = d.Service.Outcome(service.Caller{AgentID: "a"},
			service.OutcomeRequest{AnswerID: result.AnswerID, Truth: json.RawMessage(truth), Source: "human"})
		if refusal == nil || refusal.Code != "bad_truth" {
			t.Errorf("truth %s: expected bad_truth, got %+v", truth, refusal)
		}
	}
	_, refusal = d.Service.Outcome(service.Caller{AgentID: "a"},
		service.OutcomeRequest{AnswerID: result.AnswerID, Truth: json.RawMessage(`1`), Source: "human"})
	if refusal != nil {
		t.Errorf("a truth within range should be accepted: %+v", refusal)
	}
}

func TestOutcomeRejectsANoulTruthOfTheWrongType(t *testing.T) {
	tb := &backendtest.Backend{Mode: backendtest.ModeOK, Answer: backend.Answer{
		Yes: 0.5, Probabilities: map[string]float64{"true": 0.5, "false": 0.5}, Model: "test-0",
	}}
	d := newServiceWithLedger(t, noulTemplate("task"), tb)
	result, refusal := d.Service.Ask(context.Background(), service.Caller{AgentID: "a"},
		service.AskRequest{Template: "eval.outcome_met", State: json.RawMessage(`{"task":"t"}`)})
	if refusal != nil {
		t.Fatalf("unexpected refusal: %+v", refusal)
	}
	_, refusal = d.Service.Outcome(service.Caller{AgentID: "a"},
		service.OutcomeRequest{AnswerID: result.AnswerID, Truth: json.RawMessage(`"yes"`), Source: "human"})
	if refusal == nil || refusal.Code != "bad_truth" {
		t.Fatalf("expected bad_truth for a non-boolean noul truth, got %+v", refusal)
	}
}

func TestStateOverTheBoundIsRefusedWith413(t *testing.T) {
	tmpl := noulTemplate("task")
	tmpl.MaxStateBytes = 10
	d := newService(t, tmpl, backend.Stub{})
	_, refusal := d.Service.Ask(context.Background(), service.Caller{AgentID: "a"},
		service.AskRequest{Template: "eval.outcome_met", State: json.RawMessage(`{"task":"way more than ten bytes"}`)})
	if refusal == nil || refusal.Code != "state_too_large" || refusal.HTTPStatus != 413 {
		t.Fatalf("expected state_too_large/413, got %+v", refusal)
	}
}

// @test:TestAnAnsweredAskLedgersItsProbabilities
//
// Phase E (calibration) reads answers.ndjson to compute a Brier score and a
// reliability diagram, and it can only do that if the ledger carries the
// full probability distribution and the served answer, not just enough to
// validate a later truth. Before this test, ledger.AnswerRecord's
// Probabilities and Answer fields existed (a compiling stub) but nothing in
// this package populated them; run against that state, this test failed
// because both came back empty on a real, answered ask.
func TestAnAnsweredAskLedgersItsProbabilities(t *testing.T) {
	tb := &backendtest.Backend{Mode: backendtest.ModeOK, Answer: backend.Answer{
		Probabilities: map[string]float64{"true": 0.7, "false": 0.3}, Model: "test-0",
	}}
	d := newServiceWithLedger(t, noulTemplate("task"), tb)
	result, refusal := d.Service.Ask(context.Background(), service.Caller{AgentID: "a"},
		service.AskRequest{Template: "eval.outcome_met", State: json.RawMessage(`{"task":"t"}`)})
	if refusal != nil {
		t.Fatalf("unexpected refusal: %+v", refusal)
	}
	rec, ok := d.Ledger.GetAnswer(result.AnswerID)
	if !ok {
		t.Fatalf("expected the answer to be on the ledger")
	}
	if len(rec.Probabilities) != 2 || rec.Probabilities["true"] != 0.7 || rec.Probabilities["false"] != 0.3 {
		t.Errorf("expected the ledgered record to carry the full probability distribution, got %#v", rec.Probabilities)
	}
	// For a noul question, the served Answer IS probabilities["true"] (see
	// Result's doc comment and deriveAnswer), not a boolean: the ledgered
	// copy must match that, not some derived yes/no.
	var answer float64
	if err := json.Unmarshal(rec.Answer, &answer); err != nil {
		t.Fatalf("expected rec.Answer to be a JSON-encoded copy of the served answer, got %s: %v", rec.Answer, err)
	}
	if answer != result.Answer {
		t.Errorf("expected the ledgered answer %v to match the served answer %v", answer, result.Answer)
	}
	if answer != 0.7 {
		t.Errorf("expected the served noul answer to be probabilities[true]=0.7, got %v", answer)
	}
}

// @test:TestNoulCriteriaGivenReflectsWhetherTheTemplateSetAny
//
// The jev backend must send noul criteria only when a template actually
// named any, and omit the field entirely otherwise (the wire shape treats
// "no criteria" and "criteria of empty strings" as different facts). Before
// this test, questionFor discarded template.NoulCriteria's own ok result
// (assigned to _), so backend.Question had no way to tell the two apart; run
// against that code, this test failed because NoulCriteriaGiven was always
// false, including for a template that set explicit criteria.
func TestNoulCriteriaGivenReflectsWhetherTheTemplateSetAny(t *testing.T) {
	t.Run("no criteria at all", func(t *testing.T) {
		tb := &backendtest.Backend{Mode: backendtest.ModeOK, Answer: backend.Answer{
			Probabilities: map[string]float64{"true": 0.5, "false": 0.5}, Model: "test-0",
		}}
		d := newService(t, noulTemplate("task"), tb)
		_, refusal := d.Service.Ask(context.Background(), service.Caller{AgentID: "a"},
			service.AskRequest{Template: "eval.outcome_met", State: json.RawMessage(`{"task":"t"}`)})
		if refusal != nil {
			t.Fatalf("unexpected refusal: %+v", refusal)
		}
		if len(tb.Asked) != 1 {
			t.Fatalf("expected 1 call, got %d", len(tb.Asked))
		}
		if tb.Asked[0].Question.NoulCriteriaGiven {
			t.Error("expected NoulCriteriaGiven false for a template with no criteria")
		}
	})

	t.Run("explicit criteria", func(t *testing.T) {
		tmpl := template.Template{
			ID: "eval.outcome_met", Type: template.TypeNoul, Instructions: "did it work",
			Criteria: json.RawMessage(`{"true":"yes it did","false":"no it did not"}`),
			Fields:   []string{"task"},
		}
		tb := &backendtest.Backend{Mode: backendtest.ModeOK, Answer: backend.Answer{
			Probabilities: map[string]float64{"true": 0.5, "false": 0.5}, Model: "test-0",
		}}
		d := newService(t, tmpl, tb)
		_, refusal := d.Service.Ask(context.Background(), service.Caller{AgentID: "a"},
			service.AskRequest{Template: "eval.outcome_met", State: json.RawMessage(`{"task":"t"}`)})
		if refusal != nil {
			t.Fatalf("unexpected refusal: %+v", refusal)
		}
		if len(tb.Asked) != 1 {
			t.Fatalf("expected 1 call, got %d", len(tb.Asked))
		}
		q := tb.Asked[0].Question
		if !q.NoulCriteriaGiven {
			t.Error("expected NoulCriteriaGiven true for a template with explicit criteria")
		}
		if q.NoulTrueDesc != "yes it did" || q.NoulFalseDesc != "no it did not" {
			t.Errorf("unexpected descriptions: %q / %q", q.NoulTrueDesc, q.NoulFalseDesc)
		}
	})
}
