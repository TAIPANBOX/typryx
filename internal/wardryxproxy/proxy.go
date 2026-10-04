// Package wardryxproxy is a reverse proxy placed in front of wardryx, between a
// caller (the tokenfuse MCP broker) and wardryx's /v1/decide.
//
// It forwards every request to wardryx unchanged, method, path, query, headers
// (Authorization included) and body, and never alters wardryx's response. The
// one exception is POST /v1/decide whose JSON body carries a tool call: for
// that, typryx is asked in-process (service.Service, so the template's egress
// filter, the caps, the answer ledger and the journal all apply exactly as for
// any other ask) what class of risk the call is, and if it answers in time with
// a valid choice, one signal is appended to the request's `signals` and the
// modified body goes on. Any failure to get that answer (an error, a timeout,
// `unanswered`, a refusal, a cap) forwards the original bytes untouched.
//
// That is what keeps wardryx generic: it reads `signals` and names no add-on,
// and a deployment without this proxy runs the identical wardryx. Wardryx can
// only turn a signal into a hold, never a deny, so a signal that is wrong, or
// absent, costs a person a delay and nothing else.
//
// If wardryx cannot be reached the proxy answers 502, so the caller's own
// fail mode decides what that means (tokenfuse: failmode=closed refuses).
package wardryxproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TAIPANBOX/typryx/internal/door"
	"github.com/TAIPANBOX/typryx/internal/service"
)

const (
	// DefaultTemplate is the question asked about a tool call.
	DefaultTemplate = "action.risk_class"
	// SignalSource is stamped on every signal this proxy adds.
	SignalSource = "typryx"
	// MaxBodyBytes bounds how much of a decide request is read to look at. A
	// body over it is streamed through unchanged, never refused here.
	MaxBodyBytes = 1 << 20
	// MaxSignals mirrors wardryx's cap: a request already at it is forwarded
	// as it is rather than made one too long.
	MaxSignals = 16

	decidePath = "/v1/decide"
)

// The reasons an ask gave no signal.
const (
	ReasonUnanswered = "unanswered"
	ReasonRefused    = "refused"
	ReasonInvalid    = "invalid_answer"
)

// Stats counts what the proxy did with decide requests it could have signed.
type Stats struct {
	Asked     int64
	Signalled int64
	NoSignal  map[string]int64
}

// Config is everything a Proxy is told.
type Config struct {
	// Upstream is wardryx's base URL.
	Upstream *url.URL
	// Service is typryx's own service layer, asked in-process.
	Service *service.Service
	// Template is the question template, DefaultTemplate unless configured.
	Template string
	// AskTimeout bounds one ask: the sub-budget carved out of the caller's own
	// timeout to wardryx, so a slow typryx costs a decision at most this much.
	AskTimeout time.Duration
	// Keys, when configured, makes the X-Typryx-Key header required on every
	// request and gives the journal its agent identity. The header is never
	// forwarded to wardryx.
	Keys door.Keys
	// Log receives one line per reason class, never a header or a body.
	Log *slog.Logger
	// Transport carries requests to wardryx. Nil builds one that ignores the
	// environment's proxy settings and passes bodies through undecoded.
	Transport http.RoundTripper
}

// Proxy forwards to Config.Upstream and adds a signal to a decide request that
// carries a tool call. Safe for concurrent use.
type Proxy struct {
	Config

	rp        *httputil.ReverseProxy
	asked     atomic.Int64
	signalled atomic.Int64
	mu        sync.Mutex
	noSignal  map[string]int64
	logged    map[string]bool
}

// New builds a Proxy from c.
func New(c Config) *Proxy {
	out := &Proxy{Config: c}
	if out.Template == "" {
		out.Template = DefaultTemplate
	}
	if out.Log == nil {
		out.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if out.Transport == nil {
		out.Transport = &http.Transport{
			// No environment proxy: a caller's Authorization goes to the
			// upstream it was configured for and nowhere else.
			Proxy:                 nil,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			ResponseHeaderTimeout: 30 * time.Second,
			MaxIdleConnsPerHost:   16,
			IdleConnTimeout:       60 * time.Second,
			// The proxy never decodes or re-encodes a body it forwards.
			DisableCompression: true,
		}
	}
	out.noSignal = map[string]int64{}
	out.logged = map[string]bool{}
	out.rp = &httputil.ReverseProxy{
		Transport: out.Transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(out.Upstream)
			// Rewrite mode drops these from the outgoing request; they are the
			// caller's own statement about the hop before the proxy, and the
			// proxy forwards a request unchanged.
			for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded"} {
				if v, ok := pr.In.Header[h]; ok {
					pr.Out.Header[h] = v
				}
			}
		},
		// A failure to reach wardryx is a 502 with a fixed body: never the
		// dial error, which names the upstream's address.
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if !errors.Is(err, context.Canceled) {
				out.noteUpstreamFailure(err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"error":"the upstream could not be reached"}`+"\n")
		},
		ErrorLog: nil,
	}
	// httputil logs through the standard logger when ErrorLog is nil, but only
	// from the default error handler, which is replaced above.
	return out
}

func (p *Proxy) noteUpstreamFailure(err error) {
	class := "upstream_unreachable"
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		class = "upstream_timeout"
	}
	p.mu.Lock()
	first := !p.logged[class]
	p.logged[class] = true
	p.noSignal[class]++
	p.mu.Unlock()
	if first {
		p.Log.Warn("wardryx could not be reached; callers get a 502 and their own fail mode decides", "reason", class)
	}
}

// ServeHTTP forwards r to wardryx, signing it first when it is a decide
// request carrying a tool call.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	presented := r.Header.Get(door.KeyHeader)
	if p.Keys.Configured() && !p.Keys.Allow(presented) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"unauthorized"}`+"\n")
		return
	}
	agent := p.Keys.Identity(presented)
	// typryx's own credential is for typryx; wardryx is never shown it.
	r.Header.Del(door.KeyHeader)

	if r.Method == http.MethodPost && r.URL.Path == decidePath {
		if !p.sign(r, agent) {
			http.Error(w, `{"error":"could not read the request body"}`, http.StatusBadRequest)
			return
		}
	}
	p.rp.ServeHTTP(w, r)
}

// sign reads r's body and, when it is a decide request with a tool call typryx
// can classify in time, replaces it with the same body plus one signal. In every
// other case r.Body carries exactly the bytes the caller sent. It reports false
// only when the body could not be read at all.
func (p *Proxy) sign(r *http.Request, agent string) bool {
	if r.Body == nil || r.Body == http.NoBody {
		return true
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, MaxBodyBytes+1))
	if err != nil {
		return false
	}
	if len(buf) > MaxBodyBytes {
		// Too big to look at, and not ours to refuse: what was read goes first,
		// the rest follows, and wardryx answers as it answers.
		r.Body = readCloser{io.MultiReader(bytes.NewReader(buf), r.Body), r.Body}
		return true
	}
	_ = r.Body.Close()
	if signed, ok := p.withSignal(r.Context(), agent, buf); ok {
		r.Body = io.NopCloser(bytes.NewReader(signed))
		r.ContentLength = int64(len(signed))
		r.Header.Del("Content-Length") // the transport frames the body from ContentLength
		return true
	}
	r.Body = io.NopCloser(bytes.NewReader(buf))
	return true
}

type readCloser struct {
	io.Reader
	io.Closer
}

// toolCall is the part of a tool_call this proxy reads.
type toolCall struct {
	Name      json.RawMessage `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Target    json.RawMessage `json:"target"`
	Truncated json.RawMessage `json:"arguments_truncated"`
}

// withSignal returns body with one signal appended, or false when the request
// is not one to sign or typryx gave no clean answer in time.
func (p *Proxy) withSignal(ctx context.Context, agent string, body []byte) ([]byte, bool) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(body, &obj) != nil || obj == nil {
		return nil, false
	}
	rawCall, present := obj["tool_call"]
	if !present {
		return nil, false
	}
	var tc toolCall
	if json.Unmarshal(rawCall, &tc) != nil {
		return nil, false
	}
	var name string
	if json.Unmarshal(tc.Name, &name) != nil || strings.TrimSpace(name) == "" {
		return nil, false
	}
	// Arguments the caller could not send are not arguments: classifying a tool
	// name alone would be a guess dressed as an answer. Only an absent, null or
	// false marker is "not truncated"; anything else is read as truncated.
	if t := bytes.TrimSpace(tc.Truncated); len(t) > 0 && !bytes.Equal(t, []byte("null")) && !bytes.Equal(t, []byte("false")) {
		return nil, false
	}
	var target string
	if t := bytes.TrimSpace(tc.Target); len(t) > 0 && !bytes.Equal(t, []byte("null")) {
		if json.Unmarshal(t, &target) != nil {
			return nil, false
		}
	}
	args := bytes.TrimSpace(tc.Arguments)
	if len(args) == 0 || bytes.Equal(args, []byte("null")) {
		args = []byte("{}")
	}
	var existing []json.RawMessage
	if s := bytes.TrimSpace(obj["signals"]); len(s) > 0 && !bytes.Equal(s, []byte("null")) {
		if json.Unmarshal(s, &existing) != nil {
			return nil, false
		}
	}
	if len(existing) >= MaxSignals {
		return nil, false
	}

	state, err := json.Marshal(struct {
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
		Target    string          `json:"target"`
	}{name, args, target})
	if err != nil {
		return nil, false
	}
	runID := ""
	var rid string
	if json.Unmarshal(obj["run_id"], &rid) == nil && door.ValidRunID(rid) {
		runID = rid
	}

	p.asked.Add(1)
	askCtx, cancel := context.WithTimeout(ctx, p.AskTimeout)
	defer cancel()
	res, refusal := p.Service.Ask(askCtx, service.Caller{AgentID: agent, RunID: runID},
		service.AskRequest{Template: p.Template, State: state, RunID: runID})
	if refusal != nil {
		p.noSignalFor(ReasonRefused + ":" + refusal.Code)
		return nil, false
	}
	if res.Unanswered {
		reason := res.Reason
		if reason == "" {
			reason = "unknown"
		}
		p.noSignalFor(ReasonUnanswered + ":" + reason)
		return nil, false
	}
	value, ok := res.Answer.(string)
	prob, has := res.Probabilities[value]
	if !ok || value == "" || !has || math.IsNaN(prob) || prob < 0 || prob > 1 || res.Type != "choice" || res.AnswerID == "" {
		p.noSignalFor(ReasonInvalid)
		return nil, false
	}
	sig, err := json.Marshal(map[string]any{
		"name": p.Template, "value": value, "probability": prob, "source": SignalSource, "answer_id": res.AnswerID,
	})
	if err != nil {
		p.noSignalFor(ReasonInvalid)
		return nil, false
	}
	list, err := json.Marshal(append(existing, sig))
	if err != nil {
		return nil, false
	}
	obj["signals"] = list
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, false
	}
	p.signalled.Add(1)
	p.recovered()
	return out, true
}

// noSignalFor counts a failed ask and logs its reason class once until typryx
// next answers. The line names the class and nothing the request carried.
func (p *Proxy) noSignalFor(class string) {
	p.mu.Lock()
	p.noSignal[class]++
	first := !p.logged[class]
	p.logged[class] = true
	p.mu.Unlock()
	if first {
		p.Log.Warn("no risk signal added; the request goes to wardryx as it came, and further asks that fail the same way are counted, not logged again until one answers", "reason", class)
	}
}

func (p *Proxy) recovered() {
	p.mu.Lock()
	if len(p.logged) > 0 {
		p.logged = map[string]bool{}
	}
	p.mu.Unlock()
}

// Stats is a snapshot of the counters.
func (p *Proxy) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	by := make(map[string]int64, len(p.noSignal))
	for k, v := range p.noSignal {
		by[k] = v
	}
	return Stats{Asked: p.asked.Load(), Signalled: p.signalled.Load(), NoSignal: by}
}
