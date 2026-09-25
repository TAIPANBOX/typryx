// Package ledger keeps the two append-only NDJSON files an answer's later
// outcome is scored against: answers.ndjson (one record per answer typryx
// gave) and outcomes.ndjson (one record per truth that arrived later).
//
// Every write is O_APPEND and fsynced, and an in-memory index of answer_id ->
// AnswerRecord is built once at Open. A torn last line (a process killed
// mid-write) is skipped and logged rather than refusing to open, because a
// half-written line at the very end is exactly what a crash produces and it
// costs nothing but that one record; a malformed line ANYWHERE ELSE means the
// file is not what this package wrote and refuses to open, because trusting
// an index built over a file with a hole in the middle is worse than
// refusing to start.
package ledger

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

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

// Ledger holds the two files and the answer index.
type Ledger struct {
	mu           sync.Mutex
	answersFile  *os.File
	outcomesFile *os.File
	index        map[string]AnswerRecord

	// SkippedTornLines counts torn last lines skipped at Open, for a caller
	// that wants to log it.
	SkippedTornLines int
}

// Open opens (creating if needed) answers.ndjson and outcomes.ndjson under
// dir, and loads the answer index.
func Open(dir string) (*Ledger, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("ledger: creating %s: %w", dir, err)
	}
	answersPath := filepath.Join(dir, "answers.ndjson")
	outcomesPath := filepath.Join(dir, "outcomes.ndjson")

	index, tornSkipped, err := loadAnswerIndex(answersPath)
	if err != nil {
		return nil, err
	}

	af, err := os.OpenFile(answersPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("ledger: opening %s: %w", answersPath, err)
	}
	of, err := os.OpenFile(outcomesPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		af.Close()
		return nil, fmt.Errorf("ledger: opening %s: %w", outcomesPath, err)
	}
	return &Ledger{
		answersFile:      af,
		outcomesFile:     of,
		index:            index,
		SkippedTornLines: tornSkipped,
	}, nil
}

// loadAnswerIndex reads every line of path (which may not exist yet) and
// returns answer_id -> AnswerRecord. Any malformed line other than the last
// is a hard error; a malformed last line is treated as a torn write, counted
// and skipped.
func loadAnswerIndex(path string) (map[string]AnswerRecord, int, error) {
	index := map[string]AnswerRecord{}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return index, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("ledger: reading %s: %w", path, err)
	}
	defer f.Close()

	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	if err := sc.Err(); err != nil {
		return nil, 0, fmt.Errorf("ledger: scanning %s: %w", path, err)
	}

	torn := 0
	for i, line := range lines {
		var rec AnswerRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			if i == len(lines)-1 {
				// The last line, and only the last line, may be a torn
				// write: a process killed mid-fsync leaves a half-written
				// line at the end of the file, never in the middle.
				torn++
				continue
			}
			return nil, 0, fmt.Errorf("ledger: %s line %d is malformed and is not the last line, "+
				"so this is not a torn write: %w", path, i+1, err)
		}
		if rec.AnswerID == "" {
			if i == len(lines)-1 {
				torn++
				continue
			}
			return nil, 0, fmt.Errorf("ledger: %s line %d has no answer_id", path, i+1)
		}
		index[rec.AnswerID] = rec
	}
	return index, torn, nil
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
func (l *Ledger) PutOutcome(rec OutcomeRecord) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("ledger: marshaling outcome record: %w", err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.outcomesFile.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("ledger: writing outcome record: %w", err)
	}
	return l.outcomesFile.Sync()
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
