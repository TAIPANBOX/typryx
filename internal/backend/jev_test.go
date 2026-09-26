package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TAIPANBOX/typryx/internal/template"
)

// --- fixtures ---------------------------------------------------------------

func newJevBackend(baseURL, apiKey string) *Jev {
	return NewJev(JevConfig{BaseURL: baseURL, Model: "jev-latest", APIKey: apiKey})
}

// jevServer is an httptest server that can answer with a fixed sequence of
// (status, body, headers), recording every request it received.
type jevServer struct {
	*httptest.Server
	responses   []jevResponseFixture
	requests    atomic.Int64
	times       []time.Time
	lastBody    []byte
	lastAuth    string
	lastHeaders http.Header
}

type jevResponseFixture struct {
	Status int
	Header map[string]string
	Body   []byte
}

func newJevServer(t *testing.T, responses ...jevResponseFixture) *jevServer {
	t.Helper()
	js := &jevServer{responses: responses}
	js.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		js.times = append(js.times, time.Now())
		n := js.requests.Add(1)
		js.lastAuth = r.Header.Get("Authorization")
		js.lastHeaders = r.Header.Clone()
		b, _ := readAll(r)
		js.lastBody = b
		if r.URL.Path != "/systemone" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		idx := int(n - 1)
		if idx >= len(js.responses) {
			idx = len(js.responses) - 1
		}
		resp := js.responses[idx]
		for k, v := range resp.Header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(resp.Status)
		_, _ = w.Write(resp.Body)
	}))
	t.Cleanup(js.Close)
	return js
}

func readAll(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return buf, nil
}

func okJevBody(t *testing.T, model string, answer map[string]any, input, output int) []byte {
	t.Helper()
	body := map[string]any{
		"model":   model,
		"answers": map[string]any{"q": answer},
		"usage":   map[string]any{"input_tokens": input, "output_tokens": output},
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshaling fake jev response: %v", err)
	}
	return b
}

func choiceQ() Question {
	return Question{Key: "q", Type: template.TypeChoice, Instructions: "classify it",
		Options: []Option{{Name: "billing", Description: "billing things"}, {Name: "sales", Description: ""}, {Name: "technical", Description: "tech things"}}}
}

func scoreQ() Question {
	return Question{Key: "q", Type: template.TypeScore, Instructions: "rate it",
		Levels: []string{"calm", "frustrated", "angry"}}
}

func noulQ(given bool, trueDesc, falseDesc string) Question {
	return Question{Key: "q", Type: template.TypeNoul, Instructions: "is it urgent",
		NoulCriteriaGiven: given, NoulTrueDesc: trueDesc, NoulFalseDesc: falseDesc}
}

// @test:TestJevNeverForwardsRunIDOrAgentID
//
// jev.go's own wire types (jevRequest, jevQuestionWire) carry no run_id or
// agent_id field at all, so this backend cannot leak either one even by
// omission; this test proves it behaviourally too, since a wire-shape
// argument only proves what CAN'T happen through the shape as written, not
// that nobody ever wires a header up beside it. Question.RunID/AgentID are
// identity metadata every backend receives (internal/service.ask sets them
// unconditionally); jev is the one backend that reads neither.
func TestJevNeverForwardsRunIDOrAgentID(t *testing.T) {
	body := okJevBody(t, "m", map[string]any{"type": "noul", "noul": 0.5}, 10, 10)
	srv := newJevServer(t, jevResponseFixture{Status: 200, Body: body})
	o := newJevBackend(srv.URL, "k")
	q := noulQ(false, "", "")
	q.RunID = "a-run-id-that-must-never-appear"
	q.AgentID = "agent://acme.example/must-never-appear"
	if _, _, err := o.Ask(context.Background(), q, template.Egress{}); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if strings.Contains(string(srv.lastBody), "must-never-appear") {
		t.Errorf("the run id or agent id leaked into the request body: %s", srv.lastBody)
	}
	for _, h := range []string{"X-Fuse-Run-Id", "X-Fuse-Agent-Id", "Run-Id", "Agent-Id"} {
		if v := srv.lastHeaders.Get(h); v != "" {
			t.Errorf("expected no %s header from the jev backend, got %q", h, v)
		}
	}
}

// --- TestTheDocumentedExampleResponseMapsToOurKeys --------------------------

// The documented example response answers three DIFFERENT questions
// (department, frustration, is_urgent) in one payload; jev.Ask always asks
// exactly one question named "q", so each of the three is replayed as its
// own single-question response with the id rewritten to "q".
func TestTheDocumentedExampleResponseMapsToOurKeys(t *testing.T) {
	raw, err := os.ReadFile("testdata/jev_example_response.json")
	if err != nil {
		t.Fatalf("reading the pinned example: %v", err)
	}
	var doc struct {
		Model   string                     `json:"model"`
		Answers map[string]json.RawMessage `json:"answers"`
		Usage   json.RawMessage            `json:"usage"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing the pinned example: %v", err)
	}

	rewrite := func(t *testing.T, id string) []byte {
		t.Helper()
		answer, ok := doc.Answers[id]
		if !ok {
			t.Fatalf("the pinned example has no answer %q", id)
		}
		body := map[string]json.RawMessage{
			"model":   json.RawMessage(`"` + doc.Model + `"`),
			"answers": json.RawMessage(`{"q":` + string(answer) + `}`),
			"usage":   doc.Usage,
		}
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("re-marshaling: %v", err)
		}
		return b
	}

	t.Run("choice", func(t *testing.T) {
		srv := newJevServer(t, jevResponseFixture{Status: 200, Body: rewrite(t, "department")})
		o := newJevBackend(srv.URL, "k")
		ans, _, err := o.Ask(context.Background(), choiceQ(), template.Egress{})
		if err != nil {
			t.Fatalf("Ask: %v", err)
		}
		want := map[string]float64{"technical": 0.85, "sales": 0.0, "billing": 0.15}
		if !probsEqual(ans.Probabilities, want) {
			t.Errorf("choice probabilities = %#v, want %#v", ans.Probabilities, want)
		}
		if ans.Model != "jev-1.13.0" {
			t.Errorf("expected the served model version, got %q", ans.Model)
		}
	})

	t.Run("score", func(t *testing.T) {
		srv := newJevServer(t, jevResponseFixture{Status: 200, Body: rewrite(t, "frustration")})
		o := newJevBackend(srv.URL, "k")
		ans, _, err := o.Ask(context.Background(), scoreQ(), template.Egress{})
		if err != nil {
			t.Fatalf("Ask: %v", err)
		}
		want := map[string]float64{"0": 0.0, "1": 1.0, "2": 0.0}
		if !probsEqual(ans.Probabilities, want) {
			t.Errorf("score probabilities = %#v, want %#v", ans.Probabilities, want)
		}
	})

	t.Run("noul", func(t *testing.T) {
		srv := newJevServer(t, jevResponseFixture{Status: 200, Body: rewrite(t, "is_urgent")})
		o := newJevBackend(srv.URL, "k")
		ans, _, err := o.Ask(context.Background(), noulQ(false, "", ""), template.Egress{})
		if err != nil {
			t.Fatalf("Ask: %v", err)
		}
		want := map[string]float64{"true": 1.0, "false": 0.0}
		if !probsEqual(ans.Probabilities, want) {
			t.Errorf("noul probabilities = %#v, want %#v", ans.Probabilities, want)
		}
	})
}

func probsEqual(got, want map[string]float64) bool {
	if len(got) != len(want) {
		return false
	}
	for k, v := range want {
		gv, ok := got[k]
		if !ok || math.Abs(gv-v) > 1e-9 {
			return false
		}
	}
	return true
}

// --- TestTheRequestCarriesTheStateAsAnObjectAndOneQuestion ------------------

func TestTheRequestCarriesTheStateAsAnObjectAndOneQuestion(t *testing.T) {
	okBody := okJevBody(t, "jev-1.13.0", map[string]any{"type": "choice", "choice": "billing",
		"probabilities": map[string]any{"billing": 1.0, "sales": 0.0, "technical": 0.0}}, 10, 5)

	t.Run("choice: null description for an empty one", func(t *testing.T) {
		srv := newJevServer(t, jevResponseFixture{Status: 200, Body: okBody})
		o := newJevBackend(srv.URL, "k")
		eg := egressOf(t, []string{"x"}, `{"x":1,"y":2}`)
		if _, _, err := o.Ask(context.Background(), choiceQ(), eg); err != nil {
			t.Fatalf("Ask: %v", err)
		}
		var req struct {
			State     map[string]any `json:"state"`
			Model     string         `json:"model"`
			Questions map[string]struct {
				Type         string         `json:"type"`
				Instructions string         `json:"instructions"`
				Criteria     map[string]any `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.Unmarshal(srv.lastBody, &req); err != nil {
			t.Fatalf("parsing the request typryx sent: %v\n%s", err, srv.lastBody)
		}
		if req.Model != "jev-latest" {
			t.Errorf("model = %q, want jev-latest", req.Model)
		}
		if len(req.State) != 1 || req.State["x"] != float64(1) {
			t.Errorf("state = %#v, want an object with only {x:1} (the egress, not the raw state)", req.State)
		}
		q, ok := req.Questions["q"]
		if !ok {
			t.Fatalf("expected exactly one question named %q, got %#v", "q", req.Questions)
		}
		if len(req.Questions) != 1 {
			t.Errorf("expected exactly 1 question, got %d", len(req.Questions))
		}
		if q.Type != "choice" || q.Instructions != "classify it" {
			t.Errorf("unexpected question shape: %#v", q)
		}
		if desc, ok := q.Criteria["sales"]; !ok || desc != nil {
			t.Errorf("expected criteria.sales to be present and null (empty description), got %#v (present=%v)", desc, ok)
		}
		if q.Criteria["billing"] != "billing things" {
			t.Errorf("expected criteria.billing = %q, got %#v", "billing things", q.Criteria["billing"])
		}
	})

	t.Run("score: criteria is the level array", func(t *testing.T) {
		srv := newJevServer(t, jevResponseFixture{Status: 200, Body: okJevBody(t, "m",
			map[string]any{"type": "score", "score": 0.0, "probabilities": map[string]any{"0": 1.0, "1": 0.0, "2": 0.0}}, 1, 1)})
		o := newJevBackend(srv.URL, "k")
		if _, _, err := o.Ask(context.Background(), scoreQ(), template.Egress{}); err != nil {
			t.Fatalf("Ask: %v", err)
		}
		var req struct {
			Questions map[string]struct {
				Criteria []string `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.Unmarshal(srv.lastBody, &req); err != nil {
			t.Fatalf("parsing request: %v", err)
		}
		want := []string{"calm", "frustrated", "angry"}
		got := req.Questions["q"].Criteria
		if len(got) != len(want) {
			t.Fatalf("criteria = %#v, want %#v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("criteria[%d] = %q, want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("noul: no criteria field when the template gave none", func(t *testing.T) {
		srv := newJevServer(t, jevResponseFixture{Status: 200, Body: okJevBody(t, "m",
			map[string]any{"type": "noul", "noul": 0.5}, 1, 1)})
		o := newJevBackend(srv.URL, "k")
		if _, _, err := o.Ask(context.Background(), noulQ(false, "", ""), template.Egress{}); err != nil {
			t.Fatalf("Ask: %v", err)
		}
		var req map[string]any
		if err := json.Unmarshal(srv.lastBody, &req); err != nil {
			t.Fatalf("parsing request: %v", err)
		}
		questions := req["questions"].(map[string]any)
		q := questions["q"].(map[string]any)
		if _, present := q["criteria"]; present {
			t.Errorf("expected no criteria field at all, got %#v", q["criteria"])
		}
	})

	t.Run("noul: criteria present when the template gave it", func(t *testing.T) {
		srv := newJevServer(t, jevResponseFixture{Status: 200, Body: okJevBody(t, "m",
			map[string]any{"type": "noul", "noul": 0.5}, 1, 1)})
		o := newJevBackend(srv.URL, "k")
		if _, _, err := o.Ask(context.Background(), noulQ(true, "yes", "no"), template.Egress{}); err != nil {
			t.Fatalf("Ask: %v", err)
		}
		var req struct {
			Questions map[string]struct {
				Criteria map[string]string `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.Unmarshal(srv.lastBody, &req); err != nil {
			t.Fatalf("parsing request: %v", err)
		}
		got := req.Questions["q"].Criteria
		if got["true"] != "yes" || got["false"] != "no" {
			t.Errorf("criteria = %#v, want {true:yes false:no}", got)
		}
	})
}

// --- TestConfidenceIsNeverRead -----------------------------------------------

func TestConfidenceIsNeverRead(t *testing.T) {
	body := okJevBody(t, "m", map[string]any{
		"type": "choice", "choice": "a", "confidence": 0.01,
		"probabilities": map[string]any{"a": 0.9, "b": 0.1},
	}, 1, 1)
	srv := newJevServer(t, jevResponseFixture{Status: 200, Body: body})
	o := newJevBackend(srv.URL, "k")
	q := Question{Key: "q", Type: template.TypeChoice, Instructions: "x",
		Options: []Option{{Name: "a"}, {Name: "b"}}}
	ans, _, err := o.Ask(context.Background(), q, template.Egress{})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	want := map[string]float64{"a": 0.9, "b": 0.1}
	if !probsEqual(ans.Probabilities, want) {
		t.Errorf("expected the untouched probabilities map despite a much lower confidence, got %#v", ans.Probabilities)
	}
}

// --- TestANoulOutsideZeroOneIsUnanswered -------------------------------------

// A finite but out-of-range noul (-0.1, 1.1) is the case answerFrom's own
// range check exists for: it is valid JSON, so it reaches that check, and
// must come back bad_noul rather than being clamped or trusted.
func TestANoulOutsideZeroOneIsUnanswered(t *testing.T) {
	for _, n := range []float64{-0.1, 1.1} {
		t.Run(fmt.Sprintf("%v", n), func(t *testing.T) {
			body := okJevBody(t, "m", map[string]any{"type": "noul", "noul": n}, 1, 1)
			srv := newJevServer(t, jevResponseFixture{Status: 200, Body: body})
			o := newJevBackend(srv.URL, "k")
			_, _, err := o.Ask(context.Background(), noulQ(false, "", ""), template.Egress{})
			var ue *UnansweredError
			if !asErr(err, &ue) || ue.Reason != "bad_noul" {
				t.Fatalf("expected UnansweredError{bad_noul}, got %v", err)
			}
		})
	}
}

// TestANoulThatIsNotEvenAFiniteNumberFailsToParseRatherThanReachingTheRangeCheck
// documents a fact rather than exercising a branch: NaN and Infinity are not
// valid JSON tokens at all, so encoding/json refuses the whole response
// before answerFrom's finite check ever runs. That check exists anyway,
// as defense in depth against a future decode path (or a different JSON
// library) that is more permissive than the standard library's.
func TestANoulThatIsNotEvenAFiniteNumberFailsToParseRatherThanReachingTheRangeCheck(t *testing.T) {
	for _, lit := range []string{"NaN", "Infinity", "-Infinity"} {
		t.Run(lit, func(t *testing.T) {
			body := []byte(strings.Replace(string(okJevBody(t, "m", map[string]any{"type": "noul", "noul": 0}, 1, 1)),
				`"noul":0`, `"noul":`+lit, 1))
			srv := newJevServer(t, jevResponseFixture{Status: 200, Body: body})
			o := newJevBackend(srv.URL, "k")
			_, _, err := o.Ask(context.Background(), noulQ(false, "", ""), template.Egress{})
			var ue *UnansweredError
			if !asErr(err, &ue) || ue.Reason != "no_probabilities" {
				t.Fatalf("expected UnansweredError{no_probabilities} (a decode failure, not bad_noul), got %v", err)
			}
		})
	}
}

// --- TestAMissingAnswerIsUnanswered ------------------------------------------

func TestAMissingAnswerIsUnanswered(t *testing.T) {
	t.Run("no such answer id", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"model": "m", "answers": map[string]any{}, "usage": map[string]any{}})
		srv := newJevServer(t, jevResponseFixture{Status: 200, Body: body})
		o := newJevBackend(srv.URL, "k")
		_, _, err := o.Ask(context.Background(), noulQ(false, "", ""), template.Egress{})
		var ue *UnansweredError
		if !asErr(err, &ue) || ue.Reason != "no_probabilities" {
			t.Fatalf("expected UnansweredError{no_probabilities}, got %v", err)
		}
	})

	t.Run("wrong type", func(t *testing.T) {
		body := okJevBody(t, "m", map[string]any{"type": "choice", "choice": "a", "probabilities": map[string]any{"a": 1.0}}, 1, 1)
		srv := newJevServer(t, jevResponseFixture{Status: 200, Body: body})
		o := newJevBackend(srv.URL, "k")
		_, _, err := o.Ask(context.Background(), noulQ(false, "", ""), template.Egress{})
		var ue *UnansweredError
		if !asErr(err, &ue) || ue.Reason != "no_probabilities" {
			t.Fatalf("expected UnansweredError{no_probabilities} for a type mismatch, got %v", err)
		}
	})

	t.Run("no probabilities for choice", func(t *testing.T) {
		body := okJevBody(t, "m", map[string]any{"type": "choice", "choice": "a"}, 1, 1)
		srv := newJevServer(t, jevResponseFixture{Status: 200, Body: body})
		o := newJevBackend(srv.URL, "k")
		_, _, err := o.Ask(context.Background(), choiceQ(), template.Egress{})
		var ue *UnansweredError
		if !asErr(err, &ue) || ue.Reason != "no_probabilities" {
			t.Fatalf("expected UnansweredError{no_probabilities}, got %v", err)
		}
	})
}

func asErr(err error, target **UnansweredError) bool {
	ue, ok := err.(*UnansweredError)
	if !ok {
		return false
	}
	*target = ue
	return true
}

// --- retries: 429/529, backoff, Retry-After, deadlines -----------------------

func TestRateLimitIsRetriedWithBackoffAndThenSucceeds(t *testing.T) {
	okBody := okJevBody(t, "m", map[string]any{"type": "noul", "noul": 1.0}, 1, 1)
	srv := newJevServer(t,
		jevResponseFixture{Status: 429, Body: []byte(`{}`)},
		jevResponseFixture{Status: 529, Body: []byte(`{}`)},
		jevResponseFixture{Status: 200, Body: okBody},
	)
	o := newJevBackend(srv.URL, "k")
	start := time.Now()
	ans, _, err := o.Ask(context.Background(), noulQ(false, "", ""), template.Egress{})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("expected eventual success, got %v", err)
	}
	if ans.Probabilities["true"] != 1.0 {
		t.Errorf("unexpected answer: %#v", ans.Probabilities)
	}
	if srv.requests.Load() != 3 {
		t.Fatalf("expected 3 requests (2 retries), got %d", srv.requests.Load())
	}
	if len(srv.times) != 3 {
		t.Fatalf("expected 3 recorded request times, got %d", len(srv.times))
	}
	gap1 := srv.times[1].Sub(srv.times[0])
	gap2 := srv.times[2].Sub(srv.times[1])
	if gap1 < 190*time.Millisecond {
		t.Errorf("first retry gap = %v, want at least ~200ms", gap1)
	}
	if gap2 < 390*time.Millisecond {
		t.Errorf("second retry gap = %v, want at least ~400ms", gap2)
	}
	if elapsed < 590*time.Millisecond {
		t.Errorf("total elapsed = %v, want at least ~600ms (200ms + 400ms backoff)", elapsed)
	}
}

func TestRetryAfterIsHonouredButCapped(t *testing.T) {
	if got := retryDelay(1, "100"); got != 2*time.Second {
		t.Errorf("retryDelay(1, %q) = %v, want the 2s cap", "100", got)
	}
	if got := retryDelay(1, "0"); got != 0 {
		t.Errorf("retryDelay(1, %q) = %v, want 0 (Retry-After: 0 means retry immediately)", "0", got)
	}
	if got := retryDelay(2, "not-a-number"); got != 400*time.Millisecond {
		t.Errorf("retryDelay(2, %q) (malformed header, ignored) = %v, want the plain exponential 400ms", "not-a-number", got)
	}
	if got := retryDelay(1, ""); got != 200*time.Millisecond {
		t.Errorf("retryDelay(1, \"\") = %v, want 200ms", got)
	}
	if got := retryDelay(2, ""); got != 400*time.Millisecond {
		t.Errorf("retryDelay(2, \"\") = %v, want 400ms", got)
	}

	// And end to end: a server-sent Retry-After of 100s must not make the
	// test actually wait 100s (or even the 2s cap over and over); a short
	// context deadline proves the cap is real without a slow test.
	srv := newJevServer(t, jevResponseFixture{Status: 429, Header: map[string]string{"Retry-After": "100"}, Body: []byte(`{}`)})
	o := newJevBackend(srv.URL, "k")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := o.Ask(ctx, noulQ(false, "", ""), template.Egress{})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected an error: the deadline is far shorter than the (capped) retry delay")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Ask took %v; the retry-after cap of 2s (let alone the 100s the server asked for) must not be waited out", elapsed)
	}
}

func TestNoRetryOn401Or422(t *testing.T) {
	for _, status := range []int{401, 422} {
		t.Run(fmt.Sprintf("%d", status), func(t *testing.T) {
			srv := newJevServer(t, jevResponseFixture{Status: status, Body: []byte(`{"error":"nope"}`)})
			o := newJevBackend(srv.URL, "k")
			_, _, err := o.Ask(context.Background(), noulQ(false, "", ""), template.Egress{})
			if err == nil {
				t.Fatal("expected an error")
			}
			if srv.requests.Load() != 1 {
				t.Errorf("expected exactly 1 request (no retry on %d), got %d", status, srv.requests.Load())
			}
			var ue *UnansweredError
			if asErr(err, &ue) {
				t.Errorf("a %d is a generic backend_error, not a named UnansweredError reason: %v", status, ue)
			}
		})
	}
}

func TestARetryNeverCrossesTheDeadline(t *testing.T) {
	srv := newJevServer(t, jevResponseFixture{Status: 529, Body: []byte(`{}`)})
	o := newJevBackend(srv.URL, "k")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := o.Ask(ctx, noulQ(false, "", ""), template.Egress{})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected an error")
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Ask took %v past a 50ms deadline; a retry must never be made once it would cross the deadline", elapsed)
	}
}

// --- key handling, redirects, model naming -----------------------------------

func TestTheJevKeyIsSentAsBearerAndNeverLogged(t *testing.T) {
	const secretKey = "fake-test-jev-key-must-never-leak-1a2b3c"
	okBody := okJevBody(t, "m", map[string]any{"type": "noul", "noul": 0.5}, 1, 1)
	srv := newJevServer(t, jevResponseFixture{Status: 200, Body: okBody})
	o := newJevBackend(srv.URL, secretKey)
	if _, _, err := o.Ask(context.Background(), noulQ(false, "", ""), template.Egress{}); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if srv.lastAuth != "Bearer "+secretKey {
		t.Errorf("Authorization header = %q, want Bearer %s", srv.lastAuth, secretKey)
	}

	errSrv := newJevServer(t, jevResponseFixture{Status: 422, Body: []byte("validation failed, key=" + secretKey)})
	o2 := newJevBackend(errSrv.URL, secretKey)
	_, _, err := o2.Ask(context.Background(), noulQ(false, "", ""), template.Egress{})
	if err == nil {
		t.Fatal("expected an error for a 422 response")
	}
	if strings.Contains(err.Error(), secretKey) {
		t.Errorf("the key leaked into the returned error: %v", err)
	}
}

func TestARedirectIsNotFollowedByJev(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Location", "/somewhere-else")
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	o := newJevBackend(srv.URL, "k")
	_, _, err := o.Ask(context.Background(), noulQ(false, "", ""), template.Egress{})
	if err == nil {
		t.Fatal("expected an error for a 3xx response")
	}
	if hits.Load() != 1 {
		t.Errorf("expected exactly 1 request (no redirect followed), got %d", hits.Load())
	}
}

func TestTheRecordedModelIsTheServedVersion(t *testing.T) {
	t.Run("model present in the response", func(t *testing.T) {
		body := okJevBody(t, "jev-1.13.0", map[string]any{"type": "noul", "noul": 0.5}, 1, 1)
		srv := newJevServer(t, jevResponseFixture{Status: 200, Body: body})
		o := newJevBackend(srv.URL, "k")
		ans, _, err := o.Ask(context.Background(), noulQ(false, "", ""), template.Egress{})
		if err != nil {
			t.Fatalf("Ask: %v", err)
		}
		if ans.Model != "jev-1.13.0" {
			t.Errorf("Model = %q, want the served version jev-1.13.0, not the configured jev-latest", ans.Model)
		}
	})

	t.Run("model absent falls back to the configured name", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"answers": map[string]any{"q": map[string]any{"type": "noul", "noul": 0.5}},
			"usage":   map[string]any{"input_tokens": 1, "output_tokens": 1},
		})
		srv := newJevServer(t, jevResponseFixture{Status: 200, Body: body})
		o := newJevBackend(srv.URL, "k")
		ans, _, err := o.Ask(context.Background(), noulQ(false, "", ""), template.Egress{})
		if err != nil {
			t.Fatalf("Ask: %v", err)
		}
		if ans.Model != "jev-latest" {
			t.Errorf("Model = %q, want the configured fallback jev-latest", ans.Model)
		}
	})
}

// --- cost ---------------------------------------------------------------------

func TestCostIsInputTokensTimesTheConfiguredPrice(t *testing.T) {
	body := okJevBody(t, "m", map[string]any{"type": "noul", "noul": 0.5}, 2_000_000, 1_000_000)
	srv := newJevServer(t, jevResponseFixture{Status: 200, Body: body})
	o := NewJev(JevConfig{BaseURL: srv.URL, Model: "jev-latest", APIKey: "k",
		PriceInputPerMTok: 0.042, PriceOutputPerMTok: 0.5})
	_, usage, err := o.Ask(context.Background(), noulQ(false, "", ""), template.Egress{})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	want := 2.0*0.042 + 1.0*0.5
	if math.Abs(usage.CostUSD-want) > 1e-9 {
		t.Errorf("CostUSD = %v, want %v", usage.CostUSD, want)
	}
	if usage.InputTokens != 2_000_000 || usage.OutputTokens != 1_000_000 {
		t.Errorf("unexpected token usage: %+v", usage)
	}
}

func TestCostIsZeroWhenNoPriceIsConfigured(t *testing.T) {
	body := okJevBody(t, "m", map[string]any{"type": "noul", "noul": 0.5}, 500, 500)
	srv := newJevServer(t, jevResponseFixture{Status: 200, Body: body})
	o := newJevBackend(srv.URL, "k")
	_, usage, err := o.Ask(context.Background(), noulQ(false, "", ""), template.Egress{})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if usage.CostUSD != 0 {
		t.Errorf("CostUSD = %v, want 0 when unpriced", usage.CostUSD)
	}
}

// --- Name, small surface -----------------------------------------------------

func TestNewJevDefaultsTheModelWhenUnset(t *testing.T) {
	o := NewJev(JevConfig{BaseURL: "http://x/v1", APIKey: "k"})
	if o.cfg.Model != "jev-latest" {
		t.Errorf("expected the default model jev-latest, got %q", o.cfg.Model)
	}
}

func TestJevName(t *testing.T) {
	o := NewJev(JevConfig{BaseURL: "http://x/v1", Model: "m", APIKey: "k"})
	if o.Name() != "jev" {
		t.Errorf("expected jev, got %s", o.Name())
	}
}

func TestJevRetryableErrorMessageNamesTheStatus(t *testing.T) {
	err := &jevRetryableError{status: 529}
	if err.Error() != "jev: server responded with status 529" {
		t.Errorf("unexpected message: %q", err.Error())
	}
}

// TestAnUnknownQuestionTypeIsRefused is defensive: internal/service never
// builds a Question with a type outside choice/score/noul (template.Validate
// refuses anything else at load time), so this exercises jevQuestionWireFor's
// own guard directly rather than waiting for a real caller to trigger it.
func TestAnUnknownQuestionTypeIsRefused(t *testing.T) {
	q := Question{Key: "q", Type: template.Type("mystery"), Instructions: "x"}
	srv := newJevServer(t, jevResponseFixture{Status: 200, Body: []byte(`{}`)})
	o := newJevBackend(srv.URL, "k")
	_, _, err := o.Ask(context.Background(), q, template.Egress{})
	if err == nil {
		t.Fatal("expected an error for an unknown question type")
	}
	if srv.requests.Load() != 0 {
		t.Errorf("expected no request to be sent for a type this backend cannot wire, got %d", srv.requests.Load())
	}
}

// --- hostile sweep ------------------------------------------------------------

func TestJevBackendSurvivesHostileResponses(t *testing.T) {
	questions := []Question{choiceQ(), scoreQ(), noulQ(true, "yes", "no")}
	for seed := 0; seed < 200; seed++ {
		seed := seed
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			r := rand.New(rand.NewSource(int64(seed)))
			q := questions[seed%len(questions)]
			body := hostileJevBody(r, q.Type)
			srv := newJevServer(t, jevResponseFixture{Status: 200, Body: body})
			o := newJevBackend(srv.URL, "k")
			eg := egressOf(t, []string{"x"}, `{"x":1}`)

			var ans Answer
			var err error
			func() {
				defer func() {
					if p := recover(); p != nil {
						t.Fatalf("Ask panicked on seed %d: %v", seed, p)
					}
				}()
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				ans, _, err = o.Ask(ctx, q, eg)
			}()
			if err != nil {
				return
			}
			// This backend passes choice/score probabilities through
			// unvalidated (internal/service is the one authority there); the
			// one distribution IT constructs itself is noul's, and that one
			// is always checked into [0,1] before being used, so it is the
			// one this sweep can hold to that bound without re-implementing
			// the service's own validation here.
			if q.Type == template.TypeNoul {
				for k, p := range ans.Probabilities {
					if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 1 {
						t.Fatalf("seed %d: noul probability[%s] = %v, outside [0,1]", seed, k, p)
					}
				}
			}
		})
	}
}

// hostileJevBody returns a structurally-mutated variant of a valid jev
// response for the given question type: a missing field, a wrong-typed
// field, an extra field, a huge or negative number, or truncated/garbage
// bytes, depending on the seed.
func hostileJevBody(r *rand.Rand, qType template.Type) []byte {
	switch r.Intn(6) {
	case 0:
		return []byte("not json at all, seed garbage {{{")
	case 1:
		return []byte(`{"model":"m"}`) // no answers at all
	case 2:
		return []byte(`{"model":"m","answers":{}}`) // no "q"
	case 3:
		return []byte(`{"model":123,"answers":{"q":{"type":"` + string(qType) + `"}}}`) // model wrong type, no probabilities
	}
	var answer map[string]any
	switch qType {
	case template.TypeChoice:
		answer = map[string]any{"type": "choice", "choice": randHostileValue(r),
			"probabilities": map[string]any{"a": randHostileNumber(r), "b": randHostileNumber(r)}}
	case template.TypeScore:
		answer = map[string]any{"type": "score", "score": randHostileValue(r),
			"probabilities": map[string]any{"0": randHostileNumber(r), "1": randHostileNumber(r), "2": randHostileNumber(r)}}
	case template.TypeNoul:
		answer = map[string]any{"type": "noul", "noul": randHostileNumber(r)}
	}
	body, err := json.Marshal(map[string]any{
		"model": randHostileValue(r), "answers": map[string]any{"q": answer},
		"usage": map[string]any{"input_tokens": randHostileValue(r), "output_tokens": randHostileValue(r)},
	})
	if err != nil {
		return []byte(`{}`)
	}
	return body
}

func randHostileNumber(r *rand.Rand) float64 {
	switch r.Intn(5) {
	case 0:
		return r.Float64()
	case 1:
		return -r.Float64() * 1000
	case 2:
		return r.Float64() * 1e10
	case 3:
		return 0
	default:
		return 1
	}
}

func randHostileValue(r *rand.Rand) any {
	switch r.Intn(6) {
	case 0:
		return nil
	case 1:
		return r.Intn(1000)
	case 2:
		return "text"
	case 3:
		return []int{1, 2, 3}
	case 4:
		return map[string]int{"a": 1}
	default:
		return true
	}
}

// A noul answer with no `noul` field, or with `noul: null`, is an answer
// with no probability in it. Decoding into a plain float64 turned that
// absence into 0, and 0 became {"true": 0, "false": 1}: a certain "no"
// that the server never said. Found by the session model's review of the
// phase D diff on 2026-09-25.
func TestAMissingNoulIsUnansweredNotACertainNo(t *testing.T) {
	cases := map[string]string{
		"field absent": `{"model":"jev-1.13.0","answers":{"q":{"type":"noul"}},"usage":{"input_tokens":1,"output_tokens":1}}`,
		"field null":   `{"model":"jev-1.13.0","answers":{"q":{"type":"noul","noul":null}},"usage":{"input_tokens":1,"output_tokens":1}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			srv := newJevServer(t, jevResponseFixture{Status: 200, Body: []byte(body)})
			ans, _, err := newJevBackend(srv.URL, "k").Ask(context.Background(), noulQ(false, "", ""), template.Egress{})
			var ue *UnansweredError
			if !asErr(err, &ue) || ue.Reason != "no_probabilities" {
				t.Fatalf("expected UnansweredError{no_probabilities}, got err=%v probabilities=%v", err, ans.Probabilities)
			}
		})
	}
}

func TestAJevRefusalIsLoggedWithItsMachineCodeNeverItsMessage(t *testing.T) {
	const marker = "ECHOED-INPUT-MARKER-6160"
	srv := newJevServer(t, jevResponseFixture{Status: 422,
		Body: []byte(`{"error":{"type":"invalid_request","message":"` + marker + `"}}`)})
	var logBuf bytes.Buffer
	j := NewJev(JevConfig{BaseURL: srv.URL, Model: "jev-latest", APIKey: "k",
		Logger: slog.New(slog.NewTextHandler(&logBuf, nil))})
	_, _, err := j.Ask(context.Background(), noulQ(false, "", ""), template.Egress{})
	if err == nil {
		t.Fatal("expected an error for a 422")
	}
	logged := logBuf.String()
	for _, want := range []string{"server refused the call", "status=422", "error_type=invalid_request"} {
		if !strings.Contains(logged, want) {
			t.Errorf("log should contain %q, got %q", want, logged)
		}
	}
	if strings.Contains(logged, marker) || strings.Contains(err.Error(), marker) {
		t.Errorf("the body's text reached the log or the caller: log %q, err %v", logged, err)
	}
}
