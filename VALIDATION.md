# Validation records

Where each accuracy and calibration figure stated about typryx outside this repository
comes from: the date, what ran, the exact commands, where the artifacts are, and what
the figures cover. A figure here carries `@measured <how> <date>`, where `<how>` is the
command that printed it.

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
