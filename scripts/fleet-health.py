#!/usr/bin/env python3
import argparse
import json
import sys
import urllib.error
import urllib.request

DEFAULT_URL = "http://gateway.valhalla.svc:8080"
GREEN = "\033[32m"
RED = "\033[31m"
RESET = "\033[0m"


def parse_args():
    p = argparse.ArgumentParser(description="Check fleet health from the gateway")
    p.add_argument("--gateway-url", default=DEFAULT_URL, help="Gateway base URL")
    p.add_argument("--format", choices=["table", "json"], default="table", help="Output format")
    return p.parse_args()


def fetch_agents(base_url):
    url = base_url.rstrip("/") + "/api/v1/agents"
    req = urllib.request.Request(url, headers={"Accept": "application/json"})
    with urllib.request.urlopen(req, timeout=10) as resp:
        return json.load(resp)


def normalize_agents(payload):
    if isinstance(payload, list):
        return payload
    if isinstance(payload, dict):
        for key in ("agents", "data", "items"):
            value = payload.get(key)
            if isinstance(value, list):
                return value
    raise ValueError("unexpected response shape from /api/v1/agents")


def agent_name(agent, index):
    return str(agent.get("name") or agent.get("agent") or agent.get("id") or f"agent-{index}")


def agent_healthy(agent):
    status = str(agent.get("status") or agent.get("health") or "").lower()
    if status:
        return status in ("healthy", "ok", "up", "ready")
    if "healthy" in agent:
        return bool(agent.get("healthy"))
    return False


def render_table(rows):
    widths = {
        "name": max(len("AGENT"), *(len(r["name"]) for r in rows)) if rows else len("AGENT"),
        "status": len("STATUS"),
    }
    print(f"{'AGENT':<{widths['name']}}  STATUS")
    for row in rows:
        color = GREEN if row["healthy"] else RED
        label = "healthy" if row["healthy"] else "unhealthy"
        print(f"{row['name']:<{widths['name']}}  {color}{label:<{widths['status']}}{RESET}")


def main():
    args = parse_args()
    try:
        payload = fetch_agents(args.gateway_url)
        agents = normalize_agents(payload)
    except urllib.error.HTTPError as err:
        print(f"http error: {err.code} {err.reason}", file=sys.stderr)
        return 2
    except urllib.error.URLError as err:
        print(f"connection error: {err.reason}", file=sys.stderr)
        return 2
    except (ValueError, json.JSONDecodeError) as err:
        print(f"invalid response: {err}", file=sys.stderr)
        return 2

    rows = []
    for i, agent in enumerate(agents, start=1):
        if not isinstance(agent, dict):
            continue
        rows.append({"name": agent_name(agent, i), "healthy": agent_healthy(agent)})

    if args.format == "json":
        print(json.dumps(rows, indent=2))
    else:
        render_table(rows)

    if not rows:
        return 2
    return 0 if all(r["healthy"] for r in rows) else 1


if __name__ == "__main__":
    sys.exit(main())
