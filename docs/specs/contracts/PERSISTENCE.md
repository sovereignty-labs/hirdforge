# Contract 4 — Postgres Persistence (O-PERSISTENCE)

**Status: APPROVED 2026-07-23 (Kit, PR #330).** The task-lifecycle store. v2 gets its own
tables beside the existing A2A store (`a2a_tasks`/`a2a_messages`,
`cmd/gateway/a2a.go:111`) — the v1 flows keep their store untouched until
cutover; nothing v2 writes can corrupt a live v1 surface. Same database, same
`lib/pq` wiring, same `Init`-time `CREATE TABLE IF NOT EXISTS` convention.

## Tables

```sql
CREATE TABLE IF NOT EXISTS cortex_tasks (
    id             TEXT PRIMARY KEY,          -- 'hf-' || ULID (sortable by creation)
    route_id       TEXT NOT NULL,
    status         TEXT NOT NULL DEFAULT 'queued',
    attempt        INT  NOT NULL DEFAULT 1,

    issue_repo     TEXT NOT NULL,
    issue_number   BIGINT,                    -- NULL for operator dispatches without an issue
    issue_title    TEXT,

    agent          TEXT,                      -- assigned on dispatch
    sandbox_id     TEXT,                      -- Job name; NULL until allocate
    work_branch    TEXT,
    pr_repo        TEXT,
    pr_number      BIGINT,                    -- observed from Gitea, never from agent output

    bundle         JSONB NOT NULL,            -- snapshot of the route's bundle at dispatch
    done_gate      JSONB NOT NULL,            -- snapshot of gate config + last GateResult
    failure_context JSONB,                    -- latest failure; feeds the next retry envelope
    diagnostics    JSONB,                     -- M1 termination reason / runOutcome — informational ONLY

    timeout_at     TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_cortex_tasks_status ON cortex_tasks(status);
CREATE INDEX IF NOT EXISTS idx_cortex_tasks_issue  ON cortex_tasks(issue_repo, issue_number);

CREATE TABLE IF NOT EXISTS cortex_transitions (
    id          BIGSERIAL PRIMARY KEY,
    task_id     TEXT NOT NULL REFERENCES cortex_tasks(id),
    from_status TEXT,                          -- NULL only for the creating 'queued' row
    to_status   TEXT NOT NULL,
    reason      TEXT NOT NULL,                 -- one human-legible line: 'gate_failed:test-command exit 1'
    cause       JSONB NOT NULL,                -- {kind: webhook|gate|timeout|operator|sandbox, ...evidence}
    at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_cortex_transitions_task ON cortex_transitions(task_id);

CREATE TABLE IF NOT EXISTS cortex_decisions (
    id            BIGSERIAL PRIMARY KEY,
    event_type    TEXT NOT NULL,
    event         JSONB NOT NULL,              -- the event as Cortex saw it
    matched_route TEXT,                        -- NULL = no-match
    task_id       TEXT,                        -- NULL for no-match
    reason        TEXT NOT NULL,               -- 'matched build-on-label: label agent:build' | 'no-match: …'
    at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_cortex_decisions_at ON cortex_decisions(at DESC);
```

## Status values (exactly the observability contract's spine)

`queued → dispatched → building → review → approved → merged → validated`, any
of `dispatched|building|review|approved` → `failed`, `failed → dispatched` on
retry. The legal-transition set is a **table in code**; an illegal transition
is a bug that fails loudly, never a silent write.

## Invariants (enforced, not aspirational)

1. **No status change without its transition row.** `UPDATE cortex_tasks SET
   status` and `INSERT INTO cortex_transitions` happen in one transaction —
   the store's API offers only `Transition(taskID, to, reason, cause)`;
   there is no bare status setter.
2. **Every `cause` is mechanical** — `kind` ∈ webhook | gate | timeout |
   operator | sandbox. There is no cause kind for model output; `diagnostics`
   is the only place agent-reported data lands, and nothing reads it for
   control flow.
3. **`pr_number` is written only from a Gitea API observation** (webhook or
   collect-step query) — never parsed from the agent's final message.
4. **Retry copies, never mutates**: retry bumps `attempt`, snapshots the old
   `failure_context`, and transitions `failed → dispatched` on the same row —
   the transition history preserves every prior attempt's story.

## Read paths (backs the observability contract §2 directly)

- `GET /tasks` → `cortex_tasks` filtered by `status|agent|repo`.
- `GET /tasks/{id}` → task row + its `cortex_transitions` ordered by `at`.
- `GET /log` → in-memory ring buffer (last 500 decisions) mirrored to
  `cortex_decisions`; the table is the reload-rehydration source.
- `GET /stats` → SQL aggregates over `cortex_tasks`/`cortex_transitions`
  (tasks/day, success rate, avg duration, failure rate per route).

Live streams (`task.transition`, `cortex.decision`) are emitted at write time
through the existing WebSocket fan-out (`broadcastPayload`,
`cmd/gateway/websocket.go:63`); the tables are the poll/rehydrate fallback
(contract §4).

## Migration note

No data migrates from `a2a_tasks` — different lifecycle, different semantics;
v1 history stays where it is, readable until cutover retires it.
