package calibration

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strconv"
)

// numBins is the number of equal-width confidence bins ECE sums over:
// [0,0.1), [0.1,0.2), ..., [0.9,1.0], with 1.0 landing in the last bin.
const numBins = 10

// Options configures one calibration run.
type Options struct {
	LedgerDir string
	// MinN is the minimum number of scored items a group needs before it
	// gets a verdict at all. Below it: "insufficient", no bound is judged.
	MinN int
	// MaxBrier and MaxECE are the drift bounds. nil means "no bound set" for
	// that metric: a group can never drift on a metric with no bound.
	MaxBrier *float64
	MaxECE   *float64
}

// Bin is one of the 10 equal-width confidence bins of a group's reliability
// diagram.
type Bin struct {
	N              int     `json:"n"`
	MeanConfidence float64 `json:"mean_confidence"`
	Accuracy       float64 `json:"accuracy"`
}

// Group is one template x template_version x backend x model's calibration
// report. Per invariant 9, a report NEVER pools across any of these four:
// every Group here is scored from items sharing all four.
type Group struct {
	Template        string  `json:"template"`
	TemplateVersion string  `json:"template_version"`
	Backend         string  `json:"backend"`
	Model           string  `json:"model"`
	N               int     `json:"n"`
	Accuracy        float64 `json:"accuracy"`
	MeanConfidence  float64 `json:"mean_confidence"`
	Overconfidence  float64 `json:"overconfidence"`
	// Brier is the MULTI-CLASS Brier score: mean over items of
	// sum_k (p_k - y_k)^2 over every key the answer carried a probability
	// for. 0 is best; 2 is worst. For a noul (yes/no) question this is
	// exactly TWICE the usual single-term binary Brier score, because the
	// sum has both the "true" and the "false" term and they are equal by
	// construction (p_false = 1-p_true, y_false = 1-y_true). See README's
	// Calibration section.
	Brier float64 `json:"brier"`
	ECE   float64 `json:"ece"`
	// Verdict is "insufficient" (N < Options.MinN, no bound was judged),
	// "drift" (a set bound was exceeded), or "ok".
	Verdict string `json:"verdict"`
	// BoundsCrossed names each bound that was exceeded ("max_brier",
	// "max_ece") against the measured value that crossed it. Empty unless
	// Verdict is "drift".
	BoundsCrossed map[string]float64 `json:"bounds_crossed,omitempty"`
	Bins          [numBins]Bin       `json:"bins"`
}

// Counters are what could not be scored, and why, across the whole run.
type Counters struct {
	// NotScorable: the answer this outcome belongs to carries no recorded
	// probabilities (an answer ledgered before phase E, or one whose ledger
	// write predates a running upgrade).
	NotScorable int `json:"not_scorable"`
	// OrphanOutcome: an outcome names an answer_id this ledger has no answer
	// for.
	OrphanOutcome int `json:"orphan_outcome"`
	// TruthNotAKey: the outcome's truth does not name one of the keys the
	// answer actually carried a probability for.
	TruthNotAKey int `json:"truth_not_a_key"`
	// MalformedLines: a ledger line, in either file, that did not parse as
	// its record type, or parsed with no answer_id.
	MalformedLines int `json:"malformed_lines"`
	// TornLines: a file (0, 1, or 2) whose last line was cut short (no
	// trailing newline). Unlike internal/ledger.Open, this is counted and
	// left on disk exactly as found: this reader never writes.
	TornLines int `json:"torn_lines"`
}

// Report is one calibration run's whole output.
type Report struct {
	Groups   []Group  `json:"groups"`
	Counters Counters `json:"counters"`
}

// AnyDrift reports whether any group's verdict is "drift".
func (r Report) AnyDrift() bool {
	for _, g := range r.Groups {
		if g.Verdict == "drift" {
			return true
		}
	}
	return false
}

type groupKey struct {
	Template, TemplateVersion, Backend, Model string
}

type scoredItem struct {
	probs map[string]float64
	truth string
}

// Run reads dir's ledger read-only (see the package doc) and computes one
// Report. It never writes anywhere: emitting a calibration_drift event for a
// drift verdict is a separate, explicit step the caller takes (see
// cmd/typryx's --emit), never something Run does on its own.
func Run(opts Options) (Report, error) {
	if opts.LedgerDir == "" {
		return Report{}, errors.New("no ledger directory given")
	}
	answersPath := filepath.Join(opts.LedgerDir, "answers.ndjson")
	outcomesPath := filepath.Join(opts.LedgerDir, "outcomes.ndjson")

	answers, aCounts, err := readAnswers(answersPath)
	if err != nil {
		return Report{}, err
	}
	outcomes, oCounts, err := readOutcomes(outcomesPath)
	if err != nil {
		return Report{}, err
	}

	counters := Counters{
		MalformedLines: aCounts.malformed + oCounts.malformed,
		TornLines:      aCounts.torn + oCounts.torn,
	}

	byGroup := map[groupKey][]scoredItem{}
	for _, oc := range outcomes {
		ans, ok := answers[oc.AnswerID]
		if !ok {
			counters.OrphanOutcome++
			continue
		}
		if len(ans.Probabilities) == 0 {
			counters.NotScorable++
			continue
		}
		truth, ok := truthKeyFor(ans.Type, oc.Truth)
		if !ok {
			counters.TruthNotAKey++
			continue
		}
		if _, present := ans.Probabilities[truth]; !present {
			counters.TruthNotAKey++
			continue
		}
		gk := groupKey{Template: ans.Template, TemplateVersion: ans.TemplateVersion, Backend: ans.Backend, Model: ans.Model}
		byGroup[gk] = append(byGroup[gk], scoredItem{probs: ans.Probabilities, truth: truth})
	}

	groups := make([]Group, 0, len(byGroup))
	for gk, items := range byGroup {
		groups = append(groups, computeGroup(gk, items, opts))
	}
	sort.Slice(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		if a.Template != b.Template {
			return a.Template < b.Template
		}
		if a.TemplateVersion != b.TemplateVersion {
			return a.TemplateVersion < b.TemplateVersion
		}
		if a.Backend != b.Backend {
			return a.Backend < b.Backend
		}
		return a.Model < b.Model
	})

	return Report{Groups: groups, Counters: counters}, nil
}

// truthKeyFor maps a recorded outcome's truth (a JSON string for choice, a
// JSON integer for score, a JSON boolean for noul, per
// internal/service.validateTruth, which already validated it against the
// SAME answer's Options/Levels before it was ever written) onto the
// probability key it names. false means the truth did not even parse as the
// shape its type demands, which Run treats identically to "not one of the
// keys": it cannot be scored either way.
func truthKeyFor(answerType string, truth json.RawMessage) (string, bool) {
	switch answerType {
	case "choice":
		var s string
		if err := json.Unmarshal(truth, &s); err != nil {
			return "", false
		}
		return s, true
	case "score":
		var n int
		if err := json.Unmarshal(truth, &n); err != nil {
			return "", false
		}
		return strconv.Itoa(n), true
	case "noul":
		var b bool
		if err := json.Unmarshal(truth, &b); err != nil {
			return "", false
		}
		if b {
			return "true", true
		}
		return "false", true
	default:
		return "", false
	}
}

// computeGroup scores every item of one group and applies opts' verdict
// rules. n == 0 cannot happen from Run (a group only exists because at least
// one item mapped to it), but is handled defensively for any direct caller.
func computeGroup(gk groupKey, items []scoredItem, opts Options) Group {
	g := Group{Template: gk.Template, TemplateVersion: gk.TemplateVersion, Backend: gk.Backend, Model: gk.Model, N: len(items)}
	if g.N == 0 {
		g.Verdict = "insufficient"
		return g
	}

	var correct int
	var sumConf, sumBrier float64
	var bins [numBins]struct {
		n       int
		correct int
		sumConf float64
	}

	for _, it := range items {
		bestKey, bestP := argmax(it.probs)
		isCorrect := bestKey == it.truth
		if isCorrect {
			correct++
		}
		sumConf += bestP
		sumBrier += brierOf(it.probs, it.truth)

		bi := binIndex(bestP)
		bins[bi].n++
		bins[bi].sumConf += bestP
		if isCorrect {
			bins[bi].correct++
		}
	}

	n := float64(g.N)
	g.Accuracy = float64(correct) / n
	g.MeanConfidence = sumConf / n
	g.Overconfidence = g.MeanConfidence - g.Accuracy
	g.Brier = sumBrier / n

	var ece float64
	for i := 0; i < numBins; i++ {
		b := bins[i]
		if b.n == 0 {
			continue
		}
		binAcc := float64(b.correct) / float64(b.n)
		binConf := b.sumConf / float64(b.n)
		ece += (float64(b.n) / n) * math.Abs(binAcc-binConf)
		g.Bins[i] = Bin{N: b.n, MeanConfidence: binConf, Accuracy: binAcc}
	}
	g.ECE = ece

	if g.N < opts.MinN {
		g.Verdict = "insufficient"
		return g
	}
	crossed := map[string]float64{}
	if opts.MaxBrier != nil && g.Brier > *opts.MaxBrier {
		crossed["max_brier"] = g.Brier
	}
	if opts.MaxECE != nil && g.ECE > *opts.MaxECE {
		crossed["max_ece"] = g.ECE
	}
	if len(crossed) > 0 {
		g.Verdict = "drift"
		g.BoundsCrossed = crossed
	} else {
		g.Verdict = "ok"
	}
	return g
}

// argmax returns the key with the highest probability, ties broken towards
// the lexically smallest key.
func argmax(probs map[string]float64) (string, float64) {
	keys := make([]string, 0, len(probs))
	for k := range probs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	best := keys[0]
	bestP := probs[best]
	for _, k := range keys[1:] {
		if probs[k] > bestP {
			bestP = probs[k]
			best = k
		}
	}
	return best, bestP
}

// brierOf is the multi-class Brier score for one item: sum over every key the
// item carries a probability for of (p_k - y_k)^2, where y_k is 1 for the
// truth key and 0 elsewhere. A key present in probs but never named as truth
// simply contributes p_k^2, exactly as the sum says.
func brierOf(probs map[string]float64, truth string) float64 {
	var sum float64
	for k, p := range probs {
		y := 0.0
		if k == truth {
			y = 1.0
		}
		d := p - y
		sum += d * d
	}
	return sum
}

// binIndex maps a confidence in [0,1] onto one of the 10 equal-width bins,
// with 1.0 (and anything a hostile input pushes above it) landing in the
// last bin rather than one past the end, and anything below 0 clamped to the
// first: defensive, since a hostile ledger's probabilities are read, not
// re-validated, by this package (internal/service already validated them
// once, at answer time; this package's own hostile-input sweep exists
// because a ledger file can still be edited, truncated, or replayed after
// that).
func binIndex(confidence float64) int {
	bi := int(confidence * numBins)
	if bi >= numBins {
		bi = numBins - 1
	}
	if bi < 0 {
		bi = 0
	}
	return bi
}

// FormatGroupLine renders one group's text-table row to 3 decimals, with
// TemplateVersion shown as its first 8 hex characters.
func FormatGroupLine(g Group) string {
	ver := g.TemplateVersion
	if len(ver) > 8 {
		ver = ver[:8]
	}
	return fmt.Sprintf("%-24s %-8s %-16s %-12s n=%-5d acc=%.3f conf=%.3f overconf=%.3f brier=%.3f ece=%.3f %s",
		g.Template, ver, g.Backend, g.Model, g.N, g.Accuracy, g.MeanConfidence, g.Overconfidence, g.Brier, g.ECE, g.Verdict)
}
