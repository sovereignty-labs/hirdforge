#!/usr/bin/env python3
"""PR merge velocity report for Hirdforge repos.

Queries Gitea for merged PRs in the last 7 days and prints:
- Total merged count
- Merged per day
- Merged per author

Uses only stdlib, suitable for CI or dev shell.
"""

import argparse
import collections
import datetime as dt
import json
import sys
import urllib.parse
import urllib.request
from typing import Dict, List, Tuple


# Gitea token from environment
GITEA_TOKEN = None


def set_gitea_token(token: str) -> None:
    """Set Gitea auth token for API calls."""
    global GITEA_TOKEN
    GITEA_TOKEN = token


def fetch_all_merged_prs(base_url: str, repo: str) -> List[dict]:
    """Fetch all merged PRs for a repo, handling pagination.

    Uses Gitea's /repos/{owner}/{repo}/pulls endpoint with state=closed
    and filters for merged_at > 0.
    """

    prs: List[dict] = []
    page = 1
    per_page = 50

    while True:
        path = f"/api/v1/repos/{repo}/pulls?state=closed&sort=recentupdate&direction=desc&limit={per_page}&page={page}"
        url = urllib.parse.urljoin(base_url.rstrip("/"), path)

        headers = {}
        if GITEA_TOKEN:
            headers["Authorization"] = f"token {GITEA_TOKEN}"

        try:
            req = urllib.request.Request(url, headers=headers)
            with urllib.request.urlopen(req) as resp:  # nosec
                data = json.load(resp)
        except Exception as exc:
            print(f"error: failed to fetch PRs for {repo} page {page}: {exc}", file=sys.stderr)
            break

        if not data:
            break

        # Filter for merged PRs only
        for pr in data:
            if pr.get("merged_at"):
                prs.append(pr)

        # Stop when fewer than requested items are returned
        if len(data) < per_page:
            break

        page += 1

    return prs


def parse_timestamp(ts: str) -> dt.datetime:
    """Parse RFC3339 timestamp to datetime."""
    ts = ts.replace("Z", "+00:00")
    if "." in ts:
        return dt.datetime.fromisoformat(ts)
    return dt.datetime.fromisoformat(ts)


def filter_by_date(prs: List[dict], days: int) -> List[dict]:
    """Filter PRs to only those merged within the last N days."""
    cutoff = dt.datetime.now(dt.timezone.utc) - dt.timedelta(days=days)
    filtered = []

    for pr in prs:
        merged_at = pr.get("merged_at")
        if not merged_at:
            continue
        try:
            merged_dt = parse_timestamp(merged_at)
            if merged_dt.replace(tzinfo=None) >= cutoff.replace(tzinfo=None):
                filtered.append(pr)
        except Exception:
            continue

    return filtered


def group_prs_by_day(prs: List[dict]) -> Dict[str, int]:
    """Group merged PRs by merge date.

    Returns {date: pr_count}.
    """
    by_day: Dict[str, int] = collections.Counter()

    for pr in prs:
        merged_at = pr.get("merged_at")
        if not merged_at:
            continue
        try:
            day = merged_at.split("T", 1)[0]
            by_day[day] += 1
        except Exception:
            continue

    return dict(by_day)


def group_prs_by_author(prs: List[dict]) -> Dict[str, int]:
    """Count PRs by author.

    Returns {author_name: pr_count}.
    """
    author_counts: Dict[str, int] = collections.Counter()

    for pr in prs:
        user = pr.get("user", {})
        author = user.get("username") or user.get("login")
        if author:
            author_counts[author] += 1

    return dict(author_counts)


def print_velocity_report(
    total: int,
    by_day: Dict[str, int],
    by_author: Dict[str, int],
) -> None:
    """Print velocity report to stdout."""

    print("=" * 60)
    print("HIRDFORGE PR VELOCITY REPORT (Last 7 Days)")
    print("=" * 60)

    # Total merged
    print(f"\n--- Total Merged: {total} ---")

    # Merged per day
    print("\n--- Merged Per Day ---")
    if by_day:
        for day in sorted(by_day.keys()):
            count = by_day[day]
            bar = "".join("█" for _ in range(min(count, 30)))
            print(f"{day}: {bar} {count}")
    else:
        print("No PR data found.")

    # Merged per author
    print("\n--- Merged Per Author ---")
    if by_author:
        sorted_authors = sorted(by_author.items(), key=lambda x: x[1], reverse=True)
        for name, count in sorted_authors:
            bar = "".join("█" for _ in range(min(count, 30)))
            print(f"{name}: {bar} {count}")
    else:
        print("No author data found.")

    print("\n" + "=" * 60)


def main(argv: List[str]) -> int:
    parser = argparse.ArgumentParser(
        description="PR merge velocity report for Hirdforge repos.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument(
        "--gitea-url",
        default="http://gitea-http.gitea.svc.cluster.local:3000",
        help="Base URL for Gitea (default: %(default)s)",
    )
    parser.add_argument(
        "--repos",
        default="kit/hirdforge,kit/hirdforge,kit/hirdforge-personas",
        help="Comma-separated list of owner/repo names",
    )
    parser.add_argument(
        "--token",
        default=None,
        help="Gitea token for authentication (from $GITEA_TOKEN if not provided)",
    )
    parser.add_argument(
        "--days",
        type=int,
        default=7,
        help="Number of days to look back (default: %(default)s)",
    )
    args = parser.parse_args(argv)

    # Get token from environment if not provided
    token = args.token or GITEA_TOKEN
    set_gitea_token(token)

    repos = [r.strip() for r in args.repos.split(",") if r.strip()]
    if not repos:
        print("error: at least one repo must be specified via --repos", file=sys.stderr)
        return 1

    print(f"Fetching PRs from {len(repos)} repos...")

    all_merged_prs: List[dict] = []
    for repo in repos:
        print(f"  {repo}...", end=" ", flush=True)
        prs = fetch_all_merged_prs(args.gitea_url, repo)
        all_merged_prs.extend(prs)
        print(f"{len(prs)} total PRs")

    # Filter by date
    filtered_prs = filter_by_date(all_merged_prs, args.days)
    total = len(filtered_prs)

    # Calculate metrics
    by_day = group_prs_by_day(filtered_prs)
    by_author = group_prs_by_author(filtered_prs)

    # Print report
    print_velocity_report(total, by_day, by_author)

    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))