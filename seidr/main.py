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
from fastapi import FastAPI
from pydantic import BaseModel


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
]


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


class SessionStateRequest(BaseModel):
    agent: str
    content: str
    session_id: str = ""
    message_count: int = 0


class MigrateFromChromaRequest(BaseModel):
    chroma_host: str = "chromadb.valhalla.svc"
    chroma_port: int = 8000


# --- App ---
app = FastAPI(title="Seidr", description="Valhalla Knowledge Service")


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


def query_hash(agent: str, query: str) -> str:
    return hashlib.sha256(f"{agent}:{query}".encode("utf-8")).hexdigest()


def ensure_pool():
    if DB_POOL is None:
        raise RuntimeError("database pool is not initialized")
    return DB_POOL


def row_to_metadata(row) -> dict:
    meta = dict(row["metadata"] or {})
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
    if row["valid_until"] is not None:
        meta["valid_until"] = format_timestamp(row["valid_until"])
    if row["superseded_by"]:
        meta["superseded_by"] = row["superseded_by"]
    if row["supersede_reason"]:
        meta["supersede_reason"] = row["supersede_reason"]
    if "timestamp" not in meta:
        meta["timestamp"] = format_timestamp(row["created_at"])
    return meta


def memory_row_to_result(row, vector_score: float = 0.0, keyword_score: float = 0.0, similarity: float = 0.0) -> dict:
    return {
        "id": row["id"],
        "content": row["content"],
        "metadata": row_to_metadata(row),
        "similarity": round(float(similarity), 4),
        "vector_score": round(float(vector_score), 4),
        "bm25_score": round(float(keyword_score), 4),
    }


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


async def list_memories_for_agent(agent: Optional[str], limit: int = 100, memory_type: Optional[str] = None):
    pool = ensure_pool()
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
    sql += f" ORDER BY created_at DESC LIMIT ${idx}"
    params.append(limit)
    return await pool.fetch(sql, *params)


async def run_hybrid_search(query: str, limit: int = 5, agent: Optional[str] = None, where: Optional[dict] = None):
    if not query.strip():
        return []
    pool = ensure_pool()
    embeddings = await embed([query])
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
        SELECT id, content, type, layer, importance, confidence, scope, tags, source, metadata,
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
    merged = []
    for row in vector_rows:
        vector_score = max(0.0, float(row["vector_score"] or 0.0))
        keyword_score = max(0.0, keyword_score_by_id.get(row["id"], 0.0))
        combined = 0.6 * vector_score + 0.4 * keyword_score
        merged.append(memory_row_to_result(row, vector_score=vector_score, keyword_score=keyword_score, similarity=combined))
    merged.sort(key=lambda item: item["similarity"], reverse=True)
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
    pruned_row = await pool.fetchrow(
        """
        WITH deleted AS (
            DELETE FROM memories
            WHERE expires_at IS NOT NULL AND expires_at < NOW()
            RETURNING id
        )
        SELECT COUNT(*) AS pruned FROM deleted
        """
    )
    remaining_row = await pool.fetchrow("SELECT COUNT(*) AS remaining FROM memories")
    return {"pruned": int(pruned_row["pruned"] or 0), "remaining": int(remaining_row["remaining"] or 0)}


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
            latest_meta = dict(latest_item["metadata"] or {})
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
                await pool.execute(
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


@app.get("/health")
async def health():
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


@app.get("/collections")
async def list_collections():
    try:
        return {"collections": await fetch_agent_names()}
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
            importance = normalize_confidence(meta.get("importance"), normalize_layer(meta.get("layer")))
            similarity_score = float(item.get("similarity", 0.0))
            final_score = (1.0 - weight) * similarity_score + weight * importance
            item["final_score"] = round(final_score, 4)
        results.sort(key=lambda x: x.get("final_score", 0.0), reverse=True)
        results = results[: req.limit]
    if req.agent:
        asyncio.create_task(log_memory_access(req.agent, req.query, [item["id"] for item in results if item.get("id")]))
    return {"results": results, "count": len(results)}


@app.post("/remember")
async def remember(req: RememberRequest):
    memory_type = resolve_memory_type(req)
    source_layer = normalize_layer(req.layer)
    agent = sanitize_agent_name(req.agent)

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

    async def store_one(content: str, metadata: dict, check_contradiction: bool):
        embedding_values = (await embed([content]))[0]
        embedding_text = vector_literal(embedding_values)
        duplicate_rows = await fetch_similar_for_dedup(agent, memory_type, embedding_text, limit=3)
        for row in duplicate_rows:
            similarity = float(row["similarity"] or 0.0)
            existing_meta = dict(row["metadata"] or {})
            if existing_meta.get("type", "general") == memory_type and similarity > 0.92:
                log("info", "memory deduplicated", {"agent": req.agent, "similar_to": row["id"]})
                return None, row["id"]

        new_id = f"{agent}-{int(time.time())}-{os.urandom(4).hex()}"
        contradictions = []
        if check_contradiction:
            contradictions = await detect_fact_contradictions(agent, content, embedding_text)

        stored_id = await store_memory_row(
            agent=agent,
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
        if contradictions:
            await apply_contradictions(stored_id, contradictions)
        return stored_id, None

    if not cognition_enabled():
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
        embedding_values = (await embed([fact]))[0]
        embedding_text = vector_literal(embedding_values)
        importance_task = score_importance(fact)
        contradictions_task = detect_fact_contradictions(agent, fact, embedding_text)
        importance_scope, contradictions = await asyncio.gather(importance_task, contradictions_task)
        importance, scope = importance_scope
        fact_meta["importance"] = round(importance, 4)
        fact_meta["scope"] = scope
        fact_meta["layer"] = "experience" if force_experience_layer else normalize_layer(fact_meta.get("layer"))
        COGNITIVE_STATS["importance_total"] += float(importance)
        COGNITIVE_STATS["importance_count"] += 1

        duplicate_rows = await fetch_similar_for_dedup(agent, memory_type, embedding_text, limit=3)
        duplicate_id = None
        for row in duplicate_rows:
            similarity = float(row["similarity"] or 0.0)
            existing_meta = dict(row["metadata"] or {})
            if existing_meta.get("type", "general") == memory_type and similarity > 0.92:
                duplicate_id = row["id"]
                break
        if duplicate_id:
            log("info", "memory deduplicated", {"agent": req.agent, "similar_to": duplicate_id})
            duplicate_hits.append(duplicate_id)
            continue

        stored_id = await store_memory_row(
            agent=agent,
            content=fact,
            metadata=fact_meta,
            memory_type=memory_type,
            layer=normalize_layer(fact_meta.get("layer")),
            importance=float(fact_meta.get("importance", 0.5)),
            confidence=normalize_confidence(fact_meta.get("confidence"), normalize_layer(fact_meta.get("layer"))),
            scope=str(fact_meta.get("scope", "session")).strip().lower() or "session",
            source=str(fact_meta.get("source", req.source)),
            tags=str(fact_meta.get("tags", "")),
            source_ids=normalize_source_ids(fact_meta.get("source_ids")),
            validation_count=normalize_validation_count(fact_meta.get("validation_count")),
            embedding_text=embedding_text,
        )
        if contradictions:
            await apply_contradictions(stored_id, contradictions)
        stored_ids.append(stored_id)
        log("info", "memory stored", {"agent": req.agent, "id": stored_id, "tags": req.tags, "layer": fact_meta.get("layer")})

    if not stored_ids:
        return {"id": None, "stored": False, "reason": "duplicate", "similar_to": duplicate_hits[0] if duplicate_hits else None}
    return {"id": stored_ids[0], "ids": stored_ids, "stored": True, "facts_stored": len(stored_ids)}


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


@app.post("/prune")
async def prune():
    return await prune_expired_memories()


@app.post("/validate")
async def validate_memory(req: ValidateRequest):
    row = await find_memory_record(req.memory_id)
    if not row:
        return {"updated": False, "error": "memory not found"}
    meta = row_to_metadata(row)
    current_conf = normalize_confidence(meta.get("confidence"), normalize_layer(meta.get("layer")))
    current_validations = normalize_validation_count(meta.get("validation_count"))
    if req.outcome == "success":
        current_validations += 1
        current_conf = min(1.0, current_conf + 0.1)
    else:
        current_conf = max(0.0, current_conf - 0.2)
    meta["validation_count"] = current_validations
    meta["confidence"] = round(current_conf, 4)
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
    return {"updated": True, "memory_id": req.memory_id, "metadata": meta}


@app.post("/reflect")
async def reflect(req: ReflectRequest):
    agent = (req.agent or "").strip()
    if not agent:
        return {"agent": "", "clusters": [], "count": 0}
    pool = ensure_pool()
    cutoff = utcnow() - timedelta(days=14)
    experiences = await pool.fetch(
        """
        SELECT id, content, metadata, created_at, confidence, validation_count, embedding
        FROM memories
        WHERE agent = $1 AND layer = 'experience' AND created_at >= $2
        ORDER BY created_at DESC
        """,
        sanitize_agent_name(agent),
        cutoff,
    )
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

    id_to_idx = {row["id"]: idx for idx, row in enumerate(experiences)}
    for idx, row in enumerate(experiences):
        results = await pool.fetch(
            """
            SELECT id, 1 - (embedding <=> $2::vector) AS similarity
            FROM memories
            WHERE agent = $1 AND layer = 'experience'
            ORDER BY embedding <=> $2::vector
            LIMIT $3
            """,
            sanitize_agent_name(agent),
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
    return {"agent": agent, "clusters": clusters, "count": len(clusters)}


@app.post("/migrate")
async def migrate(req: MigrateRequest):
    return {"migrated": 0, "deleted": 0, "source": COLLECTION}


@app.post("/consolidate")
async def consolidate(req: ConsolidateRequest):
    return await consolidate_agent_memories(req.agent)


@app.delete("/memories/{memory_id}")
async def delete_memory(memory_id: str):
    await ensure_pool().execute("DELETE FROM memories WHERE id = $1", memory_id)
    return {"deleted": memory_id}


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
