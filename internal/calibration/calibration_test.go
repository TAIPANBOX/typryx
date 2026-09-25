package calibration_test

import (
	"encoding/json"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/TAIPANBOX/typryx/internal/calibration"
	"github.com/TAIPANBOX/typryx/internal/ledger"
)

// writeLedger writes ans and outs as answers.ndjson / outcomes.ndjson under
// dir, exactly as a real ledger would have on disk (one JSON object per
// line), without going through internal/ledger.Open at all: calibration must
// work from the files themselves, including a running service's own writes,
// never assume it can share a *ledger.Ledger.
func writeLedger(t *testing.T, dir string, ans []ledger.AnswerRecord, outs []ledger.OutcomeRecord) {
	t.Helper()
	var ansLines, outLines []string
	for _, a := range ans {
		b, err := json.Marshal(a)
		if err != nil {
			t.Fatalf("marshal answer: %v", err)
		}
		ansLines = append(ansLines, string(b))
	}
	for _, o := range outs {
		b, err := json.Marshal(o)
		if err != nil {
			t.Fatalf("marshal outcome: %v", err)
		}
		outLines = append(outLines, string(b))
	}
	if len(ansLines) > 0 {
		if err := os.WriteFile(filepath.Join(dir, "answers.ndjson"), []byte(strings.Join(ansLines, "\n")+"\n"), 0o644); err != nil {
			t.Fatalf("writing answers.ndjson: %v", err)
		}
	}
	if len(outLines) > 0 {
		if err := os.WriteFile(filepath.Join(dir, "outcomes.ndjson"), []byte(strings.Join(outLines, "\n")+"\n"), 0o644); err != nil {
			t.Fatalf("writing outcomes.ndjson: %v", err)
		}
	}
}

func truthRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal truth: %v", err)
	}
	return b
}

// @test:TestExactBrierAndECEOnAFourItemFixture
//
// A 4-item fixture, hand-computed, for both a noul and a choice group.
// This is the ground-truth arithmetic check: if this test passes, the
// formulas in calibration.go match the ones in the plan and the README,
// independent of anything statistical.
func TestExactBrierAndECEOnAFourItemFixture(t *testing.T) {
	dir := t.TempDir()
	ans := []ledger.AnswerRecord{
		{AnswerID: "n1", Template: "eval.outcome_met", TemplateVersion: "v1", Type: "noul", Backend: "stub", Model: "m1",
			Probabilities: map[string]float64{"true": 0.9, "false": 0.1}},
		{AnswerID: "n2", Template: "eval.outcome_met", TemplateVersion: "v1", Type: "noul", Backend: "stub", Model: "m1",
			Probabilities: map[string]float64{"true": 0.9, "false": 0.1}},
		{AnswerID: "n3", Template: "eval.outcome_met", TemplateVersion: "v1", Type: "noul", Backend: "stub", Model: "m1",
			Probabilities: map[string]float64{"true": 0.3, "false": 0.7}},
		{AnswerID: "n4", Template: "eval.outcome_met", TemplateVersion: "v1", Type: "noul", Backend: "stub", Model: "m1",
			Probabilities: map[string]float64{"true": 0.3, "false": 0.7}},
	}
	outs := []ledger.OutcomeRecord{
		{AnswerID: "n1", Truth: truthRaw(t, true)},  // correct, conf 0.9
		{AnswerID: "n2", Truth: truthRaw(t, false)}, // wrong, conf 0.9
		{AnswerID: "n3", Truth: truthRaw(t, false)}, // correct, conf 0.7
		{AnswerID: "n4", Truth: truthRaw(t, false)}, // correct, conf 0.7
	}
	writeLedger(t, dir, ans, outs)

	report, err := calibration.Run(calibration.Options{LedgerDir: dir, MinN: 1})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(report.Groups))
	}
	g := report.Groups[0]

	// accuracy: 3 of 4 correct (n1, n3, n4), n2 wrong.
	wantAcc := 0.75
	if math.Abs(g.Accuracy-wantAcc) > 1e-9 {
		t.Errorf("accuracy = %v, want %v", g.Accuracy, wantAcc)
	}
	// mean confidence: (0.9+0.9+0.7+0.7)/4 = 0.8
	wantConf := 0.8
	if math.Abs(g.MeanConfidence-wantConf) > 1e-9 {
		t.Errorf("mean_confidence = %v, want %v", g.MeanConfidence, wantConf)
	}
	// Brier, multi-class (doubled binary), per item:
	// n1: (0.9-1)^2+(0.1-0)^2 = 0.01+0.01 = 0.02
	// n2: (0.9-0)^2+(0.1-1)^2 = 0.81+0.81 = 1.62
	// n3: (0.3-0)^2+(0.7-1)^2 = 0.09+0.09 = 0.18
	// n4: same as n3 = 0.18
	// mean = (0.02+1.62+0.18+0.18)/4 = 2.00/4 = 0.5
	wantBrier := 0.5
	if math.Abs(g.Brier-wantBrier) > 1e-9 {
		t.Errorf("brier = %v, want %v", g.Brier, wantBrier)
	}
	// ECE: two bins, both confidence 0.9 (bin 9) and 0.7 (bin 7).
	// bin 9: n=2, conf=0.9, acc=1/2=0.5 (n1 correct, n2 wrong) -> |0.5-0.9|=0.4, weight 2/4
	// bin 7: n=2, conf=0.7, acc=2/2=1.0 (n3,n4 correct) -> |1.0-0.7|=0.3, weight 2/4
	// ECE = 0.5*0.4 + 0.5*0.3 = 0.2+0.15 = 0.35
	wantECE := 0.35
	if math.Abs(g.ECE-wantECE) > 1e-9 {
		t.Errorf("ece = %v, want %v", g.ECE, wantECE)
	}
	if g.N != 4 {
		t.Errorf("n = %d, want 4", g.N)
	}
}

// @test:TestExactBrierOnAChoiceFixture
func TestExactBrierOnAChoiceFixture(t *testing.T) {
	dir := t.TempDir()
	ans := []ledger.AnswerRecord{
		{AnswerID: "c1", Template: "request.complexity", TemplateVersion: "v1", Type: "choice", Backend: "stub", Model: "m1",
			Probabilities: map[string]float64{"cheap": 0.6, "default": 0.3, "hard": 0.1}},
	}
	outs := []ledger.OutcomeRecord{
		{AnswerID: "c1", Truth: truthRaw(t, "cheap")},
	}
	writeLedger(t, dir, ans, outs)
	report, err := calibration.Run(calibration.Options{LedgerDir: dir, MinN: 1})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	g := report.Groups[0]
	// (0.6-1)^2 + (0.3-0)^2 + (0.1-0)^2 = 0.16+0.09+0.01 = 0.26
	want := 0.26
	if math.Abs(g.Brier-want) > 1e-9 {
		t.Errorf("brier = %v, want %v", g.Brier, want)
	}
	if g.Accuracy != 1 {
		t.Errorf("accuracy = %v, want 1", g.Accuracy)
	}
}

// @test:TestArgmaxTiesBreakToTheLexicallySmallestKey
func TestArgmaxTiesBreakToTheLexicallySmallestKey(t *testing.T) {
	dir := t.TempDir()
	ans := []ledger.AnswerRecord{
		{AnswerID: "c1", Template: "request.complexity", TemplateVersion: "v1", Type: "choice", Backend: "stub", Model: "m1",
			// a genuine 3-way tie: "cheap" sorts before "default" and "hard".
			Probabilities: map[string]float64{"hard": 1.0 / 3, "default": 1.0 / 3, "cheap": 1.0 / 3}},
	}
	outs := []ledger.OutcomeRecord{
		{AnswerID: "c1", Truth: truthRaw(t, "cheap")},
	}
	writeLedger(t, dir, ans, outs)
	report, err := calibration.Run(calibration.Options{LedgerDir: dir, MinN: 1})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	g := report.Groups[0]
	if g.Accuracy != 1 {
		t.Errorf("accuracy = %v, want 1: a 3-way tie must resolve to the lexically smallest key (\"cheap\"), which is also the truth here", g.Accuracy)
	}
}

// simSource generates n synthetic items whose stated probability of "true"
// is p (per item, from genP), with truth sampled from a Bernoulli(p) draw,
// and returns them written as a ledger under dir.
func simSource(t *testing.T, dir string, n int, seed int64, genP func(r *rand.Rand) float64) {
	t.Helper()
	r := rand.New(rand.NewSource(seed))
	ans := make([]ledger.AnswerRecord, n)
	outs := make([]ledger.OutcomeRecord, n)
	for i := 0; i < n; i++ {
		p := genP(r)
		truth := r.Float64() < p
		id := "a" + strconv.Itoa(i)
		ans[i] = ledger.AnswerRecord{
			AnswerID: id, Template: "sim.template", TemplateVersion: "v1", Type: "noul",
			Backend: "sim", Model: "sim-model", Probabilities: map[string]float64{"true": p, "false": 1 - p},
		}
		outs[i] = ledger.OutcomeRecord{AnswerID: id, Truth: truthRaw(t, truth)}
	}
	writeLedger(t, dir, ans, outs)
}

// @test:TestAPerfectlyCalibratedSourceScoresNearZeroECE
func TestAPerfectlyCalibratedSourceScoresNearZeroECE(t *testing.T) {
	for seed := int64(0); seed < 20; seed++ {
		dir := t.TempDir()
		simSource(t, dir, 5000, seed, func(r *rand.Rand) float64 { return r.Float64() })
		report, err := calibration.Run(calibration.Options{LedgerDir: dir, MinN: 1})
		if err != nil {
			t.Fatalf("seed %d: Run: %v", seed, err)
		}
		if len(report.Groups) != 1 {
			t.Fatalf("seed %d: expected 1 group, got %d", seed, len(report.Groups))
		}
		if ece := report.Groups[0].ECE; ece >= 0.03 {
			t.Errorf("seed %d: expected ECE < 0.03 for a perfectly calibrated source, got %v", seed, ece)
		}
	}
}

// @test:TestAnOverconfidentSourceIsFlagged
//
// Stated 0.99 every time, but the true rate is only 0.6: the fixture states
// the true-rate directly rather than deriving it from genP, so "true rate
// 0.6" in the plan is exactly what gets simulated.
func TestAnOverconfidentSourceIsFlagged(t *testing.T) {
	dir := t.TempDir()
	r := rand.New(rand.NewSource(1))
	const n = 5000
	ans := make([]ledger.AnswerRecord, n)
	outs := make([]ledger.OutcomeRecord, n)
	for i := 0; i < n; i++ {
		truth := r.Float64() < 0.6
		id := "a" + strconv.Itoa(i)
		ans[i] = ledger.AnswerRecord{
			AnswerID: id, Template: "sim.template", TemplateVersion: "v1", Type: "noul",
			Backend: "sim", Model: "sim-model", Probabilities: map[string]float64{"true": 0.99, "false": 0.01},
		}
		outs[i] = ledger.OutcomeRecord{AnswerID: id, Truth: truthRaw(t, truth)}
	}
	writeLedger(t, dir, ans, outs)

	maxECE := 0.1
	report, err := calibration.Run(calibration.Options{LedgerDir: dir, MinN: 1, MaxECE: &maxECE})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(report.Groups))
	}
	g := report.Groups[0]
	wantECE := 0.39
	if math.Abs(g.ECE-wantECE) > 0.03 {
		t.Errorf("ECE = %v, want ~%v (within 0.03)", g.ECE, wantECE)
	}
	if g.Verdict != "drift" {
		t.Errorf("verdict = %q, want drift", g.Verdict)
	}
	if _, ok := g.BoundsCrossed["max_ece"]; !ok {
		t.Errorf("expected max_ece to be named in BoundsCrossed, got %#v", g.BoundsCrossed)
	}
}

// @test:TestTwoModelsAreNeverScoredAsOne
//
// A baseline group (well-calibrated) plus four variants, each differing from
// the baseline in EXACTLY ONE of the four group-key fields and each
// deliberately miscalibrated. Pooling on any one of the four fields would
// merge that variant into the baseline (or into another variant sharing the
// other three), visibly changing its accuracy; grouped correctly, every one
// of the five keeps its own sharply different accuracy. This is written to
// catch a mutant dropping ANY of the four fields from the group key, not
// just Model: a fixture that only ever varies Model (an earlier version of
// this test) cannot catch a mutant that drops Template, TemplateVersion, or
// Backend, because those three never differed between its two items.
func TestTwoModelsAreNeverScoredAsOne(t *testing.T) {
	dir := t.TempDir()
	const n = 300
	type spec struct {
		name                                      string
		template, templateVersion, backend, model string
		trueRate                                  float64 // actual P(truth) for a stated confidence of 0.9
	}
	specs := []spec{
		{"baseline", "eval.outcome_met", "v1", "sim", "model-a", 0.9},                  // well calibrated
		{"other template", "eval.answer_quality", "v1", "sim", "model-a", 0.1},         // template differs
		{"other version", "eval.outcome_met", "v2", "sim", "model-a", 0.1},             // template_version differs
		{"other backend", "eval.outcome_met", "v1", "openai-logprobs", "model-a", 0.1}, // backend differs
		{"other model", "eval.outcome_met", "v1", "sim", "model-b", 0.1},               // model differs
	}

	var ans []ledger.AnswerRecord
	var outs []ledger.OutcomeRecord
	r := rand.New(rand.NewSource(42))
	for _, s := range specs {
		for i := 0; i < n; i++ {
			id := s.name + strconv.Itoa(i)
			truth := r.Float64() < s.trueRate
			ans = append(ans, ledger.AnswerRecord{
				AnswerID: id, Template: s.template, TemplateVersion: s.templateVersion, Type: "noul",
				Backend: s.backend, Model: s.model, Probabilities: map[string]float64{"true": 0.9, "false": 0.1},
			})
			outs = append(outs, ledger.OutcomeRecord{AnswerID: id, Truth: truthRaw(t, truth)})
		}
	}
	writeLedger(t, dir, ans, outs)

	report, err := calibration.Run(calibration.Options{LedgerDir: dir, MinN: 1})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Groups) != len(specs) {
		t.Fatalf("expected %d groups (never pooled on template, version, backend, or model), got %d: %+v",
			len(specs), len(report.Groups), report.Groups)
	}
	for _, s := range specs {
		var found *calibration.Group
		for i := range report.Groups {
			g := report.Groups[i]
			if g.Template == s.template && g.TemplateVersion == s.templateVersion && g.Backend == s.backend && g.Model == s.model {
				found = &report.Groups[i]
			}
		}
		if found == nil {
			t.Fatalf("%s: no group found for (%s, %s, %s, %s)", s.name, s.template, s.templateVersion, s.backend, s.model)
		}
		wantHighAcc := s.trueRate > 0.5
		if wantHighAcc && found.Accuracy < 0.8 {
			t.Errorf("%s: accuracy = %v, expected close to %v", s.name, found.Accuracy, s.trueRate)
		}
		if !wantHighAcc && found.Accuracy > 0.2 {
			t.Errorf("%s: accuracy = %v, expected close to %v", s.name, found.Accuracy, s.trueRate)
		}
	}
	// The baseline (well-calibrated) and its four miscalibrated variants must
	// disagree sharply, pairwise: a mutant pooling any one field would merge
	// two of these into a single row whose accuracy sits between the two,
	// which this pairwise check catches directly.
	byKey := map[string]calibration.Group{}
	for _, g := range report.Groups {
		byKey[g.Template+"|"+g.TemplateVersion+"|"+g.Backend+"|"+g.Model] = g
	}
	base := byKey["eval.outcome_met|v1|sim|model-a"]
	for _, s := range specs[1:] {
		other := byKey[s.template+"|"+s.templateVersion+"|"+s.backend+"|"+s.model]
		if math.Abs(base.Accuracy-other.Accuracy) < 0.4 {
			t.Errorf("%s: expected the baseline (%v) and this variant (%v) to differ sharply", s.name, base.Accuracy, other.Accuracy)
		}
	}
}

// @test:TestTooFewTruthsGiveNoVerdict
func TestTooFewTruthsGiveNoVerdict(t *testing.T) {
	dir := t.TempDir()
	var ans []ledger.AnswerRecord
	var outs []ledger.OutcomeRecord
	for i := 0; i < 5; i++ {
		id := "a" + strconv.Itoa(i)
		ans = append(ans, ledger.AnswerRecord{
			AnswerID: id, Template: "eval.outcome_met", TemplateVersion: "v1", Type: "noul",
			Backend: "stub", Model: "m1", Probabilities: map[string]float64{"true": 0.99, "false": 0.01},
		})
		outs = append(outs, ledger.OutcomeRecord{AnswerID: id, Truth: truthRaw(t, false)}) // maximally uncalibrated
	}
	writeLedger(t, dir, ans, outs)

	maxBrier, maxECE := 0.01, 0.01 // absurdly tight, would drift if judged
	report, err := calibration.Run(calibration.Options{LedgerDir: dir, MinN: 30, MaxBrier: &maxBrier, MaxECE: &maxECE})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(report.Groups))
	}
	g := report.Groups[0]
	if g.Verdict != "insufficient" {
		t.Errorf("verdict = %q, want insufficient (n=%d < min-n=30)", g.Verdict, g.N)
	}
	if len(g.BoundsCrossed) != 0 {
		t.Errorf("expected no bound to be judged for an insufficient group, got %#v", g.BoundsCrossed)
	}
}

// @test:TestOrphanNotScorableAndTruthNotAKeyAreCounted
func TestOrphanNotScorableAndTruthNotAKeyAreCounted(t *testing.T) {
	dir := t.TempDir()
	ans := []ledger.AnswerRecord{
		// a1: pre-phase-E answer, no Probabilities recorded at all.
		{AnswerID: "a1", Template: "t", TemplateVersion: "v1", Type: "noul", Backend: "stub", Model: "m1"},
		// a2: scorable, but the outcome below names a truth outside its keys.
		{AnswerID: "a2", Template: "t", TemplateVersion: "v1", Type: "choice", Backend: "stub", Model: "m1",
			Probabilities: map[string]float64{"x": 0.5, "y": 0.5}},
	}
	outs := []ledger.OutcomeRecord{
		{AnswerID: "a1", Truth: truthRaw(t, true)},             // -> not_scorable
		{AnswerID: "a2", Truth: truthRaw(t, "not-a-key")},      // -> truth_not_a_key
		{AnswerID: "no-such-answer", Truth: truthRaw(t, true)}, // -> orphan_outcome
	}
	writeLedger(t, dir, ans, outs)

	report, err := calibration.Run(calibration.Options{LedgerDir: dir, MinN: 1})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Groups) != 0 {
		t.Fatalf("expected 0 scorable groups, got %d: %+v", len(report.Groups), report.Groups)
	}
	if report.Counters.NotScorable != 1 {
		t.Errorf("not_scorable = %d, want 1", report.Counters.NotScorable)
	}
	if report.Counters.TruthNotAKey != 1 {
		t.Errorf("truth_not_a_key = %d, want 1", report.Counters.TruthNotAKey)
	}
	if report.Counters.OrphanOutcome != 1 {
		t.Errorf("orphan_outcome = %d, want 1", report.Counters.OrphanOutcome)
	}
}

// @test:TestNoLedgerDirIsAConfigError
func TestNoLedgerDirIsAConfigError(t *testing.T) {
	if _, err := calibration.Run(calibration.Options{}); err == nil {
		t.Error("expected an error when LedgerDir is empty")
	}
}

// @test:TestAHostileLedgerNeverPanicsOrProducesAnOutOfRangeMetric
//
// 200 seeds of randomly mutated ledger bytes: Run must never panic, and
// every metric it does produce must stay in its documented range (no NaN,
// no Inf, accuracy/mean_confidence/ece in [0,1], brier in [0,2]).
func TestAHostileLedgerNeverPanicsOrProducesAnOutOfRangeMetric(t *testing.T) {
	goodAns, _ := json.Marshal(ledger.AnswerRecord{
		AnswerID: "a1", Template: "t", TemplateVersion: "v1", Type: "noul", Backend: "stub", Model: "m1",
		Probabilities: map[string]float64{"true": 0.7, "false": 0.3},
	})
	goodOut, _ := json.Marshal(ledger.OutcomeRecord{AnswerID: "a1", Truth: json.RawMessage(`true`)})

	for seed := int64(0); seed < 200; seed++ {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("seed %d: panicked: %v", seed, r)
				}
			}()
			dir := t.TempDir()
			r := rand.New(rand.NewSource(seed))
			if err := os.WriteFile(filepath.Join(dir, "answers.ndjson"), mutate(append(goodAns, '\n'), r), 0o644); err != nil {
				t.Fatalf("seed %d: writing answers fixture: %v", seed, err)
			}
			if err := os.WriteFile(filepath.Join(dir, "outcomes.ndjson"), mutate(append(goodOut, '\n'), r), 0o644); err != nil {
				t.Fatalf("seed %d: writing outcomes fixture: %v", seed, err)
			}
			report, err := calibration.Run(calibration.Options{LedgerDir: dir, MinN: 1})
			if err != nil {
				return // an error is a fine outcome for hostile bytes; a panic is not
			}
			for _, g := range report.Groups {
				for _, v := range []float64{g.Accuracy, g.MeanConfidence, g.ECE} {
					if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1 {
						t.Fatalf("seed %d: metric out of [0,1]: %v", seed, v)
					}
				}
				if math.IsNaN(g.Brier) || math.IsInf(g.Brier, 0) || g.Brier < 0 || g.Brier > 2 {
					t.Fatalf("seed %d: brier out of [0,2]: %v", seed, g.Brier)
				}
			}
		}()
	}
}

func mutate(b []byte, r *rand.Rand) []byte {
	out := append([]byte(nil), b...)
	n := r.Intn(10) + 1
	for i := 0; i < n; i++ {
		if len(out) == 0 {
			break
		}
		switch r.Intn(3) {
		case 0:
			out[r.Intn(len(out))] = byte(r.Intn(256))
		case 1:
			cut := r.Intn(len(out) + 1)
			out = out[:cut]
		case 2:
			pos := r.Intn(len(out) + 1)
			junk := []byte{byte(r.Intn(256))}
			out = append(out[:pos], append(junk, out[pos:]...)...)
		}
	}
	return out
}
