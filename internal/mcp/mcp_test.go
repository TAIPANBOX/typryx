package mcp_test

import (
	"bytes"
	"encoding/json"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/TAIPANBOX/typryx/internal/api"
	"github.com/TAIPANBOX/typryx/internal/backend"
	"github.com/TAIPANBOX/typryx/internal/door"
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
	mcpServer := &mcp.Server{Service: svc, AllowFreeform: allowFreeform, RunID: "test-run"}
	srv := &api.Server{Keys: keys, Service: svc, MCP: mcpServer}
	ts := httptest.NewServer(api.NewMux(srv))
	t.Cleanup(ts.Close)
	return ts, keys
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

func TestNotificationsInitializedReturnsNoContent(t *testing.T) {
	ts, _ := newTestStack(t, false)
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewReader(body))
	req.Header.Set(door.KeyHeader, "k1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", resp.StatusCode)
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
