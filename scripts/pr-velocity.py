#!/usr/bin/env python3
"""PR merge velocity report for Hirdforge repos.

Queries Gitea for merged PRs and produces:
- PRs per day (timeline)
- Top contributors (by PR count)
- Average time to merge (from open to merge)

Uses only stdlib, suitable for CI or dev shell.
"""

import argparse
import collections
import datetime as dt
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
    # Handle various formats Gitea might use
    ts = ts.replace("Z", "+00:00")
    if "." in ts:
        # Has microseconds
        return dt.datetime.fromisoformat(ts)
    return dt.datetime.fromisoformat(ts)


def group_prs_by_day(prs_by_repo: Dict[str, List[dict]]) -> Tuple[Dict[str, int], Dict[str, Dict[str, int]]]:
    """Group merged PRs by merge date.

    Returns:
        total_by_day: {date: pr_count}
        by_repo_by_day: {date: {repo: count}}
    """

    total_by_day: Dict[str, int] = collections.Counter()
    by_repo_by_day: Dict[str, Dict[str, int]] = {}

    for repo, prs in prs_by_repo.items():
        for pr in prs:
            merged_at = pr.get("merged_at")
            if not merged_at:
                continue
            try:
                day = merged_at.split("T", 1)[0]
            except Exception:
                continue

            total_by_day[day] += 1
            if day not in by_repo_by_day:
                by_repo_by_day[day] = {}
            by_repo_by_day[day][repo] = by_repo_by_day[day].get(repo, 0) + 1

    return dict(total_by_day), by_repo_by_day


def calculate_merge_time(prs: List[dict]) -> List[Tuple[str, float]]:
    """Calculate time-to-merge for each PR.

    Returns list of (PR number, hours_to_merge) for PRs with both created_at and merged_at.
    """

    merge_times: List[Tuple[str, float]] = []

    for pr in prs:
        created_at = pr.get("created_at")
        merged_at = pr.get("merged_at")

        if not created_at or not merged_at:
            continue

        try:
            created = parse_timestamp(created_at)
            merged = parse_timestamp(merged_at)
            delta = merged - created
            hours = delta.total_seconds() / 3600
            merge_times.append((str(pr.get("number", "unknown")), hours))
        except Exception:
            continue

    return merge_times


def calculate_averages(merge_times: List[Tuple[str, float]]) -> Dict[str, float]:
    """Calculate average merge time by repo.

    Returns {repo: avg_hours} for repos with merge time data.
    """

    # Group by repo (simplified - assumes all PRs in list are from same context)
    # For proper per-repo stats, we'd need to pass repo info with each PR
    if not merge_times:
        return {}

    total = sum(t for _, t in merge_times)
    count = len(merge_times)
    return {"overall": total / count if count > 0 else 0}


def print_text_report(
    total_by_day: Dict[str, int],
    contributors: Dict[str, int],
    avg_merge_times: Dict[str, float],
) -> None:
    """Print text report to stdout."""

    print("=" * 60)
    print("HIRDFORGE PR MERGE VELOCITY REPORT")
    print("=" * 60)

    # PRs per day
    print("\n--- PRs Per Day (sorted by date) ---")
    if total_by_day:
        for day in sorted(total_by_day.keys()):
            count = total_by_day[day]
            bar = "".join("█" for _ in range(min(count, 30)))
            print(f"{day}: {bar} {count}")
    else:
        print("No PR data found.")

    # Top contributors
    print("\n--- Top Contributors (by PR count) ---")
    if contributors:
        sorted_contribs = sorted(contributors.items(), key=lambda x: x[1], reverse=True)
        for idx, (name, count) in enumerate(sorted_contribs[:10], 1):
            print(f"{idx}. {name}: {count} PRs")
    else:
        print("No contributor data found.")

    # Average merge time
    print("\n--- Average Time to Merge ---")
    if avg_merge_times:
        for repo, hours in avg_merge_times.items():
            if hours < 1:
                mins = int(hours * 60)
                print(f"  {repo}: {mins} minutes")
            else:
                hrs = int(hours)
                mins = int((hours - hrs) * 60)
                print(f"  {repo}: {hrs}h {mins}m")
    else:
        print("No merge time data found.")

    print("\n" + "=" * 60)


def fetch_contributors(prs_by_repo: Dict[str, List[dict]]) -> Dict[str, int]:
    """Count PRs by author across all repos.

    Returns {author_name: pr_count}.
    """

    author_counts: Dict[str, int] = collections.Counter()

    for repo, prs in prs_by_repo.items():
        for pr in prs:
            author = pr.get("user", {}).get("username") or pr.get("user", {}).get("login")
            if author:
                author_counts[author] += 1

    return dict(author_counts)


def main(argv: List[str]) -> int:
    parser = argparse.ArgumentParser(description="PR merge velocity report for Hirdforge repos.")
    parser.add_argument(
        "--gitea-url",
        default="http://gitea-http.gitea.svc.cluster.local:3000",
        help="Base URL for Gitea (default: %(default)s)",
    )
    parser.add_argument(
        "--repos",
        default="gitea_admin/project_valhalla,kit/valhalla-infra,kit/hirdforge-personas",
        help="Comma-separated list of owner/repo names",
    )
    parser.add_argument(
        "--token",
        default=None,
        help="Gitea token for authentication (from $GITEA_TOKEN if not provided)",
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

    prs_by_repo: Dict[str, List[dict]] = {}
    for repo in repos:
        print(f"  {repo}...", end=" ", flush=True)
        prs = fetch_all_merged_prs(args.gitea_url, repo)
        prs_by_repo[repo] = prs
        print(f"{len(prs)} PRs")

    # Calculate metrics
    total_by_day, by_repo_by_day = group_prs_by_day(prs_by_repo)

    # Collect all PRs for merge time calculation
    all_prs = []
    for repo, prs in prs_by_repo.items():
        for pr in prs:
            pr_with_repo = dict(pr)
            pr_with_repo["repo"] = repo
            all_prs.append(pr_with_repo)

    merge_times = calculate_merge_time(all_prs)
    avg_merge_times = calculate_averages(merge_times)

    # Get contributors
    contributors = fetch_contributors(prs_by_repo)

    # Print report
    print_text_report(total_by_day, contributors, avg_merge_times)

    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))