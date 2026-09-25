// Package template loads and validates the JSON question templates that are
// typryx's contract with a customer: a template names a question type, the
// instructions and criteria that go to a backend, and the allowlist of state
// fields that may leave the box.
//
// A template is versioned by the content digest of its own normalized JSON,
// so an answer given under one version can never be silently re-scored
// against a later, different question (invariant: an outcome is scored
// against the version it was asked under, see internal/service).
package template

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Type is the shape of question a template asks.
type Type string

const (
	TypeChoice Type = "choice"
	TypeScore  Type = "score"
	TypeNoul   Type = "noul"
)

// DefaultMaxStateBytes and MaxMaxStateBytes bound MaxStateBytes: zero in a
// template file means the default, and nothing in this build may ask for
// more than the ceiling, template author or not.
const (
	DefaultMaxStateBytes = 16384
	MaxMaxStateBytes     = 1048576
)

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Template is one question a customer can ask, as loaded from a JSON file.
type Template struct {
	ID            string          `json:"id"`
	Type          Type            `json:"type"`
	Instructions  string          `json:"instructions"`
	Criteria      json.RawMessage `json:"criteria,omitempty"`
	Fields        []string        `json:"fields"`
	MaxStateBytes int             `json:"max_state_bytes,omitempty"`
}

// EffectiveMaxStateBytes is MaxStateBytes with the zero-value default applied.
// Validate already refuses anything above MaxMaxStateBytes, so callers that
// only ever see a validated Template need not re-check the ceiling.
func (t Template) EffectiveMaxStateBytes() int {
	if t.MaxStateBytes <= 0 {
		return DefaultMaxStateBytes
	}
	return t.MaxStateBytes
}

// ChoiceOption is one named option of a "choice" template's criteria.
type ChoiceOption struct {
	Name        string
	Description string
}

// Validate checks the shape rules a template must hold: a well-formed id, a
// known type, criteria that parses for that type, a non-empty and unique
// field list, and a state size bound within range. It does not touch a
// filesystem or a registry; that is LoadDir's job.
func (t Template) Validate() error {
	if !idPattern.MatchString(t.ID) {
		return fmt.Errorf("id %q does not match %s", t.ID, idPattern.String())
	}
	switch t.Type {
	case TypeChoice, TypeScore, TypeNoul:
	default:
		return fmt.Errorf("type %q is not one of choice, score, noul", t.Type)
	}
	if strings.TrimSpace(t.Instructions) == "" {
		return errors.New("instructions must not be empty")
	}
	if len(t.Fields) == 0 {
		return errors.New("fields must name at least one state key")
	}
	seen := map[string]bool{}
	for _, f := range t.Fields {
		if f == "" {
			return errors.New("fields must not contain an empty name")
		}
		if seen[f] {
			return fmt.Errorf("fields names %q more than once", f)
		}
		seen[f] = true
	}
	if t.MaxStateBytes < 0 {
		return errors.New("max_state_bytes must not be negative")
	}
	if t.MaxStateBytes > MaxMaxStateBytes {
		return fmt.Errorf("max_state_bytes %d exceeds the ceiling of %d", t.MaxStateBytes, MaxMaxStateBytes)
	}

	switch t.Type {
	case TypeChoice:
		_, err := t.ChoiceOptions()
		return err
	case TypeScore:
		_, err := t.ScoreLevels()
		return err
	case TypeNoul:
		_, _, _, err := t.NoulCriteria()
		return err
	}
	return nil
}

// ChoiceOptions decodes a "choice" template's criteria: an object mapping
// each option's name to its description, 2 to 255 options. The result is
// sorted by name, because a JSON object carries no order of its own and a
// backend (the stub especially) needs one it can repeat.
func (t Template) ChoiceOptions() ([]ChoiceOption, error) {
	if t.Type != TypeChoice {
		return nil, fmt.Errorf("ChoiceOptions called on a %s template", t.Type)
	}
	if len(t.Criteria) == 0 {
		return nil, errors.New("a choice template needs criteria: an object of option name to description")
	}
	var m map[string]string
	if err := json.Unmarshal(t.Criteria, &m); err != nil {
		return nil, fmt.Errorf("choice criteria must be an object of option name to description: %w", err)
	}
	if len(m) < 2 || len(m) > 255 {
		return nil, fmt.Errorf("a choice template needs 2 to 255 options, got %d", len(m))
	}
	out := make([]ChoiceOption, 0, len(m))
	for name, desc := range m {
		if name == "" {
			return nil, errors.New("a choice option name must not be empty")
		}
		out = append(out, ChoiceOption{Name: name, Description: desc})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ScoreLevels decodes a "score" template's criteria: an array of 2 to 10
// ordered level descriptions. Array position IS the score: level 0 is the
// first entry.
func (t Template) ScoreLevels() ([]string, error) {
	if t.Type != TypeScore {
		return nil, fmt.Errorf("ScoreLevels called on a %s template", t.Type)
	}
	if len(t.Criteria) == 0 {
		return nil, errors.New("a score template needs criteria: an array of 2 to 10 level descriptions")
	}
	var levels []string
	if err := json.Unmarshal(t.Criteria, &levels); err != nil {
		return nil, fmt.Errorf("score criteria must be an array of level descriptions: %w", err)
	}
	if len(levels) < 2 || len(levels) > 10 {
		return nil, fmt.Errorf("a score template needs 2 to 10 levels, got %d", len(levels))
	}
	return levels, nil
}

// NoulCriteria decodes a "noul" (yes/no) template's optional criteria:
// {"true": "...", "false": "..."}. Absent criteria is valid; ok reports
// whether any was given.
func (t Template) NoulCriteria() (trueDesc, falseDesc string, ok bool, err error) {
	if t.Type != TypeNoul {
		return "", "", false, fmt.Errorf("NoulCriteria called on a %s template", t.Type)
	}
	if len(t.Criteria) == 0 || string(t.Criteria) == "null" {
		return "", "", false, nil
	}
	var m map[string]string
	if err := json.Unmarshal(t.Criteria, &m); err != nil {
		return "", "", false, fmt.Errorf("noul criteria must be an object with true and/or false: %w", err)
	}
	for k := range m {
		if k != "true" && k != "false" {
			return "", "", false, fmt.Errorf("noul criteria only takes true and false, got %q", k)
		}
	}
	return m["true"], m["false"], true, nil
}

// canonicalForm is what Version() hashes: Criteria is decoded to a generic
// value so nested object keys sort the same way encoding/json always sorts
// map keys, and re-encoding removes any whitespace difference between two
// files that describe the same template.
//
// This is the canonical form, and it is documented here rather than assumed:
// two template files that decode to the same values but differ in Fields
// ORDER produce two different versions, because Fields is a meaningful,
// author-ordered list here, not a set. That is a real limitation and not an
// oversight: reordering Fields with no other change is rare enough that
// treating it as a new version, and therefore a new thing to re-approve, is
// the safer of the two mistakes to make by default.
type canonicalForm struct {
	ID            string   `json:"id"`
	Type          string   `json:"type"`
	Instructions  string   `json:"instructions"`
	Criteria      any      `json:"criteria"`
	Fields        []string `json:"fields"`
	MaxStateBytes int      `json:"max_state_bytes"`
}

// Version is the lowercase hex SHA-256 of the template's canonical JSON.
func (t Template) Version() string {
	var criteria any
	if len(t.Criteria) > 0 {
		_ = json.Unmarshal(t.Criteria, &criteria)
	}
	cf := canonicalForm{
		ID:            t.ID,
		Type:          string(t.Type),
		Instructions:  t.Instructions,
		Criteria:      criteria,
		Fields:        t.Fields,
		MaxStateBytes: t.EffectiveMaxStateBytes(),
	}
	b, err := json.Marshal(cf)
	if err != nil {
		// Marshal of this shape cannot fail in practice (no channels, no
		// functions, no cyclic maps reachable from JSON-decoded values); a
		// panic here means the standard library's own contract broke.
		panic("template: canonical form did not marshal: " + err.Error())
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// LoadError is one template file that failed to load or validate.
type LoadError struct {
	File string
	Err  error
}

func (e LoadError) Error() string { return fmt.Sprintf("%s: %v", e.File, e.Err) }

// Registry is a set of templates loaded from a directory, keyed by ID.
type Registry struct {
	byID map[string]Template
}

// Get looks a template up by id.
func (r *Registry) Get(id string) (Template, bool) {
	if r == nil {
		return Template{}, false
	}
	t, ok := r.byID[id]
	return t, ok
}

// List returns every template, sorted by ID.
func (r *Registry) List() []Template {
	if r == nil {
		return nil
	}
	out := make([]Template, 0, len(r.byID))
	for _, t := range r.byID {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// LoadDir reads every *.json file in dir, validates it, and returns a
// Registry of the valid ones plus one LoadError per file that was not.
// A directory that cannot be read at all is a plain error; an individual bad
// template inside it is not, because the whole point of this shape is that
// `typryx templates check` can report every failure in one pass rather than
// stopping at the first.
func LoadDir(dir string) (*Registry, []LoadError, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("reading template directory %s: %w", dir, err)
	}
	reg := &Registry{byID: map[string]Template{}}
	var loadErrs []LoadError
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			loadErrs = append(loadErrs, LoadError{File: e.Name(), Err: err})
			continue
		}
		var t Template
		if err := json.Unmarshal(b, &t); err != nil {
			loadErrs = append(loadErrs, LoadError{File: e.Name(), Err: fmt.Errorf("invalid JSON: %w", err)})
			continue
		}
		if err := t.Validate(); err != nil {
			loadErrs = append(loadErrs, LoadError{File: e.Name(), Err: err})
			continue
		}
		if _, dup := reg.byID[t.ID]; dup {
			loadErrs = append(loadErrs, LoadError{File: e.Name(), Err: fmt.Errorf("duplicate template id %q", t.ID)})
			continue
		}
		reg.byID[t.ID] = t
	}
	return reg, loadErrs, nil
}

// ErrBadState means the state a caller sent was not a JSON object.
var ErrBadState = errors.New("state must be a JSON object")

// StateTooLargeError means the state exceeded the template's byte bound,
// before it was even parsed.
type StateTooLargeError struct {
	Size, Max int
}

func (e *StateTooLargeError) Error() string {
	return fmt.Sprintf("state is %d bytes, over the template's bound of %d", e.Size, e.Max)
}

// Egress is what actually leaves the box for one ask: the subset of a
// caller's state that a template's Fields named, or (in freeform mode) the
// whole state. Its fields are unexported and it is constructible only from
// inside this package, which is how invariant 2 (only a template's fields
// reach a backend) is held by the type system rather than by a promise: a
// backend's Ask method takes an Egress, never a map or raw bytes, so there is
// no way to hand it something that skipped the filter.
type Egress struct {
	kept map[string]json.RawMessage
}

// Canonical is the JSON object of exactly the kept fields, key-sorted by
// encoding/json's own map marshaling.
func (e Egress) Canonical() []byte {
	if e.kept == nil {
		b, _ := json.Marshal(map[string]json.RawMessage{})
		return b
	}
	b, err := json.Marshal(e.kept)
	if err != nil {
		panic("template: egress did not marshal: " + err.Error())
	}
	return b
}

// SHA384 is the hex SHA-384 of Canonical, the value the record package puts
// on the trail instead of the state itself.
func (e Egress) SHA384() string {
	sum := sha512.Sum384(e.Canonical())
	return hex.EncodeToString(sum[:])
}

// Filter applies a template's field allowlist to a caller's state: the size
// bound is checked first, against the raw bytes, before anything is parsed,
// so an oversized hostile payload never reaches json.Unmarshal; then the
// state must decode as a JSON object, and finally only the named Fields are
// kept. heldBack counts the keys that were dropped.
func Filter(t Template, state json.RawMessage) (Egress, int, error) {
	max := t.EffectiveMaxStateBytes()
	if len(state) > max {
		return Egress{}, 0, &StateTooLargeError{Size: len(state), Max: max}
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(state, &m); err != nil || m == nil {
		// json.Unmarshal accepts a top-level `null` into a map with no error,
		// leaving it nil: that is not an object, so it is refused the same as
		// any other non-object state rather than silently treated as one
		// with zero fields.
		return Egress{}, 0, ErrBadState
	}
	fieldSet := map[string]bool{}
	for _, f := range t.Fields {
		fieldSet[f] = true
	}
	kept := map[string]json.RawMessage{}
	heldBack := 0
	for k, v := range m {
		if fieldSet[k] {
			kept[k] = v
		} else {
			heldBack++
		}
	}
	return Egress{kept: kept}, heldBack, nil
}

// NewFreeformEgress builds an Egress carrying the whole of state, unfiltered.
// It exists only for the freeform path (TYPRYX_ALLOW_FREEFORM), where there
// is no template to name an allowlist and the operator has explicitly chosen
// to send everything; every other path reaches a backend through Filter.
func NewFreeformEgress(state json.RawMessage, maxBytes int) (Egress, error) {
	if len(state) > maxBytes {
		return Egress{}, &StateTooLargeError{Size: len(state), Max: maxBytes}
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(state, &m); err != nil || m == nil {
		return Egress{}, ErrBadState
	}
	return Egress{kept: m}, nil
}
