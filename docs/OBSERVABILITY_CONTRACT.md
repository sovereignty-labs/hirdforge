# Hirdforge v2 — Observability & Control Contract

**Status:** Load-bearing contract (CLAUDE.md stop-and-ask #2). Frozen *before* UI
work (D-UISEQ) so the pipeline UI is wireframed against a stable surface. This is
the operator's window into the system and their hands on it. "High control and
observability, wired up front" (Kit) made concrete.

**Principle:** everything the coordination path decides is observable, and every
decision carries its *reason*. The operator can always answer "what is happening,
why did it happen, and what can I do about it" without reading logs.

---

## 1. The task lifecycle (the spine everything observes)

Status values (event-driven; every transition emits an event with a reason):

```
queued → dispatched → building → review → approved → merged → validated
                          │          │         │                  │
                          └──────────┴─────────┴──────► failed ────┘
```

- `queued` — event matched a route; not yet dispatched.
- `dispatched` → `building` — an agent was assigned a sandbox and is working.
- `review` — the agent opened a PR; the mechanical done-gate passed; a reviewer
  route fired.
- `approved` — reviewer + Lockbox authorized the merge.
- `merged` — the PR merged (the apply).
- `validated` — post-merge validation route confirmed; issue closed.
- `failed` — a mechanical gate failed, a timeout fired, or the sandbox errored;
  carries the failure reason (and, for a retry, becomes the next dispatch's
  failure context — D-LESSONS #2).

**Invariant:** every transition is caused by a mechanical event (webhook, gate
result, timeout) — never by a model asserting a state. The observable reason is
that mechanical cause.

## 2. Read / observe surface

All under `/api/v1/cortex/` on the gateway.

| Endpoint | Returns |
|---|---|
| `GET /tasks` | Task list, filterable by `status`, `agent`, `repo`. Each: id, issue ref, status, agent, pr, retry_count, timestamps, timeout_at. |
| `GET /tasks/{id}` | Full task detail: lifecycle history (each state + reason + timestamp), skills/scopes dispatched, done-gate config + last result, PR ref, reviewer, failure context if any. |
| `GET /tasks/{id}/events` | The typed event stream for one task (build output, tool calls, gate results, transitions). Backed by the existing typed-event + workspace-projector machinery. |
| `GET /routes` | Current `cortex.yaml` routing config (rules, bundles, done-gates) as loaded. |
| `GET /log` | Recent routing decisions (ring buffer): event in → rule matched → dispatch out (or no-match), with the deterministic reason. This is the "why did Cortex do that" panel. |
| `GET /fleet` | Agent fleet state (idle/busy/unhealthy per agent), reusing the gateway `agents` map + health. |
| `GET /stats` | Dispatch stats: tasks/day, success rate, avg duration, failure rate per route/skill scope. |

## 3. Control surface (D-CONTROL — dispatch / retry / cancel only)

| Endpoint | Effect |
|---|---|
| `POST /dispatch` | Operator creates + dispatches a task directly (creates the issue on git.hirdforge.com Gitea + triggers the route). The manual entry point. |
| `POST /tasks/{id}/retry` | Re-dispatch a `failed` task. The new dispatch carries the prior failure as context (D-LESSONS #2). Emits a loud event. |
| `POST /tasks/{id}/cancel` | Cancel an in-flight task: signal the agent, tear down its sandbox, mark `failed(reason=cancelled)`. Emits an event. |

**Not built now (seams left, D-CONTROL):** pause/resume, gate-override,
live-intervene. If a gate-override is ever added, it MUST emit a loud audit event —
a human override is legitimate, a silent one is a doctrine violation.

## 4. Streaming (push, not poll)

The gateway already has WebSocket fan-out + the workspace projector. The UI
subscribes and receives:
- `task.transition` — a task changed state (id, from, to, reason).
- `task.event` — a typed event for a task (tool call, output chunk, gate result).
- `cortex.decision` — a routing decision (the `/log` entries, live).
- `fleet.update` — an agent health/status change.

Polling (`GET` endpoints) is the fallback and the reload-rehydration path; the
live experience is push.

## 5. What the UI wireframes against (Phase 5)

This contract is the fixed surface. The Phase 5 pipeline UI renders:
- **Pipeline board** — tasks as cards in status columns, live via `task.transition`.
- **Cortex log** — the `/log` ring buffer, live via `cortex.decision`: every
  routing decision and its reason, legible.
- **Task detail** — `/tasks/{id}` + `/tasks/{id}/events`: the full lifecycle,
  reasons, gate results, PR link, and the three control buttons (retry/cancel;
  dispatch is global).
- **Fleet strip** — `/fleet`, live.

The UI never invents state or decisions; it renders this contract. If a surface
needs data this contract doesn't expose, the contract is extended *here first*
(stop-and-ask), then the UI consumes it.

## 6. Observability doctrine (the standing test)

At every point, the operator can name (a) what step each task is on, (b) why it
got there (the mechanical reason), and (c) the one action available to them. Any
state or transition the system can reach that is *not* observable with a reason
is a bug — the coordination path is deterministic precisely so that it is fully
explainable.
