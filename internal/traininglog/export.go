package traininglog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// ExportOptions names the two directories an export reads, never writes.
type ExportOptions struct {
	TrainingDir string
	LedgerDir   string
}

// TemplateCounts is what happened to one template's training lines.
type TemplateCounts struct {
	Exported               int
	SkippedNoTruth         int
	SkippedVersionMismatch int
	SkippedBadTruth        int
	SkippedDuplicateID     int
}

// ExportReport is the tally of one export. Templates is keyed by template id
// and holds an entry for every template that had at least one well-formed
// training line.
type ExportReport struct {
	Templates map[string]*TemplateCounts
	// MalformedLines counts lines in either file that could not be read as
	// what they should be: not JSON, not an object, or missing a field the
	// join needs. They are skipped, never repaired.
	MalformedLines int
	// TornLines counts a last line with no trailing newline, in either file.
	// It is counted and left on disk: the service may still be appending.
	TornLines int
	// OrphanTruth counts truths whose answer has no line in the training log
	// (an answer given before the log was switched on, for one).
	OrphanTruth int
}

// exportRow is one line of the export: the egressed state and the human
// truth for it, and nothing else.
type exportRow struct {
	Template        string          `json:"template"`
	TemplateVersion string          `json:"template_version"`
	Type            string          `json:"type"`
	State           json.RawMessage `json:"state"`
	Label           json.RawMessage `json:"label"`
}

// trainingIn is the part of a training line an export reads.
type trainingIn struct {
	AnswerID        string          `json:"answer_id"`
	Template        string          `json:"template"`
	TemplateVersion string          `json:"template_version"`
	Type            string          `json:"type"`
	State           json.RawMessage `json:"state"`
}

// outcomeIn is the part of an outcomes.ndjson line an export reads. It is
// deliberately NOT internal/ledger's record type: this package imports
// nothing that knows what a backend answered.
type outcomeIn struct {
	AnswerID        string          `json:"answer_id"`
	Template        string          `json:"template"`
	TemplateVersion string          `json:"template_version"`
	Truth           json.RawMessage `json:"truth"`
}

// Export joins training.ndjson (under opts.TrainingDir) with outcomes.ndjson
// (under opts.LedgerDir) by answer_id and writes one JSONL row to w for every
// answer that has a human truth: {template, template_version, type, state,
// label}, where label is the truth and nothing else.
//
// It never reads answers.ndjson, where a backend's served answer and
// probabilities live, so a backend answer (a Jev answer above all) cannot
// become a label: with no truth posted for an answer, that answer is skipped.
//
// The join rules, in the order they apply to each well-formed training line:
//
//  1. An answer_id that appears on more than one training line: every such
//     line is skipped (skipped_duplicate_id).
//  2. No truth for the id: skipped_no_truth.
//  3. More than one truth for the id: skipped_duplicate_id. Which one is the
//     human's word is not for an export to guess.
//  4. The truth names a different template or template_version than the
//     training line: skipped_version_mismatch. The service scores a truth
//     against the version the answer was asked under, so a mismatch means a
//     file was edited or mixed up.
//  5. The truth is not the shape the type demands (choice: a string; score: a
//     non-negative integer; noul: true or false): skipped_bad_truth.
//
// Both directories are only read. A torn last line is counted and left where
// it is.
func Export(opts ExportOptions, w io.Writer) (ExportReport, error) {
	rep := ExportReport{Templates: map[string]*TemplateCounts{}}

	for _, d := range []string{opts.TrainingDir, opts.LedgerDir} {
		fi, err := os.Stat(d)
		if err != nil {
			return rep, fmt.Errorf("traininglog: %w", err)
		}
		if !fi.IsDir() {
			return rep, fmt.Errorf("traininglog: %s is not a directory", d)
		}
	}
	trainingPath := filepath.Join(opts.TrainingDir, FileName)
	if _, err := os.Stat(trainingPath); err != nil {
		return rep, fmt.Errorf("traininglog: no %s in %s (is the training log switched on there?): %w", FileName, opts.TrainingDir, err)
	}

	trainLines, torn, err := splitLines(trainingPath)
	if err != nil {
		return rep, err
	}
	rep.TornLines += torn
	outLines, torn, err := splitLines(filepath.Join(opts.LedgerDir, "outcomes.ndjson"))
	if err != nil {
		return rep, err
	}
	rep.TornLines += torn

	var training []trainingIn
	trainSeen := map[string]int{}
	for _, ln := range trainLines {
		var t trainingIn
		if err := json.Unmarshal(ln, &t); err != nil || !validTraining(t) {
			rep.MalformedLines++
			continue
		}
		training = append(training, t)
		trainSeen[t.AnswerID]++
	}

	truths := map[string][]outcomeIn{}
	for _, ln := range outLines {
		var o outcomeIn
		if err := json.Unmarshal(ln, &o); err != nil || o.AnswerID == "" {
			rep.MalformedLines++
			continue
		}
		truths[o.AnswerID] = append(truths[o.AnswerID], o)
		if trainSeen[o.AnswerID] == 0 {
			rep.OrphanTruth++
		}
	}

	for _, t := range training {
		c := rep.Templates[t.Template]
		if c == nil {
			c = &TemplateCounts{}
			rep.Templates[t.Template] = c
		}
		if trainSeen[t.AnswerID] > 1 {
			c.SkippedDuplicateID++
			continue
		}
		ts := truths[t.AnswerID]
		if len(ts) == 0 {
			c.SkippedNoTruth++
			continue
		}
		if len(ts) > 1 {
			c.SkippedDuplicateID++
			continue
		}
		o := ts[0]
		if o.Template != t.Template || o.TemplateVersion != t.TemplateVersion {
			c.SkippedVersionMismatch++
			continue
		}
		if !truthFits(t.Type, o.Truth) {
			c.SkippedBadTruth++
			continue
		}
		row, err := json.Marshal(exportRow{
			Template: t.Template, TemplateVersion: t.TemplateVersion, Type: t.Type,
			State: t.State, Label: o.Truth,
		})
		if err != nil {
			// A RawMessage that parsed as part of a valid line marshals; a
			// failure here means a line that was valid a moment ago is not.
			rep.MalformedLines++
			continue
		}
		if _, err := w.Write(append(row, '\n')); err != nil {
			return rep, fmt.Errorf("traininglog: writing the export: %w", err)
		}
		c.Exported++
	}
	return rep, nil
}

func validTraining(t trainingIn) bool {
	if t.AnswerID == "" || t.Template == "" || t.TemplateVersion == "" {
		return false
	}
	switch t.Type {
	case "choice", "score", "noul":
	default:
		return false
	}
	st := bytes.TrimSpace(t.State)
	return len(st) > 0 && st[0] == '{'
}

// truthFits reports whether truth is the JSON shape a human truth for typ has
// on /v1/outcome: choice a string, score a non-negative whole number, noul a
// boolean.
func truthFits(typ string, truth json.RawMessage) bool {
	tr := bytes.TrimSpace(truth)
	if len(tr) == 0 {
		return false
	}
	switch typ {
	case "choice":
		var s string
		return tr[0] == '"' && json.Unmarshal(tr, &s) == nil
	case "score":
		n, err := strconv.ParseInt(string(tr), 10, 64)
		return err == nil && n >= 0
	case "noul":
		return string(tr) == "true" || string(tr) == "false"
	}
	return false
}

// splitLines reads path once and returns its complete, non-empty lines plus 1
// when its last line was torn (no trailing newline, non-empty after
// trimming). A missing file has no lines. The file is never modified.
func splitLines(path string) ([][]byte, int, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- operator-supplied directory, read-only
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("traininglog: reading %s: %w", path, err)
	}
	torn := 0
	if len(data) > 0 && data[len(data)-1] != '\n' {
		idx := bytes.LastIndexByte(data, '\n')
		if len(bytes.TrimSpace(data[idx+1:])) > 0 {
			torn = 1
		}
		data = data[:idx+1]
	}
	var lines [][]byte
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		lines = append(lines, append([]byte(nil), line...))
	}
	if err := sc.Err(); err != nil {
		return nil, torn, fmt.Errorf("traininglog: scanning %s: %w", path, err)
	}
	return lines, torn, nil
}
