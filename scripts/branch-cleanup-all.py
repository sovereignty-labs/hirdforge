#!/usr/bin/env python3
"""
Branch cleanup script for all Hirdforge repos.
Lists and optionally deletes stale CI/auto-generated branches across all repos.
Dry-run by default; use --delete to actually delete branches.
"""
import argparse
import datetime
import json
import os
import sys
import urllib.request
import urllib.error


# Target repos for cleanup
REPOS = [
    "kit/hirdforge",
    "kit/hirdforge",
    "kit/hirdforge-personas",
    "kit/hirdforge-tasks",
]

# Branch prefixes to consider for cleanup
STALE_PREFIXES = ["ci-", "settings/", "auto-", "feature/"]

# Protected branch names that should never be deleted
PROTECTED_BRANCHES = {"main", "master"}


def parse_args():
    parser = argparse.ArgumentParser(
        description="Clean up stale branches across all Hirdforge repos"
    )
    parser.add_argument(
        "--gitea-url",
        default=os.environ.get("GITEA_URL", "http://203.0.113.26:30637"),
        help="Gitea base URL",
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
        "--delete",
        action="store_true",
        help="Actually delete branches (default is dry-run)",
    )
    parser.add_argument(
        "--prefixes",
        default="ci-,settings/,auto-,feature/",
        help="Comma-separated list of branch prefixes to target",
    )
    return parser.parse_args()


def get_gitea_token():
    """Get Gitea token from environment."""
    return os.environ.get("GITEA_TOKEN")


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
            print(f"Error fetching branches for {owner}/{repo}: {e.code} {e.reason}", file=sys.stderr)
            break
        except urllib.error.URLError as e:
            print(f"Error connecting to {gitea_url}: {e.reason}", file=sys.stderr)
            break

    return branches


def delete_branch(gitea_url, owner, repo, branch_name, token):
    """Delete a branch via Gitea API."""
    url = f"{gitea_url}/api/v1/repos/{owner}/{repo}/branches/{branch_name}"
    req = urllib.request.Request(url, method="DELETE")
    if token:
        req.add_header("Authorization", f"token {token}")

    try:
        urllib.request.urlopen(req)
        return True, None
    except urllib.error.HTTPError as e:
        body = e.read().decode() if e.fp else ""
        return False, f"HTTP {e.code}: {body}"
    except urllib.error.URLError as e:
        return False, f"URL error: {e.reason}"


def is_stale_branch(branch_name, last_activity, max_age_days, cutoff, prefixes):
    """Check if a branch matches stale criteria."""
    # Never delete main or master
    if branch_name in PROTECTED_BRANCHES:
        return False, "protected (main/master)"

    # Check prefix match
    is_stale_prefix = any(branch_name.startswith(prefix) for prefix in prefixes)
    if not is_stale_prefix:
        return False, "does not match prefix"

    # Check age
    if last_activity and last_activity < cutoff:
        return True, "stale (age > 7 days)"

    return False, "recent"


def analyze_repo(gitea_url, owner, repo, token, max_age_days, cutoff, prefixes):
    """Analyze a repo's branches and return cleanup candidates."""
    branches = get_branches(gitea_url, owner, repo, token)
    if branches is None:
        return None

    protected = []
    stale = []
    other = []

    for b in branches:
        name = b.get("name", "")
        last_activity = None
        protected_flag = False

        # Get last activity timestamp (updated or created)
        if "updated" in b and b["updated"]:
            try:
                last_activity = datetime.datetime.fromisoformat(
                    b["updated"].replace("Z", "+00:00")
                )
            except (ValueError, TypeError):
                pass
        elif "created" in b and b["created"]:
            try:
                last_activity = datetime.datetime.fromisoformat(
                    b["created"].replace("Z", "+00:00")
                )
            except (ValueError, TypeError):
                pass

        # Check if protected
        if b.get("protected", False) or name in PROTECTED_BRANCHES:
            protected_flag = True

        is_stale, reason = is_stale_branch(
            name, last_activity, max_age_days, cutoff, prefixes
        )

        entry = {
            "name": name,
            "protected": protected_flag,
            "last_activity": last_activity.isoformat() if last_activity else None,
            "reason": reason,
        }

        if protected_flag:
            protected.append(entry)
        elif is_stale:
            stale.append(entry)
        else:
            other.append(entry)

    return {
        "repo": f"{owner}/{repo}",
        "total": len(branches),
        "protected": len(protected),
        "stale": stale,
        "other": other,
    }


def main():
    args = parse_args()
    token = get_gitea_token()

    # Parse prefixes
    prefixes = [p.strip() for p in args.prefixes.split(",") if p.strip()]

    cutoff = datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(
        days=args.max_age_days
    )

    # Summary tracking
    total_branches = 0
    total_stale = 0
    total_deleted = 0
    results = []

    for repo_path in REPOS:
        parts = repo_path.split("/", 1)
        if len(parts) != 2:
            print(f"Invalid repo format: {repo_path} (expected owner/name)", file=sys.stderr)
            continue

        owner, repo = parts

        if args.format == "json":
            result = analyze_repo(
                args.gitea_url, owner, repo, token, args.max_age_days, cutoff, prefixes
            )
            if result:
                results.append(result)
                total_branches += result["total"]
                total_stale += len(result["stale"])
            continue

        # Text output for each repo
        result = analyze_repo(
            args.gitea_url, owner, repo, token, args.max_age_days, cutoff, prefixes
        )
        if not result:
            continue

        results.append(result)
        total_branches += result["total"]
        total_stale += len(result["stale"])

        print(f"\n{'='*60}")
        print(f"Repo: {result['repo']}")
        print(f"{'='*60}")
        print(f"Total branches  : {result['total']}")
        print(f"Protected       : {result['protected']}")
        print(f"Stale candidates: {len(result['stale'])}")
        print(f"Recent/other    : {len(result['other'])}")

        if result["stale"]:
            print(f"\nStale branches ({args.max_age_days}+ days, prefixes: {', '.join(prefixes)}):")
            for b in result["stale"]:
                action = "DELETE" if args.delete else "would delete"
                print(f"  [{action}] {b['name']} (last: {b['last_activity']})")

                if args.delete:
                    ok, err = delete_branch(
                        args.gitea_url, owner, repo, b["name"], token
                    )
                    if ok:
                        total_deleted += 1
                        print(f"         -> Deleted successfully")
                    else:
                        print(f"         -> FAILED: {err}")

    # Summary
    print(f"\n{'='*60}")
    print("SUMMARY")
    print(f"{'='*60}")
    print(f"Total branches examined: {total_branches}")
    print(f"Stale candidates found : {total_stale}")
    print(f"Branches deleted       : {total_deleted}")

    if not args.delete:
        print("\n[DRY-RUN] No branches were deleted. Use --delete to delete.")

    if args.format == "json":
        output = {
            "max_age_days": args.max_age_days,
            "cutoff": cutoff.isoformat(),
            "delete_mode": args.delete,
            "repos": results,
            "summary": {
                "total_branches": total_branches,
                "total_stale": total_stale,
                "total_deleted": total_deleted,
            },
        }
        print(json.dumps(output, indent=2))


if __name__ == "__main__":
    main()