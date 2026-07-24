# Builder-agent local-model benchmark

The standing acceptance benchmark from `docs/source/builder-harness-spec.md` §5
and `docs/BUILDER_HARNESS.md`, made concrete: the **14-function pure-helper
extraction refactor** — the reproduced failure that motivated mechanisms M1–M7.
It exists to answer, with numbers, D-AUTONOMY #2: *are the builder agents
verified working against the local model fabric?*

- **Fixture** (`fixture/`): a Go package with 14 exported Format functions each
  repeating the same validate→parse→clamp→format block, plus a table test that
  pins all behavior. The duplicated block is grep-detectable
  (`TrimSpace(strings.ToLower` appears 14× before, ≤1 after a real extraction).
- **Task** (`task.md`): the dispatch prompt. States the mechanical DONE WHEN.
- **Runner** (`run.sh`): per round — fresh temp workspace, fresh agent process
  (the repo's real `cmd/agent` loop) pointed at a LiteLLM lane, task POSTed to
  `/tasks/send`, polled to terminal, then scored **mechanically**:

  | check | how |
  |---|---|
  | tests_green | `go test ./...` exit 0 in the workspace |
  | dup_gone | duplicated-block marker count ≤ 1 in `metrics.go` |
  | testfile_untouched | sha256 of `metrics_test.go` unchanged |
  | gofmt_clean | `gofmt -l` empty |

  `clean` = all four. The agent's own claim of success is never consulted —
  same principle as the v2 done-gate (D-GATE).

Failed-run workspaces are kept under `results/failed-*` for diagnosis; results
append to `results/<lane>.csv`.

## Full-flow benchmark (`run-flow.sh`) — Phase 2 P2.0

`run.sh` above is **edit-only**: it scores the refactor in place and never
exercises the leg that actually broke in the Phase-1 dogfood — `git-commit`
(commit+push) → `create-pr`. `run-flow.sh` scores the **whole flow** against a
throwaway remote, so the completion the platform depends on is measured, not
assumed.

Per round it stands up a **bare throwaway origin** seeded with the fixture on
`main`, pre-clones it into the agent workspace as `./benchfixture`, and runs the
repo's real `cmd/agent` loop with the `git-commit` and `create-pr` tools enabled
(task in `flow-task.md`). PRs land in **`fakegitea/`** — a stdlib-only, offline
Gitea PR sink that is faithful in the one way that matters: it returns Gitea's
real **404 when the PR head branch does not exist on the remote** (the exact
friction a builder hits when `git-commit` renames its branch). An accepted PR is
therefore a production-plausible PR. No live Gitea, no network.

Scoring is mechanical — the agent's claim of success is never consulted:

  | check | how |
  |---|---|
  | pr_created | fakegitea recorded ≥1 PR for the repo (ground truth on the remote) |
  | tests/dup/untouched/gofmt | the **branch the PR points at** is checked out of the origin and run through the same four edit-quality checks as `run.sh` |

`clean` = a real PR was observed **and** its branch carries a correct refactor.
`branch_pushed` is recorded as a diagnostic. Results append to
`results/<lane>-flow.csv`; failed rounds are copied to `results/failed-*-flow-*`.

The deterministic half lives in `cmd/agent/flow_bench_stub_test.go`: offline
`inference_stub` scenarios that pin the loop behavior the flow rides — including
the **"agent stops before create-pr"** early-stop this benchmark exists to
catch. That failure scenario is the fixed target the Phase-2 mechanisms (M6
todo/re-anchor, etc.) must flip from red to green.

## Usage

```sh
LITELLM_KEY=... ./run.sh qwen 10            # edit-only, 10 rounds, qwen lane
LITELLM_KEY=... ./run.sh qwen-reserved 10

LITELLM_KEY=... ./run-flow.sh qwen 10       # full issue→PR flow (P2.0)
LITELLM_KEY=... ./run-flow.sh qwen-reserved 10
```

## Bar and baseline

Target (harness spec, after M1–M5 land in Phase 2): **≥ 8/10 clean**.
Historical baseline: ~0/2 — measured against the Qwen 3.6 35B-A3B MoE, which
was swapped off the fabric 2026-07-17 for weak agentic work.

> **⚠️ RETRACTED 2026-07-24 — the numbers below are invalid.** The fixture had
> been silently solved-in-place (see commit 7cda406; a broken-exec agent run,
> exec running in the wrong cwd, refactored the real `fixture/metrics.go`, swept
> into a commit by `git add -A`). An already-refactored fixture passes all four
> checks trivially every round, so this "10/10" measured nothing. Fixed: exec
> workspace-rooted (#377), fixture restored to the 14-duplicated before-state, a
> pre-flight guard added. Re-baseline pending a valid run.

**Measured baseline, 2026-07-23 (INVALID — see retraction above)** (10
rounds per lane, all four mechanical checks required):

| lane | model | clean | round duration |
|---|---|---|---|
| `qwen` | Qwen3.6-27B dense (agent-host R9700) | **10/10** | 90–411s |
| `qwen-reserved` | Qwen3.6-27B dense + MTP (agent-host R9700) | **10/10** | 10–61s |

The D-AUTONOMY #2 verification bar is met with headroom on both lanes; the
dense-27B swap appears to have resolved the reliability gap the harness spec
was written against. Caveat, stated honestly: this is one task archetype (the
canonical refactor), scored **edit-only**. The M2–M7 mechanisms and nightly
per-model runs remain the plan for breadth; this baseline says the builders are
fit to carry real skeleton work now.

### Full-flow bar (Phase 2 gate)

The edit-only 10/10 above does **not** carry over to the full flow: the
Phase-1 live dogfood showed the same builder ending `no_pr` on roughly half its
runs once git/PR was in the loop. `run-flow.sh` is the instrument that measures
that. The Phase-2 reliability gate is **≥ 8/10 clean full-flow completions** on
both qwen lanes (from a ~50% baseline), and it is the precondition for the
skill-dispatch track. The batched live baseline + post-mechanism numbers land
here as they are measured.
