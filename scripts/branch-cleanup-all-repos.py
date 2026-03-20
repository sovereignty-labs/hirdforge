#!/usr/bin/env python3
"""
List stale branches across all accessible Gitea repos.
Discovers repos dynamically, then scans each for branches older than 7 days.
Read-only: only lists, does not delete.
"""
import argparse
import datetime
import json
import os
import sys
import urllib.request
import urllib.error


def parse_args():
    parser = argparse.ArgumentParser(
        description="List stale branches across all accessible Gitea repos"
    )
    parser.add_argument(
        "--gitea-url",
        default=os.environ.get("GITEA_URL", "http://gitea-http.gitea.svc.cluster.local:3000"),
        help="Gitea base URL (default: from GITEA_URL env or internal cluster URL)",
    )
    parser.add_argument(
        "--max-age-days",
        type=int,
        default=7,
        help="Branch age threshold in days (default: 7)",
    )
    parser.add_argument(
        "--format",
        choices=["text", "json"],
        default="text",
        help="Output format (default: text)",
    )
    parser.add_argument(
        "--owner",
        default=None,
        help="Filter repos by owner (e.g., 'kit', 'gitea_admin'). If omitted, lists all accessible repos.",
    )
    parser.add_argument(
        "--include-archived",
        action="store_true",
        help="Include archived repos (default: exclude archived repos)",
    )
    return parser.parse_args()


def get_gitea_token():
    """Get Gitea token from environment."""
    return os.environ.get("GITEA_TOKEN")


def get_all_repos(gitea_url, token, owner_filter=None, include_archived=False):
    """Fetch all repos the authenticated user can access, with optional owner filter."""
    repos = []
    page = 1
    per_page = 100
    base_url = f"{gitea_url}/api/v1"

    while True:
        url = f"{base_url}/user/repos?page={page}&limit={per_page}&sort=updated"
        req = urllib.request.Request(url)
        if token:
            req.add_header("Authorization", f"token {token}")

        try:
            with urllib.request.urlopen(req) as resp:
                data = json.loads(resp.read().decode())
                if not data:
                    break

                for repo in data:
                    # Apply owner filter
                    if owner_filter and repo.get("owner", {}).get("login") != owner_filter:
                        continue
                    # Filter archived unless requested
                    if repo.get("archived", False) and not include_archived:
                        continue
                    repos.append({
                        "owner": repo.get("owner", {}).get("login"),
                        "name": repo.get("name"),
                        "full_name": repo.get("full_name"),
                        "archived": repo.get("archived", False),
                        "updated_at": repo.get("updated_at"),
                    })

                if len(data) < per_page:
                    break
                page += 1

        except urllib.error.HTTPError as e:
            print(f"Error fetching repos: {e.code} {e.reason}", file=sys.stderr)
            break
        except urllib.error.URLError as e:
            print(f"Error connecting to {gitea_url}: {e.reason}", file=sys.stderr)
            break

    return repos


def get_branches(gitea_url, owner, repo, token):
    """Fetch all branches for a repo, handling pagination."""
    branches = []
    page = 1
    base_url = f"{gitea_url}/api/v1/repos/{owner}/{repo}/branches"

    while True:
        url = f"{base_url}?page={page}&limit=100"
        req = urllib.request.Request(url)
        if token:
            req.add_header("Authorization", f"token {token}")

        try:
            with urllib.request.urlopen(req) as resp:
                data = json.loads(resp.read().decode())
                if not data:
                    break
                branches.extend(data)
                if len(data) < 100:
                    break
                page += 1
        except urllib.error.HTTPError as e:
            print(f"  Warning: Could not fetch branches for {owner}/{repo}: {e.code}", file=sys.stderr)
            break
        except urllib.error.URLError as e:
            print(f"  Warning: Connection error for {owner}/{repo}: {e.reason}", file=sys.stderr)
            break

    return branches


def find_stale_branches(branches, cutoff, protected_names):
    """Find branches older than cutoff, excluding protected branch names."""
    stale = []

    for b in branches:
        name = b.get("name", "")
        
        # Skip protected branch names (main, master, etc.)
        if name in protected_names:
            continue

        # Skip if branch is marked as protected in Gitea
        if b.get("protected", False):
            continue

        # Get last activity timestamp
        last_activity = None
        if b.get("updated"):
            try:
                last_activity = datetime.datetime.fromisoformat(
                    b["updated"].replace("Z", "+00:00")
                )
            except (ValueError, TypeError):
                pass
        elif b.get("created"):
            try:
                last_activity = datetime.datetime.fromisoformat(
                    b["created"].replace("Z", "+00:00")
                )
            except (ValueError, TypeError):
                pass

        # Check if stale
        if last_activity and last_activity < cutoff:
            stale.append({
                "name": name,
                "last_activity": last_activity.isoformat(),
                "days_old": (datetime.datetime.now(datetime.timezone.utc) - last_activity).days,
                "protected": b.get("protected", False),
            })

    return stale


def main():
    args = parse_args()
    token = get_gitea_token()

    if not token:
        print("Error: GITEA_TOKEN environment variable not set", file=sys.stderr)
        sys.exit(1)

    cutoff = datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(
        days=args.max_age_days
    )

    protected_names = {"main", "master"}

    # Step 1: Discover all accessible repos
    print(f"Discovering repos (filter: owner={args.owner or 'all'}, include_archived={args.include_archived})...", file=sys.stderr)
    repos = get_all_repos(args.gitea_url, token, args.owner, args.include_archived)
    
    if not repos:
        print("No repos found.", file=sys.stderr)
        sys.exit(0)

    print(f"Found {len(repos)} repos to scan", file=sys.stderr)

    # Step 2: Scan each repo for stale branches
    all_stale = []
    repo_results = []
    total_branches = 0
    total_stale = 0

    for repo_info in repos:
        owner = repo_info["owner"]
        repo_name = repo_info["name"]
        full_name = repo_info["full_name"]

        branches = get_branches(args.gitea_url, owner, repo_name, token)
        total_branches += len(branches)

        stale = find_stale_branches(branches, cutoff, protected_names)
        total_stale += len(stale)

        if stale:
            repo_results.append({
                "repo": full_name,
                "owner": owner,
                "name": repo_name,
                "total_branches": len(branches),
                "stale_count": len(stale),
                "stale_branches": stale,
            })
            all_stale.extend([(full_name, s) for s in stale])

    # Step 3: Output results
    if args.format == "json":
        output = {
            "max_age_days": args.max_age_days,
            "cutoff": cutoff.isoformat(),
            "repos_scanned": len(repos),
            "total_branches": total_branches,
            "total_stale": total_stale,
            "repos_with_stale": repo_results,
        }
        print(json.dumps(output, indent=2))
        return

    # Text format
    if not all_stale:
        print("\nNo stale branches found across any repos.")
        return

    print(f"\n{'='*70}")
    print(f"STALE BRANCHES (> {args.max_age_days} days inactive, excluding main/master)")
    print(f"{'='*70}")
    print(f"Repos scanned : {len(repos)}")
    print(f"Total branches: {total_branches}")
    print(f"Stale branches: {total_stale}")
    print(f"{'='*70}\n")

    for result in repo_results:
        print(f"[{result['repo']}]")
        print(f"  Total branches : {result['total_branches']}")
        print(f"  Stale branches : {result['stale_count']}")
        for stale in result["stale_branches"]:
            days = stale["days_old"]
            print(f"    - {stale['name']} ({days} days old, last: {stale['last_activity']})")
        print()


if __name__ == "__main__":
    main()
