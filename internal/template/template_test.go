package template

import (
	"encoding/json"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func choiceCriteria(n int) json.RawMessage {
	m := map[string]string{}
	for i := 0; i < n; i++ {
		m[strings.Repeat("x", 1)+itoaTest(i)] = "description"
	}
	b, _ := json.Marshal(m)
	return b
}

func itoaTest(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	return s
}

func scoreCriteria(n int) json.RawMessage {
	levels := make([]string, n)
	for i := range levels {
		levels[i] = "level"
	}
	b, _ := json.Marshal(levels)
	return b
}

func validTemplate() Template {
	return Template{
		ID:           "eval.thing",
		Type:         TypeNoul,
		Instructions: "is it true",
		Fields:       []string{"task"},
	}
}

func TestValidateAcceptsAWellFormedTemplate(t *testing.T) {
	tpl := validTemplate()
	if err := tpl.Validate(); err != nil {
		t.Fatalf("expected a valid template, got %v", err)
	}
}

func TestValidateRejectsABadID(t *testing.T) {
	for _, id := range []string{"", "Upper", "-leads", "has space", strings.Repeat("a", 65)} {
		tpl := validTemplate()
		tpl.ID = id
		if err := tpl.Validate(); err == nil {
			t.Errorf("id %q should be rejected", id)
		}
	}
}

func TestValidateRejectsAnUnknownType(t *testing.T) {
	tpl := validTemplate()
	tpl.Type = "essay"
	if err := tpl.Validate(); err == nil {
		t.Fatal("expected an error for an unknown type")
	}
}

func TestValidateRejectsEmptyInstructions(t *testing.T) {
	tpl := validTemplate()
	tpl.Instructions = "   "
	if err := tpl.Validate(); err == nil {
		t.Fatal("expected an error for empty instructions")
	}
}

func TestValidateRejectsEmptyFields(t *testing.T) {
	tpl := validTemplate()
	tpl.Fields = nil
	if err := tpl.Validate(); err == nil {
		t.Fatal("expected an error for no fields")
	}
}

func TestValidateRejectsDuplicateFields(t *testing.T) {
	tpl := validTemplate()
	tpl.Fields = []string{"a", "a"}
	if err := tpl.Validate(); err == nil {
		t.Fatal("expected an error for a duplicate field name")
	}
}

func TestValidateRejectsMaxStateBytesOverTheCeiling(t *testing.T) {
	tpl := validTemplate()
	tpl.MaxStateBytes = MaxMaxStateBytes + 1
	if err := tpl.Validate(); err == nil {
		t.Fatal("expected an error for max_state_bytes over the ceiling")
	}
}

func TestValidateRejectsNegativeMaxStateBytes(t *testing.T) {
	tpl := validTemplate()
	tpl.MaxStateBytes = -1
	if err := tpl.Validate(); err == nil {
		t.Fatal("expected an error for a negative max_state_bytes")
	}
}

func TestEffectiveMaxStateBytesDefaults(t *testing.T) {
	tpl := validTemplate()
	if got := tpl.EffectiveMaxStateBytes(); got != DefaultMaxStateBytes {
		t.Errorf("got %d, want the default %d", got, DefaultMaxStateBytes)
	}
	tpl.MaxStateBytes = 500
	if got := tpl.EffectiveMaxStateBytes(); got != 500 {
		t.Errorf("got %d, want 500", got)
	}
}

func TestChoiceCriteriaBoundaries(t *testing.T) {
	cases := []struct {
		name    string
		n       int
		wantErr bool
	}{
		{"one option is too few", 1, true},
		{"two options is the floor", 2, false},
		{"255 options is the ceiling", 255, false},
		{"256 options is one too many", 256, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tpl := validTemplate()
			tpl.Type = TypeChoice
			tpl.Criteria = choiceCriteria(c.n)
			err := tpl.Validate()
			if c.wantErr && err == nil {
				t.Errorf("%d options: expected an error", c.n)
			}
			if !c.wantErr && err != nil {
				t.Errorf("%d options: unexpected error %v", c.n, err)
			}
		})
	}
}

func TestChoiceCriteriaMustBeAnObject(t *testing.T) {
	tpl := validTemplate()
	tpl.Type = TypeChoice
	tpl.Criteria = json.RawMessage(`["a", "b"]`)
	if err := tpl.Validate(); err == nil {
		t.Fatal("expected an error: choice criteria must be an object, not an array")
	}
}

func TestScoreCriteriaBoundaries(t *testing.T) {
	cases := []struct {
		name    string
		n       int
		wantErr bool
	}{
		{"one level is too few", 1, true},
		{"two levels is the floor", 2, false},
		{"ten levels is the ceiling", 10, false},
		{"eleven levels is one too many", 11, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tpl := validTemplate()
			tpl.Type = TypeScore
			tpl.Criteria = scoreCriteria(c.n)
			err := tpl.Validate()
			if c.wantErr && err == nil {
				t.Errorf("%d levels: expected an error", c.n)
			}
			if !c.wantErr && err != nil {
				t.Errorf("%d levels: unexpected error %v", c.n, err)
			}
		})
	}
}

func TestNoulCriteriaIsOptional(t *testing.T) {
	tpl := validTemplate()
	if err := tpl.Validate(); err != nil {
		t.Fatalf("noul with no criteria should be valid: %v", err)
	}
	trueDesc, falseDesc, ok, err := tpl.NoulCriteria()
	if err != nil || ok {
		t.Fatalf("expected no criteria, got ok=%v err=%v true=%q false=%q", ok, err, trueDesc, falseDesc)
	}
}

func TestNoulCriteriaRejectsAnUnknownKey(t *testing.T) {
	tpl := validTemplate()
	tpl.Criteria = json.RawMessage(`{"maybe": "x"}`)
	if err := tpl.Validate(); err == nil {
		t.Fatal("expected an error for a noul criteria key that is not true or false")
	}
}

func TestVersionIsStableAcrossWhitespaceAndKeyOrder(t *testing.T) {
	a := validTemplate()
	a.Type = TypeChoice
	a.Criteria = json.RawMessage(`{"x":"d1","y":"d2"}`)
	b := a
	b.Criteria = json.RawMessage(`{ "y" : "d2" , "x" : "d1" }`)
	if a.Version() != b.Version() {
		t.Errorf("versions differ for the same content with different whitespace/key order: %s vs %s", a.Version(), b.Version())
	}
}

func TestVersionChangesWithContent(t *testing.T) {
	a := validTemplate()
	b := validTemplate()
	b.Instructions = "different"
	if a.Version() == b.Version() {
		t.Error("two templates with different instructions must not share a version")
	}
}

func TestVersionIsLowercaseHex64Chars(t *testing.T) {
	v := validTemplate().Version()
	if len(v) != 64 {
		t.Fatalf("expected a 64-char hex sha256, got %d chars: %s", len(v), v)
	}
	if strings.ToLower(v) != v {
		t.Errorf("expected lowercase hex, got %s", v)
	}
}

func TestLoadDirReportsEachInvalidFileAndLoadsTheRest(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "good.json", `{"id":"good","type":"noul","instructions":"is it true","fields":["x"]}`)
	mustWrite(t, dir, "bad-json.json", `{not valid json`)
	mustWrite(t, dir, "bad-schema.json", `{"id":"BAD ID","type":"noul","instructions":"x","fields":["x"]}`)
	mustWrite(t, dir, "not-a-template.txt", `ignored`)

	reg, loadErrs, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if _, ok := reg.Get("good"); !ok {
		t.Error("the good template should have loaded")
	}
	if len(loadErrs) != 2 {
		t.Fatalf("expected 2 load errors, got %d: %v", len(loadErrs), loadErrs)
	}
}

func TestLoadDirReportsADuplicateID(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "a.json", `{"id":"dup","type":"noul","instructions":"x","fields":["x"]}`)
	mustWrite(t, dir, "b.json", `{"id":"dup","type":"noul","instructions":"y","fields":["x"]}`)
	reg, loadErrs, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(reg.List()) != 1 {
		t.Errorf("expected exactly one of the two duplicate templates to load, got %d", len(reg.List()))
	}
	if len(loadErrs) != 1 {
		t.Fatalf("expected one load error for the duplicate, got %d", len(loadErrs))
	}
}

func TestLoadDirOnAMissingDirectoryIsAnError(t *testing.T) {
	_, _, err := LoadDir(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("expected an error for a missing directory")
	}
}

func mustWrite(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

func TestFilterKeepsOnlyNamedFieldsAndCountsTheRest(t *testing.T) {
	tpl := validTemplate()
	tpl.Fields = []string{"task", "final_answer"}
	state := json.RawMessage(`{"task":"t","final_answer":"a","extra1":1,"extra2":2,"extra3":3}`)
	eg, heldBack, err := Filter(tpl, state)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if heldBack != 3 {
		t.Errorf("expected 3 held back fields, got %d", heldBack)
	}
	var kept map[string]json.RawMessage
	if err := json.Unmarshal(eg.Canonical(), &kept); err != nil {
		t.Fatalf("Canonical did not produce valid JSON: %v", err)
	}
	if len(kept) != 2 {
		t.Errorf("expected exactly 2 kept fields, got %d: %v", len(kept), kept)
	}
	if _, ok := kept["extra1"]; ok {
		t.Error("a field not named by the template reached the egress")
	}
}

func TestFilterKeepsNestedValuesWhole(t *testing.T) {
	tpl := validTemplate()
	tpl.Fields = []string{"nested"}
	state := json.RawMessage(`{"nested":{"a":[1,2,3],"b":{"c":true}}}`)
	eg, _, err := Filter(tpl, state)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	var kept map[string]json.RawMessage
	json.Unmarshal(eg.Canonical(), &kept)
	if string(kept["nested"]) != `{"a":[1,2,3],"b":{"c":true}}` {
		t.Errorf("nested value was not kept whole: %s", kept["nested"])
	}
}

func TestFilterRejectsANonObjectState(t *testing.T) {
	tpl := validTemplate()
	for _, state := range []string{`[1,2,3]`, `"a string"`, `42`, `null`, `true`} {
		_, _, err := Filter(tpl, json.RawMessage(state))
		if err != ErrBadState {
			t.Errorf("state %s: expected ErrBadState, got %v", state, err)
		}
	}
}

func TestFilterRejectsStateOverTheBound(t *testing.T) {
	tpl := validTemplate()
	tpl.MaxStateBytes = 10
	_, _, err := Filter(tpl, json.RawMessage(`{"task":"way more than ten bytes of json"}`))
	var tooLarge *StateTooLargeError
	if !asErr(err, &tooLarge) {
		t.Fatalf("expected a StateTooLargeError, got %v", err)
	}
	if tooLarge.Max != 10 {
		t.Errorf("expected Max 10, got %d", tooLarge.Max)
	}
}

func asErr(err error, target **StateTooLargeError) bool {
	e, ok := err.(*StateTooLargeError)
	if ok {
		*target = e
	}
	return ok
}

func TestEgressSHA384IsDeterministic(t *testing.T) {
	tpl := validTemplate()
	tpl.Fields = []string{"a"}
	state := json.RawMessage(`{"a":1}`)
	eg1, _, _ := Filter(tpl, state)
	eg2, _, _ := Filter(tpl, state)
	if eg1.SHA384() != eg2.SHA384() {
		t.Error("the same input produced two different hashes")
	}
	if len(eg1.SHA384()) != 96 {
		t.Errorf("expected a 96-char hex sha384, got %d", len(eg1.SHA384()))
	}
}

func TestNewFreeformEgressSendsEverything(t *testing.T) {
	state := json.RawMessage(`{"a":1,"b":2,"c":3}`)
	eg, err := NewFreeformEgress(state, 1<<20)
	if err != nil {
		t.Fatalf("NewFreeformEgress: %v", err)
	}
	var kept map[string]json.RawMessage
	json.Unmarshal(eg.Canonical(), &kept)
	if len(kept) != 3 {
		t.Errorf("expected all 3 fields, got %d", len(kept))
	}
}

func TestNewFreeformEgressRejectsOverTheBound(t *testing.T) {
	_, err := NewFreeformEgress(json.RawMessage(`{"a":"01234567890"}`), 5)
	var tooLarge *StateTooLargeError
	if !asErr(err, &tooLarge) {
		t.Fatalf("expected a StateTooLargeError, got %v", err)
	}
}

func TestLoadErrorMessageNamesTheFileAndReason(t *testing.T) {
	le := LoadError{File: "bad.json", Err: errors.New("boom")}
	if got := le.Error(); got == "" || !strings.Contains(got, "bad.json") {
		t.Errorf("expected the file name in the message, got %q", got)
	}
}

func TestStateTooLargeErrorMessage(t *testing.T) {
	e := &StateTooLargeError{Size: 100, Max: 10}
	if got := e.Error(); got == "" {
		t.Error("expected a non-empty message")
	}
}

func TestNilRegistryGetAndListAreSafe(t *testing.T) {
	var r *Registry
	if _, ok := r.Get("anything"); ok {
		t.Error("a nil registry should never find anything")
	}
	if got := r.List(); got != nil {
		t.Errorf("expected nil from a nil registry's List, got %v", got)
	}
}

func TestChoiceOptionsRejectsAnEmptyOptionName(t *testing.T) {
	tpl := validTemplate()
	tpl.Type = TypeChoice
	tpl.Criteria = json.RawMessage(`{"":"d1","b":"d2"}`)
	if _, err := tpl.ChoiceOptions(); err == nil {
		t.Fatal("expected an error for an empty option name")
	}
}

func TestChoiceOptionsCalledOnTheWrongTypeIsAnError(t *testing.T) {
	tpl := validTemplate() // noul
	if _, err := tpl.ChoiceOptions(); err == nil {
		t.Fatal("expected an error calling ChoiceOptions on a non-choice template")
	}
}

func TestScoreLevelsCalledOnTheWrongTypeIsAnError(t *testing.T) {
	tpl := validTemplate() // noul
	if _, err := tpl.ScoreLevels(); err == nil {
		t.Fatal("expected an error calling ScoreLevels on a non-score template")
	}
}

func TestScoreLevelsRejectsANonArrayCriteria(t *testing.T) {
	tpl := validTemplate()
	tpl.Type = TypeScore
	tpl.Criteria = json.RawMessage(`{"not":"an array"}`)
	if _, err := tpl.ScoreLevels(); err == nil {
		t.Fatal("expected an error for non-array score criteria")
	}
}

func TestNoulCriteriaCalledOnTheWrongTypeIsAnError(t *testing.T) {
	tpl := validTemplate()
	tpl.Type = TypeChoice
	tpl.Criteria = choiceCriteria(2)
	if _, _, _, err := tpl.NoulCriteria(); err == nil {
		t.Fatal("expected an error calling NoulCriteria on a non-noul template")
	}
}

func TestNoulCriteriaAcceptsJustOneOfTrueOrFalse(t *testing.T) {
	tpl := validTemplate()
	tpl.Criteria = json.RawMessage(`{"true":"it happened"}`)
	trueDesc, falseDesc, ok, err := tpl.NoulCriteria()
	if err != nil || !ok || trueDesc != "it happened" || falseDesc != "" {
		t.Fatalf("unexpected result: %q %q %v %v", trueDesc, falseDesc, ok, err)
	}
}

func TestZeroValueEgressCanonicalIsAnEmptyObject(t *testing.T) {
	var eg Egress
	if string(eg.Canonical()) != "{}" {
		t.Errorf("expected {}, got %s", eg.Canonical())
	}
}

func TestNewFreeformEgressRejectsANonObjectState(t *testing.T) {
	_, err := NewFreeformEgress(json.RawMessage(`[1,2,3]`), 1<<20)
	if err != ErrBadState {
		t.Fatalf("expected ErrBadState, got %v", err)
	}
}

func TestGetAndListOnALoadedRegistry(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "a.json", `{"id":"a","type":"noul","instructions":"x","fields":["f"]}`)
	reg, _, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if _, ok := reg.Get("nope"); ok {
		t.Error("expected Get to miss for an id that was never loaded")
	}
	if len(reg.List()) != 1 {
		t.Errorf("expected 1 template in List, got %d", len(reg.List()))
	}
}

// TestLoadDirNeverPanicsOnHostileFiles is a seeded sweep of mutated/random
// bytes fed to LoadDir as a template file: it must always come back as
// either a loaded template or a LoadError, and it must never panic. 200
// seeds, fixed, so a failure is reproducible.
func TestLoadDirNeverPanicsOnHostileFiles(t *testing.T) {
	base := []byte(`{"id":"a","type":"choice","instructions":"x","criteria":{"a":"x","b":"y"},"fields":["f"],"max_state_bytes":100}`)
	for seed := int64(0); seed < 200; seed++ {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("seed %d: LoadDir panicked: %v", seed, r)
				}
			}()
			dir := t.TempDir()
			mustWrite(t, dir, "t.json", string(mutate(base, seed)))
			_, _, _ = LoadDir(dir)
		}()
	}
}

// mutate returns a randomized corruption of b, deterministic in seed.
func mutate(b []byte, seed int64) []byte {
	r := rand.New(rand.NewSource(seed))
	out := append([]byte(nil), b...)
	n := r.Intn(10) + 1
	for i := 0; i < n; i++ {
		if len(out) == 0 {
			break
		}
		switch r.Intn(4) {
		case 0: // flip a byte
			out[r.Intn(len(out))] = byte(r.Intn(256))
		case 1: // truncate
			cut := r.Intn(len(out) + 1)
			out = out[:cut]
		case 2: // insert junk
			pos := r.Intn(len(out) + 1)
			junk := []byte{byte(r.Intn(256))}
			out = append(out[:pos], append(junk, out[pos:]...)...)
		case 3: // duplicate a slice
			if len(out) < 2 {
				continue
			}
			a := r.Intn(len(out))
			bnd := a + r.Intn(len(out)-a)
			out = append(out, out[a:bnd]...)
		}
	}
	return out
}
