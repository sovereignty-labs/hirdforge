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

## Usage

```sh
LITELLM_KEY=... ./run.sh qwen 10            # 10 rounds against the qwen lane
LITELLM_KEY=... ./run.sh qwen-reserved 10
```

## Bar and baseline

Target (harness spec, after M1–M5 land in Phase 2): **≥ 8/10 clean**.
Historical baseline: ~0/2 — but that was measured against the Qwen 3.6 35B-A3B
MoE, which was swapped off the fabric 2026-07-17 for weak agentic work. The
current baseline against the dense 27B lanes is recorded in `results/` and in
the PR that landed each run.
