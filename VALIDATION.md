# Validation records

Where each accuracy and calibration figure stated about typryx outside this repository
comes from: the date, what ran, the exact commands, where the artifacts are, and what
the figures cover. A figure here carries `@measured <how> <date>`, where `<how>` is the
command that printed it.

## 2026-11-25: Claude Haiku 5.5 on the 60 arithmetic items

The 60 items of `examples/calibration` (seed 1), judged by `claude-haiku-5-5` through
Anthropic's OpenAI-compatible endpoint, run on forge (4 cores, x86_64, a home
connection in the UK) from typryx `cdbfc89` plus the `examples/speed` request flags
added with this record, binaries built there with Go from this tree. One run per row.

| Way of deciding | n | Accuracy | Unparsed | p50 | p95 | Mean | Input tokens | Output tokens |
|---|---|---|---|---|---|---|---|---|
| text judge, `max_tokens: 400`, no `temperature` | 60 | 1.000 | 0 | 629 ms | 1129 ms | 715 ms | 4,440 | 4,137 |
| reasoning judge, `max_completion_tokens: 4000` | 60 | 1.000 | 0 | 635 ms | 981 ms | 704 ms | 4,440 | 4,261 |
| typed, one token, through typryx (`eval.outcome_met` v1) | 60 | 0 answered | | | | | | |

`@measured ./bin/speed -mode text -url https://api.anthropic.com/v1 -model claude-haiku-5-5 -keyfile keys/anthropic -temperature omit 2026-11-25`
(row 1) and `@measured ./bin/speed -mode reasoning -url https://api.anthropic.com/v1 -model claude-haiku-5-5 -keyfile keys/anthropic 2026-11-25`
(row 2), each under `forge-heavy`, printed verbatim:

```
text      claude-haiku-5-5         n=60 acc=1.000 unparsed=0 p50=629ms p95=1129ms mean=715ms total=42.9s in_tok=4440 out_tok=4137
reasoning claude-haiku-5-5         n=60 acc=1.000 unparsed=0 p50=635ms p95=981ms mean=704ms total=42.3s in_tok=4440 out_tok=4261
```

Row 3, `@measured ./bin/calibration -url http://127.0.0.1:4391 -key k1 -n 60 -seed 1 2026-11-25`
against `typryx serve` with `TYPRYX_BACKEND=openai-logprobs`,
`TYPRYX_OPENAI_URL=https://api.anthropic.com/v1`, `TYPRYX_OPENAI_MODEL=claude-haiku-5-5`,
`TYPRYX_OPENAI_TOP_LOGPROBS=5`, `TYPRYX_OPENAI_MAX_COMPLETION_TOKENS=16`,
`TYPRYX_OPENAI_REASONING_EFFORT=none`: `asked=60 answered=0 outcomes_recorded=0`, and
typryx's log holds 60 lines `openai-logprobs: server refused the call status=400
error_type=invalid_request_error`. A direct probe the same hour named the refusal
(`temperature` is deprecated for this model) and showed that, without `temperature`,
the endpoint returns the letter with `logprobs: null`. typryx keeps `temperature: 0` in
every request (invariant 44), so Haiku 5.5 cannot be a typed judge through this backend,
and there is no calibration row for it.

Cost: 8,880 input and 8,398 output tokens at USD 0.10 and USD 0.50 per million
(Anthropic's price for `claude-haiku-5-5`), about USD 0.005; the refused asks are not
billed. Paid from the Anthropic API key.

Not re-run on this date, and why: the OpenAI rows need an OpenAI API key, and none was
available on 2026-11-25. `qwen2.5:3b` and `qwen2.5:7b` need an Ollama server, and none
runs on forge.

## The 2026-09-25 hosted-model figures are historical

README's "Hosted models, and what one token can and cannot judge" and "How fast is a
typed decision" tables (both 2026-09-25) stay as measured and are marked historical.
Model status, from OpenAI's deprecations page read 2026-11-25:

| Model in those tables | Status on 2026-11-25 | Replacement OpenAI names | Its price on 2026-11-25 (USD per million, input / output) |
|---|---|---|---|
| `gpt-4.1-nano-2025-04-14` | shut down 2026-10-23 | `gpt-5.6-luna` | 0.20 / 1.20 |
| `gpt-4o-mini-2024-07-18` | retires 2027-01-14 | `gpt-6-luna` | 0.10 / 0.50 |
| `gpt-4.1-mini-2025-04-14` | retires 2027-01-14 | `gpt-6-luna` | 0.10 / 0.50 |
| `gpt-5.4-mini` | current | | 0.75 / 4.50 |

Both replacements were measured on the same 60 items on 2026-11-06 (README, "Current
OpenAI models, measured 2026-11-06"): `gpt-6-luna` 0.717 to 0.733 accurate at a mean
confidence of 0.960 to 0.978 (ECE 0.256 to 0.274, three runs), `gpt-5.6-luna` 0.600 to
0.650 at 0.967 to 0.984 (ECE 0.329 to 0.393, three runs).

## 2026-09-30: the typryx-evalset test split, three data modes

The figures for the three data modes (a hosted Jev, your own model, typed answers off)
come from one run of [typryx-evalset](https://github.com/TAIPANBOX/typryx-evalset) on
its frozen test split: 434 synthetic questions in six families, `data/test.jsonl`,
sha256 `bd5f3cf42b81bb1ba14938b84c0bf321df4ed1f6ec96015950c1357033bab03f`. One run per
row, one ask at a time.

| Backend | Accuracy | Wilson 95% interval | ECE | p50 latency | Where it ran |
|---|---|---|---|---|---|
| typryx + `jev-1.13.0` (hosted) | 87.1% | 83.6 to 89.9 | 0.042 | 229 ms | a development laptop calling the hosted service |
| typryx + `qwen2.5:7b` via Ollama (your own model) | 70.0% | 65.6 to 74.2 | 0.273 | 2,130 ms | an 8-vCPU CPU VM (GCP `n2-standard-8`), no GPU, model on the VM's loopback |
| constant, no model (typed answers off) | 25.1% | 21.3 to 29.4 | 0.749 | no model call | locally, no service |

`@measured python3 bench/metrics.py results/{jev,qwen7b,constant}.jsonl 2026-09-30`,
the overall row of each file. Published in typryx-evalset's README, section "Results",
from its first public commit `6017b7e` (2026-09-30), unchanged since.

How each row was produced, in typryx-evalset:

- **Your own model.** The VM was set up by typryx-evalset's `vm/setup.sh` (Debian 12,
  Ollama, `qwen2.5:7b` on `127.0.0.1:11434`). typryx ran on the same VM with
  `TYPRYX_BACKEND=openai-logprobs`, `TYPRYX_OPENAI_URL=http://127.0.0.1:11434/v1`,
  `TYPRYX_OPENAI_MODEL=qwen2.5:7b` and `TYPRYX_TEMPLATES` pointing at typryx-evalset's
  `templates/`, built with the change merged as `51e2a3d` (#11: a distribution
  rounded to four decimals is served as returned). Then:

  ```sh
  python3 bench/run.py --typryx-url http://127.0.0.1:4320 --key-file PATH \
      --backend-name qwen7b --split test --out results/qwen7b.jsonl
  python3 bench/metrics.py results/qwen7b.jsonl
  ```

  433 of the 434 asks were answered; the other came back `unanswered` with
  `label_mass_too_low` and counts as wrong.
- **Jev.** typryx `15ee54d` with `TYPRYX_BACKEND=jev`, the same two commands with
  `--backend-name jev`. The details are in README's "Jev backend" section.
- **No model.** No typryx at all: always the most frequent train label of each family,
  the stack's behaviour without typed answers.

  ```sh
  python3 -m baseline.predict --which constant --split test --out results/constant.jsonl
  python3 bench/metrics.py results/constant.jsonl
  ```

The result files stay outside typryx-evalset by its own rule (its README: results files
stay on the machine that produced them, so no hosted model's output sits beside the
data), and outside this repository too. Each row is one run on one day, on synthetic
questions; a hosted service can change under you, so its row is a statement about
2026-09-30.

## Two `qwen2.5:7b` measurements, two different questions

README's "Hosted models, and what one token can and cannot judge" table gives
`qwen2.5:7b` accuracy 0.667 and ECE 0.298. That is a different measurement from the
70.0% / 0.273 above, and the two do not contradict each other:

| | the 434-question benchmark (above) | README's calibration table |
|---|---|---|
| date | 2026-09-30 | 2026-09-25 |
| questions | typryx-evalset's test split, six families, 434 rows | 60 arithmetic items (`examples/calibration`, seed 1), one family: is `A x B = C` correct |
| template | typryx-evalset's six templates | `eval.outcome_met`, wording v2 (`781efaaf`) |
| where | a CPU VM, Ollama | a development Mac, Ollama |
| result | 70.0% accurate, ECE 0.273 | 0.667 accurate, ECE 0.298 |

The 60-item table answers how well a one-token judge checks arithmetic; the 434-question
run answers how well the model does across all six question families. Quote each with
its own question.
