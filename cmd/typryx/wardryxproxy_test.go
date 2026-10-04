package main

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// `typryx wardryx-proxy` is the same typryx behind a reverse proxy: the same
// backend, templates, caps and credentials, validated the same way, plus an
// upstream, an ask timeout and a template of its own.

const starterTemplates = "../../examples/templates"

func proxyEnv(t *testing.T, extra map[string]string) {
	t.Helper()
	clearTyprxEnv(t)
	env := map[string]string{
		"TYPRYX_BACKEND":        "stub",
		"TYPRYX_TEMPLATES":      starterTemplates,
		"TYPRYX_PROXY_UPSTREAM": "http://wardryx:8090",
	}
	for k, v := range extra {
		env[k] = v
	}
	setEnv(t, env)
}

func TestTheProxyStartsOnLoopbackWithItsDefaults(t *testing.T) {
	proxyEnv(t, nil)
	pc, err := loadProxyConfig()
	if err != nil {
		t.Fatalf("a minimal configuration was refused: %v", err)
	}
	if pc.addr != "127.0.0.1:4330" || pc.askTimeout != 150*time.Millisecond || pc.template != "action.risk_class" || pc.upstream.Host != "wardryx:8090" {
		t.Fatalf("defaults: addr %q, timeout %s, template %q, upstream %s", pc.addr, pc.askTimeout, pc.template, pc.upstream)
	}
	if _, err := buildProxyRuntime(pc, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("buildProxyRuntime: %v", err)
	}
}

func TestAnUnusableProxySettingRefusesToStartNamingTheVariable(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"no upstream", map[string]string{"TYPRYX_PROXY_UPSTREAM": ""}, "TYPRYX_PROXY_UPSTREAM"},
		{"an upstream with no scheme", map[string]string{"TYPRYX_PROXY_UPSTREAM": "wardryx:8090"}, "TYPRYX_PROXY_UPSTREAM"},
		{"an upstream with userinfo", map[string]string{"TYPRYX_PROXY_UPSTREAM": "http://u:p@wardryx"}, "TYPRYX_PROXY_UPSTREAM"},
		{"an upstream with a query", map[string]string{"TYPRYX_PROXY_UPSTREAM": "http://wardryx?x=1"}, "TYPRYX_PROXY_UPSTREAM"},
		{"a timeout of zero", map[string]string{"TYPRYX_PROXY_ASK_TIMEOUT_MS": "0"}, "TYPRYX_PROXY_ASK_TIMEOUT_MS"},
		{"a negative timeout", map[string]string{"TYPRYX_PROXY_ASK_TIMEOUT_MS": "-1"}, "TYPRYX_PROXY_ASK_TIMEOUT_MS"},
		{"a timeout over the ceiling", map[string]string{"TYPRYX_PROXY_ASK_TIMEOUT_MS": "5001"}, "TYPRYX_PROXY_ASK_TIMEOUT_MS"},
		{"a timeout that is not a number", map[string]string{"TYPRYX_PROXY_ASK_TIMEOUT_MS": "soon"}, "TYPRYX_PROXY_ASK_TIMEOUT_MS"},
		{"a template that does not exist", map[string]string{"TYPRYX_PROXY_TEMPLATE": "nope"}, "TYPRYX_PROXY_TEMPLATE"},
		{"a template that is not a choice", map[string]string{"TYPRYX_PROXY_TEMPLATE": "eval.answer_quality"}, "TYPRYX_PROXY_TEMPLATE"},
		{"a template that names a field a tool call does not carry", map[string]string{"TYPRYX_PROXY_TEMPLATE": "request.complexity"}, "TYPRYX_PROXY_TEMPLATE"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			proxyEnv(t, c.env)
			if _, ok := c.env["TYPRYX_PROXY_UPSTREAM"]; ok && c.env["TYPRYX_PROXY_UPSTREAM"] == "" {
				t.Setenv("TYPRYX_PROXY_UPSTREAM", "")
			}
			_, err := loadProxyConfig()
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("the refusal does not name %s: %v", c.want, err)
			}
			var ce *configError
			if !isConfigError(err, &ce) {
				t.Errorf("not a configError (exit 2): %v", err)
			}
		})
	}
}

func TestTheProxyRefusesAWideBindWithNoCredentialLikeTheServiceDoes(t *testing.T) {
	proxyEnv(t, map[string]string{"TYPRYX_PROXY_ADDR": "0.0.0.0:4330"})
	_, err := loadProxyConfig()
	if err == nil || !strings.Contains(err.Error(), "TYPRYX_PROXY_ADDR") || !strings.Contains(err.Error(), "TYPRYX_KEYS") {
		t.Fatalf("a wide bind with no credential was not refused by name: %v", err)
	}
	var ce *configError
	if isConfigError(err, &ce) {
		t.Error("an open-bind refusal is exit 1, not a configError (exit 2), as for the service")
	}
	proxyEnv(t, map[string]string{"TYPRYX_PROXY_ADDR": "0.0.0.0:4330", "TYPRYX_KEYS": "k=agent://demo.example/proxy"})
	if _, err := loadProxyConfig(); err != nil {
		t.Fatalf("a wide bind with a credential was refused: %v", err)
	}
	proxyEnv(t, map[string]string{"TYPRYX_PROXY_ADDR": "0.0.0.0:4330", "TYPRYX_ALLOW_OPEN_BIND": "1"})
	if _, err := loadProxyConfig(); err != nil {
		t.Fatalf("a wide bind the operator deliberately opened was refused: %v", err)
	}
}

// The service's own address variable is not the proxy's: setting only
// TYPRYX_ADDR to something wide must not make the proxy bind there.
func TestTheProxyReadsItsOwnAddressNotTheServices(t *testing.T) {
	proxyEnv(t, map[string]string{"TYPRYX_ADDR": "0.0.0.0:4320"})
	pc, err := loadProxyConfig()
	if err != nil {
		t.Fatal(err)
	}
	if pc.addr != "127.0.0.1:4330" {
		t.Fatalf("the proxy bound %s, want its own default", pc.addr)
	}
}
