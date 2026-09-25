// Package record writes what typryx did into the estate's shared agent-event
// envelope.
//
// Adapted from scopyx internal/record/record.go: its own journal, one
// writer, never the shared log; an event with no agent identity is skipped
// and counted rather than given a fabricated agent_id (agent-passport SPEC
// 6.1); the state itself never reaches the record, only a hash of what was
// actually sent, because the state is arbitrary caller data and the record
// is meant to be kept.
package record

import (
	"strings"
	"sync"
	"time"

	"github.com/TAIPANBOX/agent-stack-go/event"
)

// Schema and source, per agent-passport SPEC 6.
const (
	Schema = event.SchemaV10
	Source = "typryx"
)

// Event types and their fixed severities.
//
// Fixed in code rather than chosen per call site, so a downstream count of
// "how many high events" measures what happened rather than who wrote the
// call. calibration_drift is declared here but not emitted in this phase: it
// belongs to the calibration command (a later phase) and is listed now so the
// severity is fixed before anything emits it.
const (
	TypeAnswer           = "typed_answer"
	TypeUnanswered       = "typed_unanswered"
	TypeRefused          = "typed_refused"
	TypeCalibrationDrift = "calibration_drift"
	severityAnswer       = event.SeverityInfo
	severityUnanswered   = event.SeverityMedium
	severityRefused      = event.SeverityHigh
	severityCalibDrift   = event.SeverityHigh
)

// Outcome is what happened to one emit attempt.
type Outcome int

const (
	// Written: one line reached the journal.
	Written Outcome = iota
	// Disabled: no journal is configured. The default, and free.
	Disabled
	// SkippedNoAgentID: no agent identity, so nothing was written. SPEC 6.1
	// forbids a fabricated agent_id; skipped and counted is the honest
	// alternative, and the count is what keeps it from being silent.
	SkippedNoAgentID
	// WriteFailed: the journal could not be appended to. Fail-open: an
	// answer is not refused because its record could not be written.
	WriteFailed
)

// Journal is typryx's own append-only event log. The zero value is a
// disabled journal, which is the default.
type Journal struct {
	mu      sync.Mutex
	w       *event.ChainedWriter
	skipped int
	failed  int
}

// Open starts a journal at path. An empty path returns a disabled journal
// rather than an error: not wanting a journal is a configuration, not a
// fault.
func Open(path string) (*Journal, error) {
	if strings.TrimSpace(path) == "" {
		return &Journal{}, nil
	}
	w, err := event.NewChainedWriter(path)
	if err != nil {
		return nil, err
	}
	return &Journal{w: w}, nil
}

// Close flushes and closes.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.w == nil {
		return nil
	}
	return j.w.Close()
}

// AnswerData is what an answered ask records.
type AnswerData struct {
	Template        string
	TemplateVersion string
	Backend         string
	Model           string
	StateSHA384     string
	HeldBackFields  int
	AnswerID        string
	Answer          any
	Probabilities   map[string]float64
}

// Answer records one answered ask.
func (j *Journal) Answer(agentID, runID string, d AnswerData) Outcome {
	data := map[string]any{
		"template":         d.Template,
		"template_version": d.TemplateVersion,
		"backend":          d.Backend,
		"model":            d.Model,
		"state_sha384":     d.StateSHA384,
		"held_back_fields": d.HeldBackFields,
		"answer_id":        d.AnswerID,
		"answer":           d.Answer,
		"probabilities":    d.Probabilities,
	}
	return j.emit(TypeAnswer, severityAnswer, agentID, runID, data)
}

// UnansweredData is what an unanswered ask records.
type UnansweredData struct {
	Template        string
	TemplateVersion string
	Backend         string
	Model           string
	StateSHA384     string
	HeldBackFields  int
	AnswerID        string
	Reason          string
}

// Unanswered records one ask that reached a backend but got no usable
// answer.
func (j *Journal) Unanswered(agentID, runID string, d UnansweredData) Outcome {
	data := map[string]any{
		"template":         d.Template,
		"template_version": d.TemplateVersion,
		"backend":          d.Backend,
		"model":            d.Model,
		"state_sha384":     d.StateSHA384,
		"held_back_fields": d.HeldBackFields,
		"answer_id":        d.AnswerID,
		"reason":           d.Reason,
	}
	return j.emit(TypeUnanswered, severityUnanswered, agentID, runID, data)
}

// RefusedData is what a refused ask records. Template and TemplateVersion may
// be empty when the refusal happened before a template was even resolved
// (an unknown template id, a freeform question with freeform off).
type RefusedData struct {
	Template        string
	TemplateVersion string
	Reason          string
}

// Refused records one ask that never reached a backend at all.
func (j *Journal) Refused(agentID, runID string, d RefusedData) Outcome {
	data := map[string]any{
		"template":         d.Template,
		"template_version": d.TemplateVersion,
		"reason":           d.Reason,
	}
	return j.emit(TypeRefused, severityRefused, agentID, runID, data)
}

func (j *Journal) emit(kind, severity, agentID, runID string, data map[string]any) Outcome {
	j.mu.Lock()
	defer j.mu.Unlock()

	if j.w == nil {
		return Disabled
	}
	if strings.TrimSpace(agentID) == "" {
		j.skipped++
		return SkippedNoAgentID
	}
	e := event.Event{
		Schema:   Schema,
		TS:       time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Source:   Source,
		Type:     kind,
		AgentID:  agentID,
		Severity: severity,
		RunID:    runID,
		Data:     data,
	}
	if err := j.w.Write(e); err != nil {
		j.failed++
		return WriteFailed
	}
	return Written
}

// Counts reports how many events were skipped for want of an identity and how
// many failed to write. Exposed at GET /healthz and logged at shutdown: each
// number means "this journal is not the whole story."
func (j *Journal) Counts() (skipped, failed int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.skipped, j.failed
}
