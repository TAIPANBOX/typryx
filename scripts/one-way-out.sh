#!/usr/bin/env bash
# Only internal/backend may reach the network. Every other package answers
# HTTP and MCP requests; none of them originates one. This is the structural
# half of "a backend is the only way out": the day a real backend (jev,
# openai-logprobs) is added, its HTTP client construction belongs in
# internal/backend and nowhere else, so a future change that reaches for
# net/http from internal/api, internal/service or cmd/typryx to call out is
# caught here rather than found later as an ungoverned egress path.
set -euo pipefail
cd "$(dirname "$0")/.."

files=$(find . -name '*.go' -not -name '*_test.go' -not -path './internal/backend/*' -not -path './.git/*')
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
