"""
Seidr — Knowledge & Memory Service for Project Valhalla
CPU-only vector search with hybrid BM25 + semantic retrieval.
No GPU required. Deploys on any Kubernetes cluster.
"""
import asyncio
import os
import json
import time
from datetime import datetime, timedelta, timezone
from fastapi import FastAPI
from pydantic import BaseModel
from typing import Literal, Optional
import chromadb
from chromadb.utils import embedding_functions

# --- Config ---
CHROMA_HOST = os.getenv("CHROMA_HOST", "localhost")
CHROMA_PORT = int(os.getenv("CHROMA_PORT", "8000"))
COLLECTION = os.getenv("COLLECTION_NAME", "valhalla_knowledge")
MEMORY_TTL_HOURS = int(os.getenv("MEMORY_TTL_HOURS", "240"))
MEMORY_TYPES = {"general", "failure", "recovery", "lesson", "fact", "observation"}

# --- Models ---
class QueryRequest(BaseModel):
    query: str
    agent: Optional[str] = None
    limit: int = 5
    type: Optional[Literal["general", "failure", "recovery", "lesson", "fact", "observation"]] = None
    where: Optional[dict] = None

class RememberRequest(BaseModel):
    agent: str
    content: str
    tags: list[str] = []
    source: str = "agent"
    type: Literal["general", "failure", "recovery", "lesson", "fact", "observation"] = "general"
    metadata: Optional[dict] = None

class IngestRequest(BaseModel):
    path: str = "/docs"

# --- App ---
app = FastAPI(title="Seidr", description="Valhalla Knowledge Service")

# ChromaDB's built-in embedding: all-MiniLM-L6-v2, CPU, ~80MB, auto-downloads
embed_fn = embedding_functions.DefaultEmbeddingFunction()
chroma_client = None
collection = None

def get_collection():
    global chroma_client, collection
    if collection is None:
        chroma_client = chromadb.HttpClient(host=CHROMA_HOST, port=CHROMA_PORT)
        collection = chroma_client.get_or_create_collection(
            name=COLLECTION,
            embedding_function=embed_fn,
            metadata={"hnsw:space": "cosine"}
        )
    return collection

# --- BM25 Hybrid Search ---
def bm25_search(query: str, documents: list[dict], k: int = 10) -> list[int]:
    """Simple BM25-like keyword scoring. Returns indices sorted by relevance."""
    query_terms = set(query.lower().split())
    scores = []
    for i, doc in enumerate(documents):
        text = doc.lower()
        term_hits = sum(1 for t in query_terms if t in text)
        # Boost for exact phrase match
        if query.lower() in text:
            term_hits += 3
        scores.append((i, term_hits))
    scores.sort(key=lambda x: x[1], reverse=True)
    return [i for i, s in scores[:k] if s > 0]

def hybrid_search(query: str, limit: int = 5, agent: str = None, where: dict = None):
    """Combine vector similarity + BM25 keyword search."""
    col = get_collection()

    # Build filter
    search_where = where.copy() if where else None
    if agent:
        if search_where is None:
            search_where = {"agent": agent}
        else:
            search_where["agent"] = agent

    # Vector search
    results = col.query(
        query_texts=[query],
        n_results=min(limit * 3, 20),  # overfetch for reranking
        where=search_where,
        include=["documents", "metadatas", "distances"]
    )

    if not results["documents"] or not results["documents"][0]:
        return []

    docs = results["documents"][0]
    metas = results["metadatas"][0]
    distances = results["distances"][0]

    # BM25 on the vector results (hybrid reranking)
    bm25_indices = bm25_search(query, docs, k=len(docs))
    bm25_rank = {idx: rank for rank, idx in enumerate(bm25_indices)}

    # Score: 60% vector, 40% BM25 rank
    scored = []
    for i, (doc, meta, dist) in enumerate(zip(docs, metas, distances)):
        vector_score = 1.0 - dist  # cosine distance to similarity
        bm25_score = 1.0 - (bm25_rank.get(i, len(docs)) / len(docs)) if i in bm25_rank else 0.0
        combined = 0.6 * vector_score + 0.4 * bm25_score
        scored.append({
            "content": doc,
            "metadata": meta,
            "similarity": round(combined, 4),
            "vector_score": round(vector_score, 4),
            "bm25_score": round(bm25_score, 4),
        })

    scored.sort(key=lambda x: x["similarity"], reverse=True)
    return scored[:limit]


def utcnow() -> datetime:
    return datetime.now(timezone.utc)


def parse_timestamp(value: str) -> Optional[datetime]:
    if not value:
        return None
    try:
        return datetime.strptime(value, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=timezone.utc)
    except ValueError:
        return None


def resolve_memory_type(req: RememberRequest) -> str:
    if req.metadata and req.metadata.get("type") in MEMORY_TYPES:
        return req.metadata["type"]
    return req.type


def prune_expired_memories() -> dict:
    col = get_collection()
    total = col.count()
    if total == 0:
        return {"pruned": 0, "remaining": 0}

    results = col.get(limit=total, include=["metadatas"])
    cutoff = utcnow() - timedelta(hours=MEMORY_TTL_HOURS)
    pruned = 0
    for doc_id, meta in zip(results.get("ids", []), results.get("metadatas", [])):
        meta = meta or {}
        if meta.get("type") == "lesson":
            continue
        ts = parse_timestamp(meta.get("timestamp", ""))
        if ts is None or ts >= cutoff:
            continue
        age_hours = round((utcnow() - ts).total_seconds() / 3600, 2)
        col.delete(ids=[doc_id])
        pruned += 1
        log("info", "memory pruned", {
            "id": doc_id,
            "agent": meta.get("agent", ""),
            "age_hours": age_hours,
        })
    return {"pruned": pruned, "remaining": col.count()}


# --- Endpoints ---

@app.on_event("startup")
async def startup_prune_task():
    async def prune_loop():
        while True:
            try:
                prune_expired_memories()
            except Exception as e:
                log("error", "memory prune failed", {"error": str(e)})
            await asyncio.sleep(6 * 60 * 60)
    asyncio.create_task(prune_loop())

@app.get("/health")
async def health():
    try:
        col = get_collection()
        count = col.count()
        return {"status": "ok", "memories": count, "collection": COLLECTION}
    except Exception as e:
        return {"status": "error", "error": str(e)}

@app.post("/query")
async def query(req: QueryRequest):
    """Search knowledge base. Called by agent recall tool."""
    where = req.where.copy() if req.where else None
    if req.type:
        if where is None:
            where = {"type": req.type}
        else:
            where["type"] = req.type
    results = hybrid_search(req.query, limit=req.limit, agent=req.agent, where=where)
    return {"results": results, "count": len(results)}

@app.post("/remember")
async def remember(req: RememberRequest):
    """Store a new memory. Called by agent remember tool."""
    col = get_collection()
    memory_type = resolve_memory_type(req)
    duplicate_results = col.query(
        query_texts=[req.content],
        n_results=3,
        where={"agent": req.agent},
        include=["metadatas", "distances"],
    )
    if duplicate_results.get("distances") and duplicate_results["distances"][0]:
        ids = duplicate_results.get("ids", [[]])[0]
        metas = duplicate_results.get("metadatas", [[]])[0]
        distances = duplicate_results["distances"][0]
        for existing_id, existing_meta, distance in zip(ids, metas, distances):
            existing_meta = existing_meta or {}
            if existing_meta.get("type", "general") == memory_type and distance < 0.08:
                log("info", "memory deduplicated", {"agent": req.agent, "similar_to": existing_id})
                return {"id": None, "stored": False, "reason": "duplicate", "similar_to": existing_id}

    doc_id = f"{req.agent}-{int(time.time())}-{os.urandom(4).hex()}"
    metadata = {
        "agent": req.agent,
        "source": req.source,
        "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "tags": ",".join(req.tags) if req.tags else "",
        "type": memory_type,
    }
    if req.metadata:
        metadata.update(req.metadata)
    col.add(
        documents=[req.content],
        metadatas=[metadata],
        ids=[doc_id]
    )
    log("info", "memory stored", {"agent": req.agent, "id": doc_id, "tags": req.tags})
    return {"id": doc_id, "stored": True}

@app.get("/memories")
async def list_memories(agent: str = None, limit: int = 100, type: str = None):
    """List stored memories, optionally filtered by agent."""
    col = get_collection()
    where = {}
    if agent:
        where["agent"] = agent
    if type:
        where["type"] = type
    if not where:
        where = None
    results = col.get(where=where, limit=limit, include=["documents", "metadatas"])
    memories = []
    for doc, meta, doc_id in zip(results["documents"], results["metadatas"], results["ids"]):
        memories.append({
            "id": doc_id,
            "content": doc,
            "agent": meta.get("agent", ""),
            "source": meta.get("source", ""),
            "timestamp": meta.get("timestamp", ""),
            "type": meta.get("type", "general"),
            "tags": meta.get("tags", "").split(",") if meta.get("tags") else [],
        })
    return {"memories": memories, "count": len(memories)}

@app.post("/prune")
async def prune():
    return prune_expired_memories()

@app.delete("/memories/{memory_id}")
async def delete_memory(memory_id: str):
    """Delete a specific memory."""
    col = get_collection()
    col.delete(ids=[memory_id])
    return {"deleted": memory_id}

@app.post("/ingest")
async def ingest(req: IngestRequest):
    """Ingest markdown files from a directory into the knowledge base."""
    import glob
    col = get_collection()
    files = glob.glob(os.path.join(req.path, "**/*.md"), recursive=True)
    total = 0
    for filepath in files:
        with open(filepath, "r") as f:
            content = f.read()
        # Chunk: 1200 chars with 200 overlap
        chunks = chunk_text(content, chunk_size=1200, overlap=200)
        for i, chunk in enumerate(chunks):
            doc_id = f"doc-{os.path.basename(filepath)}-{i}"
            metadata = {
                "agent": "ingest",
                "source": filepath,
                "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                "tags": "documentation",
            }
            col.upsert(documents=[chunk], metadatas=[metadata], ids=[doc_id])
            total += 1
    log("info", "ingestion complete", {"files": len(files), "chunks": total})
    return {"status": "ok", "files": len(files), "chunks": total}


# --- Helpers ---

def chunk_text(text: str, chunk_size: int = 1200, overlap: int = 200) -> list[str]:
    """Split text into overlapping chunks."""
    chunks = []
    start = 0
    while start < len(text):
        end = start + chunk_size
        chunks.append(text[start:end])
        start = end - overlap
    return [c.strip() for c in chunks if c.strip()]

def log(level: str, msg: str, fields: dict = None):
    entry = {"ts": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "level": level, "msg": msg}
    if fields:
        entry.update(fields)
    print(json.dumps(entry), flush=True)


if __name__ == "__main__":
    import uvicorn
    port = int(os.getenv("PORT", "8082"))
    log("info", "seidr starting", {"port": port, "chroma": f"{CHROMA_HOST}:{CHROMA_PORT}"})
    uvicorn.run(app, host="0.0.0.0", port=port)
