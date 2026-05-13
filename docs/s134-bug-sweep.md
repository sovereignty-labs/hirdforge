# Hirdforge Bug Sweep — S134

## Context

Hirdforge is a Kubernetes-native multi-agent AI orchestration platform. Agents run as pods in the `asgard` namespace. They communicate via gRPC (`:8082`) and HTTP (`:8081`). A central **gateway** pod (`cmd/gateway/`) serves the UI (`cmd/gateway/index.html`, ~1,850-line single-file React SPA), handles event ingestion from agents, manages WebSocket broadcast to the UI, and coordinates the A2A task store.

**Source repo:** `kit/hirdforge` on `git.hirdforge.com`
**Go module path:** `github.com/kitporath/project_valhalla`

### Architecture Overview

```
Sovereign (human) → UI (gateway) → chieftain (coordinator) → warrior (builder)
                                                            → elder (reviewer)
                                  → sage (architect) → chieftain → ...
```

- **Gateway** (`cmd/gateway/`): HTTP server, WebSocket hub, event ingestion (`/api/v1/events`), A2A task store, Gitea webhook handler, UI serving
- **Agent binary** (`cmd/agent/`): All agents run the same binary with different `--tools`, `--soul`, and `--peers` flags
- **UI** (`cmd/gateway/index.html`): Single-file React SPA with workspace projectors, agent surfaces, Comms chat, session management
- **Event flow**: Agent → POST `/api/v1/events` → `gw.addEvent()` (ring buffer) → WebSocket broadcast → UI updates

### Key files

- `cmd/gateway/index.html` — the entire UI (~1,850 lines)
- `cmd/gateway/routes.go` — HTTP routing
- `cmd/gateway/delegation.go` — event handler for `/api/v1/events`, A2A task store, delegation tracking
- `cmd/gateway/webhooks.go` — Gitea webhook handler, PR review dispatch
- `cmd/gateway/ui.html` — possibly alternate/legacy UI file (check if still used)
- `cmd/agent/session.go` — agent session loop, tool calling, streaming
- `cmd/agent/tools.go` — tool implementations (read, write, exec, git-clone, etc.)
- `cmd/agent/a2a.go` — A2A protocol handling, gRPC server, gateway event posting

---

## Bugs — Ordered by Impact

### BUG 1: Builder (warrior) workspace surface is permanently empty

**Severity:** Critical — operators cannot observe what builders are doing

**Symptoms:**
- UI shows "No touched files yet" and "Awaiting file activity" for all warrior agents
- Gateway logs confirm events ARE being received: `event received: type=tool_call agent=warrior`
- 50+ tool_call events have flowed through the gateway with zero UI updates

**Where to look:**
1. `cmd/gateway/delegation.go` — the `/api/v1/events` handler. How does it process `tool_call` events? Does it call `applyWorkspaceEvent`? Does it associate events with the correct agent name and session?
2. `cmd/gateway/index.html` — the workspace projector. How does it subscribe to WebSocket events? What event types does it listen for? Does the agent name matching work?
3. Check the WebSocket broadcast — are tool_call events being sent over WebSocket at all, or only stored in the ring buffer?
4. Check if the workspace projector expects a specific event payload shape that the gateway isn't providing

**Expected behavior:** When a warrior calls `read`, `write`, `exec`, `git-clone`, etc., the builder surface should show the file operations, touched files, and command output in real time.

---

### BUG 2: Delegation chain is invisible in the UI

**Severity:** Critical — operators cannot follow sage→chieftain→warrior→elder

**Symptoms:**
- When chieftain delegates to warrior, warrior never shows as "active" in the UI
- Elder showed active briefly but warrior did not
- The Activity panel on chieftain's surface shows delegation events but can't drill into the delegated agent's activity
- No way to follow the full chain from the UI

**Where to look:**
1. `cmd/agent/a2a.go` — `postGatewayEvent`. When warrior receives a delegation, does it post `delegation_started` with the right agent name? (Logs confirm it does, so the issue is downstream)
2. `cmd/gateway/delegation.go` — when the gateway receives `delegation_started` from warrior, does it update the fleet state to show warrior as active?
3. `cmd/gateway/index.html` — the fleet status panel. How does it determine which agents are "active"? Does it rely on WebSocket events or polling?
4. Is there a concept of "watching" or "following" a delegation chain in the UI? If not, there should be.

**Expected behavior:** When chieftain delegates to warrior, the UI should show warrior as active, and clicking on warrior should show its live tool calls and output.

---

### BUG 3: Dispatched tasks counter double/triple counts

**Severity:** Medium — misleading metrics

**Symptoms:**
- Coordinator surface shows "Dispatched Tasks: 3" when only 1 task was delegated
- Both smoke tests showed this behavior
- PR #237 attempted to fix this with dedup by task_id but it persists

**Where to look:**
1. `cmd/gateway/index.html` — the `ArchitectTab` or coordinator surface component. How does it count dispatched tasks?
2. `cmd/gateway/delegation.go` — are duplicate events being generated? Check if `delegation_started`, `task_dispatched`, and possibly `delegation_ended` are all incrementing the same counter
3. The synthetic fallback list mentioned in PR #237 — is it still active and conflicting with the primary counter?
4. Check the `/api/v1/events` ring buffer — are duplicate events being stored?

**Expected behavior:** 1 delegation = 1 dispatched task in the counter.

---

### BUG 4: Raw token leakage — new format not caught by guardrails

**Severity:** Medium — breaks UI display

**Symptoms:**
- Chieftain output included raw: `<function=wait_for_task> <parameter=task_id> b7f691c4-... </parameter> </function>`
- This is a different format than what PR #241 regex catches (which handles Gemma `call:tool{...}thought` and Qwen `<|...|>` patterns)
- The `<function=...>` format is from a different chat template or model behavior

**Where to look:**
1. `cmd/agent/session.go` — the content scrubber / `controlTokenRE` regex. What patterns does it currently match?
2. The `<function=...> <parameter=...>` format — this looks like it might be coming from the model attempting a non-native tool call format. Check if the Qwen chat template is properly enforcing structured tool calls
3. `cmd/gateway/delegation.go` — the `controlTokenRE` in the streaming content filter. Add the `<function=` pattern

**Fix approach:** Add `<function=` and `</function>` to the control token regex. But also investigate WHY the model fell back to this format — it may indicate a chat template issue.

---

### BUG 5: Warrior completes but produces no PR

**Severity:** High — the build loop doesn't land artifacts

**Symptoms (from pod logs):**
1. `git-clone` — success
2. `exec` — success (probably echo/cat to create file)
3. `exec` — FAILED: "cat: qwen-validation.md: No such file or directory" (2 retries, all failed)
4. `exec` — success (unknown what it did)
5. `delegation_ended` — warrior reports done
6. Log: `self improvement skipped; required tool missing: gitea`

**Where to look:**
1. `cmd/agent/tools.go` — the `exec` tool implementation. When warrior runs `exec` to create a file, is the working directory correct? Is it inside the cloned repo?
2. The warrior used `exec` instead of `write` to create the file. With the tool list narrowed (PR #314 merged), `write` is now available and `gitea`/`http` are removed. Re-test after tool trim.
3. The "self improvement skipped; required tool missing: gitea" warning — what is "self improvement"? Is there post-task logic that expects a `gitea` tool? Is it trying to do something that fails and silently drops the PR creation step?
4. `cmd/agent/session.go` — after the tool loop completes, is there post-processing that depends on specific tools being available?

**Expected behavior:** Warrior should: clone → read existing files → write new file → verify → git-commit → create-pr → report back with PR URL.

---

### BUG 6: Chieftain delegates to wrong agent type

**Severity:** Medium — coordinator sends build tasks to reviewer

**Symptoms:**
- Second smoke test: chieftain was asked to delegate a file creation task to "one warrior"
- Chieftain delegated to elder (reviewer) instead
- Also decomposed a single-file task into 3 subtasks

**Where to look:**
1. This is likely a soul/prompting issue, not a code bug. But check:
2. `cmd/agent/session.go` — how is the `delegate` tool presented to the model? Does the tool description list available agents and their roles?
3. `cmd/agent/tools.go` — the `delegate` tool implementation. Does it validate that the target agent makes sense for the task type?
4. Check how `--peers` are presented in the system prompt. If the model sees a flat peer list with no role context, it may pick randomly.

---

### BUG 7: Gateway polls for non-existent warband org

**Severity:** Low — log noise, wasted API calls

**Symptoms:**
- Gateway polls `/api/v1/orgs/warband/repos` every 5 minutes
- Returns 404 because `warband` is a Gitea user, not an org
- Fills logs with errors

**Where to look:**
1. `cmd/gateway/` — search for the org polling code. It's likely in a goroutine that discovers repositories
2. Change it to query user repos (`/api/v1/users/warband/repos`) instead of org repos

---

### BUG 8: builder-01 pods crash-looping

**Severity:** Low — stale deployment

**Symptoms:**
- Two builder-01 pods with 22-23 restarts
- One is on an old image SHA
- builder-01 is likely a legacy deployment that should be cleaned up

**Where to look:**
- `kit/asgard-infra` — check if builder-01 deployment still exists and should be removed

---

### BUG 9: workspace-mcp stuck Pending

**Severity:** Low — unused service not scheduling

**Where to look:**
- `kit/asgard-infra` — check workspace-mcp deployment for resource requests, node affinity, or missing image

---

## Also Fix (from Claude Code independent audit)

These were found by Claude Code's independent codebase sweep and confirmed real:

### CC-1: Gateway nil-pointer panic on stopAgent/stopAllAgents
- `cmd/gateway/sessions.go:377` and `:387` call `ar.Cancel()` unconditionally
- `cmd/gateway/delegation.go:170` registers active request with `cancel=nil`
- Remote-trigger crash on `/api/v1/stop`
- **Fix:** `if ar.Cancel != nil { ar.Cancel() }` at both sites
- **STATUS: Fix in progress**

### CC-2: File writes not atomic
- `pkg/tools/file.go:143` and `:245` write directly via `os.WriteFile`
- Crash mid-write truncates the file
- **Fix:** Write to `.tmp` then `os.Rename`

### CC-3: Streaming channel send can outlive consumer
- `cmd/agent/main.go:951` — unbuffered channel send blocks if consumer stopped
- Goroutine + HTTP body leak
- **Fix:** `select { case chunks <- evt: case <-ctx.Done(): return }`

### CC-4: JSON handlers don't bound request body size
- `cmd/gateway/delegation.go` multiple handlers
- Unbounded `json.NewDecoder(r.Body).Decode(...)` 
- **Fix:** Wrap in `http.MaxBytesReader`

---

## Smoke Test Protocol

After fixes are applied, run this to validate the full chain:

Send to chieftain via Comms: "Create a file called smoke-test-s134.md in warband/bravo-docs with today's date. Delegate to one warrior. Report back when the PR is created."

**Pass criteria:**
- [ ] Chieftain delegates to ONE warrior (not elder, not multiple)
- [ ] Warrior shows as active in the UI fleet panel
- [ ] Warrior's builder surface shows tool calls in real time
- [ ] No raw token leakage in Comms
- [ ] Warrior creates a PR (visible in Gitea)
- [ ] Chieftain dispatched tasks counter shows 1 (not 2 or 3)
- [ ] Chieftain reports back with PR URL
- [ ] No repetition loops, no stuck states