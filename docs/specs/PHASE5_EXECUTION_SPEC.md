# Phase 5 Execution Spec — The Cockpit (the UI)

**Status:** Draft, produced at the Phase 4 boundary (2026-07-25), grounded in the
surfaces as they now run live. Checkpoints (stop-and-ask) called out inline.
Companion to `HIRDFORGE_V2_PRD.md` (Phase 5), `OBSERVABILITY_CONTRACT.md` (the
frozen data surface), `LOOP_SPEC.md` (the operator-experience acceptance test),
and `docs/ui/` (the visual north-star + behavior reference).

## Goal — make the loop *felt*

Phases 1–4 built the machine and proved it works over `curl`: an issue routes, an
agent builds, a gate decides, a reviewer approves, the merge lands behind Lockbox,
and — Phase 4 — a conversation with the interlocutor produces a blessed plan that
drives all of it. Phase 5 puts a face on it. The bar is **LOOP_SPEC's eight
moves**: the UI is done when each move *feels* natural, not merely possible. Any
surface serving none of the eight is ballast; any move it can't make feel natural
is unfinished.

Everything the UI needs is already live and frozen: `/api/v1/steward/*`,
`/api/v1/cortex/tasks` (now projecting `pr_repo`, PR #468), `/api/v1/cortex/log`,
`/api/v1/approvals`, and the `/ws/events` stream. **No new backend is in scope**
unless a move genuinely cannot be served by the frozen contract — and that is a
stop-and-ask (#2), not a silent addition.

## Grounding — what exists, and the reconciliation

- **The face already ships as one embedded React page.** `cmd/gateway/index.html`
  is an in-browser React app (`App`, `ChatPanel`, `MissionControlView`, `/ws/events`)
  built by `scripts/build-gateway-ui.mjs` (which validates required snippets) and
  served via `//go:embed`. Phase 5 **evolves this file**, it does not start a new
  stack. This is the established pattern; keeping it avoids a build-system detour.
- **The visual north-star is `docs/ui/cockpit-mock.html` + `DESIGN_SYSTEM.md`**
  (copper-ember `--accent #B25E36`/`#D08453`, `system-ui`, evidence mono-chips).
  Five surfaces: **Cockpit** (conversation + inline plan card), **Live inspector**
  (tasks, Cortex log, evidence, Lockbox gate), **Fleet & models**, **Memory
  (Seidr)**, **Settings**.
- **Reconciliation (flag, per doc-hierarchy #6).** `docs/ui/spec.md` is the
  behavior/data reference but is **locked at Workbench-era vocabulary** (Builder/
  Action/Reviewer/Architect "surfaces"; Comms/Warriors/Realm tabs; Ivar/Jeeves/
  Sindri/Ragnar patterns; its own internal "Phase 0–12" sequence). Where it
  conflicts with the v2 model, **the live backend and the cockpit-mock win**: v2
  has one interlocutor (not per-persona surfaces), a deterministic Cortex, and the
  five cockpit surfaces above. Phase 5 mines spec.md for interaction detail (§2
  layout contract, §5 backend mapping, streaming semantics) but renders the v2
  cockpit vocabulary. Its Workbench specifics are carried into
  `AUDIT_DELTA_V2.md` as superseded.

## Tasks (ordered — Cockpit slice first)

**P5.1 — Shell + design system.** Bring the cockpit-mock's tokens, typography, and
five-surface frame into `cmd/gateway/index.html`: copper-ember accent, `system-ui`,
the mono evidence-chip style, light/dark. Replace the Workbench chrome with the
cockpit frame (conversation-left, inspector-right per DESIGN_SYSTEM). *Acceptance:
the shell renders in the house style — no default-browser screen (CLAUDE.md #9) —
and `build-gateway-ui.mjs` still validates.*

**P5.2 — Cockpit conversation.** The front door is a conversation, not a form
(LOOP_SPEC move 1). Wire the chat pane to `POST /steward/chat`; render the
interlocutor's prose replies; show the configurable display name (not a hardcoded
"Steward"); rehydrate a thread from `GET /steward/sessions/{id}`. *Acceptance: a
real conversation with the deployed interlocutor runs in the UI, reply for reply.*

**P5.3 — The plan card (revise / bless).** When a turn returns a `plan`, render the
inline plan card from the mock: multi-step, each step's `gate` and a `[you]` badge
for `needs_operator`, with **[Revise]** (→ `POST /steward/revise`) and **[Dispatch]**
(→ `POST /steward/handoff`, the blessing). Dispatch is partial — held steps stay
with the operator (moves 3, 4, 7). *Acceptance: a blessed plan in the UI files real
issues and they appear flowing in the inspector; a held step is visibly not
dispatched.*

**P5.4 — Live inspector.** The right rail: active tasks from `/cortex/tasks` (now
with `pr_repo` → a real task→PR link), the Cortex log from `/cortex/log` as the
mechanical narration, per-`done` evidence chips (`secret Ready · 20s`), and the
**Lockbox gate** from `/approvals` with Approve/Reject — the one action the operator
gates (moves 2, 5, 6). Long/parallel work announces itself when done (move 5) via
`/ws/events`. *Acceptance: a task blessed in the cockpit is watchable end-to-end —
building → PR → gate → review → the Lockbox merge approval — from the inspector,
and approving the Lockbox gate here merges it (closing the loop the Phase-4 test had
to `curl`).*

**P5.5 — Fleet & models.** The fold-in from DESIGN_SYSTEM: per-lane GPU/VRAM/
throughput and model swap, incl. the interlocutor lane (the "borderline which
model" control — qwen-reserved vs qwen27b-worker made visible). Read from the
existing fleet/anvil surface. *Acceptance: the interlocutor's lane is visible and
swappable without a redeploy.*

**P5.6 — Memory (Seidr) + Settings.** Seidr browse/scope view; sectioned Settings
including the **interlocutor identity** (the user-replaceable name — D-INTERLOCUTOR)
and lane. *Acceptance: renaming the interlocutor in Settings changes the displayed
name; Settings is sectioned per the mock.*

## Checkpoints (stop-and-ask)

- **The observability contract is frozen.** If a move cannot be served by the
  current `/api/v1/*` surface, that is a load-bearing-contract change (#2): draft
  the endpoint against `OBSERVABILITY_CONTRACT.md`, present it, wait — never widen
  the surface silently. (spec.md §5.10 lists Workbench-era "backend additions
  required"; re-derive against the v2 contract, do not assume.)
- **No write path beyond the frozen ones.** The UI's only writes are the blessing
  (`/steward/handoff`) and the Lockbox merge approval (`/approvals`) — both exist.
  A new write is #3.

## Explicitly NOT in Phase 5

New agent roles (researcher — Phase 6). Multi-user auth/identity. Mobile layout.
Re-opening the frozen observability contract for convenience. The Workbench UI's
per-persona surfaces (superseded by the single interlocutor + cockpit).

## Phase acceptance (the whole phase) — the eight moves

The UI is accepted when a first-time operator can, without a terminal: (1) open a
conversation, (2) watch the interlocutor ground in live reality, (3) see a legible,
revisable plan, (4) bless a direction (partial dispatch), (5) let long work run and
announce itself, (6) approve the one Lockbox gate that merges, (7) see every `done`
backed by evidence, (8) resume a thread that "nothing starts cold." Existing gateway
tests stay green; `build-gateway-ui.mjs` validation extended, not weakened.

## Next-phase draft

At Phase 5 acceptance, draft `PHASE6_EXECUTION_SPEC.md` — the researcher role
(D-WITNESS: a general research/coding capability, not a Judge) and fleet scaling,
now that the operator surface exists to observe them.
