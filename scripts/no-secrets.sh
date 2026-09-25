#!/usr/bin/env bash
# No real secret ever reaches this repository, tracked or in history. Test
# fixtures use obviously fake short keys (k1, test-key, agent://demo.example/
# tester) precisely so this gate never needs an allowlist for them; a real
# finding here is always something to remove and rotate, never something to
# annotate past.
set -euo pipefail
cd "$(dirname "$0")/.."

tracked_count=$(git ls-files | wc -l | tr -d ' ')
[ "$tracked_count" -gt 0 ] || { echo "FAIL: no tracked files; this gate measured nothing" >&2; exit 1; }

# Each pattern is a real-secret SHAPE, not a word: an Anthropic key prefix, a
# generic 20+ char secret-looking token, a GitHub PAT (classic or
# fine-grained), an AWS access key id, a PEM private key header, and a Slack
# token. A bearer token of 24+ chars is checked separately, outside test
# files only, since fixtures use short fakes like "Bearer test-key".
pattern='sk-ant-[A-Za-z0-9_-]+|sk-[A-Za-z0-9_-]{20,}|ghp_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|AKIA[0-9A-Z]{16}|-----BEGIN [A-Z ]*PRIVATE KEY-----|xox[baprs]-[A-Za-z0-9-]+'

found=0

while IFS= read -r f; do
  [ -z "$f" ] && continue
  [ -f "$f" ] || continue
  if grep -InE "$pattern" -- "$f" > /tmp/no-secrets-hit.$$ 2>/dev/null; then
    while IFS= read -r line; do
      echo "FAIL: $f:$line matches a real-secret shape" >&2
    done < /tmp/no-secrets-hit.$$
    found=1
  fi
  rm -f /tmp/no-secrets-hit.$$

  case "$f" in
    *_test.go) : ;;
    *)
      if grep -nE 'Bearer [A-Za-z0-9_.-]{24,}' -- "$f" > /tmp/no-secrets-hit.$$ 2>/dev/null; then
        while IFS= read -r line; do
          echo "FAIL: $f:$line carries a bearer token 24 chars or longer" >&2
        done < /tmp/no-secrets-hit.$$
        found=1
      fi
      rm -f /tmp/no-secrets-hit.$$
      ;;
  esac
done < <(git ls-files)

# The full history too: a secret committed and later removed from HEAD still
# leaked, and git never forgets a blob on its own.
if git log -p --all 2>/dev/null | grep -InE "$pattern" > /tmp/no-secrets-hist.$$; then
  echo "FAIL: git history contains a real-secret shape (see /tmp/no-secrets-hist.$$ for this run, not kept)" >&2
  found=1
fi
rm -f /tmp/no-secrets-hist.$$

[ "$found" = "0" ] || exit 1
echo "no-secrets: $tracked_count tracked files and the full history clean of real-secret shapes"
