#!/usr/bin/env python3
"""Agent session analyzer - queries Seidr API and generates session analysis reports."""

import argparse
import json
import sys
import urllib.request
from collections import Counter
from datetime import datetime, timedelta


def fetch_json(url):
    """Fetch JSON from URL, return None on error."""
    try:
        req = urllib.request.Request(url)
        with urllib.request.urlopen(req, timeout=10) as resp:
            return json.loads(resp.read().decode())
    except Exception as e:
        return None


def analyze_sessions(seidr_url):
    """Analyze session data from Seidr API."""
    sessions_url = f"{seidr_url}/session-state"
    sessions_data = fetch_json(sessions_url)
    
    if not sessions_data or "agents" not in sessions_data:
        return None
    
    results = {
        "overview": {},
        "agents": {},
        "recommendations": []
    }
    
    # Session overview
    total_sessions = 0
    for agent_name, agent_data in sessions_data["agents"].items():
        session_count = len(agent_data.get("sessions", []))
        total_sessions += session_count
        results["agents"][agent_name] = {
            "session_count": session_count,
            "sessions": agent_data.get("sessions", []),
            "memory_count": 0,
            "memory_24h": 0,
            "tool_usage": Counter(),
            "error_count": 0,
            "last_session": None
        }
        if agent_data.get("sessions"):
            results["agents"][agent_name]["last_session"] = agent_data["sessions"][-1].get("created_at")
    
    results["overview"]["total_sessions"] = total_sessions
    results["overview"]["agents_with_sessions"] = len([a for a in results["agents"].values() if a["session_count"] > 0])
    
    # Per-agent analysis
    for agent_name, agent_info in results["agents"].items():
        for session in agent_info["sessions"]:
            tools = session.get("tools", [])
            for tool_call in tools:
                tool_name = tool_call.get("name", "unknown")
                agent_info["tool_usage"][tool_name] += 1
            
            content = session.get("content", "")
            if "error" in content.lower() or "failed" in content.lower():
                agent_info["error_count"] += 1
        
        # Memory analysis
        mem_url = f"{seidr_url}/memories?agent={agent_name}"
        memories = fetch_json(mem_url)
        if memories:
            total_memories = len(memories) if isinstance(memories, list) else 0
            agent_info["memory_count"] = total_memories
            
            # Count memories from last 24h
            now = datetime.utcnow()
            cutoff = now - timedelta(hours=24)
            recent = 0
            for mem in memories:
                created = mem.get("created_at", "")
                try:
                    mem_time = datetime.fromisoformat(created.replace("Z", "+00:00"))
                    if mem_time.replace(tzinfo=None) > cutoff:
                        recent += 1
                except (ValueError, TypeError):
                    pass
            agent_info["memory_24h"] = recent
    
    # Generate recommendations
    for agent_name, agent_info in results["agents"].items():
        if agent_info["session_count"] == 0:
            results["recommendations"].append(f"Agent '{agent_name}' has zero sessions - check if agent is healthy")
        else:
            error_rate = agent_info["error_count"] / agent_info["session_count"]
            if error_rate > 0.5:
                results["recommendations"].append(
                    f"Agent '{agent_name}' has high error rate: {error_rate:.1%} ({agent_info['error_count']}/{agent_info['session_count']} sessions)"
                )
        
        if agent_info["memory_24h"] == 0 and agent_info["memory_count"] > 0:
            results["recommendations"].append(
                f"Agent '{agent_name}' has no recent memory activity ({agent_info['memory_count']} total, 0 in last 24h)"
            )
    
    return results


def format_text_report(results):
    """Format results as human-readable text."""
    lines = []
    lines.append("=" * 60)
    lines.append("AGENT SESSION ANALYSIS REPORT")
    lines.append("=" * 60)
    
    # Overview
    lines.append("\n## SESSION OVERVIEW")
    lines.append(f"Total sessions analyzed: {results['overview']['total_sessions']}")
    lines.append(f"Agents with sessions: {results['overview']['agents_with_sessions']}")
    
    # Per-agent stats
    lines.append("\n## PER-AGENT SESSION STATS")
    for agent_name, agent_info in sorted(results["agents"].items()):
        if agent_info["session_count"] > 0:
            lines.append(f"\n{agent_name.upper()}")
            lines.append(f"  Sessions: {agent_info['session_count']}")
            lines.append(f"  Errors: {agent_info['error_count']}")
            lines.append(f"  Memory (total/24h): {agent_info['memory_count']}/{agent_info['memory_24h']}")
            if agent_info["tool_usage"]:
                top_tools = agent_info["tool_usage"].most_common(5)
                lines.append(f"  Top tools: {', '.join(f'{k}({v})' for k, v in top_tools)}")
            if agent_info["last_session"]:
                lines.append(f"  Last session: {agent_info['last_session']}")
    
    # Recommendations
    if results["recommendations"]:
        lines.append("\n## RECOMMENDATIONS")
        for rec in results["recommendations"]:
            lines.append(f"  - {rec}")
    else:
        lines.append("\n## RECOMMENDATIONS")
        lines.append("  No issues detected.")
    
    lines.append("\n" + "=" * 60)
    return "\n".join(lines)


def format_json_report(results):
    """Format results as JSON."""
    return json.dumps(results, indent=2)


def main():
    parser = argparse.ArgumentParser(description="Analyze agent session patterns from Seidr API")
    parser.add_argument("--seidr-url", default="http://seidr.valhalla.svc:8082",
                        help="Seidr API URL")
    parser.add_argument("--format", choices=["text", "json"], default="text",
                        help="Output format")
    args = parser.parse_args()
    
    results = analyze_sessions(args.seidr_url)
    
    if results is None:
        print("ERROR: Failed to fetch session data from Seidr API", file=sys.stderr)
        sys.exit(1)
    
    if args.format == "json":
        print(format_json_report(results))
    else:
        print(format_text_report(results))


if __name__ == "__main__":
    main()