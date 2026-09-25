#!/usr/bin/env bash
# The starter catalog (examples/templates, also what the Dockerfile ships to
# /etc/typryx/templates) loads clean and ships the minimum: no template may
# name a field that identifies a person or a customer. A template that fails
# to load, a missing directory, and a directory holding zero templates are
# all failures here, never a silent OK ("measured nothing" is a failure, not
# a pass).
set -euo pipefail
cd "$(dirname "$0")/.."

dir="examples/templates"
[ -d "$dir" ] || { echo "FAIL: $dir is gone; this gate measures nothing" >&2; exit 1; }

count=$(find "$dir" -maxdepth 1 -name '*.json' | wc -l | tr -d ' ')
[ "$count" -gt 0 ] || { echo "FAIL: $dir holds zero templates; this gate measures nothing" >&2; exit 1; }

bin=$(mktemp -d)/typryx
trap 'rm -rf "$(dirname "$bin")"' EXIT
go build -o "$bin" ./cmd/typryx

if ! out=$("$bin" templates check "$dir" 2>&1); then
  echo "FAIL: templates check reported at least one load error:" >&2
  echo "$out" >&2
  exit 1
fi

# The minimum a judge needs, never a field that identifies a person or a
# customer. This list is the catalog's own floor, not the template schema's:
# internal/template.Validate has no opinion on field NAMES, only on shape.
denylist="email user_email name phone iban card api_key token password ssn address"

found=0
for f in "$dir"/*.json; do
  hit=$(TEMPLATES_LOAD_DENYLIST="$denylist" python3 - "$f" <<'PY'
import json, os, sys

path = sys.argv[1]
denylist = set(os.environ["TEMPLATES_LOAD_DENYLIST"].split())
with open(path, encoding="utf-8") as fh:
    t = json.load(fh)
fields = t.get("fields", [])
if not isinstance(fields, list):
    fields = []
bad = [f for f in fields if f in denylist]
if bad:
    print(",".join(bad))
PY
)
  if [ -n "$hit" ]; then
    echo "FAIL: $f names an identifying field in fields: $hit" >&2
    found=1
  fi
done
[ "$found" = "0" ] || exit 1

echo "templates-load: $count starter template(s) load clean, none names an identifying field"
