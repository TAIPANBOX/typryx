package mcp

import (
	"encoding/json"
	"math/rand"
	"testing"
)

func TestNeedsNoCredentialForInitializeToolsListAndNotifications(t *testing.T) {
	cases := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":"abc","method":"tools/list"}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","method":"notifications/some_future_kind"}`,
		// "initialize" and "tools/list" are exempted by METHOD, regardless
		// of what their own "id" carries: unlike a notification, whether
		// they need no credential does not depend on "id" being absent.
		`{"jsonrpc":"2.0","id":null,"method":"initialize"}`,
	}
	for _, c := range cases {
		if !NeedsNoCredential([]byte(c)) {
			t.Errorf("expected no credential needed for %s", c)
		}
	}
}

func TestNeedsNoCredentialIsFalseForToolsCallAndUnknownMethods(t *testing.T) {
	cases := []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask"}}`,
		`{"jsonrpc":"2.0","id":1,"method":"not/a/method"}`,
		// An explicit "id":null is a present (if poorly formed) id, not an
		// absent one, so this is not a notification either: the same rule
		// internal/mcp's own ServeMCP already holds for the 202 answer.
		`{"jsonrpc":"2.0","id":null,"method":"not/a/method"}`,
	}
	for _, c := range cases {
		if NeedsNoCredential([]byte(c)) {
			t.Errorf("expected a credential still needed for %s", c)
		}
	}
}

func TestNeedsNoCredentialFailsClosedOnUnparseableBody(t *testing.T) {
	cases := []string{``, `not json`, `{"jsonrpc":`, `[1,2,3]`, `"just a string"`}
	for _, c := range cases {
		if NeedsNoCredential([]byte(c)) {
			t.Errorf("expected a credential still needed for unparseable body %q", c)
		}
	}
}

func TestExtractMetaKeyStripsOnlyTheNamedEntry(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask","arguments":{"template":"t","state":{}},"_meta":{"typryx/key":"k1","other":"keep-me"}}}`)
	key, stripped, ok := ExtractMetaKey(body)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if key != "k1" {
		t.Errorf("expected key k1, got %q", key)
	}
	var out map[string]any
	if err := json.Unmarshal(stripped, &out); err != nil {
		t.Fatalf("stripped body did not parse: %v", err)
	}
	params := out["params"].(map[string]any)
	meta, ok := params["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("expected _meta to survive with its other field, got %v", params)
	}
	if _, has := meta[MetaKeyName]; has {
		t.Errorf("expected %s removed from _meta, got %v", MetaKeyName, meta)
	}
	if meta["other"] != "keep-me" {
		t.Errorf("expected the other _meta field untouched, got %v", meta)
	}
	// Everything else survives untouched.
	if params["name"] != "ask" {
		t.Errorf("expected name preserved, got %v", params["name"])
	}
	args, _ := params["arguments"].(map[string]any)
	if args["template"] != "t" {
		t.Errorf("expected arguments preserved, got %v", params["arguments"])
	}
	if out["jsonrpc"] != "2.0" || out["id"] != float64(1) || out["method"] != "tools/call" {
		t.Errorf("expected jsonrpc/id/method untouched, got %v", out)
	}
}

func TestExtractMetaKeyDropsMetaEntirelyWhenItWasTheOnlyField(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask","_meta":{"typryx/key":"k1"}}}`)
	_, stripped, ok := ExtractMetaKey(body)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	var out map[string]any
	json.Unmarshal(stripped, &out)
	params := out["params"].(map[string]any)
	if _, has := params["_meta"]; has {
		t.Errorf("expected _meta dropped entirely once its only entry was removed, got %v", params)
	}
}

func TestExtractMetaKeyIsANoopWhenAbsentOrWrongMethodOrWrongType(t *testing.T) {
	cases := map[string]string{
		"not a tools/call":          `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"_meta":{"typryx/key":"k1"}}}`,
		"no params":                 `{"jsonrpc":"2.0","id":1,"method":"tools/call"}`,
		"no _meta":                  `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask"}}`,
		"_meta with no key entry":   `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":{"other":"x"}}}`,
		"key entry is not a string": `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":{"typryx/key":123}}}`,
		"params is not an object":   `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":[1,2,3]}`,
		"unparseable body":          `not json`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			key, stripped, ok := ExtractMetaKey([]byte(body))
			if ok {
				t.Errorf("expected ok=false, got key=%q stripped=%s", key, stripped)
			}
			if key != "" {
				t.Errorf("expected an empty key, got %q", key)
			}
			if string(stripped) != body {
				t.Errorf("expected the body returned completely unchanged, got %s", stripped)
			}
		})
	}
}

// TestExtractMetaKeyNeverPanicsOnHostileBody is the hostile-input sweep
// (T3): random/mutated bytes derived from a real tools/call carrying a meta
// credential must never panic this function, whatever nonsense they become.
func TestExtractMetaKeyNeverPanicsOnHostileBody(t *testing.T) {
	base := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask","arguments":{"template":"t","state":{"x":1}},"_meta":{"typryx/key":"k1"}}}`)
	for seed := int64(0); seed < 200; seed++ {
		body := mutateBytes(base, seed)
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("seed %d: ExtractMetaKey panicked: %v (input %s)", seed, r, body)
				}
			}()
			ExtractMetaKey(body)
		}()
	}
}

func TestNeedsNoCredentialNeverPanicsOnHostileBody(t *testing.T) {
	base := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	for seed := int64(0); seed < 200; seed++ {
		body := mutateBytes(base, seed)
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("seed %d: NeedsNoCredential panicked: %v (input %s)", seed, r, body)
				}
			}()
			NeedsNoCredential(body)
		}()
	}
}

func mutateBytes(b []byte, seed int64) []byte {
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
