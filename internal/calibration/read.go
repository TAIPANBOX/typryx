// Package calibration reads the two ledger files (answers.ndjson,
// outcomes.ndjson) a running typryx may still be appending to, and reports,
// per template x backend x model group, whether that group's probabilities
// can be trusted: an accuracy, a mean confidence, a Brier score, and a
// 10-bin reliability diagram (ECE).
//
// It is READ-ONLY. The service may be running and appending to these files
// concurrently, so this package never truncates, rewrites, or locks them: it
// takes one os.ReadFile snapshot of each and works from that. A torn last
// line (the shape a crash mid-write, or a concurrent writer caught
// mid-append, leaves) is skipped and counted, never truncated on disk the
// way internal/ledger.Open does at startup; a malformed line anywhere else
// is also skipped and counted rather than refusing to open, because a
// calibration report has to come out even over an imperfect ledger.
package calibration

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/TAIPANBOX/typryx/internal/ledger"
)

// readCounts is what one file's read-only scan found besides its records.
type readCounts struct {
	malformed int
	torn      int
}

// splitLines reads path once and returns its complete, non-empty lines
// (trimmed of surrounding whitespace) plus whether its last line was torn (no
// trailing newline, and non-empty after trimming). A missing file is not an
// error: it simply has no lines yet. The file itself is never modified.
func splitLines(path string) ([][]byte, int, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- operator-supplied TYPRYX_LEDGER_DIR, read-only, see package doc
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("calibration: reading %s: %w", path, err)
	}

	torn := 0
	if len(data) > 0 && data[len(data)-1] != '\n' {
		idx := bytes.LastIndexByte(data, '\n')
		tail := data[idx+1:]
		if len(bytes.TrimSpace(tail)) > 0 {
			torn = 1
		}
		data = data[:idx+1] // idx==-1 -> data[:0]: the whole file was one torn fragment
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
		// A line wider than the scanner's buffer, or a similar read fault:
		// counted as malformed by the caller would be wrong (it is not one
		// line, it is the whole scan), so this is the one case read.go itself
		// surfaces as an error rather than a count.
		return nil, torn, fmt.Errorf("calibration: scanning %s: %w", path, err)
	}
	return lines, torn, nil
}

// readAnswers returns every well-formed answer record in path, keyed by
// answer_id (a later duplicate id overwrites an earlier one, the same last-
// write-wins behavior internal/ledger's own index has), and how many lines
// were malformed or torn.
func readAnswers(path string) (map[string]ledger.AnswerRecord, readCounts, error) {
	lines, torn, err := splitLines(path)
	if err != nil {
		return nil, readCounts{}, err
	}
	out := map[string]ledger.AnswerRecord{}
	counts := readCounts{torn: torn}
	for _, line := range lines {
		var rec ledger.AnswerRecord
		if err := json.Unmarshal(line, &rec); err != nil || rec.AnswerID == "" {
			counts.malformed++
			continue
		}
		out[rec.AnswerID] = rec
	}
	return out, counts, nil
}

// readOutcomes returns every well-formed outcome record in path, in file
// order, and how many lines were malformed or torn.
func readOutcomes(path string) ([]ledger.OutcomeRecord, readCounts, error) {
	lines, torn, err := splitLines(path)
	if err != nil {
		return nil, readCounts{}, err
	}
	var out []ledger.OutcomeRecord
	counts := readCounts{torn: torn}
	for _, line := range lines {
		var rec ledger.OutcomeRecord
		if err := json.Unmarshal(line, &rec); err != nil || rec.AnswerID == "" {
			counts.malformed++
			continue
		}
		out = append(out, rec)
	}
	return out, counts, nil
}
