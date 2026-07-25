# Phase 4 Execution Spec — The Steward

**Status:** Draft, produced at the Phase 3 boundary, grounded in the code on
`main` as it now exists. Checkpoints (stop-and-ask) called out inline. Companion
to `HIRDFORGE_V2_PRD.md` (Phase 4), `OBSERVABILITY_CONTRACT.md`, and
`LOOP_SPEC.md`.

## Goal

Give the platform a **conversational front door**: the operator says what they
want in prose, and a Steward agent turns it into a well-formed issue that the
deterministic router picks up exactly as it would any other issue — plus answers
"what's happening?" from the observability surface.

Per D-BRAND the role is **Steward** (was Concierge). Nothing of it exists yet;
this is a from-scratch build.

## The doctrine question, answered up front

A Steward is an LLM, and the charter forbids models in the coordination path. It
is safe for exactly one reason, which every task below preserves:

> **The Steward writes issues; it never decides what the system does with them.**

It has the authority of a person typing an issue — no more. It cannot dispatch,
route, gate, approve, or merge. It creates an issue through the **existing**
`POST /api/v1/cortex/dispatch` path (create labeled issue → Gitea webhook →
Cortex routes it deterministically). If the Steward hallucinates, the blast
radius is a badly-worded issue, which the operator can close — never a wrong
state transition. Status answers are **read-only projections** of the
observability contract, never the Steward's recollection.

## Task 0 — the contract extension (STOP-AND-ASK, before code)

`OBSERVABILITY_CONTRACT.md` §5 is explicit: *"If a surface needs data this
contract doesn't expose, the contract is extended here first (stop-and-ask), then
the UI consumes it."* The Steward is a new surface, so it goes in the contract
before it goes in code — and Phase 5's UI then inherits it rather than inventing
its own chat surface.

**Proposed §7 — Steward surface (conversational front door):**

| Endpoint | Purpose |
|---|---|
| `POST /api/v1/steward/chat` | One conversational turn. Request: `{session_id, message}`. Response: `{reply, intent, created_issue?}` where `intent ∈ {chat, create_issue, status_query}` and `created_issue = {repo, number, url, label}` when one was created. |
| `GET /api/v1/steward/sessions/{id}` | The turn history of one session (for the UI to rehydrate a conversation). |

Streaming reuses the existing §4 mechanism with one added event,
`steward.turn`, so the UI can render replies live rather than polling.

**Invariants the contract must state:**
- The Steward may create issues and read the observability surface. It may
  **not** dispatch, retry, cancel, approve, or merge — those stay operator verbs
  (D-CONTROL).
- Every issue it creates records `created_by: steward` and the session id, so a
  Steward-authored task is distinguishable from an operator-authored one in the
  task record.
- A status answer must cite task ids; the UI links them. Prose without a citable
  id is a bug (§6 doctrine: everything explainable).

*Awaiting Kit's approval before P4.2+ code.*

## Tasks (ordered)

**P4.1 — The Steward profile (no new contract needed).** A `steward` profile in
`config/profiles/` (O-PROFILE): tools `read`, `create-issue`, `list-issues`,
`get-issue` — **no** edit/write/exec/git/merge, the same physical-capability
argument that makes the reviewer safe. `procedure: steward`. *Acceptance: the
profile loads; a steward-profile agent provably cannot mutate code or the
lifecycle (tool-registry test, mirroring the reviewer's).*

**P4.2 — Conversation → issue.** The chat endpoint, a session store, and the
Steward turn: given prose, either answer conversationally or produce a
`{title, body, label}` and create it through the existing dispatch path. The
issue body must capture acceptance criteria explicitly — a vague issue produces a
vague build, and the builder's DONE WHEN comes from it. *Acceptance: a live
conversation creates a real labeled issue that Cortex routes to a builder, and
the resulting task record shows `created_by: steward`.*

**P4.3 — Status queries.** "What's building?" / "why did task X fail?" answered
from `/tasks`, `/tasks/{id}`, and `/log` — projections, never recollection. The
answer cites task ids and the mechanical reason already recorded. *Acceptance: a
status answer matches the task record exactly, including the failure reason
verbatim; an unknown task is answered "I don't have that", never invented.*

**P4.4 — Clarify rather than guess.** When the request is underspecified, the
Steward asks one focused question instead of inventing acceptance criteria — the
same fail-open discipline the builder has (never fabricate; ask). *Acceptance: a
deliberately vague request produces a clarifying question, not a speculative
issue.*

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
