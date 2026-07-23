# Contract 1 — `cortex.yaml` Routing Schema (O-ROUTING-SCHEMA)

**Status: APPROVED 2026-07-23 (Kit, PR #330).** The core new contract: how events become
dispatches, deterministically.

## Semantics (the part that matters)

- Cortex holds an **ordered list of routes**. An event is tested against routes
  top-to-bottom; **first match wins**; no match → a logged `no-match` decision
  and nothing else happens. Matching is **exact string equality** on the fields
  below — no regex, no globs, no model calls, ever (PRD Principle 1).
- Every evaluation — match or no-match — is recorded as a `cortex.decision`
  (see PERSISTENCE.md) with the event, the rule consulted, and the reason.
- The file is loaded at gateway start and on `SIGHUP`/config-reload endpoint;
  the loaded config is readable back verbatim at `GET /api/v1/cortex/routes`
  (observability contract §2). A parse error keeps the previous config and
  emits a loud event — never a silent half-load.

## Event taxonomy

Two sources, one shape. An event is `{type, repo, fields…}`:

| Type | Source | Fired when |
|---|---|---|
| `issue.labeled` | Gitea webhook (`issues` event, action `label_updated`/`labeled`) | a label lands on an issue |
| `pr.review_submitted` | Gitea webhook (`pull_request_review`) | a reviewer submits APPROVE / REQUEST_CHANGES |
| `task.gate_passed` | internal (done-gate evaluator) | a task's mechanical gate went green |
| `task.gate_failed` | internal | gate went red |
| `pr.merged` | Gitea webhook (`pull_request`, closed+merged) | the apply happened |
| `operator.dispatch` | `POST /api/v1/cortex/dispatch` | manual dispatch (D-CONTROL) |

Phase 1 uses all six; the schema admits new types without change.

## Schema

```yaml
version: 1

defaults:
  repo: kit/hirdforge
  timeout_minutes: 60

bundles:                      # BUILDER_HARNESS v2 amendment: {skills, memory_scopes, profile}
  build-default:
    skills: []                # Phase 1: empty (skill dispatch is Phase 2)
    memory_scopes: []         # Phase 1: empty (skill-scoped Seidr is Phase 2)
    profile: default          # harness profile name; Phase 1 knows only "default"
  review-default:
    skills: []
    memory_scopes: []
    profile: default

routes:
  - id: build-on-label        # THE Phase-1 build route
    on:
      event: issue.labeled
      label: agent:build
      repo: kit/hirdforge     # optional; falls back to defaults.repo
    dispatch:
      role: builder
      bundle: build-default
    done_gate:                # see DONE_GATE.md
      type: test-command
      command: "go build ./... && go vet ./... && go test ./..."
      timeout_minutes: 20
    on_gate_passed: review-on-gate   # id of the follow-on route
    timeout_minutes: 60       # building-state watchdog → failed(reason=timeout)

  - id: review-on-gate        # THE Phase-1 reviewer route
    on:
      event: task.gate_passed
      route: build-on-label   # only tasks that came through the build route
    dispatch:
      role: reviewer
      bundle: review-default
      artifact: pr-diff       # D-LESSONS #4: the envelope carries the diff

  - id: revise-on-changes-requested   # D-LESSONS #3: the sanctioned revise path
    on:
      event: pr.review_submitted
      state: REQUEST_CHANGES
    dispatch:
      role: builder
      bundle: build-default
      carry: review-feedback  # envelope carries the reviewer's comments
    done_gate:
      type: test-command
      command: "go build ./... && go vet ./... && go test ./..."
      timeout_minutes: 20
    on_gate_passed: review-on-gate

  - id: approve-on-review     # APPROVE → status approved → waits on Lockbox
    on:
      event: pr.review_submitted
      state: APPROVED
    action: advance           # no dispatch — a pure lifecycle transition
    to: approved

  - id: validate-on-merge
    on:
      event: pr.merged
    action: advance
    to: validated             # post-merge validation + issue close (P1.7)
```

## Field reference

- `route.on` — the match clause. `event` is required; every other key must
  equal the event's field exactly. Recognized keys per event type are fixed in
  code; an unrecognized key is a **config error at load**, not a silent skip.
- `route.dispatch` — `{role, bundle, artifact?, carry?}`. `role` selects the
  agent pool (`builder` | `reviewer`); `bundle` names a bundle above;
  `artifact: pr-diff` and `carry: review-feedback` instruct envelope assembly
  (see DISPATCH_ENVELOPE.md). Exactly one agent is dispatched per match.
- `route.action: advance` — routes that only move the lifecycle (no agent).
  `to` names the target status; the transition reason is the event itself.
- `route.done_gate` — required on every route that dispatches a `builder`.
  Schema in DONE_GATE.md. `on_gate_passed` names the follow-on route so the
  chain is explicit in config, not implicit in code.
- **Retry is not a route.** `POST /tasks/{id}/retry` (D-CONTROL) re-executes
  the task's original route with `failure_context` populated in the envelope
  (D-LESSONS #2). Automatic retry policy is Phase 3.

## Determinism obligations (audit hooks)

1. Route evaluation is a pure function `(config, event) → decision`; a
   table-driven test pins it (same event + config ⇒ same decision, always).
2. No route field may reference model output. The reviewer's verdict enters as
   a `pr.review_submitted` **Gitea webhook** — Gitea state, not prose.
3. Label taxonomy: the Phase-1 trigger label is **`agent:build`** (new, v2's
   own namespace) — deliberately NOT v1's `status/ready` flow, which stays
   untouched until cutover.
