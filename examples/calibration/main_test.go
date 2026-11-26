package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestGenItemsIsDeterministicForASeed(t *testing.T) {
	a := genItems(60, 1)
	b := genItems(60, 1)
	if len(a) != 60 || len(b) != 60 {
		t.Fatalf("expected 60 items each, got %d and %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("item %d differs between two runs of the same seed: %+v vs %+v", i, a[i], b[i])
		}
	}
	c := genItems(60, 2)
	same := true
	for i := range a {
		if a[i] != c[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("expected a different seed to produce a different sequence")
	}
}

func TestGenItemsHalfCorrectHalfWrong(t *testing.T) {
	items := genItems(60, 1)
	correct, wrong := 0, 0
	for _, it := range items {
		want := strconv.Itoa(it.a * it.b)
		if it.truth {
			correct++
			if it.finalAnswer != want {
				t.Errorf("item claims truth=true but final_answer %q != correct product %q", it.finalAnswer, want)
			}
		} else {
			wrong++
			if it.finalAnswer == want {
				t.Errorf("item claims truth=false but final_answer %q equals the correct product", it.finalAnswer)
			}
		}
	}
	if correct != 30 || wrong != 30 {
		t.Errorf("expected 30 correct and 30 wrong out of 60, got %d correct, %d wrong", correct, wrong)
	}
}

func TestGenItemsFactorsInRange(t *testing.T) {
	for _, it := range genItems(200, 7) {
		if it.a < 2 || it.a > 19 || it.b < 2 || it.b > 19 {
			t.Fatalf("factor out of [2,19]: a=%d b=%d", it.a, it.b)
		}
	}
}

// fakeTypryx is a stand-in for a running typryx: /v1/ask answers with a
// probability chosen by prob, /v1/outcome records each truth it was told and
// replies with outcomeStatus.
type fakeTypryx struct {
	t             *testing.T
	prob          func(task, final string) (answer any, unanswered bool)
	outcomeStatus int

	mu       sync.Mutex
	asks     []map[string]any
	keys     []string
	outcomes []map[string]any
	n        int
}

func (f *fakeTypryx) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		f.t.Errorf("%s: body is not JSON: %v", req.URL.Path, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, req.Header.Get("X-Typryx-Key"))
	if ct := req.Header.Get("Content-Type"); ct != "application/json" {
		f.t.Errorf("%s: content type %q", req.URL.Path, ct)
	}
	switch req.URL.Path {
	case "/v1/ask":
		f.asks = append(f.asks, body)
		state, _ := body["state"].(map[string]any)
		task, _ := state["task"].(string)
		final, _ := state["final_answer"].(string)
		answer, unanswered := f.prob(task, final)
		if unanswered {
			_ = json.NewEncoder(w).Encode(map[string]any{"unanswered": true, "reason": "over_hourly_cap"})
			return
		}
		f.n++
		_ = json.NewEncoder(w).Encode(map[string]any{"answer_id": fmt.Sprintf("ans-%d", f.n), "answer": answer})
	case "/v1/outcome":
		f.outcomes = append(f.outcomes, body)
		w.WriteHeader(f.outcomeStatus)
	default:
		f.t.Errorf("unexpected path %s", req.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

var askRE = regexp.MustCompile(`^what is (\d+) times (\d+)$`)

// rightAnswer works the product out independently of genItems.
func rightAnswer(task, final string) bool {
	m := askRE.FindStringSubmatch(task)
	if m == nil {
		return false
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[2])
	return strconv.Itoa(a*b) == final
}

func calibrated(task, final string) (any, bool) {
	if rightAnswer(task, final) {
		return 0.8, false
	}
	return 0.2, false
}

// runCapture calls run with temp files standing in for stdout and stderr.
func runCapture(t *testing.T, url, key string, n int, seed int64) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	stdout, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	code := run(url, key, n, seed, stdout, stderr)
	_ = stdout.Close()
	_ = stderr.Close()
	o, _ := os.ReadFile(filepath.Join(dir, "stdout"))
	e, _ := os.ReadFile(filepath.Join(dir, "stderr"))
	return code, string(o), string(e)
}

func TestRunAsksEachItemAndTellsTypryxTheRealTruth(t *testing.T) {
	f := &fakeTypryx{t: t, prob: calibrated, outcomeStatus: http.StatusOK}
	srv := httptest.NewServer(f)
	defer srv.Close()

	code, out, errOut := runCapture(t, srv.URL+"/", "k1", 10, 3)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if out != "asked=10 answered=10 outcomes_recorded=10 threshold_accuracy=1.000 seed=3\n" {
		t.Errorf("summary line: %q", out)
	}
	if errOut != "" {
		t.Errorf("unexpected stderr: %q", errOut)
	}
	items := genItems(10, 3)
	if len(f.asks) != 10 || len(f.outcomes) != 10 {
		t.Fatalf("expected 10 asks and 10 outcomes, got %d and %d", len(f.asks), len(f.outcomes))
	}
	for i, ask := range f.asks {
		if ask["template"] != "eval.outcome_met" {
			t.Errorf("ask %d template %v", i, ask["template"])
		}
		state := ask["state"].(map[string]any)
		if state["task"] != fmt.Sprintf("what is %d times %d", items[i].a, items[i].b) || state["final_answer"] != items[i].finalAnswer {
			t.Errorf("ask %d state %v, want item %+v", i, state, items[i])
		}
		o := f.outcomes[i]
		if o["answer_id"] != fmt.Sprintf("ans-%d", i+1) || o["truth"] != items[i].truth || o["source"] != "examples/calibration" {
			t.Errorf("outcome %d: %v, want truth %v for ans-%d", i, o, items[i].truth, i+1)
		}
		if items[i].truth != rightAnswer(state["task"].(string), items[i].finalAnswer) {
			t.Errorf("item %d: truth label disagrees with the arithmetic", i)
		}
	}
	for i, k := range f.keys {
		if k != "k1" {
			t.Fatalf("request %d sent key %q, want k1", i, k)
		}
	}
}

func TestRunSendsNoCredentialWhenTheKeyIsEmpty(t *testing.T) {
	f := &fakeTypryx{t: t, prob: calibrated, outcomeStatus: http.StatusOK}
	srv := httptest.NewServer(f)
	defer srv.Close()
	if code, _, _ := runCapture(t, srv.URL, "", 4, 1); code != 0 {
		t.Fatalf("exit %d", code)
	}
	for i, k := range f.keys {
		if k != "" {
			t.Fatalf("request %d sent a key %q with none configured", i, k)
		}
	}
}

func TestRunThresholdAccuracyIsStrictlyAboveOneHalf(t *testing.T) {
	// Exactly 0.5 is "false" at a >0.5 threshold, so a judge saying 0.5 for
	// every wrong answer and 0.9 for every right one scores every item.
	f := &fakeTypryx{t: t, prob: func(task, final string) (any, bool) {
		if rightAnswer(task, final) {
			return 0.9, false
		}
		return 0.5, false
	}, outcomeStatus: http.StatusOK}
	srv := httptest.NewServer(f)
	defer srv.Close()
	_, out, _ := runCapture(t, srv.URL, "k", 10, 1)
	if !strings.Contains(out, "answered=10 outcomes_recorded=10 threshold_accuracy=1.000") {
		t.Errorf("summary line: %q", out)
	}

	// An answer that is not a number counts as answered but never correct.
	g := &fakeTypryx{t: t, prob: func(string, string) (any, bool) { return "true", false }, outcomeStatus: http.StatusOK}
	srv2 := httptest.NewServer(g)
	defer srv2.Close()
	_, out2, _ := runCapture(t, srv2.URL, "k", 10, 1)
	if !strings.Contains(out2, "answered=10 outcomes_recorded=10 threshold_accuracy=0.000") {
		t.Errorf("summary line: %q", out2)
	}
}

func TestRunRecordsNoOutcomeForAnUnansweredAskAndFails(t *testing.T) {
	f := &fakeTypryx{t: t, prob: func(string, string) (any, bool) { return nil, true }, outcomeStatus: http.StatusOK}
	srv := httptest.NewServer(f)
	defer srv.Close()
	code, out, _ := runCapture(t, srv.URL, "k", 6, 1)
	if code != 1 {
		t.Errorf("nothing recorded must exit 1, got %d", code)
	}
	if out != "asked=6 answered=0 outcomes_recorded=0 threshold_accuracy=0.000 seed=1\n" {
		t.Errorf("summary line: %q", out)
	}
	if len(f.outcomes) != 0 {
		t.Errorf("an unanswered ask must never get an outcome, got %d", len(f.outcomes))
	}
}

func TestRunCountsOnlyOutcomesTypryxAccepted(t *testing.T) {
	f := &fakeTypryx{t: t, prob: calibrated, outcomeStatus: http.StatusConflict}
	srv := httptest.NewServer(f)
	defer srv.Close()
	code, out, _ := runCapture(t, srv.URL, "k", 4, 1)
	if code != 1 || !strings.Contains(out, "answered=4 outcomes_recorded=0") {
		t.Errorf("a refused outcome is not recorded: exit %d, %q", code, out)
	}
}

func TestRunReportsAnUndecodableAskAndCarriesOn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "<html>")
	}))
	defer srv.Close()
	code, out, errOut := runCapture(t, srv.URL, "k", 3, 1)
	if code != 1 || !strings.HasPrefix(out, "asked=3 answered=0 ") {
		t.Errorf("exit %d, %q", code, out)
	}
	if strings.Count(errOut, "decoding response") != 3 || !strings.Contains(errOut, "ask 3: decoding response") {
		t.Errorf("stderr: %q", errOut)
	}
}

func TestRunReportsAnUnreachableTypryx(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	code, out, errOut := runCapture(t, url, "k", 2, 1)
	if code != 1 || !strings.HasPrefix(out, "asked=2 answered=0 ") {
		t.Errorf("exit %d, %q", code, out)
	}
	if !strings.Contains(errOut, "ask 1: ") || !strings.Contains(errOut, "ask 2: ") {
		t.Errorf("stderr: %q", errOut)
	}
}

func TestRunReportsAnOutcomeThatCouldNotBeSent(t *testing.T) {
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/v1/outcome" {
			// Drop the connection: the client sees a transport error.
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		mu.Lock()
		n++
		id := n
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"answer_id": fmt.Sprintf("a%d", id), "answer": 0.9})
	}))
	defer srv.Close()
	code, out, errOut := runCapture(t, srv.URL, "k", 2, 1)
	if code != 1 || !strings.Contains(out, "answered=2 outcomes_recorded=0") {
		t.Errorf("exit %d, %q", code, out)
	}
	if !strings.Contains(errOut, "outcome 1: ") || !strings.Contains(errOut, "outcome 2: ") {
		t.Errorf("stderr: %q", errOut)
	}
}

func TestRunWithNoItemsFails(t *testing.T) {
	code, out, _ := runCapture(t, "http://127.0.0.1:1", "", 0, 9)
	if code != 1 || out != "asked=0 answered=0 outcomes_recorded=0 threshold_accuracy=0.000 seed=9\n" {
		t.Errorf("exit %d, %q", code, out)
	}
}

func TestPostJSONRefusesWhatItCannotSend(t *testing.T) {
	client := &http.Client{}
	if _, err := postJSON(client, "http://127.0.0.1:1", "", make(chan int)); err == nil {
		t.Error("an unencodable body must be an error")
	}
	if _, err := postJSON(client, "http://[::1", "", map[string]any{}); err == nil {
		t.Error("a malformed URL must be an error")
	}
}

func TestAWrongAnswerAlwaysDiffersFromTheProduct(t *testing.T) {
	for seed := int64(0); seed < 200; seed++ {
		r := rand.New(rand.NewSource(seed))
		for _, n := range []int{4, 9, 10, 99, 100, 361} {
			if got := offByOneDigit(n, r); got == n {
				t.Fatalf("seed %d: offByOneDigit(%d) returned it unchanged", seed, n)
			}
			if got := plausibleWrongAnswer(n, 3, 7, r); got == n {
				t.Fatalf("seed %d: plausibleWrongAnswer(%d) returned it unchanged", seed, n)
			}
		}
	}
}
