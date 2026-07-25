# Phase 4 Execution Spec — The Steward

**Status:** Draft, produced at the Phase 3 boundary, grounded in the code on
`main` as it now exists. Checkpoints (stop-and-ask) called out inline. Companion
to `HIRDFORGE_V2_PRD.md` (Phase 4), `OBSERVABILITY_CONTRACT.md`, and
`LOOP_SPEC.md`.

## Goal — plan mode, then a blessed handoff

**Corrected 2026-07-25 after Kit clarified the role.** The first draft built a
prose-to-issue translator: single-shot, no tools, on the local worker model. That
is too small. The Steward is the **main chat interface** — the place you think
with the best model available, investigate, and turn ideas into action.

The shape, borrowed from what KWS prototyped (OWUI chat on the deep lane →
narrowed to a plan → handed off to omniagent) and from plan mode in tools like
OpenCode:

1. **Chat freely** with a big model. Casual, exploratory, tool-assisted: it can
   read the repo, query task records, and dig into a question with you.
2. **Converge on a plan.** The Steward proposes concrete work — what to change
   and how you would know it is done.
3. **You bless it.** An explicit human action, not a model decision.
4. **Handoff to Cortex.** The blessing files the issue(s); from there the
   deterministic router does what it always does.

The blessing is the whole point. Conversation has **no side effects** — the
Steward cannot file anything by deciding to. Work is created only when the
operator approves a plan, which is both what Kit asked for and stronger
doctrinally than the first draft: not "the model decided to create work" but
"the operator approved a plan and the system executed it deterministically".

## The doctrine question, answered up front

A Steward is an LLM, and the charter forbids models in the coordination path. It
stays safe because:

- **Chat is inert.** A conversational turn changes nothing. There is no path from
  model output to a state change without a human blessing.
- **Its tools are read-only.** Investigation means reading the repo and querying
  the observability surface — the reviewer's proven "physically cannot mutate"
  pattern (O-PROFILE), not a promise to behave.
- **The blessing is mechanical.** It takes an agreed plan and calls the same
  `createLabeledIssueAndRoute` path the operator's manual dispatch uses. Cortex
  then routes deterministically, as for any other issue.

## Task 0 — the contract (STOP-AND-ASK) — §7 revised

§7 was approved for the first design; plan mode changes its shape, so the
revision is called out here rather than slipped in:

| Endpoint | Purpose |
|---|---|
| `POST /api/v1/steward/chat` | One conversational turn. **No side effects.** Response `{reply, plan?}` — `plan` is a *proposal*, not an action. |
| `POST /api/v1/steward/handoff` | **The blessing.** Body `{session_id, plan_id}`. Files the issue(s) and returns `{created_issues:[...]}`. The ONLY writing endpoint. |
| `GET /api/v1/steward/sessions/{id}` | Turn + plan history, so a UI can rehydrate the thread. |

Invariants: chat never writes; only an explicit handoff does. The Steward has no
operator verbs (dispatch/retry/cancel/approve/merge). Issues it files are
attributed to the Steward and the session. Status answers project the
observability surface, never recollection.

## Architecture — a persistent agent, not gateway code

*(Also corrected: the first draft put the turn engine inside the gateway. Wrong
home.)* The Steward runs as a **persistent agent deployment** — `cmd/agent` in
its existing server mode, like the chuck/ragnar/freya pods — with a `steward`
profile. Reasons:

- **It needs a real tool loop** to investigate, which is what the agent runtime
  already is. The gateway is privileged infrastructure, not an agent runtime.
- **Its own model.** The Steward points at the **deep lane** (the biggest model
  available), independent of the local worker the builders use. Persistent agents
  already do this (ragnar runs on an external API).
- **No security widening.** `gateway-policy` already permits egress to agents on
  `:8081`, and agents already have their own inference egress. The gateway-hosted
  design would have required opening the gateway — the most privileged
  component — to the inference fabric.

The gateway's `/steward/*` endpoints become a **thin proxy** to that agent, so
the §7 surface (and the Phase 5 UI built on it) is unchanged by where the model
actually runs.

## Tasks (ordered)

**P4.1 — The `steward` profile.** `config/profiles/steward.yaml`: read-only
investigative tools (`read`, `git-diff`, `list-issues`, `get-issue`) plus the
plan/handoff path — **no** edit/write/exec/git-commit/create-pr/merge. Enforced
at load like the reviewer's. *Acceptance: a steward-profile agent provably cannot
mutate code or the lifecycle (tool-registry test).*

**P4.2 — Plan mode.** The conversational loop with plan proposal; chat produces
`{reply, plan?}` and nothing else. *Acceptance: no conversational turn, however
phrased, creates an issue.*

**P4.3 — The blessing.** `POST /steward/handoff` turns an agreed plan into
issue(s) via `createLabeledIssueAndRoute`. *Acceptance: a blessed plan produces a
real labeled issue that Cortex routes to a builder; an unblessed plan produces
nothing.*

**P4.4 — Status projection.** "What's running?" / "why did X fail?" answered from
`/tasks` + `/log`, citing ids and quoting the mechanical reason verbatim.
*Acceptance: the answer matches the task record exactly; an unknown task is
answered "I don't have that", never invented.*

**P4.5 — Deployment.** A `steward` agent deployment on the deep lane, and the
gateway proxying `/steward/*` to it. *Acceptance: a live conversation
investigates, proposes a plan, and on blessing drives a real build.*

## Explicitly NOT in Phase 4

The pipeline UI (Phase 5 — this phase ships the endpoint the UI will consume, not
the UI). The researcher role (Phase 6). Any widening of D-CONTROL: the Steward
gains no operator verbs. Multi-user auth/identity for sessions.

## Phase acceptance (the whole phase)

1. A conversation with the Steward creates a real issue that drives a real build,
   end to end, with `created_by: steward` on the task record.
2. Status queries answer from the observability surface and cite task ids; no
   invented state.
3. A vague request yields a clarifying question, not a guessed issue.
4. The steward profile provably cannot mutate code or the lifecycle.
5. Existing tests stay green; every mechanism ships stub-bed scenarios.

## Next-phase draft

At Phase 4 acceptance, draft `PHASE5_EXECUTION_SPEC.md` — the pipeline UI,
wireframed against the (now Steward-inclusive) observability contract.
