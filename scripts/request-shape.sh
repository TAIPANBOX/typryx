#!/usr/bin/env bash
# The openai-logprobs request is a contract with servers this repository does
# not control, and it has two sides that pull against each other. A deployment
# that set nothing keeps sending exactly what it always sent, because an older
# server refuses reasoning_effort by name; a deployment that set the three
# request variables sends what a current OpenAI model accepts, because that
# model refuses max_tokens by name. Raising the token budget also takes away
# what max_tokens: 1 used to guarantee for free, so the first-token rule is held
# here as well, and so is the rounding those models put on a near-certain token.
#
# Run by name, not by package: `go test` over a package stays green if one of
# these tests is deleted, so each is required to have actually run.
set -euo pipefail
cd "$(dirname "$0")/.."

tests=(
  TestNothingConfiguredSendsTheRequestAnOlderServerAlreadyAccepts
  TestACurrentModelIsAskedTheWayItAccepts
  TestEachRequestSettingMovesOnlyItsOwnField
  TestOnlyTheFirstTokenIsReadWhenTheModelWritesMore
  TestTheLogprobsACurrentOpenAIModelActuallyReturnedAreAccepted
  TestTheOpenAIRequestSettingsReachTheWire
)
pattern="^($(IFS='|'; echo "${tests[*]}"))\$"

out=$(go test ./internal/backend ./cmd/typryx -run "$pattern" -count=1 -v 2>&1) || {
  echo "$out" >&2
  echo "FAIL: the openai-logprobs request no longer matches what a server accepts" >&2
  exit 1
}
for t in "${tests[@]}"; do
  grep -q -- "^--- PASS: $t " <<<"$out" || {
    echo "FAIL: $t did not run; this gate measured nothing" >&2
    exit 1
  }
done
echo "request shape: ${#tests[@]} tests, an older server and a current model each get what they accept"
