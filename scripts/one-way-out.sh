#!/usr/bin/env bash
# Only internal/backend may reach the network. Every other package answers
# HTTP and MCP requests; none of them originates one. This is the structural
# half of "a backend is the only way out": the day a real backend (jev,
# openai-logprobs) is added, its HTTP client construction belongs in
# internal/backend and nowhere else, so a future change that reaches for
# net/http from internal/api, internal/service or cmd/typryx to call out is
# caught here rather than found later as an ungoverned egress path.
#
# examples/ is exempted: a standalone program under examples/ (e.g.
# examples/calibration) is a CLIENT of typryx's own HTTP API, run from
# outside the process this invariant governs, exactly the same shape as the
# curl commands README.md already shows. It is not a backend, it never runs
# as part of the served process, and it cannot become an ungoverned egress
# path inside typryx's own request handling, which is what this gate exists
# to catch. Excluding it here does not weaken the gate over internal/ or
# cmd/typryx, both of which scripts/gates-have-teeth.sh still proves fail on
# a planted http.Client{}.
files=$(find . -name '*.go' -not -name '*_test.go' -not -path './internal/backend/*' -not -path './examples/*' -not -path './.git/*')
[ -n "$files" ] || { echo "FAIL: no .go files found; this gate measured nothing" >&2; exit 1; }

pattern='http\.Client\{|&http\.Client|http\.Transport\{|http\.DefaultClient|http\.Get\(|http\.Post\(|net\.Dial\b|net\.Dialer'

found=0
while IFS= read -r f; do
  [ -z "$f" ] && continue
  matches=$(grep -nE "$pattern" "$f" || true)
  if [ -n "$matches" ]; then
    while IFS= read -r line; do
      echo "FAIL: $f:$line names an outbound client outside internal/backend" >&2
    done <<< "$matches"
    found=1
  fi
done <<< "$files"

[ "$found" = "0" ] || exit 1
echo "one way out: no outbound HTTP client construction found outside internal/backend"
