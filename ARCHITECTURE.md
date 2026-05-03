# Hirdforge Architecture

Hirdforge is a multi-agent AI orchestration platform. It runs a gateway UI and API, one or more agent binaries that execute model-driven work and tools, and a lockbox service that gates write-capable external integrations behind approval flows.

## Binaries And Packages

The repo is split between runtime binaries under `cmd/` and reusable packages under `pkg/`.

```text
cmd/
  agent/
    main.go        startup, flags, embedded agent page, main()
    routes.go      HTTP route wiring
    session.go     session management, message loop, streaming, typed events
    skills.go      skill loading and persona parsing
    tasks.go       delegated task queue and /tasks/send handlers
    tools.go       tool registry wiring and built-in tool implementations
  gateway/
    main.go        startup, flags, shared types, server bootstrap
    routes.go      top-level HTTP route registration
    agents.go      agent registry, fleet state, workspace endpoints
    cluster.go     cluster and deployment endpoints
    delegation.go  /api/v1/message, task dispatch, delegation timeline
    health.go      health aggregation helpers
    mcp.go         gateway MCP server and local tool exposure
    mcp_oauth.go   MCP OAuth helpers
    sessions.go    session persistence and APIs
    webhooks.go    webhook ingestion and automation
    websocket.go   websocket fan-out and event streaming
    workspace.go   workspace projector integration and refresh logic
    ui.html        embedded primary gateway UI
    index.html     standalone UI asset kept in the tree
  lockbox/
    main.go        startup, flags, shared state, route registration
    queue.go       approval queue lifecycle and approve/reject/revise handlers
    actions.go     Google and other external-service action/tool handlers
    mcp.go         MCP server and upstream MCP proxying

pkg/
  mcp/            MCP JSON-RPC client used by agents
  tasklife/       delegation lifecycle, gating, nudges, and reporting helpers
  tasks/          in-memory task store and tool-log persistence
  tools/          tool registry, tool implementations, typed event definitions
  workspace/      workspace projector used by the gateway UI
```

## Key Types

- `gateway` lives in `cmd/gateway/main.go`. It owns the agent registry, session store, websocket clients, delegation timeline, and the `workspace.Projector` that backs UI workspace views.
- `Agent` lives in `cmd/gateway/main.go`. It is the gateway's view of a registered worker, including URL, role, health, model, and runtime counters.
- `lockboxState` lives in `cmd/lockbox/main.go`. It owns hunt scopes, audit logs, approval queues, registered service handlers, and MCP tool exposure for the lockbox binary.
- `Projector` lives in `pkg/workspace/projector.go`. It turns typed tool events and delegation events into a per-agent `AgentWorkspace` snapshot.

## Runtime Interaction

At runtime the gateway is the entry point. It serves the UI, tracks known agents, exposes the main API surface, streams events over websocket, and proxies or dispatches work to agents and lockbox.

Each agent runs its own HTTP server. The gateway sends interactive work to `/message` and delegated work to `/tasks/send`. Inside the agent, the session processor builds model prompts, executes tools, emits typed tool events, and records task state through `pkg/tasks`.

Lockbox is a separate approval and external-integration service. The gateway proxies approval endpoints to it, and agents can reach it through tools or MCP when a workflow needs audited writes to systems such as Google services or upstream MCP tools.

## Message Flow

The normal user path starts in the gateway UI in `cmd/gateway/ui.html`. The UI posts to `POST /api/v1/message`, which is implemented in `cmd/gateway/delegation.go`.

The gateway chooses the target agent, forwards the request to that agent's `POST /message` endpoint, and streams server-sent events back to the browser. The agent session loop in `cmd/agent/session.go` sends model output, tool-call events, tool results, and completion chunks back over that stream.

During tool execution, the agent builds typed events with helpers in `pkg/tools/events.go`. The gateway consumes those events in `cmd/gateway/delegation.go` and `cmd/gateway/workspace.go`, broadcasts them to websocket subscribers, and updates workspace state for the active agent.

## Task Dispatch Flow

Delegation can happen in two ways:

- Interactive requests can trigger the agent's `delegate` tool from `cmd/agent/tools.go`, which posts a delegated task to another agent's `/tasks/send` endpoint.
- Operators or automation can call the gateway's `/api/v1/dispatch` endpoint in `cmd/gateway/delegation.go`, which forwards work directly to an agent task queue.

The receiving agent stores task state in `pkg/tasks.Store`, executes the work through the same conversation processor used by `/message`, and exposes task status through `/tasks` endpoints in `cmd/agent/tasks.go`. Gateway-side delegation timeline state is tracked separately so the UI can show cross-agent progress for a session.

## Workspace Projector Flow

The workspace projector in `pkg/workspace` is the gateway's materialized view of what an agent is doing right now. It understands typed events such as file reads and writes, git actions, plans, plan-step completion, and delegation.

`cmd/gateway/workspace.go` applies typed events into the projector, adds session-aware metadata, and emits `workspace_update` payloads to websocket clients. For architect sessions, it also refreshes task lists and timeline state by querying agent task APIs and merging them back into the projected workspace snapshot.

## Where To Extend The System

- New gateway APIs usually belong in the closest split file under `cmd/gateway/` and then in `registerRoutes` in `cmd/gateway/routes.go`.
- New agent tools usually start in `pkg/tools/` for reusable execution logic and are then registered in `cmd/agent/tools.go`.
- New lockbox approval-backed integrations usually live in `cmd/lockbox/actions.go` and are surfaced through lockbox route or MCP wiring.
