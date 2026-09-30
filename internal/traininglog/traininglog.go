// Package traininglog is the opt-in local training log: one NDJSON line per
// ANSWERED ask, holding the question's identity and the state that actually
// left the box, and nothing a backend said.
//
// It exists so a customer can later pair those states with the truths a human
// posted to /v1/outcome and fine-tune their own model on their own hardware.
// typryx does not train or ship a model.
//
// What a line may carry is fixed by the Line type: there is no field that
// could hold a backend's answer or probabilities, and this package imports
// neither internal/backend nor internal/ledger, so it cannot even name them
// (TestThePackagesCannotSeeABackendAnswer). The state is whatever
// template.Egress carried, handed in as bytes by internal/service: the
// template's `fields` allowlist has already run.
//
// The file is written like internal/ledger's: O_APPEND, fsync per write, a
// torn last line truncated at Open.
package traininglog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// FileName is the one file this package writes, inside the training dir.
const FileName = "training.ndjson"

// Line is one answered ask, as written to training.ndjson.
type Line struct {
	AnswerID        string `json:"answer_id"`
	AnsweredAt      string `json:"answered_at"`
	Template        string `json:"template"`
	TemplateVersion string `json:"template_version"`
	Type            string `json:"type"`
	// State is the egressed state only: a JSON object of exactly the fields
	// the template's allowlist let through.
	State   json.RawMessage `json:"state"`
	Backend string          `json:"backend"`
	Model   string          `json:"model"`
}

// Log is an open training log.
type Log struct {
	mu        sync.Mutex
	file      *os.File
	tornBytes int
}

// Open opens (creating if needed) training.ndjson under dir. dir is created
// 0700 and the file 0600: both hold a customer's own data. A torn last line
// (a crash mid-write) is truncated off the file ON DISK first, for the reason
// internal/ledger does the same: the next O_APPEND write would otherwise land
// right after the fragment and merge into one malformed line.
//
// A complete line that is malformed is NOT a reason to refuse to open, unlike
// the ledger: every line here stands alone, nothing is indexed, and an
// optional log must never stop the service from starting. The export counts
// and skips such a line.
func Open(dir string) (*Log, error) {
	// dir comes from TYPRYX_TRAINING_DIR, an operator-supplied path read once
	// at startup, the same shape internal/ledger documents.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("traininglog: creating %s: %w", dir, err)
	}
	path := filepath.Join(dir, FileName)
	torn, err := truncateTornTail(path)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- operator-supplied path, see above
	if err != nil {
		return nil, fmt.Errorf("traininglog: opening %s: %w", path, err)
	}
	return &Log{file: f, tornBytes: torn}, nil
}

// truncateTornTail cuts a last line with no trailing newline off path and
// returns how many bytes it dropped: 0 for a missing or empty file or one
// that already ends in a newline.
func truncateTornTail(path string) (int, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- operator-supplied path, see Open
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("traininglog: reading %s: %w", path, err)
	}
	if len(data) == 0 || data[len(data)-1] == '\n' {
		return 0, nil
	}
	valid := bytes.LastIndexByte(data, '\n') + 1
	if err := os.Truncate(path, int64(valid)); err != nil {
		return 0, fmt.Errorf("traininglog: truncating a torn tail from %s: %w", path, err)
	}
	return len(data) - valid, nil
}

// TornBytes is how many bytes Open dropped truncating a torn tail.
func (l *Log) TornBytes() int { return l.tornBytes }

// Put appends one line and fsyncs it.
func (l *Log) Put(line Line) error {
	b, err := json.Marshal(line)
	if err != nil {
		return fmt.Errorf("traininglog: marshaling a line: %w", err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.file.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("traininglog: writing a line: %w", err)
	}
	if err := l.file.Sync(); err != nil {
		return fmt.Errorf("traininglog: syncing the file: %w", err)
	}
	return nil
}

// Close closes the file.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Close()
}
