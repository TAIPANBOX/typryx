// Package ledger keeps the two append-only NDJSON files an answer's later
// outcome is scored against: answers.ndjson (one record per answer typryx
// gave) and outcomes.ndjson (one record per truth that arrived later).
//
// Every write is O_APPEND and fsynced, and an in-memory index of answer_id ->
// AnswerRecord (plus the set of answer ids that already have an outcome) is
// built once at Open. A torn last line (a process killed mid-write, leaving
// a line with no trailing newline) is truncated off the file ON DISK at
// Open, before the file is reopened for append: left in place, the next
// O_APPEND write would land immediately after the fragment with no
// separator, merging into one malformed line that is no longer last and so
// refuses the NEXT restart. A malformed line that DOES end in a newline is
// real corruption, not a crash artifact, and refuses to open.
package ledger

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ErrOutcomeExists is returned by PutOutcome when an outcome has already
// been recorded for the given answer id: a truth is counted once, even
// across a restart.
var ErrOutcomeExists = errors.New("ledger: an outcome already exists for this answer")

// AnswerRecord is one answered ask, as written to answers.ndjson.
//
// It carries enough of the template it was asked under (its version, and the
// shape needed to validate a later truth) that an outcome can be scored
// against exactly that version even after the template file on disk has
// changed or been removed.
type AnswerRecord struct {
	AnswerID        string   `json:"answer_id"`
	Template        string   `json:"template"`
	TemplateVersion string   `json:"template_version"`
	Type            string   `json:"type"`
	Backend         string   `json:"backend"`
	Model           string   `json:"model"`
	Options         []string `json:"options,omitempty"` // choice: the valid option names
	Levels          int      `json:"levels,omitempty"`  // score: number of levels (truth in 0..Levels-1)
	AnsweredAt      string   `json:"answered_at"`
}

// OutcomeRecord is one later truth, as written to outcomes.ndjson.
type OutcomeRecord struct {
	AnswerID        string          `json:"answer_id"`
	Template        string          `json:"template"`
	TemplateVersion string          `json:"template_version"`
	Backend         string          `json:"backend"`
	Model           string          `json:"model"`
	Truth           json.RawMessage `json:"truth"`
	Source          string          `json:"source"`
	RecordedAt      string          `json:"recorded_at"`
}

// Ledger holds the two files, the answer index, and the set of answer ids
// that already have a recorded outcome.
type Ledger struct {
	mu            sync.Mutex
	answersFile   *os.File
	outcomesFile  *os.File
	index         map[string]AnswerRecord
	outcomeExists map[string]bool

	// SkippedTornLines counts the files (0, 1 or 2) that had a torn last
	// line truncated at Open, for a caller that wants to log it.
	SkippedTornLines int
	// TornBytes is the total number of bytes dropped truncating torn tails
	// across both files.
	TornBytes int
}

// Open opens (creating if needed) answers.ndjson and outcomes.ndjson under
// dir, truncates a torn tail off either file first, and loads the answer
// index and the outcome-exists set.
func Open(dir string) (*Ledger, error) {
	// dir comes from TYPRYX_LEDGER_DIR, an operator-supplied path read once
	// at startup, the same shape vouchryx's own gosec suppression documents
	// for its operator-config reads. 0o750/0o600 rather than the more
	// permissive defaults: the ledger holds a hash of every egressed field
	// and the answers/outcomes an operator asked for, not public output.
	if err := os.MkdirAll(dir, 0o750); err != nil { // #nosec G301 -- operator-supplied path, see above
		return nil, fmt.Errorf("ledger: creating %s: %w", dir, err)
	}
	answersPath := filepath.Join(dir, "answers.ndjson")
	outcomesPath := filepath.Join(dir, "outcomes.ndjson")

	answersTorn, err := truncateTornTail(answersPath)
	if err != nil {
		return nil, err
	}
	outcomesTorn, err := truncateTornTail(outcomesPath)
	if err != nil {
		return nil, err
	}

	index, err := parseAnswerLines(answersPath)
	if err != nil {
		return nil, err
	}
	outcomeExists, err := parseOutcomeAnswerIDs(outcomesPath)
	if err != nil {
		return nil, err
	}

	af, err := os.OpenFile(answersPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 G302 -- operator-supplied path, see above
	if err != nil {
		return nil, fmt.Errorf("ledger: opening %s: %w", answersPath, err)
	}
	of, err := os.OpenFile(outcomesPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 G302 -- operator-supplied path, see above
	if err != nil {
		_ = af.Close()
		return nil, fmt.Errorf("ledger: opening %s: %w", outcomesPath, err)
	}

	tornFiles := 0
	if answersTorn > 0 {
		tornFiles++
	}
	if outcomesTorn > 0 {
		tornFiles++
	}
	return &Ledger{
		answersFile:      af,
		outcomesFile:     of,
		index:            index,
		outcomeExists:    outcomeExists,
		SkippedTornLines: tornFiles,
		TornBytes:        answersTorn + outcomesTorn,
	}, nil
}

// truncateTornTail removes a torn last write (a line with no trailing
// newline, the shape a crash mid-fsync leaves) from path, so a later
// O_APPEND write lands cleanly after the last complete line instead of
// merging with the wreckage. Returns the number of bytes dropped: 0 when the
// file does not exist yet (not a torn state) or already ends in a newline.
func truncateTornTail(path string) (int, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- operator-supplied path, see Open
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("ledger: reading %s: %w", path, err)
	}
	if len(data) == 0 || data[len(data)-1] == '\n' {
		return 0, nil
	}
	lastNL := bytes.LastIndexByte(data, '\n')
	validLen := lastNL + 1 // -1+1 == 0 when the whole file is one torn fragment
	tornBytes := len(data) - validLen
	if err := os.Truncate(path, int64(validLen)); err != nil {
		return 0, fmt.Errorf("ledger: truncating a torn tail from %s: %w", path, err)
	}
	return tornBytes, nil
}

// parseAnswerLines reads path, already torn-tail-truncated by Open, and
// returns answer_id -> AnswerRecord. With the torn tail already removed,
// every remaining line is expected to be well-formed; anything malformed
// here is real corruption, not a crash artifact, and refuses to open.
func parseAnswerLines(path string) (map[string]AnswerRecord, error) {
	index := map[string]AnswerRecord{}
	err := scanLines(path, func(lineNum int, line string) error {
		var rec AnswerRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return fmt.Errorf("ledger: %s line %d is malformed: %w", path, lineNum, err)
		}
		if rec.AnswerID == "" {
			return fmt.Errorf("ledger: %s line %d has no answer_id", path, lineNum)
		}
		index[rec.AnswerID] = rec
		return nil
	})
	return index, err
}

// parseOutcomeAnswerIDs reads path, already torn-tail-truncated by Open, and
// returns the set of answer ids that already have a recorded outcome.
func parseOutcomeAnswerIDs(path string) (map[string]bool, error) {
	seen := map[string]bool{}
	err := scanLines(path, func(lineNum int, line string) error {
		var rec OutcomeRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return fmt.Errorf("ledger: %s line %d is malformed: %w", path, lineNum, err)
		}
		if rec.AnswerID == "" {
			return fmt.Errorf("ledger: %s line %d has no answer_id", path, lineNum)
		}
		seen[rec.AnswerID] = true
		return nil
	})
	return seen, err
}

// scanLines calls fn for every non-empty line of path, 1-indexed. A missing
// file is not an error: it simply has no lines yet.
func scanLines(path string, fn func(lineNum int, line string) error) error {
	f, err := os.Open(path) // #nosec G304 -- operator-supplied path, see Open
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("ledger: reading %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	lineNum := 0
	for sc.Scan() {
		lineNum++
		line := sc.Text()
		if line == "" {
			continue
		}
		if err := fn(lineNum, line); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("ledger: scanning %s: %w", path, err)
	}
	return nil
}

// PutAnswer appends an answer record and adds it to the index.
func (l *Ledger) PutAnswer(rec AnswerRecord) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("ledger: marshaling answer record: %w", err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.answersFile.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("ledger: writing answer record: %w", err)
	}
	if err := l.answersFile.Sync(); err != nil {
		return fmt.Errorf("ledger: syncing answers file: %w", err)
	}
	l.index[rec.AnswerID] = rec
	return nil
}

// PutOutcome appends an outcome record.
// PutOutcome appends an outcome record, refusing ErrOutcomeExists if one is
// already recorded for rec.AnswerID: a truth is counted once, and the check
// is against the index built at Open, so it holds across a restart.
func (l *Ledger) PutOutcome(rec OutcomeRecord) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("ledger: marshaling outcome record: %w", err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.outcomeExists[rec.AnswerID] {
		return ErrOutcomeExists
	}
	if _, err := l.outcomesFile.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("ledger: writing outcome record: %w", err)
	}
	if err := l.outcomesFile.Sync(); err != nil {
		return fmt.Errorf("ledger: syncing outcomes file: %w", err)
	}
	l.outcomeExists[rec.AnswerID] = true
	return nil
}

// GetAnswer looks an answer up by id.
func (l *Ledger) GetAnswer(id string) (AnswerRecord, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, ok := l.index[id]
	return rec, ok
}

// Close closes both files.
func (l *Ledger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	err1 := l.answersFile.Close()
	err2 := l.outcomesFile.Close()
	if err1 != nil {
		return err1
	}
	return err2
}
