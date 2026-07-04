# Hirdforge Workbench — API route inventory

Developer-facing inventory of the routes registered in
`internal/workbench/server.go` (`registerRoutes`), grouped by operator-loop
stage. All API routes are under `/api/workbench/`. Methods listed are the ones
each handler accepts; other methods return `405`.

Audited at commit `ca0743a`. Frontend (`internal/workbench/ui/app.js`) uses a
subset of these; every UI call resolves to a registered route (0 mismatches).
Routes the UI does not call are marked _(unused by UI)_.

> **Note:** The UI evolved from a "stage-first" layout to a **chat-first** layout
> with a **Current Step** card surfaced in the central chat. These API routes
> remain unchanged — the UI evolution was purely frontend (layout, rendering,
> localStorage persistence, context normalization).

## UI assets

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/` | embedded `index.html` (chat-first operator UI) |
| GET | `/styles.css` | embedded stylesheet |
| GET | `/app.js` | embedded operator UI script (~2160 lines vanilla JS) |
| GET | `/health` | `{status, mode}` |

## Events

| Method | Path | Notes |
| --- | --- | --- |
| GET / POST | `/events` | list the event log / append an event |

## Project

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/project` | current open project, `404` if none |
| POST | `/project/open` | open a project (absolute path; must exist + be a dir) |
| GET | `/project/inspect` | run + return a read-only inspection |
| GET | `/project/inspection` | last inspection, `404` if none |

## Provider

| Method | Path | Notes |
| --- | --- | --- |
| GET / POST | `/provider` | sanitized provider state (never the key) / configure |
| POST | `/provider/test` | call the provider once; `{ok,status,model}` or `{ok:false,error}` |

## Architect (spec drafting)

| Method | Path | Notes |
| --- | --- | --- |
| GET / POST | `/architect/session` | current session / create from a goal |
| GET | `/architect/sessions` | all sessions |
| POST | `/architect/message` | one conversational turn (provider) |
| POST | `/architect/accept` | mark the spec accepted |
| POST | `/architect/cortex-task` | create a Cortex task from the accepted spec (idempotent) |

## Cortex task / lanes

| Method | Path | Notes |
| --- | --- | --- |
| GET / POST | `/cortex/task` | current task / create one |
| GET | `/cortex/tasks` | all tasks _(unused by UI)_ |
| GET | `/cortex/lanes` | current task's lanes |
| POST | `/cortex/run` | deterministic stub run of a task's lanes _(unused by UI)_ |
| GET / POST | `/cortex/worktrees` | builder lane worktrees / allocate them _(unused by UI)_ |

## Lane conversations

| Method | Path | Notes |
| --- | --- | --- |
| GET / POST | `/lane-conversation` | current conversation / create (scoped by kind/lane) |
| GET | `/lane-conversations` | list (filters `?task_id` / `?lane_id` / `?kind`) |
| POST | `/lane-conversation/message` | one turn (provider) |
| POST | `/lane-conversation/close` | close the conversation |

## Builder proposals (Cortex lane)

| Method | Path | Notes |
| --- | --- | --- |
| POST | `/cortex/lane/propose` | generate (provider); reuses an existing `proposed` proposal for the lane |
| GET | `/cortex/lane/proposal` | current proposal _(unused by UI)_ |
| GET | `/cortex/lane/proposals` | list (filters `?task_id` / `?lane_id`) |

## Aggregate / review

| Method | Path | Notes |
| --- | --- | --- |
| GET / POST | `/cortex/aggregate` | current aggregate / aggregate proposed proposals (snapshot) |
| GET | `/cortex/aggregates` | list _(unused by UI)_ |
| POST | `/cortex/aggregate/lockbox` | create a Lockbox request from an `aggregated` aggregate; reuses a pending/approved one |
| GET / POST | `/cortex/aggregate/review` | current review / run a reviewer lane (provider); reuses a `reviewed` review per aggregate+lane |
| GET | `/cortex/aggregate/reviews` | list (filters) _(unused by UI)_ |

## Lockbox approval

| Method | Path | Notes |
| --- | --- | --- |
| GET / POST | `/lockbox/request` | current request / create from a Builder proposal _(unused by UI; UI uses aggregate/lockbox)_ |
| GET | `/lockbox/requests` | all requests |
| POST | `/lockbox/approve` | approve a pending request (human gate) |
| POST | `/lockbox/reject` | reject a pending request |

## Apply preview / apply / validation (the write boundary)

| Method | Path | Notes |
| --- | --- | --- |
| GET / POST | `/cortex/apply/preview` | current preview / compute a **read-only** preview (gated: approved Lockbox + aggregated aggregate + open project) |
| GET | `/cortex/apply/previews` | list _(unused by UI)_ |
| GET / POST | `/cortex/apply` | current apply result / **write** the approved files (same gate; idempotent — a repeat returns the existing applied result, no re-write) |
| GET | `/cortex/applies` | list _(unused by UI)_ |
| POST | `/cortex/apply/validate` | run a bounded local command after a successful apply |
| GET | `/cortex/apply/validation` | current validation, `404` if none |
| GET | `/cortex/apply/validations` | list _(unused by UI)_ |

## Other / legacy

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/validation` | placeholder validation view _(unused by UI)_ |
| GET | `/diff` | placeholder diff view _(unused by UI)_ |
| * | `/build/start`, `/build/session(s)`, `/build/prompt`, `/build/propose`, `/build/proposal(s)` | legacy single-Builder flow, superseded by the Cortex lane flow _(unused by UI)_ |
