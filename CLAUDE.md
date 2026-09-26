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
./scripts/templates-load.sh
./scripts/gates-have-teeth.sh   # needs a clean tree, run it after committing
gosec -quiet ./...              # v2.29.0, as CI; silent when clean
govulncheck ./...               # v1.7.0, as CI
```

The last two ran only in CI until 2026-09-25, when phase C reached `main` with two gosec
findings (G115, G703) that every local gate had passed. Run them before pushing.

## Hard invariants

Each one carries how it is held: `(test: ...)` or `(gate: ...)`. An invariant
with no check, written as though it had one, is worse than an absent
invariant. Numbering here is this file's own running sequence, not the
plan's: the plan's invariant 9 (calibration, never pooled) is now built
(phase E) and is numbered 22 below, added at the end rather than inserted at
9, because inserting it there would have forced every invariant after it to
be renumbered. A number in this list identifies an invariant, not a position
in the plan.

1. **Never invents an answer.** A backend error, a timeout, missing or
   malformed probabilities, a cap hit: the result is `unanswered` with a
   reason, and the wire shape omits `answer` and `probabilities` entirely
   rather than sending a null or a zero. No renormalizing, no fallback guess.
   Reasons: `backend_error`, `timeout`, `canceled`, `no_probabilities`,
   `bad_probabilities`, `over_hourly_cap`, and, from the openai-logprobs
   backend's own `backend.UnansweredError` (phase C), `no_logprobs`,
   `label_mass_too_low`, `too_many_options`, `bad_logprobs`. The last is a
   server that did not send a distribution (a logprob above 1e-6, or label
   mass above 1.001): normalising it would turn nonsense into a confident
   answer, so it is refused. The bounds admit Ollama's rounding of a certain
   token to -0.0 and nothing wider. *(test:
   `TestALogprobAboveZeroOrAMassAboveOneIsUnanswered`,
   `TestTheLogprobsOllamaActuallyReturnedAreAccepted`; found in the session
   model's review of phase C, 2026-09-25, after the hostile sweep passed:
   the sweep checks the output, which normalisation always makes look valid;
   four mutants on the two bounds each caught)*
   The jev backend's own `backend.UnansweredError` (phase D) adds
   `no_probabilities` (an answer missing under its id, of the wrong type, or
   with no probabilities, or a noul answer whose `noul` field is absent or
   null) and `bad_noul` (a noul answer's own number is non-finite or outside
   [0,1]), so the two-key distribution this backend builds is never built
   out of nonsense; choice and score probabilities are passed through
   unvalidated by this backend on purpose, since
   `internal/service.validateProbabilities` stays the one authority over
   them. Until 2026-09-25 this paragraph said a MISSING noul number was
   refused while the code decoded it into a float64, read absence as 0, and
   served a certain "false"; the session model's review found it, and the
   field is now a pointer. *(test: `TestAMissingAnswerIsUnanswered`,
   `TestANoulOutsideZeroOneIsUnanswered`,
   `TestAMissingNoulIsUnansweredNotACertainNo` (red first: `{false:1
   true:0}` served; mutant restoring the zero caught), all in
   `internal/backend`)*
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

22. **Calibration groups by (template, template_version, backend, model),
    never pooled across any of the four.** This is the plan's own invariant
    9, built in phase E. Pooling any one of the four hides exactly the case
    calibration exists to catch: two models under the same template with
    opposite calibration averaging out to something in between, looking fine
    on paper while neither one is. *(test:
    `TestTwoModelsAreNeverScoredAsOne` in `internal/calibration`: a baseline
    group plus four variants, each differing from it in exactly one of the
    four fields; mutants dropping any one of the four from the group key,
    each caught)*

23. **Calibration never mutates the ledger it reads.** `internal/calibration`
    takes one `os.ReadFile` snapshot of each NDJSON file; unlike
    `internal/ledger.Open`, it never truncates a torn tail on disk, rewrites
    a line, or takes a lock, because the service may still be running and
    appending to the same files while a calibration run reads them. *(test:
    `TestCalibrationNeverModifiesTheLedgerItReads` in `internal/calibration`:
    sha256 of both files, one with a torn tail present, unchanged before and
    after `Run`; mutant: writing the truncated tail back to disk, caught)*

24. **Fewer than `--min-n` scored items gives no verdict, and no bound is
    judged.** A group's verdict is `insufficient`, never `ok` and never
    `drift`, until it has enough scored outcomes to say anything; an absurdly
    tight bound that would otherwise flag it is not even checked. *(test:
    `TestTooFewTruthsGiveNoVerdict` in `internal/calibration`; mutant:
    removing the `n < min-n` check, caught)*

25. **A calibration verdict is reported, never acted on.** `typryx
    calibration` measures and, with `--emit`, writes one `calibration_drift`
    event; nothing here turns a drift verdict into an enforcement action, a
    cap change, or a `deny`. Held structurally, not by a runtime check:
    `internal/calibration.Run` takes no path to write to and has no side
    effect beyond reading (invariant 23's test is the same evidence, from the
    other direction); `internal/service` has no dependency on
    `internal/calibration` at all (`go list -f '{{join .Deps "\n"}}'
    ./internal/service | grep -c calibration` prints 0); and `--emit`'s own
    write is one explicit, separate step a caller chooses, gated on a real
    `--agent-id` (invariant 3's identity rule, applied here too).

26. **A rate-limited or overloaded Jev call is retried, briefly, and never
    past the caller's deadline.** Only 429 and 529 are retried, up to 3
    attempts total, with an exponential backoff (200ms, then 400ms) a
    server's own `Retry-After` header can override, capped at 2s either way.
    The wait between attempts is always raced against the context's own
    `Done()`, so a retry that would cross the caller's deadline is never
    made: the ask ends `timeout`, through the same precedence every other
    backend's deadline already goes through in `internal/service.ask`, not
    through anything jev decides for itself. Every other status (401, 422,
    anything else) is never retried. *(test:
    `TestRateLimitIsRetriedWithBackoffAndThenSucceeds`,
    `TestRetryAfterIsHonouredButCapped`, `TestNoRetryOn401Or422`,
    `TestARetryNeverCrossesTheDeadline`, all in `internal/backend`)*

27. **Jev's cost is computed only from the configured price, never
    hardcoded.** `TYPRYX_JEV_PRICE_PER_MTOK_INPUT`/`_OUTPUT` are the only
    source of a non-zero `cost_usd`; unset, both default to 0 and every
    answer reports `cost_usd: 0` regardless of how many tokens were used,
    which is what "unpriced" means here rather than a guessed number.
    *(test: `TestCostIsInputTokensTimesTheConfiguredPrice`,
    `TestCostIsZeroWhenNoPriceIsConfigured`, both in `internal/backend`)*

28. **The starter catalog ships the minimum.** Every template in
    `examples/templates` (the directory the Dockerfile also copies to
    `/etc/typryx/templates`) loads clean, the directory is never missing or
    empty, and no template's `fields` names one of a short denylist of
    identifying field names (`email`, `user_email`, `name`, `phone`, `iban`,
    `card`, `api_key`, `token`, `password`, `ssn`, `address`). *(gate:
    `scripts/templates-load.sh`, teeth in `scripts/gates-have-teeth.sh`;
    test: `TestEveryStarterTemplateLoads`, `TestNoStarterTemplateNamesAnIdentifyingField`,
    both in `internal/template`)*

29. **A hosted endpoint never sees typryx's own identifiers unless the
    operator switched that on by name.** `TYPRYX_OPENAI_METER_HEADERS`
    defaults to off, and off, `openai-logprobs` sends neither `x-fuse-run-id`
    nor `x-fuse-agent-id` to any endpoint, whatever a caller's ask carried.
    On, the two headers carry the ask's own run id (or, when the caller gave
    none, `TYPRYX_OPENAI_RUN_ID`) and the agent the caller's credential
    resolved to; a run id is validated at the API and MCP boundary before it
    ever reaches a service call or an outbound header (at most 128 bytes, no
    control character, which also rules out a header-splitting CRLF), so
    `bad_run_id` is refused before anything downstream ever sees the value.
    This is what lets an operator who points `TYPRYX_OPENAI_URL` at a
    metering gateway such as tokenfuse's own OpenAI-wire gateway make
    typryx's own spend visible on that gateway's side of the door, entirely
    by configuration: tokenfuse itself is not changed for this (see the
    `@decided` note below). *(test: `TestMeteringHeadersAreNeverSentWhenTheFlagIsOff`,
    `TestMeteringHeadersCarryTheCallersRunIDAndAgentIDWhenOn`,
    `TestMeteringHeadersFallBackToTheConfiguredRunIDWhenTheCallerGaveNone`,
    `TestMeteringHeadersSendNeitherWhenOnButNothingIsSet`, all in
    `internal/backend`; `TestValidRunID` in `internal/door`;
    `TestAskRejectsABadRunID`, `TestAskAcceptsAWellFormedRunID` in
    `internal/api`; `TestAskToolRejectsABadRunID`,
    `TestAskFreeformToolRejectsABadRunID` in `internal/mcp`;
    `TestJevNeverForwardsRunIDOrAgentID` in `internal/backend`, since the jev
    backend reads neither field at all; mutant: the `MeterHeaders` check
    removed so headers are always sent, caught by
    `TestMeteringHeadersAreNeverSentWhenTheFlagIsOff`)*

30. **The openai-logprobs backend's cost is computed only from the configured
    price, never hardcoded.** `TYPRYX_OPENAI_PRICE_PER_MTOK_INPUT`/`_OUTPUT`
    are the only source of a non-zero `cost_usd` for this backend, the same
    shape invariant 27 already holds for jev; unset, both default to 0 and
    every answer reports `cost_usd: 0` regardless of how many tokens were
    used. *(test: `TestOpenAICostIsInputTokensTimesTheConfiguredPriceNeverSwapped`,
    `TestOpenAICostIsZeroWhenNoPriceIsConfigured`, both in `internal/backend`;
    mutant: the input and output prices swapped in the cost formula, caught
    by `TestOpenAICostIsInputTokensTimesTheConfiguredPriceNeverSwapped`)*

31. **An optional daily USD spend cap is checked before the call, never
    measures a backend that cannot report cost, and is lost on restart, the
    same as the hourly cap.** `TYPRYX_MAX_USD_PER_DAY` unset means no daily
    cap at all; `0` is the explicit disabled state, logging a boot warning
    like the hourly cap's own `0`; a positive value is a fixed UTC-calendar-day
    window, checked (`>=`, so the call that would reach the limit is the one
    refused) before the backend is asked and refused as `over_daily_spend_cap`
    (429), recorded exactly like `over_hourly_cap`. Spend is added after any
    call that reported usage, whether it went on to answer, come back
    unanswered, or fail outright, since tokens a paid backend already spent
    cost money regardless of what the response validated as; an unpriced
    backend's cost is always 0, so it never moves the total. Startup refuses
    (exit 2) when a positive cap is configured over a backend that would
    report `cost_usd` 0 for every call regardless (`stub`, or
    `openai-logprobs`/`jev` with both configured prices at 0): a cap on a
    backend that reports no cost measures nothing, and starting anyway would
    let an operator believe an unpriced deployment is bounded when it is not.
    The one bound this cap does not close: a call already in flight when the
    cap is reached is not stopped mid-call, so the day's actual spend can
    overshoot the configured limit by up to one call's worth; README names
    this beside the daily cap's own documentation. *(test:
    `TestTheDailyUsdCapRefusesTheCallAfterTheLimitIsReached`,
    `TestTheDailyUsdCapIsNeverConsumedByARefusedCall`,
    `TestNoDailyUsdCapNeverRefuses`,
    `TestUsdCapWindowRollsOverAtTheNextUTCDayWithAnInjectedClock`,
    `TestUsdCapAtZeroOrLessNeverRefusesOrAdds`, all in `internal/service`;
    `TestLoadConfigRefusesAPositiveMaxUsdPerDayOnTheStubBackend`,
    `TestLoadConfigRefusesAPositiveMaxUsdPerDayOnAnUnpricedOpenAIBackend`,
    `TestLoadConfigRefusesAPositiveMaxUsdPerDayOnAnUnpricedJevBackend`,
    `TestLoadConfigAcceptsMaxUsdPerDayZeroAsExplicitlyDisabled`, all in
    `cmd/typryx`; mutants: the `>=` check turned `>`, spend never added, and
    the day window never rolling forward, each caught by the tests named
    above (the first two by `TestTheDailyUsdCapRefusesTheCallAfterTheLimitIsReached`,
    the third by `TestUsdCapWindowRollsOverAtTheNextUTCDayWithAnInjectedClock`);
    the unpriced-backend start check removed, caught by
    `TestLoadConfigRefusesAPositiveMaxUsdPerDayOnTheStubBackend` and its
    `openai-logprobs`/`jev` siblings)*

32. **A brokered `tools/call` may carry its credential in `params._meta`, only
    when the operator opted in, and only for a call that needs one.**
    `TYPRYX_ACCEPT_KEY_IN_META` (off by default, `door.TruthyEnv`) changes
    `POST /mcp` alone. A `tools/call` with no `X-Typryx-Key` header may
    resolve its identity from `params._meta["typryx/key"]` instead, through
    the exact same `door.Keys` constant-time path a header goes through; a
    header, when one is also present, is always the one used.
    `initialize`, `tools/list`, and a JSON-RPC notification need no
    credential at all either way, since none of them reach a backend or
    name an agent; `ask`, `ask_freeform` and `list_questions` (every
    `tools/call`) stay authenticated in every case, and a call with neither
    a header nor a usable `_meta` credential is refused, never treated as
    one more "needs nothing" case. With the flag off, `_meta` is never even
    consulted: `initialize`/`tools/list` still need the header, exactly as
    before this invariant existed. `/v1/ask`, `/v1/outcome` and
    `/v1/templates` never accept a credential from the body, whatever this
    flag says: it names one JSON-RPC field of one method on `/mcp`, nothing
    on the `/v1/*` wire shape. The flag also never widens the open-bind
    refusal (invariant 4): a non-loopback bind with no `TYPRYX_KEYS`
    configured still refuses, since `door.RefuseOpenBind` never reads it.
    *(test: `TestAcceptKeyInMetaOnAuthenticatesToolsCallFromMeta`,
    `TestAcceptKeyInMetaInitializeAndToolsListNeedNoCredential`,
    `TestAcceptKeyInMetaNotificationNeedsNoCredential`,
    `TestAcceptKeyInMetaToolsCallWithNoKeyAtAllIsRefused`,
    `TestAcceptKeyInMetaWrongMetaKeyIsRefused`,
    `TestAcceptKeyInMetaHeaderWinsOverMetaAndMetaIsStripped`,
    `TestAcceptKeyInMetaOffKeepsTodaysBehavior`,
    `TestAcceptKeyInMetaDoesNotAffectV1Routes`,
    `TestAcceptKeyInMetaOnlyChangesPOST`, all in `internal/api`;
    `TestAcceptKeyInMetaEndToEndInitializeAndToolsListWithNoHeader` in
    `internal/mcp`; `TestAcceptKeyInMetaDoesNotWidenTheOpenBindEscape` in
    `internal/manifest`, against the real binary; mutants: a `tools/call`
    with neither credential answered anyway, caught by
    `TestAcceptKeyInMetaToolsCallWithNoKeyAtAllIsRefused`; header and
    `_meta` precedence flipped, caught by
    `TestAcceptKeyInMetaHeaderWinsOverMetaAndMetaIsStripped`; `_meta`
    consulted with the flag off, caught by
    `TestAcceptKeyInMetaOffKeepsTodaysBehavior`; `/v1/ask` reading a body
    credential, caught by `TestAcceptKeyInMetaDoesNotAffectV1Routes`; the
    open-bind escape widened by the flag, caught by
    `TestAcceptKeyInMetaDoesNotWidenTheOpenBindEscape` (shown red first
    against a deliberately widened `RefuseOpenBind` call))*

33. **A `_meta` credential is removed from the request before anything
    downstream ever sees it, used or not, and never reaches a log line, the
    journal, the ledger, an error message, or a response.**
    `mcp.ExtractMetaKey` strips only the one named entry (`typryx/key`),
    never the whole `_meta` object, and `internal/api`'s door calls it
    unconditionally, before deciding whether the request even needs a
    credential and before deciding whether a header will be used instead: a
    header-and-`_meta` call still has the `_meta` entry stripped even though
    its value is never used. `internal/mcp.Server` itself needs no change to
    hold this: its own `tools/call` parsing has never captured `_meta` at
    all, so the entry is inert to it either way, and this invariant is what
    keeps that true structurally rather than by omission. There is no log
    line to check in this codebase today: nothing in the request path logs
    a body or a credential value, checked by reading `internal/api`,
    `internal/mcp` and `internal/service` whole, so that sink is proven
    vacuous rather than merely unchecked. *(test:
    `TestExtractMetaKeyStripsOnlyTheNamedEntry`,
    `TestExtractMetaKeyDropsMetaEntirelyWhenItWasTheOnlyField`,
    `TestExtractMetaKeyIsANoopWhenAbsentOrWrongMethodOrWrongType`,
    `TestExtractMetaKeyNeverPanicsOnHostileBody` (200 seeds), all in
    `internal/mcp`; `TestAcceptKeyInMetaMetaKeyNeverReachesTheMCPHandler` in
    `internal/api`, the direct structural proof at the api/mcp seam;
    `TestAcceptKeyInMetaMetaKeyNeverReachesJournalLedgerOrResponse` in
    `internal/mcp`, end to end through the real service, journal and
    ledger; mutant: the strip step skipped (the original, unstripped body
    forwarded), caught by `TestAcceptKeyInMetaMetaKeyNeverReachesTheMCPHandler`,
    which fails on the marker credential appearing in what the handler
    received)*

34. **A backend server's refusal is told apart from its failure, by its
    machine code, never by its message.** A 4xx answer is logged as "server
    refused the call" and a 5xx as "server failed the call", with the status
    and, when the body is `{"error":{"type"|"code":"..."}}` holding only
    lowercase letters, digits and underscores (at most 64 bytes), that code as
    `error_type`: enough to read a tokenfuse gateway's `metering_required` in
    typryx's own log. The message text, which may echo what was sent, never
    reaches the log or the caller. *(test:
    `TestARefusalIsLoggedWithItsMachineCodeNeverItsMessage`,
    `TestAJevRefusalIsLoggedWithItsMachineCodeNeverItsMessage`, both in
    `internal/backend`; mutants M34-1 to M34-5, each caught)*

@decided 2026-09-25: tokenfuse is not changed for typryx. It runs exactly as it
does without typryx, and typryx joins it through the MCP broker's named-upstream
configuration alone; the agent behind a brokered call stays on tokenfuse's own
record, and typryx's journal counts such a call as `skipped_no_agent`.

@decided 2026-09-25: typryx is offered in three data modes: without it (the
stack runs exactly as before), with a local model (`openai-logprobs` against
a model server in the customer's own infrastructure, nothing leaves it), and
with a hosted model (`openai-logprobs` against a hosted endpoint, or `jev`;
only the fields a template names leave, to a named third-party processor
under its own terms). Choosing a hosted backend is the customer's own
explicit decision to send those named fields to that named processor, never
a default. See README's "Where your data goes".

@decided 2026-09-25: typryx's own spend through the `openai-logprobs` backend
can be made visible to a tokenfuse gateway's budget, entirely by
configuration and by headers tokenfuse already reads (`x-fuse-run-id`,
`x-fuse-agent-id`); this HARD CONSTRAINT holds: tokenfuse itself is not
changed for typryx, and nothing is added to tokenfuse's repository. Off by
default (`TYPRYX_OPENAI_METER_HEADERS` unset); on, only when an operator both
points `TYPRYX_OPENAI_URL` at the gateway and sets the flag by name, so a
hosted provider that is not a gateway this operator chose never receives
either header. See README's "Local model backend" for the second `typryx
connect tokenfuse` block this adds.

@decided 2026-09-26: three rules for every link between typryx and the rest of
the stack, so that adding typryx can never add a new way for a core service to
slow down or fail. (1) typryx is never on the path of an agent request: the
money plane and the policy plane (tokenfuse, wardryx) name typryx in no file,
code, configuration or docs; held across repositories by estate-gates C22. (2)
The shared event bus is the default integration: typryx writes its four event
types to the bus and a consumer reads them as ordinary events, which all three
launchers do since 2026-09-26. (3) A direct call from a consumer to typryx is
optional and off by default, with a short timeout, and the consumer behaves as
if typryx were absent when it is slow, refusing or down; where typryx's answer
IS the result a person asked for (verdryx's typed grader), a failure is
reported as that run's failure and blocks nothing else. Each new link proves
both halves in its own suite: unchanged behaviour without typryx, and typryx
off, slow or failing does not break the consumer.

### Not built yet

- **`jev`** (phase D) is built: `TYPRYX_BACKEND=jev` starts and makes real
  calls to `TYPRYX_JEV_URL`. It has been run only against an httptest fake
  replaying the wire shape documented at docs.typesafe.ai (pinned in
  `internal/backend/testdata/jev_example_response.json`); there is no key
  and no spend approval to call the real `api.typesafe.ai`, so nothing here
  has been run against it. See README's "Jev backend" section and NOT
  PROVEN. `openai-logprobs` (phase C) and calibration (phase E) are also
  built; see README's "Local model backend" and "Calibration" sections.
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

Phase D (the jev backend) followed the same discipline: `internal/backend/jev.go`
started as a compiling stub (`Ask` always returning
`UnansweredError{"not_implemented"}`, `cmd/typryx` still refusing any backend
but `stub` and `openai-logprobs`) and every test in `internal/backend/jev_test.go`,
including its 200-seed hostile response sweep, `cmd/typryx/main_test.go`'s new
jev tests, and `internal/manifest`'s two new jev tests, was run against that
stub and failed for the stated reason (a compile error for the config-struct
tests, since `cfg.jev` did not exist yet; `UnansweredError{"not_implemented"}`
for everything that called `Ask`) before the real implementation and wiring
landed. One supporting change preceded the backend itself and followed the
same rule on its own: `TestNoulCriteriaGivenReflectsWhetherTheTemplateSetAny`
in `internal/service` failed against the unfixed `questionFor` (which
discarded `template.NoulCriteria`'s own `ok` result) before that one line
was fixed.

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
