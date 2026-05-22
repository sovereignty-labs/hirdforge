import os
from typing import Any, Optional

import httpx
from mcp.server.fastmcp import FastMCP
from mcp.server.sse import SseServerTransport
from starlette.responses import PlainTextResponse


SEIDR_URL = os.getenv("SEIDR_URL", "http://localhost:8082").rstrip("/")


def _seidr_endpoint(path: str) -> str:
    return f"{SEIDR_URL}{path}"


def _split_tags(tags: Optional[str]) -> list[str]:
    if not tags:
        return []
    return [tag.strip() for tag in tags.split(",") if tag.strip()]


async def _post_json(path: str, payload: dict[str, Any], timeout: float = 10.0) -> dict[str, Any]:
    async with httpx.AsyncClient(timeout=timeout) as client:
        response = await client.post(_seidr_endpoint(path), json=payload)
        response.raise_for_status()
        return response.json()


async def _get_json(path: str, timeout: float = 10.0) -> dict[str, Any]:
    async with httpx.AsyncClient(timeout=timeout) as client:
        response = await client.get(_seidr_endpoint(path))
        response.raise_for_status()
        return response.json()


async def _get_json_params(path: str, params: dict[str, Any], timeout: float = 10.0) -> dict[str, Any]:
    async with httpx.AsyncClient(timeout=timeout) as client:
        response = await client.get(_seidr_endpoint(path), params=params)
        response.raise_for_status()
        return response.json()


async def _delete_json(path: str, timeout: float = 10.0) -> dict[str, Any]:
    async with httpx.AsyncClient(timeout=timeout) as client:
        response = await client.delete(_seidr_endpoint(path))
        response.raise_for_status()
        return response.json()


def _build_mcp_server() -> FastMCP:
    mcp = FastMCP("seidr")

    @mcp.tool(
        name="intuitive_recall",
        description=(
            "Query memory for context relevant to the current conversation. "
            "Call this at the start of every task or conversation turn to surface "
            "relevant past experience. Returns formatted memory context ready for injection."
        ),
    )
    async def intuitive_recall(message: str, agent: str) -> str:
        try:
            data = await _post_json(
                "/query",
                {
                    "query": message,
                    "agent": agent,
                    "limit": 3,
                    "collections": [agent, "warband_shared"],
                },
            )
            results = [item for item in data.get("results", []) if float(item.get("similarity", 0.0)) > 0.55]
        except Exception:
            results = []

        if not results:
            return "[INTUITION] No relevant memories found."

        lines = ["[INTUITION] Relevant memories:"]
        for item in results:
            content = str(item.get("content", "")).strip()
            if content:
                lines.append(f"- {content}")
        if len(lines) == 1:
            return "[INTUITION] No relevant memories found."
        return "\n".join(lines)

    @mcp.tool(
        name="recall",
        description="Search for specific knowledge in memory. Use when you know what you're looking for.",
        structured_output=True,
    )
    async def recall(
        query: str,
        agent: str,
        limit: int = 5,
        collections: Optional[list[str]] = None,
    ) -> dict[str, Any]:
        payload: dict[str, Any] = {
            "query": query,
            "agent": agent,
            "limit": max(1, int(limit)),
        }
        if collections:
            payload["collections"] = collections
        data = await _post_json("/query", payload)
        return {
            "query": query,
            "agent": agent,
            "limit": payload["limit"],
            "collections": collections or [],
            "results": data.get("results", []),
            "count": data.get("count", len(data.get("results", []))),
        }

    @mcp.tool(
        name="remember",
        description="Store important information for future reference. Use to save decisions, patterns, lessons learned, or task outcomes.",
        structured_output=True,
    )
    async def remember(
        content: str,
        agent: str,
        type: str = "general",
        tags: Optional[str] = None,
        shared: bool = False,
    ) -> dict[str, Any]:
        payload: dict[str, Any] = {
            "agent": agent,
            "content": content,
            "type": type,
            "tags": _split_tags(tags),
            "shared": bool(shared),
        }
        data = await _post_json("/remember", payload)
        return {
            "stored": bool(data.get("stored", False)),
            "memory_id": data.get("id"),
            "response": data,
        }

    @mcp.tool(
        name="memory_status",
        description="Check memory system health and statistics.",
        structured_output=True,
    )
    async def memory_status(agent: Optional[str] = None) -> dict[str, Any]:
        try:
            system = await _get_json("/health")
        except Exception as exc:
            system = {"status": "error", "error": str(exc), "memories": 0, "collections": []}
        result: dict[str, Any] = {
            "healthy": system.get("status") == "ok",
            "memory_count": system.get("memories", 0),
            "collections": system.get("collections", []),
            "system": system,
        }
        if agent:
            try:
                result["agent_health"] = await _get_json(f"/health/agent/{agent}")
            except Exception as exc:
                result["agent_health"] = None
                result["agent_health_error"] = str(exc)
        return result

    @mcp.tool(
        name="list_memories",
        description="List memories for review and audit. Use before deleting to find memory IDs.",
        structured_output=True,
    )
    async def list_memories(
        agent: str,
        limit: int = 20,
        offset: int = 0,
    ) -> dict[str, Any]:
        try:
            data = await _get_json_params(
                "/memories",
                {"agent": agent, "limit": limit, "offset": offset},
            )
            return {
                "memories": data.get("memories", []),
                "count": data.get("count", 0),
                "agent": agent,
                "limit": limit,
                "offset": offset,
            }
        except Exception as exc:
            return {"memories": [], "count": 0, "error": str(exc)}

    @mcp.tool(
        name="delete_memory",
        description="Delete a specific memory by ID. Use list_memories first to find the ID.",
        structured_output=True,
    )
    async def delete_memory(
        memory_id: str,
    ) -> dict[str, Any]:
        try:
            data = await _delete_json(f"/memories/{memory_id}")
            return {
                "deleted": bool(data.get("deleted", False)),
                "memory_id": memory_id,
                "response": data,
            }
        except Exception as exc:
            return {"deleted": False, "memory_id": memory_id, "error": str(exc)}

    return mcp


class _MCPASGIApp:
    def __init__(self) -> None:
        self._mcp = _build_mcp_server()
        self._transport = SseServerTransport("/messages")

    async def __call__(self, scope, receive, send):
        if scope["type"] != "http":
            return

        path = (scope.get("path") or "").rstrip("/") or "/"
        root = (scope.get("root_path") or "").rstrip("/")
        if root and path.startswith(root):
            path = path[len(root):] or "/"
        method = scope.get("method", "GET").upper()

        if method == "GET" and path == "/sse":
            async with self._transport.connect_sse(scope, receive, send) as streams:
                await self._mcp._mcp_server.run(
                    streams[0],
                    streams[1],
                    self._mcp._mcp_server.create_initialization_options(),
                )
            return

        if method == "POST" and path == "/messages":
            await self._transport.handle_post_message(scope, receive, send)
            return

        response = PlainTextResponse("Not Found", status_code=404)
        await response(scope, receive, send)


def create_mcp_app():
    return _MCPASGIApp()
