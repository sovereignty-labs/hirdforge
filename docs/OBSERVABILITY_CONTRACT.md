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

## 7. Interlocutor surface (plan mode — Phase 4)

Approved 2026-07-25; **revised twice the same day** as the role was clarified. The
interlocutor is the **main chat interface** — the working session with the best
model: think, investigate, converge on a plan — and work is created only when the
operator **blesses** it. Conversation itself is inert. The route path `/steward/*`
is a **stable API token**; the *displayed* name is configuration (a placeholder the
user can replace — "Steward" is a stand-in). Most turns produce no plan at all; a
plan appears only when the conversation warrants work.

| Endpoint | Purpose |
|---|---|
| `POST /api/v1/steward/chat` | One conversational turn. **No side effects.** Response `{session_id, reply, plan?}`. `plan` is a *proposal*, not an action: `{id, title, steps[]}`, each step `{id, title, detail, gate, needs_operator}`. |
| `POST /api/v1/steward/handoff` | **The blessing.** Body `{session_id, plan_id, step_ids?}`. Files issue(s) for the blessed, non-`needs_operator` steps and returns `{created_issues:[{repo,number,url,label,step_id}]}`. Dispatch is **partial** — `needs_operator` steps are held, never filed. The only writing endpoint on this surface. |
| `POST /api/v1/steward/revise` | First-class revise. Body `{session_id, plan_id, instruction}`. Amends a proposed plan in-session; still **inert** — returns the updated `plan`, writes nothing. |
| `GET /api/v1/steward/sessions/{id}` | Turn + plan history, so a UI can rehydrate the thread. |

A **step's `gate`** declares its done-gate the same way a route does (D-GATE): a
code step is PR-gated (`ci-status` / `test-command`), an operational step is
Lockbox-gated (`custom-validator`, e.g. `secret exists`). Not every step is a PR.
A step with `needs_operator: true` is work the interlocutor is holding *for the
human* — surfaced, never dispatched.

Streaming reuses §4 with one added event, `steward.turn`.

**Invariants:**
- **Chat never writes.** There is no path from a conversational turn to a state
  change. Work is created only by an explicit operator blessing — not "the model
  decided to create work" but "the operator approved a plan". Revise is inert too.
- The interlocutor's tools are **read-only** (repo + observability surface). It has
  no operator verbs — dispatch, retry, cancel, approve, merge remain D-CONTROL and
  are unreachable here. It **proposes**; Cortex distributes; agents do; mechanical
  gates decide. It is never in the coordination path.
- A blessing files issues through the same `createLabeledIssueAndRoute` path the
  operator's manual dispatch uses; Cortex then routes deterministically.
- Issues it files are attributed to the interlocutor and the originating session
  (`created_by: steward`), and carry the originating `step_id`.
- Status answers are **projections** of this contract, cite task ids, and quote
  the recorded mechanical reason verbatim — never recollection (§6).

*Implementation note:* the interlocutor runs as a persistent agent (its own model —
the deep lane), and the gateway proxies `/steward/*` to it. Where the model runs
is deliberately not part of this contract; the surface above is.

## 6. Observability doctrine (the standing test)

At every point, the operator can name (a) what step each task is on, (b) why it
got there (the mechanical reason), and (c) the one action available to them. Any
state or transition the system can reach that is *not* observable with a reason
is a bug — the coordination path is deterministic precisely so that it is fully
explainable.
