#!/usr/bin/env python3
"""
Seidr Memory Stats

Queries the Seidr service and prints a formatted table of memory counts per agent.
"""
import argparse
import json
import sys
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

DEFAULT_SEIDR_URL = "http://seidr.valhalla.svc:8082"


def fetch_json(url, timeout=10):
    """Fetch JSON from a URL endpoint."""
    try:
        with urlopen(Request(url, headers={"Accept": "application/json"}), timeout=timeout) as r:
            return json.load(r), None
    except HTTPError as e:
        return None, f"HTTP {e.code} {e.reason}"
    except URLError as e:
        return None, f"connection error: {e.reason}"
    except json.JSONDecodeError as e:
        return None, f"invalid JSON: {e}"


def get_collections(base_url):
    """Get list of agent collections from Seidr health endpoint."""
    url = f"{base_url.rstrip('/')}/health"
    data, err = fetch_json(url)
    if err:
        return None, err
    collections = data.get("collections", [])
    if not collections:
        return None, "no collections found in health response"
    return collections, None


def get_memory_count(base_url, agent):
    """Get memory count for a specific agent collection."""
    url = f"{base_url.rstrip('/')}/memories?agent={agent}"
    data, err = fetch_json(url)
    if err:
        return None, err
    if not isinstance(data, list):
        return None, f"unexpected response type: {type(data).__name__}"
    return len(data), None


def get_total_count(base_url):
    """Get total memory count from health endpoint."""
    url = f"{base_url.rstrip('/')}/health"
    data, err = fetch_json(url)
    if err:
        return None, err
    return data.get("memories", 0), None


def print_table(stats):
    """Print memory stats as a formatted ASCII table."""
    header = f"{'Agent':<15} {'Memory Count':>15}"
    separator = "-" * len(header)
    print(header)
    print(separator)
    for agent, count in stats:
        print(f"{agent:<15} {count:>15}")
    print(separator)
    total = sum(c for _, c in stats)
    print(f"{'TOTAL':<15} {total:>15}")


def main():
    parser = argparse.ArgumentParser(description="Print Seidr memory counts per agent collection")
    parser.add_argument(
        "--seidr-url",
        default=DEFAULT_SEIDR_URL,
        help=f"Seidr base URL (default: {DEFAULT_SEIDR_URL})",
    )
    parser.add_argument(
        "--format",
        choices=["table", "json"],
        default="table",
        help="Output format (default: table)",
    )
    args = parser.parse_args()

    # Get collections from health endpoint
    collections, err = get_collections(args.seidr_url)
    if err:
        print(f"Error fetching collections: {err}", file=sys.stderr)
        return 1

    # Get memory count for each collection
    stats = []
    for agent in sorted(collections):
        count, err = get_memory_count(args.seidr_url, agent)
        if err:
            print(f"Warning: failed to get count for {agent}: {err}", file=sys.stderr)
            count = 0
        stats.append((agent, count))

    # Get total from health for verification
    total, _ = get_total_count(args.seidr_url)

    if args.format == "json":
        output = {
            "seidr_url": args.seidr_url,
            "total": total,
            "agents": {agent: count for agent, count in stats},
        }
        print(json.dumps(output, indent=2))
    else:
        print_table(stats)

    return 0


if __name__ == "__main__":
    sys.exit(main())
