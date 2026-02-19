# ⚔️ Valhalla — K8s-Native Multi-Agent AI Platform

Deploy autonomous AI agent teams on Kubernetes. Local GPUs, cloud APIs, or both — one platform, one dashboard, one `kubectl apply`.

```
┌─────────────────────────────────────────────────┐
│           ⚔️ Valhalla Command Center            │
│                                                  │
│  ┌──────────┐  ┌──────────────────┐  ┌────────┐│
│  │ Chuck ●  │  │ > What files are │  │ Nodes  ││
│  │ qwen3:30b│  │   in workspace?  │  │ node-cp-1  ● ││
│  │ ⚡ local  │  │                  │  │ node-worker-1  ● ││
│  │          │  │ 🔨 exec: ls      │  │ node-worker-2● ││
│  │ Ragnar ● │  │ file1.go         │  │        ││
│  │ MiniMax  │  │ file2.go         │  │ Pods   ││
│  │ ☁️ cloud  │  │                  │  │ chuck ●││
│  │          │  │ Found 2 files... │  │ ragnar●││
│  │ Val    ● │  │                  │  │ val   ●││
│  │ Kimi K2.5│  └──────────────────┘  │ gw    ●││
│  │ ☁️ cloud  │                        └────────┘│
│  └──────────┘   [12:34 • chuck • exec: ls]      │
└─────────────────────────────────────────────────┘
```

## What It Does

Each agent runs in its own Kubernetes pod with:
- A **SOUL** — personality, role, and behavioral rules via ConfigMap
- **Tools** — sandboxed exec, read, write with security boundaries
- **Any inference backend** — local Ollama, MiniMax, Kimi, OpenAI, or anything that speaks `/v1/chat/completions`
- **An agentic loop** — tool calling with automatic re-invocation (up to 10 rounds)
- **SSE streaming** — real-time token-by-token responses

The **Gateway** unifies everything into a single dashboard with:
- Live agent cards with health, model, tools, uptime, and request counts
- Per-agent chat with persistent history
- Real-time Kubernetes cluster visibility (nodes + pods)
- System event log

## Quick Start

```bash
# Prerequisites: Kubernetes cluster, kubectl, Docker, Go 1.22+

# Clone
git clone https://github.com/kitporath/project_valhalla.git
cd project_valhalla

# Build
go build ./...
docker build -f Dockerfile.agent -t valhalla-agent:latest .
docker build -f Dockerfile.gateway -t valhalla-gateway:latest .

# Push to your registry
docker tag valhalla-agent:latest YOUR_REGISTRY/valhalla-agent:latest
docker push YOUR_REGISTRY/valhalla-agent:latest
docker tag valhalla-gateway:latest YOUR_REGISTRY/valhalla-gateway:latest
docker push YOUR_REGISTRY/valhalla-gateway:latest

# Deploy an agent
kubectl apply -f deploy/chuck-deployment.yaml

# Deploy the gateway
kubectl apply -f deploy/gateway-deployment.yaml

# Open the Command Center
open http://YOUR_NODE_IP:30880
```

## Architecture

```
Browser → Gateway (port 8080) → Agent Pods (port 8081) → Inference Backend
              │                       │
              ├─ /api/v1/agents       ├─ /message (SSE stream)
              ├─ /api/v1/message      ├─ /health
              ├─ /api/v1/events       ├─ /status
              ├─ /api/v1/cluster/*    └─ Tools: exec | read | write
              └─ / (dashboard)
```

**Gateway** discovers agents via `--agents` flag, polls health every 30s, proxies messages as SSE streams, and queries the Kubernetes API for cluster state.

**Agents** are stateless pods. Identity comes from SOUL ConfigMaps. Inference backends are swappable via flags. The agentic loop calls tools, feeds results back to the model, and streams the final response.

## Multi-Backend Support

The same binary runs on any OpenAI-compatible API:

| Agent | Backend | Model | Flag |
|-------|---------|-------|------|
| Chuck | Ollama (local GPU) | qwen3:30b | `--inference-url=http://ollama:11434` |
| Ragnar | MiniMax (cloud) | M2.1 | `--inference-url=https://api.minimax.io --api-key=$KEY` |
| Val | Kimi (cloud) | K2.5 | `--inference-url=https://api.moonshot.ai --api-key=$KEY` |

API keys are stored in HashiCorp Vault and injected via Kubernetes Secrets — they never touch git or ConfigMaps.

## Agent Flags

```
valhalla-agent \
  --soul=/etc/valhalla/soul.md    # Agent personality and rules
  --inference-url=http://...      # Inference backend (Ollama, cloud API, etc.)
  --model=qwen3:30b               # Model name
  --api-key=                      # Optional Bearer token for cloud APIs
  --tools=exec,read,write         # Enabled tools (comma-separated)
  --workspace=/workspace          # Tool sandbox directory
  --port=8081                     # HTTP port
```

## Gateway Flags

```
valhalla-gateway \
  --agents=chuck=http://chuck:8081,ragnar=http://ragnar:8081,val=http://val:8081
  --port=8080
```

## Security

- **Pod Security Standards** — `runAsNonRoot`, `drop ALL` capabilities, `seccompProfile: RuntimeDefault`
- **RBAC** — Gateway gets read-only cluster access via ServiceAccount + ClusterRole
- **Tool sandboxing** — `exec` runs in the agent's workspace directory only
- **Secrets** — API keys flow from Vault → K8s Secret → env var, never in manifests
- **No external Go dependencies** — entire platform is Go standard library

## Project Structure

```
project_valhalla/
├── cmd/
│   ├── agent/main.go      # Agent binary (~450 lines)
│   └── gateway/main.go    # Gateway binary (~750 lines)
├── pkg/                   # Shared packages
├── deploy/                # Kubernetes manifests
├── Dockerfile.agent       # 13.5MB Alpine image
├── Dockerfile.gateway     # 13.5MB Alpine image
└── go.mod                 # Zero external dependencies
```

## Current Fleet

Running on a 3-node Talos Linux cluster with RTX 3090 GPU inference:

- **Chuck Norse** — Builder. Local Ollama on RTX 3090. Writes code, runs tools.
- **Ragnar Codebrok** — Architect. MiniMax M2.1 cloud. Plans, decomposes, delegates.
- **Val Kyrie** — Ops Manager. Kimi K2.5 cloud. Deployments, health, triage.

## What's Next

- [ ] Context windowing (prevent token limit blowup on long sessions)
- [ ] Inter-agent messaging (Ragnar delegates tasks to Chuck/Val)
- [ ] Helm chart for one-command deployment
- [ ] Agent Swarm mode (parallel task execution)
- [ ] Embedded fine-tuned assistant (ships with the platform, runs on CPU)
- [ ] Self-training LoRA pipeline (model improves nightly from platform docs)

## Built With

Go standard library. That's it.

No LangChain. No LlamaIndex. No frameworks. Just HTTP, SSE, and Kubernetes.

## License

Apache 2.0

---

*"Secure, easily deployable intelligence with execution capabilities at any scale."*
