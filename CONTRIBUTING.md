# Contributing

## Build And Test

Build everything:

```sh
go build ./...
```

Run the full test suite:

```sh
go test ./...
```

Use those two commands before opening a PR. If your change touches static analysis or Go version behavior, also run the targeted checks that are relevant to that work.

## Adding A New Tool To The Agent

Most reusable tool behavior lives in `pkg/tools/`. Add or extend the tool implementation there when the logic should be shared or tested independently.

Register the tool in `cmd/agent/tools.go` so the agent exposes it through its runtime registry. If the tool needs agent-specific dependencies, wire them in there rather than pushing that coupling down into `pkg/tools`.

If the tool should update gateway workspace state, add typed event coverage in `pkg/tools/events.go`. The session loop in `cmd/agent/session.go` already emits typed start and result events from those helpers during `/message` processing.

## Adding A New API Endpoint To The Gateway

Put the handler in the closest existing split file under `cmd/gateway/`. For example, session APIs belong with `sessions.go`, delegation and message flow belong with `delegation.go`, and workspace behavior belongs with `workspace.go`.

Wire the endpoint through `registerRoutes` in `cmd/gateway/routes.go` or through the relevant sub-registration helper it calls. Keep shared gateway state on the existing `gateway` type in `cmd/gateway/main.go`.

If the endpoint affects browser state, also update websocket broadcasts or UI fetch paths as needed so the UI stays in sync with server behavior.

## Adding A New UI Surface

The primary operator UI is the embedded gateway page in `cmd/gateway/ui.html`, served at `/` by the gateway binary. Most new UI surfaces should be added there, backed by gateway API endpoints and websocket updates.

If a change needs a new page or asset, keep it under `cmd/gateway/` and make the serving path explicit in `cmd/gateway/routes.go`. `cmd/gateway/index.html` is still in the tree as a standalone asset, but the live gateway route serves `ui.html`.

The agent binary also embeds a lightweight page in `cmd/agent/main.go` for its own local interface. Only add UI there when the surface is agent-local rather than a gateway/operator concern.

## PR Workflow

Branch from `main`.

Keep each PR to one concern.

Make CI green before asking for review.

If a change spans gateway, agent, and lockbox, keep the behavior aligned and explain the cross-binary effect in the PR description.
