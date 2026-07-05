# Hirdforge Workbench — Project Overview

Main handoff document for Hirdforge Workbench.

**Current branch:** `feat/hirdforge-workbench-local-clean`
**Latest commit:** `c0f3fa2 fix: prevent invalid Workbench chat context`
**Author workspace:** Hirdforge — human-sovereign, GitOps-oriented, agentic development loop.

---

## Purpose

Hirdforge Workbench is a **local-first operator workbench** for Hirdforge. It is not a generic coding assistant. It is a structured development loop where a human operator orchestrates specialized AI roles through a chat-first interface backed by explicit safety gates.

Workbench coordinates:

- **Architect** — drafts and negotiates a spec from a natural-language goal
- **Cortex** — breaks the spec into lanes (builder, reviewer, validator) and tasks
- **Lanes** — role-scoped work channels (builder, reviewer, validator, architect)
- **Builder** — generates concrete change proposals (file create/modify/delete)
- **Reviewer** — evaluates the aggregate of all builder proposals
- **Validator** — lane that participates in post-apply validation
- **Lockbox** — human approval gate that must authorize changes before apply
- **Apply** — the explicit, confirmed write boundary (writes files to disk)
- **Validation** — runs bounded commands against the applied project
- **Events** — in-memory event log for the entire run
- **Provider** — the external LLM/API backend (OpenAI-compatible)

The operator does not hunt through panels to find the next action. The **chat is the primary work surface**, and workflow state (next steps, readiness, current stage) is surfaced around it.

---

## Product intent

### Chat is the primary work surface

The central column is always a chat pane. It shows either the Architect spec session (before a Cortex task exists) or the currently selected lane's conversation. The operator interacts with AI roles by typing messages in the composer.

### Workflow state surrounds chat

- **Top bar** shows project, provider, and current task chips.
- **Left rail** contains setup (project + provider), run summary, lanes board, and context panel switcher.
- **Right panel** shows artifacts for the active context (spec, aggregate, lockbox, apply, event log).
- **Bottom ticker** shows the latest event with an option to open the full log.
- **Current Step** (introduced after manual testing showed the "escape room" failure mode) surfaces the single next valid workflow action in the center of the chat, so the operator never has to hunt a side panel for the next button.

### Lockbox is the write boundary

No files are written to disk without:

1. An approved Lockbox request
2. A non-conflicted (`aggregated`) aggregate
3. An explicit human approval click on the Lockbox panel
4. A read-only preview generated first
5. An explicit confirm() dialog before the actual apply

### Apply is explicit and human-approved

The Apply button only runs from an operator click after `window.confirm()`. It is idempotent — a repeat call returns the existing applied result and does **not** re-write files.

### Validation happens after apply

Validation runs a bounded local command (no shell) against the project directory. It is completely separate from the apply step and requires an explicit operator action.

### Local Go web app, not a native desktop app yet

This is currently a single Go binary that serves embedded HTML/CSS/JS and an in-memory HTTP API. The local browser (`http://127.0.0.1:7806`) is the **development harness**. Native Linux/Windows desktop packaging is a future target, not the MVP.

---

## Current implementation shape

### Backend

| Path | Description |
| --- | --- |
| `cmd/hirdforge-workbench` | Thin entrypoint; creates `workbench.New()` and serves via `Server.Handler()` |
| `internal/workbench` | Full runtime: event store, project/provider state, architect sessions, lane conversations, builder proposals, aggregate/review, Lockbox, apply preview/apply/validation, HTTP mux |
| `internal/workbench/workbench_test.go` | ~270+ Go tests covering stages, gates, idempotency, path confinement, provider key safety |

### Embedded UI

| File | Purpose |
| --- | --- |
| `internal/workbench/ui/index.html` | Single-page layout: topbar, left rail, chat center, right context panel, bottom ticker |
| `internal/workbench/ui/styles.css` | Styling — no framework, no build step |
| `internal/workbench/ui/app.js` | ~2160 lines vanilla JS — state machine, rendering, API calls, event polling |

### Architecture notes

- **No frontend framework.** Pure DOM manipulation via `render*()` functions called after state changes.
- **No Node build step.** Assets are embedded via Go `//go:embed` and served directly.
- **Go backend serves embedded HTML/CSS/JS** on `/`, `/styles.css`, and `/app.js`.
- **Local browser is the dev harness.** The final product may be a native desktop shell, but the current target is a Go web app running on `localhost`.

---

## Core operator loop

The full loop, in order:

1. Open local project (absolute path, must exist and be a directory).
2. Configure provider (base URL, API key, model name).
3. Test provider connection.
4. Start Architect session with a goal.
5. Refine/accept spec through Architect chat.
6. Accept the Architect spec.
7. Create a Cortex task from the accepted spec.
8. View lanes board (builder, reviewer, validator roles).
9. Select a Builder lane.
10. Open/use the lane conversation (chat with that role).
11. Generate Builder proposal for the selected lane.
12. Aggregate all Builder proposals into a single aggregate.
13. Run the Reviewer on the aggregate.
14. If Reviewer verdict is "revise", open the Builder revision chat and prefill reviewer feedback. Send a revision request, then re-aggregate and re-review.
15. If Reviewer verdict is "approve", request Lockbox approval.
16. Approve or reject the Lockbox request (human gate).
17. Preview the apply (read-only) — shows what files would change.
18. Explicitly apply the approved preview (writes files to disk).
19. Run validation commands against the applied project.
20. Review the event log for the full run history.

---

## Major concepts

### Architect

Drafts a structured spec (goal, constraints, affected areas, acceptance criteria, risks, open questions, suggested lanes) from a natural-language goal. The operator refines through chat, then accepts the spec to proceed. Sessions are persistent in-memory; multiple sessions can exist.

### Cortex

The task management layer. A Cortex task is created from an accepted Architect spec. It defines lanes (role-scoped work units) and coordinates the downstream flow. A task is idempently creatable per accepted session.

### Lane

A lane is a role-scoped work channel within a Cortex task. Roles include:

- **builder** — generates file change proposals
- **reviewer** — evaluates the aggregate of builder proposals
- **validator** — participates in post-apply validation
- **architect** — holds the accepted spec

Lanes have a status, a task association, and optional workspace paths/branches. Selecting a lane switches the central chat to that lane's conversation.

### Builder

A Builder lane's primary job is to generate a concrete proposal: a list of file create/modify/delete actions with summaries and rationale. Proposals are idempotent per lane — reusing an existing `proposed` proposal when one already exists.

### Reviewer

A Reviewer lane evaluates the aggregate of all builder proposals. The verdict is one of: `approve`, `revise`, or `reject`. A `revise` verdict triggers the revision forward path (see below). A `reject` verdict blocks further progress.

### Validator

A Validator lane participates in post-apply validation. Validation is a bounded local command run after apply, not a full test suite. The operator specifies the command.

### Lockbox

The human approval gate. Before any files are written, an aggregate must pass through Lockbox approval. Requests are idempotent per aggregate — a pending/approved request is reused. The operator can approve or reject.

### Apply

The write boundary. `POST /cortex/apply` writes the approved aggregate's files to the project directory. It is gated by an approved Lockbox request + non-conflicted aggregate + open project. It is idempotent and requires explicit operator confirmation via `confirm()`.

### Validation

Runs a bounded local command (no shell) against the project. Output is captured and displayed. Separate from apply — the operator must explicitly run it.

### Events

An in-memory event log (`GET /api/workbench/events`). The backend records lifecycle events (project open, provider save, session creation, lane proposal generation, etc.). The UI polls every 5 seconds (paused when the tab is hidden).

### Provider

The external LLM/API backend (OpenAI-compatible). Stores base URL, model name, and API key. The key is stored server-side and never returned by any endpoint or echoed in events.

### Current Step

The single next valid workflow action, always visible in the center of the chat. It is derived from `loopStatus()` and shows contextual guidance plus actionable buttons. Buttons are disabled (not hidden) when their gate is unmet, so the Current Step is always visible and honest. It replaced the "stage-first" layout after manual testing revealed the "escape room" failure mode.

### Context panel

The right-side panel switches between views: **Context** (spec or lane artifact), **Agg** (aggregate + review), **Lock** (Lockbox approval + preview), **Apply** (apply + validation), and **Log** (event log). The switcher is frontend-only (CSS via `body[data-view]`) and persisted in `localStorage`. It never replaces the central chat.

### Run Summary

A compact status line per stage in the left rail, showing whether each step is done/todo and the current status value. The whole run rehydrates from read endpoints on boot (reload-safe).

### Setup collapse

Before a Cortex task exists, the setup section (project + provider) stays fully visible. Once a task exists, it can be collapsed to a compact summary line, giving the chat more screen space. The collapse preference is persisted in `localStorage` and only honored when a task exists.

### Active chat context normalization

A safety mechanism (`normalizeActiveContext()`) that enforces the chat-kind invariant at runtime:

- Before a Cortex task exists, only the Architect context is valid.
- Lane-bound kinds (builder, reviewer, validator) require a selected lane of that role.
- If the saved context would be invalid (e.g., Builder chat with no task/lane), it is normalized to the Architect.
- Invalid restored state from `localStorage` is corrected at runtime without user interaction.

This prevents the operator from being stranded in an impossible context after a browser refresh or session restore.

---

## Current UI model

### Layout (three-column, chat-first)

```
┌───────────────────────────────────────────────────────────────────┐
│ TOPBAR: brand · loading indicator · project/provider/task chips   │
├──────────────────┬──────────────────────┬─────────────────────────┤
│ LEFT RAIL        │ CENTER (CHAT)        │ RIGHT PANEL             │
│                  │                      │                         │
│ [Setup]          │ Chat head (title,    │ Context panel           │
│ - project open   │   kind chips)        │ - Context (spec/lanes)  │
│ - provider       │                      │ - Agg                   │
│ [Run Summary]    │ Next hint            │ - Lock                  │
│ - per-stage      │ Current Step         │ - Apply                 │
│   status         │ (next action + btns) │ - Log                   │
│ [Lanes]          │                      │                         │
│ - board          │ Chat stream           │                         │
│ [Context panel]  │ (Architect or lane)  │                         │
│ - Context        │ Revise banner         │                         │
│ - Agg            │ Composer              │                         │
│ - Lock           │ (scoped by kind)      │                         │
│ - Apply          │                       │                         │
│ - Log            │                       │                         │
├──────────────────┴──────────────────────┴─────────────────────────┤
│ BOTTOM TICKER: latest event · status · open log →                  │
└───────────────────────────────────────────────────────────────────┘
```

### Evolution from stage-first to chat-first

The original layout was **stage-first**: the central column showed the current workflow stage (Architect → Aggregate → Lockbox → Apply) and the chat was secondary in a drawer. Manual testing revealed a **"escape room" failure mode** — when the operator was in the middle of refining a spec or a lane conversation, the stage-first layout would hide or replace the chat surface, making it hard to find the next action.

**Chat-first correction** (`e28b437`): The chat became the always-visible primary work surface. Stages moved to the right panel. The left rail provides context (lanes, run summary).

**Current Step correction** (`d870980`, `c0f3fa2`): Even with chat-first, operators still had to hunt for the next button. The **Current Step card** was added to the center of the chat to always show the single next valid workflow action. Combined with **invalid context normalization**, this prevents the operator from being stranded in impossible contexts after reloads.

---

## Safety model

### Provider key safety

- API key is stored on the local backend only.
- No read endpoint (`GET /provider`, `GET /events`, or any other) returns the key.
- The key field is cleared from the form after save (`clearApiKeyInput()`).
- `POST /provider/test` does not echo the key in its response.

### Lockbox as write gate

Apply requires an **approved** Lockbox request whose `proposal_id` is `aggregate:<aggregate_id>`. Without this, the apply endpoint returns a 403 or 409.

### Preview is read-only

`POST /cortex/apply/preview` computes what files would change but **never writes files**. It can return `ready`, `blocked`, or `failed` statuses without writing anything.

### Apply is explicit and idempotent

- In the UI, apply only runs from an explicit click after `window.confirm()`.
- It never runs on load, preview, or approval.
- A repeat call returns the existing applied result and does **not** re-write files.
- A failed apply is retryable.

### Path confinement

Writes resolve through the project root resolver and touch only the aggregate's listed files. Path-escape and symlink protections are in place.

### Validation is separate from apply

Validation runs a bounded local command after apply. It is not automatic — the operator must explicitly run it. Output is capped.

### Failed/rejected states do not silently progress

- A rejected Lockbox request does not auto-retry.
- A failed apply does not auto-recover.
- A "revise" Reviewer verdict does not auto-aggregate — the operator must open the Builder revision chat, send the revision request, then manually re-aggregate.

---

## Backend / API summary

All API routes are under `/api/workbench/`. Methods listed are the ones each handler accepts; other methods return `405`.

See `docs/workbench-api-routes.md` for the complete route inventory. Key groups:

| Group | Routes | Purpose |
| --- | --- | --- |
| **Health** | `GET /health` | `{status, mode}` |
| **Events** | `GET /events` | Event log (polling) |
| **Project** | `POST /project/open`, `GET /project`, `GET /project/inspect` | Open, inspect local project |
| **Provider** | `POST /provider`, `POST /provider/test` | Configure + test LLM backend |
| **Architect** | `POST /architect/session`, `/message`, `/accept`, `/cortex-task` | Spec drafting from goal |
| **Lane conversation** | `/lane-conversation` (CRUD) | Per-lane AI chat sessions |
| **Cortex** | `/cortex/task`, `/lanes`, `/lane/propose`, `/aggregate`, `/review`, `/apply`, `/apply/preview`, `/apply/validate` | Task management, proposals, approval, write, validate |
| **Lockbox** | `/lockbox/requests`, `/approve`, `/reject` | Human approval gate |
| **UI assets** | `/`, `/styles.css`, `/app.js` | Embedded static files |

Legacy routes (`/build/*`, `/validation`, `/diff`) exist but are superseded or unused by the UI.

---

## State / persistence (frontend localStorage)

Only lightweight, non-sensitive UI selection is persisted:

| Key | Purpose |
| --- | --- |
| `hf.selectedLaneId.v1` | Selected lane ID (nullable) |
| `hf.selectedConsoleKind.v1` | Active chat context kind (architect/builder/reviewer/validator/lockbox/apply) |
| `hf.workbenchView.v1` | Right context panel view (context/aggregate/lockbox/apply/log) |
| `hf.setupCollapsed.v1` | Whether setup section is collapsed (1 = collapsed, 0 = expanded) |

### Active context normalization

`normalizeActiveContext()` runs on every state change that could invalidate the active context:

- **Before a task:** kind is forced to `"architect"`, selected lane is null.
- **Lane-bound kinds:** (builder/reviewer/validator) require a selected lane of that role. If the selected lane has a different role (or no lane is selected), the kind falls back to `"architect"`.
- **Lockbox/apply:** task-level, valid only when a task exists.

This normalization only mutates runtime state — it never rewrites persisted selections. Invalid restored state is normalized at boot after hydration.

---

## Important commits / evolution

| Commit | Description |
| --- | --- |
| `0feb57e..*` | Initial Workbench MVP commits (local Go binary, embedded UI, full loop) |
| `embed-ui` | Split UI into embedded `index.html` + `styles.css` + `app.js` |
| `state-restore` | Add localStorage persistence + boot hydration + resume cues |
| `setup-hardening` | Provider key cleared after save, setup stays open before task |
| `architect-hardening` | Architect flow: spec readiness gates, inline hints |
| `lane-board` | Introduce lanes board, right inspector, lane conversations |
| `lane-console` | Lane Console drawer with kind switcher and scoped conversations |
| `builder-proposals` | Builder proposal generation, reuse, inspector integration |
| `aggregate-review` | Aggregate proposals, run Reviewer, revise/reject paths |
| `lockbox-apply` | Lockbox approval gate, apply preview (read-only), explicit apply |
| `run-summary` | Per-stage Run Summary in left rail |
| `pr-prep` | PR summary, test plan, branch cleanup |
| `clean-split` | Split to `feat/hirdforge-workbench-local-clean` |
| `focused-shell` | Introduce focused-shell flow (later reverted) |
| `chat-first` | `e28b437` — restore chat-first layout, fix escape room failure |
| `current-step` | `d870980` / `c0f3fa2` — Current Step card + invalid context normalization |

---

## Current known limitations

- **Native shell not built** — Linux/Windows native packaging is a future target.
- **Current UI runs as local Go web server in browser dev harness** — the browser at `http://127.0.0.1:7806` is the development harness, not the final product target.
- **Provider model-name validation/dropdown not implemented** — the model name field is plain text; it must match a name returned by the provider's `/v1/models` endpoint. If it doesn't match, the provider call will fail.
- **Architect spec quality gate not implemented** — the spec is accepted without automated quality checks.
- **Reviewer revise → revised proposal regeneration** has a backend limitation: existing proposed proposals may be reused, so a truly fresh regenerated proposal is not always possible after revision.
- **No browser automation tests** — the UI is not tested with Puppeteer, Playwright, or similar.
- **No persistence** — all state is in-memory and lost on restart (survives reload via localStorage selection keys).
- **No rollback/undo** after apply.
- **No streaming** — provider responses are parsed as complete blocks.
- **Validation runs a single bounded command** (no shell); output is capped.
- **Legacy `build/*` routes** remain but are superseded and unused by the UI.
- **No merge yet** — this branch is for review.

---

## How to run locally

```bash
cd ~/hirdforge

HIRDFORGE_WORKBENCH_HOST=127.0.0.1 \
HIRDFORGE_WORKBENCH_PORT=7806 \
go run ./cmd/hirdforge-workbench
```

Browser dev harness:

```
http://127.0.0.1:7806
```

### Sample test project setup

```bash
rm -rf /tmp/hf-clicktest
mkdir -p /tmp/hf-clicktest/src/auth

cat > /tmp/hf-clicktest/src/auth/session.ts <<'EOF'
export function issueSession(userId: string) {
  return {
    userId,
    accessToken: "old-access-token",
  };
}
EOF
```

### Provider example (discovered during manual test)

```
Base URL: http://203.0.113.15:11435/v1
Model: gwen-nex
```

**Important:** Model names must match what the provider returns at `/v1/models`. If the model name doesn't match, the provider call will fail. There is no dropdown or validation for this field.

---

## Manual smoke checklist

- [ ] Open a project (absolute path, must exist)
- [ ] Configure a provider (base URL, model)
- [ ] Test the provider — confirm `{ok: true}`
- [ ] Start an Architect session with a goal
- [ ] Send a refinement message (spec should be drafted)
- [ ] Accept the spec
- [ ] Create a Cortex task (idempotent per accepted session)
- [ ] Select a Builder lane from the lanes board
- [ ] Current Step shows "Generate Builder Proposal" for the builder lane
- [ ] Generate a Builder proposal
- [ ] Aggregate the proposals (Agg panel)
- [ ] Run the Reviewer
- [ ] If revise: open Builder revision chat, send revision, re-aggregate, re-review
- [ ] If approve: request Lockbox approval
- [ ] Approve or reject the Lockbox request
- [ ] Preview the apply (read-only)
- [ ] Apply the approved preview (explicit confirm dialog)
- [ ] Run validation
- [ ] Reload the page — verify resume cues appear
- [ ] Check provider key is NOT visible in the provider meta section
- [ ] Check `/api/workbench/provider` GET does not return the key
- [ ] Check `/api/workbench/events` does not contain the key

---

## PR readiness / merge guidance

- This branch (`feat/hirdforge-workbench-local-clean`) is intended for **PR review**.
- **Do not merge** until manual click-through is acceptable.
- No backend gate changes were made in the recent UI fixes (chat-first, Current Step, normalization).
- The branch should be pushed to Gitea as `feat/hirdforge-workbench-local-clean`.
- See `docs/workbench-pr-summary.md` for the full PR summary template.

---

## See also

- `docs/workbench-api-routes.md` — complete route inventory
- `docs/workbench-mvp-status.md` — MVP capability snapshot
- `docs/workbench-operator-loop-smoke.md` — full-loop API smoke test (curl + mock provider)
- `docs/workbench-pr-summary.md` — PR draft summary
