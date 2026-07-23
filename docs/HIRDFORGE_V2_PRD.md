# Hirdforge v2 — Product Requirements Document (Plan of Record)

**Status:** Plan of record. Promotes `docs/source/hirdforge-v2-architecture-spec.md`
(2026-05-28) into the binding PRD, folding in the four carry-forward lessons from
Workbench and the locked build calls (see DECISIONS.md). Where this PRD and the
source spec disagree, this PRD wins; where either and the code disagree, the code
wins (flag it).

**Owner:** Kit Porath (Sovereign). **Builder:** Claude Code, under CLAUDE.md.

---

## 1. Mission

Make the local agent fleet do human-supervised autonomous build work as a single,
observable, deterministically-coordinated system — one platform that knows what
work is flowing where, decides every routing/completion step mechanically (never
by model judgment), isolates every execution, and gates every write behind a PR
and a human approval — operable by one person, running on your own hardware.

## 2. The Problem (why v1 failed, in one number)

v1's coordination was LLM-driven: a stateful "chieftain" agent managed a
multi-phase pipeline, and task completion was inferred by regex-matching an
agent's prose with a "nudge" on failure (`pkg/tasklife`). Each LLM-dependent step
succeeds ~80%; a 5-step pipeline (decompose → dispatch → build → review → merge)
is 0.8⁵ ≈ **33% end-to-end.** Meanwhile single-agent tools (Claude Code, Codex,
OpenCode) reliably finish because their pipeline is two steps: human → agent →
code → human review.

**The fix:** replace LLM-driven coordination with infrastructure-driven
coordination. Remove the chieftain. Introduce Cortex — a deterministic
event-driven router. LLMs do LLM work (think, code, review, research); they never
route, track, dispatch, or self-certify completion.

## 3. Core Principles

1. **Determinism in the coordination path.** Cortex never calls inference. Every
   routing and lifecycle decision is a rule over observable state.
2. **Mechanical completion gates.** "Done" is a CI status / test exit code /
   declared validator — per route, always mechanical (DECISIONS D-GATE).
3. **Isolated execution.** Every agent runs in a reset-to-clean sandbox; no agent
   touches live platform or operator state.
4. **PR-gated writes.** The PR is staging, CI is the gate, merge is the apply,
   Lockbox authorizes the merge. No agent writes to a protected branch.
5. **Loud escalation, no silent degradation.** Every retry/reroute/override is an
   event.
6. **The human directs.** The operator sees everything and controls dispatch,
   retry, and cancel. The Sovereign is always in command.

## 4. Component Architecture

| Component | Role | State this build |
|---|---|---|
| **Cortex** | Deterministic event router in the gateway. Webhook/dispatch events → routing rules → dispatch message → lifecycle tracking in Postgres. Replaces the chieftain + `pkg/tasklife`. | Build fresh per the v2 spec (do NOT port Workbench's cortex — different engine; see D-PORT). |
| **Agent** (was "warrior") | Generic model-driven worker. Loads a per-task skill+profile bundle, runs the tool loop in a sandbox, produces a PR. `cmd/agent` exists. | Reuse + harden the loop (BUILDER_HARNESS M1–M7). |
| **Sandbox** | Reset-to-clean isolated execution environment per task. | Native rebuild of the fast ephemeral-VM pattern (D-SANDBOX). Was scaffolded (empty `agent-testing` namespace, gVisor TODO), never built. |
| **Reviewer** | An agent dispatched with a review bundle to judge a PR — a gate *on top of* the mechanical done-gate, never instead of it. | Wire per Phase 3; PR-review endpoints exist. |
| **Researcher** | A general research/coding agent role (search/read-broadly/propose). NOT a Witness Judge in this build (D-WITNESS). | Greenfield role + tools; Phase later. |
| **Steward** (was Concierge) | Conversational front-door agent: turns operator conversation into issues. Renamed at kickoff (O-BRAND-NAMES). | Phase 4. Deferred out of the walking skeleton. |
| **Seidr** | Skill-scoped memory (RAG). Its own named component; kept. | Reuse; collection-naming convention only, no API change. |
| **Lockbox** | Human approval gate authorizing PR merge. | Unchanged. |
| **Gateway** | Hosts Cortex, the fleet registry, health, WebSocket event streaming, MCP surface, the pipeline UI. Runs on Asgard. | Reuse + add Cortex module + observability/control endpoints. |
| **Pipeline UI** | Operator view: pipeline board, Cortex log, task detail, controls. | Backend-first; deep wireframe before final UI (D-UISEQ). |

## 5. Locked Build Calls (full text in DECISIONS.md)

- **D-SKELETON — Walking skeleton first.** One agent → Cortex router →
  mechanically-gated reviewer → one PR, end to end, then widen.
- **D-SANDBOX — Native sandbox.** Rebuild the fast ephemeral-VM pattern into
  hirdforge's dispatch; no runtime dependency on the KWS/omniagent deployment.
- **D-INFRA — Single home (corrected 2026-07-23).** Git master =
  **`git.hirdforge.com`** (Gitea, hosted in Asgard): repo, PRs, webhooks, CI;
  deploys ride its existing CI/CD pipeline. Compute (gateway/agents/sandboxes/
  Postgres/Seidr) on **Asgard** k8s. Build/dev on **agent-host**. KWS wiring dropped.
- **D-WITNESS — Platform-first.** Build hirdforge fully as a general platform;
  a customized Witness adaptation is a separate downstream project. No Witness
  seams in the core.
- **D-GATE — Per-route mechanical done-gate.** Each `cortex.yaml` route declares
  `done_gate: ci-status | test-command | custom-validator`. Which mechanical
  check varies; that it is mechanical does not.
- **D-PORT — Build Cortex anew, borrow lessons not code.** Workbench's cortex is
  an in-process fan-out/aggregate/apply engine for a hostless desktop app; v2's
  is a Gitea-leaning router. Carry the four lessons (below), not the modules.
- **D-BRAND — Rebrand.** Module path → `git.hirdforge.com/kit/hirdforge`. Drop
  the Valhalla-military flavor (warrior→agent, warband→fleet, chieftain→removed);
  keep the good component names (Cortex, Seidr, Lockbox, hirdforge). Names
  resolved 2026-07-23: Seidr stays, SOUL→Persona, Concierge→Steward. Rebrand is
  Phase 0.
- **D-CONTROL — Control surface = dispatch / retry / cancel** up front. No
  pause/override/live-intervene yet (seams left, not built).

## 6. The Four Carry-Forward Lessons (from Workbench; binding — DECISIONS)

1. **Ground-truth completion** — the whole point of D-GATE. A task's/reviewer's
   "done" resolves to something mechanical, never model-judged prose. Dropping
   this resurrects the exact v1 `tasklife` failure.
2. **Escalation with failure context** — when a task retries on a fresh agent,
   the dispatch tells it *why the last attempt failed* (prior agent, reason,
   validator excerpt). Blind retry wastes a stronger attempt.
3. **A sanctioned revise path** — a reviewer "changes requested" has a defined
   route back to a builder, not a dead end. In v2 this is a PR-review-event route.
4. **The reviewer must see the artifact** — the blind-reviewer bug: a reviewer
   given only "action + path" cannot judge. Trivially satisfied when reviewing a
   PR diff, but the principle generalizes: every gated role receives the material
   it is asked to judge.

## 7. Roadmap (dependency-ordered)

- **Phase 0 — Rebrand + foundation.** Module-path rename, vocabulary rebrand,
  import source specs, confirm the build is green. No behavior change.
- **Phase 1 — Walking skeleton (THIS BUILD).** Cortex core (routing engine +
  dispatch + lifecycle in Postgres) minimal; one agent; one reviewer;
  mechanically-gated; one PR on this repo end to end. See PHASE1_EXECUTION_SPEC.
- **Phase 2 — Skill-based dispatch.** Agents load skill+profile bundles per task;
  skill-scoped Seidr memory. Fold in BUILDER_HARNESS mechanisms.
- **Phase 3 — Review & validation routing.** PR-event routes, retry-on-failure
  routing with failure context, the revise path. Full issue→build→review→merge→
  validate with zero LLM coordination.
- **Phase 4 — Steward.** Conversation → issue creation; status queries.
- **Phase 5 — Pipeline UI.** Deep wireframe against the frozen observability
  contract, then the board / Cortex log / task detail / controls.
- **Phase 6 — Researcher role + Seidr validation loop.** The general research
  agent + its tools; skill-amendment / validated-learning loop.

Sandbox hardening (D-SANDBOX) rides Phase 1 (skeleton needs isolation) and hardens
through Phase 3. Builder-harness mechanisms (M1–M7) ride Phases 1–3 as small
test-first PRs against the stub-inference bed.

## 8. Success Criteria (adapted from the source spec)

1. **End-to-end task completion >80%** (up from ~33% compound).
2. **Zero LLM-dependent steps in the coordination path** — all routing/completion
   is mechanical, provable by code inspection.
3. **New capability in <10 minutes** — write a skill file, add a route, push.
4. **The fleet learns measurably** — failure rates per skill scope tracked over
   time (Phase 6).
5. **The operator sees and controls everything** — pipeline view + Cortex log +
   dispatch/retry/cancel.
6. **`helm install` to a working platform in 15 minutes** — the Asgard-deployable
   chart is the finish line.

---

*Hirdforge v2 — coordination is infrastructure, not agency. On your hardware,
under your control.*
