package traininglog_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/TAIPANBOX/typryx/internal/traininglog"
)

// fixture builds a training dir and a ledger dir from raw lines, so a test
// controls every byte an export reads.
type fixture struct {
	training string
	ledger   string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	return fixture{training: t.TempDir(), ledger: t.TempDir()}
}

func (f fixture) write(t *testing.T, dir, name string, lines ...string) {
	t.Helper()
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

func (f fixture) trainingLines(t *testing.T, lines ...string) {
	t.Helper()
	f.write(t, f.training, traininglog.FileName, lines...)
}

func (f fixture) outcomeLines(t *testing.T, lines ...string) {
	t.Helper()
	f.write(t, f.ledger, "outcomes.ndjson", lines...)
}

func tline(id, tmpl, version, typ, state string) string {
	return fmt.Sprintf(`{"answer_id":%q,"answered_at":"2026-09-30T12:00:00Z","template":%q,"template_version":%q,"type":%q,"state":%s,"backend":"jev","model":"jev-1"}`,
		id, tmpl, version, typ, state)
}

func oline(id, tmpl, version, truth string) string {
	return fmt.Sprintf(`{"answer_id":%q,"template":%q,"template_version":%q,"backend":"jev","model":"jev-1","truth":%s,"source":"human","recorded_at":"2026-09-30T13:00:00Z"}`,
		id, tmpl, version, truth)
}

func run(t *testing.T, f fixture) ([]map[string]json.RawMessage, traininglog.ExportReport) {
	t.Helper()
	var out bytes.Buffer
	rep, err := traininglog.Export(traininglog.ExportOptions{TrainingDir: f.training, LedgerDir: f.ledger}, &out)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	var rows []map[string]json.RawMessage
	for _, ln := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if ln == "" {
			continue
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Fatalf("an exported line is not JSON: %q", ln)
		}
		rows = append(rows, m)
	}
	return rows, rep
}

func counts(rep traininglog.ExportReport, tmpl string) traininglog.TemplateCounts {
	if c := rep.Templates[tmpl]; c != nil {
		return *c
	}
	return traininglog.TemplateCounts{}
}

// @test:TestExportJoinsByAnswerIDAndLabelsWithTheHumanTruth
func TestExportJoinsByAnswerIDAndLabelsWithTheHumanTruth(t *testing.T) {
	f := newFixture(t)
	f.trainingLines(t,
		tline("a1", "request.complexity", "v1", "choice", `{"prompt":"hello"}`),
		tline("a2", "eval.outcome_met", "v7", "noul", `{"task":"t"}`),
		tline("a3", "eval.answer_quality", "v2", "score", `{"answer":"x"}`),
	)
	f.outcomeLines(t,
		oline("a1", "request.complexity", "v1", `"hard"`),
		oline("a2", "eval.outcome_met", "v7", `false`),
		oline("a3", "eval.answer_quality", "v2", `3`),
	)
	rows, rep := run(t, f)
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	want := []map[string]string{
		{"template": `"request.complexity"`, "template_version": `"v1"`, "type": `"choice"`, "state": `{"prompt":"hello"}`, "label": `"hard"`},
		{"template": `"eval.outcome_met"`, "template_version": `"v7"`, "type": `"noul"`, "state": `{"task":"t"}`, "label": `false`},
		{"template": `"eval.answer_quality"`, "template_version": `"v2"`, "type": `"score"`, "state": `{"answer":"x"}`, "label": `3`},
	}
	for i, w := range want {
		got := map[string]string{}
		for k, v := range rows[i] {
			got[k] = string(v)
		}
		if !reflect.DeepEqual(got, w) {
			t.Errorf("row %d = %v, want %v", i, got, w)
		}
	}
	for _, tmpl := range []string{"request.complexity", "eval.outcome_met", "eval.answer_quality"} {
		if c := counts(rep, tmpl); c.Exported != 1 || c.SkippedNoTruth != 0 {
			t.Errorf("%s counts = %+v", tmpl, c)
		}
	}
}

// @test:TestExportRowsCarryExactlyTheFiveFieldsAndNoModelOutput
func TestExportRowsCarryExactlyTheFiveFieldsAndNoModelOutput(t *testing.T) {
	f := newFixture(t)
	f.trainingLines(t, tline("a1", "t", "v1", "noul", `{"x":1}`))
	f.outcomeLines(t, oline("a1", "t", "v1", `true`))
	// An extra key a hand-edited training line might carry is never forwarded.
	bad := strings.Replace(tline("a2", "t", "v1", "noul", `{"x":2}`), `"backend"`, `"probabilities":{"true":0.9},"answer":0.9,"backend"`, 1)
	f.trainingLines(t, tline("a1", "t", "v1", "noul", `{"x":1}`), bad)
	f.outcomeLines(t, oline("a1", "t", "v1", `true`), oline("a2", "t", "v1", `false`))
	rows, _ := run(t, f)
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	for _, r := range rows {
		var keys []string
		for k := range r {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if want := []string{"label", "state", "template", "template_version", "type"}; !reflect.DeepEqual(keys, want) {
			t.Errorf("row keys = %v, want %v", keys, want)
		}
	}
}

// @test:TestExportSkipsAnAnswerWithNoTruthAndCountsIt
func TestExportSkipsAnAnswerWithNoTruthAndCountsIt(t *testing.T) {
	f := newFixture(t)
	f.trainingLines(t,
		tline("a1", "t", "v1", "noul", `{"x":1}`),
		tline("a2", "t", "v1", "noul", `{"x":2}`),
		tline("a3", "u", "v1", "noul", `{"x":3}`),
	)
	f.outcomeLines(t, oline("a1", "t", "v1", `true`))
	rows, rep := run(t, f)
	if len(rows) != 1 {
		t.Fatalf("expected only the answer with a truth, got %d rows", len(rows))
	}
	if c := counts(rep, "t"); c.Exported != 1 || c.SkippedNoTruth != 1 {
		t.Errorf("t counts = %+v, want exported 1 skipped_no_truth 1", c)
	}
	if c := counts(rep, "u"); c.Exported != 0 || c.SkippedNoTruth != 1 {
		t.Errorf("u counts = %+v, want exported 0 skipped_no_truth 1", c)
	}
}

// @test:TestExportNeverUsesABackendAnswerAsALabel
//
// The ledger's answers.ndjson carries the backend's served answer and its
// probabilities: for a Jev answer, exactly the output TypeSafe's terms forbid
// as a training label. With no human truth posted, nothing may be exported,
// however confident that answer is; with a truth posted, the label is the
// truth even when the backend disagreed with it.
func TestExportNeverUsesABackendAnswerAsALabel(t *testing.T) {
	f := newFixture(t)
	f.trainingLines(t,
		tline("a1", "t", "v1", "noul", `{"x":1}`),
		tline("a2", "t", "v1", "noul", `{"x":2}`),
	)
	f.write(t, f.ledger, "answers.ndjson",
		`{"answer_id":"a1","template":"t","template_version":"v1","type":"noul","backend":"jev","model":"jev-1","answered_at":"x","probabilities":{"true":0.9999,"false":0.0001},"answer":0.9999}`,
		`{"answer_id":"a2","template":"t","template_version":"v1","type":"noul","backend":"jev","model":"jev-1","answered_at":"x","probabilities":{"true":0.9999,"false":0.0001},"answer":0.9999}`,
	)
	rows, rep := run(t, f)
	if len(rows) != 0 {
		t.Fatalf("exported %d rows with no human truth posted: a backend answer became a label: %v", len(rows), rows)
	}
	if c := counts(rep, "t"); c.SkippedNoTruth != 2 {
		t.Errorf("expected both answers counted as skipped_no_truth, got %+v", c)
	}

	f.outcomeLines(t, oline("a1", "t", "v1", `false`))
	rows, _ = run(t, f)
	if len(rows) != 1 || string(rows[0]["label"]) != "false" {
		t.Fatalf("a human truth of false must label the row false even though the backend said 0.9999 true, got %v", rows)
	}
}

// @test:TestExportSkipsATruthScoredAgainstAnotherTemplateVersion
//
// Rule: a training line and a truth are joined only when they name the same
// template AND the same template_version. The service scores an outcome
// against the version the answer was asked under, so a mismatch means one of
// the two files was edited or mixed up; the pair is skipped and counted, not
// guessed at.
func TestExportSkipsATruthScoredAgainstAnotherTemplateVersion(t *testing.T) {
	f := newFixture(t)
	f.trainingLines(t,
		tline("a1", "t", "v1", "noul", `{"x":1}`),
		tline("a2", "t", "v1", "noul", `{"x":2}`),
		tline("a3", "t", "v1", "noul", `{"x":3}`),
	)
	f.outcomeLines(t,
		oline("a1", "t", "v2", `true`),  // other version
		oline("a2", "u", "v1", `true`),  // other template
		oline("a3", "t", "v1", `false`), // matches
	)
	rows, rep := run(t, f)
	if len(rows) != 1 || string(rows[0]["label"]) != "false" {
		t.Fatalf("only the matching pair may be exported, got %v", rows)
	}
	if c := counts(rep, "t"); c.Exported != 1 || c.SkippedVersionMismatch != 2 {
		t.Errorf("t counts = %+v, want exported 1 skipped_version_mismatch 2", c)
	}
}

func TestExportSkipsATruthOfTheWrongShapeForItsType(t *testing.T) {
	f := newFixture(t)
	f.trainingLines(t,
		tline("c1", "t", "v1", "choice", `{"x":1}`),
		tline("c2", "t", "v1", "choice", `{"x":1}`),
		tline("s1", "t", "v1", "score", `{"x":1}`),
		tline("s2", "t", "v1", "score", `{"x":1}`),
		tline("s3", "t", "v1", "score", `{"x":1}`),
		tline("n1", "t", "v1", "noul", `{"x":1}`),
		tline("n2", "t", "v1", "noul", `{"x":1}`),
		tline("ok", "t", "v1", "score", `{"x":1}`),
	)
	f.outcomeLines(t,
		oline("c1", "t", "v1", `3`),
		oline("c2", "t", "v1", `null`),
		oline("s1", "t", "v1", `1.5`),
		oline("s2", "t", "v1", `"2"`),
		oline("s3", "t", "v1", `-1`),
		oline("n1", "t", "v1", `"true"`),
		oline("n2", "t", "v1", `1`),
		oline("ok", "t", "v1", `2`),
	)
	rows, rep := run(t, f)
	if len(rows) != 1 || string(rows[0]["label"]) != "2" {
		t.Fatalf("only the well-shaped truth may be exported, got %v", rows)
	}
	if c := counts(rep, "t"); c.Exported != 1 || c.SkippedBadTruth != 7 {
		t.Errorf("counts = %+v, want exported 1 skipped_bad_truth 7", c)
	}
}

// @test:TestExportRefusesAnAnswerIDThatAppearsTwice
//
// Rule: an answer_id is 128 random bits, so a repeat is corruption or
// tampering. Every training line carrying a repeated id is skipped, and so is
// a training line whose id has two truths on the ledger: which one is the
// human's word is not something an export may guess.
func TestExportRefusesAnAnswerIDThatAppearsTwice(t *testing.T) {
	f := newFixture(t)
	f.trainingLines(t,
		tline("dup", "t", "v1", "noul", `{"x":1}`),
		tline("dup", "t", "v1", "noul", `{"x":2}`),
		tline("two", "t", "v1", "noul", `{"x":3}`),
		tline("fine", "t", "v1", "noul", `{"x":4}`),
	)
	f.outcomeLines(t,
		oline("dup", "t", "v1", `true`),
		oline("two", "t", "v1", `true`),
		oline("two", "t", "v1", `false`),
		oline("fine", "t", "v1", `true`),
	)
	rows, rep := run(t, f)
	if len(rows) != 1 || string(rows[0]["state"]) != `{"x":4}` {
		t.Fatalf("only the unambiguous pair may be exported, got %v", rows)
	}
	if c := counts(rep, "t"); c.Exported != 1 || c.SkippedDuplicateID != 3 {
		t.Errorf("counts = %+v, want exported 1 skipped_duplicate_id 3", c)
	}
}

func TestExportCountsOrphanTruthsAndMalformedAndTornLines(t *testing.T) {
	f := newFixture(t)
	good := tline("a1", "t", "v1", "noul", `{"x":1}`)
	body := strings.Join([]string{
		good,
		`not json at all`,
		`[1,2,3]`,
		`{"answer_id":"","template":"t","template_version":"v1","type":"noul","state":{}}`,
		`{"answer_id":"z","template":"t","template_version":"v1","type":"banana","state":{}}`,
		`{"answer_id":"z","template":"t","template_version":"v1","type":"noul","state":"not an object"}`,
		`{"answer_id":"z","template":"t","template_version":"v1","type":"noul"}`,
		`{"answer_id":7,"template":"t"}`,
	}, "\n") + "\n" + `{"answer_id":"torn","templ`
	if err := os.WriteFile(filepath.Join(f.training, traininglog.FileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	f.outcomeLines(t, oline("a1", "t", "v1", `true`), oline("ghost", "t", "v1", `true`), `garbage`)
	rows, rep := run(t, f)
	if len(rows) != 1 {
		t.Fatalf("expected the one good pair, got %d rows", len(rows))
	}
	if rep.MalformedLines != 7+1 { // seven bad training lines, one bad outcome line
		t.Errorf("malformed_lines = %d, want 8", rep.MalformedLines)
	}
	if rep.TornLines != 1 {
		t.Errorf("torn_lines = %d, want 1", rep.TornLines)
	}
	if rep.OrphanTruth != 1 {
		t.Errorf("orphan_truth = %d, want 1 (the truth for an answer with no training line)", rep.OrphanTruth)
	}
}

// @test:TestExportIsReadOnlyOnBothDirectories
func TestExportIsReadOnlyOnBothDirectories(t *testing.T) {
	f := newFixture(t)
	// Torn tails on every file: the export must count them and leave them.
	if err := os.WriteFile(filepath.Join(f.training, traininglog.FileName),
		[]byte(tline("a1", "t", "v1", "noul", `{"x":1}`)+"\n"+`{"answer_id":"torn`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.ledger, "outcomes.ndjson"),
		[]byte(oline("a1", "t", "v1", `true`)+"\n"+`{"answer_id":"torn`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.ledger, "answers.ndjson"), []byte(`{"answer_id":"a1"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snap := func() string {
		h := sha256.New()
		for _, d := range []string{f.training, f.ledger} {
			entries, err := os.ReadDir(d)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				b, err := os.ReadFile(filepath.Join(d, e.Name()))
				if err != nil {
					t.Fatal(err)
				}
				fmt.Fprintf(h, "%s/%s:%d:", d, e.Name(), len(b))
				h.Write(b)
			}
		}
		return fmt.Sprintf("%x", h.Sum(nil))
	}
	before := snap()
	rows, rep := run(t, f)
	if len(rows) != 1 || rep.TornLines != 2 {
		t.Fatalf("expected 1 row and 2 torn lines counted, got %d rows, %+v", len(rows), rep)
	}
	if after := snap(); after != before {
		t.Error("the export changed a file or added one in the training or ledger directory")
	}
}

func TestExportWithNoOutcomesFileExportsNothingAndDoesNotFail(t *testing.T) {
	f := newFixture(t)
	f.trainingLines(t, tline("a1", "t", "v1", "noul", `{"x":1}`))
	rows, rep := run(t, f)
	if len(rows) != 0 || counts(rep, "t").SkippedNoTruth != 1 {
		t.Errorf("rows=%v counts=%+v", rows, counts(rep, "t"))
	}
}

func TestExportRefusesADirectoryThatDoesNotExistOrHoldsNoTrainingLog(t *testing.T) {
	f := newFixture(t)
	var out bytes.Buffer
	if _, err := traininglog.Export(traininglog.ExportOptions{TrainingDir: f.training, LedgerDir: f.ledger}, &out); err == nil {
		t.Error("an export over a training dir with no training.ndjson succeeded; a mistyped path must not look like an empty result")
	}
	f.trainingLines(t, tline("a1", "t", "v1", "noul", `{}`))
	if _, err := traininglog.Export(traininglog.ExportOptions{TrainingDir: f.training, LedgerDir: filepath.Join(f.ledger, "nope")}, &out); err == nil {
		t.Error("an export over a missing ledger dir succeeded")
	}
	if _, err := traininglog.Export(traininglog.ExportOptions{TrainingDir: filepath.Join(f.training, "nope"), LedgerDir: f.ledger}, &out); err == nil {
		t.Error("an export over a missing training dir succeeded")
	}
}

// @test:TestExportSurvivesHostileLines
//
// 200 seeds of mangled training and outcome files. Whatever the bytes, the
// export never panics, never fails, and every row it does write is a
// well-formed five-field row whose label has the shape its type demands.
func TestExportSurvivesHostileLines(t *testing.T) {
	pieces := []string{
		tline("a1", "t", "v1", "noul", `{"x":1}`),
		tline("a2", "t", "v1", "choice", `{"x":2}`),
		tline("a3", "t", "v1", "score", `{"x":3}`),
		oline("a1", "t", "v1", `true`),
		oline("a2", "t", "v1", `"hard"`),
		oline("a3", "t", "v1", `2`),
		oline("a1", "t", "v2", `true`),
		oline("a2", "t", "v1", `7`),
		oline("a3", "t", "v1", `null`),
		`{`, `}`, `null`, `[]`, `""`, `{"answer_id":null}`, "\x00\x01\x02", `{"answer_id":"a1","template":{}}`,
		strings.Repeat("x", 70000), `{"answer_id":"a1","template":"t","template_version":"v1","type":"noul","state":null}`,
	}
	for seed := 0; seed < 200; seed++ {
		rng := rand.New(rand.NewSource(int64(seed)))
		f := newFixture(t)
		build := func() string {
			var b strings.Builder
			for i, n := 0, rng.Intn(12); i < n; i++ {
				p := pieces[rng.Intn(len(pieces))]
				if rng.Intn(6) == 0 && len(p) > 2 {
					p = p[:rng.Intn(len(p))] // truncate a piece anywhere
				}
				b.WriteString(p)
				b.WriteByte('\n')
			}
			s := b.String()
			if rng.Intn(3) == 0 && len(s) > 1 {
				s = s[:len(s)-1] // tear the tail
			}
			return s
		}
		if err := os.WriteFile(filepath.Join(f.training, traininglog.FileName), []byte(build()), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.ledger, "outcomes.ndjson"), []byte(build()), 0o600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("seed %d: panic: %v", seed, r)
				}
			}()
			if _, err := traininglog.Export(traininglog.ExportOptions{TrainingDir: f.training, LedgerDir: f.ledger}, &out); err != nil {
				t.Fatalf("seed %d: Export failed over hostile bytes: %v", seed, err)
			}
		}()
		for _, ln := range strings.Split(strings.TrimSpace(out.String()), "\n") {
			if ln == "" {
				continue
			}
			var row struct {
				Template        string          `json:"template"`
				TemplateVersion string          `json:"template_version"`
				Type            string          `json:"type"`
				State           json.RawMessage `json:"state"`
				Label           json.RawMessage `json:"label"`
			}
			var keys map[string]json.RawMessage
			if err := json.Unmarshal([]byte(ln), &keys); err != nil || len(keys) != 5 {
				t.Fatalf("seed %d: bad row %q", seed, ln)
			}
			_ = json.Unmarshal([]byte(ln), &row)
			if row.Template == "" || row.TemplateVersion == "" || len(row.State) == 0 || row.State[0] != '{' {
				t.Fatalf("seed %d: incomplete row %q", seed, ln)
			}
			switch row.Type {
			case "choice":
				var s string
				if json.Unmarshal(row.Label, &s) != nil {
					t.Fatalf("seed %d: choice label %s is not a string", seed, row.Label)
				}
			case "score":
				var n int
				if json.Unmarshal(row.Label, &n) != nil || n < 0 {
					t.Fatalf("seed %d: score label %s is not a non-negative integer", seed, row.Label)
				}
			case "noul":
				var b bool
				if json.Unmarshal(row.Label, &b) != nil {
					t.Fatalf("seed %d: noul label %s is not a boolean", seed, row.Label)
				}
			default:
				t.Fatalf("seed %d: row of type %q", seed, row.Type)
			}
		}
	}
}
