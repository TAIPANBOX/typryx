#!/usr/bin/env bash
# Each gate is shown its own fault and required to fail on it, then shown a
# non-fault and required not to. A gate that cannot go red is a green badge
# over nothing.
#
# Needs a clean tree: several cases edit tracked files and restore with
# `git checkout`, and cannot tell your edits from their own.
#
# Harness copied from vouchryx scripts/gates-have-teeth.sh (fault/fault_all);
# every case below is typryx's own.
set -euo pipefail
cd "$(dirname "$0")/.."
[ -z "$(git status --porcelain)" ] || {
  echo "this script mutates tracked files, so it needs a clean tree." >&2
  echo "commit or stash first." >&2
  exit 2
}
cases=0

fault() { # name file from to expect(fail|pass) gate
  python3 - "$2" "$3" "$4" <<'PY'
import io,sys
p,a,b=sys.argv[1],sys.argv[2],sys.argv[3]
s=io.open(p,encoding='utf-8').read()
assert s.count(a)==1, f"anchor x{s.count(a)} in {p}"
io.open(p,'w',encoding='utf-8').write(s.replace(a,b))
PY
  if "$6" >/dev/null 2>&1; then got=pass; else got=fail; fi
  git checkout -- . >/dev/null 2>&1
  cases=$((cases + 1))
  if [ "$got" != "$5" ]; then
    echo "TOOTHLESS: $1 -> $got, wanted $5" >&2
    exit 1
  fi
  printf "ok  %-56s (%s)\n" "$1" "$5"
}

fault_all() { # name file from to expect(fail|pass) gate
  python3 - "$2" "$3" "$4" <<'PY'
import io,sys
p,a,b=sys.argv[1],sys.argv[2],sys.argv[3]
s=io.open(p,encoding='utf-8').read()
assert s.count(a) >= 1, f"anchor absent in {p}"
io.open(p,'w',encoding='utf-8').write(s.replace(a,b))
PY
  if "$6" >/dev/null 2>&1; then got=pass; else got=fail; fi
  git checkout -- . >/dev/null 2>&1
  cases=$((cases + 1))
  if [ "$got" != "$5" ]; then
    echo "TOOTHLESS: $1 -> $got, wanted $5" >&2
    exit 1
  fi
  printf "ok  %-56s (%s)\n" "$1" "$5"
}

# --- features-are-bound.sh --------------------------------------------------

fault "features: a scenario points at a test that does not exist" \
  features/typed-answers.feature '# @test:TestTheManifestMatchesWhatTheBinaryReads' '# @test:TestNoSuchTestAnywhere' \
  fail ./scripts/features-are-bound.sh

fault "features: a scenario with no binding" \
  features/typed-answers.feature \
  '  Scenario: What the service declares about itself is true' \
  '  Scenario: An unbound scenario nobody wired to a test
    Given nothing in particular

  Scenario: What the service declares about itself is true' \
  fail ./scripts/features-are-bound.sh

fault "features: a test name mentioned in prose must not fire the binding" \
  features/typed-answers.feature \
  '  Scenario: What the service declares about itself is true' \
  '  Scenario: What the service declares about itself is true
    # TestTheManifestMatchesWhatTheBinaryReads is mentioned here in prose, not as a binding' \
  pass ./scripts/features-are-bound.sh

echo "-- features dir removed --"
mv features features.gate-teeth-hidden
if ./scripts/features-are-bound.sh >/dev/null 2>&1; then got=pass; else got=fail; fi
mv features.gate-teeth-hidden features
cases=$((cases + 1))
if [ "$got" != "fail" ]; then
  echo "TOOTHLESS: features dir removed -> $got, wanted fail" >&2
  exit 1
fi
printf "ok  %-56s (%s)\n" "features dir removed" "fail"

# --- readme-numbers.sh -------------------------------------------------------

fault "readme numbers: the badge count drifts from the suite" \
  README.md 'tests-' 'tests-99999-' \
  fail ./scripts/readme-numbers.sh

prose_anchor=$(grep -oE '^[0-9]+ tests\.' README.md | head -1 || true)
[ -n "$prose_anchor" ] || {
  echo "UNJUDGEABLE: readme numbers: no '<N> tests.' line in README.md; this case measured nothing" >&2
  exit 1
}
fault "readme numbers: the prose test count drifts from the suite" \
  README.md "$prose_anchor" '999 tests.' \
  fail ./scripts/readme-numbers.sh

# --- one-way-out.sh -----------------------------------------------------------

fault "one-way-out: an http.Client{} planted in internal/api/api.go" \
  internal/api/api.go 'func NewMux(s *Server) *http.ServeMux {' 'var leaked = http.Client{}

func NewMux(s *Server) *http.ServeMux {' \
  fail ./scripts/one-way-out.sh

fault "one-way-out: the same, planted in a _test.go file, must not fire" \
  internal/api/api_test.go 'package api_test' 'package api_test

var leakedInTest = http.Client{}' \
  pass ./scripts/one-way-out.sh

fault "one-way-out: a net.Dial planted in internal/service" \
  internal/service/service.go 'package service' 'package service

var _ = net.Dial' \
  fail ./scripts/one-way-out.sh

fault "one-way-out: an http.Client{} planted in internal/wardryxproxy, the named exception, must not fire" \
  internal/wardryxproxy/proxy.go 'func New(c Config) *Proxy {' 'var leakedInProxy = http.Client{}

func New(c Config) *Proxy {' \
  pass ./scripts/one-way-out.sh

fault "one-way-out: an http.Client{} planted in internal/wardryxproxy's neighbour, internal/door, still fires" \
  internal/door/door.go 'package door' 'package door

var leakedInDoor = http.Client{}' \
  fail ./scripts/one-way-out.sh

fault "one-way-out: an http.Client{} planted in examples/calibration, exempted, must not fire" \
  examples/calibration/main.go 'func main() {' 'var leakedInExample = http.Client{}

func main() {' \
  pass ./scripts/one-way-out.sh

echo "-- one-way-out: every .go file removed --"
find . -name '*.go' -not -path './.git/*' -exec mv {} {}.gate-teeth-hidden \;
if ./scripts/one-way-out.sh >/dev/null 2>&1; then got=pass; else got=fail; fi
find . -name '*.go.gate-teeth-hidden' -not -path './.git/*' -exec bash -c 'mv "$1" "${1%.gate-teeth-hidden}"' _ {} \;
cases=$((cases + 1))
if [ "$got" != "fail" ]; then
  echo "TOOTHLESS: one-way-out with no .go files -> $got, wanted fail" >&2
  exit 1
fi
printf "ok  %-56s (%s)\n" "one-way-out: every .go file removed" "fail"

# --- no-secrets.sh -------------------------------------------------------------
#
# The two fault strings below (an sk-ant- shaped key, a PEM private key
# header) are built by concatenating pieces at RUN TIME rather than written
# as one contiguous literal. If they were written as one literal, this
# script's own tracked source would itself match no-secrets.sh's patterns,
# and so would the git history entry that commits this file: the gate would
# then fail forever on its own teeth harness rather than only during the
# instant a case plants and immediately reverts a fault.

fake_ant_key="sk-ant$(printf -- '-')api03$(printf -- '-')$(printf '0%.0s' $(seq 1 48))"

fault "no-secrets: a fake sk-ant- key planted in internal/api/api.go" \
  internal/api/api.go 'package api' "package api

// ${fake_ant_key}" \
  fail ./scripts/no-secrets.sh

echo "-- no-secrets: a PEM private key header in a new tracked file --"
pem_header="$(printf -- '-----BEGIN')$(printf ' RSA PRIVATE KEY')$(printf -- '-----')"
printf '%s\nnot a real key, just the header shape\n' "$pem_header" > .gate-teeth-secret-fixture.pem.txt
git add -f .gate-teeth-secret-fixture.pem.txt
if ./scripts/no-secrets.sh >/dev/null 2>&1; then got=pass; else got=fail; fi
git reset -q
git checkout -- . >/dev/null 2>&1 || true
git clean -fdq .gate-teeth-secret-fixture.pem.txt
cases=$((cases + 1))
if [ "$got" != "fail" ]; then
  echo "TOOTHLESS: no-secrets with a planted PEM header -> $got, wanted fail" >&2
  exit 1
fi
printf "ok  %-56s (%s)\n" "no-secrets: a planted PEM private key header" "fail"

echo "-- no-secrets: an ordinary new tracked file must pass --"
printf 'just an ordinary line, nothing secret about it\n' > .gate-teeth-normal-fixture.txt
git add -f .gate-teeth-normal-fixture.txt
if ./scripts/no-secrets.sh >/dev/null 2>&1; then got=pass; else got=fail; fi
git reset -q
git checkout -- . >/dev/null 2>&1 || true
git clean -fdq .gate-teeth-normal-fixture.txt
cases=$((cases + 1))
if [ "$got" != "pass" ]; then
  echo "TOOTHLESS: no-secrets with an ordinary file -> $got, wanted pass" >&2
  exit 1
fi
printf "ok  %-56s (%s)\n" "no-secrets: an ordinary new file" "pass"

# --- templates-load.sh -------------------------------------------------------

fault "templates-load: a syntax error in a template file" \
  examples/templates/eval.outcome_met.json '"id": "eval.outcome_met",' '"id": "eval.outcome_met" THIS IS NOT JSON,' \
  fail ./scripts/templates-load.sh

fault "templates-load: a template naming user_email in fields" \
  examples/templates/eval.outcome_met.json '"fields": ["task", "final_answer"],' '"fields": ["task", "final_answer", "user_email"],' \
  fail ./scripts/templates-load.sh

echo "-- templates-load: the directory emptied --"
mv examples/templates examples/templates.gate-teeth-hidden
mkdir examples/templates
if ./scripts/templates-load.sh >/dev/null 2>&1; then got=pass; else got=fail; fi
rm -rf examples/templates
mv examples/templates.gate-teeth-hidden examples/templates
cases=$((cases + 1))
if [ "$got" != "fail" ]; then
  echo "TOOTHLESS: templates-load with the directory emptied -> $got, wanted fail" >&2
  exit 1
fi
printf "ok  %-56s (%s)\n" "templates-load: the directory emptied" "fail"

echo "-- templates-load: a new valid template must not fire the gate --"
cat > examples/templates/.gate-teeth-extra-template.json <<'JSON'
{
  "id": "gate.teeth.extra",
  "type": "noul",
  "instructions": "is this a harmless extra starter template",
  "fields": ["x"]
}
JSON
if ./scripts/templates-load.sh >/dev/null 2>&1; then got=pass; else got=fail; fi
rm -f examples/templates/.gate-teeth-extra-template.json
cases=$((cases + 1))
if [ "$got" != "pass" ]; then
  echo "TOOTHLESS: templates-load with a new valid template -> $got, wanted pass" >&2
  exit 1
fi
printf "ok  %-56s (%s)\n" "templates-load: a new valid template" "pass"

# --- request-shape.sh --------------------------------------------------------

fault "request-shape: reasoning_effort sent even when unset" \
  internal/backend/openai.go '`json:"reasoning_effort,omitempty"`' '`json:"reasoning_effort"`' \
  fail ./scripts/request-shape.sh

fault "request-shape: the default stops sending max_tokens: 1" \
  internal/backend/openai.go 'reqBody.MaxTokens = 1' 'reqBody.MaxCompletionTokens = 1' \
  fail ./scripts/request-shape.sh

fault "request-shape: the default asks for five alternatives" \
  internal/backend/openai.go 'const MaxTopLogProbs = 20' 'const MaxTopLogProbs = 5' \
  fail ./scripts/request-shape.sh

fault "request-shape: max_tokens sent beside max_completion_tokens" \
  internal/backend/openai.go 'reqBody.MaxCompletionTokens = o.cfg.MaxCompletionTokens' 'reqBody.MaxCompletionTokens = o.cfg.MaxCompletionTokens
		reqBody.MaxTokens = 1' \
  fail ./scripts/request-shape.sh

fault "request-shape: a later token read instead of the first" \
  internal/backend/openai.go 'return content[0].TopLogProbs, true' 'return content[len(content)-1].TopLogProbs, true' \
  fail ./scripts/request-shape.sh

fault "request-shape: a near-certain token refused as a broken distribution" \
  internal/backend/openai.go 'maxLogprob   = 1e-3' 'maxLogprob   = 1e-6' \
  fail ./scripts/request-shape.sh

fault "request-shape: a setting dropped on its way to the backend" \
  cmd/typryx/main.go 'ReasoningEffort:     cfg.openai.reasoningEffort,' '' \
  fail ./scripts/request-shape.sh

fault "request-shape: a comment reworded must not fire" \
  internal/backend/openai.go '// chatRequest is the request body.' '// chatRequest is the body of the request.' \
  pass ./scripts/request-shape.sh

fault "request-shape: a test renamed away measures nothing" \
  internal/backend/openai_test.go 'func TestACurrentModelIsAskedTheWayItAccepts(' 'func TestNoLongerTheNameTheGateReads(' \
  fail ./scripts/request-shape.sh

# --- ./scripts/coverage-floor.sh -------------------------------------------
# The coverage floor reads a report the toolchain wrote, so its cases plant
# reports in a scratch directory instead of mutating a tracked file: one per
# format the gate reads (go, rust, python, js), each below the floor (must
# fail), above it (must pass), and with nothing in it (must say MEASURED
# NOTHING). Then the ratchet: a floor that went down must fail, one that rose
# must pass, and no floor file at all is MEASURED NOTHING.
cov_dir="$(mktemp -d)"
cov_failures=0
cov_case() { # name, expect (fail|pass|nothing), command...
	local name="$1" expect="$2" out rc ok=0 said=0
	shift 2
	cases=$((cases + 1))
	if out="$("$@" 2>&1)"; then rc=0; else rc=$?; fi
	if grep -q 'MEASURED NOTHING' <<<"$out"; then said=1; fi
	case "$expect" in
	fail) if [ "$rc" -ne 0 ] && [ "$said" -eq 0 ]; then ok=1; fi ;;
	pass) if [ "$rc" -eq 0 ]; then ok=1; fi ;;
	nothing) if [ "$rc" -ne 0 ] && [ "$said" -eq 1 ]; then ok=1; fi ;;
	esac
	if [ "$ok" -eq 1 ]; then
		printf 'ok    coverage-floor: %s\n' "$name"
	else
		printf 'FAIL  coverage-floor: %s (expected %s, exit %s)\n' "$name" "$expect" "$rc"
		printf '%s\n' "$out" | sed 's/^/      /'
		cov_failures=$((cov_failures + 1))
	fi
}
printf '50\n' >"$cov_dir/floor"
printf 'a.go:1:\tF\t40.0%%\ntotal:\t(statements)\t40.0%%\n' >"$cov_dir/go-low"
printf 'a.go:1:\tF\t60.0%%\ntotal:\t(statements)\t60.0%%\n' >"$cov_dir/go-high"
printf 'total:\t(statements)\t0.0%%\n' >"$cov_dir/go-empty"
printf '{"data":[{"totals":{"lines":{"count":10,"covered":4,"percent":40.0}}}]}' >"$cov_dir/rust-low"
printf '{"data":[{"totals":{"lines":{"count":10,"covered":6,"percent":60.0}}}]}' >"$cov_dir/rust-high"
printf '{"data":[{"totals":{"lines":{"count":0,"covered":0,"percent":0}}}]}' >"$cov_dir/rust-empty"
printf '{"totals":{"num_statements":10,"percent_covered":40.0}}' >"$cov_dir/py-low"
printf '{"totals":{"num_statements":10,"percent_covered":60.0}}' >"$cov_dir/py-high"
printf '{"totals":{"num_statements":0,"percent_covered":100.0}}' >"$cov_dir/py-empty"
printf '{"total":{"lines":{"total":10,"covered":4,"pct":40}}}' >"$cov_dir/js-low"
printf '{"total":{"lines":{"total":10,"covered":6,"pct":60}}}' >"$cov_dir/js-high"
printf '{"total":{"lines":{"total":0,"covered":0,"pct":100}}}' >"$cov_dir/js-empty"
for f in go rust py js; do
	cov_case "$f below the floor" fail ./scripts/coverage-floor.sh check "$cov_dir/$f-low" "$cov_dir/floor"
	cov_case "$f above the floor" pass ./scripts/coverage-floor.sh check "$cov_dir/$f-high" "$cov_dir/floor"
	cov_case "$f counting nothing" nothing ./scripts/coverage-floor.sh check "$cov_dir/$f-empty" "$cov_dir/floor"
done
cov_case "no report at all" nothing ./scripts/coverage-floor.sh check "$cov_dir/absent" "$cov_dir/floor"
printf 'not a report\n' >"$cov_dir/garbage"
cov_case "a file that is not a report" nothing ./scripts/coverage-floor.sh check "$cov_dir/garbage" "$cov_dir/floor"
cov_case "no floor recorded" nothing ./scripts/coverage-floor.sh check "$cov_dir/go-high" "$cov_dir/absent"
printf '49.5\n' >"$cov_dir/lower"
printf '# comment\n51\n' >"$cov_dir/higher"
cov_case "ratchet: floor went down" fail ./scripts/coverage-floor.sh ratchet "$cov_dir/floor" "$cov_dir/lower"
cov_case "ratchet: floor rose" pass ./scripts/coverage-floor.sh ratchet "$cov_dir/floor" "$cov_dir/higher"
cov_case "ratchet: no floor file" nothing ./scripts/coverage-floor.sh ratchet "$cov_dir/floor" "$cov_dir/absent"
# The floor this repository actually records must be one the gate can read.
cov_case "the recorded floor parses" pass ./scripts/coverage-floor.sh ratchet "$cov_dir/absent" "${COVERAGE_FLOOR_FILE:-.coverage-floor}"
rm -rf "$cov_dir"
if [ "$cov_failures" -gt 0 ]; then
	if [ -n "${failures+x}" ]; then
		failures=$((failures + cov_failures))
	else
		echo "TOOTHLESS: coverage-floor, $cov_failures case(s) above" >&2
		exit 1
	fi
fi
# ---------------------------------------------------------------------------

# --- every gate in scripts/ has a case here ---------------------------------

uncovered=""
for gate in scripts/*.sh; do
  base="$(basename "$gate")"
  [ "$base" = gates-have-teeth.sh ] && continue
  grep -qF -- "./scripts/$base" "$0" || uncovered="$uncovered $base"
done
if [ -n "$uncovered" ]; then
  echo
  echo "no case in this file exercises:$uncovered" >&2
  echo "A gate with no case here is a gate nothing proves can go red." >&2
  exit 1
fi

echo
echo "$cases cases: every gate fails on its own fault and passes on what it must not catch."
