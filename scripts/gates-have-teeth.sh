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
