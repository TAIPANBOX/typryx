package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

// capturingMCP is a test double that records exactly what handleMCPRoute
// decided and handed down: the agent identity it resolved, and the raw
// bytes of the body it forwarded. It never dispatches a real tool call, so
// these tests are about the door's own decision (credential source,
// precedence, stripping), not about internal/mcp.Server's JSON-RPC
// semantics, which mcp_test.go already covers end to end.
type capturingMCP struct {
	agentID string
	body    []byte
}

func (c *capturingMCP) ServeMCP(w http.ResponseWriter, r *http.Request, agentID string) {
	c.agentID = agentID
	b, _ := io.ReadAll(r.Body)
	c.body = b
	w.WriteHeader(http.StatusOK)
}

// newMetaTestServer builds an api.Server with the given keys and
// AcceptKeyInMeta setting, and returns it alongside the fake it captures
// into. The service carries a disabled journal (never nil: record.Open("")
// still needs to be called for that) so that a door-decision test which
// reaches all the way into internal/service, whether by design or by a
// planted mutant that wrongly lets a call through, fails on its own
// assertion rather than on a nil-pointer panic that would obscure it.
func newMetaTestServer(keys door.Keys, acceptKeyInMeta bool) (*httptest.Server, *capturingMCP) {
	fake := &capturingMCP{}
	svc := service.New()
	j, _ := record.Open("")
	svc.Journal = j
	srv := &api.Server{Keys: keys, Service: svc, MCP: fake, AcceptKeyInMeta: acceptKeyInMeta}
	ts := httptest.NewServer(api.NewMux(srv))
	return ts, fake
}

func postMCP(t *testing.T, ts *httptest.Server, headerKey, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	if headerKey != "" {
		req.Header.Set(door.KeyHeader, headerKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	return resp
}

const toolsCallWithMeta = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask","arguments":{"template":"t","state":{}},"_meta":{"typryx/key":"%s"}}}`

// @test:TestAcceptKeyInMetaOffKeepsTodaysBehavior
//
// Mutant: "_meta key accepted with the flag off". With AcceptKeyInMeta
// false (the default), a tools/call carrying a valid credential only in
// params._meta, with no header, must be refused exactly as it always was:
// the meta path must never even be consulted.
func TestAcceptKeyInMetaOffKeepsTodaysBehavior(t *testing.T) {
	ts, fake := newMetaTestServer(door.ParseKeys("k1=agent://acme.example/bot"), false)
	defer ts.Close()

	resp := postMCP(t, ts, "", fmt.Sprintf(toolsCallWithMeta, "k1"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 401 with the flag off and no header, got %d: %s", resp.StatusCode, body)
	}
	if fake.agentID != "" || fake.body != nil {
		t.Errorf("the MCP handler must never have been reached, got agentID=%q body=%s", fake.agentID, fake.body)
	}

	// initialize/tools/list must still need the header too, with the flag off.
	resp2 := postMCP(t, ts, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected initialize to still need a credential with the flag off, got %d", resp2.StatusCode)
	}
	resp3 := postMCP(t, ts, "", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected tools/list to still need a credential with the flag off, got %d", resp3.StatusCode)
	}
}

// @test:TestAcceptKeyInMetaInitializeAndToolsListNeedNoCredential
func TestAcceptKeyInMetaInitializeAndToolsListNeedNoCredential(t *testing.T) {
	ts, fake := newMetaTestServer(door.ParseKeys("k1=agent://acme.example/bot"), true)
	defer ts.Close()

	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
	} {
		resp := postMCP(t, ts, "", body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: expected 200 with no credential at all, got %d", body, resp.StatusCode)
		}
		if fake.agentID != "" {
			t.Errorf("%s: expected an empty agent id (no credential was presented), got %q", body, fake.agentID)
		}
	}
}

// @test:TestAcceptKeyInMetaNotificationNeedsNoCredential
func TestAcceptKeyInMetaNotificationNeedsNoCredential(t *testing.T) {
	ts, _ := newMetaTestServer(door.ParseKeys("k1=agent://acme.example/bot"), true)
	defer ts.Close()
	resp := postMCP(t, ts, "", `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// capturingMCP always answers 200; the point under test is that the
		// door let the notification through with no credential at all.
		t.Errorf("expected the fake handler to be reached, got %d", resp.StatusCode)
	}
}

// @test:TestAcceptKeyInMetaOnAuthenticatesToolsCallFromMeta
//
// The agent identity resolved from a meta credential goes through the exact
// same door.Keys.Identity path a header does.
func TestAcceptKeyInMetaOnAuthenticatesToolsCallFromMeta(t *testing.T) {
	ts, fake := newMetaTestServer(door.ParseKeys("k1=agent://acme.example/bot"), true)
	defer ts.Close()

	resp := postMCP(t, ts, "", fmt.Sprintf(toolsCallWithMeta, "k1"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	if fake.agentID != "agent://acme.example/bot" {
		t.Errorf("expected the identity bound to the meta credential, got %q", fake.agentID)
	}
}

// @test:TestAcceptKeyInMetaToolsCallWithNoKeyAtAllIsRefused
//
// Mutant: "tools/call allowed without any key when the flag is on". Neither
// a header nor a meta credential is present; this must still be refused,
// not treated as one more "needs no credential" case.
func TestAcceptKeyInMetaToolsCallWithNoKeyAtAllIsRefused(t *testing.T) {
	ts, fake := newMetaTestServer(door.ParseKeys("k1=agent://acme.example/bot"), true)
	defer ts.Close()

	resp := postMCP(t, ts, "", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask","arguments":{}}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 with neither a header nor a meta credential, got %d", resp.StatusCode)
	}
	if fake.agentID != "" || fake.body != nil {
		t.Errorf("the MCP handler must never have been reached, got agentID=%q body=%s", fake.agentID, fake.body)
	}
}

// @test:TestAcceptKeyInMetaWrongMetaKeyIsRefused
func TestAcceptKeyInMetaWrongMetaKeyIsRefused(t *testing.T) {
	ts, _ := newMetaTestServer(door.ParseKeys("k1=agent://acme.example/bot"), true)
	defer ts.Close()
	resp := postMCP(t, ts, "", fmt.Sprintf(toolsCallWithMeta, "not-a-real-key"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for an unknown meta credential, got %d", resp.StatusCode)
	}
}

// @test:TestAcceptKeyInMetaHeaderWinsOverMetaAndMetaIsStripped
//
// Mutant: "header-vs-meta precedence flipped". Both a header and a (valid,
// but DIFFERENT) meta credential are present; the header's identity must be
// the one used, and the meta value must never reach the handler regardless.
func TestAcceptKeyInMetaHeaderWinsOverMetaAndMetaIsStripped(t *testing.T) {
	keys := door.ParseKeys("k1=agent://acme.example/header-bot,k2=agent://acme.example/meta-bot")
	ts, fake := newMetaTestServer(keys, true)
	defer ts.Close()

	resp := postMCP(t, ts, "k1", fmt.Sprintf(toolsCallWithMeta, "k2"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	if fake.agentID != "agent://acme.example/header-bot" {
		t.Errorf("expected the HEADER's identity to win, got %q", fake.agentID)
	}
	if bytes.Contains(fake.body, []byte("k2")) {
		t.Errorf("the unused meta credential must still be stripped from the forwarded body, got %s", fake.body)
	}
	if bytes.Contains(fake.body, []byte("_meta")) {
		t.Errorf("expected _meta dropped entirely (it carried only the credential), got %s", fake.body)
	}
}

// @test:TestAcceptKeyInMetaMetaKeyNeverReachesTheMCPHandler
//
// Mutant: "_meta key not stripped". This is the direct, structural proof for
// invariant 33 at the one seam that matters: whatever body
// internal/mcp.Server (and everything downstream of it: a log line, the
// journal, the ledger, an error message, a response) ever sees, the meta
// credential's own VALUE must not be in it, used or not.
func TestAcceptKeyInMetaMetaKeyNeverReachesTheMCPHandler(t *testing.T) {
	const marker = "th15-m3ta-cr3d3nt14l-must-nev3r-l1nger"
	keys := door.ParseKeys(marker + "=agent://acme.example/bot")
	ts, fake := newMetaTestServer(keys, true)
	defer ts.Close()

	resp := postMCP(t, ts, "", fmt.Sprintf(toolsCallWithMeta, marker))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	if fake.agentID != "agent://acme.example/bot" {
		t.Fatalf("expected the meta credential's identity to be used when no header is present, got %q", fake.agentID)
	}
	if bytes.Contains(fake.body, []byte(marker)) {
		t.Errorf("the meta credential leaked into the body internal/mcp.Server receives: %s", fake.body)
	}
}

// @test:TestAcceptKeyInMetaOnlyChangesPOST
//
// GET (or any other method) on /mcp must still go through withDoor exactly
// as it always has, flag or no flag: TYPRYX_ACCEPT_KEY_IN_META names a
// tools/call params field, not a way around the method check.
func TestAcceptKeyInMetaOnlyChangesPOST(t *testing.T) {
	ts, _ := newMetaTestServer(door.ParseKeys("k1=agent://acme.example/bot"), true)
	defer ts.Close()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/mcp", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for GET with no credential, got %d", resp.StatusCode)
	}
}

// TestAcceptKeyInMetaOversizedBodyIsBadRequest covers handleMCPRoute's own
// body-read error path (the flag's io.ReadAll, distinct from
// internal/mcp.Server's own MaxBytesReader further down the same request):
// a body over MaxBodyBytes must be refused with 400, never let through to
// the door decision with a half-read body.
func TestAcceptKeyInMetaOversizedBodyIsBadRequest(t *testing.T) {
	ts, fake := newMetaTestServer(door.ParseKeys("k1=agent://acme.example/bot"), true)
	defer ts.Close()

	oversized := strings.Repeat("a", api.MaxBodyBytes+1)
	resp := postMCP(t, ts, "", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"pad":"`+oversized+`"}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400 for a body over MaxBodyBytes, got %d: %s", resp.StatusCode, body)
	}
	if fake.agentID != "" || fake.body != nil {
		t.Errorf("the MCP handler must never have been reached, got agentID=%q body=%s", fake.agentID, fake.body)
	}
}

// @test:TestAcceptKeyInMetaDoesNotAffectV1Routes
//
// Mutant: "/v1/ask accepting a body key". /v1/* never accepts a credential
// from the body, whatever TYPRYX_ACCEPT_KEY_IN_META says: it names one
// field of one method on /mcp alone.
func TestAcceptKeyInMetaDoesNotAffectV1Routes(t *testing.T) {
	ts, _ := newMetaTestServer(door.ParseKeys("k1=agent://acme.example/bot"), true)
	defer ts.Close()

	body := `{"template":"t","state":{},"_meta":{"typryx/key":"k1"}}`
	resp, err := http.Post(ts.URL+"/v1/ask", "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401: a credential inside the JSON body of /v1/ask must never be read, got %d", resp.StatusCode)
	}
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

// @test:TestAskRejectsABadRunID
func TestAskRejectsABadRunID(t *testing.T) {
	ts, _ := newTestServer(t, door.ParseKeys(""))
	cases := map[string]string{
		"too long":                    strings.Repeat("a", 129),
		"a newline":                   "before\nafter",
		"a header-splitter with CRLF": "id\r\nX-Injected: evil",
	}
	for name, runID := range cases {
		t.Run(name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{
				"template": "eval.outcome_met", "state": map[string]any{"task": "t"}, "run_id": runID,
			})
			resp, err := http.Post(ts.URL+"/v1/ask", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400 bad_run_id, got %d", resp.StatusCode)
			}
			var out map[string]string
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				t.Fatalf("decoding error body: %v", err)
			}
			if out["error"] != "bad_run_id" {
				t.Errorf("expected error code bad_run_id, got %q", out["error"])
			}
		})
	}
}

// @test:TestAskAcceptsAWellFormedRunID
func TestAskAcceptsAWellFormedRunID(t *testing.T) {
	ts, _ := newTestServer(t, door.ParseKeys(""))
	body, _ := json.Marshal(map[string]any{
		"template": "eval.outcome_met", "state": map[string]any{"task": "t"}, "run_id": "eval-1234",
	})
	resp, err := http.Post(ts.URL+"/v1/ask", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for a well-formed run_id, got %d", resp.StatusCode)
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
