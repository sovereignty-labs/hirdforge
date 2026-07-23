# Phase 1 Task 0 — The Load-Bearing Contracts (DRAFT, awaiting Kit's approval)

**Status: DRAFT.** Per CLAUDE.md stop-and-ask #2 and PHASE1_EXECUTION_SPEC Task 0,
these five contracts are drafted standalone and presented for approval **before any
P1 code is written**. Nothing in this directory is binding until Kit approves it;
approval is recorded by merging the PR and flipping each doc's status line.

Grounded in the code as it exists post-Phase-0 (`phase0/rebrand`), via a full
reconnaissance of `cmd/gateway`, `cmd/agent`, `cmd/lockbox`, `pkg/tasks`,
`pkg/tasklife`, `pkg/workspace`, and `deploy/`.

| # | Contract | Doc | Resolves |
|---|---|---|---|
| 1 | `cortex.yaml` routing schema | [ROUTING_SCHEMA.md](ROUTING_SCHEMA.md) | O-ROUTING-SCHEMA |
| 2 | Dispatch envelope | [DISPATCH_ENVELOPE.md](DISPATCH_ENVELOPE.md) | (part of O-ROUTING-SCHEMA) |
| 3 | Sandbox lifecycle | [SANDBOX_LIFECYCLE.md](SANDBOX_LIFECYCLE.md) | O-SANDBOX-CONTRACT |
| 4 | Postgres persistence | [PERSISTENCE.md](PERSISTENCE.md) | O-PERSISTENCE |
| 5 | Done-gate interface | [DONE_GATE.md](DONE_GATE.md) | (D-GATE made concrete) |

## The three design calls that ripple through all five (decide these first)

1. **Reviewer verdicts are Gitea PR reviews, not parsed prose.** The reviewer
   agent submits a real Gitea review (`APPROVE` / `REQUEST_CHANGES`) with its
   existing `create-review` tool; Cortex observes the `pull_request_review`
   webhook. The verdict is then a mechanical observation of Gitea state — no
   free-text parsing anywhere in the coordination path.
2. **One-shot sandbox Jobs, not long-lived agent pods.** v2 task execution runs
   the agent image as a per-task k8s Job in a sandbox namespace (fresh pod +
   fresh emptyDir = reset-to-clean by construction). Today's long-lived agent
   pods stay for v1 surfaces until cutover.
3. **The v2 dispatch payload is a structured envelope, not prose.** Today's
   dispatch wraps everything into a text prompt and later regex-extracts facts
   back out (the v1 disease). The envelope carries structured fields; the prompt
   is *rendered from* the envelope by a deterministic template.

## Doctrine gaps the recon surfaced (Phase 1 must close)

- **No Lockbox-mediated merge exists today.** Agents can merge PRs directly via
  the `merge-pr` tool (`pkg/tools/gitea_split.go:349`). P1.7 closes this: on the
  v2 path the *gateway* merges, only after Lockbox approval; the agent's
  `merge-pr` tool is not in the v2 builder/reviewer bundles.
- **`pkg/tasklife` still gates completion by regexing agent prose** in the live
  agent loop (`cmd/agent/tasks.go`). The v2 path never calls it; it is retired
  with v1 cutover, not silently — its removal is its own commit citing D-GATE.
- **Webhook registration only subscribes `pull_request`/`pull_request_review`.**
  Cortex needs `issues` (for `issue.labeled`) added to `ensureGiteaWebhooks`.
