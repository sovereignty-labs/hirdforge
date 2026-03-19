#!/usr/bin/env python3
"""
Stale branch cleanup tool for Gitea repos.
Dry-run by default; use --execute to actually delete branches.
"""
import argparse
import datetime
import json
import sys
import urllib.request
import urllib.error


def parse_args():
    parser = argparse.ArgumentParser(description="Clean up stale Gitea branches")
    parser.add_argument(
        "--gitea-url",
        default="http://203.0.113.26:30637",
        help="Gitea base URL",
    )
    parser.add_argument(
        "--repos",
        default="gitea_admin/project_valhalla,kit/valhalla-infra,kit/hirdforge-personas",
        help="Comma-separated repo list (owner/name,...)",
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
        help="Output format",
    )
    parser.add_argument(
        "--execute",
        action="store_true",
        help="Actually delete branches (default is dry-run)",
    )
    return parser.parse_args()


def get_branches(gitea_url, owner, repo, token):
    """Fetch all branches for a repo, handling pagination."""
    branches = []
    page = 1
    while True:
        url = f"{gitea_url}/api/v1/repos/{owner}/{repo}/branches?page={page}&limit=100"
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


def analyze_repo(gitea_url, owner, repo, token, max_age_days, cutoff):
    """Analyze a repo's branches and return cleanup candidates."""
    branches = get_branches(gitea_url, owner, repo, token)
    if branches is None:
        return None

    protected_names = {"main", "master"}
    protected = []
    candidates = []
    recent = []

    for b in branches:
        name = b.get("name", "")
        is_protected = b.get("protected", False) or name in protected_names
        created = None
        updated = None

        if "created" in b:
            try:
                created = datetime.datetime.fromisoformat(b["created"].replace("Z", "+00:00"))
            except (ValueError, TypeError):
                pass
        if "updated" in b:
            try:
                updated = datetime.datetime.fromisoformat(b["updated"].replace("Z", "+00:00"))
            except (ValueError, TypeError):
                pass

        ref_time = updated or created
        is_stale = ref_time is not None and ref_time < cutoff

        entry = {
            "name": name,
            "protected": is_protected,
            "stale": is_stale,
            "last_activity": (updated or created).isoformat() if (updated or created) else None,
        }

        if is_protected:
            protected.append(entry)
        elif is_stale:
            candidates.append(entry)
        else:
            recent.append(entry)

    return {
        "repo": f"{owner}/{repo}",
        "total": len(branches),
        "protected": protected,
        "candidates": candidates,
        "recent": recent,
    }


def main():
    args = parse_args()
    token = None
    if "GITEA_TOKEN" in __import__("os").environ:
        import os
        token = os.environ.get("GITEA_TOKEN")
    elif "GITEA_TOKEN" in dir():
        token = GITEA_TOKEN

    repos = [r.strip() for r in args.repos.split(",") if r.strip()]
    cutoff = datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(days=args.max_age_days)
    results = []

    for repo_path in repos:
        parts = repo_path.split("/", 1)
        if len(parts) != 2:
            print(f"Invalid repo format: {repo_path} (expected owner/name)", file=sys.stderr)
            continue
        owner, repo = parts
        result = analyze_repo(args.gitea_url, owner, repo, token, args.max_age_days, cutoff)
        if result:
            results.append(result)

    if args.format == "json":
        output = {
            "max_age_days": args.max_age_days,
            "cutoff": cutoff.isoformat(),
            "execute": args.execute,
            "repos": results,
        }
        print(json.dumps(output, indent=2))
        return

    # Text output
    for r in results:
        print(f"\n=== {r['repo']} ===")
        print(f"  Total branches : {r['total']}")
        print(f"  Protected      : {len(r['protected'])}")
        print(f"  Cleanup cand.  : {len(r['candidates'])} ({args.max_age_days}+ days stale)")
        print(f"  Recent         : {len(r['recent'])}")

        if r["candidates"]:
            print("  Candidates:")
            for b in r["candidates"]:
                action = "DELETE" if args.execute else "would delete"
                print(f"    [{action}] {b['name']} (last: {b['last_activity']})")
                if args.execute:
                    ok, err = delete_branch(args.gitea_url, owner, repo, b["name"], token)
                    print(f"      -> {'Deleted' if ok else f'FAILED: {err}'}")

    if not args.execute:
        print("\n[DRY-RUN] No branches were deleted. Use --execute to delete.")


if __name__ == "__main__":
    main()
