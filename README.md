<div align="center">

![typryx: typed answers with a probability, for an agent stack that has to show its work](docs/banner.svg)

# typryx

Typed answers with a probability, for an agent stack that has to show its work: a
question in, a choice, a score, or a yes/no (called `noul` here) out, reachable over
HTTP and MCP, with what leaves the box, what it costs, and what it claims all governed
and recorded.

[![ci](https://github.com/TAIPANBOX/typryx/actions/workflows/ci.yml/badge.svg)](https://github.com/TAIPANBOX/typryx/actions/workflows/ci.yml)
![Go 1.27](https://img.shields.io/badge/Go-1.27-4493f8)
![one direct dependency](https://img.shields.io/badge/direct%20dependency-one-2dd4bf)
![license Apache 2.0](https://img.shields.io/badge/license-Apache--2.0-9aa7b8)
![tests](https://img.shields.io/badge/tests-500-brightgreen)

</div>

![Your agent, optionally through the tokenfuse MCP broker, reaches typryx's five stages (door, template, egress filter, hourly cap, backend); the answer and its record come back](docs/hero.svg)

typryx is an OPTIONAL add-on to the TAIPANBOX agent-governance stack. A customer who
does not add it runs exactly the stack they ran before. One who does gets a small
service that answers a typed question with a probability, reachable over HTTP and MCP.

## What it is, in one question

Agents already ask each other, and their models, yes/no and which-one questions in
prose, and prose does not thread through a policy. "Is this task done?" answered as a
paragraph cannot be thresholded, cannot be capped, and cannot be scored later against
what actually happened. A typed answer with a probability can be all three: a policy
can compare it to a number, an operator can see what it cost, and a later truth can be
checked against the exact question that was asked.

## Why add it

Each of these is something built in this repository, not a claim about a future one:

- **A typed answer with a probability, not a sentence.** `choice`, `score`, or `noul`
  (yes/no), with a probability for every option and the highest one served as the
  answer, derived from the distribution itself and never taken from what the backend
  claims outright.
- **Only the fields a template names leave the process.** Measured: asking
  `eval.outcome_met` with a state carrying `user_email` and, separately, `customer_iban`
  in a state of five fields, neither ever reached the backend or the record; the answer
  reports how many fields were held back.
- **Every answer and refusal is on a tamper-evident record**, carrying a SHA-384 of what
  was actually sent, never the state itself.
- **A spend cap is on by default**: a finite hourly call limit, counted only against
  calls that got past the door, the template lookup, and the egress filter.
- **The model is a swappable backend, and the templates are the contract.** `stub` is
  free and deterministic, for tests and demos; `openai-logprobs` asks any
  OpenAI-compatible server (a local Ollama, vLLM, llama.cpp, or a cloud endpoint) for its
  own token probabilities. Jev (TypeSafe AI, under the name "typed-decision models") is a
  later, paid backend behind the same interface, not the point of the service.
- **A later truth is recorded against the exact question that was asked**, by the
  template's own content digest, never against whatever the template says today.

No claim here is about how fast, cheap, or accurate any vendor's model is; see
[What it will not do](#what-it-will-not-do).

## Install

With Go 1.27 (there is no tagged release yet, so this builds `main`):

```sh
go install github.com/TAIPANBOX/typryx/cmd/typryx@main
```

Or from a clone, which also gives you the example templates:

```sh
git clone https://github.com/TAIPANBOX/typryx && cd typryx
go build ./cmd/typryx
./typryx templates check examples/templates
```

Or with Docker:

```sh
docker build -t typryx:dev .
docker run --rm -p 4320:4320 \
  -e TYPRYX_BACKEND=stub -e TYPRYX_KEYS='k1=agent://demo.example/tester' \
  typryx:dev
```

Measured 2026-09-25 on a development Mac (Apple Silicon, Docker Desktop): the image builds to **15.7 MB**; with
`TYPRYX_BACKEND=stub` and `TYPRYX_KEYS` set it answers `/healthz` and a real
`POST /v1/ask`; run with `TYPRYX_BACKEND=stub` alone, no keys, against the image's own
wide default bind (`TYPRYX_ADDR=0.0.0.0:4320`), it refuses to start with exit 1, naming
`TYPRYX_KEYS` and `TYPRYX_ALLOW_OPEN_BIND`: the open-bind refusal matrix working exactly
as it does outside a container. There is no published image or tag.

## Connect it

![Four ways to connect: Claude Code, tokenfuse's MCP broker, and plain curl are each measured; stack launchers are planned](docs/connect.svg)

`typryx connect <target>` prints ready-to-paste configuration for one client and
nothing else, and never prints a real key: it prints `${TYPRYX_KEY}` (or the name given
to `--key-env`) and says where to set it.

**Claude Code, or any MCP client speaking Streamable HTTP:**

```sh
typryx connect claude-code
```

```
claude mcp add --transport http typryx http://127.0.0.1:4320/mcp --header "X-Typryx-Key: ${TYPRYX_KEY}"

{"mcpServers":{"typryx":{"type":"http","url":"http://127.0.0.1:4320/mcp","headers":{"X-Typryx-Key":"${TYPRYX_KEY}"}}}}
```

Measured 2026-09-25 with Claude Code 2.1.270 (headless, this generated `.mcp.json`):
`tools/list` returned typryx's tools, `mcp__typryx__ask` on `eval.outcome_met` answered
`noul` 0.131 (`probabilities: {false: 0.869, true: 0.131}`, `backend: stub`,
`held_back_fields: 1`), and typryx's journal wrote one `typed_answer` under
`agent://demo.example/claude-code`; an extra `api_token` field in the state never
reached the record. Claude Code's own first request is `server/discover` (a newer
protocol), which typryx does not implement; Claude Code falls back to `initialize` on
the resulting error and proceeds normally, observed on this run.

That confirmed run followed a real defect a client, not typryx's own test suite, found
first: before commit `badc790`, `tools/list` answered a tool with no required arguments
with `"required":null` (a nil Go slice marshals as JSON `null`), and Claude Code silently
dropped typryx's entire tool list rather than reporting an error. typryx's own MCP tests
were all green at the time. Fixed and covered by
`TestEveryToolSchemaIsValidJSONSchemaForStrictClients`.

**tokenfuse's MCP broker, as a named upstream:**

```sh
typryx connect tokenfuse
```

```
TOKENFUSE_MCP_UPSTREAMS=typryx=http://127.0.0.1:4320/mcp
```
a caller picks it with `X-Fuse-Mcp-Upstream: typryx`.

Measured 2026-09-25 (typryx on loopback with no keys, tokenfuse's `mcp-broker` built
from `TAIPANBOX/tokenfuse` main `9bbbfc1`, `TOKENFUSE_MCP_KEYS=brokerkey:support-bot`): a
client sending `x-fuse-key: brokerkey` and `X-Fuse-Mcp-Upstream: typryx` got
`tools/list` (`ask`, `list_questions`) and a real `ask` answer, `customer_iban` in the
state held back and never reaching the backend; an unknown upstream name was refused
(`-32005 unknown mcp upstream`); tokenfuse recorded one `tool_call` event under
`agent://demo.example/support-bot`.

**A real limitation, stated rather than hidden**: the broker forwards to a named
upstream with only a content-type header, no credential and no agent identity
(`tokenfuse crates/gateway/src/mcpbroker.rs`). typryx cannot name the agent behind the
broker, so its own journal skipped that call and counted it
(`skipped_no_agent` at `GET /healthz`); the agent is on tokenfuse's record, not
typryx's. Run typryx on loopback or a private network with no `TYPRYX_KEYS` while it
sits behind this broker. Closing this needs tokenfuse to forward an identity to a named
upstream, which is not built.

**Plain HTTP:**

```sh
typryx connect curl
```

prints one `/v1/ask` and one `/v1/outcome` example, ready to run against a live
deployment.

**stack-single, stack-up, and stack-k8s** carry no typryx entry yet; see
[Status](#status).

## How an answer is made

![Measured probabilities for request.complexity from the stub backend, the highest bar served as the answer; a backend's own claim is ignored; no usable probabilities means unanswered with a reason](docs/answer.svg)

```sh
curl -s -X POST http://127.0.0.1:4320/v1/ask \
  -H 'X-Typryx-Key: k1' -H 'Content-Type: application/json' \
  -d '{"template":"request.complexity","state":{"prompt":"summarize this doc"}}'
```

```json
{"answer_id":"...","template":"request.complexity","template_version":"...",
 "type":"choice","answer":"default","probabilities":{"cheap":0.333,"default":0.346,"hard":0.185,"reasoning":0.136},
 "backend":"stub","model":"stub-0","latency_ms":0,"held_back_fields":0}
```

The answer is computed from `probabilities` itself (the argmax over the template's
option keys for `choice`, the argmax index for `score`, `probabilities["true"]` for
`noul`), never read from whatever the backend's own answer field claims: a backend
returning a valid distribution alongside a contradicting claimed answer has the
distribution win, every time. `stub` is deterministic and free, and every answer it
gives says `backend: stub` so nothing downstream mistakes it for a judgement.

A backend error, a timeout, a canceled request, missing probabilities, or a probability
set that does not validate (wrong keys, out of range, not summing to one) all produce
`unanswered` with a `reason` (`backend_error`, `timeout`, `canceled`,
`no_probabilities`, `bad_probabilities`, and the openai-logprobs backend's own
`no_logprobs`, `label_mass_too_low`, `too_many_options`, `bad_logprobs`, see below) and the wire shape
omits `answer` and `probabilities` entirely. No renormalizing, no fallback guess.

## Local model backend

`TYPRYX_BACKEND=openai-logprobs` asks any OpenAI-compatible chat-completions server
(a local Ollama, vLLM, llama.cpp, OpenAI itself, or tokenfuse's gateway in front of a
provider): nothing leaves the machine when the endpoint is local. Every option is
relabelled to a single token (`A`, `B`, `C`, ...) and the probability of each comes from
the server's own `top_logprobs` for that letter, never from anything the model writes in
prose: `choice` gets one label per sorted option, `score` gets one label per level
(array position is the score), `noul` gets exactly two (`A` for true, `B` for false). A
choice with more than 26 options is refused by this backend alone (`too_many_options`);
another backend may still take it. The request asks for one token at temperature 0 with
`logprobs: true` and `top_logprobs: 20`; a response missing `logprobs`, `content`, or
`top_logprobs` anywhere along that path is `unanswered` with `no_logprobs`; if the labels
together capture less than `TYPRYX_OPENAI_MIN_LABEL_MASS` (default 0.9) of the response's
probability mass, the result is `unanswered` with `label_mass_too_low` rather than a
distribution stretched to sum to one anyway. The state reaches the model only as
canonical JSON inside a fenced block, after the system message says it is data to judge,
never instructions; only `internal/backend` builds the outbound HTTP client, follows no
redirect, and caps the response at 1 MiB.

Measured 2026-09-25 on this Mac, against Ollama 0.34.2 (`qwen2.5:3b` and `qwen2.5:7b`,
both already pulled, `http://127.0.0.1:11434/v1`), `TYPRYX_TIMEOUT_MS=15000`:

| template | state | answer | top probability | latency |
|---|---|---|---|---|
| `request.complexity` | "what is 2+2" | `cheap` | 0.99999990 | 314 ms |
| `request.complexity` | "prove Fermat's last theorem... with full rigor" | `reasoning` | 0.5452 (`hard` 0.4548, a close call) | 79 ms |
| `eval.outcome_met` | task "7 times 8", answer "56" (correct) | `true` | 0.99968 | 210 ms |
| `eval.outcome_met` | task "7 times 8", answer "42" (**wrong**) | `true` | 0.94602 | 97 ms |
| `eval.answer_quality` | a full, correct water-cycle summary | score `3` (fully addresses) | 0.8584 | 316 ms |
| `eval.answer_quality` | "water go up then down lol" | score `0` (does not address) | 0.9848 | 96 ms |
| `request.complexity` (`qwen2.5:7b`) | "what is 2+2" | `cheap` | 0.99999999 | 5067 ms (first call to this model in this process) |

Reported honestly, not tuned to look good: the `eval.outcome_met` row for the wrong
arithmetic answer is a **wrong and overconfident judgement** (`true` at 0.946 for an
answer that does not achieve the task), taken verbatim from this run, prompts unchanged
from what this section already describes. The `eval.answer_quality` and
`request.complexity` rows above look sound on this small, informal sample, but six asks
prove nothing about calibration; see NOT PROVEN.

## What leaves the box

![Of five fields in the state, only the one the template names travels to the backend; the other four are held back and counted](docs/egress.svg)

A template's `fields` is the only allowlist: `internal/template.Filter` runs before any
backend sees the state, a key not named is dropped and counted as `held_back_fields`,
and the resulting `template.Egress` is a type whose fields are unexported and
constructible only inside package `template`, so a backend cannot be handed a map or raw
state even by an implementation mistake. Free-form questions (no template) are refused
unless an operator explicitly sets `TYPRYX_ALLOW_FREEFORM=1`.

## The record

![An answer_id flows to the journal and the ledger; a later truth is scored against the template version an answer was asked under, a second truth is refused with 409, a torn last line is truncated before the next write, and calibration is planned](docs/record.svg)

When `TYPRYX_EVENTS` is set, every answer and refusal writes one agent-event line
(schema `taipanbox.dev/agent-event/v1.0`, source `typryx`): `typed_answer`,
`typed_unanswered`, or `typed_refused`, carrying a SHA-384 of the fields that actually
left the box, never the state. An event with no agent identity behind the credential is
skipped and counted, never given a fabricated `agent_id`.

When `TYPRYX_LEDGER_DIR` is set, every answer is appended to `answers.ndjson`, fsynced
per write; a later truth posted to `/v1/outcome` is scored against the template
**version the answer was asked under** (read from the ledger's own record, never the
live template registry) and appended to `outcomes.ndjson`. A second outcome for the same
`answer_id` is refused with `409 outcome_exists`, including across a restart. A torn
last line left by a crash mid-write is truncated on disk before the file is reopened, so
the next answer is written cleanly rather than merged into the wreckage.

## Templates

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
- `type`: `choice` (criteria: 2 to 255 named options), `score` (criteria: 2 to 10
  ordered level descriptions, array position is the score), or `noul` (yes/no; criteria
  optional).
- `fields`: the egress allowlist, non-empty and unique. Nothing else ever leaves.
- `max_state_bytes`: defaults to 16384, capped at 1048576.
- The version is the lowercase hex SHA-256 of the template's own canonical JSON.

`examples/templates/` holds three: `eval.outcome_met` (noul), `eval.answer_quality`
(score, four levels), and `request.complexity` (choice: cheap, default, hard,
reasoning, matching tokenfuse's router task classes).

```sh
typryx templates check examples/templates
```

## Configuration

| var | required | default | notes |
|---|---|---|---|
| `TYPRYX_ADDR` | no | `127.0.0.1:4320` | |
| `TYPRYX_KEYS` | no | none | `key=agent://domain/name,key2,...` |
| `TYPRYX_ALLOW_OPEN_BIND` | no | unset | only `1`/`true` count |
| `TYPRYX_BACKEND` | **yes** | none | `stub` or `openai-logprobs` in this phase; anything else (including `jev`) refuses to start naming it, at exit 2 |
| `TYPRYX_TEMPLATES` | **yes** | none | directory of `*.json` templates |
| `TYPRYX_EVENTS` | no | none = journal off | NDJSON agent-event path |
| `TYPRYX_LEDGER_DIR` | no | none | holds `answers.ndjson`, `outcomes.ndjson`; unset means `POST /v1/outcome` refuses every call with `no_ledger`, and answers are not ledgered |
| `TYPRYX_MAX_CALLS_PER_HOUR` | no | `1000` | `0` disables it, with a warning logged at boot |
| `TYPRYX_ALLOW_FREEFORM` | no | unset | only `1`/`true` count |
| `TYPRYX_TIMEOUT_MS` | no | `2000` | backend deadline |
| `TYPRYX_OPENAI_URL` | when `TYPRYX_BACKEND=openai-logprobs` | none | an OpenAI-compatible base URL ending in `/v1` (e.g. `http://127.0.0.1:11434/v1` for a local Ollama); must be absolute http/https with a host and no userinfo, query, or fragment |
| `TYPRYX_OPENAI_MODEL` | when `TYPRYX_BACKEND=openai-logprobs` | none | the model name sent with every request |
| `TYPRYX_OPENAI_KEY_FILE` | no | none = no `Authorization` header | path to a file holding a bearer key, trimmed; never read from the environment value itself, never logged, never echoed into an error |
| `TYPRYX_OPENAI_MIN_LABEL_MASS` | no | `0.9` | fraction of the response's probability mass that must land on a lettered option; below it, the ask is unanswered with `label_mass_too_low` |

A missing required variable, or a required-when-chosen variable missing for the backend
actually named, exits 2 and names the variable. A non-loopback bind with no
`TYPRYX_KEYS` refuses to start (exit 1) unless `TYPRYX_ALLOW_OPEN_BIND=1`; required
configuration is checked first, so that refusal always fires with the rest of the
configuration already known sane.

A future `jev` (or any other paid backend)'s API key is read from a file path named by
an environment variable, the same shape `TYPRYX_OPENAI_KEY_FILE` already uses, never
from the environment value itself and never logged. `TYPRYX_BACKEND` set to `jev`
refuses to start; only `stub` and `openai-logprobs` are accepted in this phase.

## What it will not do

- **No `deny` from a probability, anywhere**, in this repository or a consumer. A
  probability is a signal a policy can threshold, never an enforcement decision typryx
  makes on its own.
- **No field a template does not name** ever reaches a backend or the record.
- **No default backend, and nothing paid without a named choice.** `TYPRYX_BACKEND` is
  required; there is no fallback.
- **No answer without a probability.** A guess dressed as a number is worse than a
  refusal that says why.
- **No key printed by anything in this repository**, including `typryx connect`.
- **No claim about a vendor model's speed, cost, or accuracy.** Jev (TypeSafe AI) is
  named only as one of the typed-decision models a later backend can target; its
  published price and availability are vendor figures, quoted as vendor figures or not
  at all.

## What is checked, and how

```sh
test -z "$(gofmt -l .)"
go vet ./...
staticcheck ./...
go test ./... -race
go build ./...
./scripts/features-are-bound.sh
./scripts/readme-numbers.sh
./scripts/one-way-out.sh
./scripts/no-secrets.sh
./scripts/gates-have-teeth.sh
```

232 tests. `go test ./... -race` covers every package; `internal/manifest` builds and
starts the real binary to prove `components.json` against what it actually does; CI's
`image` job builds the Dockerfile on every push and pull request, pushing nowhere.

Coverage (`go test ./... -coverprofile=cover.out`, measured 2026-09-25, after the
openai-logprobs backend landed): **90.6%** overall. `internal/backend/backendtest` and
`internal/record` 100%, `internal/api` 98.4%, `internal/backend` 92.5% (up from 98.1%:
the denominator grew with `openai.go`'s hostile-input handling, most of it exercised by
the 220-seed sweep in `internal/backend/openai_test.go`), `internal/door` 97.8%,
`internal/service` 93.7%, `internal/mcp` 93.5%, `internal/template` 94.0%,
`internal/ledger` 90.4%, `cmd/typryx` 78.0% (its `main`/`run` are the signal-driven
serve loop, proved by starting the real binary in `internal/manifest` and by
process-level tests in `cmd/typryx/main_test.go` rather than by in-process
instrumentation).

`scripts/gates-have-teeth.sh` plants 13 faults, one per gate behaviour, and requires
each gate to fail on its own fault and pass on what it must not catch. Eleven defects
were found in a whole-file review on 2026-09-25 and fixed red-first (see CLAUDE.md for
the mutants each fix's test catches); a twelfth, the `required: null` schema defect
above, was found afterward by a real MCP client rather than by any test in this
repository, and is now covered.

## NOT PROVEN

- **`stub` answers mean nothing about any real question.** It is deterministic and free,
  for tests and demos only.
- **LLM token probabilities are often overconfident and uncalibrated.** The measured run
  above shows exactly this: a wrong arithmetic answer still scored `true` at 0.946 for
  `eval.outcome_met`. Whether, and how much, a given template x backend x model can be
  trusted is what phase E's calibration is for; nothing in this phase measures it.
- **Position and label bias are not measured.** Whether relabelling options A, B, C in a
  different order, or using different letters, shifts the answer is unmeasured here.
- **A hostile state can still steer the model's answer.** typryx keeps the state
  structurally delimited (a fenced, JSON-escaped block, with a system message saying it
  is data, not instructions), which is a containment measure, not a claim of injection
  resistance: nothing here proves a sufficiently adversarial state cannot change what the
  model says about it.
- **The openai-logprobs backend is unpriced.** `cost_usd` is always `0`, even against a
  paid OpenAI-compatible endpoint, because this phase has no price configuration; a paid
  endpoint's actual cost is not tracked.
- **Calibration is not built.** `outcomes.ndjson` is written; nothing reads it yet to
  compute a Brier score or check whether a probability can be trusted.
- **typryx records no agent behind the tokenfuse broker.** The broker forwards no
  identity to a named upstream; see [Connect it](#connect-it).
- **No launcher installs this.** stack-single, stack-up, and stack-k8s carry no typryx
  entry yet.
- **Not registered in the agent-passport SPEC.** The event types and schema version used
  here are not yet a numbered section of that document.
- **No published image or tag.** The Dockerfile is built and run locally and by CI on
  every push; nothing is pushed to a registry.
- **The manifest test and the Docker and tokenfuse runs prove behavior on one development Mac,
  on these commits; they are not a claim about any other environment.**

## Status

- [x] **Phase A**: the core, HTTP-only: templates, the stub backend, the journal, the
      ledger, the door, the spend cap, `components.json` proved against the real binary.
- [x] **Phase B1**: the MCP surface (`initialize`, `tools/list`, `tools/call`).
- [x] **Phase B2**: run behind a real tokenfuse MCP broker, and from Claude Code as a
      real MCP client (see [Connect it](#connect-it)).
- [x] **Phase C**: a local `openai-logprobs` backend against an OpenAI-compatible
      endpoint, measured against Ollama 0.34.2 (`qwen2.5:3b`, `qwen2.5:7b`); see
      [Local model backend](#local-model-backend).
- [ ] **Phase D**: the `jev` backend, needs a decision on signing up and spending before
      any live call.
- [ ] **Phase E onward**: calibration, the agent-passport registration, launcher wiring
      (stack-single, stack-up, stack-k8s), and consumers (verdryx, wardryx, tokenfuse's
      router, costcrew, engram).

Next: calibration (phase E), then Jev (phase D, needs a spend decision first).
