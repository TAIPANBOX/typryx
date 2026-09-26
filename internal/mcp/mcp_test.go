package mcp_test

import (
	"bytes"
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

	"github.com/TAIPANBOX/typryx/internal/api"
	"github.com/TAIPANBOX/typryx/internal/backend"
	"github.com/TAIPANBOX/typryx/internal/door"
	"github.com/TAIPANBOX/typryx/internal/ledger"
	"github.com/TAIPANBOX/typryx/internal/mcp"
	"github.com/TAIPANBOX/typryx/internal/record"
	"github.com/TAIPANBOX/typryx/internal/service"
	"github.com/TAIPANBOX/typryx/internal/template"
)

func newTestStack(t *testing.T, allowFreeform bool) (*httptest.Server, door.Keys) {
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
	j, err := record.Open("")
	if err != nil {
		t.Fatalf("record.Open: %v", err)
	}

	svc := service.New()
	svc.Templates = reg
	svc.Backend = backend.Stub{}
	svc.Cap = service.NewCap(1000)
	svc.Journal = j
	svc.AllowFreeform = allowFreeform

	keys := door.ParseKeys("k1=agent://acme.example/bot")
	mcpServer := &mcp.Server{Service: svc}
	srv := &api.Server{Keys: keys, Service: svc, MCP: mcpServer}
	ts := httptest.NewServer(api.NewMux(srv))
	t.Cleanup(ts.Close)
	return ts, keys
}

// metaStack is the full, real wiring (journal on disk, ledger on disk, the
// real internal/mcp.Server, the real internal/api door) that the
// TYPRYX_ACCEPT_KEY_IN_META tests below need: proving invariant 33 ("never
// reaches the journal, the ledger, ... a response") needs the real journal
// and ledger, not a fake, or the test would prove nothing about them.
type metaStack struct {
	ts          *httptest.Server
	journalPath string
	ledgerDir   string
}

func newMetaStack(t *testing.T, keys door.Keys, acceptKeyInMeta bool) metaStack {
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
	ledgerDir := filepath.Join(dir, "ledger")
	led, err := ledger.Open(ledgerDir)
	if err != nil {
		t.Fatalf("ledger.Open: %v", err)
	}
	t.Cleanup(func() { led.Close() })

	svc := service.New()
	svc.Templates = reg
	svc.Backend = backend.Stub{}
	svc.Cap = service.NewCap(1000)
	svc.Journal = j
	svc.Ledger = led

	srv := &api.Server{Keys: keys, Service: svc, MCP: &mcp.Server{Service: svc}, AcceptKeyInMeta: acceptKeyInMeta}
	ts := httptest.NewServer(api.NewMux(srv))
	t.Cleanup(ts.Close)
	return metaStack{ts: ts, journalPath: journalPath, ledgerDir: ledgerDir}
}

func postRawMCP(t *testing.T, ts *httptest.Server, headerKey, body string) *http.Response {
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

// @test:TestAcceptKeyInMetaMetaKeyNeverReachesJournalLedgerOrResponse
//
// End-to-end, through the real service, journal and ledger: a marker
// credential presented only in params._meta must answer the call (so the
// credential really was used), name the right agent in the journal, and
// still never appear, anywhere, in the HTTP response body, the journal
// file, or the ledger files. There is no log line to check here: nothing in
// this codebase logs a request body or a credential value at any point in
// this path (checked by reading internal/api, internal/mcp and
// internal/service whole), so that sink is vacuous by construction rather
// than proven by an assertion with nothing to fail against.
func TestAcceptKeyInMetaMetaKeyNeverReachesJournalLedgerOrResponse(t *testing.T) {
	const marker = "th15-m3ta-cr3d3nt14l-must-nev3r-l1nger-anywh3r3"
	keys := door.ParseKeys(marker + "=agent://acme.example/meta-bot")
	st := newMetaStack(t, keys, true)

	body := fmt.Sprintf(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask","arguments":{"template":"eval.outcome_met","state":{"task":"t"}},"_meta":{"typryx/key":"%s"}}}`,
		marker)
	resp := postRawMCP(t, st.ts, "", body)
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, respBody)
	}
	if bytes.Contains(respBody, []byte(marker)) {
		t.Errorf("the meta credential leaked into the HTTP response: %s", respBody)
	}

	var out map[string]any
	if err := json.Unmarshal(respBody, &out); err != nil {
		t.Fatalf("response did not parse: %v (%s)", err, respBody)
	}
	result := out["result"].(map[string]any)
	if result["isError"] == true {
		t.Fatalf("expected a successful answer, got %v", result)
	}

	journalBytes, err := os.ReadFile(st.journalPath)
	if err != nil {
		t.Fatalf("reading the journal: %v", err)
	}
	if !strings.Contains(string(journalBytes), "agent://acme.example/meta-bot") {
		t.Errorf("expected the journal to name the agent the meta credential is bound to, got %s", journalBytes)
	}
	if bytes.Contains(journalBytes, []byte(marker)) {
		t.Errorf("the meta credential leaked into the journal: %s", journalBytes)
	}

	ledgerFiles, _ := filepath.Glob(filepath.Join(st.ledgerDir, "*.ndjson"))
	if len(ledgerFiles) == 0 {
		t.Fatal("expected at least one ledger file, so this measured nothing")
	}
	for _, f := range ledgerFiles {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		if bytes.Contains(b, []byte(marker)) {
			t.Errorf("the meta credential leaked into the ledger file %s: %s", f, b)
		}
	}
}

// @test:TestAcceptKeyInMetaEndToEndInitializeAndToolsListWithNoHeader
//
// The door-level unit tests in internal/api prove the decision; this proves
// the real internal/mcp.Server actually answers a real initialize and a
// real tools/list correctly when no credential was presented at all.
func TestAcceptKeyInMetaEndToEndInitializeAndToolsListWithNoHeader(t *testing.T) {
	st := newMetaStack(t, door.ParseKeys("k1=agent://acme.example/bot"), true)

	resp := postRawMCP(t, st.ts, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	result, ok := out["result"].(map[string]any)
	if !ok || result["protocolVersion"] != mcp.ProtocolVersion {
		t.Fatalf("expected a real initialize result with no credential, got %v", out)
	}

	resp2 := postRawMCP(t, st.ts, "", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	defer resp2.Body.Close()
	var out2 map[string]any
	json.NewDecoder(resp2.Body).Decode(&out2)
	result2, ok := out2["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected a real tools/list result with no credential, got %v", out2)
	}
	tools, _ := result2["tools"].([]any)
	if len(tools) == 0 {
		t.Fatalf("expected a non-empty tool list, got %v", result2)
	}
}

func rpcCall(t *testing.T, ts *httptest.Server, method, key string, params any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewReader(body))
	req.Header.Set(door.KeyHeader, key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return out
}

// @test:TestTheMCPToolAnswersTheSameAsTheHTTPRoute
func TestTheMCPToolAnswersTheSameAsTheHTTPRoute(t *testing.T) {
	ts, _ := newTestStack(t, false)

	httpReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/ask", bytes.NewReader(
		[]byte(`{"template":"eval.outcome_met","state":{"task":"the same question"}}`)))
	httpReq.Header.Set(door.KeyHeader, "k1")
	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("http request: %v", err)
	}
	defer httpResp.Body.Close()
	var httpResult map[string]any
	json.NewDecoder(httpResp.Body).Decode(&httpResult)

	mcpOut := rpcCall(t, ts, "tools/call", "k1", map[string]any{
		"name":      "ask",
		"arguments": map[string]any{"template": "eval.outcome_met", "state": map[string]any{"task": "the same question"}},
	})
	result, ok := mcpOut["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected a result object, got %v", mcpOut)
	}
	structured, ok := result["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("expected structuredContent, got %v", result)
	}

	for _, field := range []string{"template", "template_version", "type", "answer", "backend", "model", "held_back_fields"} {
		if httpResult[field] != structured[field] {
			t.Errorf("field %q differs: http=%v mcp=%v", field, httpResult[field], structured[field])
		}
	}
	httpProbs, _ := httpResult["probabilities"].(map[string]any)
	mcpProbs, _ := structured["probabilities"].(map[string]any)
	if len(httpProbs) != len(mcpProbs) || len(httpProbs) == 0 {
		t.Errorf("probabilities differ in shape: http=%v mcp=%v", httpProbs, mcpProbs)
	}
	for k, v := range httpProbs {
		if mcpProbs[k] != v {
			t.Errorf("probability %q differs: http=%v mcp=%v", k, v, mcpProbs[k])
		}
	}
}

func TestInitializeAndToolsList(t *testing.T) {
	ts, _ := newTestStack(t, false)
	out := rpcCall(t, ts, "initialize", "k1", map[string]any{})
	result, ok := out["result"].(map[string]any)
	if !ok || result["protocolVersion"] != "2025-06-18" {
		t.Fatalf("unexpected initialize result: %v", out)
	}

	out = rpcCall(t, ts, "tools/list", "k1", map[string]any{})
	result = out["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	names := map[string]bool{}
	for _, tl := range tools {
		m := tl.(map[string]any)
		names[m["name"].(string)] = true
	}
	if !names["ask"] || !names["list_questions"] {
		t.Errorf("expected ask and list_questions, got %v", names)
	}
	if names["ask_freeform"] {
		t.Error("ask_freeform must not be listed when freeform is off")
	}
}

func TestAskFreeformToolIsListedOnlyWhenFreeformIsOn(t *testing.T) {
	ts, _ := newTestStack(t, true)
	out := rpcCall(t, ts, "tools/list", "k1", map[string]any{})
	result := out["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	found := false
	for _, tl := range tools {
		if tl.(map[string]any)["name"] == "ask_freeform" {
			found = true
		}
	}
	if !found {
		t.Error("expected ask_freeform to be listed when freeform is on")
	}
}

// @test:TestTheFreeformToolIsListedExactlyWhenTheServiceAllowsIt
//
// mcp.Server used to carry its OWN AllowFreeform flag, separate from
// service.Service.AllowFreeform: the two could disagree, and tools/list
// would then answer a question the service itself would answer differently.
// There must be exactly one source of truth, read live off the service, so
// toggling it on the service (as buildRuntime does once, from config, but
// which a test can do directly) is immediately reflected with no separate
// field to keep in sync.
func TestTheFreeformToolIsListedExactlyWhenTheServiceAllowsIt(t *testing.T) {
	dir := t.TempDir()
	tmpl := template.Template{ID: "t", Type: template.TypeNoul, Instructions: "is it true", Fields: []string{"task"}}
	b, _ := json.Marshal(tmpl)
	os.WriteFile(filepath.Join(dir, "t.json"), b, 0o644)
	reg, _, err := template.LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	j, _ := record.Open("")
	svc := service.New()
	svc.Templates = reg
	svc.Backend = backend.Stub{}
	svc.Cap = service.NewCap(1000)
	svc.Journal = j
	svc.AllowFreeform = false

	keys := door.ParseKeys("k1")
	srv := &api.Server{Keys: keys, Service: svc, MCP: &mcp.Server{Service: svc}}
	ts := httptest.NewServer(api.NewMux(srv))
	t.Cleanup(ts.Close)

	hasFreeform := func() bool {
		out := rpcCall(t, ts, "tools/list", "k1", map[string]any{})
		result := out["result"].(map[string]any)
		tools, _ := result["tools"].([]any)
		for _, tl := range tools {
			if tl.(map[string]any)["name"] == "ask_freeform" {
				return true
			}
		}
		return false
	}

	if hasFreeform() {
		t.Fatal("expected ask_freeform NOT listed while the service has freeform off")
	}
	svc.AllowFreeform = true
	if !hasFreeform() {
		t.Fatal("expected ask_freeform listed the moment the service's own AllowFreeform turned on, with no separate mcp-level flag to update")
	}
}

func TestListQuestionsTool(t *testing.T) {
	ts, _ := newTestStack(t, false)
	out := rpcCall(t, ts, "tools/call", "k1", map[string]any{"name": "list_questions", "arguments": map[string]any{}})
	result := out["result"].(map[string]any)
	structured := result["structuredContent"].(map[string]any)
	templates, _ := structured["templates"].([]any)
	if len(templates) != 1 {
		t.Fatalf("expected 1 template, got %v", templates)
	}
}

func TestUnknownMethodIsAJSONRPCError(t *testing.T) {
	ts, _ := newTestStack(t, false)
	out := rpcCall(t, ts, "not/a/method", "k1", map[string]any{})
	if _, ok := out["error"]; !ok {
		t.Fatalf("expected an error for an unknown method, got %v", out)
	}
}

func TestUnknownToolIsAJSONRPCError(t *testing.T) {
	ts, _ := newTestStack(t, false)
	out := rpcCall(t, ts, "tools/call", "k1", map[string]any{"name": "no-such-tool", "arguments": map[string]any{}})
	if _, ok := out["error"]; !ok {
		t.Fatalf("expected an error for an unknown tool, got %v", out)
	}
}

func TestToolCallRejectsAnUnknownArgument(t *testing.T) {
	ts, _ := newTestStack(t, false)
	out := rpcCall(t, ts, "tools/call", "k1", map[string]any{
		"name":      "ask",
		"arguments": map[string]any{"template": "t", "state": map[string]any{}, "extra_header": "sneaky"},
	})
	if _, ok := out["error"]; !ok {
		t.Fatalf("expected additionalProperties:false to refuse an unknown argument, got %v", out)
	}
}

func TestToolCallRejectsAMissingRequiredArgument(t *testing.T) {
	ts, _ := newTestStack(t, false)
	out := rpcCall(t, ts, "tools/call", "k1", map[string]any{
		"name":      "ask",
		"arguments": map[string]any{"template": "t"},
	})
	if _, ok := out["error"]; !ok {
		t.Fatalf("expected a missing required field (state) to be refused, got %v", out)
	}
}

func TestAskFreeformToolAnswersWhenSwitchedOn(t *testing.T) {
	ts, _ := newTestStack(t, true)
	out := rpcCall(t, ts, "tools/call", "k1", map[string]any{
		"name": "ask_freeform",
		"arguments": map[string]any{
			"type": "noul", "instructions": "is it true", "state": map[string]any{"whatever": "goes"},
		},
	})
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected a result, got %v", out)
	}
	if result["isError"] == true {
		t.Fatalf("expected a successful answer, got %v", result)
	}
	structured := result["structuredContent"].(map[string]any)
	if structured["template"] != "freeform" {
		t.Errorf("expected template freeform, got %v", structured["template"])
	}
}

// @test:TestAskToolRejectsABadRunID
func TestAskToolRejectsABadRunID(t *testing.T) {
	ts, _ := newTestStack(t, false)
	out := rpcCall(t, ts, "tools/call", "k1", map[string]any{
		"name": "ask",
		"arguments": map[string]any{
			"template": "eval.outcome_met", "state": map[string]any{"task": "t"},
			"run_id": "before\nafter",
		},
	})
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected a result, got %v", out)
	}
	if result["isError"] != true {
		t.Fatalf("expected isError true for a control-character run_id, got %v", result)
	}
	structured, ok := result["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("expected structuredContent, got %v", result)
	}
	if structured["error"] != "bad_run_id" {
		t.Errorf("expected error bad_run_id, got %v", structured["error"])
	}
}

// @test:TestAskFreeformToolRejectsABadRunID
func TestAskFreeformToolRejectsABadRunID(t *testing.T) {
	ts, _ := newTestStack(t, true)
	out := rpcCall(t, ts, "tools/call", "k1", map[string]any{
		"name": "ask_freeform",
		"arguments": map[string]any{
			"type": "noul", "instructions": "is it true", "state": map[string]any{"x": 1},
			"run_id": strings.Repeat("a", 129),
		},
	})
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected a result, got %v", out)
	}
	if result["isError"] != true {
		t.Fatalf("expected isError true for an over-length run_id, got %v", result)
	}
}

func TestAskFreeformToolWithABadEnumValueIsRejectedBeforeDispatch(t *testing.T) {
	ts, _ := newTestStack(t, true)
	out := rpcCall(t, ts, "tools/call", "k1", map[string]any{
		"name": "ask_freeform",
		"arguments": map[string]any{
			"type": "essay", "instructions": "x", "state": map[string]any{},
		},
	})
	if _, ok := out["error"]; !ok {
		t.Fatalf("expected the enum check to reject type=essay before it ever reached the service, got %v", out)
	}
}

func TestListQuestionsIncludesChoiceOptionsAndScoreLevels(t *testing.T) {
	dir := t.TempDir()
	choice := template.Template{ID: "c", Type: template.TypeChoice, Instructions: "x",
		Criteria: json.RawMessage(`{"a":"d1","b":"d2"}`), Fields: []string{"x"}}
	score := template.Template{ID: "s", Type: template.TypeScore, Instructions: "y",
		Criteria: json.RawMessage(`["l0","l1"]`), Fields: []string{"x"}}
	cb, _ := json.Marshal(choice)
	sb, _ := json.Marshal(score)
	os.WriteFile(filepath.Join(dir, "c.json"), cb, 0o644)
	os.WriteFile(filepath.Join(dir, "s.json"), sb, 0o644)
	reg, _, err := template.LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	j, _ := record.Open("")
	svc := service.New()
	svc.Templates = reg
	svc.Backend = backend.Stub{}
	svc.Cap = service.NewCap(1000)
	svc.Journal = j
	keys := door.ParseKeys("k1")
	srv := &api.Server{Keys: keys, Service: svc, MCP: &mcp.Server{Service: svc}}
	ts := httptest.NewServer(api.NewMux(srv))
	t.Cleanup(ts.Close)

	out := rpcCall(t, ts, "tools/call", "k1", map[string]any{"name": "list_questions", "arguments": map[string]any{}})
	result := out["result"].(map[string]any)
	structured := result["structuredContent"].(map[string]any)
	templates, _ := structured["templates"].([]any)
	optionsFound, levelsFound := false, false
	for _, tv := range templates {
		m := tv.(map[string]any)
		if opts, ok := m["options"].([]any); ok && len(opts) > 0 {
			optionsFound = true
		}
		if levels, ok := m["levels"].([]any); ok && len(levels) > 0 {
			levelsFound = true
		}
	}
	if !optionsFound {
		t.Error("expected a choice template's options to be listed")
	}
	if !levelsFound {
		t.Error("expected a score template's levels to be listed")
	}
}

func TestAskFreeformRefusedResultIsMarkedIsError(t *testing.T) {
	ts, _ := newTestStack(t, false) // freeform off
	// ask_freeform is not even listed, but tools/call still dispatches by
	// name; the service itself must refuse regardless of what tools/list
	// shows, since a client could call it without listing first.
	out := rpcCall(t, ts, "tools/call", "k1", map[string]any{
		"name": "ask_freeform",
		"arguments": map[string]any{
			"type": "noul", "instructions": "is it true", "state": map[string]any{},
		},
	})
	if _, ok := out["error"]; ok {
		// additionalProperties:false is fine either way here since
		// ask_freeform's schema is not published when off; what matters is
		// this never returns a successful answer.
		return
	}
	result := out["result"].(map[string]any)
	if result["isError"] != true {
		t.Errorf("expected isError true for a refused freeform ask, got %v", result)
	}
}

// TestANotificationIsAnswered202WithNoBody holds MCP 2025-06-18's Streamable
// HTTP transport rule: a JSON-RPC notification (a request object with no
// "id" member) MUST be answered 202 Accepted with no body. "notifications/
// initialized" is the notification a real client actually sends; a second,
// invented method name checks the rule is general (keyed on the absent
// "id", not on that one method name) rather than a special case for it.
func TestANotificationIsAnswered202WithNoBody(t *testing.T) {
	for _, method := range []string{"notifications/initialized", "notifications/some_future_kind"} {
		t.Run(method, func(t *testing.T) {
			ts, _ := newTestStack(t, false)
			body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
			req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewReader(body))
			req.Header.Set(door.KeyHeader, "k1")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusAccepted {
				t.Errorf("expected 202, got %d", resp.StatusCode)
			}
			b, _ := io.ReadAll(resp.Body)
			if len(b) != 0 {
				t.Errorf("expected no body, got %q", b)
			}
		})
	}
}

func TestWrongJSONRPCVersionIsRejected(t *testing.T) {
	ts, _ := newTestStack(t, false)
	body, _ := json.Marshal(map[string]any{"jsonrpc": "1.0", "method": "initialize"})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewReader(body))
	req.Header.Set(door.KeyHeader, "k1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	if _, ok := out["error"]; !ok {
		t.Errorf("expected an error for a non-2.0 jsonrpc version, got %v", out)
	}
}

func TestGetOnMCPRouteIsRejected(t *testing.T) {
	ts, _ := newTestStack(t, false)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/mcp", nil)
	req.Header.Set(door.KeyHeader, "k1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", resp.StatusCode)
	}
}

// @test:TestEveryToolSchemaIsValidJSONSchemaForStrictClients
//
// Found running Claude Code 2.1.270 against typryx (2026-09-25): tools/list
// answered with list_questions's inputSchema carrying `"required":null`,
// because Schema.Required is a nil slice and encoding/json marshals a nil
// slice as JSON null rather than an empty array. Claude Code silently
// dropped typryx's whole tool list rather than reporting an error; the model
// then had no tool to call at all. A proxy that rewrote only that one
// `"required":null` to `"required":[]` made Claude Code call `ask`
// successfully. This checks the shape a strict client actually parses:
// `required` must be a JSON array, never null, for every tool typryx
// publishes, with freeform on and off, and every name it lists must be one
// of the tool's own properties.
func TestEveryToolSchemaIsValidJSONSchemaForStrictClients(t *testing.T) {
	for _, freeform := range []bool{false, true} {
		ts, _ := newTestStack(t, freeform)
		out := rpcCall(t, ts, "tools/list", "k1", nil)
		result, ok := out["result"].(map[string]any)
		if !ok {
			t.Fatalf("freeform=%v: expected a result, got %v", freeform, out)
		}
		tools, ok := result["tools"].([]any)
		if !ok || len(tools) == 0 {
			t.Fatalf("freeform=%v: expected a non-empty tool list, got %v", freeform, result)
		}
		for _, raw := range tools {
			tool := raw.(map[string]any)
			name, _ := tool["name"].(string)
			schema, ok := tool["inputSchema"].(map[string]any)
			if !ok {
				t.Fatalf("freeform=%v, tool %q: inputSchema is not an object: %v", freeform, name, tool["inputSchema"])
			}
			if schema["type"] != "object" {
				t.Errorf("freeform=%v, tool %q: expected type object, got %v", freeform, name, schema["type"])
			}
			props, ok := schema["properties"].(map[string]any)
			if !ok {
				t.Fatalf("freeform=%v, tool %q: properties is not an object: %v", freeform, name, schema["properties"])
			}
			required, ok := schema["required"].([]any)
			if !ok {
				t.Fatalf("freeform=%v, tool %q: required is not a JSON array (got %#v, likely null)", freeform, name, schema["required"])
			}
			for _, r := range required {
				rn, _ := r.(string)
				if _, ok := props[rn]; !ok {
					t.Errorf("freeform=%v, tool %q: required name %q is not in properties", freeform, name, rn)
				}
			}
		}
	}
}

// TestMCPBodyParserNeverPanicsOnHostileInput is a seeded sweep of
// random/mutated JSON-RPC bodies: the server must always answer with SOME
// HTTP response and never crash.
func TestMCPBodyParserNeverPanicsOnHostileInput(t *testing.T) {
	ts, _ := newTestStack(t, false)
	base := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask","arguments":{"template":"eval.outcome_met","state":{"task":"t"}}}}`)
	for seed := int64(0); seed < 200; seed++ {
		body := mutate(base, seed)
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewReader(body))
		req.Header.Set(door.KeyHeader, "k1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("seed %d: request failed (server may have crashed): %v", seed, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 500 {
			t.Errorf("seed %d: got a 5xx (%d)", seed, resp.StatusCode)
		}
	}
}

func mutate(b []byte, seed int64) []byte {
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
