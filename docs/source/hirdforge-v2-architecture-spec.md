# Hirdforge v2 Architecture Spec

## The Problem

Hirdforge's multi-agent coordination is fragile. The compound reliability problem: each LLM-dependent step succeeds ~80% of the time, and a 5-step pipeline (decompose → dispatch → build → review → merge-route) succeeds 0.8⁵ = 33% end-to-end. The chieftain/coordinator role — a stateful LLM managing a multi-phase pipeline — is the primary source of fragility. It loses track of task IDs, stalls in INTAKE, fills context, and fails silently.

Meanwhile, external single-agent tools (Claude Code, Codex CLI) reliably complete tasks because their pipeline is: human → agent → code → human review. Two steps, not five.

## The Fix

Replace LLM-driven coordination with infrastructure-driven coordination. Remove the chieftain role entirely. Introduce **Cortex** — a deterministic event-driven router that receives events, matches routing rules, assigns skills and memory context, and dispatches generic warriors. No LLM reasoning in the routing path.

## Core Principles

1. **LLMs do LLM work.** Thinking, coding, reviewing, designing. Not routing, tracking, or dispatching.
2. **Infrastructure does infrastructure work.** Event matching, skill assignment, memory scoping, lifecycle tracking, retry logic. Deterministic. Testable. Reliable.
3. **Skills are the unit of capability.** Not agent deployments, not roles, not pod names. A markdown file in git defines what an agent can do on any given task.
4. **Memory belongs to skills, not agents.** Knowledge accumulates around capabilities, not identities. Every warrior that loads a skill inherits all lessons learned through that skill.
5. **The human directs.** The concierge is the conversational interface. Cortex is the nervous system. Warriors are the hands. The Sovereign is always in command.

---

## Component Architecture

### 1. Cortex (Event-Driven Router)

**What it replaces:** TaskLB + the chieftain role + all LLM-driven coordination.

**What it is:** A Go module inside the gateway binary (not a separate service). Receives events from Gitea webhooks, the UI, the concierge, and agent completion callbacks. Matches events against declarative routing rules. Constructs dispatch messages with skill assignments and memory scopes. Dispatches to idle warriors. Tracks task lifecycle state.

**What it is NOT:** An LLM. Cortex never calls an inference endpoint. Every decision is rule-based and deterministic.

#### 1.1 Event Sources

| Source | Events |
|--------|--------|
| Gitea webhooks | `issue.created`, `issue.labeled`, `pull_request.opened`, `pull_request.merged`, `pull_request.closed`, `pull_request_review.submitted` |
| Concierge | `task.requested` (concierge creates an issue on behalf of the user) |
| Agent completion | `task.completed` (warrior reports PR URL or output), `task.failed` (warrior exceeded tool rounds or timed out) |
| UI / API | `task.manual_dispatch` (Sovereign dispatches directly from pipeline view) |
| Timer | `task.timeout` (configurable per-route, fires if dispatched task doesn't complete within window) |

#### 1.2 Routing Rules

Declarative YAML loaded at gateway startup. No code changes needed to add new routes.

```yaml
# cortex.yaml
skills_repo: kit/hirdforge-personas  # git repo containing skill files
skills_path: skills/                  # directory within repo

bundles:
  build-go:
    skills: [go-builder, git-pr-workflow, testing-patterns, code-quality]
  build-python:
    skills: [python-builder, git-pr-workflow, testing-patterns, code-quality]
  review:
    skills: [pr-review, code-quality]
  review-security:
    skills: [pr-review, security-checklist, code-quality]
  design:
    skills: [spec-design, architecture-patterns]
  validate:
    skills: [deployment-validator, smoke-test]
  docs:
    skills: [docs-writer, markdown-standards, git-pr-workflow]

routes:
  # Specs: issues labeled type/spec get the design bundle
  - event: issue.created
    match:
      labels: [type/spec]
    action:
      bundle: design
      strategy: idle-first
      timeout: 30m

  # Go builds: issues labeled lang/go get the Go build bundle
  - event: issue.created
    match:
      labels: [type/build, lang/go]
    action:
      bundle: build-go
      strategy: idle-first
      timeout: 45m

  # Python builds
  - event: issue.created
    match:
      labels: [type/build, lang/python]
    action:
      bundle: build-python
      strategy: idle-first
      timeout: 45m

  # PR opened on castle repos → review
  - event: pull_request.opened
    match:
      repo_owner: kit
      repos: [hirdforge, asgard-infra, seidr, lockbox]
    action:
      bundle: review
      strategy: idle-first
      exclude_author: true  # don't assign reviewer = author
      timeout: 20m

  # PR opened on persona repos → lighter review
  - event: pull_request.opened
    match:
      repo_owner: warband
    action:
      bundle: review
      strategy: idle-first
      timeout: 15m

  # Review approved on castle repo → notify sovereign
  - event: pull_request_review.submitted
    match:
      state: approved
      repo_owner: kit
    action:
      notify: sovereign
      message: "PR #{number} in {repo} approved by {reviewer}. Ready for merge."

  # PR merged → validate deployment
  - event: pull_request.merged
    match:
      repo_owner: kit
      repos: [hirdforge, seidr, lockbox]
    action:
      bundle: validate
      strategy: idle-first
      timeout: 10m
      delay: 120s  # wait for ArgoCD sync

  # Task failed → re-dispatch once to a different warrior
  - event: task.failed
    match:
      retry_count: 0
    action:
      redispatch: true
      strategy: idle-first-exclude-previous
      timeout: 45m

  # Task failed after retry → notify sovereign
  - event: task.failed
    match:
      retry_count: 1
    action:
      notify: sovereign
      message: "Task #{issue} failed after retry. Manual intervention needed."
```

#### 1.3 Dispatch Strategy

| Strategy | Behavior |
|----------|----------|
| `idle-first` | Pick the first idle warrior. Round-robin across idle warriors if multiple are available. |
| `idle-first-exclude-previous` | Same as idle-first but excludes the warrior that previously failed the task. |
| `affinity` | Prefer the warrior that worked on the same repo recently (has warm workspace). Fall back to idle-first. |

#### 1.4 Dispatch Message Construction

When Cortex dispatches, it constructs a deterministic prompt from the issue content and the assigned skill bundle. No LLM involved.

**Template:**
```
SKILLS: {comma-separated skill file paths}
MEMORY: {comma-separated Seidr scope names}

TASK: {issue title}
REPO: {target repo from issue label or body}
ISSUE: #{issue number}

{issue body}

DONE WHEN: {extracted from issue body, or default: "PR created with issue reference"}
```

The warrior's runtime loads the specified skill files and queries the specified Seidr scopes. The warrior never needs to decide what skills to load — Cortex decided for it.

#### 1.5 Lifecycle Tracking

Cortex maintains task state in PostgreSQL (extending the existing A2A task store):

```
Task {
  id:            uuid
  issue_number:  int
  issue_repo:    string
  status:        queued | dispatched | building | review | approved | merged | validated | failed
  warrior:       string (agent name)
  session_id:    string
  skills:        []string
  memory_scopes: []string
  pr_number:     int (set when warrior reports PR)
  reviewer:      string (set when review dispatched)
  retry_count:   int
  created_at:    timestamp
  updated_at:    timestamp
  timeout_at:    timestamp
}
```

Status transitions are event-driven:
- `issue.created` → `queued`
- Cortex dispatches → `dispatched` → `building`
- Warrior reports PR → `review` (triggers review route)
- Reviewer approves → `approved` (triggers sovereign notification)
- PR merged → `merged` (triggers validation route)
- Validator confirms → `validated` → issue closed
- Timeout or failure → `failed` (triggers retry or sovereign notification)

#### 1.6 Integration with Existing Gateway

Cortex is a new module within the gateway binary, not a separate service. It:
- Registers webhook handlers alongside existing ones (reuses `webhook.go` patterns and HMAC validation)
- Uses the existing `gateway.agents` map for fleet state
- Uses the existing `/api/v1/dispatch` endpoint mechanism (or an internal equivalent) for sending work to agents
- Extends the existing A2A task store for lifecycle tracking
- Exposes new endpoints under `/api/v1/cortex/` for the pipeline UI

**New gateway endpoints:**
```
GET  /api/v1/cortex/tasks              Task list with filters (status, warrior, repo)
GET  /api/v1/cortex/tasks/{id}         Task detail
POST /api/v1/cortex/tasks/{id}/retry   Manual retry
POST /api/v1/cortex/tasks/{id}/cancel  Manual cancel
GET  /api/v1/cortex/routes             Current routing configuration
GET  /api/v1/cortex/log                Recent routing decisions (ring buffer)
POST /api/v1/cortex/dispatch           Manual dispatch (Sovereign creates issue + triggers route)
GET  /api/v1/cortex/stats              Dispatch stats (tasks/day, success rate, avg duration)
```

---

### 2. Skill Files

**What they replace:** Role-specific agent deployments, agent-specific skill files, the role taxonomy as a deployment concern.

**What they are:** Markdown files in a git repository. Each skill file defines a capability, its memory scopes, and the instructions an agent needs to perform that capability. Skills are composable — multiple skills are loaded together per task.

#### 2.1 Skill File Format

```markdown
# go-builder.md
---
memory:
  - go-patterns
  - build-workflows
tools:
  required: [exec, read, write, edit, git-clone, git-commit, git-diff, gitea]
  optional: [http]
---

## You are building Go code in the Hirdforge codebase.

### Before You Start
1. Load this skill file completely before taking any action.
2. Clone the target repo if not already in your workspace.
3. Read the existing code around the area you'll be changing.
4. Check for an ARCHITECTURE.md or AGENTS.md in the repo root.

### Build Workflow
1. Create a branch: `{agent-name}/{issue-number}-{short-description}`
2. Make changes. Run `go vet ./...` and `go build ./...` after each logical change.
3. If tests exist, run them: `go test ./...`
4. Commit with a descriptive message referencing the issue.
5. Create a PR with `Closes #{issue-number}` in the body.

### Go Patterns
- This codebase uses stdlib patterns. No external frameworks.
- Error handling: return errors, don't panic. Wrap with context: `fmt.Errorf("failed to X: %w", err)`
- Logging: use `logJSON()` for structured JSON logs, not `log.Printf` in library code.
- HTTP handlers: validate input, return JSON via `writeJSON()`.

### Report Back
After creating the PR, report: `PR #{number} created in {owner}/{repo} on branch {branch}`
```

#### 2.2 Memory Scope Declaration

The `memory` field in the frontmatter declares which Seidr scopes this skill needs. When Cortex assigns this skill, it includes these scopes in the dispatch. The warrior's runtime queries these scopes at session start and writes lessons back to them at session end.

Multiple skills can share scopes. `go-builder` and `go-reviewer` both reference `go-patterns`. Lessons from building surface during review and vice versa.

#### 2.3 Tool Declaration

The `tools.required` field declares what tools the warrior MUST have for this skill to work. Cortex checks tool availability before dispatching — if no warrior has the required tools, the task is queued until one is available (or times out). The `tools.optional` field lists tools that are useful but not blocking.

#### 2.4 Skill Bundles

Bundles are defined in `cortex.yaml`, not in skill files. A bundle is an ordered list of skill file names. When Cortex dispatches with a bundle, the warrior loads all skills in order. Later skills can override or extend earlier ones.

#### 2.5 Adding New Capabilities

To add a new capability (e.g., Rust builds):
1. Write `rust-builder.md` with `memory: [rust-patterns, build-workflows]`
2. Add a bundle: `build-rust: [rust-builder, git-pr-workflow, testing-patterns, code-quality]`
3. Add a route: `event: issue.created, match: {labels: [type/build, lang/rust]}, action: {bundle: build-rust}`
4. Commit to git. Gateway reloads config.

No new deployments. No new pods. No Helm changes. No ArgoCD sync for agent infrastructure.

---

### 3. Generic Warriors

**What they replace:** Specialized agent deployments (chieftain, elder, sage, warrior, steward as separate pods).

**What they are:** Identical agent pods running the `valhalla-agent` binary with a minimal base SOUL, all tools enabled, and Seidr connected. The base SOUL contains only identity (agent name), core behavioral rules (call tools directly, don't narrate), and the delivery protocol (report PR numbers). No skill router table — Cortex handles skill assignment.

#### 3.1 Runtime Changes

The agent runtime needs one new capability: **accept skills and memory scopes from the dispatch message.**

Currently, the warrior receives a message via `/message` or `/tasks/send` with a `content` field. Extend the task request to include:

```json
{
  "content": "TASK: Implement retry backoff...",
  "session_id": "cortex-task-abc123",
  "skills": ["skills/go-builder.md", "skills/git-pr-workflow.md"],
  "memory_scopes": ["go-patterns", "build-workflows", "git-workflows"]
}
```

On receiving a task with `skills`:
1. Load each skill file from the persona repo (already cloned at startup via `--persona-repo`)
2. Inject skill content into the system prompt after the base SOUL
3. Query Seidr for the top memories from each specified scope, inject as context
4. Proceed with normal tool-calling loop

On task completion:
1. Any `/remember` calls during the task are tagged with the active memory scopes
2. Structured outcome memories (tool_failure, action_success) are written to the relevant scopes based on which skill was being used when the outcome occurred

#### 3.2 Base SOUL (Minimal)

```markdown
# {agent-name}

You are a Hirdforge warrior — a general-purpose AI agent.

## Rules
- Call tools directly. Never narrate what you're going to do.
- Read before you write. Understand existing code before changing it.
- One logical change per commit.
- Report results with PR numbers and branch names.

## Environment
- Gitea: {gitea-url}
- Git identity: {agent-name} <{agent-name}@asgard.local>
- Workspace: /workspace
```

That's it. ~10 lines. Everything else comes from skills loaded per task.

#### 3.3 Deployment

All warriors are identical deployments. The only differences are:
- `--agent-name` (for Seidr identity and git commits)
- Potentially `--inference-url` / `--model` (if mixing local and cloud inference)

A 4-warrior deployment is four copies of the same Helm template with different names.

#### 3.4 Scaling

Add warriors by adding deployments. Remove warriors by removing deployments. Cortex automatically discovers warriors through the existing gateway agent registration (`--agents` flag). No routing rules need to change — Cortex dispatches to any idle warrior.

---

### 4. Seidr Skill-Scoped Memory

**What changes:** Memory collections are organized by skill-declared scopes instead of agent names.

#### 4.1 Scope-Based Collections

Currently: `warrior-01-memory`, `warrior-02-memory` (per-agent).
New: `scope/go-patterns`, `scope/build-workflows`, `scope/git-workflows` (per-skill-scope).

When a warrior is dispatched with `memory_scopes: ["go-patterns", "build-workflows"]`, the runtime:
1. Queries `scope/go-patterns` and `scope/build-workflows` at session start
2. Injects results as bootstrap context
3. Writes any new memories (failures, successes, lessons) back to the relevant scopes

#### 4.2 Seidr API Changes

Minimal. The existing `/remember` and `/query` endpoints already accept a `collection` parameter. The only change is that collection names shift from `{agent-name}-memory` to `scope/{scope-name}`. This is a convention change, not an API change.

One new concept: the dispatch message specifies `memory_scopes`, and the runtime translates these to collection names when calling Seidr. The mapping is: scope name `go-patterns` → collection `scope/go-patterns`.

#### 4.3 Warband-Wide Memory

The existing `warband-context` shared collection continues to serve cross-cutting operational knowledge that isn't skill-specific. Bootstrap always includes warband-context regardless of assigned scopes.

#### 4.4 Memory Validation and Promotion

The validation pipeline designed earlier integrates directly:

1. When a SOUL amendment (now a **skill amendment**) is merged, a `skill_amendment` memory is written to the relevant scope with the failure pattern it addresses.
2. Seidr's cognitive processing compares new failures against `skill_amendment` memories. Recurrence = contradiction = amendment marked as ineffective.
3. Absence of recurrence over 30 days = amendment validated.
4. Validated amendments that appear across multiple scopes = `universal_candidate` in warband-context.
5. Concierge or Sovereign reviews universal candidates and promotes them to skill files via PR.

#### 4.5 Concierge Memory

The concierge is the ONE agent with persistent identity-level memory. Its collection is `concierge-{name}`, not scope-based. This stores user preferences, conversation history, and operational context that's specific to the human-agent relationship.

---

### 5. The Concierge

**What it replaces:** The chieftain as the human-facing agent.

**What it is:** A single named warrior with a persistent steward-focused SOUL and its own Seidr collection. The concierge is the agent the user talks to in Comms. It handles:

1. **Conversation** — answering questions, brainstorming, discussing strategy
2. **Triage** — understanding what the user wants and determining whether it's conversational (handle directly) or work (create an issue for Cortex)
3. **Issue creation** — writing well-structured issues with appropriate labels, acceptance criteria, and repo targets based on the user's request
4. **Status reporting** — querying Cortex's task state and summarizing pipeline status for the user
5. **Direct execution** — simple tasks (send an email via Lockbox, look up information, read a file) that don't need the full build pipeline

**What it does NOT do:** Dispatch agents. Track pipeline state. Manage multi-phase workflows. Route reviews. That's Cortex's job.

#### 5.1 Concierge SOUL

```markdown
# Concierge

You are the Sovereign's concierge — the human-facing interface to the Hirdforge warband.

## Your Job
- Talk to the Sovereign naturally. Understand what they want.
- If they want something BUILT, REVIEWED, or DESIGNED: create an issue on the task board.
- If they want conversation, information, or simple tasks: handle it directly.
- Never try to dispatch agents or manage work pipelines. That's infrastructure.

## Creating Issues
When the Sovereign wants work done, create a Gitea issue with:
- Title: clear, concise description of the task
- Labels: type/ (build, spec, review, docs), lang/ (go, python, yaml), priority/ (high, normal, low)
- Body: what needs to change, which repo, acceptance criteria
- Use the `gitea` tool with action `create-issue`.

## Skills
Load steward skills for email/calendar tasks via Lockbox.
Load no build skills — you don't build. Warriors build.

## Memory
You remember the Sovereign's preferences, ongoing projects, and conversation context.
```

#### 5.2 Concierge Deployment

One pod, always running, with the concierge SOUL. The concierge has Lockbox MCP access for PA tasks. It has the `gitea` tool for issue creation. It does NOT have `git-clone`, `git-commit`, or `git-diff` — it's not a builder.

---

### 6. Pipeline UI

**What it replaces:** The agent-centric Warriors tab and agent surfaces.

**What it is:** A task-centric operations dashboard showing work flowing through the pipeline.

#### 6.1 Pipeline View (New Primary Tab)

A kanban-style board showing all active and recent tasks:

```
┌─────────┐  ┌──────────┐  ┌────────┐  ┌─────────┐  ┌────────┐
│ QUEUED  │  │ BUILDING │  │ REVIEW │  │ READY   │  │ DONE   │
│         │  │          │  │        │  │         │  │        │
│ #318    │  │ #315     │  │ #312   │  │ #310    │  │ #308 ✓ │
│ #317    │  │ warrior-2│  │ war.-4 │  │ Merge?  │  │ #306 ✓ │
│         │  │ go-build │  │ review │  │         │  │ #304 ✓ │
│         │  │ 12m      │  │ 3m     │  │         │  │        │
└─────────┘  └──────────┘  └────────┘  └─────────┘  └────────┘
```

Each card shows: issue number + title, assigned warrior, skill bundle, elapsed time.

Click a card → detail panel:
- Full issue description
- Assigned skills and memory scopes
- Warrior's live tool calls (streaming)
- File tree of touched files
- PR link (when available)
- Review comments (when in review)
- Cortex routing decision log for this task

Cards in READY column have a merge button (uses existing PR merge endpoint).

#### 6.2 Cortex Log

A collapsible panel (bottom or sidebar) showing Cortex's routing decisions in real time:

```
[14:23:01] Issue #318 created (type/build, lang/go) → queued
[14:23:02] Route matched: build-go → dispatched to warrior-03 (idle)
[14:23:02] Skills loaded: go-builder, git-pr-workflow, testing-patterns, code-quality
[14:23:02] Memory scopes: go-patterns, build-workflows, git-workflows
[14:35:15] warrior-03 reported PR #289 → status: review
[14:35:16] Route matched: review (castle repo) → dispatched to warrior-01
[14:41:30] warrior-01 submitted review: APPROVED → status: approved
[14:41:31] Sovereign notified: PR #289 ready for merge
```

This is the system's pulse. At a glance, the Sovereign knows what Cortex is doing and why.

#### 6.3 Comms Tab (Mostly Unchanged)

The Comms tab stays but is now exclusively the concierge conversation. No agent picker dropdown — you're always talking to the concierge. Streaming responses, tool calls visible, same as today.

If the Sovereign needs to talk directly to a specific warrior (debugging, testing), a "Direct Message" mode in the pipeline detail panel allows sending a message to the warrior working on a specific task.

#### 6.4 Realm Tab (Unchanged)

Cluster health, pods, nodes, infrastructure. Unchanged from current implementation.

#### 6.5 Approval Panel (Unchanged)

Lockbox write-approval queue. Unchanged.

---

## Migration Path

This architecture is not a rewrite. It's a refactor that reuses most of the existing codebase.

### What Stays
- Agent runtime binary (warriors ARE the existing `valhalla-agent`)
- Gateway binary (Cortex is a new module inside it)
- Seidr (collection naming convention changes, no API changes)
- Lockbox (unchanged)
- Gitea + ArgoCD + CI pipeline (unchanged)
- MCP server on gateway (unchanged, extend with Cortex tools)
- All existing gateway endpoints (Cortex adds new ones, doesn't replace)
- Webhook handling infrastructure (Cortex builds on it)
- A2A task store (extended for Cortex lifecycle)
- Session management, SSE streaming, workspace projector (unchanged)

### What Changes
- **Gateway:** Add Cortex module (routing rules engine, dispatch, lifecycle tracking)
- **Agent runtime:** Accept `skills` and `memory_scopes` in task requests, load skill files from persona repo, scope memory operations to assigned scopes
- **UI:** Add Pipeline tab, add Cortex log panel, refocus Comms on concierge
- **Skill files:** Refactor existing shared skills into the new format with frontmatter
- **Deployments:** Replace role-specific deployments with N identical warrior deployments + 1 concierge

### What's Removed
- Chieftain/coordinator role and all associated skill files
- `--peers` topology for delegation (warriors don't delegate to each other — Cortex routes)
- Agent-specific Seidr collections for warriors (replaced by scope-based)
- Role-based agent labeling in Kubernetes (`asgard.io/tier`, `asgard.io/alias`)
- The Comms agent picker (replaced by concierge-only + direct message from pipeline)

---

## Implementation Sequence

### Phase 1: Cortex Core
Build the routing rules engine and dispatch mechanism inside the gateway. Parse `cortex.yaml`. Match Gitea webhook events to routes. Construct dispatch messages from issue content + skill bundle. Use existing `/api/v1/dispatch` for sending to agents. Track lifecycle in the A2A task store.

**Deliverable:** Cortex dispatches a warrior when an issue is created with matching labels. The warrior builds, reports PR, Cortex updates status.

### Phase 2: Skill-Based Dispatch
Extend the agent runtime to accept `skills` and `memory_scopes` in task requests. Load skill files from the persona repo. Scope Seidr operations to assigned scopes. Write existing shared skills in the new format with frontmatter.

**Deliverable:** Warriors load skills per-task and use skill-scoped memory.

### Phase 3: Review and Validation Routing
Add PR-event routing: PR opened → dispatch warrior with review skills. Review approved → notify sovereign. PR merged → dispatch warrior with validation skills. Implement retry-on-failure routing.

**Deliverable:** The full pipeline (issue → build → review → approve → merge → validate) runs with zero LLM coordination.

### Phase 4: Concierge
Deploy the concierge with its steward SOUL and Lockbox access. Issue creation from conversation. Status queries against Cortex.

**Deliverable:** The Sovereign talks to the concierge, the concierge creates issues, Cortex handles everything else.

### Phase 5: Pipeline UI
Build the pipeline view (kanban board), Cortex log panel, and task detail view. Refocus Comms on concierge-only conversation.

**Deliverable:** The Sovereign sees all work flowing through the system in one view.

### Phase 6: Seidr Validation Loop
Implement `skill_amendment` memory type, validation-via-absence, cross-scope promotion to universal candidates, and the promotion-to-skill-file pipeline.

**Deliverable:** The system measurably improves over time. Skills evolve through validated learning.

---

## Success Criteria

1. **End-to-end task completion rate >80%** (up from ~33% current compound reliability)
2. **Zero LLM-dependent steps in the coordination path** — all routing is deterministic
3. **New capability added in <10 minutes** — write a skill file, add a route, push to git
4. **Warband learns measurably** — track failure rates per skill scope over time, show improvement
5. **Sovereign can see everything** — pipeline view shows all work, Cortex log explains all decisions
6. **`helm install` to working warband in 15 minutes** — warriors + concierge + Cortex from a single Forgedoc

---

*Hirdforge v2 — the system that gets smarter every day, running on your hardware, under your control.*
