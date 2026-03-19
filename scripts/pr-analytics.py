#!/usr/bin/env python3
import argparse
import datetime
import json
import sys
import urllib.request

REPOS = [
    "gitea_admin/project_valhalla",
    "kit/valhalla-infra",
    "kit/hirdforge-personas",
]


def fetch_json(url):
    req = urllib.request.Request(url, headers={"Accept": "application/json"})
    with urllib.request.urlopen(req) as resp:
        return json.loads(resp.read().decode("utf-8"))


def parse_dt(value):
    if not value:
        return None
    return datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))


def fmt_duration(delta):
    total = int(delta.total_seconds())
    days, rem = divmod(total, 86400)
    hours, rem = divmod(rem, 3600)
    minutes, _ = divmod(rem, 60)
    if days:
        return "%dd %dh %dm" % (days, hours, minutes)
    if hours:
        return "%dh %dm" % (hours, minutes)
    return "%dm" % minutes


def fetch_repo_prs(base_url, repo):
    page = 1
    pulls = []
    while True:
        url = "%s/api/v1/repos/%s/pulls?state=all&limit=50&page=%d" % (base_url, repo, page)
        batch = fetch_json(url)
        if not batch:
            break
        pulls.extend(batch)
        if len(batch) < 50:
            break
        page += 1
    return pulls


def analyze_repo(repo, pulls, now):
    counts = {"open": 0, "closed": 0, "merged": 0}
    authors = {}
    merged_durations = []
    longest = []
    recent_merged = 0
    cutoff = now - datetime.timedelta(days=7)

    for pr in pulls:
        state = pr.get("state")
        merged = bool(pr.get("merged_at"))
        created_at = parse_dt(pr.get("created_at"))
        merged_at = parse_dt(pr.get("merged_at"))
        closed_at = parse_dt(pr.get("closed_at"))
        author = ((pr.get("user") or {}).get("login")) or "unknown"

        if state == "open":
            counts["open"] += 1
        elif merged:
            counts["merged"] += 1
        else:
            counts["closed"] += 1

        if author not in authors:
            authors[author] = {"created": 0, "merged": 0}
        authors[author]["created"] += 1
        if merged:
            authors[author]["merged"] += 1

        if created_at and merged_at:
            duration = merged_at - created_at
            merged_durations.append(duration)
            longest.append(
                {
                    "repo": repo,
                    "number": pr.get("number"),
                    "title": pr.get("title"),
                    "author": author,
                    "status": "merged",
                    "duration_seconds": int(duration.total_seconds()),
                    "duration": fmt_duration(duration),
                }
            )
            if merged_at >= cutoff:
                recent_merged += 1
        elif created_at:
            duration = now - created_at
            longest.append(
                {
                    "repo": repo,
                    "number": pr.get("number"),
                    "title": pr.get("title"),
                    "author": author,
                    "status": state or "unknown",
                    "duration_seconds": int(duration.total_seconds()),
                    "duration": fmt_duration(duration),
                }
            )

        if closed_at and not merged and state != "open":
            pass

    avg_merge = None
    if merged_durations:
        total_seconds = sum(int(d.total_seconds()) for d in merged_durations)
        avg_merge = datetime.timedelta(seconds=total_seconds // len(merged_durations))

    velocity = round(float(recent_merged) / 7.0, 2)
    return {
        "repo": repo,
        "counts": counts,
        "authors": authors,
        "average_time_to_merge": fmt_duration(avg_merge) if avg_merge else None,
        "average_time_to_merge_seconds": int(avg_merge.total_seconds()) if avg_merge else None,
        "longest": longest,
        "velocity_7d": velocity,
        "errors": [],
    }


def build_report(base_url):
    now = datetime.datetime.now(datetime.timezone.utc)
    report = {
        "generated_at": now.isoformat(),
        "repos": {},
        "errors": [],
        "top_longest_open": [],
        "overall_velocity_7d": 0.0,
    }
    velocities = []
    longest = []

    for repo in REPOS:
        try:
            pulls = fetch_repo_prs(base_url.rstrip("/"), repo)
            repo_report = analyze_repo(repo, pulls, now)
            report["repos"][repo] = repo_report
            velocities.append(repo_report["velocity_7d"])
            longest.extend(repo_report["longest"])
        except Exception as exc:
            message = "%s: %s" % (repo, exc)
            report["errors"].append(message)
            report["repos"][repo] = {"repo": repo, "error": message}

    longest.sort(key=lambda item: item["duration_seconds"], reverse=True)
    report["top_longest_open"] = longest[:5]
    report["overall_velocity_7d"] = round(sum(velocities), 2)
    return report


def print_text(report):
    print("PR Delivery Analytics")
    print("Generated: %s" % report["generated_at"])
    if report["errors"]:
        print("Errors:")
        for err in report["errors"]:
            print("  - %s" % err)
    for repo in REPOS:
        data = report["repos"].get(repo, {})
        print("\nRepo: %s" % repo)
        if data.get("error"):
            print("  Error: %s" % data["error"])
            continue
        counts = data["counts"]
        print("  Totals: open=%d closed=%d merged=%d" % (counts["open"], counts["closed"], counts["merged"]))
        print("  Avg time-to-merge: %s" % (data["average_time_to_merge"] or "n/a"))
        print("  PR velocity (7d): %.2f merged/day" % data["velocity_7d"])
        print("  Authors:")
        for author in sorted(data["authors"]):
            stats = data["authors"][author]
            rate = 0.0
            if stats["created"]:
                rate = (float(stats["merged"]) / float(stats["created"])) * 100.0
            print(
                "    - %s: created=%d merged=%d merge_rate=%.1f%%"
                % (author, stats["created"], stats["merged"], rate)
            )
    print("\nTop 5 longest-open PRs:")
    for item in report["top_longest_open"]:
        print(
            "  - %s#%s [%s] %s by %s (%s)"
            % (item["repo"], item["number"], item["status"], item["title"], item["author"], item["duration"])
        )
    print("\nOverall velocity (7d): %.2f merged/day" % report["overall_velocity_7d"])


def main():
    parser = argparse.ArgumentParser(description="Analyze PR delivery performance from Gitea")
    parser.add_argument("--gitea-url", default="http://203.0.113.26:30637")
    parser.add_argument("--format", choices=["text", "json"], default="text")
    args = parser.parse_args()

    try:
        report = build_report(args.gitea_url)
    except Exception as exc:
        print("fatal: %s" % exc, file=sys.stderr)
        return 1

    if args.format == "json":
        print(json.dumps(report, indent=2, sort_keys=True))
    else:
        print_text(report)
    return 0


if __name__ == "__main__":
    sys.exit(main())
