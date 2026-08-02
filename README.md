# Hirdforge

A platform for running fleets of AI coding agents under human supervision. A gateway UI
and API, agent binaries that execute model-driven work, and a lockbox service that gates
write-capable external integrations behind approval flows.

## The idea: coordination is infrastructure, not agency

The reliability of a multi-agent system does not come from the models. It comes from the
engineering around them.

So nothing here asks a model to judge its own work. Routing is deterministic. Completion
is decided by mechanical checks — tests and CI — not by an agent reporting success.
Changes land as pull requests behind human approval. When an agent claims it is done,
that claim is a hypothesis until something unforgiving disagrees or doesn't.

That constraint is the whole design. Everything below is downstream of it.

## Status

**Deployed to production Kubernetes; in validation.** The end-to-end pipeline is
operational and being hardened against real tasks. The embedded gateway UI is 11 of 12
phases complete. This is not a finished product, and the roadmap says so.

## How this was built, plainly

Most of the commits in this repository were not typed by a human.

```
git shortlog -sn --no-merges
```

Run that and you will see agent personas — `warband`, `ragnar`, `Knut the Swift`,
`Chuck Norse` and others — authoring roughly half the non-merge commits, across 712 merge
commits and under six months of history.

That is the point, not an embarrassment. This project exists to find out whether fleets of
agents can produce engineering that holds up, and the honest way to report the answer is to
let the history show who wrote what. The architecture was mine. The decisions, the
rejected alternatives and the review were mine. A large share of the implementation was
delegated, gated, and merged only after mechanical verification passed.

If that arrangement is going to work anywhere, it has to work on the thing that builds it.

## Verification

The gates are the argument, so they run on every change:

| Gate | |
|---|---|
| `gofmt` | formatting |
| `go vet` | correctness |
| `staticcheck` | static analysis |
| `gocyclo` | complexity ceiling |
| `go test ./...` | 107 test files |
| **coverage ratchet** | coverage cannot regress below baseline |
| oversized-file check | keeps files reviewable |
| redaction check | no live infrastructure identifiers |

225 Go files, ~67,000 lines. The coverage ratchet matters most: an agent cannot quietly
trade tests away for a passing build.

## Architecture

```
User ──▶ Gateway ──▶ Agent(s) ──▶ Model + Tools
              │            │
              │            └──▶ Lockbox (approval-gated external actions)
              │
              └──▶ Knowledge service (RAG, internal)
```

Go binaries communicating over HTTP.

| Binary | Location | Role |
|---|---|---|
| **gateway** | `cmd/gateway/` | Central API and embedded UI. Routes messages, tracks agents, streams events over WebSocket, proxies to agents and lockbox, exposes MCP tools. |
| **agent** | `cmd/agent/` | Worker. Receives interactive work at `POST /message` and delegated tasks at `POST /tasks/send`. Runs model prompts, executes tools, emits typed events. |
| **lockbox** | `cmd/lockbox/` | Approval service. Gates write-capable external integrations behind approve / reject / revise flows. |

Additional utility binaries: `hirdforge` (`cmd/hirdforge/`), `validate` (`cmd/validate/`),
`astgraph` (`cmd/astgraph/`).

### Reusable packages

| Package | Purpose |
|---|---|
| `pkg/tools/` | Tool implementations and typed event definitions |
| `pkg/mcp/` | MCP JSON-RPC client used by agents |
| `pkg/workspace/` | Workspace projector — materialised view of agent activity for the UI |
| `pkg/tasklife/` | Delegation lifecycle, gating, nudges |
| `pkg/tasks/` | In-memory task store and tool-log persistence |

### Runtime flow

1. User interacts with the embedded gateway UI (`cmd/gateway/ui.html`).
2. UI posts to `POST /api/v1/message` (handler in `cmd/gateway/delegation.go`).
3. Gateway selects the target agent and forwards to that agent's `POST /message`.
4. The agent session loop (`cmd/agent/session.go`) processes the prompt, returning model
   output, tool-call events, tool results and completion chunks over an SSE stream.
5. Gateway consumes typed events, broadcasts them to WebSocket subscribers, and updates
   workspace state via `pkg/workspace/projector.go`.

Delegated tasks flow through agent `POST /tasks/send` with state in `pkg/tasks.Store`.
Lockbox is reached when a tool execution requires an audited write to an external system.

## Development

```bash
go build ./...
go test ./...
```

Each service builds as a single static binary (`CGO_ENABLED=0`). Dockerfiles are provided
for each service in the repo root. The gateway UI is assembled with `npm run build`
(`scripts/build-gateway-ui.mjs`).

### Adding code

- **New agent tool** — implement in `pkg/tools/`, register in `cmd/agent/tools.go`
- **New gateway API** — handler under `cmd/gateway/`, wire in `cmd/gateway/routes.go`
- **New UI surface** — edit `cmd/gateway/ui.html`, backed by gateway endpoints and
  WebSocket updates

See [CONTRIBUTING.md](CONTRIBUTING.md) for detailed extension guides.

## Documentation

- [ARCHITECTURE.md](ARCHITECTURE.md) — binaries, packages, message flows, where to extend
- [DECISIONS.md](docs/DECISIONS.md) — the decision log, with rejected alternatives and
  corrections left visible
- [CONTRIBUTING.md](CONTRIBUTING.md) — tools, endpoints, UI surfaces, PR workflow
- [ROADMAP.md](ROADMAP.md) — milestones
- [SECURITY.md](SECURITY.md) — vulnerability reporting

## What is not in this repository

**Deployment manifests.** They are managed privately via GitOps and carry real
infrastructure detail. What is here builds and tests; it does not describe where it runs.

**The knowledge service.** The RAG component referenced in the architecture is developed
inside Hirdforge rather than published separately.

Commit history is rewritten to remove live infrastructure identifiers. Merge topology,
author dates and authorship are preserved unchanged — the history is evidence, so it was
not squashed.

## License

Apache-2.0

A [Sovereignty Labs](https://github.com/sovereignty-labs) project ·
[kitporath](https://github.com/kitporath)
