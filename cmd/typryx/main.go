// Command typryx answers typed questions with a probability, over HTTP and
// MCP. It is an optional add-on: absent, the rest of the stack behaves
// exactly as it does today.
//
// This file is configuration and wiring only. Every rule it appears to
// enforce lives in internal/service, internal/template, internal/door and
// internal/record, which is what lets those be tested without a running
// process. What lives HERE is the set of defaults and the startup order, and
// the order is a decision: required configuration is checked BEFORE the
// open-bind refusal, unlike scopyx (which has no equivalent of a "required
// variable" check ahead of its bind check). That is simpler to reason about
// from a log line, and internal/manifest proves the open-bind matrix holds
// with every required variable already set, so nothing about the matrix
// itself depends on the order.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/TAIPANBOX/typryx/internal/api"
	"github.com/TAIPANBOX/typryx/internal/backend"
	"github.com/TAIPANBOX/typryx/internal/door"
	"github.com/TAIPANBOX/typryx/internal/ledger"
	"github.com/TAIPANBOX/typryx/internal/mcp"
	"github.com/TAIPANBOX/typryx/internal/record"
	"github.com/TAIPANBOX/typryx/internal/service"
	"github.com/TAIPANBOX/typryx/internal/template"
)

var version = "dev"

const (
	defaultAddr            = "127.0.0.1:4320"
	defaultMaxCallsPerHour = 1000
	defaultTimeoutMS       = 2000
	defaultShutdownTimeout = 10 * time.Second
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	args := os.Args[1:]
	if len(args) >= 2 && args[0] == "templates" && args[1] == "check" {
		dir := "."
		if len(args) >= 3 {
			dir = args[2]
		}
		os.Exit(templatesCheck(dir, os.Stdout, os.Stderr))
	}
	if len(args) >= 1 && args[0] == "connect" {
		os.Exit(connectCmd(args[1:], os.Stdout, os.Stderr))
	}
	if len(args) >= 1 && args[0] == "calibration" {
		os.Exit(calibrationCmd(args[1:], os.Stdout, os.Stderr))
	}
	if len(args) >= 1 && args[0] != "serve" {
		fmt.Fprintf(os.Stderr, "typryx: unknown subcommand %q. Known: serve (the default), templates check <dir>, connect <target>, calibration\n", args[0])
		os.Exit(2)
	}

	if err := run(log); err != nil {
		var cfg *configError
		if errors.As(err, &cfg) {
			fmt.Fprintln(os.Stderr, "typryx: "+err.Error())
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "typryx: "+err.Error())
		os.Exit(1)
	}
}

// configError marks a startup failure that exits 2: a missing or malformed
// setting, as opposed to a deliberate refusal (the open-bind matrix), which
// exits 1.
type configError struct{ msg string }

func (e *configError) Error() string { return e.msg }

func missingVar(name, why string) error {
	return &configError{msg: fmt.Sprintf("%s is required and is not set. %s", name, why)}
}

func badVar(name, value, why string) error {
	return &configError{msg: fmt.Sprintf("%s=%q is invalid: %s", name, value, why)}
}

// config is every setting run() needs, resolved and validated by loadConfig
// before anything else happens: no socket bound, no file opened. Splitting
// this out from run() is what lets the config-error paths (missing and
// malformed variables, invalid templates, the open-bind refusal) be tested
// in-process, fast, and by name, while run() itself is still what
// internal/manifest exercises end to end by starting the real binary. Neither
// replaces the other: this proves EVERY branch; the manifest test proves the
// real process behaves.
type config struct {
	addr            string
	keys            door.Keys
	allowOpenBind   bool
	allowFreeform   bool
	acceptKeyInMeta bool
	backendName     string
	templatesDir    string
	templates       *template.Registry
	maxCallsPerHour int64
	timeoutMS       int64
	eventsPath      string
	ledgerDir       string
	openai          *openaiBackendConfig
	jev             *jevBackendConfig
	// maxUsdPerDay is nil when TYPRYX_MAX_USD_PER_DAY is unset (no daily
	// USD cap at all), and points to the parsed value otherwise: 0 is the
	// explicit disabled state (a boot warning, like TYPRYX_MAX_CALLS_PER_HOUR=0),
	// a positive value is the cap itself, and negative is rejected in
	// loadConfig before this is ever built.
	maxUsdPerDay *float64
}

// openaiBackendConfig is the validated TYPRYX_OPENAI_* configuration, set
// only when TYPRYX_BACKEND=openai-logprobs.
type openaiBackendConfig struct {
	url                string
	model              string
	key                string
	minLabelMass       float64
	meterHeaders       bool
	defaultRunID       string
	priceInputPerMTok  float64
	priceOutputPerMTok float64
}

// isUnpriced reports whether this backend config's own prices would leave
// cost_usd always 0. Used only to refuse an unpriced backend combined with a
// nonzero TYPRYX_MAX_USD_PER_DAY: a cap over a backend that reports no cost
// measures nothing.
func (c *openaiBackendConfig) isUnpriced() bool {
	return c.priceInputPerMTok == 0 && c.priceOutputPerMTok == 0
}

const defaultOpenAIMinLabelMass = 0.9

// jevBackendConfig is the validated TYPRYX_JEV_* configuration, set only
// when TYPRYX_BACKEND=jev.
type jevBackendConfig struct {
	url                string
	model              string
	key                string
	priceInputPerMTok  float64
	priceOutputPerMTok float64
}

// isUnpriced mirrors openaiBackendConfig.isUnpriced: see its doc comment.
func (c *jevBackendConfig) isUnpriced() bool {
	return c.priceInputPerMTok == 0 && c.priceOutputPerMTok == 0
}

const (
	defaultJevURL   = "https://api.typesafe.ai/v1"
	defaultJevModel = "jev-latest"
)

// loadJevConfig reads and validates the TYPRYX_JEV_* variables, set only
// when TYPRYX_BACKEND=jev. Unlike the openai-logprobs backend, jev is a paid
// backend with no local, free equivalent, so its key is not merely optional:
// TYPRYX_JEV_KEY_FILE is itself required-when-chosen (a file path, never an
// environment value, matching TYPRYX_OPENAI_KEY_FILE's own shape), and an
// unreadable or empty file exits 2 naming the variable, never the contents.
func loadJevConfig() (*jevBackendConfig, error) {
	keyFile := os.Getenv("TYPRYX_JEV_KEY_FILE")
	if keyFile == "" {
		return nil, missingVar("TYPRYX_JEV_KEY_FILE",
			"set it to a file path holding the Jev API key. jev is a paid backend, so its key is always a named file, never an environment value.")
	}
	b, err := os.ReadFile(keyFile) // #nosec G304 G703 -- an operator-provided path from TYPRYX_JEV_KEY_FILE, read once at startup, never from a request
	if err != nil {
		return nil, &configError{msg: fmt.Sprintf(
			"TYPRYX_JEV_KEY_FILE=%s could not be read: %v", keyFile, err)}
	}
	key := strings.TrimSpace(string(b))
	if key == "" {
		return nil, &configError{msg: fmt.Sprintf(
			"TYPRYX_JEV_KEY_FILE=%s is empty", keyFile)}
	}

	rawURL := envOr("TYPRYX_JEV_URL", defaultJevURL)
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" ||
		u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, badVar("TYPRYX_JEV_URL", rawURL,
			"must be an absolute http or https URL with a host and no userinfo, query, or fragment")
	}

	model := envOr("TYPRYX_JEV_MODEL", defaultJevModel)

	priceIn, err := envFloat("TYPRYX_JEV_PRICE_PER_MTOK_INPUT", 0)
	if err != nil {
		return nil, err
	}
	if priceIn < 0 {
		return nil, badVar("TYPRYX_JEV_PRICE_PER_MTOK_INPUT", strconv.FormatFloat(priceIn, 'g', -1, 64),
			"must not be negative")
	}
	priceOut, err := envFloat("TYPRYX_JEV_PRICE_PER_MTOK_OUTPUT", 0)
	if err != nil {
		return nil, err
	}
	if priceOut < 0 {
		return nil, badVar("TYPRYX_JEV_PRICE_PER_MTOK_OUTPUT", strconv.FormatFloat(priceOut, 'g', -1, 64),
			"must not be negative")
	}

	return &jevBackendConfig{url: rawURL, model: model, key: key,
		priceInputPerMTok: priceIn, priceOutputPerMTok: priceOut}, nil
}

// envFloat reads name as a float64, refusing a malformed value by name
// rather than silently falling back to the default, the same reasoning
// envInt already gives for TYPRYX_TIMEOUT_MS.
func envFloat(name string, fallback float64) (float64, error) {
	v := os.Getenv(name)
	if v == "" {
		return fallback, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, badVar(name, v, "must be a number")
	}
	return f, nil
}

// envFloatPtr reads name as a float64 pointer: nil when the variable is
// unset at all (its own "no such thing" state, as opposed to an explicit
// 0), otherwise a pointer to the parsed value. Used only for
// TYPRYX_MAX_USD_PER_DAY, where unset and 0 mean two different things (no
// cap at all, versus a cap explicitly disabled with a boot warning); every
// other float here has a real numeric default and uses envFloat instead. A
// malformed value refuses by name rather than silently falling back.
func envFloatPtr(name string) (*float64, error) {
	v := os.Getenv(name)
	if v == "" {
		return nil, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return nil, badVar(name, v, "must be a number")
	}
	return &f, nil
}

// loadOpenAIConfig reads and validates the TYPRYX_OPENAI_* variables, which
// are required only when TYPRYX_BACKEND=openai-logprobs (a "required when
// chosen" shape components.json declares as required:false plus a
// required_when note, since they must NOT be demanded of a deployment that
// picked a different backend).
func loadOpenAIConfig() (*openaiBackendConfig, error) {
	rawURL := os.Getenv("TYPRYX_OPENAI_URL")
	if rawURL == "" {
		return nil, missingVar("TYPRYX_OPENAI_URL",
			"set it to an OpenAI-compatible base URL ending in /v1, e.g. http://127.0.0.1:11434/v1 for a local Ollama.")
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" ||
		u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, badVar("TYPRYX_OPENAI_URL", rawURL,
			"must be an absolute http or https URL with a host and no userinfo, query, or fragment")
	}

	model := os.Getenv("TYPRYX_OPENAI_MODEL")
	if model == "" {
		return nil, missingVar("TYPRYX_OPENAI_MODEL", "set it to the model name this endpoint should answer with.")
	}

	var key string
	if keyFile := os.Getenv("TYPRYX_OPENAI_KEY_FILE"); keyFile != "" {
		b, err := os.ReadFile(keyFile) // #nosec G304 G703 -- an operator-provided path from TYPRYX_OPENAI_KEY_FILE, read once at startup, never from a request
		if err != nil {
			return nil, &configError{msg: fmt.Sprintf(
				"TYPRYX_OPENAI_KEY_FILE=%s could not be read: %v", keyFile, err)}
		}
		key = strings.TrimSpace(string(b))
	}

	minLabelMass := defaultOpenAIMinLabelMass
	if raw := os.Getenv("TYPRYX_OPENAI_MIN_LABEL_MASS"); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || v <= 0 || v > 1 {
			return nil, badVar("TYPRYX_OPENAI_MIN_LABEL_MASS", raw, "must be a number greater than 0 and at most 1")
		}
		minLabelMass = v
	}

	meterHeaders := door.TruthyEnv(os.Getenv("TYPRYX_OPENAI_METER_HEADERS"))

	defaultRunID := os.Getenv("TYPRYX_OPENAI_RUN_ID")
	if !door.ValidRunID(defaultRunID) {
		return nil, badVar("TYPRYX_OPENAI_RUN_ID", defaultRunID,
			"must be at most 128 bytes with no control characters")
	}

	priceIn, err := envFloat("TYPRYX_OPENAI_PRICE_PER_MTOK_INPUT", 0)
	if err != nil {
		return nil, err
	}
	if priceIn < 0 {
		return nil, badVar("TYPRYX_OPENAI_PRICE_PER_MTOK_INPUT", strconv.FormatFloat(priceIn, 'g', -1, 64),
			"must not be negative")
	}
	priceOut, err := envFloat("TYPRYX_OPENAI_PRICE_PER_MTOK_OUTPUT", 0)
	if err != nil {
		return nil, err
	}
	if priceOut < 0 {
		return nil, badVar("TYPRYX_OPENAI_PRICE_PER_MTOK_OUTPUT", strconv.FormatFloat(priceOut, 'g', -1, 64),
			"must not be negative")
	}

	return &openaiBackendConfig{
		url: rawURL, model: model, key: key, minLabelMass: minLabelMass,
		meterHeaders: meterHeaders, defaultRunID: defaultRunID,
		priceInputPerMTok: priceIn, priceOutputPerMTok: priceOut,
	}, nil
}

// loadConfig reads and validates every TYPRYX_ variable. It is checked in the
// documented order: required configuration first (TYPRYX_BACKEND, including
// whether its value is a backend this build has, and TYPRYX_TEMPLATES,
// including whether the directory's templates are all valid), then the
// numeric variables, then the open-bind refusal last, once the rest of the
// configuration is already known to be sane.
func loadConfig() (*config, error) {
	backendName := os.Getenv("TYPRYX_BACKEND")
	if backendName == "" {
		return nil, missingVar("TYPRYX_BACKEND", "there is no default backend, on purpose: a paid backend must always be a named choice. Set TYPRYX_BACKEND=stub for this build.")
	}
	var openaiCfg *openaiBackendConfig
	var jevCfg *jevBackendConfig
	switch backendName {
	case "stub":
		// nothing further to read.
	case "openai-logprobs":
		cfg, err := loadOpenAIConfig()
		if err != nil {
			return nil, err
		}
		openaiCfg = cfg
	case "jev":
		cfg, err := loadJevConfig()
		if err != nil {
			return nil, err
		}
		jevCfg = cfg
	default:
		return nil, &configError{msg: fmt.Sprintf("backend %s is not built yet", backendName)}
	}

	templatesDir := os.Getenv("TYPRYX_TEMPLATES")
	if templatesDir == "" {
		return nil, missingVar("TYPRYX_TEMPLATES", "set it to a directory of *.json question templates.")
	}
	reg, loadErrs, err := template.LoadDir(templatesDir)
	if err != nil {
		return nil, &configError{msg: err.Error()}
	}
	if len(loadErrs) > 0 {
		msgs := make([]string, len(loadErrs))
		for i, e := range loadErrs {
			msgs[i] = e.Error()
		}
		return nil, &configError{msg: fmt.Sprintf("TYPRYX_TEMPLATES=%s has %d invalid template(s): %s",
			templatesDir, len(loadErrs), joinSemicolon(msgs))}
	}

	maxCallsPerHour, err := envInt("TYPRYX_MAX_CALLS_PER_HOUR", defaultMaxCallsPerHour)
	if err != nil {
		return nil, err
	}
	// 0 is the one explicit, documented uncapped opt-out; anything negative
	// is not "uncapped" spelled differently, it is a value nobody chose on
	// purpose, and silently treating it as uncapped would spend without a
	// decision behind it.
	if maxCallsPerHour < 0 {
		return nil, badVar("TYPRYX_MAX_CALLS_PER_HOUR", strconv.FormatInt(maxCallsPerHour, 10),
			"must be 0 (the explicit uncapped opt-out) or a positive whole number")
	}
	timeoutMS, err := envInt("TYPRYX_TIMEOUT_MS", defaultTimeoutMS)
	if err != nil {
		return nil, err
	}
	if timeoutMS <= 0 {
		return nil, badVar("TYPRYX_TIMEOUT_MS", strconv.FormatInt(timeoutMS, 10),
			"must be a positive whole number of milliseconds; there is no uncapped-deadline opt-out")
	}

	// nil means TYPRYX_MAX_USD_PER_DAY is unset: no daily USD cap at all, not
	// even the "0, disabled, with a warning" state the hourly cap's own 0
	// means. That state exists here too, spelled by an explicit "0".
	maxUsdPerDay, err := envFloatPtr("TYPRYX_MAX_USD_PER_DAY")
	if err != nil {
		return nil, err
	}
	if maxUsdPerDay != nil && *maxUsdPerDay < 0 {
		return nil, badVar("TYPRYX_MAX_USD_PER_DAY", strconv.FormatFloat(*maxUsdPerDay, 'g', -1, 64),
			"must be 0 (the explicit disabled state) or a positive number of US dollars")
	}
	// A cap on a backend that always reports cost_usd 0 measures nothing: it
	// would sit there looking like a real ceiling while nothing ever moves
	// it. Refuse to start rather than let an operator believe an unpriced
	// deployment is bounded.
	if maxUsdPerDay != nil && *maxUsdPerDay > 0 && backendIsUnpriced(backendName, openaiCfg, jevCfg) {
		return nil, &configError{msg: fmt.Sprintf(
			"TYPRYX_MAX_USD_PER_DAY=%s is set, but backend %s is unpriced (cost_usd is always 0): "+
				"a spend cap over a backend that reports no cost measures nothing. Configure a price "+
				"or unset TYPRYX_MAX_USD_PER_DAY.", strconv.FormatFloat(*maxUsdPerDay, 'g', -1, 64), backendName)}
	}

	addr := envOr("TYPRYX_ADDR", defaultAddr)
	keys := door.ParseKeys(os.Getenv("TYPRYX_KEYS"))
	// A credential bound to anything that is not a well-formed agent://
	// identity would have that value written as agent_id on every event and
	// answer it produces: never the credential itself in this message, only
	// the (non-secret) identity it was bound to.
	for _, id := range keys.Identities() {
		if !door.ValidIdentity(id) {
			return nil, &configError{msg: fmt.Sprintf(
				"TYPRYX_KEYS binds a credential to %q, which is not a well-formed agent:// identity "+
					"(need agent://<host>/<path>); refusing to start", id)}
		}
	}
	allowOpenBind := door.TruthyEnv(os.Getenv("TYPRYX_ALLOW_OPEN_BIND"))
	allowFreeform := door.TruthyEnv(os.Getenv("TYPRYX_ALLOW_FREEFORM"))
	// TYPRYX_ACCEPT_KEY_IN_META plays no part in the open-bind decision
	// below, on purpose: it only lets a tools/call carry its credential in a
	// different place, never lets a call through with none. A wide bind
	// with no TYPRYX_KEYS configured refuses exactly as it always has,
	// whatever this flag says (internal/manifest proves that row).
	acceptKeyInMeta := door.TruthyEnv(os.Getenv("TYPRYX_ACCEPT_KEY_IN_META"))

	if why := door.RefuseOpenBind(addr, keys, allowOpenBind); why != "" {
		return nil, errors.New(why)
	}

	return &config{
		addr: addr, keys: keys, allowOpenBind: allowOpenBind, allowFreeform: allowFreeform,
		acceptKeyInMeta: acceptKeyInMeta,
		backendName:     backendName, templatesDir: templatesDir, templates: reg,
		maxCallsPerHour: maxCallsPerHour, timeoutMS: timeoutMS,
		eventsPath: os.Getenv("TYPRYX_EVENTS"), ledgerDir: os.Getenv("TYPRYX_LEDGER_DIR"),
		openai: openaiCfg, jev: jevCfg, maxUsdPerDay: maxUsdPerDay,
	}, nil
}

// backendIsUnpriced reports whether the named backend, configured as given,
// would report cost_usd 0 for every call: stub always does (it has no price
// configuration at all), and openai-logprobs/jev do exactly when both of
// their own configured prices are 0 (the default, meaning "unpriced").
func backendIsUnpriced(name string, openaiCfg *openaiBackendConfig, jevCfg *jevBackendConfig) bool {
	switch name {
	case "openai-logprobs":
		return openaiCfg.isUnpriced()
	case "jev":
		return jevCfg.isUnpriced()
	default:
		return true
	}
}

// runtime is what buildRuntime assembled: the HTTP server ready to listen,
// and the resources run() must close on the way out.
type runtime struct {
	server  *http.Server
	journal *record.Journal
	ledger  *ledger.Ledger
}

// buildRuntime turns a validated config into a runnable server: it opens the
// journal and (if configured) the ledger, wires the service and both
// surfaces, and builds the http.Server. It never blocks and never opens a
// socket (http.Server does that lazily, in ListenAndServe), which is what
// lets this be tested in-process: every wiring decision below the
// config-validation layer is reachable from a test without a network.
func buildRuntime(cfg *config, log *slog.Logger) (*runtime, error) {
	if cfg.maxCallsPerHour <= 0 {
		log.Warn("this deployment has NO hourly call cap; TYPRYX_MAX_CALLS_PER_HOUR=0 disables it deliberately")
	}

	journal, err := record.Open(cfg.eventsPath)
	if err != nil {
		return nil, err
	}

	var led *ledger.Ledger
	if cfg.ledgerDir != "" {
		led, err = ledger.Open(cfg.ledgerDir)
		if err != nil {
			_ = journal.Close()
			return nil, err
		}
		if led.SkippedTornLines > 0 {
			log.Warn("the ledger's answer log had a torn last line, skipped", "count", led.SkippedTornLines)
		}
	}

	svc := service.New()
	svc.Templates = cfg.templates
	switch cfg.backendName {
	case "openai-logprobs":
		if cfg.openai.meterHeaders {
			log.Info("typryx openai-logprobs metering headers are ON: x-fuse-run-id and x-fuse-agent-id will be sent to TYPRYX_OPENAI_URL")
		}
		svc.Backend = backend.NewOpenAI(backend.OpenAIConfig{
			BaseURL:            cfg.openai.url,
			Model:              cfg.openai.model,
			APIKey:             cfg.openai.key,
			MinLabelMass:       cfg.openai.minLabelMass,
			MeterHeaders:       cfg.openai.meterHeaders,
			DefaultRunID:       cfg.openai.defaultRunID,
			PriceInputPerMTok:  cfg.openai.priceInputPerMTok,
			PriceOutputPerMTok: cfg.openai.priceOutputPerMTok,
			Logger:             log,
		})
	case "jev":
		// The URL was already validated by loadJevConfig; re-parsing it here
		// only extracts the host for the log line below, never the key.
		jevURL, _ := url.Parse(cfg.jev.url)
		log.Info("typryx jev backend configured",
			"url_host", jevURL.Host, "model", cfg.jev.model)
		svc.Backend = backend.NewJev(backend.JevConfig{
			BaseURL:            cfg.jev.url,
			Model:              cfg.jev.model,
			APIKey:             cfg.jev.key,
			PriceInputPerMTok:  cfg.jev.priceInputPerMTok,
			PriceOutputPerMTok: cfg.jev.priceOutputPerMTok,
			Logger:             log,
		})
	default:
		svc.Backend = backend.Stub{}
	}
	svc.Cap = service.NewCap(cfg.maxCallsPerHour)
	if cfg.maxUsdPerDay != nil {
		if *cfg.maxUsdPerDay <= 0 {
			log.Warn("this deployment has NO daily USD spend cap; TYPRYX_MAX_USD_PER_DAY=0 disables it deliberately")
		} else {
			svc.UsdCap = service.NewUsdCap(*cfg.maxUsdPerDay)
		}
	}
	svc.Ledger = led
	svc.Journal = journal
	svc.Timeout = time.Duration(cfg.timeoutMS) * time.Millisecond
	svc.AllowFreeform = cfg.allowFreeform

	mcpServer := &mcp.Server{Service: svc}
	apiServer := &api.Server{Keys: cfg.keys, Service: svc, MCP: mcpServer, AcceptKeyInMeta: cfg.acceptKeyInMeta}

	srv := &http.Server{
		Addr:              cfg.addr,
		Handler:           api.NewMux(apiServer),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Info("typryx listening",
		"version", version, "addr", cfg.addr, "backend", cfg.backendName,
		"templates", len(cfg.templates.List()), "credentials_required", cfg.keys.Configured(),
		"journal", journalState(cfg.eventsPath),
		"ledger", ledgerState(cfg.ledgerDir),
		"calls_per_hour", capState(cfg.maxCallsPerHour),
		"usd_per_day", usdCapState(cfg.maxUsdPerDay),
		"freeform", cfg.allowFreeform,
		"accept_key_in_meta", cfg.acceptKeyInMeta)

	return &runtime{server: srv, journal: journal, ledger: led}, nil
}

func run(log *slog.Logger) error {
	mcp.Version = version

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	rt, err := buildRuntime(cfg, log)
	if err != nil {
		return err
	}
	journal, led, srv := rt.journal, rt.ledger, rt.server
	defer func() {
		if err := journal.Close(); err != nil {
			log.Error("the journal did not close cleanly", "error", err)
		}
	}()
	if led != nil {
		defer func() {
			if err := led.Close(); err != nil {
				log.Error("the ledger did not close cleanly", "error", err)
			}
		}()
	}

	errc := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-errc:
		return err
	case <-stop:
	}

	log.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
	defer cancel()
	if skipped, failed := journal.Counts(); skipped > 0 || failed > 0 {
		log.Warn("the record is incomplete", "skipped_no_agent_id", skipped, "write_failures", failed)
	}
	return srv.Shutdown(ctx)
}

func templatesCheck(dir string, stdout, stderr io.Writer) int {
	reg, loadErrs, err := template.LoadDir(dir)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 1
	}
	for _, t := range reg.List() {
		fmt.Fprintf(stdout, "%s %s\n", t.ID, t.Version())
	}
	for _, e := range loadErrs {
		fmt.Fprintln(stderr, e.Error())
	}
	if len(loadErrs) > 0 {
		return 1
	}
	return 0
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// envInt refuses a malformed value by name rather than silently falling back
// to the default: an operator who typed TYPRYX_TIMEOUT_MS=2s gets an error
// naming it, not a bound they believe they set.
func envInt(name string, fallback int64) (int64, error) {
	v := os.Getenv(name)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, badVar(name, v, "must be a whole number")
	}
	return n, nil
}

func journalState(path string) string {
	if path == "" {
		return "disabled"
	}
	return path
}

func ledgerState(dir string) string {
	if dir == "" {
		return "disabled: /v1/outcome will refuse with no_ledger, and answers are not ledgered"
	}
	return dir
}

func capState(n int64) string {
	if n <= 0 {
		return "UNCAPPED"
	}
	return strconv.FormatInt(n, 10)
}

// usdCapState renders TYPRYX_MAX_USD_PER_DAY's three-way state for the
// startup log line: nil (unset) and <= 0 (explicitly disabled) are both
// "no cap enforced", but distinguishable in the log by which word is used,
// since an operator reading a log line should be able to tell "I never set
// this" from "I set this to 0 on purpose" without opening the config.
func usdCapState(v *float64) string {
	if v == nil {
		return "UNSET"
	}
	if *v <= 0 {
		return "DISABLED"
	}
	return strconv.FormatFloat(*v, 'g', -1, 64)
}

func joinSemicolon(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += "; "
		}
		out += s
	}
	return out
}
