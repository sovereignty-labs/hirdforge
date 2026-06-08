# Hirdforge

A multi-agent AI orchestration platform. A gateway UI and API, agent binaries that execute model-driven work, and a lockbox service that gates write-capable external integrations behind approval flows.

## Architecture

```
User ──▶ Gateway ──▶ Agent(s) ──▶ Model + Tools
              │            │
              │            └──▶ Lockbox (approval-gated external actions)
              │
              └──▶ Seidr (RAG / knowledge service)
```

Three primary Go binaries and one Python service, all communicating over HTTP.

| Binary | Location | Role |
|---|---|---|
| **gateway** | `cmd/gateway/` | Central API and embedded UI. Routes messages, tracks agents, streams events via WebSocket, proxies to agents and lockbox, exposes MCP tools. |
| **agent** | `cmd/agent/` | Worker. Receives interactive work at `POST /message` and delegated tasks at `POST /tasks/send`. Runs model prompts, executes tools, emits typed events. |
| **lockbox** | `cmd/lockbox/` | Approval service. Gates write-capable external integrations (Google services, upstream MCP tools) behind approve/reject/revise flows. |
| **seidr** | `seidr/` | Python RAG and knowledge service. Ingests content, serves semantic search via MCP. |

Additional utility binaries: `hirdforge` (`cmd/hirdforge/`), `validate` (`cmd/validate/`), `astgraph` (`cmd/astgraph/`).

## Reusable Packages

| Package | Purpose |
|---|---|
| `pkg/tools/` | Tool implementations and typed event definitions |
| `pkg/mcp/` | MCP JSON-RPC client used by agents |
| `pkg/workspace/` | Workspace projector — materialized view of agent activity for the UI |
| `pkg/tasklife/` | Delegation lifecycle, gating, nudges |
| `pkg/tasks/` | In-memory task store and tool-log persistence |

## Runtime Flow

1. User interacts with the embedded gateway UI (`cmd/gateway/ui.html`).
2. UI posts to `POST /api/v1/message` (handler in `cmd/gateway/delegation.go`).
3. Gateway selects the target agent and forwards the request to that agent's `POST /message`.
4. Agent session loop (`cmd/agent/session.go`) processes the prompt: sends model output, tool-call events, tool results, and completion chunks back over an SSE stream.
5. Gateway consumes typed events, broadcasts them to WebSocket subscribers, and updates workspace state via the projector in `pkg/workspace/projector.go`.

Delegated tasks flow through agent `POST /tasks/send` endpoints with state tracked in `pkg/tasks.Store`. Lockbox is reached when a tool execution requires an audited write to an external system.

## Development Quickstart

```bash
# Build all Go binaries
go build ./...

# Run the test suite
go test ./...
```

Each Go service builds as a single static binary (`CGO_ENABLED=0`). Dockerfiles are provided for each service in the repo root. The gateway UI is assembled via `npm run build` (runs `scripts/build-gateway-ui.mjs`).

### Adding Code

- **New agent tool** — implement in `pkg/tools/`, register in `cmd/agent/tools.go`.
- **New gateway API** — handler in the relevant file under `cmd/gateway/`, wire it in `cmd/gateway/routes.go`.
- **New UI surface** — edit `cmd/gateway/ui.html`, backed by gateway endpoints and WebSocket updates.

See [Contributing](CONTRIBUTING.md) for detailed extension guides.

## Documentation

- [Architecture](ARCHITECTURE.md) — binaries, packages, message flows, where to extend
- [Contributing](CONTRIBUTING.md) — how to add tools, endpoints, UI surfaces, PR workflow
- [Roadmap](ROADMAP.md) — v0.1.0, v0.2.0, v1.0.0 milestones
- [Security](SECURITY.md) — vulnerability reporting policy
- [Hirdforge Overview](HIRDFORGE.md) — UI phase checklist, build rules, CI and git rules

## Project Status

The project is under active development. The embedded gateway UI is 11 of 12 phases complete. See [HIRDFORGE.md](HIRDFORGE.md) for the current phase checklist and [ROADMAP.md](ROADMAP.md) for upcoming milestones.

## License

MIT

A [Sovereignty Labs](https://github.com/sovereignty-labs) project.
