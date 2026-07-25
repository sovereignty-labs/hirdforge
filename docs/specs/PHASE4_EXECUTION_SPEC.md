# Phase 4 Execution Spec — The Interlocutor (working name: "Steward")

**Status:** Draft, produced at the Phase 3 boundary, grounded in the code on
`main` as it now exists. Checkpoints (stop-and-ask) called out inline. Companion
to `HIRDFORGE_V2_PRD.md` (Phase 4), `OBSERVABILITY_CONTRACT.md`, and
`LOOP_SPEC.md`.

## What this actually is (Kit's framing, 2026-07-25)

**A working session with a smart model — not a ticket funnel.** Think of talking
to Claude or ChatGPT. Sometimes it is all talk. Sometimes talk with an occasional
light task. Sometimes a session that runs for hours against a giant codebase. The
platform's job is to **facilitate that conversation for as long as we can help the
model hold it together**, and let it deploy work to Cortex and the agents when work
is warranted.

The plan card is not the main event — it is what appears *when* the conversation
produces work. Most turns produce none.

Three earlier drafts were too small (a prose→issue translator; then a
single-plan→single-issue handoff). The corrections that define this phase:

1. **It is the main agent the user interfaces with.** The place you think with the
   best model available, investigate, and turn ideas into action.
2. **"Steward" is a stand-in name, user-replaceable.** Identity is configuration,
   not code (the mock's byline said "Concierge"; personas are already a repo
   pattern). Nothing in the surface hardcodes the word "Steward".
3. **It facilitates; it does not do the real action itself.** The big model is
   *too slow* to be the hands. It is only ever the head: talk, converge, bless,
   hand off. All real action goes to **Cortex and the agents we already built.**

## The three shapes a turn can take

The interlocutor chooses the *shape of execution* per request (LOOP_SPEC move 5 —
"work happens directly when small, and fans out to parallel workers when scale
helps"):

| You ask | It does |
|---|---|
| a question | **answers in chat** — no tasks, no ceremony |
| a small one-off | **calls up one coder** — like handing OpenCode a task |
| something big | **fan-out** — Cortex distributes to one agent or many, per how the work decomposes |

Collapsing all three into "plan → issues" was the core mistake. A question must
not incur heavyweight ceremony.

## Two task shapes (not everything is a PR)

A one-off is "like having OpenCode do a task." In hirdforge the goal is **often** a
PR — but not always. **Lockbox exists for the other kind**, typically simpler
operational tasks. Both are already expressible via D-GATE:

| Shape | Path | Gate |
|---|---|---|
| **Code** | agent → sandbox → PR → CI | `ci-status` / `test-command`, then your merge |
| **Operational** | action → Lockbox approval | `custom-validator` (e.g. `secret exists`) |

The mock proves it: plan step 1 was *"Issue cert from example-internal-ca · gate:
secret exists"* — no PR anywhere. I had been thinking only in PRs; D-GATE already
supports both.

## Plan mode — the felt loop

Borrowed from what KWS prototyped (OWUI chat on the deep lane → narrowed to a plan
→ handed to omniagent) and from plan mode in tools like OpenCode:

1. **Ground before planning.** The interlocutor reads live reality first
   (LOOP_SPEC move 2) — repo state, task records, the cluster — and distrusts
   stale docs. The mock demonstrates it correcting a stale DNS assumption from live
   state before proposing.
2. **Chat freely,** tool-assisted, for as long as the model can hold the thread.
3. **Converge on a plan** — *multi-step*, each step proposing its own done-gate.
4. **You bless it** (or **revise** — a first-class action, not a restart). Blessing
   is an explicit human act, never a model decision.
5. **Handoff to Cortex** — files the issue(s) for the blessed steps and lets the
   deterministic router do what it always does. Steps marked `needs_operator` are
   **held**, not dispatched: dispatch is **partial**.

The blessing is the whole point. Conversation has **no side effects** — the
interlocutor cannot file anything by deciding to. Work is created only when the
operator approves, which is stronger doctrinally than "the model decided": *the
operator approved a plan and the system executed it deterministically.*

## The doctrine question, answered up front

The interlocutor is an LLM, and the charter forbids models in the coordination
path. It stays safe because:

- **Chat is inert.** A conversational turn changes nothing. There is no path from
  model output to a state change without a human blessing.
- **Its tools are read-only.** Investigation means reading the repo and querying
  the observability surface — the reviewer's proven "physically cannot mutate"
  pattern (O-PROFILE), enforced at load, not a promise to behave.
- **The blessing is mechanical.** It takes an agreed plan and calls the same
  `createLabeledIssueAndRoute` path the operator's manual dispatch uses. Cortex
  then routes deterministically. **The interlocutor is never in the coordination
  path** — it proposes; Cortex distributes; agents do; mechanical gates decide.

## Session endurance — O-M7-SCOPE reopens here

I deferred M7 (context summarization) on 2026-07-25 with the rationale that every
task so far was a short builder run. **That reasoning was about builders, and it
does not survive this role.** Kit described the interlocutor holding a session *for
hours* — "for as long as we can help the model hold it together." That **is** a
context-management requirement, and it is the interlocutor's core competency, not
an edge case.

So: the builder-side M7 deferral stands (builders are short-lived); the
**interlocutor's does not.** Session endurance — real summarization of the dropped
span plus Seidr continuity (LOOP_SPEC move 8, "nothing starts cold that shouldn't")
— is a **named Phase 4 requirement**, not the trim-and-announce shipped in P3.3.
O-M7-SCOPE is reopened with this as the evidence, rather than a silent change of
mind.

## Task 0 — the contract (STOP-AND-ASK) — §7 revised

§7 changes shape again for the multi-step plan; the revision is called out here
rather than slipped in. See `OBSERVABILITY_CONTRACT.md` §7 for the authority; the
surface in brief:

| Endpoint | Purpose |
|---|---|
| `POST /api/v1/steward/chat` | One conversational turn. **No side effects.** Returns `{session_id, reply, plan?}` — `plan` is a *proposal*: `{id, title, steps[]}`, each step `{title, detail, gate, needs_operator}`. |
| `POST /api/v1/steward/handoff` | **The blessing.** Body `{session_id, plan_id, step_ids?}`. Files issue(s) for the blessed, non-`needs_operator` steps and returns `{created_issues:[...]}`. The only writing endpoint. |
| `POST /api/v1/steward/revise` | First-class revise: amend a proposed plan in-session. Still inert — returns the updated `plan`, writes nothing. |
| `GET /api/v1/steward/sessions/{id}` | Turn + plan history, so a UI can rehydrate the thread. |

Route path stays `/steward/*` as a stable API token; the *displayed* name is
configuration (a placeholder the user can replace). Invariants: chat never writes;
only handoff does; the interlocutor has no operator verbs
(dispatch/retry/cancel/approve/merge); status answers project the observability
surface, never recollection.

## Architecture — a persistent agent, not gateway code

The interlocutor runs as a **persistent agent deployment** — `cmd/agent` in its
server mode, like the chuck/ragnar/freya pods — with a configurable profile
(default `steward`). Reasons:

- **It needs a real tool loop** to investigate; the agent runtime already is one.
  The gateway is privileged infrastructure, not an agent runtime.
- **Its own model — the deep lane** (the biggest available), independent of the
  local worker the builders use. Persistent agents already do this.
- **No security widening.** `gateway-policy` already permits egress to agents on
  `:8081`, and agents already have their own inference egress. A gateway-hosted
  model would have required opening the most privileged component to the inference
  fabric.

The gateway's `/steward/*` endpoints are a **thin proxy** to that agent, so the §7
surface (and the Phase 5 UI on it) is unchanged by where the model runs.

## Tasks (ordered)

**P4.1 — The profile.** `config/profiles/steward.yaml`: read-only investigative
tools (`read`, `git-diff`, `list-issues`, `get-issue`, plus the status projection
reads) and the plan/handoff/revise path — **no** edit/write/exec/git-commit/
create-pr/merge. Enforced at load like the reviewer's. The *display name* is a
profile field, defaulting to a placeholder, so it is user-replaceable. *Acceptance:
a profile agent provably cannot mutate code or the lifecycle (tool-registry test);
the display name is read from config, hardcoded nowhere.*

**P4.2 — Plan mode.** The conversational loop with grounding-before-planning and a
*multi-step* plan proposal; chat produces `{reply, plan?}` and nothing else. A
plan's steps each carry their own `gate` and `needs_operator`. *Acceptance: no
conversational turn, however phrased, creates an issue; a plan with a
`needs_operator` step surfaces that step held.*

**P4.3 — The blessing (partial dispatch).** `POST /steward/handoff` turns the
blessed, non-`needs_operator` steps into issue(s) via `createLabeledIssueAndRoute`
— code steps as PR-gated, operational steps as Lockbox-gated per their declared
gate. `needs_operator` steps are held. *Acceptance: a blessed plan produces real
labeled issues Cortex routes; `needs_operator` steps produce nothing; an unblessed
plan produces nothing.*

**P4.4 — Status projection.** "What's running?" / "why did X fail?" answered from
`/tasks` + `/log`, citing ids and quoting the mechanical reason verbatim.
*Acceptance: the answer matches the record exactly; an unknown task is answered "I
don't have that", never invented.*

**P4.5 — Session endurance (M7 for the interlocutor).** Real summarization of the
dropped span with the task/plan context pinned, plus Seidr continuity so a session
survives beyond one context window and does not start cold. *Acceptance: a session
driven past the context budget keeps the active plan and the grounding facts
intact (not just announced-and-dropped); `context_compacted` telemetry shows
summarization, not bare trim.*

**P4.6 — Deployment.** A profile agent deployment on the deep lane, and the gateway
proxying `/steward/*` to it. *Acceptance: a live conversation investigates,
proposes a multi-step plan, and on blessing drives a real build — with at least one
step held as `needs_operator`.*

## Explicitly NOT in Phase 4

The pipeline UI (Phase 5 — this phase ships the endpoint the UI consumes, not the
UI). The researcher role (Phase 6). Any widening of D-CONTROL: the interlocutor
gains no operator verbs. Multi-user auth/identity for sessions. Multiple named
interlocutors per user (one configurable agent this phase; a roster is a later
consideration).

## Phase acceptance (the whole phase)

1. A conversation creates a real issue that drives a real build, end to end, with
   `created_by: steward` on the task record.
2. A multi-step plan dispatches partially — non-`needs_operator` steps route,
   `needs_operator` steps are held.
3. Both task shapes work: a code step lands PR-gated, an operational step
   Lockbox-gated.
4. Status queries answer from the observability surface and cite task ids; no
   invented state.
5. A vague request yields a clarifying question, not a guessed plan.
6. The profile provably cannot mutate code or the lifecycle; the display name is
   configuration.
7. A long session keeps its plan and grounding intact past the context budget.
8. Existing tests stay green; every mechanism ships stub-bed scenarios.

## Next-phase draft

At Phase 4 acceptance, draft `PHASE5_EXECUTION_SPEC.md` — the pipeline UI,
wireframed against the (now interlocutor-inclusive) observability contract.
