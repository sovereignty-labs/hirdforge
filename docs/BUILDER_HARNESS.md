# Hirdforge v2 — Builder Harness (agent tool-loop reliability)

**Status:** Working doc. Summarizes `docs/source/builder-harness-spec.md`
(2026-06-10) and maps its seven mechanisms to what is **already built** vs.
**remaining**, per a code audit of `cmd/agent` (2026-07-18). The mechanisms ride
Phases 1–3 as small, test-first PRs against the deterministic stub-inference bed.

## Why this exists

Held constant on model, quant, and Anvil endpoint, local models (incl. Qwen 3.6
35B-A3B MoE) complete coding tasks reliably under OpenCode but under-perform in
our agent loop. Every difference is *harness*, not model. The spec reimplements
OpenCode's reliability mechanisms **natively from observed behavior** — no code
ported, no attribution obligations — cross-validated against Claude Code. The goal:
match OpenCode's reliability, then exceed it with instruments OpenCode lacks (the
stub-inference test bed, per-model telemetry).

**Ethical stance (binding):** mechanisms are informed by OpenCode's/Claude Code's
*observed behavior*; implementation is native (Go, our tools, our architecture);
growth is driven by our own telemetry. We do not port code.

## Status map (code audit, 2026-07-18)

| Mechanism | What it is | Status | Evidence |
|---|---|---|---|
| **Stub-inference test bed** | Deterministic agent-loop verification without live models | **BUILT** | `inference_stub`, used by 10 agent test files |
| **M1 — Loud/structured exits** | Every loop exit has a structured reason; abnormal exits get a model-authored summary | **PARTIAL** | Structured reasons BUILT (`result_outcome.go`, `termination.go`, `termination_test`). The spec's **abnormal-exit summary is NOT built**: no synthetic tools-disabled final message making the model state why it stopped / what it did / what remains / next step. `todo_remain` is only a log field, not that report. Blocks M6's "summary carries the todo verbatim" |
| **M2 — Coached error results** | Malformed calls / errors continue the loop with coaching (static + optional HEAT lessons through one seam) | **PARTIAL** | `skills.go`; git-commit "no changes" coaches + fires an M6 wobble (P2.1); repo-not-found now names the repos actually present, and mutating shell git is redirected to the verified tool (`RepoNotFoundMessage`, `ExecGitRedirectPrefix`, P2.3); general tool-result coaching seam still open (P2.2) |
| **M3 — Edit replacer cascade** | Multi-strategy edit match (start at 3) + rescue telemetry; ambiguity fails with coaching, never guesses | **ABSENT** | no replacer/cascade code found |
| **M4 — Context budgets & read discipline** | Read/exec budgets, line numbers, read-before-edit enforcement | **ABSENT** (audited 2026-07-24; was optimistically "PARTIAL") | `read` has only a 1MB guard — **no** line numbers, **no** offset/limit paging, **no** truncation markers, **no** exec head+tail budget, **no** read-before-edit or staleness invalidation. This is the mechanism aimed squarely at the benchmark task's own failure mode ("died behind a wall of grep/sed output") |
| **M5 — Procedural prompt + model routing** | Persona (was Soul) → operating procedure; per-model-family prompt routing | **PARTIAL** (P2.3) | BUILT: routing (`model_template.go` + `templates/*.txt`); procedure (`procedure.go`) Orient→…→Deliver pinning git-commit→create-pr + the four terminal outcomes, builders only; `procedure_test`. MISSING: spec step 7 output discipline — **`<system-reminder>` = harness guidance, not user input** (undercuts M6, whose vehicle is system-reminders); procedure lives in Go, not the git-reviewed personas/skills repo; ≤15-line persona applied only to the bench soul, not live personas; no A/B vs the old soul |
| **M6 — Externalized plan (todo)** | Todo tool + re-anchoring | **PARTIAL** (P2.1) | BUILT: `todo.go` (session-scoped plan, re-anchor on cadence + wobble, fail-open terminal gate extended to task mode), `todo_test`, `m6_stub_test`. MISSING: re-anchor "always after abnormal recovery" (`tool_recovery` unhooked); M3-rescue wobble trigger (M3 absent); session-report persistence (in-memory only); telemetry (todo usage rate / correlation with completion); spec stub scenarios — cadence re-anchor untested (only wobble) and "abnormal exit carries the list" unasserted; depends on M1's unbuilt abnormal-exit summary. Deploy TODO: add `todo` to the live builder bundle tools |
| **M7 — Auto-compaction** | Sliding window + summarization (partly obviated by M4/M6) | **ABSENT** | no agent-side compaction |

**Read:** the *hard scaffolding is done* — the tool set (`read/write/edit/exec/
message/delegate`), M1 structured exits, and the deterministic stub bed (the
expensive part) all exist. What remains is the local-model *reliability* layer:
M3, M6, M7, and completing M2/M4/M5. These are exactly what the spec scopes as
small test-first PRs with a benchmark gate.

**Benchmark status (2026-07-23):** the standing benchmark now exists in-repo
(`bench/builder/`) and the baseline was re-measured on the current fabric:
**qwen 10/10 clean, qwen-reserved 10/10 clean** — the ≥8/10 bar met *before*
M2–M7, on the dense Qwen3.6-27B lanes. The spec's ~0/2 was against the 35B-A3B
MoE, swapped off the fabric 2026-07-17 for weak agentic work — strong evidence
for the spec's MoE hypothesis (§5). M2–M7 remain scheduled for breadth and
telemetry, no longer as the gate to using builders at all.

## Sequencing into the phases

Per the spec's own order (§7), folded into the v2 phases:

1. **Phase 1 (skeleton):** M1 is already load-bearing for the skeleton (structured
   completion + failure context feed the mechanical gate and D-LESSONS #2). No new
   mechanism required to prove the loop — but the skeleton's first real dogfood run
   is M1's first report on where the loop actually breaks.
2. **Phase 2 (skill dispatch):** M2 (coached errors), M3 (replacer cascade + rescue
   telemetry), M4 (budgets/read-before-edit), M5 (procedural prompt + model
   routing). Each a small PR with stub scenarios.
3. **Benchmark gate:** the spec's 14-function-refactor benchmark ≥8/10 clean
   completions (from ~0/2 today) before proceeding. This is the "local models are
   now reliable" proof.
4. **Phase 3+:** M6 (todo), M7 (compaction), then the reviewer profile, then the
   profile abstraction.

## The v2 amendment (from the spec)

Bundles carry a harness **profile** alongside skills; the dispatch payload gains
`profile`; the agent is "a generic loop engine configured per-task by a harness
profile." This folds cleanly into the `cortex.yaml` bundle schema
(O-ROUTING-SCHEMA): a bundle is `{skills, memory_scopes, profile}`.

## Success criteria (from the spec)

- Zero silent loop exits (every exit auditable).
- Benchmark: 14-function refactor ≥8/10 clean completions in realistic mode.
- Fumbles absorbed not fatal (malformed calls / imprecise edits continue with
  coaching; absorbed-fumble telemetry flowing).
- The MoE question answered with per-model data from nightly runs.
- One coaching seam, two sources (static + HEAT lessons).
- Persona shrinkage: identity prose ≤ ~15 lines; the rest is procedure.
- No regression: existing agent tests stay green throughout (CI-enforced).

See `docs/source/builder-harness-spec.md` for the full mechanism designs and
verification scenarios.
