---
spec: seidr-split-health-endpoints
repo: kit/hirdforge
component: seidr
priority: high
type: feature
status: ready
owner: val
created: 2026-04-16
tags: [healthcheck, observability, embedding, liveness]
acceptance:
  - /health returns HTTP 200 within 100ms with only {"status": "ok"} on success
  - /health performs only a DB connection ping — no count queries
  - /health/embedding performs embedding health check and returns model/device status
  - /health/embedding returns HTTP 503 when embedding is unavailable
  - Kubernetes liveness probe uses /health (existing probes continue to work)
  - Kubernetes readiness probe or monitoring can use /health/embedding
  - Both endpoints return JSON
  - Both endpoints accept only GET
---

# Seidr — Split Health Endpoints

## Context

Seidr's `/health` endpoint currently executes two PostgreSQL `COUNT(*)` queries and fetches the list of agent collections on every invocation. This makes it unsuitable as a Kubernetes liveness probe: a slow or congested database causes the probe to fail even when the service itself is healthy. Meanwhile, callers have no way to probe the embedding subsystem independently.

## Decision

Split into two endpoints:

| Endpoint | Purpose | Probe Type |
|---|---|---|
| `GET /health` | Process liveness | Kubernetes `livenessProbe` |
| `GET /health/embedding` | Embedding readiness | Monitoring / `readinessProbe` |

## `/health` — Liveness

**Response time target:** < 100ms

Performs a single DB connection ping — no queries, no counts.

```python
@app.get("/health")
async def health():
    try:
        pool = ensure_pool()
        await pool.fetchval("SELECT 1")
        return {"status": "ok"}
    except Exception as e:
        raise HTTPException(status_code=503, detail=str(e))
```

No other fields. No DB reads. The goal is "is the process alive and can it reach the database".

## `/health/embedding` — Embedding Readiness

**Response time target:** < 3s (embedding model or remote service)

Checks:
1. If `EMBED_URL` is set — calls `POST /embed` with a short warmup string and verifies the response contains valid embeddings.
2. If `EMBED_URL` is empty — checks that `EMBED_MODEL` is initialized (not `None`).

```python
@app.get("/health/embedding")
async def health_embedding():
    try:
        if EMBED_URL:
            async with httpx.AsyncClient(timeout=5.0) as client:
                resp = await client.post(
                    f"{EMBED_URL.rstrip('/')}/embed",
                    json={"texts": ["ping"]}
                )
                resp.raise_for_status()
                data = resp.json()
                embeddings = data.get("embeddings", [])
                if not embeddings or len(embeddings[0]) == 0:
                    raise HTTPException(status_code=503, detail="empty embedding response")
                return {
                    "status": "ok",
                    "backend": "remote",
                    "embed_url": EMBED_URL,
                    "model": EMBEDDING_MODEL,
                    "embedding_dim": len(embeddings[0]),
                }
        else:
            if EMBED_MODEL is None:
                raise HTTPException(status_code=503, detail="embedding model not initialized")
            return {
                "status": "ok",
                "backend": "local",
                "model": EMBEDDING_MODEL,
                "device": EMBEDDING_DEVICE,
            }
    except httpx.TimeoutException:
        raise HTTPException(status_code=503, detail="embedding service timed out")
    except httpx.HTTPStatusError as e:
        raise HTTPException(status_code=503, detail=f"embedding service returned {e.response.status_code}")
    except Exception as e:
        raise HTTPException(status_code=503, detail=str(e))
```

## HTTP Status Codes

| Endpoint | Healthy | Degraded | Error |
|---|---|---|---|
| `GET /health` | 200 `{"status": "ok"}` | — | 503 |
| `GET /health/embedding` | 200 with full status | — | 503 with `detail` |

## Non-Goals

- No authentication on health endpoints.
- No changes to `/status` or other existing endpoints.
- No metrics endpoint here — use `/metrics` if metrics are needed.

## Open Questions

- Should `/health/embedding` include a latency field (e.g., `"latency_ms": 42}`)? Useful for dashboards. **Recommendation: add it.**
- Does the embed server's own `/health` need to stay as-is, or should it also adopt this pattern? The embed server is a sidecar — the main Seidr service should be the canonical health source for k8s probes.
