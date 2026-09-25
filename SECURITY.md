# Security Policy

typryx is an optional add-on to the TAIPANBOX stack: it answers typed
questions (choice, score, yes/no) with a probability, governs what state
leaves the process, caps spend, and records every answer.

## Reporting a vulnerability

Please report security issues privately, not in public issues or pull
requests: open a GitHub private security advisory at
<https://github.com/TAIPANBOX/typryx/security/advisories/new>. Include the
affected version or commit, a description and a minimal reproduction. We aim
to acknowledge within a few days and to fix high-severity issues before any
public disclosure, with coordinated disclosure within 90 days of the report.
There is no bug-bounty programme; reporters are credited in the advisory
unless they prefer otherwise.

## Supported versions

Before this repository's 1.0, only `main` is supported: fixes land on `main`
and are not backported.

## Verifying a build

Every change passes the repository's gates before merge: `go build ./...`,
`go vet ./...`, `gofmt -l .`, `staticcheck ./...`, `go test ./... -race`,
`./scripts/features-are-bound.sh`, `./scripts/readme-numbers.sh`,
`./scripts/one-way-out.sh`, `./scripts/no-secrets.sh`, `./scripts/templates-load.sh`,
`./scripts/gates-have-teeth.sh`, `govulncheck ./...` and `gosec -quiet ./...`.

## What this service does not protect against

This phase has one backend (`stub`), which is deterministic, free, and
answers nobody's real question. There is no live model call, no vendor
credential, and nothing metered in this phase, so there is no key or token
this service holds today. When a real backend (`jev`, `openai-logprobs`)
lands, its credential is read from a file path named by an environment
variable, never from the environment value itself and never logged; see
README.md's configuration table for the exact variable once it exists.
