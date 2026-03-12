# Architecture — project_valhalla

## Language Split
- Go (cmd/, pkg/) — all platform binaries
- Python (seidr/) — memory service only

## Binaries
| Source | Binary | Dockerfile | Image |
|--------|--------|-----------|-------|
| cmd/agent/main.go | valhalla-agent | Dockerfile.agent | valhalla-agent |
| cmd/gateway/main.go | valhalla-gateway | Dockerfile.gateway | valhalla-gateway |
| cmd/lockbox/main.go | valhalla-lockbox | Dockerfile.lockbox | valhalla-lockbox |
| seidr/main.py | Seidr FastAPI | Dockerfile.seidr | valhalla-seidr |

## Container Environment
Alpine Linux with: git, curl, jq, python3, apply_patch.
NOTHING ELSE. No pip, npm, go compiler, gcc, make, sudo, apt, apk.
You CANNOT install packages at runtime.

## Code Patterns
- New agent tool: create in pkg/tools/, register in cmd/agent/main.go
- New gateway route: add handler in cmd/gateway/main.go, register in router
- New Seidr endpoint: add FastAPI decorator in seidr/main.py
- UI changes: edit index.html embedded in gateway via go:embed
- New lockbox handler: add in cmd/lockbox/main.go

## Build
- Go: 1.23-alpine, CGO_ENABLED=0, static binary
- Seidr: python:3.11-slim, pip install from requirements.txt
- CI builds 4 images on push to main

## Key Files
- go.mod, go.sum — Go dependencies
- seidr/requirements.txt — Python dependencies
- .gitea/workflows/build.yaml — CI pipeline
