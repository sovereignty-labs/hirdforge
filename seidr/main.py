"""
Seidr — Knowledge & Memory Service for Project Valhalla
CPU-only vector search with hybrid BM25 + semantic retrieval.
No GPU required. Deploys on any Kubernetes cluster.
"""
import asyncio
import os
import json
import re
import time
import urllib.request
from datetime import datetime, timedelta, timezone
from fastapi import FastAPI
from pydantic import BaseModel
from typing import Literal, Optional
import chromadb
from chromadb.utils import embedding_functions
try:
    import httpx
except Exception:
    httpx = None

# --- Config ---
CHROMA_HOST = os.getenv("CHROMA_HOST", "localhost")
CHROMA_PORT = int(os.getenv("CHROMA_PORT", "8000"))
COLLECTION = os.getenv("COLLECTION_NAME", "valhalla_knowledge")
MEMORY_TTL_HOURS = int(os.getenv("MEMORY_TTL_HOURS", "240"))
LESSON_TTL_HOURS = 90 * 24
MEMORY_TYPES = {"general", "failure", "recovery", "lesson", "fact", "observation"}
COGNITIVE_LAYERS = {"experience", "lesson", "soul_candidate"}
EMBEDDING_MODEL = os.getenv("EMBEDDING_MODEL", "default")
EMBEDDING_DEVICE = os.getenv("EMBEDDING_DEVICE", "cpu")
COGNITION_INFERENCE_URL = os.getenv("COGNITION_INFERENCE_URL", "").strip()

COGNITIVE_STATS = {
    "facts_extracted": 0,
    "contradictions_detected": 0,
    "importance_total": 0.0,
    "importance_count": 0,
}

# --- Models ---
class QueryRequest(BaseModel):
    query: str
    agent: Optional[str] = None
    limit: int = 5
    type: Optional[Literal["general", "failure", "recovery", "lesson", "fact", "observation"]] = None
    layer: Optional[Literal["experience", "lesson", "soul_candidate"]] = None
    importance_weight: float = 0.0
    as_of: Optional[str] = None
    filter: Optional[dict] = None
    where: Optional[dict] = None

class RememberRequest(BaseModel):
    agent: str
    content: str
    tags: list[str] = []
    source: str = "agent"
    type: Literal["general", "failure", "recovery", "lesson", "fact", "observation"] = "general"
    layer: Optional[Literal["experience", "lesson", "soul_candidate"]] = None
    confidence: Optional[float] = None
    source_ids: Optional[str] = None
    validation_count: Optional[int] = None
    metadata: Optional[dict] = None


class ValidateRequest(BaseModel):
    memory_id: str
    outcome: Literal["success", "contradiction"]


class ReflectRequest(BaseModel):
    agent: str

class IngestRequest(BaseModel):
    path: str = "/docs"

class MigrateRequest(BaseModel):
    delete_source: bool = False

class ConsolidateRequest(BaseModel):
    agent: str

# --- App ---
app = FastAPI(title="Seidr", description="Valhalla Knowledge Service")

# ChromaDB's built-in embedding: all-MiniLM-L6-v2, CPU, ~80MB, auto-downloads
class _LazyEmbeddingFunction:
    """Defer ONNX model loading until first use to avoid startup deadlock under uvicorn."""

    def __init__(self):
        self._ef = None

    def _init(self):
        if EMBEDDING_MODEL != "default":
            self._ef = embedding_functions.SentenceTransformerEmbeddingFunction(
                model_name=EMBEDDING_MODEL,
                device=EMBEDDING_DEVICE,
            )
        else:
            self._ef = embedding_functions.DefaultEmbeddingFunction()

    def __call__(self, input):
        if self._ef is None:
            self._init()
        return self._ef(input)

    def __getattr__(self, name):
        if name.startswith("_"):
            raise AttributeError(name)
        if self._ef is None:
            self._init()
        return getattr(self._ef, name)


embed_fn = _LazyEmbeddingFunction()
chroma_client = None
collection_cache = {}

def get_client():
    global chroma_client
    if chroma_client is None:
        chroma_client = chromadb.HttpClient(host=CHROMA_HOST, port=CHROMA_PORT)
    return chroma_client

def sanitize_agent_name(agent: str) -> str:
    agent = (agent or "").lower()
    agent = re.sub(r"[^a-z0-9]+", "_", agent)
    agent = re.sub(r"_+", "_", agent).strip("_")
    agent = agent[:50]
    if len(agent) < 3:
        agent = agent.ljust(3, "_")
    return agent

def collection_name_for_agent(agent: Optional[str]) -> str:
    if not agent:
        return COLLECTION
    return f"{COLLECTION}_{sanitize_agent_name(agent)}"

def get_named_collection(name: str):
    if name not in collection_cache:
        collection_cache[name] = get_client().get_or_create_collection(
            name=name,
            embedding_function=embed_fn,
            metadata={"hnsw:space": "cosine"}
        )
    return collection_cache[name]

def get_collection(agent: Optional[str] = None):
    return get_named_collection(collection_name_for_agent(agent))

def list_agent_collections() -> list[str]:
    prefix = f"{COLLECTION}_"
    names = set(collection_cache.keys())
    try:
        for item in get_client().list_collections():
            if isinstance(item, str):
                name = item
            else:
                name = getattr(item, "name", "")
            if name.startswith(prefix):
                names.add(name)
    except Exception:
        pass
    return sorted(name for name in names if name.startswith(prefix))

def list_all_collection_names() -> list[str]:
    names = set(list_agent_collections())
    try:
        for item in get_client().list_collections():
            if isinstance(item, str):
                name = item
            else:
                name = getattr(item, "name", "")
            if name == COLLECTION:
                names.add(name)
    except Exception:
        pass
    if COLLECTION in collection_cache:
        names.add(COLLECTION)
    return sorted(names)

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

def wrap_where_filter(where):
    if not where:
        return None
    if not isinstance(where, dict):
        return where
    if "$and" in where:
        return where
    plain_keys = [k for k in where.keys() if not str(k).startswith("$")]
    if len(plain_keys) > 1:
        return {"$and": [{k: where[k]} for k in plain_keys]}
    return where

def hybrid_search(query: str, limit: int = 5, agent: str = None, where: dict = None):
    """Combine vector similarity + BM25 keyword search."""
    col = get_collection(agent)

    # Build filter
    search_where = wrap_where_filter(where.copy() if where else None)

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
    ids = results.get("ids", [[]])[0]

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
            "id": ids[i] if i < len(ids) else "",
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


def normalize_layer(value: Optional[str]) -> str:
    layer = (value or "").strip().lower()
    if layer in COGNITIVE_LAYERS:
        return layer
    return "experience"


def normalize_confidence(value: Optional[float], layer: str) -> float:
    if value is None:
        return 0.7 if layer == "lesson" else 0.5
    return max(0.0, min(1.0, float(value)))


def normalize_source_ids(value: Optional[str]) -> str:
    if value is None:
        return "[]"
    if isinstance(value, str):
        stripped = value.strip()
        if stripped == "":
            return "[]"
        try:
            parsed = json.loads(stripped)
            if isinstance(parsed, list):
                return json.dumps(parsed)
        except Exception:
            pass
        return stripped
    if isinstance(value, list):
        return json.dumps(value)
    return "[]"


def normalize_validation_count(value: Optional[int]) -> int:
    if value is None:
        return 0
    try:
        n = int(value)
    except Exception:
        return 0
    return max(0, n)


def parse_iso_datetime(value: Optional[str]) -> Optional[datetime]:
    if not value:
        return None
    ts = parse_timestamp(value)
    if ts is not None:
        return ts
    try:
        normalized = value.replace("Z", "+00:00")
        dt = datetime.fromisoformat(normalized)
        if dt.tzinfo is None:
            dt = dt.replace(tzinfo=timezone.utc)
        return dt.astimezone(timezone.utc)
    except Exception:
        return None


def cognition_enabled() -> bool:
    return COGNITION_INFERENCE_URL != ""


async def post_json_async(url: str, payload: dict, timeout: float = 20.0) -> dict:
    if httpx is not None:
        async with httpx.AsyncClient(timeout=timeout) as client:
            resp = await client.post(url, json=payload)
            resp.raise_for_status()
            return resp.json()

    def do_request():
        data = json.dumps(payload).encode("utf-8")
        req = urllib.request.Request(url, data=data, headers={"Content-Type": "application/json"}, method="POST")
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return json.loads(resp.read().decode("utf-8"))

    return await asyncio.to_thread(do_request)


def extract_first_json_fragment(text: str, open_char: str, close_char: str) -> Optional[str]:
    start = text.find(open_char)
    if start < 0:
        return None
    depth = 0
    for idx in range(start, len(text)):
        ch = text[idx]
        if ch == open_char:
            depth += 1
        elif ch == close_char:
            depth -= 1
            if depth == 0:
                return text[start:idx + 1]
    return None


def parse_json_array_text(text: str) -> Optional[list[str]]:
    fragment = extract_first_json_fragment(text.strip(), "[", "]")
    if fragment is None:
        return None
    try:
        parsed = json.loads(fragment)
    except Exception:
        return None
    if not isinstance(parsed, list):
        return None
    out = []
    for item in parsed:
        s = str(item).strip()
        if s:
            out.append(s)
    return out


def parse_json_object_text(text: str) -> Optional[dict]:
    fragment = extract_first_json_fragment(text.strip(), "{", "}")
    if fragment is None:
        return None
    try:
        parsed = json.loads(fragment)
    except Exception:
        return None
    if not isinstance(parsed, dict):
        return None
    return parsed


async def cognition_chat(system_prompt: str, user_prompt: str) -> Optional[str]:
    if not cognition_enabled():
        return None
    payload = {
        "model": "qwen",
        "temperature": 0.1,
        "messages": [
            {"role": "system", "content": system_prompt},
            {"role": "user", "content": user_prompt},
        ],
    }
    try:
        data = await post_json_async(f"{COGNITION_INFERENCE_URL.rstrip('/')}/v1/chat/completions", payload)
        choices = data.get("choices", [])
        if not choices:
            return None
        message = choices[0].get("message", {})
        content = message.get("content")
        if isinstance(content, str):
            return content.strip()
    except Exception as e:
        log("warn", "cognition call failed", {"error": str(e)})
    return None


async def extract_atomic_facts(content: str) -> list[str]:
    system = (
        "You are a fact extraction engine. Given text, extract individual atomic facts. "
        "Return ONLY a JSON array of strings, each being one fact. No other text."
    )
    reply = await cognition_chat(system, content)
    if not reply:
        return [content]
    facts = parse_json_array_text(reply)
    if not facts:
        return [content]
    COGNITIVE_STATS["facts_extracted"] += len(facts)
    return facts


async def score_importance(fact: str) -> tuple[float, str]:
    system = (
        "Score the importance of this fact for a software development team's institutional memory. "
        "Return ONLY a JSON object: {\"importance\": 0.0-1.0, \"scope\": \"universal|project|session\"}. No other text."
    )
    reply = await cognition_chat(system, fact)
    if not reply:
        return 0.5, "session"
    obj = parse_json_object_text(reply)
    if not obj:
        return 0.5, "session"
    importance = normalize_confidence(obj.get("importance"), "experience")
    scope = str(obj.get("scope", "session")).strip().lower()
    if scope not in {"universal", "project", "session"}:
        scope = "session"
    return importance, scope


async def detect_contradiction(old_fact: str, new_fact: str) -> tuple[bool, str]:
    system = (
        "Compare these two facts and determine if the new fact contradicts or supersedes the old fact. "
        "Return ONLY a JSON object: {\"contradicts\": true/false, \"explanation\": \"brief reason\"}. No other text."
    )
    user = f"Old fact: {old_fact}\nNew fact: {new_fact}"
    reply = await cognition_chat(system, user)
    if not reply:
        return False, ""
    obj = parse_json_object_text(reply)
    if not obj:
        return False, ""
    contradicts = bool(obj.get("contradicts", False))
    explanation = str(obj.get("explanation", "")).strip()
    return contradicts, explanation


def find_memory_record(memory_id: str):
    for name in list_all_collection_names():
        col = get_named_collection(name)
        result = col.get(ids=[memory_id], include=["documents", "metadatas"])
        ids = result.get("ids", [])
        if not ids:
            continue
        docs = result.get("documents", [])
        metas = result.get("metadatas", [])
        return name, col, ids[0], docs[0], (metas[0] or {})
    return None


def iter_collection_entries(col):
    total = col.count()
    if total == 0:
        return []
    results = col.get(limit=total, include=["documents", "metadatas"])
    return list(zip(results.get("ids", []), results.get("documents", []), results.get("metadatas", [])))


def prune_collection(name: str) -> dict:
    col = get_named_collection(name)
    total = col.count()
    if total == 0:
        return {"pruned": 0, "remaining": 0}

    pruned = 0
    for doc_id, _, meta in iter_collection_entries(col):
        meta = meta or {}
        layer = normalize_layer(meta.get("layer"))
        if layer == "soul_candidate":
            continue
        ttl_hours = LESSON_TTL_HOURS if layer == "lesson" else MEMORY_TTL_HOURS
        cutoff = utcnow() - timedelta(hours=ttl_hours)
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


def prune_expired_memories() -> dict:
    pruned = 0
    remaining = 0
    for name in list_all_collection_names():
        stats = prune_collection(name)
        pruned += stats["pruned"]
        remaining += stats["remaining"]
    return {"pruned": pruned, "remaining": remaining}


def split_sentences(text: str) -> list[str]:
    parts = re.split(r"(?<=[.!?])\s+", text.strip())
    return [part.strip() for part in parts if part.strip()]


def merge_cluster_documents(documents: list[str]) -> str:
    seen = set()
    merged = []
    for document in documents:
        for sentence in split_sentences(document):
            if sentence not in seen:
                seen.add(sentence)
                merged.append(sentence)
    if merged:
        return " ".join(merged)
    return "\n".join(dict.fromkeys(documents))


def consolidate_agent_memories(agent: str) -> dict:
    col = get_collection(agent)
    entries = iter_collection_entries(col)
    if not entries:
        return {"agent": agent, "consolidated": 0, "remaining": 0}

    by_type = {}
    for doc_id, document, meta in entries:
        meta = meta or {}
        mem_type = meta.get("type", "general")
        by_type.setdefault(mem_type, []).append((doc_id, document, meta))

    consolidated = 0
    for mem_type, items in by_type.items():
        if len(items) < 3:
            continue
        parent = list(range(len(items)))

        def find(x):
            while parent[x] != x:
                parent[x] = parent[parent[x]]
                x = parent[x]
            return x

        def union(a, b):
            ra, rb = find(a), find(b)
            if ra != rb:
                parent[rb] = ra

        for idx, (_, document, _) in enumerate(items):
            results = col.query(
                query_texts=[document],
                n_results=len(items),
                where=wrap_where_filter({"type": mem_type}),
                include=["distances"],
            )
            ids = results.get("ids", [[]])[0]
            distances = results.get("distances", [[]])[0]
            id_to_idx = {item_id: i for i, (item_id, _, _) in enumerate(items)}
            for match_id, distance in zip(ids, distances):
                if match_id not in id_to_idx or match_id == items[idx][0]:
                    continue
                if distance < 0.15:
                    union(idx, id_to_idx[match_id])

        clusters = {}
        for idx in range(len(items)):
            clusters.setdefault(find(idx), []).append(idx)

        for cluster_indices in clusters.values():
            if len(cluster_indices) < 3:
                continue
            cluster_items = [items[i] for i in cluster_indices]
            merged_content = merge_cluster_documents([doc for _, doc, _ in cluster_items])
            timestamps = [
                parse_timestamp((meta or {}).get("timestamp", ""))
                for _, _, meta in cluster_items
            ]
            latest_idx = max(
                range(len(cluster_items)),
                key=lambda i: timestamps[i] or datetime.min.replace(tzinfo=timezone.utc),
            )
            latest_meta = dict(cluster_items[latest_idx][2] or {})
            latest_meta["timestamp"] = (
                timestamps[latest_idx] or utcnow()
            ).astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
            latest_meta["consolidated_from"] = len(cluster_items)
            tag_values = []
            for _, _, meta in cluster_items:
                tags = (meta or {}).get("tags", "")
                if tags:
                    tag_values.extend(tag.strip() for tag in tags.split(",") if tag.strip())
            latest_meta["tags"] = ",".join(dict.fromkeys(tag_values))

            old_ids = [doc_id for doc_id, _, _ in cluster_items]
            new_id = f"{agent}-{int(time.time())}-consolidated-{os.urandom(4).hex()}"
            col.delete(ids=old_ids)
            col.add(documents=[merged_content], metadatas=[latest_meta], ids=[new_id])
            consolidated += 1

    return {"agent": agent, "consolidated": consolidated, "remaining": col.count()}


# --- Endpoints ---

@app.on_event("startup")
async def startup_prune_task():
    log("info", "embedding config", {"model": EMBEDDING_MODEL, "device": EMBEDDING_DEVICE})
    async def prune_loop():
        while True:
            try:
                prune_expired_memories()
                for name in list_agent_collections():
                    agent = name[len(f"{COLLECTION}_"):]
                    consolidate_agent_memories(agent)
            except Exception as e:
                log("error", "memory prune failed", {"error": str(e)})
            await asyncio.sleep(6 * 60 * 60)
    asyncio.create_task(prune_loop())

@app.get("/health")
async def health():
    try:
        count = sum(get_named_collection(name).count() for name in list_all_collection_names())
        return {"status": "ok", "memories": count, "collections": list_all_collection_names()}
    except Exception as e:
        return {"status": "error", "error": str(e)}



@app.get("/collections")
async def list_collections():
    try:
        collections = get_client().list_collections()
        names = []
        for item in collections:
            if isinstance(item, str):
                names.append(item)
            else:
                name = getattr(item, "name", "")
                if name:
                    names.append(name)
        return {"collections": sorted(set(names))}
    except Exception as e:
        return {"collections": [], "error": str(e)}

@app.get("/cognitive/status")
async def cognitive_status():
    count = COGNITIVE_STATS["importance_count"]
    avg_importance = (COGNITIVE_STATS["importance_total"] / count) if count > 0 else 0.0
    return {
        "enabled": cognition_enabled(),
        "inference_url": COGNITION_INFERENCE_URL,
        "stats": {
            "total_facts_extracted": COGNITIVE_STATS["facts_extracted"],
            "contradictions_detected": COGNITIVE_STATS["contradictions_detected"],
            "average_importance_score": round(avg_importance, 4),
        },
    }

@app.post("/query")
async def query(req: QueryRequest):
    """Search knowledge base. Called by agent recall tool."""
    where = req.where.copy() if req.where else {}
    if req.filter:
        where.update(req.filter)
    if req.type:
        where["type"] = req.type
    if req.layer:
        where["layer"] = req.layer
    if not where:
        where = None
    results = hybrid_search(req.query, limit=req.limit, agent=req.agent, where=where)
    as_of_dt = parse_iso_datetime(req.as_of) if req.as_of else None
    if as_of_dt is not None:
        filtered = []
        for item in results:
            meta = item.get("metadata", {}) or {}
            valid_until = parse_iso_datetime(meta.get("valid_until"))
            if valid_until is not None and valid_until < as_of_dt:
                continue
            filtered.append(item)
        results = filtered
    weight = max(0.0, min(1.0, float(req.importance_weight or 0.0)))
    if weight > 0.0:
        for item in results:
            meta = item.get("metadata", {}) or {}
            importance = normalize_confidence(meta.get("importance"), normalize_layer(meta.get("layer")))
            similarity_score = float(item.get("similarity", 0.0))
            final_score = (1.0 - weight) * similarity_score + weight * importance
            item["final_score"] = round(final_score, 4)
        results.sort(key=lambda x: x.get("final_score", 0.0), reverse=True)
        results = results[:req.limit]
    return {"results": results, "count": len(results)}

@app.post("/remember")
async def remember(req: RememberRequest):
    """Store a new memory. Called by agent remember tool."""
    col = get_collection(req.agent)
    memory_type = resolve_memory_type(req)
    cognitive_on = cognition_enabled()
    source_layer = normalize_layer(req.layer)

    def base_metadata(layer_override: Optional[str] = None) -> dict:
        layer = normalize_layer(layer_override or source_layer)
        metadata = {
            "agent": req.agent,
            "source": req.source,
            "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "tags": ",".join(req.tags) if req.tags else "",
            "type": memory_type,
            "layer": layer,
            "confidence": normalize_confidence(req.confidence, layer),
            "source_ids": normalize_source_ids(req.source_ids),
            "validation_count": normalize_validation_count(req.validation_count),
        }
        if req.metadata:
            metadata.update(req.metadata)
        metadata["layer"] = normalize_layer(metadata.get("layer"))
        metadata["confidence"] = normalize_confidence(metadata.get("confidence"), metadata["layer"])
        metadata["source_ids"] = normalize_source_ids(metadata.get("source_ids"))
        metadata["validation_count"] = normalize_validation_count(metadata.get("validation_count"))
        return metadata

    async def store_one(content: str, metadata: dict, check_contradiction: bool) -> tuple[Optional[str], Optional[str]]:
        duplicate_results = col.query(
            query_texts=[content],
            n_results=3,
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
                    return None, existing_id

        doc_id = f"{req.agent}-{int(time.time())}-{os.urandom(4).hex()}"
        if check_contradiction:
            similar = hybrid_search(content, limit=3, agent=req.agent)
            for item in similar:
                similarity = float(item.get("vector_score", item.get("similarity", 0.0)))
                old_id = (item.get("id") or "").strip()
                if similarity <= 0.40 or old_id == "":
                    continue
                log("info", "contradiction check", {
                    "new_id": doc_id,
                    "old_id": old_id,
                    "similarity": similarity,
                })
                old_content = item.get("content", "")
                contradicts, explanation = await detect_contradiction(old_content, content)
                log("info", "contradiction result", {
                    "old_id": old_id,
                    "contradicts": contradicts,
                    "explanation": explanation,
                })
                if not contradicts:
                    continue
                record = find_memory_record(old_id)
                if not record:
                    continue
                _, old_col, old_doc_id, old_doc, old_meta = record
                old_meta = dict(old_meta or {})
                old_meta["superseded_by"] = doc_id
                old_meta["valid_until"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
                if explanation:
                    old_meta["supersede_reason"] = explanation
                old_col.upsert(documents=[old_doc], metadatas=[old_meta], ids=[old_doc_id])
                log("info", "memory superseded", {
                    "old_id": old_doc_id,
                    "new_id": doc_id,
                    "reason": explanation,
                })
                COGNITIVE_STATS["contradictions_detected"] += 1

        col.add(documents=[content], metadatas=[metadata], ids=[doc_id])
        return doc_id, None

    if not cognitive_on:
        metadata = base_metadata()
        stored_id, similar_to = await store_one(req.content, metadata, check_contradiction=False)
        if stored_id is None:
            return {"id": None, "stored": False, "reason": "duplicate", "similar_to": similar_to}
        log("info", "memory stored", {"agent": req.agent, "id": stored_id, "tags": req.tags})
        return {"id": stored_id, "stored": True}

    if len(req.content or "") > 100:
        facts = await extract_atomic_facts(req.content)
        force_experience_layer = True
    else:
        facts = [req.content]
        force_experience_layer = False

    stored_ids = []
    duplicate_hits = []
    for fact in [f.strip() for f in facts if (f or "").strip()]:
        fact_meta = base_metadata("experience" if force_experience_layer else source_layer)
        importance, scope = await score_importance(fact)
        fact_meta["importance"] = round(importance, 4)
        fact_meta["scope"] = scope
        fact_meta["layer"] = "experience" if force_experience_layer else normalize_layer(fact_meta.get("layer"))
        COGNITIVE_STATS["importance_total"] += float(importance)
        COGNITIVE_STATS["importance_count"] += 1
        stored_id, similar_to = await store_one(fact, fact_meta, check_contradiction=True)
        if stored_id is None:
            duplicate_hits.append(similar_to)
            continue
        stored_ids.append(stored_id)
        log("info", "memory stored", {"agent": req.agent, "id": stored_id, "tags": req.tags, "layer": fact_meta.get("layer")})

    if not stored_ids:
        return {"id": None, "stored": False, "reason": "duplicate", "similar_to": duplicate_hits[0] if duplicate_hits else None}
    return {"id": stored_ids[0], "ids": stored_ids, "stored": True, "facts_stored": len(stored_ids)}

@app.get("/memories")
async def list_memories(agent: str = None, limit: int = 100, type: str = None):
    """List stored memories, optionally filtered by agent."""
    memories = []
    names = [collection_name_for_agent(agent)] if agent else list_all_collection_names()
    for name in names:
        col = get_named_collection(name)
        where = wrap_where_filter({"type": type} if type else None)
        results = col.get(where=where, limit=limit, include=["documents", "metadatas"])
        for doc, meta, doc_id in zip(results["documents"], results["metadatas"], results["ids"]):
            meta = meta or {}
            memories.append({
                "id": doc_id,
                "content": doc,
                "metadata": meta,
                "agent": meta.get("agent", ""),
                "source": meta.get("source", ""),
                "timestamp": meta.get("timestamp", ""),
                "type": meta.get("type", "general"),
                "tags": meta.get("tags", "").split(",") if meta.get("tags") else [],
            })
    memories.sort(key=lambda x: x["timestamp"], reverse=True)
    memories = memories[:limit]
    return {"memories": memories, "count": len(memories)}

@app.post("/prune")
async def prune():
    return prune_expired_memories()


@app.post("/validate")
async def validate_memory(req: ValidateRequest):
    record = find_memory_record(req.memory_id)
    if not record:
        return {"updated": False, "error": "memory not found"}
    _, col, doc_id, document, meta = record
    meta = dict(meta or {})
    current_conf = normalize_confidence(meta.get("confidence"), normalize_layer(meta.get("layer")))
    current_validations = normalize_validation_count(meta.get("validation_count"))
    if req.outcome == "success":
        current_validations += 1
        current_conf = min(1.0, current_conf + 0.1)
    else:
        current_conf = max(0.0, current_conf - 0.2)
    meta["validation_count"] = current_validations
    meta["confidence"] = round(current_conf, 4)
    col.upsert(documents=[document], metadatas=[meta], ids=[doc_id])
    return {"updated": True, "memory_id": doc_id, "metadata": meta}


@app.post("/reflect")
async def reflect(req: ReflectRequest):
    agent = (req.agent or "").strip()
    if not agent:
        return {"agent": "", "clusters": [], "count": 0}
    col = get_collection(agent)
    cutoff = utcnow() - timedelta(days=14)
    experiences = []
    for doc_id, document, meta in iter_collection_entries(col):
        meta = meta or {}
        layer = normalize_layer(meta.get("layer"))
        if layer != "experience":
            continue
        ts = parse_timestamp(meta.get("timestamp", ""))
        if ts is None or ts < cutoff:
            continue
        experiences.append((doc_id, document, meta, ts))
    if len(experiences) < 3:
        return {"agent": agent, "clusters": [], "count": 0}

    parent = list(range(len(experiences)))

    def find(x: int) -> int:
        while parent[x] != x:
            parent[x] = parent[parent[x]]
            x = parent[x]
        return x

    def union(a: int, b: int) -> None:
        ra, rb = find(a), find(b)
        if ra != rb:
            parent[rb] = ra

    id_to_idx = {doc_id: idx for idx, (doc_id, _, _, _) in enumerate(experiences)}
    for idx, (doc_id, document, _, _) in enumerate(experiences):
        results = col.query(
            query_texts=[document],
            n_results=len(experiences),
            include=["distances"],
        )
        ids = results.get("ids", [[]])[0]
        distances = results.get("distances", [[]])[0]
        for other_id, distance in zip(ids, distances):
            if other_id == doc_id or other_id not in id_to_idx or distance is None:
                continue
            similarity = 1.0 - float(distance)
            if similarity > 0.80:
                union(idx, id_to_idx[other_id])

    grouped = {}
    for idx in range(len(experiences)):
        grouped.setdefault(find(idx), []).append(idx)

    clusters = []
    for members in grouped.values():
        if len(members) < 3:
            continue
        cluster_memories = []
        memory_ids = []
        for i in members:
            mem_id, content, meta, ts = experiences[i]
            memory_ids.append(mem_id)
            cluster_memories.append({
                "id": mem_id,
                "content": content,
                "timestamp": ts.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
                "confidence": normalize_confidence(meta.get("confidence"), "experience"),
                "validation_count": normalize_validation_count(meta.get("validation_count")),
            })
        synthesis_prompt = (
            "Synthesize these related experiences into one reusable lesson with actionable steps. "
            f"Memory IDs: {', '.join(memory_ids)}."
        )
        clusters.append({
            "size": len(cluster_memories),
            "members": cluster_memories,
            "suggested_synthesis_prompt": synthesis_prompt,
        })

    clusters.sort(key=lambda c: c["size"], reverse=True)
    return {"agent": agent, "clusters": clusters, "count": len(clusters)}


@app.post("/migrate")
async def migrate(req: MigrateRequest):
    legacy_name = COLLECTION
    legacy = get_named_collection(legacy_name)
    if legacy.count() == 0:
        return {"migrated": 0, "deleted": 0, "source": legacy_name}
    migrated = 0
    deleted = 0
    entries = iter_collection_entries(legacy)
    for doc_id, document, meta in entries:
        meta = meta or {}
        agent = meta.get("agent")
        if not agent:
            continue
        get_collection(agent).upsert(documents=[document], metadatas=[meta], ids=[doc_id])
        migrated += 1
    if req.delete_source and entries:
        legacy.delete(ids=[doc_id for doc_id, _, _ in entries])
        deleted = len(entries)
    return {"migrated": migrated, "deleted": deleted, "source": legacy_name}

@app.post("/consolidate")
async def consolidate(req: ConsolidateRequest):
    return consolidate_agent_memories(req.agent)

@app.delete("/memories/{memory_id}")
async def delete_memory(memory_id: str):
    """Delete a specific memory."""
    for name in list_all_collection_names():
        get_named_collection(name).delete(ids=[memory_id])
    return {"deleted": memory_id}

@app.post("/ingest")
async def ingest(req: IngestRequest):
    """Ingest markdown files from a directory into the knowledge base."""
    import glob
    col = get_collection("ingest")
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
    embed_fn._init()
    uvicorn.run(app, host="0.0.0.0", port=port)
