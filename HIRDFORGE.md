# HIRDFORGE.md

## Project Overview
This repo contains Valhalla source code: Go binaries for agent, gateway, and lockbox; Python source for Seidr; Dockerfiles for each service; and the Gitea CI workflow that builds and publishes images.

## UI 1.0 Status (12-phase plan)
Phase 11 (**Architect surface**) shipped in PR #173. UI 1.0 is now **11/12** phases complete.

### Phase checklist
- [x] Phase 0 — Snapshot
- [x] Phase 0.5 — Obliterate Temporal (Asgard scope only)
- [x] Phase 1 — Shell at /ui/
- [x] Phase 2 — Comms (Left Half)
- [x] Phase 3 — Active Strip + Context Bar + Idle Surface
- [x] Phase 4 — Settings Menu + Realm Tab
- [x] Phase 5 — Backend: Typed Tool Events + Workspace Projector
- [x] Phase 6 — Builder Surface
- [x] Phase 7 — Backend: Lockbox Trigger Context + Plan Structure + Revise Endpoint
- [x] Phase 8 — Action Surface
- [x] Phase 9 — Backend: PR Review Endpoints
- [x] Phase 10 — Reviewer Surface
- [x] Phase 11 — Architect Surface (PR #173)
- [ ] **Phase 12 — Cutover (NEXT)**

### Next milestone: Phase 12 — Cutover
- Make new UI the default (`/` redirects to `/ui/` or serves the new bundle at `/`).
- Remove legacy UI from `cmd/gateway/index.html` (snapshot remains on `legacy-ui-snapshot`).

## Directory Structure
- `cmd/agent/` — agent binary entrypoint and tool wiring
- `cmd/gateway/` — gateway binary entrypoint, routes, and embedded UI
- `cmd/lockbox/` — lockbox binary entrypoint and handlers
- `seidr/` — Python Seidr service source and requirements
- `pkg/tools/` — agent tool implementations
- `pkg/mcp/` — MCP protocol and transport code
- `scripts/` — repo utility and validation scripts

## Build Rules
- Go version: 1.23
- Build each Go service as a single static binary with `CGO_ENABLED=0`
- Keep runtime dependencies external to the binary at zero unless already required by the service design
- Target Alpine-based container images for Go services

## CI Pipeline
- Pushes to `main` trigger builds for agent, gateway, lockbox, and Seidr
- CI publishes SHA-tagged images to the internal registry
- After publishing, CI opens or updates an infrastructure PR in `kit/valhalla-infra`

## Git Rules
- Never push directly to `main`
- Always work on a branch and open a pull request
- Agent commits must use the warband account: `warband <warband@hirdforge>`
- `kit` is the only user whitelisted to merge to `main`

## Testing
- Go changes: run `go vet` and `go build` for affected packages or binaries
- Python changes: run `python3 -m py_compile` for affected scripts
- Do not claim completion without reporting what checks were run
