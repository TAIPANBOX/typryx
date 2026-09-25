// jev.go: the "jev" backend, for the Jev API (TypeSafe AI, "typed-decision
// models"). One question per ask, sent as {state, model, questions:{"q":...}}
// to POST {base}/systemone; the response's answers.q is mapped back to
// typryx's own probability shape. Never trusted beyond that: invariant 1
// (never invent an answer) holds here the same way it holds for every other
// backend, and the service package's own probability validation stays the
// one authority over choice and score distributions (this backend does not
// renormalize them; see answerFrom's doc comment).
//
// Built and tested only against an httptest fake that replays the wire shape
// documented at docs.typesafe.ai/api and /introduction/quickstart (pinned
// verbatim in testdata/jev_example_response.json, read 2026-09-25); there is
// no key and no spend approval to call the real api.typesafe.ai, so nothing
// here has been run against it. See README.md's "Jev backend" section and
// NOT PROVEN.
package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/TAIPANBOX/typryx/internal/template"
)

// jevMaxAttempts is the total number of tries an ask gets: the first attempt
// plus up to two retries. jevBaseBackoff is the delay before the first retry;
// each later one doubles it. jevRetryAfterCap bounds any wait this backend
// ever makes, whether computed from the exponential schedule or read from a
// server's own Retry-After header: the documented example gives no rate
// limit numbers, so a server naming an absurd wait must not be allowed to
// make typryx sit idle for it.
const (
	jevMaxAttempts   = 3
	jevBaseBackoff   = 200 * time.Millisecond
	jevRetryAfterCap = 2 * time.Second
)

// JevConfig configures the jev backend.
type JevConfig struct {
	// BaseURL is the Jev API base, e.g. "https://api.typesafe.ai/v1".
	// Validated by cmd/typryx before this is ever constructed.
	BaseURL string
	// Model is sent as the request's "model" field, e.g. "jev-latest".
	Model string
	// APIKey is the bearer key, read from a file by cmd/typryx. Never
	// logged, never echoed into an error.
	APIKey string
	// PriceInputPerMTok and PriceOutputPerMTok are USD per million tokens,
	// used only to compute Usage.CostUSD. Zero (the default, and the zero
	// value of this struct) means unpriced: cost_usd is always 0. Neither is
	// ever hardcoded here; both come from cmd/typryx's own
	// TYPRYX_JEV_PRICE_PER_MTOK_INPUT/OUTPUT, so a price change is
	// configuration, never a code change.
	PriceInputPerMTok  float64
	PriceOutputPerMTok float64
	// Logger receives operational lines (a server error's status code, a
	// retry being attempted) that must never reach the caller. Defaults to
	// slog.Default().
	Logger *slog.Logger
}

// Jev is the jev backend.
type Jev struct {
	cfg    JevConfig
	client *http.Client
}

// NewJev builds a Jev backend from a validated JevConfig. The http.Client is
// built here and only here: internal/backend is the one package
// scripts/one-way-out.sh lets construct one.
func NewJev(cfg JevConfig) *Jev {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Model == "" {
		cfg.Model = "jev-latest"
	}
	return &Jev{
		cfg: cfg,
		client: &http.Client{
			// A 3xx is treated as a failure by Ask itself, never silently
			// chased to wherever it points.
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (j *Jev) Name() string { return "jev" }

// Ask sends q as one question, id "q", to POST {BaseURL}/systemone, retrying
// on 429 (rate limited) and 529 (overloaded) only, up to jevMaxAttempts
// total, with an exponential backoff a server's own Retry-After header can
// override (capped at jevRetryAfterCap either way). A retry is never made
// once it would cross ctx's own deadline: the wait between attempts is
// always raced against ctx.Done(), so a caller whose deadline fires mid-wait
// gets ctx's own error back immediately rather than a wait for the full
// backoff followed by one more doomed request.
func (j *Jev) Ask(ctx context.Context, q Question, eg template.Egress) (Answer, Usage, error) {
	wire, err := jevQuestionWireFor(q)
	if err != nil {
		return Answer{}, Usage{}, err
	}
	reqBody := jevRequest{
		State:     json.RawMessage(eg.Canonical()),
		Model:     j.cfg.Model,
		Questions: map[string]jevQuestionWire{"q": wire},
	}
	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		// reqBody's State is already-canonical JSON and everything else is
		// built from strings, a []string, and a map of the same; a marshal
		// failure here would mean the standard library's own contract broke.
		return Answer{}, Usage{}, fmt.Errorf("jev: marshaling the request: %w", err)
	}

	var lastErr error
	for attempt := 1; attempt <= jevMaxAttempts; attempt++ {
		ans, usage, err := j.tryOnce(ctx, reqJSON, q.Type)
		if err == nil {
			return ans, usage, nil
		}
		var re *jevRetryableError
		if !errors.As(err, &re) || attempt == jevMaxAttempts {
			return Answer{}, Usage{}, err
		}
		lastErr = err
		j.cfg.Logger.Warn("jev: retrying after the server asked to slow down",
			"status", re.status, "attempt", attempt)
		delay := retryDelay(attempt, re.retryAfter)
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return Answer{}, Usage{}, ctx.Err()
		}
	}
	// Unreachable: the loop above always returns by the time attempt reaches
	// jevMaxAttempts. Kept so the function has an explicit, typed return
	// rather than relying on that being obvious to a future reader.
	return Answer{}, Usage{}, lastErr
}

// jevRetryableError marks a response this backend may retry (429 or 529),
// carrying the server's own Retry-After header (empty if absent) for
// retryDelay to honor.
type jevRetryableError struct {
	status     int
	retryAfter string
}

func (e *jevRetryableError) Error() string {
	return fmt.Sprintf("jev: server responded with status %d", e.status)
}

// retryDelay is how long to wait before the retry that follows a failed
// attempt (1-based: the wait after attempt 1 fails is jevBaseBackoff, after
// attempt 2 fails it is doubled). A server's own Retry-After header, when it
// parses as a non-negative whole number of seconds, replaces the exponential
// value outright rather than adding to it; either way, the result is capped
// at jevRetryAfterCap, so neither an aggressive backoff schedule nor a
// server naming an absurd wait can make this backend sit idle for long.
//
// A pure function, deliberately: the cap and the header-parsing behaviour
// this is responsible for are provable without any test actually waiting on
// a timer.
func retryDelay(attempt int, retryAfterHeader string) time.Duration {
	d := jevBaseBackoff
	for i := 1; i < attempt; i++ {
		d *= 2
	}
	if retryAfterHeader != "" {
		if secs, err := strconv.Atoi(retryAfterHeader); err == nil && secs >= 0 {
			d = time.Duration(secs) * time.Second
		}
	}
	if d > jevRetryAfterCap {
		d = jevRetryAfterCap
	}
	return d
}

// tryOnce makes one HTTP attempt and, on a 200, maps the response to an
// Answer and Usage. A 429 or 529 comes back as *jevRetryableError, for Ask's
// loop to act on; every other failure is a plain error (or, when the server
// answered but gave nothing usable, an *UnansweredError).
func (j *Jev) tryOnce(ctx context.Context, reqJSON []byte, qType template.Type) (Answer, Usage, error) {
	url := strings.TrimRight(j.cfg.BaseURL, "/") + "/systemone"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqJSON))
	if err != nil {
		return Answer{}, Usage{}, fmt.Errorf("jev: building the request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+j.cfg.APIKey)

	resp, err := j.client.Do(httpReq)
	if err != nil {
		return Answer{}, Usage{}, fmt.Errorf("jev: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		j.cfg.Logger.Warn("jev: server redirected instead of answering; not followed",
			"status", resp.StatusCode)
		return Answer{}, Usage{}, fmt.Errorf("jev: server responded with a redirect (status %d)", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == 529 {
		return Answer{}, Usage{}, &jevRetryableError{status: resp.StatusCode, retryAfter: resp.Header.Get("Retry-After")}
	}
	if resp.StatusCode >= 400 {
		// The body is deliberately never included in the returned error or
		// the log: it may carry anything the server wants to say (401
		// invalid key, 422 validation failure), and neither the caller nor
		// the log line needs more than the status to act on.
		j.cfg.Logger.Warn("jev: server error", "status", resp.StatusCode)
		return Answer{}, Usage{}, fmt.Errorf("jev: server responded with status %d", resp.StatusCode)
	}
	if readErr != nil {
		return Answer{}, Usage{}, &UnansweredError{Reason: "no_probabilities"}
	}

	var parsed jevResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Answer{}, Usage{}, &UnansweredError{Reason: "no_probabilities"}
	}

	ans, err := answerFrom(parsed, qType)
	if err != nil {
		return Answer{}, Usage{}, err
	}
	model := parsed.Model
	if model == "" {
		model = j.cfg.Model
	}
	ans.Model = model

	usage := Usage{}
	if parsed.Usage != nil {
		usage.InputTokens = parsed.Usage.InputTokens
		usage.OutputTokens = parsed.Usage.OutputTokens
		usage.CostUSD = j.cost(usage.InputTokens, usage.OutputTokens)
	}
	return ans, usage, nil
}

// cost is InputTokens/1e6 * PriceInputPerMTok plus the same for output. Both
// prices default to their zero value (unpriced), so an unconfigured
// deployment always gets cost_usd 0, never a guessed number.
func (j *Jev) cost(inputTokens, outputTokens int) float64 {
	return float64(inputTokens)/1e6*j.cfg.PriceInputPerMTok +
		float64(outputTokens)/1e6*j.cfg.PriceOutputPerMTok
}

// answerFrom maps one jev answer (found under key "q") to typryx's own
// Answer shape.
//
// choice and score probabilities are passed through EXACTLY as the server
// sent them: this function does not renormalize, clip, or otherwise repair
// them, even though a hostile or buggy server could send values outside
// [0,1] or that do not sum to 1. internal/service.validateProbabilities is
// the one place that check is made, over every backend's answer alike (the
// exact key set, in range, summing to 1); doing a second, looser version of
// that check here would only give a false sense that this layer validates
// anything, while actually letting this backend's own idea of "close enough"
// diverge from the service's.
//
// noul is the one shape this backend DOES construct a distribution for, since
// the wire response carries a single number rather than a map: that number is
// checked finite and in [0,1] before {"true": noul, "false": 1-noul} is built,
// because building a two-key distribution out of a NaN or an out-of-range
// value would otherwise manufacture something that merely LOOKS like a valid
// distribution to len()- and key-based checks downstream.
func answerFrom(parsed jevResponse, qType template.Type) (Answer, error) {
	a, ok := parsed.Answers["q"]
	if !ok {
		return Answer{}, &UnansweredError{Reason: "no_probabilities"}
	}
	if template.Type(a.Type) != qType {
		return Answer{}, &UnansweredError{Reason: "no_probabilities"}
	}
	switch qType {
	case template.TypeChoice, template.TypeScore:
		if len(a.Probabilities) == 0 {
			return Answer{}, &UnansweredError{Reason: "no_probabilities"}
		}
		return Answer{Probabilities: a.Probabilities}, nil
	case template.TypeNoul:
		if a.Noul == nil {
			return Answer{}, &UnansweredError{Reason: "no_probabilities"}
		}
		n := *a.Noul
		if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 1 {
			return Answer{}, &UnansweredError{Reason: "bad_noul"}
		}
		return Answer{Probabilities: map[string]float64{"true": n, "false": 1 - n}}, nil
	}
	return Answer{}, fmt.Errorf("jev: unknown question type %q", qType)
}

// jevQuestionWireFor builds the wire shape for one question. See the wire
// types below for the criteria shape per type: choice sends {name:
// description}, with an empty description sent as JSON null rather than "";
// score sends the level array; noul sends {"true":.., "false":..} only when
// the template actually named criteria, and no criteria field at all
// otherwise (Question.NoulCriteriaGiven, set by internal/service.questionFor,
// is what makes that distinction possible here).
func jevQuestionWireFor(q Question) (jevQuestionWire, error) {
	w := jevQuestionWire{Type: string(q.Type), Instructions: q.Instructions}
	switch q.Type {
	case template.TypeChoice:
		m := make(map[string]any, len(q.Options))
		for _, o := range q.Options {
			if o.Description == "" {
				m[o.Name] = nil
			} else {
				m[o.Name] = o.Description
			}
		}
		w.Criteria = m
	case template.TypeScore:
		w.Criteria = q.Levels
	case template.TypeNoul:
		if q.NoulCriteriaGiven {
			w.Criteria = map[string]string{"true": q.NoulTrueDesc, "false": q.NoulFalseDesc}
		}
	default:
		return jevQuestionWire{}, fmt.Errorf("jev: unknown question type %q", q.Type)
	}
	return w, nil
}

// --- wire shapes -------------------------------------------------------------

type jevRequest struct {
	State     json.RawMessage            `json:"state"`
	Model     string                     `json:"model"`
	Questions map[string]jevQuestionWire `json:"questions"`
}

type jevQuestionWire struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	// Criteria is any (a map for choice/noul, a []string for score) so each
	// type's own shape is sent as documented; omitempty drops it entirely
	// for a noul question with no criteria (a nil interface is "empty" to
	// encoding/json), which is different from sending an empty object.
	Criteria any `json:"criteria,omitempty"`
}

type jevAnswerWire struct {
	Type string `json:"type"`
	// Choice, Score, Noul and Confidence are read only where documented
	// (Noul, for the noul type); Choice, Score and Confidence are never read
	// at all, the same "advisory, unread" status backend.Answer's own
	// Choice/Score/Yes fields document, because the probability distribution
	// (or, for noul, the number this maps into one) is the only signal this
	// backend or internal/service ever trusts.
	Choice string  `json:"choice,omitempty"`
	Score  float64 `json:"score,omitempty"`
	// Noul is a pointer so that an absent or null field stays absent: a
	// plain float64 decoded it as 0, which became a certain "false" the
	// server never sent.
	Noul          *float64           `json:"noul,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Legend        map[string]string  `json:"legend,omitempty"`
}

type jevUsageWire struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type jevResponse struct {
	Model   string                   `json:"model"`
	Answers map[string]jevAnswerWire `json:"answers"`
	Usage   *jevUsageWire            `json:"usage"`
}
