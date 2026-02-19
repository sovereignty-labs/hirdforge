# Valhalla Roadmap

## v0.1.0 — Minimum Lovable Core

### valhalla-agent (The Warrior)
- [x] HTTP server with SSE streaming
- [x] SOUL.md loading (identity)
- [x] Ollama inference client (streaming)
- [x] Session history (in-memory)
- [x] Health endpoint
- [ ] Tool framework (interface + registry)
- [ ] Tools: exec, read, write
- [ ] Tools: message (inter-agent)
- [ ] Tools: memory_read, memory_write
- [ ] Tool call parsing from model responses
- [ ] Tool rate limiting (max calls/session, timeout, max output)
- [ ] Tool security: exec off by default, hard timeouts, PID limits
- [ ] Prompt injection defense (input sanitization)
- [ ] Skill/Rune loading and matching
- [ ] Prometheus metrics (/metrics)
- [ ] Context compaction (sliding window + summarization)
- [ ] Dockerfile

### valhalla-gateway (The Gate)
- [ ] WebSocket server for browser clients
- [ ] REST API (/api/v1/message, /api/v1/agents)
- [ ] K8s agent discovery via labels (valhalla.io/role=warrior)
- [ ] Message routing (user → agent → user streaming)
- [ ] Session store (PostgreSQL)
- [ ] Device authentication (Ed25519)
- [ ] Telegram bridge
- [ ] Embedded dashboard (HTML/JS via embed.FS)
- [ ] Prometheus metrics
- [ ] Dockerfile

### Helm Chart (The Hall)
- [ ] valhalla-core chart (agent + gateway + postgres)
- [ ] Agent deployment template (loops over values.agents)
- [ ] ServiceAccount + RBAC per agent
- [ ] NetworkPolicy (deny-all default + per-agent egress)
- [ ] Pod Security Standards (restricted namespace)
- [ ] PVC templates
- [ ] Vault integration (External Secrets)
- [ ] Gitea subchart (The Forge)
- [ ] ArgoCD Application template
- [ ] values.yaml with secure defaults

### Seidr (Knowledge Service)
- [ ] Port existing Python RAG stack as Helm subchart
- [ ] Gitea webhook for auto-ingestion
- [ ] knowledge tool in valhalla-agent
- [ ] ChromaDB + BGE reranker deployment

## v0.2.0 — Production Hardening

### Security
- [ ] mTLS for out-of-cluster inference traffic (Linkerd)
- [ ] gVisor/Kata sandbox for exec tool
- [ ] automountServiceAccountToken: false by default
- [ ] Vault production mode (HA, durable storage, unseal)

### Scaling
- [ ] PostgreSQL LISTEN/NOTIFY for gateway pub/sub
- [ ] Multi-replica gateway with session affinity
- [ ] NATS for async inter-agent workflows
- [ ] Agent multiplexing (multi-agent per pod for lite mode)
- [ ] GPU sharing docs (NVIDIA device plugin, MIG, time-slicing)
- [ ] HPA with custom metrics (GPU-aware autoscaling)

### Storage
- [ ] Ceph integration (RBD, CephFS, RGW)
- [ ] Shared skill library via CephFS (ReadWriteMany)
- [ ] S3 artifact storage for session transcripts
- [ ] Skills as OCI artifacts / git repo checkout

### Observability (valhalla-stack chart)
- [ ] Prometheus + Grafana with pre-built dashboards
- [ ] Loki for centralized logging
- [ ] Headlamp for K8s dashboard
- [ ] Hubble for network observability (Cilium)
- [ ] Traefik dashboard

### UX
- [ ] valhalla init CLI (bootstrap keys, values.yaml, Vault)
- [ ] Minikube quick-start guide
- [ ] Talos Linux deployment guide
- [ ] Demo video for portfolio
- [ ] Discord bridge
- [ ] Slack bridge

## v1.0.0 — Open Source Release
- [ ] Rename repo to valhalla-ai/valhalla
- [ ] Public GitHub with README + architecture docs
- [ ] GitHub Actions CI (build, test, push images)
- [ ] OCI Helm chart registry
- [ ] Contributing guide
- [ ] Performance benchmarks
- [ ] Community Discord/Matrix
