# PR: Hirdforge Workbench — local operator loop (MVP)

Draft summary for opening the PR for `feat/hirdforge-workbench-local-clean`.

> **Note:** This is a docs-only handoff/PR-prep commit on `feat/hirdforge-workbench-local-clean`.
> No backend gate changes. The UI evolved from "stage-first" to "chat-first" with
> Current Step and invalid context normalization.

## Title suggestion

`feat: Hirdforge Workbench — local operator loop (MVP)`

## Summary

Adds the Hirdforge Workbench: a local-first, single Go binary
(`cmd/hirdforge-workbench`) that serves an embedded browser UI and an in-memory
HTTP API implementing the full first operator loop end to end:

```
Project → Provider → Architect → Cortex Task → Builder Proposal → Aggregate
→ Review → Lockbox Approval → Apply Preview → Explicit Apply → Validation
```

The product differentiator is the **safe write boundary**: reviewed aggregate →
human Lockbox approval → read-only preview → explicit, confirmed apply →
validation. All product logic lives in `internal/workbench`; the UI is embedded
via `//go:embed` (`index.html` / `styles.css` / `app.js`) — vanilla JS, no
framework, no Node build step.

### Branch composition

PR this clean branch: **`feat/hirdforge-workbench-local-clean`** (from `main`,
`b8c19bd`). It contains exactly the **34** Workbench MVP commits
(`0feb57e..HEAD`); `git diff --stat main...HEAD` is purely Workbench files
(`internal/workbench/`, `cmd/hirdforge-workbench/`, `docs/workbench-*`,
`docs/adr-hirdforge-workbench-local.md`).

The 4 pre-existing Phase-4 staging-substrate commits (`4523acc`, `0160266`,
`dbdfffd`, `5a1d125`) that rode along on the original `feat/hirdforge-workbench-local`
branch have been **excluded** here; they belong to `feat/phase4-staging-substrate`
(`4523acc` + `0160266` are already on `origin/main`). The original branch is
preserved, and a backup exists at `backup/workbench-mvp-d4e7c82`.

## Safety / idempotency guarantees

Safety gates:

- **Lockbox approval** is a human gate; apply requires an *approved* request
  whose `proposal_id` is `aggregate:<id>`.
- **Preview is read-only** — never writes files.
- **Apply is the only write path**, gated by approved Lockbox + non-conflicted
  (`aggregated`) aggregate + open project.
- **Path confinement** — writes resolve through the project-root resolver and
  touch only the aggregate's listed files (path-escape/symlink tests included).
- **Explicit apply** — in the UI, apply runs only from a click after a
  `confirm()`; never on load, preview, or approval. No auto-apply.
- **Provider key safety** — the API key is stored backend-side and never echoed
  by any read/response/event (covered by no-secret tests on POST/GET/events).

Idempotency / reuse:

- Architect → Cortex task creation is idempotent per accepted session.
- Builder proposal generation reuses an existing `proposed` proposal per lane.
- Review generation reuses an existing `reviewed` review per aggregate+lane.
- Lockbox request reuses an existing pending/approved request per aggregate.
- Apply is idempotent — a repeat returns the existing applied result and does
  **not** re-write files; a failed apply is retryable.
- Aggregate generation is intentionally snapshot-based; the UI discourages
  duplicate aggregation when the proposed set is unchanged.

## UI / operator-loop coverage

- Per-stage **Run Summary** + a full-loop **Next** hint; the whole run rehydrates
  from read endpoints on boot (reload-safe), with resume cues and 5s event
  polling (paused when hidden).
- **Chat-first layout** — chat is the always-visible primary work surface; stages
  moved to the right panel.
- **Current Step** card in the center of the chat — the single next valid workflow
  action, derived from `loopStatus()`, with actionable buttons (disabled when
  gates are unmet). Replaced the "stage-first" layout after manual testing showed
  the "escape room" failure mode.
- **Active context normalization** (`normalizeActiveContext()`) — runs on every
  state change and after boot to prevent the operator from being stranded in an
  impossible context (e.g., Builder chat before a task exists).
- Lane board + right inspector; lane-scoped conversations with reuse.
- Setup (project/provider) hardening, provider key cleared from the form after
  save, and operator-safe empty/error copy across panels.
- Frontend↔backend route audit: **0 mismatches** (every UI call resolves to a
  registered route). See `docs/workbench-api-routes.md`.

## Test plan

```bash
gofmt -l internal/workbench                 # clean (no noise)
go test ./cmd/hirdforge-workbench/... ./internal/workbench/... -count=1
go test ./... -count=1
go build ./cmd/hirdforge-workbench/...
node --check internal/workbench/ui/app.js
```

- ~270+ Go tests in `internal/workbench` cover every stage, the gates, the
  idempotency/reuse guards, path confinement, and provider key safety; UI
  asset-marker tests assert the served `app.js` carries each stage's logic.
- Full-loop API smoke (mock provider, no external services):
  `docs/workbench-operator-loop-smoke.md`. Verified: apply writes only the
  approved file, repeat apply does not re-write, validation passes, no key leak.

## Known limitations (this MVP)

- No persistence — state is in-memory and lost on restart.
- No native shell (Linux/Windows packaging is a future target).
- Provider model-name field has no dropdown or validation; it must match
  the provider's `/v1/models` response.
- No browser automation tests.
- No rollback/undo after apply.
- No streaming; compact file/path diffs (not full unified diffs).
- Validation runs a single bounded command (no shell); output is capped.
- Legacy single-Builder `build/*` routes remain but are superseded/unused by the
  UI.

## Follow-up work

- Packaging / cross-platform distribution (Linux + Windows).
- UI design pass (current UI is functional, not final).
- Persistence durability (survive restarts).
- Richer diffs in preview/apply.
- Deeper validation configuration (multiple commands, per-project defaults).

See also: `docs/workbench-mvp-status.md`, `docs/workbench-api-routes.md`,
`docs/workbench-operator-loop-smoke.md`.
