package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
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

// runMain drives main() in-process the way `go run ./examples/speed ...`
// would: a fresh flag set, os.Args, and stdout/stderr captured through pipes.
func runMain(t *testing.T, args ...string) (string, string) {
	t.Helper()
	oldArgs, oldFlags, oldOut, oldErr := os.Args, flag.CommandLine, os.Stdout, os.Stderr
	t.Cleanup(func() { os.Args, flag.CommandLine, os.Stdout, os.Stderr = oldArgs, oldFlags, oldOut, oldErr })

	flag.CommandLine = flag.NewFlagSet("speed", flag.ContinueOnError)
	os.Args = append([]string{"speed"}, args...)
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = wOut, wErr

	var outBuf, errBuf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(&outBuf, rOut) }()
	go func() { defer wg.Done(); _, _ = io.Copy(&errBuf, rErr) }()

	main()

	_ = wOut.Close()
	_ = wErr.Close()
	wg.Wait()
	os.Stdout, os.Stderr = oldOut, oldErr
	return outBuf.String(), errBuf.String()
}

var taskRE = regexp.MustCompile(`what is (\d+) times (\d+)`)

// isCorrect works the arithmetic out independently of genItems, so a wrong
// truth label in genItems shows up as a lost point of accuracy.
func isCorrect(t *testing.T, task, final string) bool {
	m := taskRE.FindStringSubmatch(task)
	if m == nil {
		t.Errorf("task %q is not an arithmetic ask", task)
		return false
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[2])
	return strconv.Itoa(a*b) == final
}

// recorder keeps every request body and header a fake endpoint saw.
type recorder struct {
	mu      sync.Mutex
	bodies  []map[string]any
	headers []http.Header
	paths   []string
}

func (r *recorder) add(req *http.Request, body map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bodies = append(r.bodies, body)
	r.headers = append(r.headers, req.Header.Clone())
	r.paths = append(r.paths, req.URL.Path)
}

func decodeBody(t *testing.T, req *http.Request) map[string]any {
	var body map[string]any
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		t.Errorf("request body is not JSON: %v", err)
	}
	return body
}

// typryxServer answers /v1/ask with probability pRight when the proposed
// answer is right and pWrong when it is wrong.
func typryxServer(t *testing.T, rec *recorder, pRight, pWrong float64) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body := decodeBody(t, req)
		rec.add(req, body)
		state, _ := body["state"].(map[string]any)
		task, _ := state["task"].(string)
		final, _ := state["final_answer"].(string)
		p := pWrong
		if isCorrect(t, task, final) {
			p = pRight
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"answer_id": "x", "answer": p})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// chatServer answers /chat/completions with a verdict line built by verdict.
func chatServer(t *testing.T, rec *recorder, verdict func(correct bool) string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body := decodeBody(t, req)
		rec.add(req, body)
		msgs, _ := body["messages"].([]any)
		content := ""
		if len(msgs) == 1 {
			content, _ = msgs[0].(map[string]any)["content"].(string)
		}
		task := taskRE.FindString(content)
		final := ""
		if i := strings.Index(content, "Final answer given: "); i >= 0 {
			final = strings.SplitN(content[i+len("Final answer given: "):], "\n", 2)[0]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": verdict(isCorrect(t, task, final))}}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func honestVerdict(correct bool) string {
	if correct {
		return "The product matches.\nverdict: true"
	}
	return "It is off.\nVERDICT: FALSE"
}

func keyFile(t *testing.T, content string) string {
	p := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func wantLine(t *testing.T, out string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(out, p) {
			t.Errorf("summary line %q lacks %q", out, p)
		}
	}
}

func TestTypryxModeScoresAPerfectJudgeAsFullyAccurate(t *testing.T) {
	rec := &recorder{}
	srv := typryxServer(t, rec, 0.9, 0.1)
	out, _ := runMain(t, "-mode", "typryx", "-url", srv.URL, "-model", "LABEL")

	wantLine(t, out, "typryx", "LABEL", "n=60 acc=1.000 unparsed=0", "in_tok=0 out_tok=0")
	if len(rec.bodies) != 60 {
		t.Fatalf("expected 60 asks, got %d", len(rec.bodies))
	}
	for i, h := range rec.headers {
		if h.Get("X-Typryx-Key") != "k1" || h.Get("Authorization") != "" {
			t.Fatalf("ask %d: typryx mode must send X-Typryx-Key k1 and no bearer, got %v", i, h)
		}
		if rec.paths[i] != "/v1/ask" || rec.bodies[i]["template"] != "eval.outcome_met" {
			t.Fatalf("ask %d: wrong path or template: %s %v", i, rec.paths[i], rec.bodies[i]["template"])
		}
	}
}

func TestTypryxModeCountsAnAnswerWithoutAProbabilityAsUnparsed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"unanswered":true,"reason":"cap"}`)
	}))
	defer srv.Close()
	out, _ := runMain(t, "-mode", "typryx", "-url", srv.URL)
	wantLine(t, out, "n=60 acc=0.000 unparsed=60")
}

func TestTypryxModeThresholdIsInclusiveAtOneHalf(t *testing.T) {
	// Exactly 0.5 for every right answer reads as "true", so every item scores.
	srv := typryxServer(t, &recorder{}, 0.5, 0.49)
	out, _ := runMain(t, "-mode", "typryx", "-url", srv.URL)
	wantLine(t, out, "n=60 acc=1.000 unparsed=0")
}

func TestTextModeSendsTheMeasuredRequestAndParsesTheVerdict(t *testing.T) {
	rec := &recorder{}
	srv := chatServer(t, rec, honestVerdict)
	out, errOut := runMain(t, "-mode", "text", "-url", srv.URL, "-model", "m1", "-keyfile", keyFile(t, "  sk-test \n"))

	wantLine(t, out, "text", "m1", "n=60 acc=1.000 unparsed=0", "in_tok=600 out_tok=300")
	if errOut != "" {
		t.Errorf("unexpected stderr: %q", errOut)
	}
	if len(rec.bodies) != 60 {
		t.Fatalf("expected 60 calls, got %d", len(rec.bodies))
	}
	b, h := rec.bodies[0], rec.headers[0]
	if rec.paths[0] != "/chat/completions" {
		t.Errorf("path %q", rec.paths[0])
	}
	if h.Get("Authorization") != "Bearer sk-test" {
		t.Errorf("the key file must be trimmed and sent as a bearer, got %q", h.Get("Authorization"))
	}
	if b["model"] != "m1" || b["max_tokens"] != float64(400) || b["temperature"] != float64(0) {
		t.Errorf("text request: %v", b)
	}
	if _, ok := b["max_completion_tokens"]; ok {
		t.Errorf("text mode must not send max_completion_tokens: %v", b)
	}
	if _, ok := b["reasoning_effort"]; ok {
		t.Errorf("no reasoning_effort unless asked: %v", b)
	}
}

func TestTextModeOmitsTemperatureAndSendsAChosenTemperature(t *testing.T) {
	rec := &recorder{}
	srv := chatServer(t, rec, honestVerdict)
	runMain(t, "-mode", "text", "-url", srv.URL, "-temperature", "omit")
	if _, ok := rec.bodies[0]["temperature"]; ok {
		t.Errorf("-temperature omit still sent one: %v", rec.bodies[0])
	}

	rec2 := &recorder{}
	srv2 := chatServer(t, rec2, honestVerdict)
	runMain(t, "-mode", "text", "-url", srv2.URL, "-temperature", "0.7")
	if rec2.bodies[0]["temperature"] != 0.7 {
		t.Errorf("expected temperature 0.7, got %v", rec2.bodies[0]["temperature"])
	}
}

func TestReasoningModeUsesItsOwnBudgetAndTheFlagsItNames(t *testing.T) {
	rec := &recorder{}
	srv := chatServer(t, rec, honestVerdict)
	out, _ := runMain(t, "-mode", "reasoning", "-url", srv.URL, "-model", "r1", "-reasoning-effort", "low")
	wantLine(t, out, "reasoning", "n=60 acc=1.000")
	b := rec.bodies[0]
	if b["max_completion_tokens"] != float64(4000) || b["reasoning_effort"] != "low" {
		t.Errorf("reasoning request: %v", b)
	}
	if _, ok := b["temperature"]; ok {
		t.Errorf("reasoning mode must not send a temperature: %v", b)
	}
	if _, ok := b["max_tokens"]; ok {
		t.Errorf("reasoning mode must not send max_tokens: %v", b)
	}

	rec2 := &recorder{}
	srv2 := chatServer(t, rec2, honestVerdict)
	runMain(t, "-mode", "reasoning", "-url", srv2.URL, "-tokens-field", "max_tokens")
	b2 := rec2.bodies[0]
	if b2["max_tokens"] != float64(4000) {
		t.Errorf("-tokens-field must rename the budget field and keep the budget: %v", b2)
	}
	if _, ok := b2["max_completion_tokens"]; ok {
		t.Errorf("-tokens-field must replace, not add: %v", b2)
	}
}

func TestAVerdictTakesTheLastVerdictLineAndAnUnclearOneIsUnparsed(t *testing.T) {
	// The model first quotes the instruction, then answers; only the last
	// VERDICT: counts. A wrong verdict for every item scores zero.
	srv := chatServer(t, &recorder{}, func(correct bool) string {
		if correct {
			return "End with VERDICT: TRUE or VERDICT: FALSE.\nVERDICT: FALSE"
		}
		return "VERDICT: TRUE"
	})
	out, _ := runMain(t, "-mode", "text", "-url", srv.URL)
	wantLine(t, out, "n=60 acc=0.000 unparsed=0")

	srv2 := chatServer(t, &recorder{}, func(bool) string { return "VERDICT: maybe" })
	out2, _ := runMain(t, "-mode", "text", "-url", srv2.URL)
	wantLine(t, out2, "n=60 acc=0.000 unparsed=60")

	srv3 := chatServer(t, &recorder{}, func(bool) string { return "I think it is TRUE" })
	out3, _ := runMain(t, "-mode", "text", "-url", srv3.URL)
	wantLine(t, out3, "unparsed=60")
}

func TestAnErroringEndpointIsReportedAndCountedAsUnparsed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"max_tokens is not supported"}}`)
	}))
	defer srv.Close()
	out, errOut := runMain(t, "-mode", "text", "-url", srv.URL)
	wantLine(t, out, "n=60 acc=0.000 unparsed=60")
	if strings.Count(errOut, "call error: status 400") != 60 || !strings.Contains(errOut, "max_tokens is not supported") {
		t.Errorf("expected 60 status-400 call errors naming the refusal, got %q", errOut)
	}
}

func TestANonJSONReplyIsAnErrorCarryingTheStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "<html>bad gateway</html>")
	}))
	defer srv.Close()
	out, _, err := post(srv.URL, "", map[string]any{})
	if out != nil || err == nil || !strings.Contains(err.Error(), "status 502") || !strings.Contains(err.Error(), "bad gateway") {
		t.Fatalf("expected a status-502 error quoting the body, got %v, %v", out, err)
	}
}

func TestPostSendsTheTypryxKeyAsItsOwnHeaderAndAnythingElseAsABearer(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rec.add(req, nil)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()
	for _, key := range []string{"X-Typryx-Key:abc", "sk-1", ""} {
		out, ms, err := post(srv.URL, key, map[string]any{"a": 1})
		if err != nil || out["ok"] != true || ms < 0 {
			t.Fatalf("key %q: %v %v %v", key, out, ms, err)
		}
	}
	if h := rec.headers[0]; h.Get("X-Typryx-Key") != "abc" || h.Get("Authorization") != "" {
		t.Errorf("typryx key: %v", h)
	}
	if h := rec.headers[1]; h.Get("Authorization") != "Bearer sk-1" || h.Get("X-Typryx-Key") != "" {
		t.Errorf("bearer key: %v", h)
	}
	if h := rec.headers[2]; h.Get("Authorization") != "" || h.Get("X-Typryx-Key") != "" {
		t.Errorf("no key must send no credential: %v", h)
	}
	if rec.headers[0].Get("Content-Type") != "application/json" {
		t.Errorf("content type: %v", rec.headers[0])
	}
}

func TestPostReportsAnUnreachableEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	if _, _, err := post(url, "", map[string]any{}); err == nil {
		t.Fatal("expected an error from a closed endpoint")
	}
}

func TestPct(t *testing.T) {
	if got := pct(nil, 0.5); got != 0 {
		t.Errorf("empty: %v", got)
	}
	xs := []float64{50, 10, 40, 20, 30}
	if got := pct(xs, 0.5); got != 30 {
		t.Errorf("p50: %v", got)
	}
	if got := pct(xs, 0.95); got != 40 {
		t.Errorf("p95 of 5 values (index 3): %v", got)
	}
	if got := pct(xs, 1); got != 50 {
		t.Errorf("p100: %v", got)
	}
	if got := pct(xs, 0); got != 10 {
		t.Errorf("p0: %v", got)
	}
	if fmt.Sprint(xs) != "[50 10 40 20 30]" {
		t.Errorf("pct must not reorder its input: %v", xs)
	}
}

func TestGenItemsHalfCorrectHalfWrongAndDeterministic(t *testing.T) {
	a, b := genItems(60, 1), genItems(60, 1)
	correct := 0
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("item %d differs for the same seed", i)
		}
		want := strconv.Itoa(a[i].a * a[i].b)
		if a[i].truth != (a[i].finalAnswer == want) {
			t.Fatalf("item %d: truth %v but final %q vs product %q", i, a[i].truth, a[i].finalAnswer, want)
		}
		if a[i].truth {
			correct++
		}
	}
	if correct != 30 {
		t.Errorf("expected 30 correct of 60, got %d", correct)
	}
}
