// Package service is the one place ask and outcome logic lives. Both the
// HTTP surface (internal/api) and the MCP surface (internal/mcp) call this
// package and nothing else, which is why TestTheMCPToolAnswersTheSameAsTheHTTPRoute
// can hold: there is exactly one code path to compare against itself.
package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TAIPANBOX/typryx/internal/backend"
	"github.com/TAIPANBOX/typryx/internal/ledger"
	"github.com/TAIPANBOX/typryx/internal/record"
	"github.com/TAIPANBOX/typryx/internal/template"
)

// Caller is who is asking, established by the door (a credential's bound
// identity) and never by anything the request body claims.
type Caller struct {
	AgentID string
	RunID   string
}

// FreeformQuestion is a question that names no template. It is refused
// unless the operator switched freeform on.
type FreeformQuestion struct {
	Type         string
	Instructions string
	Criteria     json.RawMessage
}

// AskRequest is one ask, from either surface.
type AskRequest struct {
	Template string
	State    json.RawMessage
	RunID    string
	Question *FreeformQuestion
}

// Result is what an ask produced, whether answered or not.
//
// Answer holds a string for choice, an int for score, and the probability of
// true (a float64) for noul. Probabilities and Answer are both omitted
// entirely when Unanswered is true: an unanswered result carries no
// probability at all, which is the whole of invariant 1 reflected in the
// wire shape rather than only in prose.
type Result struct {
	AnswerID        string             `json:"answer_id"`
	Template        string             `json:"template"`
	TemplateVersion string             `json:"template_version"`
	Type            string             `json:"type"`
	Answer          any                `json:"answer,omitempty"`
	Probabilities   map[string]float64 `json:"probabilities,omitempty"`
	Backend         string             `json:"backend,omitempty"`
	Model           string             `json:"model,omitempty"`
	LatencyMS       int64              `json:"latency_ms"`
	CostUSD         float64            `json:"cost_usd,omitempty"`
	HeldBackFields  int                `json:"held_back_fields"`
	Unanswered      bool               `json:"unanswered,omitempty"`
	Reason          string             `json:"reason,omitempty"`
}

// Refusal is a typed error the API and MCP surfaces translate into their own
// wire shape (an HTTP status and a JSON error, or an MCP isError result).
type Refusal struct {
	Code       string
	HTTPStatus int
	Message    string
}

func (r *Refusal) Error() string { return r.Message }

func refusal(code string, status int, format string, a ...any) *Refusal {
	return &Refusal{Code: code, HTTPStatus: status, Message: fmt.Sprintf(format, a...)}
}

// OutcomeRequest carries a later truth for a given answer.
type OutcomeRequest struct {
	AnswerID string
	Truth    json.RawMessage
	Source   string
}

// OutcomeResult confirms an outcome was recorded.
type OutcomeResult struct {
	AnswerID        string `json:"answer_id"`
	Template        string `json:"template"`
	TemplateVersion string `json:"template_version"`
}

// Cap is an hourly call cap, counted only against calls admitted past the
// freeform gate, the template lookup and the egress filter: a call refused
// before this point never touches anyone's budget.
//
// A fixed window, not a sliding one: the point is a ceiling an operator can
// reason about from a log line, not smooth pacing.
type Cap struct {
	mu       sync.Mutex
	limit    int64
	used     int64
	windowAt time.Time
	now      func() time.Time
}

// NewCap builds a cap. limit <= 0 disables it (the caller is expected to log
// a warning at boot when that happens; Cap itself only enforces).
func NewCap(limit int64) *Cap {
	return &Cap{limit: limit, windowAt: time.Now(), now: time.Now}
}

func (c *Cap) take() bool {
	if c.limit <= 0 {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if now.Sub(c.windowAt) >= time.Hour {
		c.windowAt = now
		c.used = 0
	}
	if c.used >= c.limit {
		return false
	}
	c.used++
	return true
}

// Service answers typed questions.
type Service struct {
	Templates             *template.Registry
	Backend               backend.Backend
	Cap                   *Cap
	Ledger                *ledger.Ledger // nil: outcomes and answer ledgering are off
	Journal               *record.Journal
	Timeout               time.Duration
	AllowFreeform         bool
	FreeformMaxStateBytes int

	// ledgerFailures counts PutAnswer failures: the answer itself is still
	// served (the backend already answered), but a write failure means it
	// cannot later be scored, which is a fact an operator has to be able to
	// see rather than one silently dropped. Atomic because Ask and
	// LedgerFailures (read from GET /healthz) run concurrently.
	ledgerFailures atomic.Int64

	// now and randomID are seams for tests; both default when the zero value
	// is used through New.
	now      func() time.Time
	randomID func() string
}

// LedgerFailures reports how many PutAnswer calls have failed, for GET
// /healthz to surface alongside the journal's own counts.
func (s *Service) LedgerFailures() int64 {
	return s.ledgerFailures.Load()
}

// New builds a Service with real clock and id generation.
func New() *Service {
	return &Service{now: time.Now, randomID: newAnswerID, FreeformMaxStateBytes: template.MaxMaxStateBytes}
}

func newAnswerID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing means the platform's entropy source is gone;
		// there is no sane fallback that keeps answer ids unpredictable.
		panic("service: reading random answer id: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Service) newID() string {
	if s.randomID != nil {
		return s.randomID()
	}
	return newAnswerID()
}

// Ask answers one typed question, or refuses it, or answers "unanswered"
// with a reason. See the package doc for the order this follows.
func (s *Service) Ask(ctx context.Context, caller Caller, req AskRequest) (Result, *Refusal) {
	if req.Question != nil {
		return s.askFreeform(ctx, caller, req)
	}
	return s.askTemplate(ctx, caller, req)
}

func (s *Service) askFreeform(ctx context.Context, caller Caller, req AskRequest) (Result, *Refusal) {
	if !s.AllowFreeform {
		s.Journal.Refused(caller.AgentID, req.RunID, record.RefusedData{
			Reason: "freeform_disabled",
		})
		return Result{}, refusal("freeform_disabled", 403,
			"freeform questions are switched off. Set TYPRYX_ALLOW_FREEFORM=1 to enable them")
	}
	q := req.Question
	tmpl := template.Template{
		ID:           "freeform",
		Type:         template.Type(q.Type),
		Instructions: q.Instructions,
		Criteria:     q.Criteria,
		// A freeform question sends the whole state (NewFreeformEgress
		// below ignores Fields entirely); this only satisfies
		// Template.Validate()'s non-empty requirement.
		Fields: []string{"*"},
	}
	// Validate the question's own shape (instructions, criteria) and build
	// the backend Question BEFORE the cap is taken and before the state is
	// even looked at: a caller sending malformed freeform questions must
	// not be able to spend the deployment's real hourly budget for free,
	// and every refusal, this one included, reaches the journal.
	if err := tmpl.Validate(); err != nil {
		return s.badQuestion(caller, req, tmpl.ID, err)
	}
	question, keys, err := questionFor(tmpl)
	if err != nil {
		return s.badQuestion(caller, req, tmpl.ID, err)
	}
	// The whole state is sent for a freeform question: there is no template
	// to name a field allowlist, and the operator switched this on knowingly.
	eg, err := template.NewFreeformEgress(req.State, s.freeformMax())
	if err != nil {
		return s.badState(caller, req, "freeform", "", err)
	}
	// version = digest of the question itself, since there is no file on
	// disk to version.
	version := freeformVersion(q)
	return s.ask(ctx, caller, req, tmpl, version, eg, 0, question, keys)
}

// badQuestion refuses a template whose shape does not parse into an askable
// question: instructions, criteria, or the type itself. In practice this is
// the freeform path; a template loaded through template.LoadDir was already
// Validate()'d before it ever reached the registry, so askTemplate's own
// call to this is defensive rather than reachable in normal operation.
func (s *Service) badQuestion(caller Caller, req AskRequest, templateID string, err error) (Result, *Refusal) {
	s.Journal.Refused(caller.AgentID, req.RunID, record.RefusedData{
		Template: templateID, Reason: "bad_question",
	})
	return Result{}, refusal("bad_question", 400, "%s", err.Error())
}

func freeformVersion(q *FreeformQuestion) string {
	t := template.Template{ID: "freeform", Type: template.Type(q.Type), Instructions: q.Instructions, Criteria: q.Criteria, Fields: []string{"*"}}
	return t.Version()
}

func (s *Service) freeformMax() int {
	if s.FreeformMaxStateBytes <= 0 {
		return template.MaxMaxStateBytes
	}
	return s.FreeformMaxStateBytes
}

func (s *Service) askTemplate(ctx context.Context, caller Caller, req AskRequest) (Result, *Refusal) {
	tmpl, ok := s.Templates.Get(req.Template)
	if !ok {
		s.Journal.Refused(caller.AgentID, req.RunID, record.RefusedData{
			Template: req.Template,
			Reason:   "unknown_template",
		})
		return Result{}, refusal("unknown_template", 404, "no template named %q is registered", req.Template)
	}

	eg, heldBack, err := template.Filter(tmpl, req.State)
	if err != nil {
		return s.badState(caller, req, tmpl.ID, tmpl.Version(), err)
	}
	// A template loaded through template.LoadDir was already Validate()'d,
	// so questionFor here should never fail; it is still called before the
	// cap for the same reason the freeform path is: nothing that reaches
	// the backend consumes the cap ahead of being fully validated.
	question, keys, err := questionFor(tmpl)
	if err != nil {
		return s.badQuestion(caller, req, tmpl.ID, err)
	}
	return s.ask(ctx, caller, req, tmpl, tmpl.Version(), eg, heldBack, question, keys)
}

func (s *Service) badState(caller Caller, req AskRequest, templateID, version string, err error) (Result, *Refusal) {
	var tooLarge *template.StateTooLargeError
	code, status := "bad_state", 400
	if asStateTooLarge(err, &tooLarge) {
		code, status = "state_too_large", 413
	}
	s.Journal.Refused(caller.AgentID, req.RunID, record.RefusedData{
		Template: templateID, TemplateVersion: version, Reason: code,
	})
	return Result{}, refusal(code, status, "%s", err.Error())
}

func asStateTooLarge(err error, target **template.StateTooLargeError) bool {
	if e, ok := err.(*template.StateTooLargeError); ok {
		*target = e
		return true
	}
	return false
}

// ask is the common tail once a template (real or freeform), its egress and
// its already-validated Question/keys are known: cap, backend call under
// timeout, probability validation, ledger, journal. The caller (askTemplate
// or askFreeform) has already called questionFor and handled its error, so
// nothing here can fail for a reason that should have been caught before the
// cap was taken.
func (s *Service) ask(ctx context.Context, caller Caller, req AskRequest, tmpl template.Template, version string, eg template.Egress, heldBack int, q backend.Question, keys []string) (Result, *Refusal) {
	if s.Cap != nil && !s.Cap.take() {
		s.Journal.Refused(caller.AgentID, req.RunID, record.RefusedData{
			Template: tmpl.ID, TemplateVersion: version, Reason: "over_hourly_cap",
		})
		return Result{}, refusal("over_hourly_cap", 429,
			"this deployment's hourly call cap is spent")
	}

	answerID := s.newID()
	bctx, cancel := context.WithTimeout(ctx, s.timeout())
	defer cancel()

	start := s.clock()
	ans, usage, askErr := s.Backend.Ask(bctx, q, eg)
	latency := s.clock().Sub(start)

	unansweredData := record.UnansweredData{
		Template: tmpl.ID, TemplateVersion: version, Backend: s.Backend.Name(),
		StateSHA384: eg.SHA384(), HeldBackFields: heldBack, AnswerID: answerID,
	}

	if askErr != nil {
		reason := "backend_error"
		var ue *backend.UnansweredError
		switch {
		case ctx.Err() != nil:
			// The CALLER's own context, not bctx (which wraps it and would
			// report the same Canceled the instant the parent does): the
			// other end hung up, which is a different fact from our own
			// deadline firing and must not be logged as one.
			reason = "canceled"
		case bctx.Err() == context.DeadlineExceeded:
			reason = "timeout"
		case errors.As(askErr, &ue):
			// A backend's own, more specific reason (e.g. no_logprobs,
			// label_mass_too_low, too_many_options) than the generic
			// backend_error every other failure gets. Checked after the
			// timeout/canceled cases so a deadline or a caller cancel always
			// wins, even if a backend happened to also wrap its own reason
			// around a context error.
			reason = ue.Reason
		}
		unansweredData.Reason = reason
		unansweredData.Model = ans.Model
		s.Journal.Unanswered(caller.AgentID, req.RunID, unansweredData)
		return unansweredResult(answerID, tmpl.ID, version, string(tmpl.Type), heldBack, latency, reason), nil
	}

	code, ok := validateProbabilities(ans.Probabilities, keys)
	if !ok {
		unansweredData.Reason = code
		unansweredData.Model = ans.Model
		s.Journal.Unanswered(caller.AgentID, req.RunID, unansweredData)
		return unansweredResult(answerID, tmpl.ID, version, string(tmpl.Type), heldBack, latency, code), nil
	}

	answerValue := deriveAnswer(tmpl.Type, keys, ans.Probabilities)

	if s.Ledger != nil {
		rec := ledger.AnswerRecord{
			AnswerID: answerID, Template: tmpl.ID, TemplateVersion: version,
			Type: string(tmpl.Type), Backend: s.Backend.Name(), Model: ans.Model,
			AnsweredAt: s.clock().UTC().Format(time.RFC3339Nano),
		}
		switch tmpl.Type {
		case template.TypeChoice:
			rec.Options = keys
		case template.TypeScore:
			rec.Levels = len(keys)
		}
		if err := s.Ledger.PutAnswer(rec); err != nil {
			// The answer was already produced; a ledger write failure does
			// not turn a real answer into a refusal. It does mean this
			// answer cannot later be scored, which is counted here rather
			// than silently dropped: LedgerFailures is surfaced at
			// GET /healthz for an operator to see.
			s.ledgerFailures.Add(1)
		}
	}

	s.Journal.Answer(caller.AgentID, req.RunID, record.AnswerData{
		Template: tmpl.ID, TemplateVersion: version, Backend: s.Backend.Name(), Model: ans.Model,
		StateSHA384: eg.SHA384(), HeldBackFields: heldBack, AnswerID: answerID,
		Answer: answerValue, Probabilities: ans.Probabilities,
	})

	return Result{
		AnswerID: answerID, Template: tmpl.ID, TemplateVersion: version, Type: string(tmpl.Type),
		Answer: answerValue, Probabilities: ans.Probabilities, Backend: s.Backend.Name(), Model: ans.Model,
		LatencyMS: latency.Milliseconds(), CostUSD: usage.CostUSD, HeldBackFields: heldBack,
	}, nil
}

func (s *Service) timeout() time.Duration {
	if s.Timeout <= 0 {
		return 2 * time.Second
	}
	return s.Timeout
}

func unansweredResult(answerID, tmplID, version, typ string, heldBack int, latency time.Duration, reason string) Result {
	return Result{
		AnswerID: answerID, Template: tmplID, TemplateVersion: version, Type: typ,
		LatencyMS: latency.Milliseconds(), HeldBackFields: heldBack,
		Unanswered: true, Reason: reason,
	}
}

// deriveAnswer computes the served answer from the ALREADY-VALIDATED
// probability distribution, never from the backend's own Choice/Score/Yes
// fields (backend.Answer documents those as advisory and unread, exactly
// because of this function). keys is in the same order questionFor built it
// in: sorted option names for choice, "0".."n-1" for score. Ties are broken
// towards the first key in that order, which is the lowest sorted option
// name for choice and the lower index for score.
func deriveAnswer(t template.Type, keys []string, probs map[string]float64) any {
	switch t {
	case template.TypeChoice:
		return keys[argmaxIndexOf(keys, probs)]
	case template.TypeScore:
		i := argmaxIndexOf(keys, probs)
		n, err := strconv.Atoi(keys[i])
		if err != nil {
			// keys for a score template are always produced by questionFor
			// as strconv.Itoa(i); a value that does not parse back means
			// questionFor's own contract broke, not a runtime input.
			panic("service: score key did not parse as an integer: " + keys[i])
		}
		return n
	case template.TypeNoul:
		return probs["true"]
	}
	return nil
}

// argmaxIndexOf returns the index into keys of the highest probability,
// ties broken towards the lower index (the first one encountered, since the
// comparison is strict).
func argmaxIndexOf(keys []string, probs map[string]float64) int {
	best := 0
	bestP := -1.0
	for i, k := range keys {
		if p := probs[k]; p > bestP {
			bestP = p
			best = i
		}
	}
	return best
}

// questionFor builds the backend.Question for a template and returns the
// set of probability keys a valid answer must carry.
func questionFor(t template.Template) (backend.Question, []string, error) {
	q := backend.Question{Key: t.ID, Type: t.Type, Instructions: t.Instructions}
	switch t.Type {
	case template.TypeChoice:
		opts, err := t.ChoiceOptions()
		if err != nil {
			return q, nil, err
		}
		keys := make([]string, len(opts))
		for i, o := range opts {
			q.Options = append(q.Options, backend.Option{Name: o.Name, Description: o.Description})
			keys[i] = o.Name
		}
		return q, keys, nil
	case template.TypeScore:
		levels, err := t.ScoreLevels()
		if err != nil {
			return q, nil, err
		}
		q.Levels = levels
		keys := make([]string, len(levels))
		for i := range levels {
			keys[i] = strconv.Itoa(i)
		}
		return q, keys, nil
	case template.TypeNoul:
		trueDesc, falseDesc, _, err := t.NoulCriteria()
		if err != nil {
			return q, nil, err
		}
		q.NoulTrueDesc = trueDesc
		q.NoulFalseDesc = falseDesc
		return q, []string{"true", "false"}, nil
	}
	return q, nil, fmt.Errorf("unknown template type %q", t.Type)
}

// validateProbabilities is the service's own check of what a backend
// answered: never trusted, always verified. Missing or empty is
// "no_probabilities"; present but not covering every expected key, out of
// [0,1], NaN/Inf, or not summing to 1 within 1e-6 is "bad_probabilities".
// There is no renormalizing path: a backend that cannot produce a valid
// distribution gets refused, not corrected.
func validateProbabilities(probs map[string]float64, keys []string) (string, bool) {
	if len(probs) == 0 {
		return "no_probabilities", false
	}
	// The key set must match EXACTLY: an extra key a backend invented, on
	// top of every one of the template's own keys otherwise being valid and
	// summing to 1, must not silently reach the caller as if it belonged.
	if len(probs) != len(keys) {
		return "bad_probabilities", false
	}
	sum := 0.0
	for _, k := range keys {
		v, present := probs[k]
		if !present {
			return "bad_probabilities", false
		}
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1 {
			return "bad_probabilities", false
		}
		sum += v
	}
	if math.Abs(sum-1) > 1e-6 {
		return "bad_probabilities", false
	}
	return "", true
}

// Outcome records a later truth against the answer it belongs to, scored
// against exactly the template version, backend and model recorded when the
// answer was given, never against whatever the registry holds now.
func (s *Service) Outcome(caller Caller, req OutcomeRequest) (OutcomeResult, *Refusal) {
	if s.Ledger == nil {
		return OutcomeResult{}, refusal("no_ledger", 503,
			"no ledger is configured (set TYPRYX_LEDGER_DIR); outcomes cannot be recorded")
	}
	ans, ok := s.Ledger.GetAnswer(req.AnswerID)
	if !ok {
		return OutcomeResult{}, refusal("unknown_answer", 404, "no answer with id %q is on the ledger", req.AnswerID)
	}
	if err := validateTruth(ans, req.Truth); err != nil {
		return OutcomeResult{}, refusal("bad_truth", 400, "%s", err.Error())
	}
	rec := ledger.OutcomeRecord{
		AnswerID: req.AnswerID, Template: ans.Template, TemplateVersion: ans.TemplateVersion,
		Backend: ans.Backend, Model: ans.Model, Truth: req.Truth, Source: req.Source,
		RecordedAt: s.clock().UTC().Format(time.RFC3339Nano),
	}
	if err := s.Ledger.PutOutcome(rec); err != nil {
		if errors.Is(err, ledger.ErrOutcomeExists) {
			return OutcomeResult{}, refusal("outcome_exists", 409,
				"an outcome is already recorded for answer %q; a truth is counted once", req.AnswerID)
		}
		return OutcomeResult{}, refusal("ledger_write_failed", 500, "%s", err.Error())
	}
	return OutcomeResult{AnswerID: req.AnswerID, Template: ans.Template, TemplateVersion: ans.TemplateVersion}, nil
}

func validateTruth(ans ledger.AnswerRecord, truth json.RawMessage) error {
	switch template.Type(ans.Type) {
	case template.TypeChoice:
		var s string
		if err := json.Unmarshal(truth, &s); err != nil {
			return fmt.Errorf("truth for a choice answer must be a string naming one of the recorded options: %w", err)
		}
		for _, o := range ans.Options {
			if o == s {
				return nil
			}
		}
		return fmt.Errorf("truth %q is not one of the options this answer was given under", s)
	case template.TypeScore:
		var n int
		if err := json.Unmarshal(truth, &n); err != nil {
			return fmt.Errorf("truth for a score answer must be an integer: %w", err)
		}
		if n < 0 || n >= ans.Levels {
			return fmt.Errorf("truth %d is out of range for the %d levels this answer was given under", n, ans.Levels)
		}
		return nil
	case template.TypeNoul:
		var b bool
		if err := json.Unmarshal(truth, &b); err != nil {
			return fmt.Errorf("truth for a noul answer must be a boolean: %w", err)
		}
		return nil
	}
	return fmt.Errorf("the recorded answer has an unknown type %q", ans.Type)
}
