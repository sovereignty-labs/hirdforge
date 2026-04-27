# Hirdforge UI 1.0 — Specification

*S119, post-strawman. This document is the contract.*

---

## 0. How to use this document

This spec is the answer to every drift question that arises during the build. If a builder isn't sure how a surface should behave, they read this. If a question isn't answered here, they ask Kit before building rather than guessing. If something here turns out to be wrong, the spec gets amended via PR — never silently overridden in code.

The v0.9 strawman (`docs/ui/strawman.jsx`) is the visual/layout reference. This spec is the behavior/data/contract reference. They co-exist.

**Anti-drift rules:**
- Surface skeleton (left rail + main artifact + activity timeline) is locked. No surface deviates from this skeleton.
- Cosmetic refinement allowed. Layout shape changes require a spec amendment.
- Backend additions called out in §5 are the only new capabilities the UI assumes. Any UI element that requires a different backend addition needs its own callout added before being built.

---

## 1. Scope

**In scope for 1.0:**
- New UI shell deployed at `/ui/` on the existing gateway (side-by-side with legacy at `/`)
- Layout shell: top bar, 50/50 split body, footer
- Comms left half (warband rail + chat with active agent + input)
- Activity surface right half: agent strip, watching context bar, four surface types
- Builder surface (Ivar pattern)
- Action surface (Jeeves pattern)
- Reviewer surface (Sindri pattern — stub-then-fill, see §3.4)
- Architect surface (Ragnar pattern — stub-then-fill, see §3.5)
- Settings menu
- Full Realm tab (cluster, ArgoCD, Gitea PRs, certs)
- Header shield with pending approval count
- Dark scrollbars and dark resize handles

**Out of scope for 1.0:**
- Replacing the legacy UI at `/`. Legacy stays until cutover after parity.
- Reorganizing existing endpoints. The new UI consumes what's there.
- Authentication changes. The existing whoami flow stands.
- Any agent runtime changes beyond the typed tool events called out in §5.

**Hirdforge 1.0 ship benchmark** (broader context — this UI is one chunk):
- This UI built and live
- Delegated autonomy work shipped
- Forgedoc + Helm packaging ready
- All four surfaces real and working
- Agents, gateway, Seidr, Lockbox proven against the new UI

---

## 2. Layout Contract

```
┌────────────────────────────────────────────────────────────────┐
│  TOP BAR (h-10)                                                │
│  [logo HIRDFORGE]  [Comms][Warriors][Realm]   [shield N] user │
├────────────────────────────┬───────────────────────────────────┤
│  LEFT HALF (50%)           │  RIGHT HALF (50%)                 │
│  ┌──────┬─────────────────┐│  ┌─────────────────────────────┐  │
│  │      │                 ││  │  ACTIVE STRIP (wraps)       │  │
│  │ Wrbd │  Tab content    ││  │  [Ragnar][Ivar][Jeeves]…   │  │
│  │ rail │                 ││  ├─────────────────────────────┤  │
│  │      │                 ││  │  CONTEXT BAR (h-fixed)      │  │
│  │      │                 ││  │  Watching X · task line     │  │
│  │      │                 ││  ├─────────────────────────────┤  │
│  │      │                 ││  │                             │  │
│  │      │                 ││  │  SURFACE                    │  │
│  │      │                 ││  │  (skeleton: rail|artifact)  │  │
│  │      │                 ││  │  + activity timeline below  │  │
│  │      │                 ││  │                             │  │
│  └──────┴─────────────────┘│  └─────────────────────────────┘  │
├────────────────────────────┴───────────────────────────────────┤
│  FOOTER (h-6)  cluster · nodes · sync state                    │
└────────────────────────────────────────────────────────────────┘
```

**Fixed dimensions:**
- Top bar: 40px tall
- Footer: 24px tall
- Left/right split: 50/50 by default
- Warband rail in left half: 176px (`w-44`)
- Active strip and context bar: auto-height (strip wraps as fleet grows)

**Resizable dimensions:**
- Surface left rail (the tree/context column): 160–500px, default 260px, drag handle on right edge
- Activity timeline: 60–400px tall, default 176px, drag handle on top edge

**Color tokens:**
- Background: `zinc-950`
- Surfaces: `zinc-900`, `zinc-900/50`, `zinc-900/30`
- Borders: `zinc-800`
- Resize handles: transparent default, `zinc-800` hover, `zinc-700` drag
- Scrollbars: 8px, `zinc-800` thumb, transparent track, `zinc-700` thumb hover
- Forge accent (active work, focus, primary action): `amber-400`/`amber-500`
- Health/active: `emerald-400`
- Read state (recent file access): `blue-400`
- Text: `zinc-100` primary, `zinc-300/400/500/600` graduated

**Tabs:** Comms, Warriors, Realm. Tabs scope only the left half. The right half (activity surface) persists across tab changes — switching tabs on the left does not affect what's being watched on the right.

---

## 3. Surfaces

Every surface obeys the same skeleton:

```
[ left rail ] | [ main artifact ]
        + activity timeline below (full width of right half)
```

The left rail gives spatial/contextual orientation. The main artifact is the thing the agent is producing or acting on. The activity timeline is the chronological history of what's happened in this task. Resize handles between rail and artifact (horizontal drag) and between artifact and timeline (vertical drag).

When an agent is idle, the surface area shows a simple "agent idle" state — no rail, no artifact, just a centered marker.

### 3.1 Builder Surface (Ivar pattern)

**Triggered when:** focused agent is actively executing a coding task — clone, edit, commit, push, PR.

**Left rail:** repository tree of the repo the agent is currently in. Tree shows the directory structure, expanded around files the agent has touched or is touching. Each file has a state:
- `writing` — agent is currently editing this file. Amber background, animated pulse dot, left border accent. Only one file should be in `writing` state at a time per agent.
- `read` — agent has read this file in the current task. Subtle blue dot. Recency annotation (e.g. "70s ago"). Should fade with time.
- `clean` — file in the repo, not touched in this task. Muted text, no annotation.

Below the tree: a small legend explaining the state colors, plus an "other agents in this repo" line that lists any other warband members currently working in the same repo (with their avatars/initials). Empty state if none.

**Main artifact:** the file the agent is currently editing. Header shows file path, an amber "writing" indicator. Body is the file content with line numbers. The line currently being written has an amber background tint and a blinking caret at the end.

**Activity timeline:** chronological tool calls for this task. Each row: icon, action description, relative time. States: completed (`zinc-400` text), current (`amber-300` text), pending (`zinc-600` text, dimmed icon). Examples:
- "cloned kit/asgard-infra"
- "created branch ivar/argocd-rbac-fix"
- "read rbac/agent-roles.yaml"
- "writing rbac/gateway-argocd.yaml" (current)
- "validate yaml" (pending)
- "commit and push" (pending)
- "open PR" (pending)

**Action bar:** none. The writing IS the action.

### 3.2 Action Surface (Jeeves pattern)

**Triggered when:** focused agent is doing non-code work — email, calendar, alarms, camera, document drafting, generic API calls. Whether or not an approval is currently pending. The surface persists across the task's lifecycle.

**Left rail:** trigger context + plan checklist.
- Top: the trigger that initiated this task (e.g. the inbound email being replied to, the calendar request, the user instruction). Compact, scrollable if long.
- Below: a checklist of plan steps with state markers. Completed steps with green check. Current step with amber clock. Pending steps muted. Same visual vocabulary as the activity timeline but at higher abstraction.

**Main artifact:** the artifact being produced or acted on, rendered in its native shape.
- Email: To/Subject/Body fields, formatted as if in a composer.
- Calendar event: invitee list, time slot, agenda, location.
- Alarm/automation: device, target state, schedule.
- Camera feed: live view (or thumbnail), retention setting, duration.
- Document: rendered preview of the content.
- Generic API call: endpoint URL, method, payload preview, expected response.

Header shows artifact type, current Lockbox tier badge if a write is queued (`destructive_write · awaiting approval` or similar), and any classification.

**Activity timeline:** task-level chronological events specific to non-code work:
- "received email from Sarah Chen"
- "classified as response-needed"
- "checked calendar — Fri 2pm available"
- "composed reply"
- "queued send for Sovereign approval" (current)
- "send email" (pending)
- "send calendar invite" (pending)

**Action bar:** present only when an action is queued for approval. Shown as a footer of the artifact pane (above the activity timeline). Contents:
- Left: Lockbox queue id reference (e.g. `Lockbox · #queue-7af2`)
- Right: three buttons — Reject, Revise, Approve & send
- "Revise" sends the agent back to refine the artifact based on Kit's note (uses Comms input or a dedicated revision panel — TBD in implementation, not in 1.0 spec)

When no action is queued, the action bar is absent and the artifact pane fills the same space.

### 3.3 Reviewer Surface (Sindri pattern)

**Triggered when:** focused agent is reviewing a PR.

**Left rail:** changed-files tree for the PR being reviewed.
- Tree shows only files changed in the PR, grouped by directory.
- Each file annotated with diff stats (+lines/-lines).
- File state shows reviewer progress: `unread`, `viewing`, `reviewed-approved`, `reviewed-comment`, `reviewed-blocking`.

**Main artifact:** the diff viewer for the file currently being reviewed.
- Side-by-side or unified diff (configurable, default unified).
- Comment threads inline with the diff lines.
- The agent's auto-generated review notes inline as draft comments.

**Activity timeline:** review-specific events:
- "fetched PR #210 metadata"
- "read PR body"
- "reading cmd/gateway/main.go (diff)"
- "drafted comment on line 482"
- "marked file rbac/agent-roles.yaml as approved"
- "approved PR" (pending — awaiting Sovereign confirmation if Castle repo)

**Action bar:** present when review is complete and awaiting submission. Buttons: Comment, Request Changes, Approve. For Castle repos, "Approve" only marks the agent's review — Sovereign still merges separately.

### 3.4 Architect Surface (Ragnar pattern)

**Triggered when:** focused agent is coordinating multi-agent work — decomposing objectives, dispatching, tracking the lifecycle pipeline.

**Left rail:** active task tree.
- Top: the current strategic objective (one-sentence summary).
- Below: dispatched tasks as a tree. Each task shows assigned agent, current status (queued/running/reviewing/merged/failed), and PR number if applicable.
- Visual indicators connect parent objective → child tasks.

**Main artifact:** the dispatch detail for the task currently selected in the rail. Shows:
- Task description
- Acceptance criteria
- Assigned agent and dispatch timestamp
- Current state in the lifecycle (INTAKE, DISPATCH, BUILD, REVIEW, ESCALATE, VERIFY, CLOSE)
- Linked Gitea issue and PR
- Live status feed (subset of the architect's events filtered to this task)

If no task selected, the artifact area shows the dispatch decomposition view — the strategic objective at the top, and Ragnar's decomposition reasoning below.

**Activity timeline:** coordination events for the current strategic objective:
- "received objective from Sovereign"
- "decomposed into 3 tasks"
- "dispatched task-1 to Chuck"
- "dispatched task-2 to Sindri"
- "Chuck reported PR #145 ready for review"
- "dispatched review to Freya"
- "Freya approved"
- "merged" (current/pending)

**Action bar:** none. Architect work is dispatch and coordination — actions happen via delegate calls, not via UI buttons. The Interject button in the context bar is sufficient.

### 3.5 Idle / Unrecognized State

**Triggered when:** focused agent is idle or in a state the UI doesn't yet have a surface for.

Centered placeholder with the agent's name and a one-line status. No rail, no artifact, no timeline.

---

## 4. Tab Contracts (Left Half)

### 4.1 Comms Tab

Default tab on load.

**Warband rail:** vertical list of all agents in the warband. Each entry shows status dot, name, role. Click to switch the active conversation in the chat pane. The currently-conversed-with agent is visually distinct (border or background highlight).

**Chat pane:**
- Top: header showing the agent currently in conversation (status dot, name, role, model).
- Middle: scrollable message list. Messages render with speaker label, timestamp, and content. Sovereign messages right-aligned with amber accent; agent messages left-aligned with neutral surface.
- Bottom: input box with placeholder "Message {agent}…" and a send button. Enter to send, Shift+Enter for newline.

**Streaming behavior:** while the agent is responding, an in-progress message appears with a subtle pulse indicator. Tool calls during the response surface as inline event chips ("tool: gitea_file_read", "tool: exec git clone…") within the streaming message body.

### 4.2 Warriors Tab

Fleet roster view. Each agent gets a card showing:
- Status, name, role, model
- Health (uptime, requests served, tool calls made)
- Current task summary (if active)
- Pause/Resume button
- Inject (send Sovereign-tagged message) button

This tab is where agents get managed, not chatted with. Click any agent's card to jump to that agent in Comms.

### 4.3 Realm Tab

Multi-section infrastructure view. Sections:

1. **Cluster**
   - Pods table (name, namespace, status, node, restarts, age, ready, image)
   - Nodes table (name, status, roles, kubelet version, CPU%, memory%)
   - Click any pod → expandable detail with logs

2. **GitOps (ArgoCD)**
   - Application status: sync state, health, last sync
   - Drift indicator (shows when deployed SHA differs from git head)
   - Recent deployments (image, SHA, deployed-at)
   - Force-sync button (calls `/api/v1/gitops/sync`)

3. **Gitea**
   - Open PRs across watched repos. Each row: repo, number, title, author, age, review status.
   - Click any PR → full detail with diff and merge button (when authorized)

4. **Certs**
   - Cert expiry table for cluster and ingress certs

5. **K8s Events**
   - Recent cluster events stream (from `/api/v1/k8s/events`)

Sections collapsible. Default state: Cluster + GitOps expanded, others collapsed.

---

## 5. Backend Mapping

For each visible UI element, this section identifies the data source. **bold = exists today**. *italic = needs new capability*.

### 5.1 Top Bar

| Element | Source |
|---|---|
| Sovereign user identity | `GET /api/v1/whoami` (**exists**) |
| Pending approvals count + shield | `GET /api/v1/approvals` (**exists**) — count items with status="pending" |
| Tab navigation | client-side state |

### 5.2 Comms Tab (Left Half)

| Element | Source |
|---|---|
| Warband rail (agent list) | `GET /api/v1/agents` (**exists**) |
| Active agent identity in chat header | client-side state |
| Send message | `POST /api/v1/message` (**exists**, SSE response) |
| Streaming response chunks | SSE from `/api/v1/message` (**exists**) |
| Inline tool-call event chips during stream | *needs typed tool events — see §5.7* |

### 5.3 Active Strip + Context Bar (Right Half)

| Element | Source |
|---|---|
| Active agents list with status | `GET /api/v1/fleet/state` (**exists**) |
| Per-agent task summary on chip | *needs new field on fleet/state response: `current_task_summary`* |
| Event flag dot on chip | `WS /ws/events` (**exists**) — flag when type ∈ {`pr_created`, `task_complete`, `task_failed`, `injection_queued`} for that agent |
| "Watching X" identity | client-side state from chip click |
| Interject button | wires to `POST /api/v1/notify` or message endpoint (**exists**) |

### 5.4 Builder Surface

| Element | Source |
|---|---|
| Repo tree structure | `GET /api/v1/gitea/repo?owner=X&repo=Y` (**exists**) — fetches tree |
| File state (writing/read/clean) | *needs per-agent workspace state — see §5.7* |
| Currently editing file path | *needs per-agent workspace state* |
| File content for editor view | `GET /api/v1/gitea/repo` content fetch (**exists** — used by recon) + live overlay from agent write events |
| Live writing cursor / line highlight | *needs typed `file_write` events with line/position* |
| Activity timeline events | `WS /ws/events` (**exists**) filtered by agent — but *needs typed events for granularity* |

### 5.5 Action Surface

| Element | Source |
|---|---|
| Trigger context (e.g. inbound email) | *needs Lockbox trigger context exposed — see §5.7* |
| Plan checklist | *needs structured plan from agent — see §5.7* |
| Artifact preview (email body, etc.) | `GET /api/v1/approvals` (**exists**) — `params` field carries action params |
| Lockbox tier badge | `GET /api/v1/approvals` (**exists**) — derive from `service`+`action` |
| Approve / Reject buttons | `POST /api/v1/approvals/approve`, `/reject` (**exists**) |
| Revise button | *needs new endpoint: `POST /api/v1/approvals/revise` with revision note* |

### 5.6 Reviewer Surface

| Element | Source |
|---|---|
| PR metadata | `GET /api/v1/gitea/prs/{owner}/{repo}/{index}` (**exists**) |
| Changed-files tree | *needs new endpoint or extend gitea proxy: `GET /api/v1/gitea/prs/.../files`* |
| Diff content per file | *needs new endpoint: `GET /api/v1/gitea/prs/.../diff?file=X`* |
| Inline comment threads | *needs new endpoint: `GET /api/v1/gitea/prs/.../comments`* |
| Submit review (comment/request-changes/approve) | *needs new endpoint: `POST /api/v1/gitea/prs/.../review`* |

### 5.7 Architect Surface

| Element | Source |
|---|---|
| Active strategic objective | *needs new endpoint or session metadata field* |
| Dispatched task tree | `GET /api/v1/tasks` + `GET /api/v1/delegation-timeline` (**both exist**) |
| Per-task lifecycle state | `GET /api/v1/tasks/{id}` (**exists**) |
| Linked PRs | join with `GET /api/v1/gitea/prs` (**exists**) |
| Coordination events | `GET /api/v1/delegation-timeline?session_id=X` (**exists**) |

### 5.8 Realm Tab

| Element | Source |
|---|---|
| Pods | `GET /api/v1/cluster/pods` (**exists**) |
| Nodes | `GET /api/v1/cluster/nodes` (**exists**) |
| Pod logs | `GET /api/v1/cluster/pods/{name}/logs` (**exists**) |
| ArgoCD sync status | `GET /api/v1/gitops/status` (**exists** — RBAC fixed S118 PR #211) |
| ArgoCD force-sync | `POST /api/v1/gitops/sync` (**exists**) |
| Open PRs | `GET /api/v1/gitea/prs` (**exists**) |
| PR merge | `POST /api/v1/gitea/prs/{owner}/{repo}/{index}/merge` (**exists**) |
| Certs | `GET /api/v1/cluster/certs` (**exists**) |
| K8s events | `GET /api/v1/k8s/events` (**exists**) |

### 5.9 Settings Menu

| Element | Source |
|---|---|
| Settings list | `GET /api/v1/settings` (**exists**) |
| Update setting | `PUT /api/v1/settings/{key}` (**exists**) |
| Per-agent settings | `GET/PUT /api/v1/settings/agents/{name}` (**exists**) |

Settings menu accessed via gear icon in the top bar (to be added). Locked decision (S119): right-side drawer (~360px, dismissable, keeps warband visible).

### 5.10 Backend Capability Additions Required

The following are **new** capabilities the gateway and/or agent runtime must add for this UI to work fully. Each is a small project; together they're the core backend deliverable for UI 1.0.

**A. Typed tool events.** The agent runtime currently emits a generic `tool_call` event with a free-text summary for most tool invocations (gateway main.go ~line 4272). The UI needs typed events with structured payloads:

- `file_read` — params: `{repo, branch, path, bytes}`
- `file_write` — params: `{repo, branch, path, line_start, line_end, status: "writing"|"complete"}`
- `git_clone` — params: `{repo, branch, target_path}`
- `git_branch_create` — params: `{repo, branch_from, branch_new}`
- `git_commit` — params: `{repo, branch, message, files_changed}`
- `git_push` — params: `{repo, branch, remote}`
- `pr_create` — params: `{repo, number, title, branch_from, branch_to}` (already partially typed as `pr_created`)
- `exec` — params: `{command, working_dir, exit_code}`
- `http_request` — params: `{method, url, status_code}`

Owner: agent runtime (`pkg/tools/`). The runtime already has the data — it just needs to emit structured events instead of a single summary string.

**B. Per-agent workspace state.** The gateway needs a goroutine/struct that consumes the typed tool events from each agent and maintains a workspace projection per agent:

```go
type AgentWorkspace struct {
    AgentName     string
    CurrentRepo   string
    CurrentBranch string
    CurrentFile   string  // file currently being written, if any
    FilesTouched  map[string]FileState  // path → state
    LastUpdated   time.Time
}

type FileState struct {
    Path       string
    State      string  // "writing", "read", "clean"
    LastAccess time.Time
    LineCursor int  // current writing position, if state=="writing"
}
```

New endpoint: `GET /api/v1/agents/{name}/workspace` returns this struct. UI polls or subscribes (via a new subset of /ws/events filtered to workspace updates) to drive the Builder surface left rail and editor view.

Owner: gateway (`cmd/gateway/main.go`). New file likely: `pkg/workspace/projector.go`.

**C. Lockbox trigger context.** Each approval queue item should carry the trigger that produced it (the inbound email, the Sovereign instruction, the recurring schedule, etc.) so the Action surface left rail can display it. Currently `approvalQueueItem.Params` is a free-form map; we should standardize a `Trigger` field with a typed structure.

Owner: Lockbox (`cmd/lockbox/main.go`).

**D. Plan structure.** The Action surface left rail shows a plan checklist. This requires the agent to produce a structured plan when it begins a non-code task. Two options:
- (preferred) Agent emits a `plan` event with a list of steps, then `plan_step_complete` events as it progresses. UI renders from those.
- (fallback) Plan is inferred from activity events — every event is a plan step.

Recommend the preferred option for clarity. Owner: agent runtime + skill files.

**E. PR review surface backend.** The Gitea proxy needs to expose:
- PR file list with diff stats
- Diff content per file
- Comment threads
- Review submission (approve / request-changes / comment)

Owner: gateway. New handlers in the existing `/api/v1/gitea/prs/` route group.

**F. Approval revision endpoint.** When Kit clicks "Revise" on a queued approval, the action gets sent back to the agent with a Sovereign note. New endpoint: `POST /api/v1/approvals/{queue_id}/revise` with body `{note: string}`. Lockbox marks the queue item as "revising" and the agent is notified.

Owner: Lockbox + gateway.

---

## 6. Implementation Sequence

The build is phased. Each phase ends with something working and reviewable. **Phases land sequentially — no parallel branches across phase boundaries.** Within a phase, parallel dispatch is fine when work is in different files.

### Phase 0 — Snapshot

**Goal:** preserve the legacy UI as a reference.

- Create branch `legacy-ui-snapshot` containing current `cmd/gateway/index.html` at session-start SHA. No PRs against this branch; pure archive.
- Tag the snapshot commit `legacy-ui-pre-v1`.

Single-PR phase. Owner: surgeon (Claude direct, expedience).

### Phase 0.5 — Obliterate Temporal (Asgard scope only)

**Goal:** Remove all Temporal references from the Asgard-side codebase. Valhalla cluster Temporal is Deep Lab's responsibility — they handle their own teardown when syncing with Asgard.

Three concurrent sub-phases:
- **A.** Gateway code: remove `gw.temporalClient`, `/api/v2/message` handler, `pkg/workflows/` package, all Temporal SDK imports.
- **B.** (Deep Lab scope, not ours.)
- **C.** Docs cleanup: scrub Temporal references from repo-tracked documentation.

### Phase 1 — Shell at /ui/

**Goal:** New UI shell exists and serves the layout. No surfaces. No real data.

- Add new route `/ui/` to gateway serving a new embedded HTML/JS bundle.
- Bundle implements the layout shell: top bar, 50/50 split, footer. All the static structure from the strawman.
- Stubs for tabs, strip, context bar, surface area. Mock data inline.
- Resize handles (tree + activity) functional with mock state.
- Dark scrollbars, dark resize handles, color tokens.
- Footer reads "v1.0 · scaffolding".

Acceptance: `/ui/` loads in browser, layout matches strawman v0.9, tab clicks switch placeholder content, resize handles work.

Owner: builder. Single PR.

### Phase 2 — Comms (Left Half)

**Goal:** Left half is real. Talking to agents works in the new UI.

- Warband rail wired to `/api/v1/agents`.
- Chat pane wired to message endpoint with SSE streaming.
- Input sends, response streams in.
- Inline tool call chips render as raw `tool_call` events (no typed events yet — that comes in Phase 5).

Acceptance: switching agents in the rail switches the conversation. Sending a message gets a streamed response. Tool calls visible as generic chips.

Owner: builder. 1–2 PRs.

### Phase 3 — Active Strip + Context Bar + Idle Surface

**Goal:** Right half top is real. Switching focus works. Surfaces just show the idle state.

- Active strip wired to `/api/v1/fleet/state`.
- Strip wraps to multiple rows when agent count exceeds first row.
- Context bar shows watching state from client selection.
- Idle/placeholder surface for all agents — show last-task contents fully rendered but greyed out, live indicators off (locked S119).
- Header shield wired to `/api/v1/approvals` count.

Acceptance: clicking a chip changes the watching agent in the context bar. Shield shows correct count.

Owner: builder. 1 PR.

### Phase 4 — Settings Menu + Realm Tab

**Goal:** Tabs other than Comms become real. (Sequenced before surfaces because they unblock operational visibility.)

- Settings menu (gear icon → right drawer per S119 lock, reading/writing `/api/v1/settings`).
- Warriors tab: card grid wired to `/api/v1/agents` health.
- Realm tab: all five sections (Cluster, GitOps, Gitea, Certs, K8s Events) wired to existing endpoints.

Acceptance: Realm tab shows live ArgoCD state, pods, nodes, PRs. Settings menu can update at least one setting.

Owner: builders. Decompose into ~3 PRs (one per section group).

### Phase 5 — Backend: Typed Tool Events + Workspace Projector

**Goal:** Backend additions A and B from §5.10 land. Surfaces aren't built yet — this is pure plumbing.

- Agent runtime emits typed events for the file/git operations listed in §5.10-A.
- Gateway adds the workspace projector + `/api/v1/agents/{name}/workspace` endpoint.
- WS subscription path for workspace updates (subset of `/ws/events`).

Acceptance: querying `/api/v1/agents/{name}/workspace` while a builder agent is mid-task returns accurate state. New event types visible in `/ws/events` stream.

Owner: builders. 2–3 PRs (runtime change + gateway projector + endpoint).

### Phase 6 — Builder Surface

**Goal:** Watching a builder agent shows the real Builder surface from the strawman, fed by Phase 5 plumbing.

- Repo tree rendered from gitea repo fetch + workspace state overlay.
- File editor renders current file content with live writing cursor.
- Activity timeline rendered from typed events.

Acceptance: dispatch a coding task to a builder agent, switch to them in the strip, watch the tree update as they read files, watch the file fill in as they write, watch the timeline progress.

Owner: builder. 2 PRs (tree + editor; timeline integration).

### Phase 7 — Backend: Lockbox Trigger Context + Plan Structure + Revise Endpoint

**Goal:** Backend additions C, D, F from §5.10.

- Lockbox approval queue items carry trigger context.
- Agents emit structured plans for non-code tasks.
- `/api/v1/approvals/{queue_id}/revise` endpoint added.

Owner: builders. 2 PRs.

### Phase 8 — Action Surface

**Goal:** Watching a PA-class agent shows the real Action surface, fed by Phase 7 plumbing.

- Left rail with trigger + plan.
- Artifact pane with email/calendar/etc. preview.
- Action bar with Approve/Reject/Revise buttons wired to Lockbox.
- Revise flow renders as in-surface revision panel above the action bar (locked S119 — no modal).
- Activity timeline rendered.

Acceptance: trigger a real email via the PA, surface shows trigger, plan, artifact, action bar. Approval buttons work.

Owner: builder. 1–2 PRs.

### Phase 9 — Backend: PR Review Endpoints

**Goal:** Backend addition E from §5.10.

Owner: builder. 1 PR.

### Phase 10 — Reviewer Surface

**Goal:** Watching a reviewer-class agent shows the real Reviewer surface.

- Diff viewer supports both side-by-side and unified, toggle in header, default side-by-side (locked S119).

Owner: builder. 1–2 PRs.

### Phase 11 — Architect Surface

**Goal:** Watching a coordinator-class agent shows the real Architect surface.

- Dispatch viz rendered as directed graph with status-colored nodes (locked S119 — not tree-list).

Owner: builder. 1–2 PRs.

### Phase 12 — Cutover

**Goal:** New UI becomes the default.

- Route `/` to redirect to `/ui/`.
- Or: serve the new UI bundle at `/` directly.
- Legacy UI removed from `cmd/gateway/index.html`. Snapshot remains on the `legacy-ui-snapshot` branch.

Owner: surgeon. 1 PR.

**Total estimate:** 12 phases, ~18–25 PRs, sequenced. Phases 5/7/9 (backend) can be started in parallel with the prior surface phase to reduce wall-clock time, but every phase ships before the surface that depends on it.

---

## 7. Locked Decisions (S119)

These were the open questions resolved during S119 design lock. Recorded here as audit trail:

1. **Settings menu form factor.** Locked: right-side drawer (~360px, dismissable, keeps warband visible). (Phase 4)
2. **Diff viewer style.** Locked: side-by-side and unified, toggle in header, default side-by-side. (Phase 10)
3. **Architect dispatch viz.** Locked: directed graph with status-colored nodes. (Phase 11)
4. **Revise flow.** Locked: in-surface revision panel above the action bar — no modal. (Phase 7/8)
5. **Idle surfaces.** Locked: last task contents fully rendered but greyed out, live indicators off. (Phase 3)

---

## 8. What This Spec Does Not Cover

- **Visual polish beyond v0.9 fidelity.** That's a Claude Design pass after Phase 12 cutover, optional.
- **Mobile layout.** 1.0 is desktop-only.
- **Multi-Sovereign / multi-user.** 1.0 assumes single user (Kit). Permissions stay at the OAuth2 Proxy layer.
- **Notifications outside the UI** (Discord webhook etc. — those flows already exist and are unchanged).
- **Data retention / TTL of workspace state.** The projector keeps in-memory state per agent; on gateway restart, agents re-emit on resume. No persistence layer in 1.0.

---

## 9. Identity-Free Surface Selection

**Hard rule:** specific agent names never gate UI rendering. Surfaces select based on activity (Builder/Action/Reviewer/Architect/Idle), not on declared role and never on agent identity. The strawman's references to "Ivar pattern," "Jeeves pattern," etc. are illustrative — the real implementation must work for any agent in those activity modes regardless of name.

---

*This spec is the contract. Amendments land via PR against this file.*