#!/usr/bin/env python3
"""
Task Board Report — hirdforge-tasks analytics

Queries Gitea issues on kit/hirdforge-tasks and generates a board status report:
  - Total issues by status label
  - Issues by agent label
  - Oldest open issues
  - Issues with no agent assigned
"""

import argparse
import datetime
import json
import sys
import urllib.error
import urllib.request

# ── config ────────────────────────────────────────────────────────────────────
GITEA_BASE = "http://gitea-http.gitea.svc.cluster.local:3000"
TASKS_REPO = "kit/hirdforge-tasks"

# ── helpers ────────────────────────────────────────────────────────────────────

def get_token():
    token = ""
    # Support both env-var naming conventions
    for var in ("GITEA_TOKEN", "GITEA_REVIEWERS_TOKEN"):
        val = _env(var)
        if val:
            token = val
            break
    if not token:
        print("ERROR: GITEA_TOKEN or GITEA_REVIEWERS_TOKEN is not set", file=sys.stderr)
        sys.exit(1)
    return token


def _env(name):
    import os
    return os.environ.get(name, "")


def api_url(path):
    return f"{GITEA_BASE}/api/v1{path}"


def fetch(url):
    token = get_token()
    req = urllib.request.Request(
        url,
        headers={
            "Authorization": f"token {token}",
            "Accept": "application/json",
        },
    )
    with urllib.request.urlopen(req, timeout=15) as resp:
        return json.loads(resp.read())


def fetch_all_issues(state="open"):
    """Paginate through all issues, return a flat list."""
    issues = []
    page = 1
    per_page = 50
    while True:
        url = api_url(
            f"/repos/{TASKS_REPO}/issues"
            f"?state={state}&type=issues&page={page}&per_page={per_page}"
        )
        batch = fetch(url)
        if not batch:
            break
        issues.extend(batch)
        if len(batch) < per_page:
            break
        page += 1
    return issues


def label_map(issue):
    """Return a dict of label-name → Label object for an issue."""
    return {lb["name"]: lb for lb in issue.get("labels") or []}


def parse_age(created_str):
    """Parse ISO-8601 created_at string, return datetime (naive UTC)."""
    # Gitea returns e.g. "2024-01-15T10:30:00Z"
    s = created_str.rstrip("Z")
    return datetime.datetime.fromisoformat(s)


# ── formatters ─────────────────────────────────────────────────────────────────

def fmt_age(created_str):
    """Human-readable age string from ISO timestamp."""
    created = parse_age(created_str)
    delta = datetime.datetime.utcnow() - created
    days = delta.days
    if days == 0:
        return "today"
    if days == 1:
        return "1 day"
    return f"{days} days"


def fmt_issue_line(issue, width=72):
    """Single-line summary of an issue suitable for a table."""
    num = issue["number"]
    title = issue["title"][: width - 12]
    age = fmt_age(issue["created_at"])
    return f"  #{num}  {title:<{width-12}}  ({age})"


def section(title):
    print(f"\n{'=' * 60}")
    print(f" {title}")
    print("=" * 60)


# ── report sections ────────────────────────────────────────────────────────────

def report_total_by_status(issues):
    section("Issues by Status Label")
    counts = {}
    for issue in issues:
        lbs = label_map(issue)
        for name in lbs:
            if name.startswith("status/"):
                counts[name] = counts.get(name, 0) + 1
    if not counts:
        print("  (no status labels found)")
    for name in sorted(counts):
        print(f"  {name:<30}  {counts[name]:>4}")


def report_by_agent(issues):
    section("Issues by Agent Label")
    counts = {}
    unassigned = []
    for issue in issues:
        lbs = label_map(issue)
        agent_labels = [n for n in lbs if n.startswith("agent/")]
        if agent_labels:
            for name in agent_labels:
                counts[name] = counts.get(name, 0) + 1
        else:
            unassigned.append(issue)

    if counts:
        for name in sorted(counts):
            print(f"  {name:<30}  {counts[name]:>4}")
    else:
        print("  (no agent labels found)")

    if unassigned:
        print(f"\n  Issues with NO agent assigned ({len(unassigned)}):")
        for issue in sorted(unassigned, key=lambda i: i["created_at"])[:10]:
            print(fmt_issue_line(issue))


def report_oldest(issues, limit=10):
    section(f"Oldest {limit} Open Issues")
    sorted_issues = sorted(issues, key=lambda i: i["created_at"])
    for issue in sorted_issues[:limit]:
        print(fmt_issue_line(issue))


def report_summary(issues):
    section("Summary")
    print(f"  Total open issues :  {len(issues)}")
    lbs_all = set()
    for issue in issues:
        lbs_all.update(label_map(issue))
    status_labels = [n for n in lbs_all if n.startswith("status/")]
    agent_labels  = [n for n in lbs_all if n.startswith("agent/")]
    print(f"  Status labels     :  {len(status_labels)}")
    print(f"  Agent labels      :  {len(agent_labels)}")
    unassigned = [i for i in issues if not any(l.startswith("agent/") for l in label_map(i))]
    print(f"  Unassigned issues :  {len(unassigned)}")


def report_json(issues):
    """Emit structured JSON for machine consumers."""
    out = {
        "total": len(issues),
        "by_status": {},
        "by_agent": {},
        "unassigned": [],
        "oldest": [],
    }
    for issue in issues:
        lbs = label_map(issue)
        for name in lbs:
            if name.startswith("status/"):
                out["by_status"][name] = out["by_status"].get(name, 0) + 1
        agent_labels = [n for n in lbs if n.startswith("agent/")]
        if agent_labels:
            for name in agent_labels:
                out["by_agent"][name] = out["by_agent"].get(name, 0) + 1
        else:
            out["unassigned"].append({"number": issue["number"], "title": issue["title"]})

    sorted_issues = sorted(issues, key=lambda i: i["created_at"])
    out["oldest"] = [
        {"number": i["number"], "title": i["title"], "created_at": i["created_at"]}
        for i in sorted_issues[:10]
    ]
    print(json.dumps(out, indent=2))


# ── CLI ────────────────────────────────────────────────────────────────────────

def parse_args():
    p = argparse.ArgumentParser(description="Task board status report for kit/hirdforge-tasks")
    p.add_argument("--format", choices=["table", "json"], default="table",
                   help="Output format (default: table)")
    p.add_argument("--limit", type=int, default=10,
                   help="How many oldest issues to show (default: 10)")
    p.add_argument("--state", choices=["open", "closed", "all"], default="open",
                   help="Which issue states to query (default: open)")
    return p.parse_args()


def main():
    args = parse_args()

    # Map "all" → fetch both open and closed
    states = ["open", "closed"] if args.state == "all" else [args.state]

    all_issues = []
    for state in states:
        try:
            all_issues.extend(fetch_all_issues(state))
        except urllib.error.HTTPError as err:
            print(f"HTTP {err.code} fetching {state} issues: {err.reason}", file=sys.stderr)
            return 2
        except urllib.error.URLError as err:
            print(f"Connection error: {err.reason}", file=sys.stderr)
            return 2

    if args.format == "json":
        report_json(all_issues)
    else:
        report_summary(all_issues)
        report_total_by_status(all_issues)
        report_by_agent(all_issues)
        report_oldest(all_issues, limit=args.limit)

    return 0


if __name__ == "__main__":
    sys.exit(main())
