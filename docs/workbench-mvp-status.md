# Hirdforge Workbench — MVP status

Short, factual snapshot of the local Workbench MVP on branch
`feat/hirdforge-workbench-local-clean` (as of commit `c0f3fa2`). Current state;
no new product capability.

> **Note:** The UI evolved from a "stage-first" layout to a **chat-first** layout
> with a **Current Step** card surfaced in the central chat. All safety gates
> and API routes remain unchanged.

## What works — the first operator loop

A single Go binary (`cmd/hirdforge-workbench`) serves embedded UI assets and an
in-memory HTTP API. The full first operator loop is wired end to end:

```
Project → Provider → Architect → Cortex Task → Builder Proposal → Aggregate
→ Review → Lockbox Approval → Apply Preview → Explicit Apply → Validation
```

The browser UI (no framework, no build step) hydrates the whole run from read
endpoints on boot, shows a per-stage **Run Summary**, a full-loop **Next** hint,
and a **Current Step** card in the central chat so the operator always sees the
next valid action without hunting side panels.

### Chat-first UI

The current layout centers the chat as the primary work surface. The left rail
contains setup, run summary, lanes, and the context panel switcher. The right
panel shows stage artifacts (spec, aggregate, lockbox, apply, log). The **Current
Step** card sits in the center of the chat, deriving the next action from
`loopStatus()` and showing actionable buttons (disabled when gates are unmet).

### Active context normalization

`normalizeActiveContext()` runs on every state change and after boot hydration to
prevent the operator from being stranded in an impossible context (e.g., Builder
chat before a task exists). Lane-bound kinds require a matching selected lane;
otherwise the context falls back to Architect.

### Current known limitations

- **Native packaging** — Linux/Windows desktop packaging is a future target.
- **Current UI runs as local Go web server** — the browser at `http://127.0.0.1:7806`
  is the development harness, not the final product.
- **Provider model-name validation** — the model name field is plain text; it must
  match a name returned by the provider's `/v1/models` endpoint. No dropdown or
  validation is implemented.
- **Architect spec quality gate** — not implemented.
- **Reviewer revise → revised proposal regeneration** has a backend limitation:
  existing proposed proposals may be reused, so a fresh regeneration is not always
  possible.
- **No browser automation tests** — the UI is not tested with Puppeteer, Playwright,
  or similar.

## Safety gates (do not weaken)

- **Lockbox approval** is a human gate. Apply requires an *approved* request
  whose `proposal_id` is `aggregate:<id>`.
- **Preview is read-only** — it never writes files.
- **Apply is the only write path** and is gated by: approved Lockbox request +
  non-conflicted (`aggregated`) aggregate + open project.
- **Path confinement** — writes resolve through the project root resolver and
  touch only the aggregate's listed files (create/modify/delete with checks).
- **Explicit apply** — in the UI, apply runs only from a click after a
  `confirm()`; never on load, preview, or approval. No auto-apply.
- **Provider key safety** — the API key is stored on the backend and never
  returned by any read or echoed in any response/event.

## Idempotency / reuse guarantees

- Architect → Cortex task creation is idempotent per accepted session.
- Builder proposal generation reuses an existing `proposed` proposal per lane.
- Review generation reuses an existing `reviewed` review per aggregate+lane.
- Lockbox approval request reuses an existing pending/approved request per
  aggregate.
- Apply is idempotent — a repeat returns the existing applied result and does
  **not** re-write files. A failed apply is retryable.
- Aggregate generation is intentionally snapshot-based (not idempotent); the UI
  discourages duplicate aggregation when the proposed set is unchanged.

## Intentionally not done yet

- No persistence: all state is in-memory and lost on restart.
- No rollback/undo after apply (a failed apply does not roll back).
- No auto-apply, no auto-validation; apply and validation are explicit.
- Provider responses are parsed leniently; no streaming.
- Validation runs a single bounded command (no shell); output is capped.
- Diffs are compact file/path summaries, not full unified diffs.
- The legacy single-Builder `build/*` routes remain but are superseded and
  unused by the UI.

## Known next phase

- Packaging / cross-platform distribution (Linux + Windows).
- A UI design pass (the current UI is functional, not final).
- Persistence durability (survive restarts).
- Richer diffs in preview/apply.
- Deeper validation configuration (multiple commands, per-project defaults).

## Validate locally

```bash
gofmt -l internal/workbench
go test ./cmd/hirdforge-workbench/... ./internal/workbench/... -count=1
go test ./... -count=1
go build ./cmd/hirdforge-workbench/...
node --check internal/workbench/ui/app.js
```

See `docs/workbench-operator-loop-smoke.md` for the full-loop API smoke
sequence and `docs/workbench-api-routes.md` for the route inventory.
