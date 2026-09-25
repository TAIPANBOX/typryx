# CLAUDE.md, working instructions for typryx

Process and invariants only. **No status**: status goes stale and a stale
instruction file is worse than none. For where the code actually is, read the
tests and README.md's NOT PROVEN section.

## Read before you change anything

1. `README.md`, and specifically NOT PROVEN.
2. `features/typed-answers.feature`. Every scenario there is bound to a named
   test, both directions, by `scripts/features-are-bound.sh`.
3. `components.json`. The `checked` bucket is proved by `internal/manifest`
   building and starting the real binary; the `declared` bucket is a
   statement nobody verifies and carries its own `why`.

## What this is

An OPTIONAL add-on to the TAIPANBOX stack. It answers a typed question
(choice, score, or noul, meaning yes/no) with a probability, over HTTP and
MCP, governs what leaves the box and what it costs, records every answer, and
keeps a ledger so a later truth can be scored against exactly the question
that was asked. Absent, the rest of the stack behaves exactly as it does
today: nothing in this repository is consumed by anything else unless an
operator wires it in.

It is defensive. It exists so an operator can govern their own agents asking
typed questions. Never describe it, in code, docs or commit messages, as
tooling for acting against anyone else.

## The working loop

1. Branch off `main`, one logical increment per branch.
2. Run every gate below. All must pass locally before the push.
3. Commit with Conventional Commits. End the message with the standard
   co-author trailer naming the model that actually did the work.
4. Push the branch, open a PR with `gh`.
5. Wait for all CI checks to go green. Fix forward, do not force-push over red.
6. **Ask the user before merging.** Do not self-merge.

## Gates

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
./scripts/gates-have-teeth.sh   # needs a clean tree, run it after committing
```

## Hard invariants

Each one carries how it is held: `(test: ...)` or `(gate: ...)`. An invariant
with no check, written as though it had one, is worse than an absent
invariant. Numbering follows the plan this phase implements
(`~/Development/typryx-plan-2026-09-25.md`); invariant 9 is listed under NOT
BUILT YET below rather than skipped, so the numbering stays stable across
phases.

1. **Never invents an answer.** A backend error, a timeout, missing or
   malformed probabilities, a cap hit: the result is `unanswered` with a
   reason, and the wire shape omits `answer` and `probabilities` entirely
   rather than sending a null or a zero. No renormalizing, no fallback guess.
   Reasons: `backend_error`, `timeout`, `canceled`, `no_probabilities`,
   `bad_probabilities`, `over_hourly_cap`, and, from the openai-logprobs
   backend's own `backend.UnansweredError` (phase C), `no_logprobs`,
   `label_mass_too_low`, `too_many_options`.
   *(test: `TestAFailingBackendGivesUnansweredAndNeverAGuess`,
   `TestBadProbabilitiesAreUnansweredNotGuessed`, both in
   `internal/service`; `TestABackendsNamedReasonReachesTheCallerAndTheRecord`
   and `TestATimeoutStillWinsOverABackendsNamedReason`, also
   `internal/service`, for the backend-named reasons and the precedence a
   deadline or a caller cancel still holds over them)*

2. **Only a template's `fields` leave the box.** The egress filter
   (`template.Filter`) runs before any backend sees the state, and a key not
   named is dropped and counted as `held_back_fields`. This is held by the
   TYPE SYSTEM, not by a promise: `backend.Backend.Ask` takes a
   `template.Egress`, whose fields are unexported and which is constructible
   only inside package `template`. A backend cannot be handed a map or raw
   bytes even by an implementation mistake, because nothing outside
   `internal/template` can construct one except the freeform path
   (`NewFreeformEgress`), which exists only for the case an operator
   explicitly switched on. *(test:
   `TestOnlyTheFieldsATemplateNamesReachTheBackend` in `internal/service`;
   mutant: the filter loop replaced with "keep every field", caught by that
   test)*

3. **Identity comes from the credential**, `X-Typryx-Key` maps to an
   `agent://` identity through `internal/door`; a header a caller sent
   claiming an agent (`X-Fuse-Agent-Id`, `X-Agent-Id`, `Agent-Passport`) is
   never read by `internal/api` or anywhere downstream. *(test:
   `TestIdentityComesFromTheCredentialNeverFromAHeader` in `internal/api`;
   mutant: `withDoor` made to prefer `X-Fuse-Agent-Id` when present, caught)*

4. **Open-bind refusal matrix.** A non-loopback bind with no `TYPRYX_KEYS`
   configured refuses to start (exit 1) unless `TYPRYX_ALLOW_OPEN_BIND=1`.
   Adapted from scopyx's matrix. *(test:
   `TestAWideBindWithNoCredentialRefusesToStart` in `internal/manifest`,
   against the real binary, four rows)*

5. **One way out.** Only `internal/backend` may construct an outbound HTTP
   client (`http.Client{}`, `&http.Client`, `http.Transport{}`,
   `http.DefaultClient`, `http.Get`, `http.Post`) or dial directly
   (`net.Dial`, `net.Dialer`). There is no real network backend in this
   phase, so today this gate holds trivially; it exists now so the day `jev`
   or `openai-logprobs` land, a client built in the wrong package is caught
   immediately rather than found later as an ungoverned egress path.
   *(gate: `scripts/one-way-out.sh`, teeth cases in
   `scripts/gates-have-teeth.sh`)*

6. **Nothing paid by default.** `TYPRYX_BACKEND` is required with no
   default; only `stub` is accepted in this phase, and any other value
   (including `jev`) refuses to start naming it, at exit 2. *(test:
   `TestTheServiceRefusesToStartWithoutANamedBackend` in `cmd/typryx`,
   `TestABackendNotBuiltYetRefusesToStart` in `internal/manifest`)*

7. **Spend is capped by default.** `TYPRYX_MAX_CALLS_PER_HOUR` defaults to
   1000; `0` disables it, and disabling it logs a warning at boot. The cap is
   counted only against calls admitted past the freeform gate, the template
   lookup and the egress filter: a call refused before that point never
   touches the budget. *(test: `TestTheHourlyCapRefusesTheCallAfterTheLimit`,
   `TestRefusedCallsDoNotCountAgainstTheCap`, `TestCapWindowRollsOverWithAnInjectedClock`,
   all in `internal/service`; mutant: the cap taken before the template
   lookup instead of after, caught by
   `TestRefusedCallsDoNotCountAgainstTheCap`)*

8. **Every answer and refusal reaches the record**, unless the journal is
   disabled (`TYPRYX_EVENTS` unset). An event with no agent identity is
   skipped and COUNTED, never given a fabricated `agent_id`. The record
   carries a SHA-384 of the egressed state, never the state itself.
   *(test: `TestEveryAnswerAndRefusalIsRecordedWithoutTheState`,
   `TestAnEventWithNoAgentIsSkippedAndCounted`, both in `internal/service`;
   100% statement coverage on `internal/record`)*

9. **The answer is derived from the probability distribution, never taken
   from the backend's own claim.** `deriveAnswer` computes choice (the argmax
   over the template's sorted option keys), score (the argmax index, ties to
   the lower index) and noul (`Probabilities["true"]`) itself;
   `backend.Answer`'s Choice/Score/Yes fields are documented as advisory and
   are read nowhere. Found in the 2026-09-25 review. *(test:
   `TestTheAnswerIsDerivedFromTheProbabilitiesNotTakenFromTheBackend` in
   `internal/service`, three subtests, each run red first: a backend
   claiming Choice "banana", Score 7, or Yes 0.99 alongside a valid,
   disagreeing distribution had the CLAIM served in every case before the
   fix)*

10. **The probability key set must match exactly.** `validateProbabilities`
    refuses an extra key a backend invented, even when the template's own
    keys already sum to a valid distribution on their own; `len(probs) !=
    len(keys)` is checked before anything else. Found in the 2026-09-25
    review. *(test: `TestAProbabilityForAnOptionTheTemplateDoesNotHaveIsRefused`
    in `internal/service`, run red first: an extra key reached
    `bad_probabilities` silently, i.e. was accepted)*

11. **A torn ledger tail is truncated on disk before the file is reopened for
    append.** A half-written last line (a crash mid-fsync) is cut off both
    `answers.ndjson` and `outcomes.ndjson` at `Open`, before the answer index
    or the outcome-exists set is built and before an `O_APPEND` write can
    land right after it with no separator. Found in the 2026-09-25 review.
    *(test: `TestAnAnswerWrittenAfterATornLineSurvivesTheNextRestart` in
    `internal/ledger`, run red first: the second `Open` succeeded, but the
    answer written right after the crash was silently lost, merged into the
    torn fragment rather than truncated away from it)*

12. **A template is identified by its content digest.** An answer names the
    version it was asked under (`Template.Version()`, sha256 of the
    canonical form), and the ledger's `AnswerRecord` carries that version, so
    `Outcome` scores a later truth against exactly the question that was
    asked, never against whatever the live registry says today.
    *(test: `TestAnOutcomeIsScoredAgainstTheVersionItWasAskedUnder` in
    `internal/service`; mutant: `Outcome` made to re-look-up the current
    template version instead of using the ledger's recorded one. The first
    version of this test PASSED against that mutant, because it built a
    hypothetical changed template and computed its version but never
    replaced the service's live `Templates` registry, so a mutant reading
    the registry saw the same v1 by coincidence. Fixed by actually swapping
    `Service.Templates` for a registry holding the changed template before
    calling `Outcome`; the mutant now fails it)*

13. **An outcome is recorded once per answer, and that holds across a
    restart.** `ledger.PutOutcome` refuses a second outcome for an
    `answer_id` already present in the outcome-exists set built at `Open`
    from `outcomes.ndjson`, with `ErrOutcomeExists` mapped to a 409
    `outcome_exists` refusal. Found in the 2026-09-25 review. *(test:
    `TestASecondOutcomeForTheSameAnswerIsRefused` in `internal/service`,
    including a ledger close/reopen, run red first: a second, contradicting
    outcome for the same answer was accepted)*

14. **components.json is true**, both buckets, proved by starting the real
    binary. *(test: `TestTheManifestMatchesWhatTheBinaryReads` and the rest
    of `internal/manifest`)*

15. **Every scenario binds to a test and back.**
    *(gate: `scripts/features-are-bound.sh`)*

16. **README numbers are true.**
    *(gate: `scripts/readme-numbers.sh`)*

17. **The gates have teeth.**
    *(gate: `scripts/gates-have-teeth.sh`)*

18. **No real secret ever reaches this repository**, tracked or in history.
    *(gate: `scripts/no-secrets.sh`)*

19. **A JSON-RPC notification (a request with no `id`) is answered 202
    Accepted with no body**, keyed on the absent `id` rather than the method
    name, per MCP 2025-06-18's Streamable HTTP transport. Before this, the
    one notification the server handled by name got 204, and any other
    notification got 200 with an error body. *(test:
    `TestANotificationIsAnswered202WithNoBody` in `internal/mcp`, two cases:
    `notifications/initialized` and an invented notification name, so the
    rule is proven general rather than special-cased for one method)*

20. **Every tool's schema is valid JSON Schema for a strict client.**
    `required` is always a JSON array, never `null`: a nil `[]string` in Go
    marshals to `null`, and Claude Code 2.1.270 silently dropped typryx's
    whole tool list over exactly that on `list_questions` (no required
    arguments) before this was fixed, on `main`'s `tools/list` response,
    with typryx's own MCP tests all green at the time. Found running a real
    client, not by any test in this repository. *(test:
    `TestEveryToolSchemaIsValidJSONSchemaForStrictClients` in `internal/mcp`,
    checked with freeform on and off, run red first against the unfixed
    code: `required is not a JSON array (got <nil>, likely null)`)*

21. **`typryx connect` never prints a real key.** Every target names the
    environment variable `--key-env` points at (`TYPRYX_KEY` by default)
    only by its NAME, in a `${...}` placeholder; the variable itself is
    never read. *(test: `TestConnectNeverPrintsAKey` in `cmd/typryx`, which
    sets a fake `TYPRYX_KEYS` in the process environment and checks it in no
    target's output; golden-output tests per target in the same file)*

### Not built yet

- **Calibration**: a probability's calibration is computed per template x
  backend x model, never pooled across them. This is phase E (`typryx
  calibration`). Nothing in this phase reads `outcomes.ndjson` for that
  purpose; it is only written.
- **`jev`** (phase D) does not exist. `TYPRYX_BACKEND=jev` refuses to start,
  naming it. `openai-logprobs` (phase C) is now built; see README's "Local
  model backend" for what it does and does not prove.
- **MCP behind tokenfuse's broker**: untested until phase B2. See README.

## Tier

**T2**: this service parses input from outside the process (HTTP and MCP
request bodies) and is a CLI/HTTP surface another repo will eventually
consume. It becomes **T3** the day wardryx or tokenfuse actually consume it
(phase J), per the estate's testing rule; nothing here reaches that bar yet
because nothing downstream depends on it. No Fable review (paused
estate-wide).

Phase A's own 13 scenario tests were written AFTER the implementation, not
before it: this is a stated deviation, not a claim of red-first, and it was
caught in review. Each was still shown red rather than assumed correct, by
reverting or planting a targeted fault in the code it depends on and
confirming the test failed for that reason, then restoring the fix. Five
mutants were planted by hand against the invariants judged most load-bearing
(2, 3, 6, 7, 12 above); one of those five (invariant 12's) survived its first
version and the test itself had to be strengthened, which is recorded there
rather than smoothed over.

On 2026-09-25 the session model read every product file whole, end to end,
rather than a diff, and found the eleven defects fixed on branch
`fix/review-2026-09-25` (invariants 9, 10, 11 and 13 above are four of
them). This time each fix followed the discipline phase A itself did not:
the test was written first, run against the unfixed code, and shown red for
the stated reason, before anything was changed.

Phase C (the openai-logprobs backend) followed red-first throughout: every
test in `internal/backend/openai_test.go`, `internal/service`'s two new
tests, and `internal/manifest`'s three new tests were run against a
compiling but non-functional stub (`Ask` always returning
`UnansweredError{"not_implemented"}`, or, for the config tests, the
pre-existing `loadConfig` that still refused any backend but `stub`) and
shown red for the stated reason before the real implementation landed. One
exception, named rather than smoothed over:
`TestOpenAIBackendSurvivesHostileResponses` (the 220-seed hostile sweep)
PASSED against the stub, vacuously: an implementation that always errors out
trivially satisfies "never panics, never returns an invalid distribution",
so this property test could not be meaningfully red until the real parsing
existed to be broken. The other 24 new/changed tests all failed against
their stub or reverted state for the reason the test names, including two
verified by temporarily reverting a working fix with `git stash` rather than
never having written it unfixed in the first place (the `errors.As`
precedence in `internal/service.ask`, and `loadConfig`'s
`TYPRYX_BACKEND=openai-logprobs` branch), since those two were implemented
in the same pass as their tests were drafted.

## Design notes worth keeping visible

- **`internal/door`, not `internal/mcp`.** Both `internal/api` and
  `internal/mcp` need the credential check, so it is its own package rather
  than living inside `internal/mcp` (which is where scopyx's equivalent
  lives, since scopyx has only one surface).
- **`internal/service` is the one place ask/outcome logic lives.** Both
  `internal/api` and `internal/mcp` call it and nothing else. This is not
  incidental: it is the reason
  `TestTheMCPToolAnswersTheSameAsTheHTTPRoute` is a real comparison rather
  than two implementations that happen to agree today.
- **`cmd/typryx` splits `loadConfig` / `buildRuntime` / `run`.** `loadConfig`
  validates every `TYPRYX_*` variable and returns before anything is opened
  or bound; `buildRuntime` wires the journal, ledger, service and HTTP
  server without ever calling `ListenAndServe`; `run` is the thin loop that
  actually serves and waits for a signal. This is what lets the
  config-error paths and the wiring be tested in-process and fast, while
  `run`/`main` themselves (the signal-driven serve loop) are proved instead
  by `internal/manifest` starting the real binary and by the process-level
  tests in `cmd/typryx/main_test.go`. Coverage on `cmd/typryx` (78.0%,
  measured `go test ./cmd/typryx/... -cover`, 2026-09-25 after phase C)
  undercounts on purpose for
  exactly this reason: `main` and `run` show 0% in that number because a
  separate `exec.Command`-built binary is not instrumented, even though both
  are exercised by every process-level test in the package.
- **Env var check order in `cmd/typryx`**: required configuration
  (`TYPRYX_BACKEND`, `TYPRYX_TEMPLATES`, including whether the templates
  directory itself is valid) is checked BEFORE the open-bind refusal. This
  differs from scopyx, which has no equivalent required-variable check ahead
  of its bind check. `internal/manifest`'s open-bind matrix always sets both
  required variables first, so the matrix result does not depend on this
  ordering; the ordering is simply the simpler one to reason about from a
  single log line.
- **State-size check runs before JSON parsing.** `template.Filter` checks the
  raw byte length against `MaxStateBytes` before calling `json.Unmarshal`, so
  an oversized hostile payload never reaches the parser.
- **`internal/backend/openai.go`'s wire types never fail to unmarshal.**
  `flexibleFloat` and `flexibleString` accept a JSON number, a JSON string
  (including `"NaN"`, `"Infinity"`, a value too large for float64), or
  anything else, and turn whatever they cannot make sense of into `NaN` or
  `""` rather than returning an error. A single hostile `top_logprobs` entry
  then simply does not count towards any label, instead of an
  `encoding/json` type-mismatch on one entry discarding every other,
  well-formed entry beside it. `probabilitiesFor` skips any non-finite value
  explicitly, so this can never let a NaN or Inf reach a served probability.
- **A "required when chosen" variable is `"required": false` plus a
  `required_when` note in `components.json`.** `TYPRYX_OPENAI_URL` and
  `TYPRYX_OPENAI_MODEL` must not be demanded of a deployment that picked
  `TYPRYX_BACKEND=stub`, but must be demanded, by name, of one that picked
  `openai-logprobs`; `internal/manifest`'s
  `TestARequiredWhenChosenVariableRefusesToStartByNameWhenMissing` walks
  every env var carrying a `required_when` note and proves both halves
  against the real binary, generically enough that a future conditionally
  required variable is covered by adding a working-env fixture rather than a
  new test.

## Escalate, do not push through

Stop and tell the user, then wait:

- A second backend, a default backend, or enabling any paid backend.
- Sending a field a template does not name.
- Turning a probability into a `deny` anywhere, in this repo or a consumer.
- Registering event types in agent-passport SPEC (phase F).
- Creating the `TAIPANBOX/typryx` GitHub repository, or any other
  outward-facing action.

## Conventions

- No long dashes anywhere: not in code comments, docs, commit messages, or PR
  bodies. Use a comma, a colon, parentheses, or a short hyphen.
- Nothing paid or metered gets enabled without telling the user first and
  getting agreement. There is nothing paid in this phase to enable.
- Do not delete or revoke keys, tokens, or certificates on your own initiative.
