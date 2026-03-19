#!/usr/bin/env python3
import argparse
import json
import sys
from pathlib import Path
from urllib.error import HTTPError, URLError
from urllib.request import urlopen


def fetch_json(url):
    try:
        with urlopen(url) as response:
            return json.load(response)
    except HTTPError as exc:
        print(f"HTTP error for {url}: {exc.code} {exc.reason}", file=sys.stderr)
        sys.exit(1)
    except URLError as exc:
        print(f"Connection error for {url}: {exc.reason}", file=sys.stderr)
        sys.exit(1)
    except json.JSONDecodeError as exc:
        print(f"Invalid JSON from {url}: {exc}", file=sys.stderr)
        sys.exit(1)


def discover_agents(seidr_url):
    health = fetch_json(f"{seidr_url}/health")
    collections = health.get("collections", [])
    agents = []
    for item in collections:
        name = item.get("name") if isinstance(item, dict) else str(item)
        if name and name.endswith("-memory"):
            agents.append(name[:-7])
    return sorted(set(agents))


def export_agent(seidr_url, output_dir, agent):
    memories = fetch_json(f"{seidr_url}/memories?agent={agent}")
    output_path = output_dir / f"{agent}_memories.json"
    payload = {
        "agent": agent,
        "source": seidr_url,
        "memory_count": len(memories),
        "memories": memories,
    }
    output_path.write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")
    print(f"Exported {len(memories)} memories for agent {agent} to {output_path}")


def parse_args():
    parser = argparse.ArgumentParser(description="Export Seidr memories to JSON files")
    parser.add_argument(
        "--seidr-url",
        default="http://seidr.valhalla.svc:8082",
        help="Base URL for Seidr API",
    )
    parser.add_argument(
        "--output-dir",
        default="./seidr-export",
        help="Directory to write exported JSON files",
    )
    parser.add_argument(
        "--agent",
        help="Export memories for one agent only",
    )
    return parser.parse_args()


def main():
    args = parse_args()
    seidr_url = args.seidr_url.rstrip("/")
    output_dir = Path(args.output_dir)
    output_dir.mkdir(parents=True, exist_ok=True)
    agents = [args.agent] if args.agent else discover_agents(seidr_url)
    if not agents:
        print("No agent memory collections found.", file=sys.stderr)
        sys.exit(1)
    for agent in agents:
        export_agent(seidr_url, output_dir, agent)


if __name__ == "__main__":
    main()
