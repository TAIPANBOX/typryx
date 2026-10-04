package wardryxproxy_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TAIPANBOX/agent-stack-go/event"
	"github.com/TAIPANBOX/typryx/internal/backend"
	"github.com/TAIPANBOX/typryx/internal/backend/backendtest"
	"github.com/TAIPANBOX/typryx/internal/door"
	"github.com/TAIPANBOX/typryx/internal/record"
	"github.com/TAIPANBOX/typryx/internal/service"
	"github.com/TAIPANBOX/typryx/internal/template"
	"github.com/TAIPANBOX/typryx/internal/wardryxproxy"
)

// The proxy sits between a caller (the tokenfuse MCP broker) and wardryx. Its
// whole contract is two sentences: forward everything, byte for byte, and on a
// decide request that carries a tool call add one signal to the body when
// typryx answers in time; any failure leaves the body exactly as it came.

const (
	bearerMarker = "Bearer SEKRET-BEARER-MARKER-77"
	argMarker    = "SECRET-ARGUMENT-MARKER-9f3"
	agentID      = "agent://demo.example/proxy"
)

// seen is one request as the fake wardryx received it.
type seen struct {
	Method string
	URI    string
	Header http.Header
	Body   []byte
}

// fakeWardryx records every request and answers with a fixed, odd response, so
// a byte-for-byte pass-through of the response is checkable too.
type fakeWardryx struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []seen
	// override, when set, answers instead of the fixed response.
	override http.HandlerFunc
}

const upstreamBody = "{\n  \"decision\": \"allow\",\n  \"reason\": \"as wardryx said it\"\n}\n"

func newFakeWardryx(t *testing.T) *fakeWardryx {
	t.Helper()
	f := &fakeWardryx{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, seen{Method: r.Method, URI: r.RequestURI, Header: r.Header.Clone(), Body: b})
		ov := f.override
		f.mu.Unlock()
		if ov != nil {
			ov(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Wardryx-Odd", "kept-as-is")
		w.Header().Add("Set-Cookie", "a=1")
		w.Header().Add("Set-Cookie", "b=2")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, upstreamBody)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeWardryx) all() []seen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]seen(nil), f.reqs...)
}

func (f *fakeWardryx) last(t *testing.T) seen {
	t.Helper()
	r := f.all()
	if len(r) == 0 {
		t.Fatal("wardryx received no request")
	}
	return r[len(r)-1]
}

// rig is a proxy in front of a fake wardryx, with a real service behind it.
type rig struct {
	up      *fakeWardryx
	proxy   *httptest.Server
	p       *wardryxproxy.Proxy
	back    *backendtest.Backend
	journal string
	logs    *bytes.Buffer
}

type rigOpts struct {
	back       *backendtest.Backend // nil: the stub backend
	askTimeout time.Duration
	keys       string
	maxCalls   int64
}

func destructive() *backendtest.Backend {
	return &backendtest.Backend{Mode: backendtest.ModeOK, Answer: backend.Answer{
		Model: "bt-1",
		Probabilities: map[string]float64{
			"read_only": 0.02, "reversible_change": 0.02, "destructive": 0.94, "external_send": 0.01, "financial": 0.01,
		},
	}}
}

func newRig(t *testing.T, o rigOpts) *rig {
	t.Helper()
	reg, loadErrs, err := template.LoadDir(filepath.Join("..", "..", "examples", "templates"))
	if err != nil || len(loadErrs) != 0 {
		t.Fatalf("loading the starter templates: %v %v", err, loadErrs)
	}
	journalPath := filepath.Join(t.TempDir(), "events.ndjson")
	j, err := record.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })

	svc := service.New()
	svc.Templates = reg
	if o.back != nil {
		svc.Backend = o.back
	} else {
		svc.Backend = backend.Stub{}
	}
	calls := o.maxCalls
	if calls == 0 {
		calls = 1000
	}
	svc.Cap = service.NewCap(calls)
	svc.Journal = j
	svc.Timeout = 2 * time.Second

	up := newFakeWardryx(t)
	u, _ := url.Parse(up.srv.URL)
	logs := &bytes.Buffer{}
	timeout := o.askTimeout
	if timeout == 0 {
		timeout = time.Second
	}
	p := wardryxproxy.New(wardryxproxy.Config{
		Upstream:   u,
		Service:    svc,
		Template:   wardryxproxy.DefaultTemplate,
		AskTimeout: timeout,
		Keys:       door.ParseKeys(o.keys),
		Log:        slog.New(slog.NewJSONHandler(logs, nil)),
	})
	ps := httptest.NewServer(p)
	t.Cleanup(ps.Close)
	return &rig{up: up, proxy: ps, p: p, back: o.back, journal: journalPath, logs: logs}
}

func (r *rig) do(t *testing.T, method, path string, hdr map[string]string, body []byte) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, r.proxy.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	c := &http.Client{Transport: &http.Transport{DisableCompression: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

// untouchedResponse reports whether resp is exactly what the fake wardryx sent:
// its status, its body, and no header it did not send (Date and the framing
// headers are the HTTP server's own).
func untouchedResponse(resp *http.Response, got []byte) string {
	if resp.StatusCode != http.StatusForbidden || string(got) != upstreamBody {
		return fmt.Sprintf("status %d body %q", resp.StatusCode, got)
	}
	allowed := map[string]bool{"Content-Type": true, "X-Wardryx-Odd": true, "Set-Cookie": true, "Date": true, "Content-Length": true}
	for k := range resp.Header {
		if !allowed[k] {
			return "an extra response header: " + k
		}
	}
	if resp.Header.Get("X-Wardryx-Odd") != "kept-as-is" || len(resp.Header.Values("Set-Cookie")) != 2 {
		return fmt.Sprintf("headers changed: %v", resp.Header)
	}
	return ""
}

func decideBody(extra string) []byte {
	return []byte(`{"agent_id":"agent://acme.example/support/bot1","run_id":"run-1","tool_names":["s3.delete_object"]` + extra + `}`)
}

func toolCall(args string) string {
	return `,"tool_call":{"name":"s3.delete_object","arguments":` + args + `,"target":"s3://prod-backups"}`
}

var decideHdr = map[string]string{"Authorization": bearerMarker, "Content-Type": "application/json"}

// --- pass-through ---

func TestEveryOtherRouteIsForwardedByteForByte(t *testing.T) {
	r := newRig(t, rigOpts{})
	binary := []byte("\x00\x01\xff binary \r\n body \xc3\x28")
	cases := []struct{ method, path string }{
		{"GET", "/healthz"}, {"GET", "/readyz"}, {"GET", "/v1/status?verbose=1&x=%2F"},
		{"POST", "/v1/filter-tools"}, {"POST", "/v1/approvals/ap_1/decide"},
		{"PUT", "/v1/policies/p1"}, {"DELETE", "/v1/policies/p1"}, {"GET", "/v1/policies"},
		{"POST", "/v1/decide/"}, {"GET", "/v1/decide"}, {"POST", "/v1/decide?x=1"},
	}
	hdr := map[string]string{"Authorization": bearerMarker, "X-Fuse-Custom": "kept", "Content-Type": "application/octet-stream", "Accept": "*/*"}
	for _, c := range cases {
		var body []byte
		if c.method != "GET" && c.method != "DELETE" {
			body = binary
		}
		resp, got := r.do(t, c.method, c.path, hdr, body)
		seen := r.up.last(t)
		if seen.Method != c.method || seen.URI != c.path {
			t.Errorf("%s %s reached wardryx as %s %s", c.method, c.path, seen.Method, seen.URI)
		}
		if !bytes.Equal(seen.Body, body) {
			t.Errorf("%s %s: body changed: %q", c.method, c.path, seen.Body)
		}
		for k, v := range hdr {
			if seen.Header.Get(k) != v {
				t.Errorf("%s %s: header %s = %q, want %q", c.method, c.path, k, seen.Header.Get(k), v)
			}
		}
		if seen.Header.Get("X-Forwarded-For") != "" {
			t.Errorf("%s %s: the proxy invented an X-Forwarded-For", c.method, c.path)
		}
		// And the response is wardryx's, untouched.
		if why := untouchedResponse(resp, got); why != "" {
			t.Errorf("%s %s: the response was altered: %s", c.method, c.path, why)
		}
	}
	if n := len(r.up.all()); n != len(cases) {
		t.Errorf("wardryx received %d requests, want %d", n, len(cases))
	}
}

func TestADecideWithoutAToolCallIsForwardedUntouched(t *testing.T) {
	b := destructive()
	r := newRig(t, rigOpts{back: b})
	// Odd spacing and key order on purpose: nothing is re-encoded.
	body := []byte("{ \"run_id\" : \"r\",\n  \"agent_id\":\"agent://x/y\", \"tool_names\": [\"a\"] }")
	resp, got := r.do(t, "POST", "/v1/decide", decideHdr, body)
	if !bytes.Equal(r.up.last(t).Body, body) {
		t.Fatalf("a decide with no tool call was changed: %q", r.up.last(t).Body)
	}
	if string(got) != upstreamBody || resp.StatusCode != 403 {
		t.Fatalf("the response was altered: %d %q", resp.StatusCode, got)
	}
	if len(b.Asked) != 0 {
		t.Fatalf("typryx was asked %d time(s) with no tool call to ask about", len(b.Asked))
	}
}

// --- the one thing it changes ---

func TestADecideWithAToolCallGetsTheSignalAppended(t *testing.T) {
	b := destructive()
	r := newRig(t, rigOpts{back: b})
	body := decideBody(toolCall(`{"bucket":"prod-backups","key":"` + argMarker + `"}`))
	resp, got := r.do(t, "POST", "/v1/decide", decideHdr, body)

	seen := r.up.last(t)
	var out map[string]json.RawMessage
	if err := json.Unmarshal(seen.Body, &out); err != nil {
		t.Fatalf("wardryx was sent something that is not JSON: %v\n%s", err, seen.Body)
	}
	var sigs []map[string]any
	if err := json.Unmarshal(out["signals"], &sigs); err != nil || len(sigs) != 1 {
		t.Fatalf("signals = %s, want exactly one added", out["signals"])
	}
	s := sigs[0]
	if s["name"] != "action.risk_class" || s["value"] != "destructive" || s["probability"] != 0.94 || s["source"] != "typryx" {
		t.Fatalf("the signal is %#v", s)
	}
	if id, _ := s["answer_id"].(string); len(id) < 8 {
		t.Fatalf("no answer id on the signal: %#v", s)
	}
	if len(s) != 5 {
		t.Errorf("the signal has %d keys, want name, value, probability, source, answer_id: %#v", len(s), s)
	}
	// Every other field is exactly what the caller sent.
	var orig map[string]json.RawMessage
	_ = json.Unmarshal(body, &orig)
	for k, v := range orig {
		if string(out[k]) != string(v) {
			t.Errorf("field %q changed: %s -> %s", k, v, out[k])
		}
	}
	if len(out) != len(orig)+1 {
		t.Errorf("wardryx got %d fields, want %d (the caller's plus signals)", len(out), len(orig)+1)
	}
	if seen.Header.Get("Authorization") != bearerMarker {
		t.Errorf("Authorization was not forwarded: %q", seen.Header.Get("Authorization"))
	}
	if seen.Header.Get("Content-Length") != fmt.Sprint(len(seen.Body)) {
		t.Errorf("Content-Length %q does not match the body sent (%d)", seen.Header.Get("Content-Length"), len(seen.Body))
	}
	// The response is wardryx's, untouched.
	if why := untouchedResponse(resp, got); why != "" {
		t.Fatalf("the response was altered: %s", why)
	}
	// Typryx was asked once, with the tool, the arguments and the target.
	if len(b.Asked) != 1 {
		t.Fatalf("typryx asked %d times", len(b.Asked))
	}
	var egress map[string]json.RawMessage
	_ = json.Unmarshal(b.Asked[0].Egress.Canonical(), &egress)
	if len(egress) != 3 || string(egress["tool"]) != `"s3.delete_object"` || string(egress["target"]) != `"s3://prod-backups"` || !bytes.Contains(egress["arguments"], []byte(argMarker)) {
		t.Fatalf("what typryx's backend received: %s", b.Asked[0].Egress.Canonical())
	}
	if st := r.p.Stats(); st.Asked != 1 || st.Signalled != 1 {
		t.Errorf("stats = %+v", st)
	}
}

func TestTheSignalIsAppendedAfterTheCallersOwn(t *testing.T) {
	r := newRig(t, rigOpts{back: destructive()})
	own := `{"name":"mine","value":"v","probability":0.5,"source":"caller"}`
	r.do(t, "POST", "/v1/decide", decideHdr, decideBody(toolCall(`{}`)+`,"signals":[`+own+`]`))
	var out struct{ Signals []map[string]any }
	_ = json.Unmarshal(r.up.last(t).Body, &out)
	if len(out.Signals) != 2 || out.Signals[0]["source"] != "caller" || out.Signals[1]["source"] != "typryx" {
		t.Fatalf("signals = %#v, want the caller's, then typryx's", out.Signals)
	}
	// An explicit null counts as none.
	r.do(t, "POST", "/v1/decide", decideHdr, decideBody(toolCall(`{}`)+`,"signals":null`))
	_ = json.Unmarshal(r.up.last(t).Body, &out)
	if len(out.Signals) != 1 || out.Signals[0]["source"] != "typryx" {
		t.Fatalf("signals after null = %#v", out.Signals)
	}
}

func TestACallWithNoArgumentsIsAskedWithAnEmptyObject(t *testing.T) {
	b := destructive()
	r := newRig(t, rigOpts{back: b})
	r.do(t, "POST", "/v1/decide", decideHdr, decideBody(`,"tool_call":{"name":"list_files"}`))
	if len(b.Asked) != 1 || !strings.Contains(string(b.Asked[0].Egress.Canonical()), `"arguments":{}`) {
		t.Fatalf("asked %d, egress %v", len(b.Asked), b.Asked)
	}
}

// Anything that stops the ask from giving a clean answer in time leaves the
// body byte for byte what the caller sent, and the call goes on.
func TestAnythingButACleanAnswerLeavesTheBodyUntouched(t *testing.T) {
	huge := `{"x":"` + strings.Repeat("a", 20000) + `"}` // over the template's 16 KiB state
	cases := []struct {
		name string
		opts func() rigOpts
		body []byte
	}{
		{"the backend fails", func() rigOpts { return rigOpts{back: &backendtest.Backend{Mode: backendtest.ModeFail}} }, decideBody(toolCall(`{}`))},
		{"the backend returns nothing", func() rigOpts { return rigOpts{back: &backendtest.Backend{Mode: backendtest.ModeEmpty}} }, decideBody(toolCall(`{}`))},
		{"the backend outlasts the ask timeout", func() rigOpts {
			return rigOpts{back: &backendtest.Backend{Mode: backendtest.ModeHang}, askTimeout: 50 * time.Millisecond}
		}, decideBody(toolCall(`{}`))},
		{"the hourly cap is spent", func() rigOpts { return rigOpts{back: destructive(), maxCalls: 1} }, decideBody(toolCall(`{}`))},
		{"the state is over the template's bound", func() rigOpts { return rigOpts{back: destructive()} }, decideBody(toolCall(huge))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, c.opts())
			if c.name == "the hourly cap is spent" {
				r.do(t, "POST", "/v1/decide", decideHdr, c.body) // takes the one call
			}
			start := time.Now()
			resp, got := r.do(t, "POST", "/v1/decide", decideHdr, c.body)
			if time.Since(start) > 2*time.Second {
				t.Fatalf("the call took %s", time.Since(start))
			}
			if !bytes.Equal(r.up.last(t).Body, c.body) {
				t.Fatalf("the body was changed:\n got %s\nwant %s", r.up.last(t).Body, c.body)
			}
			if resp.StatusCode != 403 || string(got) != upstreamBody {
				t.Fatalf("the response was altered: %d %q", resp.StatusCode, got)
			}
		})
	}
}

func TestTruncatedArgumentsAreNeverAskedAbout(t *testing.T) {
	b := destructive()
	r := newRig(t, rigOpts{back: b})
	for _, tc := range []string{
		`,"tool_call":{"name":"t","arguments_truncated":true}`,
		`,"tool_call":{"name":"t","arguments":null,"arguments_truncated":true,"target":"x"}`,
		`,"tool_call":{"name":"t","arguments":{"a":1},"arguments_truncated":true}`,
		`,"tool_call":{"name":"t","arguments_truncated":"true"}`,
		`,"tool_call":{"name":"t","arguments_truncated":1}`,
	} {
		body := decideBody(tc)
		r.do(t, "POST", "/v1/decide", decideHdr, body)
		if !bytes.Equal(r.up.last(t).Body, body) {
			t.Errorf("a truncated call was changed: %s", r.up.last(t).Body)
		}
	}
	if len(b.Asked) != 0 {
		t.Fatalf("typryx was asked %d time(s) about calls whose arguments were never sent", len(b.Asked))
	}
	// The control: an explicit false is asked about.
	r.do(t, "POST", "/v1/decide", decideHdr, decideBody(`,"tool_call":{"name":"t","arguments":{},"arguments_truncated":false}`))
	if len(b.Asked) != 1 {
		t.Fatalf("arguments_truncated:false was asked %d times, want 1", len(b.Asked))
	}
}

func TestShapesThatAreNotATypicalToolCallAreForwardedUntouched(t *testing.T) {
	b := destructive()
	r := newRig(t, rigOpts{back: b})
	full := make([]string, 0, wardryxproxy.MaxSignals)
	for i := 0; i < wardryxproxy.MaxSignals; i++ {
		full = append(full, `{"name":"n","value":"v","probability":0.1,"source":"s"}`)
	}
	for name, extra := range map[string]string{
		"a tool call that is a string": `,"tool_call":"s3.delete_object"`,
		"a tool call that is a list":   `,"tool_call":[]`,
		"a tool call that is null":     `,"tool_call":null`,
		"no name":                      `,"tool_call":{"arguments":{}}`,
		"a blank name":                 `,"tool_call":{"name":"  "}`,
		"a numeric name":               `,"tool_call":{"name":7}`,
		"a target that is a number":    `,"tool_call":{"name":"t","target":7}`,
		"signals that are an object":   toolCall(`{}`) + `,"signals":{"a":1}`,
		"signals that are a string":    toolCall(`{}`) + `,"signals":"x"`,
		"sixteen signals already":      toolCall(`{}`) + `,"signals":[` + strings.Join(full, ",") + `]`,
	} {
		body := decideBody(extra)
		r.do(t, "POST", "/v1/decide", decideHdr, body)
		if !bytes.Equal(r.up.last(t).Body, body) {
			t.Errorf("%s: the body was changed: %s", name, r.up.last(t).Body)
		}
	}
	if len(b.Asked) != 0 {
		t.Fatalf("typryx was asked %d time(s) about a shape it should not have asked about", len(b.Asked))
	}
}

// --- hostile bodies ---

func TestHostileBodiesAreForwardedNotCrashedOn(t *testing.T) {
	r := newRig(t, rigOpts{back: destructive()})
	bodies := [][]byte{
		nil, []byte(""), []byte("null"), []byte("[]"), []byte(`"x"`), []byte("{"), []byte(`{"tool_call":`),
		[]byte("\xff\xfe\x00"), []byte(strings.Repeat("[", 5000)), []byte(strings.Repeat(`{"a":`, 3000)),
		[]byte(`{"tool_call":{"name":"t"},"tool_call":{"name":"u"}}`),
	}
	for i, b := range bodies {
		resp, got := r.do(t, "POST", "/v1/decide", decideHdr, b)
		if resp.StatusCode != 403 || string(got) != upstreamBody {
			t.Errorf("body %d: the response was %d %q", i, resp.StatusCode, got)
		}
	}
	for i, b := range bodies[:10] {
		if !bytes.Equal(r.up.all()[i].Body, b) {
			t.Errorf("body %d was changed on the way: %q", i, r.up.all()[i].Body)
		}
	}
}

// A body over the bound is neither buffered whole nor refused here: it goes to
// wardryx unchanged and wardryx says what it says (a 413).
func TestABodyOverTheBoundIsStreamedThroughUnchanged(t *testing.T) {
	b := destructive()
	r := newRig(t, rigOpts{back: b})
	body := decideBody(toolCall(`{"x":"` + strings.Repeat("a", wardryxproxy.MaxBodyBytes+100) + `"}`))
	resp, got := r.do(t, "POST", "/v1/decide", decideHdr, body)
	if !bytes.Equal(r.up.last(t).Body, body) {
		t.Fatalf("an oversize body was changed (%d bytes arrived of %d)", len(r.up.last(t).Body), len(body))
	}
	if resp.StatusCode != 403 || string(got) != upstreamBody {
		t.Fatalf("the response was altered: %d", resp.StatusCode)
	}
	if len(b.Asked) != 0 {
		t.Fatal("an oversize body was asked about")
	}
}

// 200 seeded mutations of a valid body: whatever reaches wardryx is the
// original bytes, or the original object plus exactly one more signal.
func TestMutatedBodiesNeverComeOutAsAnythingButTheOriginalOrOneMoreSignal(t *testing.T) {
	r := newRig(t, rigOpts{back: destructive()})
	seedBody := decideBody(toolCall(`{"a":[1,2,{"b":null}]}`) + `,"signals":[{"name":"n","value":"v","probability":0.2,"source":"s"}]`)
	for seed := int64(1); seed <= 200; seed++ {
		rng := rand.New(rand.NewSource(seed))
		b := append([]byte(nil), seedBody...)
		for n := 1 + rng.Intn(4); n > 0 && len(b) > 0; n-- {
			switch rng.Intn(3) {
			case 0:
				b[rng.Intn(len(b))] = byte(rng.Intn(256))
			case 1:
				i := rng.Intn(len(b))
				b = append(b[:i], b[i+1:]...)
			default:
				i := rng.Intn(len(b))
				b = append(b[:i], append([]byte{byte(rng.Intn(256))}, b[i:]...)...)
			}
		}
		before := len(r.up.all())
		resp, _ := r.do(t, "POST", "/v1/decide", decideHdr, b)
		if resp.StatusCode != 403 {
			t.Fatalf("seed %d: status %d", seed, resp.StatusCode)
		}
		reqs := r.up.all()
		if len(reqs) != before+1 {
			t.Fatalf("seed %d: wardryx saw %d new requests", seed, len(reqs)-before)
		}
		got := reqs[len(reqs)-1].Body
		if bytes.Equal(got, b) {
			continue
		}
		var orig, out map[string]json.RawMessage
		if json.Unmarshal(b, &orig) != nil || json.Unmarshal(got, &out) != nil || orig == nil {
			t.Fatalf("seed %d: the body changed but the original was not an object we could extend:\n%q\n%q", seed, b, got)
		}
		var os0, os1 []json.RawMessage
		_ = json.Unmarshal(orig["signals"], &os0)
		if err := json.Unmarshal(out["signals"], &os1); err != nil || len(os1) != len(os0)+1 {
			t.Fatalf("seed %d: signals went from %d to %d entries", seed, len(os0), len(os1))
		}
		for k, v := range orig {
			if k != "signals" && string(out[k]) != string(v) {
				t.Fatalf("seed %d: field %q changed", seed, k)
			}
		}
	}
}

// --- credentials and the record ---

func TestAuthorizationIsForwardedAndNeverLoggedOrRecorded(t *testing.T) {
	r := newRig(t, rigOpts{back: destructive(), keys: "k-proxy=" + agentID})
	hdr := map[string]string{"Authorization": bearerMarker, "X-Typryx-Key": "k-proxy"}
	r.do(t, "POST", "/v1/decide", hdr, decideBody(toolCall(`{"key":"`+argMarker+`"}`)))
	r.do(t, "GET", "/v1/status", hdr, nil)

	for _, s := range r.up.all() {
		if s.Header.Get("Authorization") != bearerMarker {
			t.Errorf("Authorization not forwarded on %s: %q", s.URI, s.Header.Get("Authorization"))
		}
		if s.Header.Get("X-Typryx-Key") != "" {
			t.Errorf("the typryx credential was forwarded to wardryx on %s", s.URI)
		}
	}
	journal, _ := os.ReadFile(r.journal)
	for name, text := range map[string]string{"log": r.logs.String(), "journal": string(journal)} {
		if strings.Contains(text, "SEKRET-BEARER-MARKER-77") || strings.Contains(text, "k-proxy") {
			t.Errorf("a credential reached the %s:\n%s", name, text)
		}
		if strings.Contains(text, argMarker) {
			t.Errorf("the tool arguments reached the %s", name)
		}
	}
}

func TestTheAskIsOnTheRecordUnderTheCredentialsAgentWithoutTheArguments(t *testing.T) {
	r := newRig(t, rigOpts{back: destructive(), keys: "k-proxy=" + agentID})
	r.do(t, "POST", "/v1/decide", map[string]string{"X-Typryx-Key": "k-proxy"}, decideBody(toolCall(`{"key":"`+argMarker+`"}`)))
	events, err := event.ReadFile(r.journal)
	if err != nil || len(events) != 1 {
		t.Fatalf("journal: %v, %d events", err, len(events))
	}
	ev := events[0]
	if ev.Type != record.TypeAnswer || ev.AgentID != agentID {
		t.Fatalf("event = %s for %s", ev.Type, ev.AgentID)
	}
	raw, _ := json.Marshal(ev.Data)
	if bytes.Contains(raw, []byte(argMarker)) || !bytes.Contains(raw, []byte("sha384")) && !bytes.Contains(raw, []byte("egress")) {
		t.Fatalf("record data = %s: want a hash of what was sent and never the arguments", raw)
	}
}

func TestACallerWithoutTheTyprxCredentialIsRefusedWhenKeysAreConfigured(t *testing.T) {
	b := destructive()
	r := newRig(t, rigOpts{back: b, keys: "k-proxy=" + agentID})
	for _, hdr := range []map[string]string{nil, {"X-Typryx-Key": "wrong"}} {
		resp, _ := r.do(t, "POST", "/v1/decide", hdr, decideBody(toolCall(`{}`)))
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status %d, want 401", resp.StatusCode)
		}
	}
	if len(r.up.all()) != 0 || len(b.Asked) != 0 {
		t.Fatalf("a refused caller reached wardryx (%d) or the backend (%d)", len(r.up.all()), len(b.Asked))
	}
}

// --- the upstream ---

func TestWardryxBeingDownIsA502NotAnAnswer(t *testing.T) {
	r := newRig(t, rigOpts{back: destructive()})
	r.up.srv.Close()
	for _, c := range []struct{ method, path string }{{"POST", "/v1/decide"}, {"GET", "/healthz"}} {
		resp, got := r.do(t, c.method, c.path, decideHdr, decideBody(toolCall(`{}`)))
		if resp.StatusCode != http.StatusBadGateway {
			t.Errorf("%s %s: status %d, want 502", c.method, c.path, resp.StatusCode)
		}
		if strings.Contains(string(got), "127.0.0.1") || strings.Contains(string(got), "refused") {
			t.Errorf("the 502 body leaks the upstream's address or the dial error: %s", got)
		}
	}
	if strings.Contains(r.logs.String(), "SEKRET-BEARER-MARKER-77") {
		t.Error("the bearer reached the log on the failure path")
	}
}

// A redirect is wardryx's answer, passed on: the proxy dials the one upstream
// it was told and nothing else.
func TestARedirectIsPassedOnAndNothingElseIsDialled(t *testing.T) {
	var hits int
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer other.Close()
	r := newRig(t, rigOpts{})
	r.up.mu.Lock()
	r.up.override = func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, other.URL, http.StatusTemporaryRedirect)
	}
	r.up.mu.Unlock()
	resp, _ := r.do(t, "GET", "/v1/status", decideHdr, nil)
	if resp.StatusCode != http.StatusTemporaryRedirect || resp.Header.Get("Location") != other.URL {
		t.Fatalf("the redirect was not passed on: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if hits != 0 {
		t.Fatalf("the redirect target was dialled %d time(s)", hits)
	}
}
