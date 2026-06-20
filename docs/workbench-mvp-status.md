# Hirdforge Workbench — MVP status

Short, factual snapshot of the local Workbench MVP on branch
`feat/hirdforge-workbench-local` (as of commit `ca0743a`). Stabilization audit;
no new product capability.

## What works — the first operator loop

A single Go binary (`cmd/hirdforge-workbench`) serves embedded UI assets and an
in-memory HTTP API. The full first operator loop is wired end to end:

```
Project → Provider → Architect → Cortex Task → Builder Proposal → Aggregate
→ Review → Lockbox Approval → Apply Preview → Explicit Apply → Validation
```

The browser UI (no framework, no build step) hydrates the whole run from read
endpoints on boot, shows a per-stage **Run Summary** and a full-loop **Next**
hint, and survives reloads.

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
