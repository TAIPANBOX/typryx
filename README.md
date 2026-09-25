# typryx

![tests](https://img.shields.io/badge/tests-191-brightgreen)

typryx is an OPTIONAL add-on to the TAIPANBOX agent-governance stack. A
customer who does not add it runs exactly the stack they ran before. One who
does gets a small service that answers a typed question, a choice, a score,
or a yes/no (called `noul` here), with a probability, reachable over HTTP and
over MCP.

It governs three things: what state leaves the process (only the fields a
template names, never the whole state, unless an operator explicitly turns
freeform questions on), what it costs (a finite hourly call cap by default),
and what it claims (a backend that fails, times out, or returns a broken
probability distribution gets `unanswered`, never a guess).

The model behind it is a backend that can be swapped. In this phase there is
exactly one: `stub`, which is deterministic, spends nothing, and answers
nobody's real question. Its every answer says `backend: stub` so nothing
downstream mistakes it for a judgement.

## Quickstart

Build and check the example templates:

```sh
go build ./cmd/typryx
./typryx templates check examples/templates
```

Start it:

```sh
TYPRYX_BACKEND=stub \
TYPRYX_TEMPLATES=examples/templates \
TYPRYX_KEYS='k1=agent://demo.example/tester' \
TYPRYX_EVENTS=/tmp/typryx-events.ndjson \
TYPRYX_LEDGER_DIR=/tmp/typryx-ledger \
./typryx
```

Ask a question over HTTP:

```sh
curl -s -X POST http://127.0.0.1:4320/v1/ask \
  -H 'X-Typryx-Key: k1' -H 'Content-Type: application/json' \
  -d '{"template":"request.complexity","state":{"prompt":"summarize this doc"}}'
```

The answer names one class, a probability for every class, the backend and
model version:

```json
{"answer_id":"...","template":"request.complexity","template_version":"...",
 "type":"choice","answer":"default","probabilities":{"cheap":0.1,"default":0.6,"hard":0.2,"reasoning":0.1},
 "backend":"stub","model":"stub-0","latency_ms":0,"held_back_fields":0}
```

Record a later truth against that answer:

```sh
curl -s -X POST http://127.0.0.1:4320/v1/outcome \
  -H 'X-Typryx-Key: k1' -H 'Content-Type: application/json' \
  -d '{"answer_id":"<the id from above>","truth":"default","source":"human"}'
```

## MCP

`POST /mcp` is JSON-RPC 2.0: `initialize`, `tools/list`, `tools/call`. Tools:
`ask` (`template`, `state`, optional `run_id`), `list_questions` (no
arguments). `ask_freeform` is listed only when `TYPRYX_ALLOW_FREEFORM=1`.

To sit behind tokenfuse's MCP broker as a named upstream:

```
TOKENFUSE_MCP_UPSTREAMS=typryx=http://127.0.0.1:4320/mcp
```

and a caller sends `X-Fuse-Mcp-Upstream: typryx`. **This path is untested
until phase B2**: nothing in this phase runs a real tokenfuse broker against
typryx.

## Configuration

| var | required | default | notes |
|---|---|---|---|
| `TYPRYX_ADDR` | no | `127.0.0.1:4320` | |
| `TYPRYX_KEYS` | no | none | `key=agent://domain/name,key2,...` |
| `TYPRYX_ALLOW_OPEN_BIND` | no | unset | only `1`/`true` count |
| `TYPRYX_BACKEND` | **yes** | none | only `stub` is accepted in this phase; anything else refuses to start naming it, at exit 2 |
| `TYPRYX_TEMPLATES` | **yes** | none | directory of `*.json` templates |
| `TYPRYX_EVENTS` | no | none = journal off | NDJSON agent-event path |
| `TYPRYX_LEDGER_DIR` | no | none | holds `answers.ndjson`, `outcomes.ndjson`; unset means `POST /v1/outcome` refuses every call with `no_ledger`, and answers are not ledgered |
| `TYPRYX_MAX_CALLS_PER_HOUR` | no | `1000` | `0` disables it, with a warning logged at boot; a malformed value refuses to start naming the variable |
| `TYPRYX_ALLOW_FREEFORM` | no | unset | only `1`/`true` count |
| `TYPRYX_TIMEOUT_MS` | no | `2000` | backend deadline; a malformed value refuses to start naming the variable |

A missing required variable exits 2 and names the variable on stderr. A
non-loopback bind (`TYPRYX_ADDR` not on `127.0.0.1`/`localhost`/`::1`) with no
`TYPRYX_KEYS` configured refuses to start (exit 1) unless
`TYPRYX_ALLOW_OPEN_BIND=1`; required configuration is checked first, so that
refusal always fires with the rest of the configuration already known to be
sane (see CLAUDE.md for why that order was chosen over checking the bind
first).

**A future `jev` or other paid backend's API key is read from a file path
named by an environment variable, never from the environment value itself and
never logged.** There is no such variable in this phase, because there is no
such backend yet: `TYPRYX_BACKEND` set to anything but `stub` refuses to
start.

## Template format

One JSON file per template, in the directory named by `TYPRYX_TEMPLATES`:

```json
{
  "id": "eval.outcome_met",
  "type": "noul",
  "instructions": "The agent's final answer achieves the task described in `task`.",
  "criteria": {"true": "...", "false": "..."},
  "fields": ["task", "final_answer"],
  "max_state_bytes": 16384
}
```

- `id`: `^[a-z0-9][a-z0-9._-]{0,63}$`, unique in the directory.
- `type`: `choice` (criteria: an object of 2 to 255 option names to
  descriptions), `score` (criteria: an array of 2 to 10 ordered level
  descriptions; array position is the score), or `noul` (yes/no; criteria
  optional).
- `fields`: the allowlist of state keys that may reach the backend. Non-empty,
  unique. Nothing else in a caller's state ever leaves the process.
- `max_state_bytes`: defaults to 16384, capped at 1048576.
- The template's version is the lowercase hex SHA-256 of its own normalized
  JSON. An answer names the version it was given under, and a later outcome
  is scored against that version, never against whatever the template file
  says today.

`examples/templates/` holds three starter templates: `eval.outcome_met`
(noul), `eval.answer_quality` (score, four levels), and
`request.complexity` (choice: cheap, default, hard, reasoning, matching
tokenfuse's router task classes).

## Events

When `TYPRYX_EVENTS` is set, every answer and refusal is written as one
agent-event line (schema `taipanbox.dev/agent-event/v1.0`, source `typryx`):
`typed_answer` (info), `typed_unanswered` (medium), `typed_refused` (high).
`calibration_drift` (high) is declared but not emitted in this phase. The
event carries a SHA-384 of the fields that actually left the box, never the
state itself. An event with no agent identity behind the presented credential
is skipped and counted, visible at `GET /healthz` as
`{"status":"ok","journal":{"skipped_no_agent":N,"write_failed":M}}`.

## Testing

172 tests. Tier T2. `go test ./... -race` covers every package;
`internal/manifest` builds and starts the real binary to prove
`components.json` against what it actually does.

Coverage (`go test ./... -coverprofile=cover.out`, per package):
`internal/backend/backendtest` and `internal/record` 100%,
`internal/api` 98.4%, `internal/backend` 98.1%, `internal/door` 97.6%,
`internal/service` 96.6%, `internal/mcp` 94.1%, `internal/template` 94.0%,
`internal/ledger` 90.7%, `cmd/typryx` 74.0% (its `main`/`run` are the
signal-driven serve loop, proved by starting the real binary in
`internal/manifest` and by process-level tests in `cmd/typryx/main_test.go`
rather than by in-process instrumentation; see CLAUDE.md).

## NOT PROVEN

- **No real model backend exists.** `stub` is deterministic and free; its
  answers mean nothing about any real question.
- **Calibration is not built.** `outcomes.ndjson` is written; nothing reads it
  yet to compute a Brier score or check whether a probability can be
  trusted.
- **Not run behind a real tokenfuse MCP broker.** The upstream configuration
  above is documented, not exercised.
- **No launcher installs this.** stack-single, stack-up and stack-k8s carry
  no typryx entry yet.
- **Not registered in the agent-passport SPEC.** The event types and schema
  version used here are not yet a numbered section of that document.
- **The manifest test proves the open-bind matrix and required-variable
  refusals on this machine, on this commit; it is not a claim about any other
  environment.**
