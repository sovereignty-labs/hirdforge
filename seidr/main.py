"""
Seidr — Knowledge & Memory Service for Project Valhalla
PostgreSQL + pgvector hybrid retrieval with asynchronous cognitive processing.
"""

import asyncio
import glob
import hashlib
import json
import os
import re
import threading
import time
import urllib.request
from datetime import datetime, timedelta, timezone
from typing import Literal, Optional

import asyncpg
import httpx
from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import JSONResponse
from pydantic import BaseModel, Field

try:
    from mcp_server import create_mcp_app
    _mcp_available = True
except ImportError:
    _mcp_available = False


# --- Config ---
DATABASE_URL = os.getenv("DATABASE_URL", "postgresql://seidr:seidr@seidr-postgres:5432/seidr")
EMBED_URL = os.getenv("EMBED_URL", "").strip()
COGNITION_INFERENCE_URL = os.getenv("COGNITION_INFERENCE_URL", "").strip()
COLLECTION = os.getenv("COLLECTION_NAME", "valhalla_knowledge")
MEMORY_TTL_HOURS = int(os.getenv("MEMORY_TTL_HOURS", "240"))
LESSON_TTL_HOURS = 90 * 24
MEMORY_TYPES = {"general", "failure", "recovery", "lesson", "fact", "observation"}
COGNITIVE_LAYERS = {"experience", "lesson", "soul_candidate"}
EMBEDDING_MODEL = os.getenv("EMBEDDING_MODEL", "all-MiniLM-L6-v2")
EMBEDDING_DEVICE = os.getenv("EMBEDDING_DEVICE", "cpu")

COGNITIVE_STATS = {
    "facts_extracted": 0,
    "contradictions_detected": 0,
    "importance_total": 0.0,
    "importance_count": 0,
}

DB_POOL = None
EMBED_MODEL = None
MAIN_LOOP = None
APP_START_TIME = time.time()
AUDIT_CHAIN_LOCK_KEY = 424242

SCHEMA_STATEMENTS = [
    "CREATE EXTENSION IF NOT EXISTS vector;",
    """
    CREATE TABLE IF NOT EXISTS memories (
        id TEXT PRIMARY KEY,
        agent VARCHAR(64) NOT NULL,
        content TEXT NOT NULL,
        embedding vector(384),
        type VARCHAR(32) NOT NULL DEFAULT 'general',
        layer VARCHAR(32) NOT NULL DEFAULT 'experience',
        importance FLOAT DEFAULT 0.5,
        confidence FLOAT DEFAULT 0.5,
        scope VARCHAR(16) DEFAULT 'session',
        tags TEXT DEFAULT '',
        source TEXT DEFAULT 'agent',
        metadata JSONB DEFAULT '{}',
        superseded_by TEXT,
        supersede_reason TEXT,
        valid_until TIMESTAMP WITH TIME ZONE,
        source_ids TEXT DEFAULT '[]',
        validation_count INTEGER DEFAULT 0,
        created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
        expires_at TIMESTAMP WITH TIME ZONE,
        access_count INTEGER DEFAULT 0,
        last_accessed TIMESTAMP WITH TIME ZONE
    );
    """,
    """
    DO $$ BEGIN
      ALTER TABLE memories ADD COLUMN tsv tsvector GENERATED ALWAYS AS (to_tsvector('english', content)) STORED;
    EXCEPTION WHEN duplicate_column THEN NULL;
    END $$;
    """,
    "CREATE INDEX IF NOT EXISTS idx_memories_agent ON memories(agent);",
    "CREATE INDEX IF NOT EXISTS idx_memories_agent_type ON memories(agent, type);",
    "CREATE INDEX IF NOT EXISTS idx_memories_importance ON memories(importance DESC);",
    "CREATE INDEX IF NOT EXISTS idx_memories_expires ON memories(expires_at);",
    "CREATE INDEX IF NOT EXISTS idx_memories_tsv ON memories USING gin(tsv);",
    "CREATE INDEX IF NOT EXISTS idx_memories_embedding ON memories USING hnsw (embedding vector_cosine_ops) WITH (m = 16, ef_construction = 64);",
    """
    CREATE TABLE IF NOT EXISTS session_states (
        id TEXT PRIMARY KEY,
        agent VARCHAR(64) NOT NULL,
        session_id VARCHAR(128) DEFAULT '',
        content TEXT NOT NULL,
        message_count INTEGER DEFAULT 0,
        compressed BOOLEAN DEFAULT FALSE,
        compressed_at TIMESTAMP WITH TIME ZONE,
        updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
    );
    """,
    "CREATE INDEX IF NOT EXISTS idx_session_agent ON session_states(agent, updated_at DESC);",
    """
    CREATE TABLE IF NOT EXISTS relationships (
        id SERIAL PRIMARY KEY,
        agent VARCHAR(64) NOT NULL,
        entity_a VARCHAR(128) NOT NULL,
        entity_b VARCHAR(128) NOT NULL,
        relation_type VARCHAR(64) NOT NULL,
        strength FLOAT DEFAULT 0.5,
        evidence_count INTEGER DEFAULT 1,
        last_observed TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
        metadata JSONB DEFAULT '{}'
    );
    """,
    "CREATE INDEX IF NOT EXISTS idx_rel_agent ON relationships(agent);",
    "CREATE UNIQUE INDEX IF NOT EXISTS idx_relationships_unique ON relationships(agent, entity_a, entity_b, relation_type);",
    """
    CREATE TABLE IF NOT EXISTS access_log (
        id SERIAL PRIMARY KEY,
        agent VARCHAR(64) NOT NULL,
        memory_id TEXT,
        query_hash VARCHAR(64) NOT NULL,
        session_id VARCHAR(128),
        accessed_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
    );
    """,
    "CREATE INDEX IF NOT EXISTS idx_access_agent ON access_log(agent, accessed_at DESC);",
    """
    CREATE TABLE IF NOT EXISTS audit_log (
        id SERIAL PRIMARY KEY,
        timestamp TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        agent TEXT NOT NULL,
        operation TEXT NOT NULL,
        memory_id TEXT NOT NULL,
        collection TEXT NOT NULL,
        content_hash TEXT NOT NULL,
        prev_hash TEXT NOT NULL,
        chain_hash TEXT NOT NULL
    );
    """,
    "CREATE INDEX IF NOT EXISTS idx_audit_log_chain ON audit_log(chain_hash);",
    "CREATE INDEX IF NOT EXISTS idx_audit_log_agent ON audit_log(agent);",
]


# --- Models ---
class QueryRequest(BaseModel):
    query: str
    agent: Optional[str] = None
    limit: int = 5
    collections: Optional[list[str]] = None
    similarity_threshold: Optional[float] = None
    cursor: Optional[str] = None
    type: Optional[Literal["general", "failure", "recovery", "lesson", "fact", "observation"]] = None
    layer: Optional[Literal["experience", "lesson", "soul_candidate"]] = None
    importance_weight: float = 0.0
    as_of: Optional[str] = None
    filter: Optional[dict] = None
    where: Optional[dict] = None


class RememberRequest(BaseModel):
    agent: str
    content: str
    tags: list[str] = Field(default_factory=list)
    source: str = "agent"
    shared: bool = False
    type: Literal["general", "failure", "recovery", "lesson", "fact", "observation"] = "general"
    layer: Optional[Literal["experience", "lesson", "soul_candidate"]] = None
    confidence: Optional[float] = None
    source_ids: Optional[str] = None
    validation_count: Optional[int] = None
    metadata: Optional[dict] = None


class ValidateRequest(BaseModel):
    memory_id: str
    outcome: Literal["confirmed", "contradicted", "irrelevant"]
    context: Optional[dict] = None


class ReflectRequest(BaseModel):
    agent: str
    collections: Optional[list[str]] = None


class IngestRequest(BaseModel):
    path: str = "/docs"


class MigrateRequest(BaseModel):
    delete_source: bool = False


class ConsolidateRequest(BaseModel):
    agent: str


class SessionStateRequest(BaseModel):
    agent: str
    content: str
    session_id: str = ""
    message_count: int = 0


class MigrateFromChromaRequest(BaseModel):
    chroma_host: str = "chromadb.valhalla.svc"
    chroma_port: int = 8000


class UpdateMemoryRequest(BaseModel):
    content: Optional[str] = None
    type: Optional[str] = None
    layer: Optional[str] = None
    importance: Optional[float] = None
    tags: Optional[str] = None
    expires_at: Optional[str] = None
    collection: Optional[str] = None


class A2MMemoryCreate(BaseModel):
    agent: str
    content: str
    type: Literal["general", "failure", "recovery", "lesson", "fact", "observation"] = "general"
    scope: Optional[str] = None
    shared: bool = False
    provenance: Optional[str] = None
    metadata: Optional[dict] = None
    tags: Optional[list[str]] = None


class A2MStoreParams(BaseModel):
    memories: list[A2MMemoryCreate]


class A2MQueryFilters(BaseModel):
    types: Optional[list[str]] = None
    min_importance: Optional[float] = None
    tags_any: Optional[list[str]] = None


class A2MQueryParams(BaseModel):
    agent: str
    query: str
    collections: Optional[list[str]] = None
    limit: int = 10
    similarity_threshold: Optional[float] = None
    importance_weight: float = 0.0
    filters: Optional[A2MQueryFilters] = None
    cursor: Optional[str] = None


class A2MValidationItem(BaseModel):
    memory_id: str
    outcome: Literal["success", "contradiction"]
    context: Optional[dict] = None


class A2MValidateParams(BaseModel):
    validations: list[A2MValidationItem]


class A2MReflectParams(BaseModel):
    agent: str
    collections: Optional[list[str]] = None


class A2MLifecycleParams(BaseModel):
    action: str
    memory_id: Optional[str] = None
    memory_ids: Optional[list[str]] = None
    expires_at: Optional[str] = None
    collection: Optional[str] = None
    target_collection: Optional[str] = None
    agent: Optional[str] = None


# --- App ---
app = FastAPI(title="Seidr", description="Valhalla Knowledge Service")
if _mcp_available:
    app.mount("/mcp", create_mcp_app())


def log(level: str, msg: str, fields: dict = None):
    entry = {"ts": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "level": level, "msg": msg}
    if fields:
        entry.update(fields)
    print(json.dumps(entry), flush=True)


def sanitize_agent_name(agent: str) -> str:
    agent = (agent or "").lower()
    agent = re.sub(r"[^a-z0-9]+", "_", agent)
    agent = re.sub(r"_+", "_", agent).strip("_")
    agent = agent[:50]
    if len(agent) < 3:
        agent = agent.ljust(3, "_")
    return agent


def utcnow() -> datetime:
    return datetime.now(timezone.utc)


def format_timestamp(dt: Optional[datetime] = None) -> str:
    dt = dt or utcnow()
    return dt.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def parse_timestamp(value: str) -> Optional[datetime]:
    if not value:
        return None
    try:
        return datetime.strptime(value, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=timezone.utc)
    except ValueError:
        return None


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


def resolve_memory_type(req: RememberRequest) -> str:
    if req.metadata and req.metadata.get("type") in MEMORY_TYPES:
        return req.metadata["type"]
    return req.type


def sha256_hex(text: str) -> str:
    return hashlib.sha256((text or "").encode("utf-8")).hexdigest()


def compute_chain_hash(prev_hash, agent, operation, memory_id, content, timestamp_str):
    content_hash = sha256_hex(content)
    payload = "|".join(
        [
            str(prev_hash or ""),
            str(agent or ""),
            str(operation or ""),
            str(memory_id or ""),
            content_hash,
            str(timestamp_str or ""),
        ]
    )
    return sha256_hex(payload)


def compute_chain_hash_from_content_hash(prev_hash, agent, operation, memory_id, content_hash, timestamp_str):
    payload = "|".join(
        [
            str(prev_hash or ""),
            str(agent or ""),
            str(operation or ""),
            str(memory_id or ""),
            str(content_hash or ""),
            str(timestamp_str or ""),
        ]
    )
    return sha256_hex(payload)


def schedule_audit_log(agent: str, operation: str, memory_id: str, collection: str, content: str):
    try:
        asyncio.create_task(append_audit_log(agent, operation, memory_id, collection, content))
    except Exception as e:
        log("warn", "audit log scheduling failed", {"agent": agent, "operation": operation, "memory_id": memory_id, "error": str(e)})


def memory_ttl_days_for_type(memory_type: str) -> Optional[int]:
    normalized = (memory_type or "").strip().lower()
    if normalized == "soul_candidate":
        return None
    if normalized in {"lesson", "workflow"}:
        return 90
    return 10


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


def expires_at_for_layer(layer: str) -> Optional[datetime]:
    if layer == "soul_candidate":
        return None
    ttl_hours = LESSON_TTL_HOURS if layer == "lesson" else MEMORY_TTL_HOURS
    return utcnow() + timedelta(hours=ttl_hours)


def vector_literal(values: list[float]) -> str:
    return "[" + ",".join(f"{float(v):.8f}" for v in values) + "]"


def normalize_collection_names(collections: Optional[list[str]]) -> list[str]:
    if not collections:
        return []
    normalized = []
    seen = set()
    for collection in collections:
        name = sanitize_agent_name(collection)
        if not name or name in seen:
            continue
        seen.add(name)
        normalized.append(name)
    return normalized


def chunk_text(text: str, chunk_size: int = 1200, overlap: int = 200) -> list[str]:
    chunks = []
    start = 0
    while start < len(text):
        end = start + chunk_size
        chunks.append(text[start:end])
        start = end - overlap
    return [c.strip() for c in chunks if c.strip()]


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


def parse_json_array_objects(text: str) -> Optional[list[dict]]:
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
        if isinstance(item, dict):
            out.append(item)
    return out


def query_hash(agent: str, query: str) -> str:
    return hashlib.sha256(f"{agent}:{query}".encode("utf-8")).hexdigest()


def ensure_pool():
    if DB_POOL is None:
        raise RuntimeError("database pool is not initialized")
    return DB_POOL


def row_to_metadata(row) -> dict:
    meta = safe_metadata(row["metadata"])
    meta["agent"] = meta.get("agent", row["agent"])
    meta["source"] = meta.get("source", row["source"])
    meta["type"] = meta.get("type", row["type"])
    meta["layer"] = meta.get("layer", row["layer"])
    meta["confidence"] = meta.get("confidence", row["confidence"])
    meta["importance"] = meta.get("importance", row["importance"])
    meta["scope"] = meta.get("scope", row["scope"])
    meta["tags"] = meta.get("tags", row["tags"] or "")
    meta["source_ids"] = meta.get("source_ids", row["source_ids"] or "[]")
    meta["validation_count"] = meta.get("validation_count", row["validation_count"])
    meta["contradiction_count"] = meta.get("contradiction_count", 0)
    meta["irrelevant_count"] = meta.get("irrelevant_count", 0)
    if row["valid_until"] is not None:
        meta["valid_until"] = format_timestamp(row["valid_until"])
    if row["superseded_by"]:
        meta["superseded_by"] = row["superseded_by"]
    if row["supersede_reason"]:
        meta["supersede_reason"] = row["supersede_reason"]
    if "timestamp" not in meta:
        meta["timestamp"] = format_timestamp(row["created_at"])
    return meta


def safe_metadata(raw) -> dict:
    """Safely parse metadata that may be a JSON string, dict, or None."""
    if raw is None:
        return {}
    if isinstance(raw, str):
        try:
            parsed = json.loads(raw)
            if isinstance(parsed, dict):
                return parsed
        except Exception:
            pass
        return {}
    if isinstance(raw, dict):
        return dict(raw)
    return {}


def memory_row_to_result(row, vector_score: float = 0.0, keyword_score: float = 0.0, similarity: float = 0.0) -> dict:
    return {
        "id": row["id"],
        "content": row["content"],
        "metadata": row_to_metadata(row),
        "similarity": round(float(similarity), 4),
        "vector_score": round(float(vector_score), 4),
        "bm25_score": round(float(keyword_score), 4),
    }


async def dedupe_results_by_content(results: list[dict], threshold: float = 0.92) -> list[dict]:
    if len(results) < 2:
        return results
    embeddings = await embed([str(item.get("content", "")) for item in results])
    kept: list[dict] = []
    kept_embeddings: list[list[float]] = []
    for item, item_embedding in zip(results, embeddings):
        duplicate_index = None
        for idx, kept_embedding in enumerate(kept_embeddings):
            similarity = sum(float(a) * float(b) for a, b in zip(item_embedding, kept_embedding))
            if similarity > threshold:
                duplicate_index = idx
                break
        if duplicate_index is None:
            kept.append(item)
            kept_embeddings.append(item_embedding)
            continue
        if float(item.get("similarity", 0.0)) > float(kept[duplicate_index].get("similarity", 0.0)):
            kept[duplicate_index] = item
            kept_embeddings[duplicate_index] = item_embedding
    kept.sort(key=lambda item: item.get("similarity", 0.0), reverse=True)
    return kept


def parse_a2m_major_version(value: Optional[str]) -> Optional[int]:
    if not value:
        return None
    match = re.match(r"^\s*(\d+)(?:\.\d+)?\s*$", str(value))
    if not match:
        return None
    return int(match.group(1))


def coerce_tag_list(value) -> list[str]:
    if value is None:
        return []
    if isinstance(value, list):
        items = value
    elif isinstance(value, str):
        items = value.split(",")
    else:
        items = [value]
    out = []
    seen = set()
    for item in items:
        tag = str(item).strip()
        if not tag or tag in seen:
            continue
        seen.add(tag)
        out.append(tag)
    return out


def memory_recency_score(meta: dict) -> float:
    ts = parse_iso_datetime(meta.get("timestamp")) or parse_iso_datetime(meta.get("created_at")) or parse_iso_datetime(meta.get("updated_at"))
    if ts is None:
        return 0.0
    age_seconds = max(0.0, (utcnow() - ts).total_seconds())
    age_days = age_seconds / 86400.0
    return max(0.0, min(1.0, 1.0 - (age_days / 30.0)))


def serialize_memory_record(row) -> dict:
    if row is None:
        return None
    meta = row_to_metadata(row)
    tags = coerce_tag_list(meta.get("tags", ""))
    return {
        "id": row["id"],
        "agent": row["agent"],
        "content": row["content"],
        "type": row["type"],
        "layer": row["layer"],
        "importance": float(row["importance"] or 0.0),
        "confidence": float(row["confidence"] or 0.0),
        "scope": row["scope"],
        "tags": tags,
        "source": row["source"],
        "metadata": meta,
        "source_ids": meta.get("source_ids", row["source_ids"] or "[]"),
        "validation_count": int(row["validation_count"] or 0),
        "created_at": format_timestamp(row["created_at"]) if row["created_at"] else "",
        "expires_at": format_timestamp(row["expires_at"]) if row["expires_at"] else None,
        "access_count": int(row["access_count"] or 0),
        "last_accessed": format_timestamp(row["last_accessed"]) if row["last_accessed"] else None,
        "valid_until": format_timestamp(row["valid_until"]) if row["valid_until"] else None,
        "superseded_by": row["superseded_by"],
        "supersede_reason": row["supersede_reason"],
    }


def serialize_query_memory(item: dict) -> dict:
    meta = item.get("metadata", {}) or {}
    return {
        "id": item.get("id"),
        "agent": meta.get("agent", ""),
        "content": item.get("content", ""),
        "type": meta.get("type", "general"),
        "layer": meta.get("layer", "experience"),
        "importance": float(meta.get("importance", 0.0) or 0.0),
        "confidence": float(meta.get("confidence", 0.0) or 0.0),
        "scope": meta.get("scope", "session"),
        "tags": coerce_tag_list(meta.get("tags", "")),
        "source": meta.get("source", ""),
        "metadata": meta,
        "source_ids": meta.get("source_ids", "[]"),
        "validation_count": int(meta.get("validation_count", 0) or 0),
        "created_at": meta.get("timestamp", ""),
        "expires_at": meta.get("expires_at"),
        "access_count": int(meta.get("access_count", 0) or 0),
        "last_accessed": meta.get("last_accessed"),
        "valid_until": meta.get("valid_until"),
        "superseded_by": meta.get("superseded_by"),
        "supersede_reason": meta.get("supersede_reason"),
    }


def build_score_components(item: dict) -> dict:
    meta = item.get("metadata", {}) or {}
    return {
        "similarity": round(float(item.get("similarity", 0.0) or 0.0), 4),
        "importance": round(max(0.0, min(1.0, float(meta.get("importance", 0.0) or 0.0))), 4),
        "confidence": round(max(0.0, min(1.0, float(meta.get("confidence", 0.0) or 0.0))), 4),
        "recency": round(memory_recency_score(meta), 4),
    }


def jsonrpc_error(code: int, message: str, request_id=None) -> JSONResponse:
    return JSONResponse({"jsonrpc": "2.0", "id": request_id, "error": {"code": code, "message": message}})


def jsonrpc_result(result, request_id=None) -> JSONResponse:
    return JSONResponse({"jsonrpc": "2.0", "id": request_id, "result": result})


async def query_memory_data(req: QueryRequest):
    where = req.where.copy() if req.where else {}
    if req.filter:
        where.update(req.filter)
    if req.type:
        where["type"] = req.type
    if req.layer:
        where["layer"] = req.layer
    if req.as_of:
        as_of_dt = parse_iso_datetime(req.as_of)
        if as_of_dt is not None:
            where["as_of"] = as_of_dt
    else:
        as_of_dt = None
    if not where:
        where = None
    query_embedding = None
    if req.collections:
        query_embedding = (await embed([req.query]))[0]
        results = await run_hybrid_search_across_collections(
            req.query,
            req.collections,
            limit=req.limit,
            where=where,
            query_embedding=query_embedding,
        )
    else:
        results = await run_hybrid_search(req.query, limit=req.limit, agent=req.agent, where=where)
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
            importance = max(0.0, min(1.0, float(meta.get("importance", 0.0) or 0.0)))
            similarity_score = float(item.get("similarity", 0.0))
            final_score = (1.0 - weight) * similarity_score + weight * importance
            item["final_score"] = round(final_score, 4)
        results.sort(key=lambda x: x.get("final_score", 0.0), reverse=True)
        results = results[: req.limit]
    if req.similarity_threshold is not None:
        threshold = float(req.similarity_threshold)
        results = [item for item in results if float(item.get("similarity", 0.0)) >= threshold]
    if req.agent:
        asyncio.create_task(log_memory_access(req.agent, req.query, [item["id"] for item in results if item.get("id")]))
    return {"results": results, "count": len(results)}


async def remember_memory_data(req: RememberRequest):
    memory_type = resolve_memory_type(req)
    source_layer = normalize_layer(req.layer)
    agent = sanitize_agent_name(req.agent)
    target_collection = "warband_shared" if req.shared else agent

    def base_metadata(layer_override: Optional[str] = None) -> dict:
        layer = normalize_layer(layer_override or source_layer)
        metadata = {
            "agent": req.agent,
            "source": req.source,
            "timestamp": format_timestamp(),
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
        metadata["tags"] = metadata.get("tags", "")
        return metadata

    async def store_one(
        content: str,
        metadata: dict,
        check_contradiction: bool,
        collection_agent: str = target_collection,
        embedding_text: Optional[str] = None,
        contradictions: Optional[list[tuple[str, str]]] = None,
    ):
        collection_agent = sanitize_agent_name(collection_agent)
        if embedding_text is None:
            embedding_values = (await embed([content]))[0]
            embedding_text = vector_literal(embedding_values)
        duplicate_rows = await fetch_similar_for_dedup(collection_agent, memory_type, embedding_text, limit=3)
        for row in duplicate_rows:
            similarity = float(row["similarity"] or 0.0)
            existing_meta = safe_metadata(row["metadata"])
            if existing_meta.get("type", "general") == memory_type and similarity > 0.92:
                log("info", "memory deduplicated", {"agent": req.agent, "collection": collection_agent, "similar_to": row["id"]})
                return None, row["id"], 0

        detected_contradictions = contradictions if contradictions is not None else []
        if check_contradiction and contradictions is None:
            detected_contradictions = await detect_fact_contradictions(collection_agent, content, embedding_text)

        stored_id = await store_memory_row(
            agent=collection_agent,
            content=content,
            metadata=metadata,
            memory_type=memory_type,
            layer=normalize_layer(metadata.get("layer")),
            importance=float(metadata.get("importance", 0.5)),
            confidence=normalize_confidence(metadata.get("confidence"), normalize_layer(metadata.get("layer"))),
            scope=str(metadata.get("scope", "session")).strip().lower() or "session",
            source=str(metadata.get("source", req.source)),
            tags=str(metadata.get("tags", "")),
            source_ids=normalize_source_ids(metadata.get("source_ids")),
            validation_count=normalize_validation_count(metadata.get("validation_count")),
            embedding_text=embedding_text,
        )
        if detected_contradictions:
            await apply_contradictions(stored_id, detected_contradictions)
        return stored_id, None, len(detected_contradictions)

    async def auto_promote_shared_fact(content: str, metadata: dict, embedding_text: str):
        try:
            promoted_id, duplicate_id, _ = await store_one(
                content,
                metadata,
                check_contradiction=False,
                collection_agent="warband_shared",
                embedding_text=embedding_text,
            )
            if promoted_id is None:
                log("info", "shared memory deduplicated", {"agent": req.agent, "similar_to": duplicate_id})
            else:
                log("info", "memory auto-promoted", {"agent": req.agent, "id": promoted_id})
        except Exception as e:
            log("warn", "shared auto-promotion failed", {"agent": req.agent, "error": str(e)})

    facts_extracted = 0
    contradictions_found = 0
    auto_promoted = 0

    if not cognition_enabled():
        metadata = base_metadata()
        stored_id, similar_to, _ = await store_one(req.content, metadata, check_contradiction=False)
        if stored_id is None:
            row = await find_memory_record(similar_to) if similar_to else None
            return {
                "id": None,
                "stored": False,
                "reason": "duplicate",
                "similar_to": similar_to,
                "memory": serialize_memory_record(row) if row else None,
                "facts_extracted": facts_extracted,
                "contradictions_found": contradictions_found,
                "auto_promoted": auto_promoted,
                "cognitive_status": "skipped",
            }
        row = await find_memory_record(stored_id)
        log("info", "memory stored", {"agent": req.agent, "id": stored_id, "tags": req.tags})
        return {
            "id": stored_id,
            "stored": True,
            "memory": serialize_memory_record(row) if row else None,
            "facts_extracted": facts_extracted,
            "contradictions_found": contradictions_found,
            "auto_promoted": auto_promoted,
            "cognitive_status": "skipped",
        }

    if len(req.content or "") > 100:
        facts = await extract_atomic_facts(req.content)
        force_experience_layer = True
    else:
        facts = [req.content]
        force_experience_layer = False
    facts_extracted = len([fact for fact in facts if (fact or "").strip()])

    stored_ids = []
    duplicate_hits = []
    for fact in [f.strip() for f in facts if (f or "").strip()]:
        fact_meta = base_metadata("experience" if force_experience_layer else source_layer)
        embedding_values = (await embed([fact]))[0]
        embedding_text = vector_literal(embedding_values)
        importance_task = score_importance(fact)
        contradictions_task = detect_fact_contradictions(target_collection, fact, embedding_text)
        relationships_task = extract_relationships(fact, target_collection)
        importance_scope, contradictions, _ = await asyncio.gather(
            importance_task,
            contradictions_task,
            relationships_task,
            return_exceptions=True,
        )
        if isinstance(importance_scope, Exception):
            log("warn", "importance scoring failed", {"agent": agent, "error": str(importance_scope)})
            importance_scope = (0.5, "session")
        if isinstance(contradictions, Exception):
            log("warn", "contradiction detection failed", {"agent": agent, "error": str(contradictions)})
            contradictions = []
        importance, scope = importance_scope
        fact_meta["importance"] = round(importance, 4)
        fact_meta["scope"] = scope
        fact_meta["layer"] = "experience" if force_experience_layer else normalize_layer(fact_meta.get("layer"))
        COGNITIVE_STATS["importance_total"] += float(importance)
        COGNITIVE_STATS["importance_count"] += 1
        duplicate_rows = await fetch_similar_for_dedup(target_collection, memory_type, embedding_text, limit=3)
        duplicate_id = None
        for row in duplicate_rows:
            similarity = float(row["similarity"] or 0.0)
            existing_meta = safe_metadata(row["metadata"])
            if existing_meta.get("type", "general") == memory_type and similarity > 0.92:
                duplicate_id = row["id"]
                break
        if duplicate_id:
            log("info", "memory deduplicated", {"agent": req.agent, "collection": target_collection, "similar_to": duplicate_id})
            duplicate_hits.append(duplicate_id)
            continue

        stored_id, duplicate_id, contradiction_count = await store_one(
            fact,
            fact_meta,
            check_contradiction=True,
            collection_agent=target_collection,
            embedding_text=embedding_text,
            contradictions=contradictions if not isinstance(contradictions, list) else contradictions,
        )
        contradictions_found += contradiction_count
        if stored_id is None:
            if duplicate_id:
                duplicate_hits.append(duplicate_id)
            continue
        stored_ids.append(stored_id)
        log("info", "memory stored", {"agent": req.agent, "id": stored_id, "tags": req.tags, "layer": fact_meta.get("layer")})
        if not req.shared and importance > 0.7 and scope == "universal":
            shared_meta = dict(fact_meta)
            shared_meta["auto_promoted"] = True
            shared_meta["source_agent"] = req.agent
            shared_meta["agent"] = req.agent
            auto_promoted += 1
            asyncio.create_task(auto_promote_shared_fact(fact, shared_meta, embedding_text))

    if not stored_ids:
        return {
            "id": None,
            "stored": False,
            "reason": "duplicate",
            "similar_to": duplicate_hits[0] if duplicate_hits else None,
            "memory": serialize_memory_record(await find_memory_record(duplicate_hits[0])) if duplicate_hits else None,
            "facts_extracted": facts_extracted,
            "contradictions_found": contradictions_found,
            "auto_promoted": auto_promoted,
            "cognitive_status": "processed",
        }
    row = await find_memory_record(stored_ids[0])
    return {
        "id": stored_ids[0],
        "ids": stored_ids,
        "stored": True,
        "facts_stored": len(stored_ids),
        "memory": serialize_memory_record(row) if row else None,
        "facts_extracted": facts_extracted,
        "contradictions_found": contradictions_found,
        "auto_promoted": auto_promoted,
        "cognitive_status": "processed",
    }


async def validate_memory_data(req: ValidateRequest):
    row = await find_memory_record(req.memory_id)
    if not row:
        return {"updated": False, "error": "memory not found"}
    meta = row_to_metadata(row)
    current_conf = normalize_confidence(meta.get("confidence"), normalize_layer(meta.get("layer")))
    current_validations = normalize_validation_count(meta.get("validation_count"))
    current_contradictions = int(meta.get("contradiction_count", 0) or 0)
    current_irrelevant = int(meta.get("irrelevant_count", 0) or 0)
    if req.outcome == "confirmed":
        current_validations += 1
        current_conf = min(1.0, current_conf + 0.1)
    elif req.outcome == "contradicted":
        current_contradictions += 1
        current_conf = max(0.0, current_conf - 0.2)
    else:
        current_irrelevant += 1
    meta["validation_count"] = current_validations
    meta["confidence"] = round(current_conf, 4)
    meta["contradiction_count"] = current_contradictions
    meta["irrelevant_count"] = current_irrelevant
    if req.context:
        meta["validation_context"] = req.context
    await ensure_pool().execute(
        """
        UPDATE memories
        SET validation_count = $2, confidence = $3, metadata = $4::jsonb
        WHERE id = $1
        """,
        req.memory_id,
        current_validations,
        current_conf,
        json.dumps(meta),
    )
    updated = await find_memory_record(req.memory_id)
    schedule_audit_log(row["agent"], "update", req.memory_id, row["agent"], row["content"])
    return {
        "updated": True,
        "memory_id": req.memory_id,
        "metadata": meta,
        "memory": serialize_memory_record(updated) if updated else None,
        "validation_count": current_validations,
        "contradiction_count": current_contradictions,
        "irrelevant_count": current_irrelevant,
    }


async def update_memory_data(memory_id: str, req: UpdateMemoryRequest):
    row = await find_memory_record(memory_id)
    if not row:
        raise HTTPException(status_code=404, detail="memory not found")
    if req.type is not None and req.type not in MEMORY_TYPES:
        raise HTTPException(status_code=400, detail="invalid memory type")
    if req.layer is not None and req.layer not in COGNITIVE_LAYERS:
        raise HTTPException(status_code=400, detail="invalid cognitive layer")
    if req.importance is not None and not (0.0 <= float(req.importance) <= 1.0):
        raise HTTPException(status_code=400, detail="importance must be between 0.0 and 1.0")

    content = row["content"]
    memory_type = row["type"]
    layer = row["layer"]
    importance = float(row["importance"] or 0.5)
    tags = row["tags"] or ""
    embedding_text = None
    expires_at = row["expires_at"]
    metadata = row_to_metadata(row)
    agent = row["agent"]

    if req.content is not None:
        content = req.content
        embedding_values = (await embed([content]))[0]
        embedding_text = vector_literal(embedding_values)
        metadata["content_updated_at"] = format_timestamp()
    if req.type is not None:
        memory_type = req.type
        metadata["type"] = req.type
    if req.layer is not None:
        layer = req.layer
        expires_at = expires_at_for_layer(layer)
        metadata["layer"] = req.layer
    if req.importance is not None:
        importance = max(0.0, min(1.0, float(req.importance)))
        metadata["importance"] = round(importance, 4)
    if req.tags is not None:
        tags = req.tags
        metadata["tags"] = req.tags
    if req.expires_at is not None:
        parsed_expires_at = parse_iso_datetime(req.expires_at)
        if parsed_expires_at is None:
            raise HTTPException(status_code=400, detail="invalid expires_at")
        expires_at = parsed_expires_at
    if req.collection is not None:
        agent = sanitize_agent_name(req.collection)
        metadata["agent"] = agent
        metadata["collection"] = req.collection

    metadata["updated_at"] = format_timestamp()
    if embedding_text is None:
        existing_embedding = await ensure_pool().fetchrow("SELECT embedding FROM memories WHERE id = $1", memory_id)
        embedding_text = str(existing_embedding["embedding"])

    await ensure_pool().execute(
        """
        UPDATE memories
        SET agent = $2,
            content = $3,
            embedding = $4::vector,
            type = $5,
            layer = $6,
            importance = $7,
            tags = $8,
            metadata = $9::jsonb,
            expires_at = $10
        WHERE id = $1
        """,
        memory_id,
        agent,
        content,
        embedding_text,
        memory_type,
        layer,
        importance,
        tags,
        json.dumps(metadata),
        expires_at,
    )
    updated = await find_memory_record(memory_id)
    if updated:
        schedule_audit_log(updated["agent"], "update", memory_id, updated["agent"], updated["content"])
        if req.collection is not None and sanitize_agent_name(req.collection) == "warband_shared":
            schedule_audit_log(updated["agent"], "promote", memory_id, sanitize_agent_name(req.collection), updated["content"])
    return {"updated": True, "memory": serialize_memory_record(updated)}


async def prune_memory_data() -> dict:
    return await prune_expired_memories()


async def consolidate_memory_data(req: ConsolidateRequest) -> dict:
    return await consolidate_agent_memories(req.agent)


async def delete_memory_data(memory_id: str) -> dict:
    row = await find_memory_record(memory_id)
    await ensure_pool().execute("DELETE FROM memories WHERE id = $1", memory_id)
    if row:
        schedule_audit_log(row["agent"], "delete", memory_id, row["agent"], row["content"])
    return {"deleted": memory_id}


async def health_data():
    try:
        pool = ensure_pool()
        count_row = await pool.fetchrow("SELECT COUNT(*) AS count FROM memories")
        ss_row = await pool.fetchrow("SELECT COUNT(*) AS count FROM session_states")
        collections = await fetch_agent_names()
        return {
            "status": "ok",
            "memories": int(count_row["count"] or 0),
            "collections": collections,
            "session_states": int(ss_row["count"] or 0),
        }
    except Exception as e:
        return {"status": "error", "error": str(e)}


async def cognitive_status_data():
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


async def reflect_data(req: ReflectRequest):
    agent = (req.agent or "").strip()
    if not agent and not req.collections:
        return {"agent": "", "clusters": [], "count": 0, "total_memories_analyzed": 0}
    pool = ensure_pool()
    cutoff = utcnow() - timedelta(days=14)
    collections = normalize_collection_names(req.collections) if req.collections else [sanitize_agent_name(agent)]
    if not collections:
        return {"agent": agent, "clusters": [], "count": 0, "total_memories_analyzed": 0}
    experiences = await pool.fetch(
        """
        SELECT id, content, metadata, created_at, confidence, validation_count, embedding
        FROM memories
        WHERE agent = ANY($1::text[]) AND layer = 'experience' AND created_at >= $2
        ORDER BY created_at DESC
        """,
        collections,
        cutoff,
    )
    if len(experiences) < 3:
        return {"agent": agent or collections[0], "clusters": [], "count": 0, "total_memories_analyzed": len(experiences)}

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

    id_to_idx = {row["id"]: idx for idx, row in enumerate(experiences)}
    for idx, row in enumerate(experiences):
        results = await pool.fetch(
            """
            SELECT id, 1 - (embedding <=> $2::vector) AS similarity
            FROM memories
            WHERE agent = ANY($1::text[]) AND layer = 'experience'
            ORDER BY embedding <=> $2::vector
            LIMIT $3
            """,
            collections,
            row["embedding"],
            len(experiences),
        )
        for other in results:
            other_id = other["id"]
            similarity = float(other["similarity"] or 0.0)
            if other_id == row["id"] or other_id not in id_to_idx:
                continue
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
            row = experiences[i]
            memory_ids.append(row["id"])
            cluster_memories.append(
                {
                    "id": row["id"],
                    "content": row["content"],
                    "timestamp": format_timestamp(row["created_at"]),
                    "confidence": normalize_confidence(row["confidence"], "experience"),
                    "validation_count": normalize_validation_count(row["validation_count"]),
                }
            )
        synthesis_prompt = (
            "Synthesize these related experiences into one reusable lesson with actionable steps. "
            f"Memory IDs: {', '.join(memory_ids)}."
        )
        clusters.append({"size": len(cluster_memories), "members": cluster_memories, "suggested_synthesis_prompt": synthesis_prompt})

    clusters.sort(key=lambda c: c["size"], reverse=True)
    return {"agent": agent or collections[0], "clusters": clusters, "count": len(clusters), "total_memories_analyzed": len(experiences)}


async def init_db():
    pool = ensure_pool()
    async with pool.acquire() as conn:
        for statement in SCHEMA_STATEMENTS:
            await conn.execute(statement)


async def embed(texts: list[str]) -> list[list[float]]:
    if not texts:
        return []
    if EMBED_URL:
        async with httpx.AsyncClient(timeout=10.0) as client:
            resp = await client.post(f"{EMBED_URL.rstrip('/')}/embed", json={"texts": texts})
            resp.raise_for_status()
            data = resp.json()
            embeddings = data.get("embeddings", [])
            return [list(map(float, item)) for item in embeddings]
    if EMBED_MODEL is None:
        raise RuntimeError("embedding model is not initialized")

    def encode():
        return EMBED_MODEL.encode(texts, normalize_embeddings=True).tolist()

    return await asyncio.to_thread(encode)


async def post_json_async(url: str, payload: dict, timeout: float = 20.0) -> dict:
    async with httpx.AsyncClient(timeout=timeout) as client:
        resp = await client.post(url, json=payload)
        resp.raise_for_status()
        return resp.json()


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
        "Extract operational facts from the following agent memory. Focus ONLY on:\n"
        "- Tool outcomes: which tools succeeded or failed, with what parameters\n"
        "- Task completions: what was built, which repo, which branch, PR numbers\n"
        "- Failure patterns: what went wrong and why\n"
        "- Recovery patterns: what fixed a problem\n"
        "- Code patterns: file paths, language used, architectural decisions\n\n"
        "DO NOT extract:\n"
        "- Agent identity or personality descriptions\n"
        "- Communication style observations\n"
        "- Role descriptions or team structure\n"
        "- Anything that starts with 'I am' or describes who someone is\n\n"
        "Output each fact on its own line. Each fact should be actionable — something that would help an agent avoid a mistake or replicate a success. "
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
        "Facts about tool failures, PR completions, and recovery patterns should usually score 0.7 or higher. "
        "Facts about personality, identity, or who someone is should score 0.1 or lower, and should be dropped entirely when possible. "
        "Prefer higher scores for actionable operational knowledge, code patterns, and repeatable task outcomes. "
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


async def extract_relationships(fact: str, agent: str):
    try:
        if not cognition_enabled() or not fact.strip():
            return []
        system = (
            "Extract entity relationships from the fact. "
            "Entity types may include: file, repo, agent, tool, concept. "
            "Return ONLY a JSON array of objects with this exact shape: "
            "[{\"source\": \"entity_a\", \"target\": \"entity_b\", \"relation\": \"type\", \"strength\": 0.0}] "
            "Use strength in the range 0.0 to 1.0. Do not include any prose."
        )
        reply = await cognition_chat(system, fact)
        if not reply:
            return []
        relationships = parse_json_array_objects(reply)
        if not relationships:
            return []
        pool = ensure_pool()
        stored = []
        for item in relationships:
            source = str(item.get("source", "")).strip()[:128]
            target = str(item.get("target", "")).strip()[:128]
            relation = str(item.get("relation", "")).strip()[:64]
            if not source or not target or not relation:
                continue
            try:
                strength = max(0.0, min(1.0, float(item.get("strength", 0.5))))
            except Exception:
                strength = 0.5
            metadata = {
                "fact": fact,
                "timestamp": format_timestamp(),
            }
            await pool.execute(
                """
                INSERT INTO relationships (
                    agent, entity_a, entity_b, relation_type, strength, evidence_count, last_observed, metadata
                ) VALUES (
                    $1, $2, $3, $4, $5, 1, NOW(), $6::jsonb
                )
                ON CONFLICT (agent, entity_a, entity_b, relation_type)
                DO UPDATE SET
                    strength = GREATEST(relationships.strength, EXCLUDED.strength),
                    evidence_count = relationships.evidence_count + 1,
                    last_observed = NOW(),
                    metadata = EXCLUDED.metadata
                """,
                sanitize_agent_name(agent),
                source,
                target,
                relation,
                strength,
                json.dumps(metadata),
            )
            stored.append({"source": source, "target": target, "relation": relation, "strength": strength})
        if stored:
            log("info", "relationships extracted", {"agent": sanitize_agent_name(agent), "count": len(stored)})
        return stored
    except Exception as e:
        log("warn", "relationship extraction failed", {"agent": sanitize_agent_name(agent), "error": str(e)})
        return []


async def fetch_agent_names() -> list[str]:
    pool = ensure_pool()
    rows = await pool.fetch("SELECT DISTINCT agent FROM memories ORDER BY agent")
    return [row["agent"] for row in rows]


async def find_memory_record(memory_id: str):
    pool = ensure_pool()
    return await pool.fetchrow(
        """
        SELECT id, agent, content, type, layer, importance, confidence, scope, tags, source,
               metadata, superseded_by, supersede_reason, valid_until, source_ids,
               validation_count, created_at, expires_at, access_count, last_accessed
        FROM memories
        WHERE id = $1
        """,
        memory_id,
    )


async def list_memories_for_agent(
    agent: Optional[str],
    limit: int = 100,
    memory_type: Optional[str] = None,
    offset: int = 0,
):
    pool = ensure_pool()
    limit = max(1, int(limit))
    offset = max(0, int(offset))
    sql = """
        SELECT id, agent, content, type, layer, importance, confidence, scope, tags, source,
               metadata, superseded_by, supersede_reason, valid_until, source_ids,
               validation_count, created_at, expires_at, access_count, last_accessed
        FROM memories
    """
    conditions = []
    params = []
    idx = 1
    if agent:
        conditions.append(f"agent = ${idx}")
        params.append(sanitize_agent_name(agent))
        idx += 1
    if memory_type:
        conditions.append(f"type = ${idx}")
        params.append(memory_type)
        idx += 1
    if conditions:
        sql += " WHERE " + " AND ".join(conditions)
    sql += f" ORDER BY created_at DESC LIMIT ${idx} OFFSET ${idx + 1}"
    params.append(limit)
    params.append(offset)
    return await pool.fetch(sql, *params)


async def get_co_access_scores(agent: str, memory_ids: list[str], limit: int = 10) -> dict[str, float]:
    if not agent or not memory_ids:
        return {}
    pool = ensure_pool()
    agent_name = sanitize_agent_name(agent)
    count_row = await pool.fetchrow("SELECT COUNT(*) AS count FROM access_log WHERE agent = $1", agent_name)
    if int(count_row["count"] or 0) < 5:
        return {}
    rows = await pool.fetch(
        """
        SELECT a2.memory_id AS partner_memory_id, COUNT(*)::float AS co_count
        FROM access_log a1
        JOIN access_log a2
          ON a1.agent = a2.agent
         AND a1.query_hash = a2.query_hash
        WHERE a1.agent = $1
          AND a1.memory_id = ANY($2::text[])
          AND a2.memory_id IS NOT NULL
          AND a2.memory_id <> a1.memory_id
        GROUP BY a2.memory_id
        ORDER BY co_count DESC
        LIMIT $3
        """,
        agent_name,
        memory_ids,
        limit,
    )
    if not rows:
        return {}
    max_count = max(float(row["co_count"] or 0.0) for row in rows) or 1.0
    scores = {}
    for row in rows:
        partner_id = row["partner_memory_id"]
        if not partner_id:
            continue
        scores[str(partner_id)] = float(row["co_count"] or 0.0) / max_count
    return scores


async def run_hybrid_search(
    query: str,
    limit: int = 5,
    agent: Optional[str] = None,
    where: Optional[dict] = None,
    query_embedding: Optional[list[float]] = None,
):
    if not query.strip():
        return []
    pool = ensure_pool()
    embeddings = [query_embedding] if query_embedding is not None else await embed([query])
    query_vector = vector_literal(embeddings[0])
    limit = max(1, limit)
    filters = where.copy() if where else {}
    where_clauses = []
    params = []
    idx = 1
    if agent:
        where_clauses.append(f"agent = ${idx}")
        params.append(sanitize_agent_name(agent))
        idx += 1
    if filters.get("type"):
        where_clauses.append(f"type = ${idx}")
        params.append(filters["type"])
        idx += 1
    if filters.get("layer"):
        where_clauses.append(f"layer = ${idx}")
        params.append(filters["layer"])
        idx += 1
    if filters.get("as_of"):
        where_clauses.append(f"(valid_until IS NULL OR valid_until >= ${idx})")
        params.append(filters["as_of"])
        idx += 1
    for key, value in filters.items():
        if key in {"type", "layer", "as_of"}:
            continue
        if value is None:
            continue
        where_clauses.append(f"metadata ->> ${idx} = ${idx + 1}")
        params.extend([str(key), str(value)])
        idx += 2
    where_sql = " AND ".join(where_clauses) if where_clauses else "TRUE"

    vector_sql = f"""
        SELECT id, agent, content, type, layer, importance, confidence, scope, tags, source, metadata,
               superseded_by, supersede_reason, valid_until, source_ids, validation_count,
               created_at, expires_at, access_count, last_accessed,
               1 - (embedding <=> ${idx}::vector) AS vector_score
        FROM memories
        WHERE {where_sql}
        ORDER BY embedding <=> ${idx}::vector
        LIMIT ${idx + 1}
    """
    vector_params = params + [query_vector, min(limit * 3, 20)]

    keyword_sql = f"""
        SELECT id,
               ts_rank(tsv, plainto_tsquery('english', ${idx})) AS keyword_score
        FROM memories
        WHERE {where_sql}
          AND tsv @@ plainto_tsquery('english', ${idx})
        ORDER BY keyword_score DESC
        LIMIT ${idx + 1}
    """
    keyword_params = params + [query, min(limit * 3, 20)]

    vector_rows, keyword_rows = await asyncio.gather(
        pool.fetch(vector_sql, *vector_params),
        pool.fetch(keyword_sql, *keyword_params),
    )
    keyword_score_by_id = {row["id"]: float(row["keyword_score"] or 0.0) for row in keyword_rows}
    max_keyword_score = max(keyword_score_by_id.values()) if keyword_score_by_id else 1.0
    candidate_ids = [row["id"] for row in vector_rows if row["id"]]
    co_access_scores = {}
    use_four_signal = False
    if agent and candidate_ids:
        co_access_scores = await get_co_access_scores(agent, candidate_ids, limit=max(limit * 3, 10))
        use_four_signal = bool(co_access_scores)
    merged = []
    for row in vector_rows:
        vector_score = max(0.0, min(1.0, float(row["vector_score"] or 0.0)))
        raw_keyword_score = max(0.0, keyword_score_by_id.get(row["id"], 0.0))
        normalized_keyword = raw_keyword_score / max_keyword_score if max_keyword_score > 0 else 0.0
        importance_score = max(0.0, min(1.0, float(row["importance"] or 0.0)))
        co_access_score = max(0.0, min(1.0, float(co_access_scores.get(row["id"], 0.0))))
        if use_four_signal:
            combined = 0.50 * vector_score + 0.30 * normalized_keyword + 0.10 * importance_score + 0.10 * co_access_score
        else:
            combined = 0.6 * vector_score + 0.4 * normalized_keyword
        merged.append(memory_row_to_result(row, vector_score=vector_score, keyword_score=normalized_keyword, similarity=combined))
    merged.sort(key=lambda item: item["similarity"], reverse=True)
    return merged[:limit]


async def run_hybrid_search_across_collections(
    query: str,
    collections: list[str],
    limit: int = 5,
    where: Optional[dict] = None,
    query_embedding: Optional[list[float]] = None,
):
    limit = max(1, int(limit))
    collection_names = normalize_collection_names(collections)
    if not collection_names:
        return await run_hybrid_search(query, limit=limit, where=where, query_embedding=query_embedding)

    per_collection_limit = max(limit * 3, limit + 2)
    search_tasks = [
        run_hybrid_search(query, limit=per_collection_limit, agent=collection, where=where, query_embedding=query_embedding)
        for collection in collection_names
    ]
    search_results = await asyncio.gather(*search_tasks, return_exceptions=True)
    merged = []
    for collection, result in zip(collection_names, search_results):
        if isinstance(result, Exception):
            log("warn", "collection search failed", {"collection": collection, "error": str(result)})
            continue
        merged.extend(result)
    merged.sort(key=lambda item: item.get("similarity", 0.0), reverse=True)
    merged = await dedupe_results_by_content(merged, threshold=0.92)
    return merged[:limit]


async def log_memory_access(agent: str, query: str, memory_ids: list[str]):
    if not memory_ids:
        return
    pool = ensure_pool()
    agent_name = sanitize_agent_name(agent)
    qhash = query_hash(agent_name, query)
    now = utcnow()
    async with pool.acquire() as conn:
        async with conn.transaction():
            for memory_id in memory_ids:
                await conn.execute(
                    """
                    INSERT INTO access_log (agent, memory_id, query_hash, session_id, accessed_at)
                    VALUES ($1, $2, $3, '', $4)
                    """,
                    agent_name,
                    memory_id,
                    qhash,
                    now,
                )
            await conn.execute(
                """
                UPDATE memories
                SET access_count = access_count + 1, last_accessed = $2
                WHERE id = ANY($1::text[])
                """,
                memory_ids,
                now,
            )


async def fetch_similar_for_dedup(agent: str, memory_type: str, embedding_text: str, limit: int = 3):
    pool = ensure_pool()
    return await pool.fetch(
        """
        SELECT id, metadata, 1 - (embedding <=> $2::vector) AS similarity
        FROM memories
        WHERE agent = $1 AND type = $3
        ORDER BY embedding <=> $2::vector
        LIMIT $4
        """,
        sanitize_agent_name(agent),
        embedding_text,
        memory_type,
        limit,
    )


async def fetch_similar_for_contradiction(agent: str, embedding_text: str, limit: int = 3):
    pool = ensure_pool()
    return await pool.fetch(
        """
        SELECT id, content, metadata, 1 - (embedding <=> $2::vector) AS similarity
        FROM memories
        WHERE agent = $1
        ORDER BY embedding <=> $2::vector
        LIMIT $3
        """,
        sanitize_agent_name(agent),
        embedding_text,
        limit,
    )


async def apply_contradictions(new_id: str, contradictions: list[tuple[str, str]]):
    if not contradictions:
        return
    pool = ensure_pool()
    now = utcnow()
    for old_id, explanation in contradictions:
        old_row = await pool.fetchrow("SELECT agent, content FROM memories WHERE id = $1", old_id)
        await pool.execute(
            """
            UPDATE memories
            SET superseded_by = $2,
                valid_until = $3,
                supersede_reason = $4,
                metadata = jsonb_set(
                    jsonb_set(
                        COALESCE(metadata, '{}'::jsonb),
                        '{valid_until}',
                        to_jsonb($5::text),
                        true
                    ),
                    '{superseded_by}',
                    to_jsonb($2::text),
                    true
                )
            WHERE id = $1
            """,
            old_id,
            new_id,
            now,
            explanation or None,
            format_timestamp(now),
        )
        if old_row:
            schedule_audit_log(old_row["agent"], "update", old_id, old_row["agent"], old_row["content"])
        COGNITIVE_STATS["contradictions_detected"] += 1


async def detect_fact_contradictions(agent: str, fact: str, embedding_text: str):
    rows = await fetch_similar_for_contradiction(agent, embedding_text, limit=3)
    contradictions = []
    for row in rows:
        similarity = float(row["similarity"] or 0.0)
        if similarity <= 0.40:
            continue
        old_id = row["id"]
        old_content = row["content"] or ""
        log("info", "contradiction check", {"old_id": old_id, "similarity": similarity})
        contradicts, explanation = await detect_contradiction(old_content, fact)
        log("info", "contradiction result", {"old_id": old_id, "contradicts": contradicts, "explanation": explanation})
        if contradicts:
            contradictions.append((old_id, explanation))
    return contradictions


async def append_audit_log(agent: str, operation: str, memory_id: str, collection: str, content: str):
    try:
        pool = ensure_pool()
        timestamp_dt = utcnow()
        timestamp_str = format_timestamp(timestamp_dt)
        content_hash = sha256_hex(content)
        async with pool.acquire() as conn:
            async with conn.transaction():
                await conn.execute("SELECT pg_advisory_xact_lock($1)", AUDIT_CHAIN_LOCK_KEY)
                prev_row = await conn.fetchrow("SELECT chain_hash FROM audit_log ORDER BY id DESC LIMIT 1")
                prev_hash = prev_row["chain_hash"] if prev_row else "genesis"
                chain_hash = compute_chain_hash_from_content_hash(
                    prev_hash,
                    agent,
                    operation,
                    memory_id,
                    content_hash,
                    timestamp_str,
                )
                await conn.execute(
                    """
                    INSERT INTO audit_log (
                        timestamp, agent, operation, memory_id, collection, content_hash, prev_hash, chain_hash
                    ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
                    """,
                    timestamp_dt,
                    str(agent or ""),
                    str(operation or ""),
                    str(memory_id or ""),
                    str(collection or ""),
                    content_hash,
                    prev_hash,
                    chain_hash,
                )
    except Exception as e:
        log("warn", "audit log append failed", {"agent": agent, "operation": operation, "memory_id": memory_id, "error": str(e)})


async def store_memory_row(
    agent: str,
    content: str,
    metadata: dict,
    memory_type: str,
    layer: str,
    importance: float,
    confidence: float,
    scope: str,
    source: str,
    tags: str,
    source_ids: str,
    validation_count: int,
    embedding_text: str,
):
    pool = ensure_pool()
    doc_id = f"{sanitize_agent_name(agent)}-{int(time.time())}-{os.urandom(4).hex()}"
    expires_at = expires_at_for_layer(layer)
    await pool.execute(
        """
        INSERT INTO memories (
            id, agent, content, embedding, type, layer, importance, confidence, scope,
            tags, source, metadata, source_ids, validation_count, expires_at, created_at
        ) VALUES (
            $1, $2, $3, $4::vector, $5, $6, $7, $8, $9,
            $10, $11, $12::jsonb, $13, $14, $15, $16
        )
        """,
        doc_id,
        sanitize_agent_name(agent),
        content,
        embedding_text,
        memory_type,
        layer,
        float(importance),
        float(confidence),
        scope,
        tags,
        source,
        json.dumps(metadata),
        source_ids,
        validation_count,
        expires_at,
        utcnow(),
    )
    schedule_audit_log(sanitize_agent_name(agent), "store", doc_id, sanitize_agent_name(agent), content)
    return doc_id


async def iter_agent_entries(agent: str):
    pool = ensure_pool()
    return await pool.fetch(
        """
        SELECT id, agent, content, type, layer, importance, confidence, scope, tags, source,
               metadata, superseded_by, supersede_reason, valid_until, source_ids,
               validation_count, created_at, expires_at, access_count, last_accessed,
               embedding
        FROM memories
        WHERE agent = $1
        ORDER BY created_at DESC
        """,
        sanitize_agent_name(agent),
    )


async def prune_expired_memories() -> dict:
    pool = ensure_pool()
    deleted_rows = await pool.fetch(
        """
        WITH deleted AS (
            DELETE FROM memories
            WHERE expires_at IS NOT NULL AND expires_at < NOW()
            RETURNING id, agent, content
        )
        SELECT id, agent, content FROM deleted
        """
    )
    for row in deleted_rows:
        schedule_audit_log(row["agent"], "delete", row["id"], row["agent"], row["content"])
    remaining_row = await pool.fetchrow("SELECT COUNT(*) AS remaining FROM memories")
    return {"pruned": len(deleted_rows), "remaining": int(remaining_row["remaining"] or 0)}


async def consolidate_agent_memories(agent: str) -> dict:
    rows = await iter_agent_entries(agent)
    if not rows:
        return {"agent": agent, "consolidated": 0, "remaining": 0}

    by_type = {}
    for row in rows:
        by_type.setdefault(row["type"], []).append(row)

    consolidated = 0
    pool = ensure_pool()
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

        id_to_idx = {item["id"]: idx for idx, item in enumerate(items)}
        for idx, row in enumerate(items):
            results = await pool.fetch(
                """
                SELECT id, embedding <=> $3::vector AS distance
                FROM memories
                WHERE agent = $1 AND type = $2
                ORDER BY embedding <=> $3::vector
                LIMIT $4
                """,
                sanitize_agent_name(agent),
                mem_type,
                row["embedding"],
                len(items),
            )
            for match in results:
                match_id = match["id"]
                distance = float(match["distance"] or 1.0)
                if match_id == row["id"] or match_id not in id_to_idx:
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
            merged_content = merge_cluster_documents([item["content"] for item in cluster_items])
            latest_item = max(cluster_items, key=lambda item: item["created_at"] or utcnow())
            latest_meta = safe_metadata(latest_item["metadata"])
            latest_meta["timestamp"] = format_timestamp(latest_item["created_at"])
            latest_meta["consolidated_from"] = len(cluster_items)
            tag_values = []
            for item in cluster_items:
                tags = (item["tags"] or "")
                if tags:
                    tag_values.extend(tag.strip() for tag in tags.split(",") if tag.strip())
            latest_meta["tags"] = ",".join(dict.fromkeys(tag_values))
            merged_embedding = (await embed([merged_content]))[0]
            new_id = f"{sanitize_agent_name(agent)}-{int(time.time())}-consolidated-{os.urandom(4).hex()}"
            expires_at = expires_at_for_layer(latest_item["layer"])
            async with pool.acquire() as conn:
                async with conn.transaction():
                    await conn.execute("DELETE FROM memories WHERE id = ANY($1::text[])", [item["id"] for item in cluster_items])
                    await conn.execute(
                        """
                        INSERT INTO memories (
                            id, agent, content, embedding, type, layer, importance, confidence, scope,
                            tags, source, metadata, source_ids, validation_count, expires_at, created_at
                        ) VALUES (
                            $1, $2, $3, $4::vector, $5, $6, $7, $8, $9,
                            $10, $11, $12::jsonb, $13, $14, $15, $16
                        )
                        """,
                        new_id,
                        sanitize_agent_name(agent),
                        merged_content,
                        vector_literal(merged_embedding),
                        latest_item["type"],
                        latest_item["layer"],
                        float(latest_item["importance"] or 0.5),
                        float(latest_item["confidence"] or 0.5),
                        latest_item["scope"] or "session",
                        latest_meta["tags"],
                        latest_item["source"] or "agent",
                        json.dumps(latest_meta),
                        latest_item["source_ids"] or "[]",
                        int(latest_item["validation_count"] or 0),
                        expires_at,
                        utcnow(),
                    )
            for item in cluster_items:
                schedule_audit_log(item["agent"], "delete", item["id"], item["agent"], item["content"])
            schedule_audit_log(sanitize_agent_name(agent), "store", new_id, sanitize_agent_name(agent), merged_content)
            consolidated += 1

    remaining_row = await pool.fetchrow("SELECT COUNT(*) AS remaining FROM memories WHERE agent = $1", sanitize_agent_name(agent))
    return {"agent": agent, "consolidated": consolidated, "remaining": int(remaining_row["remaining"] or 0)}


def compress_one_sync(content: str) -> Optional[str]:
    if not cognition_enabled():
        return None
    payload = {
        "model": "qwen",
        "temperature": 0.1,
        "messages": [
            {
                "role": "system",
                "content": "Compress this agent work session log into a concise narrative summary. Focus on: what task was attempted, which repos/branches/files were touched, what tools succeeded or failed, what the final status was. Write in past tense. Keep under 150 words. Output only the summary, no preamble.",
            },
            {"role": "user", "content": content},
        ],
    }
    try:
        data = json.dumps(payload).encode("utf-8")
        req = urllib.request.Request(
            f"{COGNITION_INFERENCE_URL.rstrip('/')}/v1/chat/completions",
            data=data,
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        with urllib.request.urlopen(req, timeout=30) as resp:
            result = json.loads(resp.read().decode("utf-8"))
        choices = result.get("choices", [])
        if not choices:
            return None
        text = choices[0].get("message", {}).get("content", "").strip()
        return text if text else None
    except Exception as e:
        log("warn", "session state compression failed", {"error": str(e)})
        return None


async def compress_session_states_async():
    pool = ensure_pool()
    rows = await pool.fetch(
        """
        SELECT id, agent, content, session_id, message_count, compressed, compressed_at, updated_at
        FROM session_states
        WHERE compressed = FALSE
        ORDER BY updated_at DESC
        LIMIT 5
        """
    )
    for row in rows:
        content = row["content"] or ""
        if len(content) < 200:
            continue
        compressed = compress_one_sync(content)
        if compressed is None:
            continue
        await pool.execute(
            """
            UPDATE session_states
            SET content = $2,
                compressed = TRUE,
                compressed_at = $3,
                updated_at = $3
            WHERE id = $1
            """,
            row["id"],
            compressed,
            utcnow(),
        )
        log(
            "info",
            "session state compressed",
            {
                "id": row["id"],
                "agent": row["agent"],
                "original_len": len(content),
                "compressed_len": len(compressed),
            },
        )


def compress_session_states():
    try:
        asyncio.run(compress_session_states_async())
    except Exception as e:
        log("error", "session state compression failed", {"error": str(e)})


async def list_session_state_entries(agent: str, limit: int = 20) -> list[dict]:
    pool = ensure_pool()
    rows = await pool.fetch(
        """
        SELECT id, content, agent, session_id, message_count, compressed, compressed_at, updated_at
        FROM session_states
        WHERE agent = $1
        ORDER BY updated_at DESC
        LIMIT $2
        """,
        sanitize_agent_name(agent),
        limit,
    )
    entries = []
    for row in rows:
        entries.append(
            {
                "id": row["id"],
                "document": row["content"],
                "metadata": {
                    "agent": row["agent"],
                    "session_id": row["session_id"] or "",
                    "updated_at": format_timestamp(row["updated_at"]),
                    "message_count": int(row["message_count"] or 0),
                    "compressed": "true" if row["compressed"] else "false",
                    "compressed_at": format_timestamp(row["compressed_at"]) if row["compressed_at"] else "",
                },
            }
        )
    return entries


async def migrate_from_chromadb_once(req: MigrateFromChromaRequest):
    base = f"http://{req.chroma_host}:{req.chroma_port}"
    migrated = 0
    async with httpx.AsyncClient(timeout=30.0) as client:
        collections_resp = await client.get(f"{base}/api/v1/collections")
        collections_resp.raise_for_status()
        collections = collections_resp.json()
        for collection in collections:
            collection_id = collection.get("id")
            collection_name = collection.get("name", "")
            if not collection_id:
                continue
            get_resp = await client.get(f"{base}/api/v1/collections/{collection_id}/get")
            get_resp.raise_for_status()
            payload = get_resp.json()
            ids = payload.get("ids", [])
            documents = payload.get("documents", [])
            metadatas = payload.get("metadatas", [])
            if not documents:
                continue
            embeddings = await embed([doc or "" for doc in documents])
            pool = ensure_pool()
            for doc_id, document, metadata, embedding_values in zip(ids, documents, metadatas, embeddings):
                metadata = dict(metadata or {})
                raw_agent = metadata.get("agent")
                if not raw_agent:
                    prefix = f"{COLLECTION}_"
                    if collection_name.startswith(prefix):
                        raw_agent = collection_name[len(prefix):]
                    else:
                        raw_agent = collection_name or "default"
                agent = sanitize_agent_name(raw_agent)
                layer = normalize_layer(metadata.get("layer"))
                importance = normalize_confidence(metadata.get("importance"), layer)
                confidence = normalize_confidence(metadata.get("confidence"), layer)
                source_ids = normalize_source_ids(metadata.get("source_ids"))
                validation_count = normalize_validation_count(metadata.get("validation_count"))
                metadata["agent"] = raw_agent
                if "timestamp" not in metadata:
                    metadata["timestamp"] = format_timestamp()
                result = await pool.execute(
                    """
                    INSERT INTO memories (
                        id, agent, content, embedding, type, layer, importance, confidence, scope,
                        tags, source, metadata, superseded_by, supersede_reason, valid_until,
                        source_ids, validation_count, created_at, expires_at
                    ) VALUES (
                        $1, $2, $3, $4::vector, $5, $6, $7, $8, $9,
                        $10, $11, $12::jsonb, $13, $14, $15, $16, $17, $18, $19
                    )
                    ON CONFLICT (id) DO NOTHING
                    """,
                    str(doc_id),
                    agent,
                    document or "",
                    vector_literal(embedding_values),
                    metadata.get("type", "general"),
                    layer,
                    float(metadata.get("importance", importance)),
                    float(metadata.get("confidence", confidence)),
                    metadata.get("scope", "session"),
                    metadata.get("tags", ""),
                    metadata.get("source", "agent"),
                    json.dumps(metadata),
                    metadata.get("superseded_by"),
                    metadata.get("supersede_reason"),
                    parse_iso_datetime(metadata.get("valid_until")),
                    source_ids,
                    validation_count,
                    parse_iso_datetime(metadata.get("timestamp")) or utcnow(),
                    expires_at_for_layer(layer),
                )
                if str(result).endswith("1"):
                    schedule_audit_log(agent, "store", str(doc_id), agent, document or "")
                migrated += 1
    return {"migrated": migrated}


def background_prune_loop():
    time.sleep(60)
    while True:
        try:
            if MAIN_LOOP is None:
                raise RuntimeError("main loop is not initialized")
            asyncio.run_coroutine_threadsafe(prune_expired_memories(), MAIN_LOOP).result()
            agents = asyncio.run_coroutine_threadsafe(fetch_agent_names(), MAIN_LOOP).result()
            for agent in agents:
                asyncio.run_coroutine_threadsafe(consolidate_agent_memories(agent), MAIN_LOOP).result()
        except Exception as e:
            log("error", "memory prune failed", {"error": str(e)})
        try:
            if MAIN_LOOP is None:
                raise RuntimeError("main loop is not initialized")
            asyncio.run_coroutine_threadsafe(compress_session_states_async(), MAIN_LOOP).result()
        except Exception as e:
            log("error", "session state compression failed", {"error": str(e)})
        time.sleep(6 * 60 * 60)


# --- Endpoints ---


@app.get("/.well-known/a2m.json")
async def a2m_capability_card():
    return {
        "a2m": "0.3",
        "name": "seidr",
        "description": "Cognitive memory system with fact extraction, contradiction detection, and cross-agent sharing",
        "transport": {"jsonrpc": "2.0", "endpoint": "/a2m", "streaming": ["sse"], "content_type": "application/json"},
        "auth": {"schemes": ["bearer", "none"], "agent_identity_binding": True},
        "methods": ["memory/store", "memory/query", "memory/validate", "memory/reflect", "memory/lifecycle", "memory/status"],
        "capabilities": {
            "store": {"batch": True, "partial_success": True},
            "query": {"semantic": True, "temporal": True, "multi_collection": True, "importance_ranking": True, "pagination": True},
            "validate": True,
            "reflect": True,
            "lifecycle": {"ttl": True, "prune": True, "delete": True, "expire": True, "consolidate": True, "promote": True},
            "cognitive": {"fact_extraction": True, "importance_scoring": True, "contradiction_detection": True, "relationship_extraction": True},
            "sharing": {"scoped_collections": True, "auto_promotion": True, "write_tier_enforcement": True},
        },
        "limits": {"max_batch_size": 100, "max_content_length": 100000, "max_query_results": 100, "default_timeout_ms": 3000},
        "constraints": {
            "supported_types": ["general", "fact", "lesson", "observation", "failure", "recovery", "workflow", "soul_candidate"],
            "embedding_dimensions": 384,
        },
    }


async def compute_memory_health(agent: str) -> dict:
    agent_name = sanitize_agent_name(agent)
    pool = ensure_pool()
    rows = await pool.fetch(
        """
        SELECT id, agent, content, type, layer, importance, confidence, scope, tags, source,
               metadata, superseded_by, supersede_reason, valid_until, source_ids,
               validation_count, created_at, expires_at, access_count, last_accessed, embedding
        FROM memories
        WHERE agent = $1
        ORDER BY created_at DESC
        """,
        agent_name,
    )
    total_count = len(rows)
    if total_count == 0:
        return {
            "score": 1.0,
            "staleness_ratio": 0.0,
            "contradiction_density": 0.0,
            "confidence_distribution": {"high": 1.0, "medium": 0.0, "low": 0.0},
            "dedup_pressure": 0.0,
            "recommendations": [],
        }

    now = utcnow()
    stale_count = 0
    contradicted_count = 0
    confidence_high = 0
    confidence_medium = 0
    confidence_low = 0

    for row in rows:
        meta = row_to_metadata(row)
        mem_type = str(meta.get("type") or row["type"] or "general").strip().lower()
        ttl_days = memory_ttl_days_for_type(mem_type)
        if ttl_days is not None and row["created_at"] is not None:
            age_days = (now - row["created_at"]).total_seconds() / 86400.0
            if age_days > ttl_days:
                stale_count += 1

        contradiction_count = 0
        try:
            contradiction_count = int(meta.get("contradiction_count", 0) or 0)
        except Exception:
            contradiction_count = 0
        if contradiction_count > 0 or row["superseded_by"]:
            contradicted_count += 1

        confidence = normalize_confidence(meta.get("confidence"), normalize_layer(meta.get("layer")))
        if confidence > 0.7:
            confidence_high += 1
        elif confidence >= 0.4:
            confidence_medium += 1
        else:
            confidence_low += 1

    recent_rows = rows[:100]
    checked_count = 0
    near_duplicate_count = 0
    dedup_tasks = []
    for row in recent_rows:
        if not row["embedding"]:
            continue
        checked_count += 1
        dedup_tasks.append(
            pool.fetchrow(
                """
                SELECT 1 - (embedding <=> $2::vector) AS similarity
                FROM memories
                WHERE agent = $1 AND id <> $3
                ORDER BY embedding <=> $2::vector
                LIMIT 1
                """,
                agent_name,
                row["embedding"],
                row["id"],
            )
        )

    if dedup_tasks:
        dedup_results = await asyncio.gather(*dedup_tasks, return_exceptions=True)
        for result in dedup_results:
            if isinstance(result, Exception) or result is None:
                continue
            if float(result["similarity"] or 0.0) > 0.85:
                near_duplicate_count += 1

    staleness_ratio = stale_count / total_count
    contradiction_density = contradicted_count / total_count
    confidence_distribution = {
        "high": confidence_high / total_count,
        "medium": confidence_medium / total_count,
        "low": confidence_low / total_count,
    }
    dedup_pressure = (near_duplicate_count / checked_count) if checked_count > 0 else 0.0
    score = 1.0 - (
        0.3 * staleness_ratio
        + 0.25 * contradiction_density
        + 0.25 * (1.0 - confidence_distribution["high"])
        + 0.2 * dedup_pressure
    )
    score = max(0.0, min(1.0, score))

    recommendations = []
    if staleness_ratio > 0.3:
        recommendations.append("Consider running prune to remove expired memories")
    if contradiction_density > 0.1:
        recommendations.append("High contradiction rate - review contradicted memories")
    if dedup_pressure > 0.2:
        recommendations.append("Consider running consolidate to merge similar memories")
    if confidence_distribution["low"] > 0.2:
        recommendations.append("Many low-confidence memories - consider validation")

    return {
        "score": round(score, 4),
        "staleness_ratio": round(staleness_ratio, 4),
        "contradiction_density": round(contradiction_density, 4),
        "confidence_distribution": {
            "high": round(confidence_distribution["high"], 4),
            "medium": round(confidence_distribution["medium"], 4),
            "low": round(confidence_distribution["low"], 4),
        },
        "dedup_pressure": round(dedup_pressure, 4),
        "recommendations": recommendations,
    }


@app.get("/health")
async def health():
    return await health_data()


@app.get("/health/agent/{agent_name}")
async def agent_memory_health(agent_name: str):
    agent = sanitize_agent_name(agent_name)
    pool = ensure_pool()
    count_row = await pool.fetchrow("SELECT COUNT(*) AS count FROM memories WHERE agent = $1", agent)
    total_memories = int(count_row["count"] or 0)
    try:
        health = await asyncio.wait_for(compute_memory_health(agent), timeout=5.0)
        recommendations = health.pop("recommendations", [])
    except Exception as e:
        log("warn", "memory health computation failed", {"agent": agent, "error": str(e)})
        health = None
        recommendations = []
    return {
        "agent": agent_name,
        "total_memories": total_memories,
        "health": health,
        "recommendations": recommendations,
    }


@app.get("/collections")
async def list_collections():
    try:
        return {"collections": await fetch_agent_names()}
    except Exception as e:
        return {"collections": [], "error": str(e)}


@app.get("/a2m/health")
async def a2m_health():
    return {"healthy": True, "a2m_version": "0.3", "provider": "seidr"}

@app.get("/audit/verify")
async def verify_audit_log():
    pool = ensure_pool()
    rows = await pool.fetch(
        """
        SELECT id, timestamp, agent, operation, memory_id, collection, content_hash, prev_hash, chain_hash
        FROM audit_log
        ORDER BY id ASC
        """
    )
    prev_hash = "genesis"
    for row in rows:
        timestamp_str = format_timestamp(row["timestamp"]) if row["timestamp"] else ""
        expected = compute_chain_hash_from_content_hash(
            prev_hash,
            row["agent"],
            row["operation"],
            row["memory_id"],
            row["content_hash"],
            timestamp_str,
        )
        if expected != row["chain_hash"]:
            return {"valid": False, "break_at": row["id"], "entries": len(rows)}
        prev_hash = row["chain_hash"]
    return {"valid": True, "entries": len(rows)}


@app.get("/audit/log")
async def list_audit_log(agent: Optional[str] = None, limit: int = 100):
    pool = ensure_pool()
    limit = max(1, min(int(limit), 500))
    if agent:
        rows = await pool.fetch(
            """
            SELECT id, timestamp, agent, operation, memory_id, collection, content_hash, prev_hash, chain_hash
            FROM audit_log
            WHERE agent = $1
            ORDER BY id DESC
            LIMIT $2
            """,
            sanitize_agent_name(agent),
            limit,
        )
    else:
        rows = await pool.fetch(
            """
            SELECT id, timestamp, agent, operation, memory_id, collection, content_hash, prev_hash, chain_hash
            FROM audit_log
            ORDER BY id DESC
            LIMIT $1
            """,
            limit,
        )
    entries = []
    for row in rows:
        entries.append(
            {
                "id": row["id"],
                "timestamp": format_timestamp(row["timestamp"]) if row["timestamp"] else "",
                "agent": row["agent"],
                "operation": row["operation"],
                "memory_id": row["memory_id"],
                "collection": row["collection"],
                "content_hash": row["content_hash"],
                "prev_hash": row["prev_hash"],
                "chain_hash": row["chain_hash"],
            }
        )
    return {"entries": entries, "count": len(entries), "agent": sanitize_agent_name(agent) if agent else None, "limit": limit}


@app.get("/cognitive/status")
async def cognitive_status():
    return await cognitive_status_data()


@app.get("/relationships")
async def get_relationships(agent: str, entity: Optional[str] = None, limit: int = 20):
    pool = ensure_pool()
    agent_name = sanitize_agent_name(agent)
    limit = max(1, min(int(limit), 200))
    if entity:
        rows = await pool.fetch(
            """
            SELECT id, agent, entity_a, entity_b, relation_type, strength, evidence_count, last_observed, metadata
            FROM relationships
            WHERE agent = $1 AND (entity_a = $2 OR entity_b = $2)
            ORDER BY strength DESC, evidence_count DESC, last_observed DESC
            LIMIT $3
            """,
            agent_name,
            entity,
            limit,
        )
    else:
        rows = await pool.fetch(
            """
            SELECT id, agent, entity_a, entity_b, relation_type, strength, evidence_count, last_observed, metadata
            FROM relationships
            WHERE agent = $1
            ORDER BY strength DESC, evidence_count DESC, last_observed DESC
            LIMIT $2
            """,
            agent_name,
            limit,
        )
    relationships = []
    for row in rows:
        relationships.append(
            {
                "id": row["id"],
                "agent": row["agent"],
                "entity_a": row["entity_a"],
                "entity_b": row["entity_b"],
                "relation_type": row["relation_type"],
                "strength": float(row["strength"] or 0.0),
                "evidence_count": int(row["evidence_count"] or 0),
                "last_observed": format_timestamp(row["last_observed"]) if row["last_observed"] else "",
                "metadata": safe_metadata(row["metadata"]),
            }
        )
    return {"relationships": relationships, "count": len(relationships)}


@app.get("/co-access")
async def co_access(agent: str, memory_id: str, limit: int = 10):
    scores = await get_co_access_scores(agent, [memory_id], limit=max(1, min(int(limit), 100)))
    items = [{"memory_id": memory_id, "score": round(float(score), 4)} for memory_id, score in scores.items()]
    items.sort(key=lambda item: item["score"], reverse=True)
    return {"agent": sanitize_agent_name(agent), "memory_id": memory_id, "associations": items[:limit], "count": len(items[:limit])}


@app.post("/a2m")
async def a2m_dispatch(request: Request):
    version = request.headers.get("A2M-Version")
    if version is not None and parse_a2m_major_version(version) != 0:
        return jsonrpc_error(-32050, "Unsupported A2M version")

    request_id = None
    try:
        raw_body = await request.body()
        if not raw_body:
            return jsonrpc_error(-32700, "Parse error")
        payload = json.loads(raw_body)
    except Exception:
        return jsonrpc_error(-32700, "Parse error")

    if not isinstance(payload, dict):
        return jsonrpc_error(-32600, "Invalid Request")

    request_id = payload.get("id")
    if payload.get("jsonrpc") != "2.0" or not isinstance(payload.get("method"), str):
        return jsonrpc_error(-32600, "Invalid Request", request_id)

    method = payload["method"]
    params = payload.get("params") or {}
    if not isinstance(params, dict):
        return jsonrpc_error(-32600, "Invalid Request", request_id)

    try:
        if method == "memory/store":
            store_params = A2MStoreParams.model_validate(params)
            if len(store_params.memories) > 100:
                return jsonrpc_error(-32600, "Invalid Request", request_id)
            stored_results = []
            for item in store_params.memories:
                metadata = dict(item.metadata or {})
                if item.scope is not None:
                    metadata["scope"] = item.scope
                if item.provenance is not None:
                    metadata["provenance"] = item.provenance
                tags = []
                tags.extend(coerce_tag_list(metadata.pop("tags", None)))
                tags.extend(item.tags or [])
                tags = coerce_tag_list(tags)
                remember_req = RememberRequest(
                    agent=item.agent,
                    content=item.content,
                    type=item.type,
                    shared=item.shared,
                    metadata=metadata or None,
                    tags=tags,
                )
                result = await remember_memory_data(remember_req)
                memory = result.get("memory")
                if memory is None and result.get("id"):
                    stored_row = await find_memory_record(result["id"])
                    memory = serialize_memory_record(stored_row) if stored_row else None
                if memory is None and result.get("similar_to"):
                    similar_row = await find_memory_record(result["similar_to"])
                    memory = serialize_memory_record(similar_row) if similar_row else None
                stored_results.append(
                    {
                        "id": result.get("id") or result.get("similar_to"),
                        "status": "stored" if result.get("stored") else "deduplicated",
                        "memory": memory,
                        "cognitive": {
                            "status": result.get("cognitive_status", "processed"),
                            "facts_extracted": result.get("facts_extracted", 0),
                            "contradictions_found": result.get("contradictions_found", 0),
                            "auto_promoted": bool(result.get("auto_promoted", 0)),
                        },
                    }
                )
            return jsonrpc_result({"stored": stored_results, "failed": []}, request_id)

        if method == "memory/query":
            query_params = A2MQueryParams.model_validate(params)
            limit = max(1, min(100, int(query_params.limit or 10)))
            fetch_limit = max(1, min(100, max(limit * 3, limit)))
            query_req = QueryRequest(
                query=query_params.query,
                agent=query_params.agent,
                limit=fetch_limit,
                collections=query_params.collections,
                importance_weight=query_params.importance_weight,
            )
            if query_params.filters and query_params.filters.types:
                types = [str(item).strip() for item in query_params.filters.types if str(item).strip()]
                if len(types) == 1:
                    query_req.type = types[0]
            result = await query_memory_data(query_req)
            threshold = query_params.similarity_threshold
            filtered = []
            for item in result["results"]:
                if threshold is not None and float(item.get("similarity", 0.0)) < float(threshold):
                    continue
                memory = serialize_query_memory(item)
                if query_params.filters:
                    meta = item.get("metadata", {}) or {}
                    if query_params.filters.min_importance is not None:
                        importance = float(meta.get("importance", 0.0) or 0.0)
                        if importance < float(query_params.filters.min_importance):
                            continue
                    if query_params.filters.types:
                        allowed_types = {str(t).strip() for t in query_params.filters.types if str(t).strip()}
                        if memory["type"] not in allowed_types:
                            continue
                    if query_params.filters.tags_any:
                        memory_tags = set(coerce_tag_list(memory.get("tags", [])))
                        requested_tags = {str(tag).strip() for tag in query_params.filters.tags_any if str(tag).strip()}
                        if requested_tags and memory_tags.isdisjoint(requested_tags):
                            continue
                score_components = build_score_components(item)
                filtered.append(
                    {
                        "memory": memory,
                        "score": round(float(item.get("final_score", item.get("similarity", 0.0)) or 0.0), 4),
                        "score_components": score_components,
                    }
                )
            filtered.sort(key=lambda item: item["score_components"]["similarity"], reverse=True)
            return jsonrpc_result({"results": filtered[:limit]}, request_id)

        if method == "memory/validate":
            validate_params = A2MValidateParams.model_validate(params)
            validations = []
            failed = []
            for index, item in enumerate(validate_params.validations):
                result = await validate_memory_data(
                    ValidateRequest(memory_id=item.memory_id, outcome=item.outcome, context=item.context)
                )
                if result.get("error"):
                    failed.append(
                        {
                            "index": index,
                            "memory_id": item.memory_id,
                            "error": {"code": -32046, "message": "Memory not found"},
                        }
                    )
                    continue
                validations.append(
                    {
                        "memory_id": item.memory_id,
                        "outcome": item.outcome,
                        "updated": result.get("updated", False),
                        "metadata": result.get("metadata"),
                        "memory": result.get("memory"),
                        "validation_count": result.get("validation_count", 0),
                        "contradiction_count": result.get("contradiction_count", 0),
                        "irrelevant_count": result.get("irrelevant_count", 0),
                    }
                )
            return jsonrpc_result({"validations": validations, "failed": failed}, request_id)

        if method == "memory/reflect":
            reflect_params = A2MReflectParams.model_validate(params)
            result = await reflect_data(ReflectRequest(agent=reflect_params.agent, collections=reflect_params.collections))
            return jsonrpc_result(
                {
                    "clusters": result.get("clusters", []),
                    "total_memories_analyzed": result.get("total_memories_analyzed", 0),
                    "agent": result.get("agent", reflect_params.agent),
                },
                request_id,
            )

        if method == "memory/lifecycle":
            lifecycle = A2MLifecycleParams.model_validate(params)
            action = (lifecycle.action or "").strip().lower()
            memory_ids = lifecycle.memory_ids or ([lifecycle.memory_id] if lifecycle.memory_id else [])
            if action == "prune":
                return jsonrpc_result(await prune_memory_data(), request_id)
            if action == "delete":
                if not memory_ids:
                    return jsonrpc_error(-32600, "Invalid Request", request_id)
                deleted = []
                for memory_id in memory_ids:
                    try:
                        deleted.append(await delete_memory_data(memory_id))
                    except HTTPException as exc:
                        deleted.append({"memory_id": memory_id, "error": exc.detail or "error"})
                return jsonrpc_result({"deleted": deleted}, request_id)
            if action == "expire":
                if not memory_ids or not lifecycle.expires_at:
                    return jsonrpc_error(-32600, "Invalid Request", request_id)
                expired = []
                for memory_id in memory_ids:
                    try:
                        expired.append(await update_memory_data(memory_id, UpdateMemoryRequest(expires_at=lifecycle.expires_at)))
                    except HTTPException as exc:
                        expired.append({"memory_id": memory_id, "error": exc.detail or "error"})
                return jsonrpc_result({"expired": expired}, request_id)
            if action == "consolidate":
                consolidate_agent = lifecycle.agent or lifecycle.collection or lifecycle.target_collection
                if not consolidate_agent:
                    return jsonrpc_error(-32600, "Invalid Request", request_id)
                return jsonrpc_result(await consolidate_memory_data(ConsolidateRequest(agent=consolidate_agent)), request_id)
            if action == "promote":
                if not memory_ids:
                    return jsonrpc_error(-32600, "Invalid Request", request_id)
                target_collection = lifecycle.collection or lifecycle.target_collection or lifecycle.agent
                if not target_collection:
                    return jsonrpc_error(-32600, "Invalid Request", request_id)
                promoted = []
                for memory_id in memory_ids:
                    try:
                        promoted.append(await update_memory_data(memory_id, UpdateMemoryRequest(collection=target_collection)))
                    except HTTPException as exc:
                        promoted.append({"memory_id": memory_id, "error": exc.detail or "error"})
                return jsonrpc_result({"promoted": promoted}, request_id)
            return jsonrpc_error(-32601, "Method not found", request_id)

        if method == "memory/status":
            health_result = await health_data()
            cognitive_result = await cognitive_status_data()
            uptime_seconds = max(0, int(time.time() - APP_START_TIME))
            cognitive = {
                "available": bool(cognitive_result.get("enabled", False)),
                "queue_depth": 0,
                "processed_total": int(cognitive_result.get("stats", {}).get("total_facts_extracted", 0) or 0),
                "errors_total": 0,
                "last_processed_at": None,
            }
            return jsonrpc_result(
                {
                    "healthy": health_result.get("status") == "ok",
                    "memory_count": health_result.get("memories", 0),
                    "collections": health_result.get("collections", []),
                    "cognitive": cognitive,
                    "uptime_seconds": uptime_seconds,
                },
                request_id,
            )

        return jsonrpc_error(-32601, "Method not found", request_id)
    except HTTPException as exc:
        return jsonrpc_error(-32600, "Invalid Request", request_id) if exc.status_code < 500 else jsonrpc_error(-32603, "Internal error", request_id)
    except Exception as exc:
        log("warn", "a2m dispatch failed", {"method": method, "error": str(exc)})
        return jsonrpc_error(-32603, "Internal error", request_id)


@app.post("/query")
async def query(req: QueryRequest):
    return await query_memory_data(req)


@app.post("/remember")
async def remember(req: RememberRequest):
    return await remember_memory_data(req)


@app.post("/session-state")
async def store_session_state(req: SessionStateRequest):
    agent = sanitize_agent_name(req.agent)
    pool = ensure_pool()
    doc_id = f"{agent}-{int(time.time())}"
    await pool.execute(
        """
        INSERT INTO session_states (id, agent, session_id, content, message_count, compressed, updated_at)
        VALUES ($1, $2, $3, $4, $5, FALSE, $6)
        """,
        doc_id,
        agent,
        req.session_id,
        req.content,
        int(req.message_count or 0),
        utcnow(),
    )
    entries = await list_session_state_entries(agent, limit=20)
    if len(entries) > 5:
        await pool.execute("DELETE FROM session_states WHERE id = ANY($1::text[])", [entry["id"] for entry in entries[5:]])
        entries = entries[:5]
    return {"stored": True, "agent": agent, "entries": len(entries)}


@app.get("/session-state")
async def get_session_state(agent: str):
    sanitized_name = sanitize_agent_name(agent)
    entries = await list_session_state_entries(sanitized_name, limit=20)
    if not entries:
        return {"agent": sanitized_name, "content": None, "entries_count": 0}
    selected = None
    for entry in entries:
        if entry["metadata"].get("compressed") == "true":
            selected = entry
            break
    if selected is None:
        selected = entries[0]
    meta = selected["metadata"]
    return {
        "agent": sanitized_name,
        "content": selected["document"],
        "session_id": meta.get("session_id", ""),
        "updated_at": meta.get("updated_at", ""),
        "compressed": meta.get("compressed") == "true",
        "entries_count": len(entries),
    }


@app.delete("/session-state")
async def delete_session_state(agent: str):
    sanitized_name = sanitize_agent_name(agent)
    entries = await list_session_state_entries(sanitized_name, limit=100)
    if entries:
        await ensure_pool().execute("DELETE FROM session_states WHERE id = ANY($1::text[])", [entry["id"] for entry in entries])
    return {"deleted": True, "agent": sanitized_name, "count": len(entries)}


@app.get("/memories")
async def list_memories(agent: str = None, limit: int = 100, type: str = None):
    rows = await list_memories_for_agent(agent, limit=limit, memory_type=type)
    memories = []
    for row in rows:
        meta = row_to_metadata(row)
        memories.append(
            {
                "id": row["id"],
                "content": row["content"],
                "metadata": meta,
                "agent": meta.get("agent", ""),
                "source": meta.get("source", ""),
                "timestamp": meta.get("timestamp", ""),
                "type": meta.get("type", "general"),
                "tags": meta.get("tags", "").split(",") if meta.get("tags") else [],
            }
        )
    memories.sort(key=lambda x: x["timestamp"], reverse=True)
    memories = memories[:limit]
    return {"memories": memories, "count": len(memories)}


@app.get("/shared/memories")
async def list_shared_memories(limit: int = 100, offset: int = 0):
    rows = await list_memories_for_agent("warband_shared", limit=limit, offset=offset)
    memories = []
    for row in rows:
        meta = row_to_metadata(row)
        memories.append(
            {
                "id": row["id"],
                "content": row["content"],
                "metadata": meta,
                "agent": meta.get("agent", ""),
                "source": meta.get("source", ""),
                "timestamp": meta.get("timestamp", ""),
                "type": meta.get("type", "general"),
                "tags": meta.get("tags", "").split(",") if meta.get("tags") else [],
            }
        )
    return {"memories": memories, "count": len(memories), "limit": limit, "offset": offset}


@app.patch("/memories/{memory_id}")
async def update_memory(memory_id: str, req: UpdateMemoryRequest):
    return await update_memory_data(memory_id, req)


@app.post("/prune")
async def prune():
    return await prune_memory_data()


@app.post("/validate")
async def validate_memory(req: ValidateRequest):
    return await validate_memory_data(req)


@app.post("/reflect")
async def reflect(req: ReflectRequest):
    return await reflect_data(req)


@app.post("/migrate")
async def migrate(req: MigrateRequest):
    return {"migrated": 0, "deleted": 0, "source": COLLECTION}


@app.post("/consolidate")
async def consolidate(req: ConsolidateRequest):
    return await consolidate_memory_data(req)


@app.delete("/memories/{memory_id}")
async def delete_memory(memory_id: str):
    return await delete_memory_data(memory_id)


@app.post("/ingest")
async def ingest(req: IngestRequest):
    files = glob.glob(os.path.join(req.path, "**/*.md"), recursive=True)
    total = 0
    for filepath in files:
        with open(filepath, "r") as f:
            content = f.read()
        chunks = chunk_text(content, chunk_size=1200, overlap=200)
        embeddings = await embed(chunks)
        for i, (chunk, embedding_values) in enumerate(zip(chunks, embeddings)):
            doc_id = f"doc-{os.path.basename(filepath)}-{i}"
            metadata = {
                "agent": "ingest",
                "source": filepath,
                "timestamp": format_timestamp(),
                "tags": "documentation",
                "type": "general",
                "layer": "experience",
                "confidence": 0.5,
                "source_ids": "[]",
                "validation_count": 0,
            }
            await ensure_pool().execute(
                """
                INSERT INTO memories (
                    id, agent, content, embedding, type, layer, importance, confidence, scope,
                    tags, source, metadata, source_ids, validation_count, expires_at, created_at
                ) VALUES (
                    $1, $2, $3, $4::vector, $5, $6, $7, $8, $9,
                    $10, $11, $12::jsonb, $13, $14, $15, $16
                )
                ON CONFLICT (id) DO UPDATE SET
                    content = EXCLUDED.content,
                    embedding = EXCLUDED.embedding,
                    metadata = EXCLUDED.metadata,
                    source = EXCLUDED.source,
                    tags = EXCLUDED.tags
                """,
                doc_id,
                sanitize_agent_name("ingest"),
                chunk,
                vector_literal(embedding_values),
                "general",
                "experience",
                0.5,
                0.5,
                "session",
                "documentation",
                filepath,
                json.dumps(metadata),
                "[]",
                0,
                expires_at_for_layer("experience"),
                utcnow(),
            )
            schedule_audit_log("ingest", "store", doc_id, "ingest", chunk)
            total += 1
    log("info", "ingestion complete", {"files": len(files), "chunks": total})
    return {"status": "ok", "files": len(files), "chunks": total}


@app.post("/migrate-from-chromadb")
async def migrate_from_chromadb(req: MigrateFromChromaRequest):
    return await migrate_from_chromadb_once(req)


if __name__ == "__main__":
    import uvicorn

    async def init_runtime():
        global DB_POOL
        DB_POOL = await asyncpg.create_pool(DATABASE_URL, min_size=1, max_size=10)
        await init_db()

    port = int(os.getenv("PORT", "8082"))
    log("info", "seidr starting", {"port": port, "database": DATABASE_URL, "embed_url": EMBED_URL or ""})

    loop = asyncio.new_event_loop()
    asyncio.set_event_loop(loop)
    MAIN_LOOP = loop
    loop.run_until_complete(init_runtime())

    if not EMBED_URL:
        import torch  # noqa: F401
        from sentence_transformers import SentenceTransformer

        EMBED_MODEL = SentenceTransformer("all-MiniLM-L6-v2", device="cpu")
        _ = EMBED_MODEL.encode(["warmup"], normalize_embeddings=True).tolist()
        log("info", "embedding initialized", {"model": EMBEDDING_MODEL, "device": EMBEDDING_DEVICE, "backend": "local"})
    else:
        loop.run_until_complete(embed(["warmup"]))
        log("info", "embedding initialized", {"model": EMBEDDING_MODEL, "device": EMBEDDING_DEVICE, "backend": "remote"})

    prune_thread = threading.Thread(target=background_prune_loop, daemon=True)
    prune_thread.start()
    log("info", "background prune thread started")

    config = uvicorn.Config(app, host="0.0.0.0", port=port)
    server = uvicorn.Server(config)
    loop.run_until_complete(server.serve())
