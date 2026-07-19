# Valhalla Builder Harness Spec v1 — Harness Parity

**Purpose:** Make valhalla-agent's tool loop as reliable for local models as OpenCode's demonstrably is — same models, same Anvil endpoints, same tasks. Then exceed it, using instruments OpenCode doesn't have (the HEAT Lesson Ledger, the stub-inference test bed). This spec defines seven mechanisms, their requirements, their verification scenarios, and the order they ship.

**Provenance & method:** Requirements derive from a source reading of OpenCode (sst/opencode, 2026-06-10) — the harness in which Kit's local models, including Qwen 3.6 35B-A3B MoE, complete coding tasks "almost every time," while the same weights under-perform in valhalla-agent. Since model, quant, and endpoint are held constant, every reliability difference is harness. **Method rule for all implementation work: mechanism informed by OpenCode's observed behavior; implementation native to valhalla-agent (Go, our tools, our architecture); growth driven by our own telemetry.** We do not port code. The deliverable from the source reading is the catalog of failure cases each mechanism must handle — captured in §1.

**Sequencing:** Follows the Testing/Validation/Autonomy implementation (Phases 0–2 minimum; Phase 3's stub-inference harness is this spec's verification substrate). M1 absorbs and extends the already-drafted turn-under-run instrumentation PR.

**Companion docs:** `hirdforge-testing-validation-autonomy-spec.md` (stub harness, e2e, autonomy levels), `heat-lesson-ledger-spec.md` (reactive lesson injection — shares a seam with M2), `hirdforge-v2-architecture-spec.md` (amended by §6).

---

## 1. Evidence Base — What the Source Reading Established

The prior attempt extracted "OpenCode uses a minimal system prompt" and applied it as "remove structure." That conclusion was factually wrong and directionally backwards:

1. **The default prompt is ~95 dense lines, model-routed.** Separate prompts per model family (GPT/Gemini/Claude/Kimi/Codex/default). Minimal in identity, lore, and protocol; maximal in operating procedure: an explicit task algorithm (search → implement → verify → run lint/typecheck), convention rules with worked examples, tool policy, output discipline. The structure was never removed — it was relocated from prose-about-identity into procedure-about-work.
2. **Edits are forgiven mechanically.** The edit tool tries ~9 fallback replacement strategies in sequence (exact, line-trimmed, block-anchor, whitespace-normalized, indentation-flexible, escape-normalized, trimmed-boundary, context-aware, multi-occurrence). A quantized model's slightly-off `oldString` is absorbed, not failed.
3. **Nothing exits silently.** Step cap reached → a loud instruction is injected: tools disabled, model MUST produce a summary of done / remaining / next. Doom-loop detection routes through an intervention point (permission system), not silent mid-stream truncation. Malformed tool arguments become an `invalid` tool whose *result* tells the model what was wrong; the loop continues.
4. **Errors coach.** "Found multiple matches for oldString. Provide more surrounding context to make the match unique." Tool results are treated as prompts that steer the next action.
5. **Context is a managed resource.** Reads capped (50KB / 2000 lines, long lines truncated with explicit markers), line-numbered, offset-pageable; edit requires a prior read; token usage counted per turn against real model limits with a reserved buffer; on overflow the session auto-compacts (summarize + continue) instead of degrading.
6. **The plan is externalized.** A todo tool persists structured task state outside the context window and surfaces it back.
7. **The harness adapts to the model**, not vice versa: prompt routing, provider retry machinery, reminders injected as structured synthetic message parts the prompt teaches the model to recognize (`<system-reminder>` convention).

**Mapping to our reproduced failures:**

| Valhalla failure (observed) | Missing mechanism |
|---|---|
| Turn under-run: silent loop exit ~20 calls into a 14-function refactor, no reason logged | M1 (loud exits) + M6 (todo) + M7 (compaction) |
| Repetition detector truncates mid-stream via plain `log.Printf`, turn just ends | M1 |
| Improvised shell git → `exit 128: 'origin' does not appear to be a git repository`, raw error, no guidance, model improvises further | M2 (coached errors) |
| create-pr 409 thrash (pre-#283) | M2 — #283 was the first coached-error fix, done by hand; M2 systematizes it |
| MoE (35B-A3B, ~3B active) markedly worse than dense ~1/3-size models in our harness, fine in OpenCode | All — see §1.1 |

### 1.1 The MoE amplification hypothesis

~3B active parameters per token means less capacity for format precision and instruction-juggling than a dense 27B. MoEs therefore fumble more: slightly-off edit strings, occasional malformed arguments, drift under context clutter. In a forgiving harness, fumbles are absorbed → MoE ≈ dense. In a brittle harness, every fumble is a hard failure, failures compound across a long task → the lower-active-capacity model falls off the cliff first. **The harness gap is an amplifier, and MoEs sit closest to the edge.** Testable: the stub/realistic eval modes (autonomy spec Phase 3 + nightly) measure fumble rate per model per mechanism. Prediction: dense-vs-MoE gap shrinks dramatically as M1–M4 land. This is the data that finally settles the dense-vs-MoE question.

### 1.2 Cross-validation (2026-06-10, post-spec)

The mechanism catalog was checked against Anthropic's harness (publicly documented Claude Code behavior + the claude.ai tool environment, observed firsthand). Result: near one-for-one corroboration — read-before-edit enforcement, middle-elision truncation, todo externalization, auto-compaction, harness-injected reminder parts with prompt-taught handling, skills as mandatory per-task procedural files. Three independently built harnesses (OpenCode, Claude Code, claude.ai) converge on this catalog. One deliberate divergence: Anthropic's edit tool is strict (exact + unique match, fail-and-coach) where OpenCode's is forgiving (multi-strategy fallback). The split tracks target-model capacity — strictness is cheap for frontier models, forgiveness is essential for low-active-capacity locals — which independently confirms both §1.1 and this spec's choice of the forgiving branch with strictness's no-guessing rule retained (M3: ambiguity always fails with coaching, never guesses).

---

## 2. Design Principles

1. **The harness is an active partner.** It adapts to, forgives, coaches, re-anchors, and compresses for the model. Model imprecision is a design input, not a model defect (Rule #1, structurally encoded).
2. **Structure lives in mechanism and procedure, not identity prose.** Soul files shrink toward v2's ~10-line base SOUL; everything else is procedural prompt (M5), tool design, and loop policy.
3. **Telemetry drives growth.** Every forgiveness mechanism records when it fired and what it absorbed. We build the next strategy when our data demands it, not because someone else's harness has it.
4. **No silent failure, anywhere in the loop.** Every loop exit has a structured reason AND a model-visible final turn. Every absorbed fumble is logged. (Decision 0009 applied to the runtime itself.)
5. **Tool results are prompts.** Error strings are authored with the same care as system prompts — they are the highest-leverage prompt surface in the loop because they arrive at the exact decision point.
6. **One mechanism, two coaching sources.** Static coaching (harness-authored, universal mechanics) and learned coaching (HEAT `/lessons/match`, fleet-specific) flow through the same tool-result seam (M2). This is where our harness exceeds the one it learned from.

---

## 3. The Mechanisms

Each mechanism: requirement (failure cases it must handle) → valhalla-native design → telemetry → stub scenarios (verification) → DONE WHEN. Stub scenarios are fixtures for the `pkg/stubinfer` harness (autonomy spec Phase 3); each mechanism PR ships with its scenarios.

### M1 — Loud Exits (extends the drafted instrumentation PR)

**Requirement.** No tool-loop exit may be silent. The exits to cover: round-cap reached, repetition detected, inference error, context canceled, no-tool-calls (natural completion), overflow (post-M7). Today, round-cap and repetition both end the turn with nothing — the reproduced under-run.

**Design.**
- Structured `tool_loop_exit` JSON log with `reason` at every exit path (the drafted PR, unchanged).
- For *abnormal* reasons (round_cap, repetition, overflow): before ending, inject a final synthetic user message — tools disabled — instructing the model to produce: (a) statement of why it's stopping, (b) what was accomplished (files touched, commits made, branch state), (c) remaining work as a checklist, (d) recommended next step. That text becomes the agent's report to the gateway instead of silence.
- Repetition detector: stop truncating mid-stream. On detection, finish the current tool round, then route to the abnormal-exit protocol with `reason: repetition`. Detection telemetry (what repeated, how many times) logged via `logJSON`, never `log.Printf`.
- The exit reason and the final summary both land in the HEAT session report's `outcome` field — an abnormal exit is *reportable evidence*, feeding ledger exposure tracking correctly.

**Telemetry.** Exit-reason counts per agent per week; abnormal-exit rate is a fleet health KPI.

**Stub scenarios.** (1) Scenario exceeding the round cap → assert summary turn occurs, report contains checklist, exit log has `round_cap`. (2) Scripted repetitive output → assert detector fires, no mid-stream truncation, summary turn occurs.

**DONE WHEN:** re-dispatching the 14-function refactor produces, at minimum, a readable account of where and why it stopped — never silence.

### M2 — Coached Error Results (the shared seam with HEAT)

**Requirement.** Every tool error result must answer "what should the model do next," not merely "what happened." Cases from our own history: git exit-128 on improvised shell commands; clone-destination-exists; create-pr 409 (now handled in-tool by #283 — the pattern, generalized); edit-target-not-found; file-not-read-before-edit (post-M4).

**Design.**
- Each tool's error paths return `{what failed} + {why, if known} + {recommended next action}`. Examples (authored per-tool during implementation):
  - exec running raw git → append: `Note: use the git-commit tool for commits and pushes; raw 'git push' lacks credentials in this workspace.`
  - edit no-match → `oldString not found. The file may differ from your last read — re-read the file and retry with the exact current text.`
- **The HEAT hook:** after composing the static result, if the tool call failed, the runtime calls `POST /lessons/match {tool, exit_code, raw_error, scopes}` and appends any rendered lesson lines to the same result. One seam, two coaching sources. Matched atoms are recorded in `atoms_injected` (ledger spec §7 obligation). If HEAT is unreachable, static coaching still ships — learned coaching is enhancement, never dependency.
- Malformed tool-call arguments (bad JSON, unknown tool, schema violation) do not error the loop: they become a synthetic tool result describing exactly what was invalid, and the loop continues. Telemetry counts these as fumbles-absorbed.

**Telemetry.** Per-tool error counts; malformed-call counts per model (primary input to the MoE hypothesis measurement); lesson-match hit rate.

**Stub scenarios.** (1) Scripted malformed tool call → assert loop continues with coaching result. (2) Scripted exec git failure → assert result contains the redirect-to-git-commit coaching. (3) With a stub HEAT match response → assert lesson line appended to tool result and recorded as injected.

**DONE WHEN:** the improvised-shell-git failure, replayed, yields a next-turn correction to the proper tool instead of further improvisation (verified in realistic mode against inference-host).

### M3 — Edit Replacer Cascade (start at three, grow on data)

**Requirement.** Slightly-imprecise `oldString` from quantized local models must be absorbed when the intent is unambiguous, and must fail with coaching when it isn't. Initial strategy set, chosen for the dominant local-model fumbles: (1) exact match, (2) whitespace-normalized (runs of spaces/tabs collapsed for comparison), (3) indentation-flexible (leading indentation ignored per-line, re-applied from the file on replacement). Uniqueness is enforced at every strategy level — an ambiguous match fails with the M2-coached "provide more surrounding context" result rather than guessing.

**Design.**
- Strategies tried in order; first unambiguous match wins; replacement always preserves the file's actual formatting (the model's whitespace never overwrites the file's).
- **Rescue telemetry — the growth driver:** when any non-exact strategy succeeds, log `{strategy, tool, model, a diff-shaped sample of the mismatch}`. When all strategies fail, log the near-miss. This corpus, not OpenCode's strategy list, decides what strategy #4 is. (OpenCode's nine are the fossil record of *their* users' fumbles; ours grows from ours.)
- Read-before-edit enforcement arrives with M4 and tightens this further.

**Telemetry.** Rescue rate by strategy and by model — the single clearest fumble-rate signal for §1.1.

**Stub scenarios.** (1) oldString with collapsed whitespace → assert strategy-2 rescue, file formatting preserved. (2) oldString with wrong indentation → strategy-3 rescue. (3) Ambiguous oldString → assert failure with coached message, no edit applied. Table-driven Go unit tests cover the strategy functions directly (pure functions — same testing shape as HEAT's signature normalizer).

**DONE WHEN:** rescue telemetry is flowing from real dispatches and a whitespace-fumbled edit demonstrably completes instead of failing.

### M4 — Context Budgets & Read Discipline

**Requirement.** Tool output must never flood the context. The 14-function refactor died behind a wall of grep/sed output; long tasks degrade as the original instruction recedes. Cases: oversized file reads, unbounded exec output, edits against stale file knowledge.

**Design.**
- `read`: line-numbered output (`N: content` — the prompt's edit instructions teach the model the prefix is not file content), default cap ~2000 lines / ~50KB, long lines truncated with an explicit marker, `offset`/`limit` paging, truncation always announced in the result ("output truncated at line N; use offset to continue").
- `exec` and other stream tools: byte budget with head+tail retention and an explicit elision marker, so the model sees the start (command echo, early errors) and the end (exit status, final lines).
- **Read-before-edit + staleness invalidation:** edit fails with a coached result if the file hasn't been read this session, has changed since last read (mtime/hash), or has been edited since last read — a successful edit marks all prior reads of that file stale, and the next edit requires a fresh read. This kills the cascade failure where edit #2 targets text edit #1 just changed — the exact multi-edit sequence the benchmark refactor is made of. Forces fresh ground truth — directly reduces M3's workload. (Cross-validated: Anthropic's harness enforces the same staleness rule.)
- Budgets are config, not constants; tuned from telemetry, not guessed.

**Telemetry.** Truncation frequency per tool; context tokens consumed per task (the input M7 needs).

**Stub scenarios.** (1) Read of an oversized fixture → assert cap + marker + working offset paging. (2) Edit without prior read → coached failure. (3) Huge exec output → head+tail shape with elision marker. (4) Two sequential edits to the same file with no interleaved read → second edit fails with a coached re-read message.

**DONE WHEN:** no single tool result can exceed its budget, and every truncation is model-visible.

### M5 — Procedural Prompt (soul → operating procedure)

**Requirement.** Replace identity-and-rules prose with a builder operating procedure, per model family. The warrior's lean soul (PR #99) was the right instinct; M5 completes it: minimal identity (v2's ~10-line base SOUL), maximal procedure.

**Design.** Builder procedure skeleton (final text authored at implementation, with worked examples in the OpenCode style):
1. **Orient** — read the task; check the todo list (M6); clone if needed; read AGENTS/ARCHITECTURE docs if present.
2. **Survey before changing** — read the code around every planned change; mimic existing conventions; never assume a library exists, verify it's already used.
3. **Plan** — for multi-step work, write the todo list first; one logical change per commit.
4. **Implement** — read before edit; prefer edit over write; run `go build ./...` (or the project's equivalent) after each logical change.
5. **Verify** — run tests; run lint/typecheck if the project provides them.
6. **Deliver** — branch, commit referencing the issue, create-pr tool (never raw git), report PR number and branch.
7. Output discipline: act through tools, don't narrate; keep text terse; `<system-reminder>` parts are harness guidance, not user input.

Prompt routing by model family (Qwen / MiniMax / dense-vs-MoE variants as evidence demands) selected by the runtime from the model string — the harness adapts to the model. Prompt files live in the personas/skills repo so they're git-reviewed like everything else; this is also where v2 skill files will append their lesson sections (HEAT §8 promotion target).

**Telemetry.** None direct — M5's effect shows up in every other mechanism's numbers. A/B against the current soul on the benchmark task (§7) is the measurement.

**DONE WHEN:** the builder runs on procedure + base SOUL only, and benchmark completion rate does not regress (expected: improves).

### M6 — Externalized Plan (todo state)

**Requirement.** Multi-step tasks must not depend on the model holding the full checklist in attention across a long context. The 14-function refactor is the canonical case.

**Design.**
- `todo` tool: model writes/updates a structured list `{content, status: pending|in_progress|completed}`; runtime persists it per session (in-memory + session report; no new service).
- Re-anchoring: the current todo list is re-surfaced to the model as a synthetic `<system-reminder>` part at a cheap cadence (e.g., every N tool rounds, and always after an abnormal recovery) — and escalates on wobble signals the telemetry already collects: a repetition near-miss, an M3 rescue, or two consecutive tool failures trigger an immediate re-anchor. Re-anchor hardest exactly when the model is drifting; the goal stays pinned no matter how much grep output has flowed past. (Cross-validated: Anthropic's harness injects correctives conditionally on detected drift, not only on cadence.)
- The procedural prompt (M5) instructs: tasks with >3 steps start by writing the todo list; mark items completed as you go.
- M1's abnormal-exit summary includes the todo list verbatim — the "remaining work" section writes itself, and a re-dispatch can resume from it.

**Telemetry.** Todo usage rate on multi-step tasks; correlation of todo usage with completion (the direct test of this mechanism's value).

**Stub scenarios.** Scripted multi-step scenario → assert reminder part appears at cadence; assert abnormal exit carries the list.

**DONE WHEN:** the benchmark refactor's dispatch shows the model writing, following, and completing a todo list, and abnormal exits enumerate remaining items.

### M7 — Auto-Compaction (last, and partially obviated by M4/M6)

**Requirement.** A session approaching the model's real context limit must compress and continue rather than degrade or die. Shipped last deliberately: M4 budgets and M6 re-anchoring reduce how often this fires, and its trigger needs M4's token telemetry.

**Design.**
- Token accounting per turn against the model's configured limits with a reserved output buffer.
- On threshold: summarize the conversation so far (task, decisions, files touched, current todo state — this IS an LLM call, and that's fine: summarization is LLM work), replace the elided history with the summary as a synthetic part, continue the loop. Log `compaction` event; count toward telemetry, never a silent operation (principle 4).
- Compaction failure (summary call errors) degrades to the M1 abnormal-exit protocol with `reason: overflow` — still never silent.

**Telemetry.** Compactions per task; post-compaction completion rate.

**Stub scenarios.** Low artificial limit + long scripted scenario → assert compaction occurs, task continues, summary contains todo state.

**DONE WHEN:** a task that previously exceeded usable context completes across a compaction boundary in realistic mode.

---

## 4. Harness Profiles — defined now, extracted later

A **profile** is the mechanical configuration of the loop: tool set, budgets, step caps, exit protocol, procedural prompt, loop policies (todo on/off, compaction on/off). Skills are knowledge; profiles are machinery. v2's bundles will carry both: Cortex dispatch becomes `{skills, memory_scopes, profile}`.

Anticipated profiles (illustrative, NOT to be built yet):
- **builder** — full tool set, M1–M7, "PR created" completion.
- **reviewer** — read/grep/glob/review-submit only (no edit/write/exec-mutating: a reviewer that cannot modify code cannot be prompt-injected into modifying it — a security property), smaller step cap, "review submitted" completion, review procedure prompt.
- **validator** — health/smoke/report tools, minimal caps.

**Discipline rule:** build the builder first; extract the profile abstraction only when the reviewer is built and the genuinely shared seams have revealed themselves from two working examples. Abstracting from one example designs the wrong seams. The builder implementation should merely avoid *hard-coding* what is obviously profile-shaped (tool registry, budgets, prompt selection are already config-adjacent).

---

## 5. Verification & Measurement

- **Stub-first:** every mechanism ships with its `pkg/stubinfer` scenarios (autonomy spec Phase 3); gate decisions use deterministic mode only.
- **The benchmark task:** the 14-function pure-helper extraction refactor — our reproduced failure — becomes the standing acceptance benchmark. Target: ≥8/10 clean completions in realistic mode (inference-host, Qwen 35B-A3B) after M1–M5, from the current 0/2. Decision 0009: the original failure mode, exercised, observed fixed.
- **MoE hypothesis measurement:** nightly realistic runs record fumble metrics (malformed calls, edit rescues, repetition events) per model. Prediction on record: the dense-vs-MoE completion gap shrinks materially as M1–M4 land. If it doesn't, the hypothesis is wrong and we've learned something real about the models.
- **Fleet KPI:** abnormal-exit rate and fumbles-absorbed rate per week, alongside the autonomy report.

---

## 6. v2 Architecture Spec Amendment

One change to `hirdforge-v2-architecture-spec.md`: bundles carry a harness **profile** in addition to skills; the dispatch payload adds `profile`; §3 (Generic Warriors) gains the sentence "warriors are a generic loop engine configured per-task by a harness profile, per the Builder Harness Spec." Everything else in v2 stands. (Edit deferred until v2 work begins, same as the §4.4 pointer.)

---

## 7. Implementation Sequence

All in kit/hirdforge `cmd/agent` (+ small `pkg/` additions), small PRs (decision 0008), each with its stub scenarios. Prerequisites: autonomy Phases 0–2 done (CI runs tests — these PRs are test-heavy by design); Phase 3 `pkg/stubinfer` lands with or immediately before M1's scenarios.

1. **M1** — absorbs the drafted instrumentation PR; structured exits + abnormal-exit summary turn. *Also finally names the under-run's mechanism on first re-dispatch.*
2. **Under-run root-cause fix** — whatever M1's first report reveals (bump cap / fix detector / decompose), verified by the benchmark task per decision 0009.
3. **M2** — coached errors + malformed-call absorption. HEAT `/lessons/match` hook lands here but ships dark until Ledger Step 3 exists; static coaching works standalone.
4. **M3** — replacer cascade (3 strategies) + rescue telemetry.
5. **M4** — read/exec budgets, line numbers, read-before-edit.
6. **M5** — procedural prompt + model routing; A/B on the benchmark.
7. **M6** — todo tool + re-anchoring.
8. **Benchmark gate:** ≥8/10 on the benchmark task before proceeding.
9. **M7** — compaction.
10. **Reviewer profile** (separate effort, after the above) → then extract the profile abstraction (§4).

---

## 8. Success Criteria

1. **Zero silent loop exits** — every exit has a structured reason and, for abnormal exits, a model-authored summary. Auditable from logs.
2. **The benchmark holds:** 14-function refactor ≥8/10 clean completions in realistic mode (from 0/2 today).
3. **Fumbles absorbed, not fatal:** malformed calls and imprecise edits continue the loop with coaching; absorbed-fumble telemetry flowing.
4. **The MoE question answered with data:** per-model fumble and completion metrics published from nightly runs; hypothesis confirmed or refuted.
5. **One coaching seam, two sources:** HEAT lessons and static coaching demonstrably arrive through the same tool-result mechanism.
6. **Soul shrinkage:** builder runs on base SOUL + procedure; identity prose ≤ ~15 lines.
7. **No regression in the things that already work:** existing 46+ tests stay green throughout (CI now enforces this — autonomy Phase 0).

---

## 9. What This Does NOT Include

- No code ported from OpenCode — mechanisms reimplemented natively from observed behavior; no attribution obligations incurred.
- No profile abstraction yet (§4 discipline rule).
- No changes to Cortex, the gateway dispatch path, or A2A — this is entirely inside the agent runtime.
- No new services. Todo state and telemetry ride the session and the existing log/report paths.
- No dependency on HEAT availability — learned coaching enhances, never gates.
