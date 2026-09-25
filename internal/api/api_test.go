package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/TAIPANBOX/agent-stack-go/event"
	"github.com/TAIPANBOX/typryx/internal/api"
	"github.com/TAIPANBOX/typryx/internal/backend"
	"github.com/TAIPANBOX/typryx/internal/door"
	"github.com/TAIPANBOX/typryx/internal/ledger"
	"github.com/TAIPANBOX/typryx/internal/record"
	"github.com/TAIPANBOX/typryx/internal/service"
	"github.com/TAIPANBOX/typryx/internal/template"
)

type noopMCP struct{ called bool }

func (n *noopMCP) ServeMCP(w http.ResponseWriter, r *http.Request, agentID string) {
	n.called = true
	w.WriteHeader(http.StatusOK)
}

func newTestServer(t *testing.T, keys door.Keys) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	tmplDir := filepath.Join(dir, "templates")
	os.Mkdir(tmplDir, 0o755)
	tmpl := template.Template{ID: "eval.outcome_met", Type: template.TypeNoul, Instructions: "is it true", Fields: []string{"task"}}
	b, _ := json.Marshal(tmpl)
	os.WriteFile(filepath.Join(tmplDir, "t.json"), b, 0o644)
	reg, _, err := template.LoadDir(tmplDir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	journalPath := filepath.Join(dir, "events.ndjson")
	j, err := record.Open(journalPath)
	if err != nil {
		t.Fatalf("record.Open: %v", err)
	}
	t.Cleanup(func() { j.Close() })

	svc := service.New()
	svc.Templates = reg
	svc.Backend = stubBackend{}
	svc.Cap = service.NewCap(1000)
	svc.Journal = j

	srv := &api.Server{Keys: keys, Service: svc, MCP: &noopMCP{}}
	ts := httptest.NewServer(api.NewMux(srv))
	t.Cleanup(ts.Close)
	return ts, journalPath
}

// stubBackend answers every noul question with a fixed, valid distribution;
// api_test only needs a backend that produces a real answer, not the
// determinism of the real backend.Stub.
type stubBackend struct{}

func (stubBackend) Name() string { return "stub" }

func (stubBackend) Ask(_ context.Context, q backend.Question, _ template.Egress) (backend.Answer, backend.Usage, error) {
	return backend.Answer{Yes: 0.5, Probabilities: map[string]float64{"true": 0.5, "false": 0.5}, Model: "stub-0"}, backend.Usage{}, nil
}

// @test:TestIdentityComesFromTheCredentialNeverFromAHeader
func TestIdentityComesFromTheCredentialNeverFromAHeader(t *testing.T) {
	keys := door.ParseKeys("k1=agent://acme.example/real-bot")
	ts, journalPath := newTestServer(t, keys)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/ask", bytes.NewReader(
		[]byte(`{"template":"eval.outcome_met","state":{"task":"t"}}`)))
	req.Header.Set(door.KeyHeader, "k1")
	req.Header.Set("X-Fuse-Agent-Id", "agent://attacker.example/fake")
	req.Header.Set("X-Agent-Id", "attacker2")
	req.Header.Set("Agent-Passport", "attacker3")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	events, err := event.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("reading journal: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].AgentID != "agent://acme.example/real-bot" {
		t.Errorf("expected the credential's bound identity, got %q (a header claim leaked through)", events[0].AgentID)
	}
}

func TestHealthzNeedsNoCredential(t *testing.T) {
	ts, _ := newTestServer(t, door.ParseKeys("k1"))
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with no credential, got %d", resp.StatusCode)
	}
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Errorf("expected status ok, got %v", body)
	}
	journal, ok := body["journal"].(map[string]any)
	if !ok {
		t.Fatalf("expected a journal object, got %v", body)
	}
	if _, ok := journal["skipped_no_agent"]; !ok {
		t.Error("expected skipped_no_agent in the journal counts")
	}
}

// @test:TestALedgerWriteFailureIsCountedAndVisibleAtHealthz
//
// A ledger write failure must never turn a real answer into a refusal (the
// caller already got a valid answer from the backend), but it also must not
// be swallowed silently: an operator needs to see it. Counted on the
// service and exposed at GET /healthz next to the journal counts.
func TestALedgerWriteFailureIsCountedAndVisibleAtHealthz(t *testing.T) {
	dir := t.TempDir()
	tmplDir := filepath.Join(dir, "templates")
	os.Mkdir(tmplDir, 0o755)
	tmpl := template.Template{ID: "eval.outcome_met", Type: template.TypeNoul, Instructions: "is it true", Fields: []string{"task"}}
	b, _ := json.Marshal(tmpl)
	os.WriteFile(filepath.Join(tmplDir, "t.json"), b, 0o644)
	reg, _, _ := template.LoadDir(tmplDir)
	j, _ := record.Open("")
	led, err := ledger.Open(filepath.Join(dir, "ledger"))
	if err != nil {
		t.Fatalf("ledger.Open: %v", err)
	}
	// Close the ledger's files out from under the service: PutAnswer will
	// now fail on every call, which is the write-failure path under test.
	if err := led.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	svc := service.New()
	svc.Templates = reg
	svc.Backend = stubBackend{}
	svc.Cap = service.NewCap(1000)
	svc.Journal = j
	svc.Ledger = led

	srv := &api.Server{Keys: door.ParseKeys(""), Service: svc, MCP: &noopMCP{}}
	ts := httptest.NewServer(api.NewMux(srv))
	t.Cleanup(ts.Close)

	askResp, err := http.Post(ts.URL+"/v1/ask", "application/json",
		bytes.NewReader([]byte(`{"template":"eval.outcome_met","state":{"task":"t"}}`)))
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	defer askResp.Body.Close()
	if askResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(askResp.Body)
		t.Fatalf("the answer itself must still succeed despite the ledger failure, got %d: %s", askResp.StatusCode, body)
	}

	healthResp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	defer healthResp.Body.Close()
	var body map[string]any
	json.NewDecoder(healthResp.Body).Decode(&body)
	ledgerCounts, ok := body["ledger"].(map[string]any)
	if !ok {
		t.Fatalf("expected a ledger object in /healthz, got %v", body)
	}
	failed, _ := ledgerCounts["write_failed"].(float64)
	if failed != 1 {
		t.Errorf("expected ledger.write_failed=1, got %v (full body: %v)", ledgerCounts["write_failed"], body)
	}
}

func TestOtherRoutesRefuseWithoutACredential(t *testing.T) {
	ts, _ := newTestServer(t, door.ParseKeys("k1"))
	for _, path := range []string{"/v1/ask", "/v1/outcome", "/v1/templates", "/mcp"} {
		resp, err := http.Post(ts.URL+path, "application/json", bytes.NewReader([]byte(`{}`)))
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: expected 401 with no credential, got %d", path, resp.StatusCode)
		}
	}
}

func TestAskRefusalCodesMapToTheDocumentedHTTPStatus(t *testing.T) {
	ts, _ := newTestServer(t, door.ParseKeys(""))
	resp, err := http.Post(ts.URL+"/v1/ask", "application/json",
		bytes.NewReader([]byte(`{"template":"no-such-template","state":{}}`)))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for an unknown template, got %d", resp.StatusCode)
	}
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	if body["error"] != "unknown_template" {
		t.Errorf("expected error unknown_template, got %v", body)
	}
}

func TestTemplatesListsWhatCanBeAsked(t *testing.T) {
	ts, _ := newTestServer(t, door.ParseKeys(""))
	resp, err := http.Get(ts.URL + "/v1/templates")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Templates []map[string]any `json:"templates"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Templates) != 1 {
		t.Fatalf("expected 1 template, got %d", len(body.Templates))
	}
	if body.Templates[0]["id"] != "eval.outcome_met" {
		t.Errorf("expected eval.outcome_met, got %v", body.Templates[0]["id"])
	}
}

func TestUnparseableAskBodyIs400NotA500(t *testing.T) {
	ts, _ := newTestServer(t, door.ParseKeys(""))
	resp, err := http.Post(ts.URL+"/v1/ask", "application/json", bytes.NewReader([]byte(`{not json`)))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for unparseable JSON, got %d", resp.StatusCode)
	}
}

func TestOutcomeHappyPathAndErrors(t *testing.T) {
	dir := t.TempDir()
	tmplDir := filepath.Join(dir, "templates")
	os.Mkdir(tmplDir, 0o755)
	tmpl := template.Template{ID: "eval.outcome_met", Type: template.TypeNoul, Instructions: "is it true", Fields: []string{"task"}}
	b, _ := json.Marshal(tmpl)
	os.WriteFile(filepath.Join(tmplDir, "t.json"), b, 0o644)
	reg, _, _ := template.LoadDir(tmplDir)
	j, _ := record.Open(filepath.Join(dir, "events.ndjson"))
	t.Cleanup(func() { j.Close() })
	led, err := ledger.Open(filepath.Join(dir, "ledger"))
	if err != nil {
		t.Fatalf("ledger.Open: %v", err)
	}
	t.Cleanup(func() { led.Close() })

	svc := service.New()
	svc.Templates = reg
	svc.Backend = stubBackend{}
	svc.Cap = service.NewCap(1000)
	svc.Journal = j
	svc.Ledger = led

	srv := &api.Server{Keys: door.ParseKeys(""), Service: svc, MCP: &noopMCP{}}
	ts := httptest.NewServer(api.NewMux(srv))
	t.Cleanup(ts.Close)

	askResp, err := http.Post(ts.URL+"/v1/ask", "application/json",
		bytes.NewReader([]byte(`{"template":"eval.outcome_met","state":{"task":"t"}}`)))
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	var askBody struct {
		AnswerID string `json:"answer_id"`
	}
	json.NewDecoder(askResp.Body).Decode(&askBody)
	askResp.Body.Close()

	outResp, err := http.Post(ts.URL+"/v1/outcome", "application/json",
		bytes.NewReader([]byte(`{"answer_id":"`+askBody.AnswerID+`","truth":true,"source":"human"}`)))
	if err != nil {
		t.Fatalf("outcome: %v", err)
	}
	defer outResp.Body.Close()
	if outResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(outResp.Body)
		t.Fatalf("expected 200, got %d: %s", outResp.StatusCode, body)
	}

	// Wrong method.
	resp, _ := http.Get(ts.URL + "/v1/outcome")
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for GET /v1/outcome, got %d", resp.StatusCode)
	}
	// Bad JSON.
	resp2, _ := http.Post(ts.URL+"/v1/outcome", "application/json", bytes.NewReader([]byte(`{not json`)))
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for unparseable outcome body, got %d", resp2.StatusCode)
	}
	// Unknown answer.
	resp3, _ := http.Post(ts.URL+"/v1/outcome", "application/json", bytes.NewReader([]byte(`{"answer_id":"nope","truth":true}`)))
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for an unknown answer id, got %d", resp3.StatusCode)
	}
}

func TestMCPRouteReachesTheHandler(t *testing.T) {
	ts, _ := newTestServer(t, door.ParseKeys(""))
	resp, err := http.Post(ts.URL+"/mcp", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected the noop MCP handler's 200, got %d", resp.StatusCode)
	}
}

func TestTemplatesViewIncludesChoiceOptionsAndScoreLevels(t *testing.T) {
	dir := t.TempDir()
	tmplDir := filepath.Join(dir, "templates")
	os.Mkdir(tmplDir, 0o755)
	choice := template.Template{ID: "c", Type: template.TypeChoice, Instructions: "x",
		Criteria: json.RawMessage(`{"a":"d1","b":"d2"}`), Fields: []string{"x"}}
	score := template.Template{ID: "s", Type: template.TypeScore, Instructions: "y",
		Criteria: json.RawMessage(`["l0","l1"]`), Fields: []string{"x"}}
	cb, _ := json.Marshal(choice)
	sb, _ := json.Marshal(score)
	os.WriteFile(filepath.Join(tmplDir, "c.json"), cb, 0o644)
	os.WriteFile(filepath.Join(tmplDir, "s.json"), sb, 0o644)
	reg, _, _ := template.LoadDir(tmplDir)
	j, _ := record.Open("")
	svc := service.New()
	svc.Templates = reg
	svc.Backend = stubBackend{}
	svc.Cap = service.NewCap(1000)
	svc.Journal = j
	srv := &api.Server{Keys: door.ParseKeys(""), Service: svc, MCP: &noopMCP{}}
	ts := httptest.NewServer(api.NewMux(srv))
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/v1/templates")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		Templates []struct {
			ID      string   `json:"id"`
			Options []string `json:"options,omitempty"`
			Levels  []string `json:"levels,omitempty"`
		} `json:"templates"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	found := map[string][]string{}
	foundLevels := map[string][]string{}
	for _, tv := range body.Templates {
		found[tv.ID] = tv.Options
		foundLevels[tv.ID] = tv.Levels
	}
	if len(found["c"]) != 2 {
		t.Errorf("expected 2 options for the choice template, got %v", found["c"])
	}
	if len(foundLevels["s"]) != 2 {
		t.Errorf("expected 2 levels for the score template, got %v", foundLevels["s"])
	}
}

func TestWrongMethodsAreRejected(t *testing.T) {
	ts, _ := newTestServer(t, door.ParseKeys(""))
	resp, err := http.Get(ts.URL + "/v1/ask")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for GET /v1/ask, got %d", resp.StatusCode)
	}
	resp2, err := http.Post(ts.URL+"/v1/templates", "application/json", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for POST /v1/templates, got %d", resp2.StatusCode)
	}
}

// TestAskBodyParserNeverPanicsOnHostileInput is a seeded sweep of
// random/mutated bytes posted as the request body: the handler must always
// answer with an HTTP status (never crash the server, never 500).
func TestAskBodyParserNeverPanicsOnHostileInput(t *testing.T) {
	ts, _ := newTestServer(t, door.ParseKeys(""))
	base := []byte(`{"template":"eval.outcome_met","state":{"task":"t"},"run_id":"r1"}`)
	client := &http.Client{}
	for seed := int64(0); seed < 200; seed++ {
		body := mutateJSON(base, seed)
		resp, err := client.Post(ts.URL+"/v1/ask", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("seed %d: request failed (server may have crashed): %v", seed, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 500 {
			t.Errorf("seed %d: got a 5xx (%d) for hostile input %s", seed, resp.StatusCode, body)
		}
	}
}

func mutateJSON(b []byte, seed int64) []byte {
	r := rand.New(rand.NewSource(seed))
	out := append([]byte(nil), b...)
	n := r.Intn(12) + 1
	for i := 0; i < n; i++ {
		if len(out) == 0 {
			break
		}
		switch r.Intn(4) {
		case 0:
			out[r.Intn(len(out))] = byte(r.Intn(256))
		case 1:
			cut := r.Intn(len(out) + 1)
			out = out[:cut]
		case 2:
			pos := r.Intn(len(out) + 1)
			junk := []byte{byte(r.Intn(256))}
			out = append(out[:pos], append(junk, out[pos:]...)...)
		case 3:
			if len(out) < 2 {
				continue
			}
			a := r.Intn(len(out))
			bnd := a + r.Intn(len(out)-a)
			out = append(out, out[a:bnd]...)
		}
	}
	return out
}
