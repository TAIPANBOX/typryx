package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/TAIPANBOX/typryx/internal/door"
	"github.com/TAIPANBOX/typryx/internal/template"
	"github.com/TAIPANBOX/typryx/internal/wardryxproxy"
)

const (
	defaultProxyAddr         = "127.0.0.1:4330"
	defaultProxyAskTimeoutMS = 150
	maxProxyAskTimeoutMS     = 5000
)

// proxyConfig is the service configuration (the same backend, templates, caps,
// journal and credentials as `typryx serve`, validated the same way) plus the
// four settings only the proxy has.
type proxyConfig struct {
	*config
	upstream   *url.URL
	askTimeout time.Duration
	template   string
}

// proxyRefuseOpenBind is door.RefuseOpenBind for the proxy: the same rule, named
// for the proxy's own address variable. A wide bind with no credential would let
// anything that reaches it spend this deployment's ask budget.
func proxyRefuseOpenBind(addr string, keys door.Keys, allowOpenBind bool) string {
	if keys.Configured() || allowOpenBind || door.IsLoopback(addr) {
		return ""
	}
	return fmt.Sprintf(
		"refusing to start: the wardryx-proxy is bound to %s, which is not loopback, and no client "+
			"credentials are configured (TYPRYX_KEYS is unset). Anything that reaches this address could "+
			"make this deployment ask typed questions under nobody's name. Set TYPRYX_KEYS=\"key1,key2\" to "+
			"require an X-Typryx-Key header, bind to loopback instead (TYPRYX_PROXY_ADDR=127.0.0.1:4330, the "+
			"default), or, if you have deliberately decided to run it open, set TYPRYX_ALLOW_OPEN_BIND=1.", addr)
}

// proxyFields are the only state fields the proxy can fill: a tool call carries
// a tool, its arguments and a target, and nothing else.
var proxyFields = map[string]bool{"tool": true, "arguments": true, "target": true}

func loadProxyConfig() (*proxyConfig, error) {
	cfg, err := loadConfigAt("TYPRYX_PROXY_ADDR", defaultProxyAddr, proxyRefuseOpenBind)
	if err != nil {
		return nil, err
	}
	raw := os.Getenv("TYPRYX_PROXY_UPSTREAM")
	if raw == "" {
		return nil, missingVar("TYPRYX_PROXY_UPSTREAM", "set it to wardryx's base URL, e.g. http://wardryx:8090. The proxy forwards every request there.")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, badVar("TYPRYX_PROXY_UPSTREAM", raw, "must be an absolute http or https URL with a host")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, badVar("TYPRYX_PROXY_UPSTREAM", raw, "must not carry userinfo, a query or a fragment")
	}
	ms, err := envInt("TYPRYX_PROXY_ASK_TIMEOUT_MS", defaultProxyAskTimeoutMS)
	if err != nil {
		return nil, err
	}
	if ms <= 0 || ms > maxProxyAskTimeoutMS {
		return nil, badVar("TYPRYX_PROXY_ASK_TIMEOUT_MS", strconv.FormatInt(ms, 10),
			fmt.Sprintf("must be a whole number of milliseconds from 1 to %d: it is the most a slow typryx may add to every decision", maxProxyAskTimeoutMS))
	}
	name := envOr("TYPRYX_PROXY_TEMPLATE", wardryxproxy.DefaultTemplate)
	t, ok := cfg.templates.Get(name)
	if !ok {
		return nil, badVar("TYPRYX_PROXY_TEMPLATE", name, "no such template in TYPRYX_TEMPLATES (the starter catalog ships action.risk_class)")
	}
	if t.Type != template.TypeChoice {
		return nil, badVar("TYPRYX_PROXY_TEMPLATE", name, "must be a choice template: a risk class is one of several options")
	}
	for _, f := range t.Fields {
		if !proxyFields[f] {
			return nil, badVar("TYPRYX_PROXY_TEMPLATE", name,
				fmt.Sprintf("names the field %q, and a tool call carries only tool, arguments and target", f))
		}
	}
	return &proxyConfig{config: cfg, upstream: u, askTimeout: time.Duration(ms) * time.Millisecond, template: name}, nil
}

// buildProxyRuntime wires the same service `typryx serve` runs behind a reverse
// proxy to wardryx. It never opens a socket.
func buildProxyRuntime(pc *proxyConfig, log *slog.Logger) (*runtime, error) {
	svc, rt, err := buildService(pc.config, log)
	if err != nil {
		return nil, err
	}
	p := wardryxproxy.New(wardryxproxy.Config{
		Upstream:   pc.upstream,
		Service:    svc,
		Template:   pc.template,
		AskTimeout: pc.askTimeout,
		Keys:       pc.keys,
		Log:        log,
	})
	rt.server = &http.Server{
		Addr:              pc.addr,
		Handler:           p,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Info("typryx wardryx-proxy listening",
		"version", version, "addr", pc.addr, "upstream_host", pc.upstream.Host,
		"template", pc.template, "ask_timeout", pc.askTimeout.String(),
		"backend", pc.backendName, "credentials_required", pc.keys.Configured(),
		"journal", journalState(pc.eventsPath),
		"calls_per_hour", capState(pc.maxCallsPerHour),
		"usd_per_day", usdCapState(pc.maxUsdPerDay))
	return rt, nil
}

func runWardryxProxy(log *slog.Logger) error {
	pc, err := loadProxyConfig()
	if err != nil {
		return err
	}
	rt, err := buildProxyRuntime(pc, log)
	if err != nil {
		return err
	}
	return serveRuntime(rt, log)
}
