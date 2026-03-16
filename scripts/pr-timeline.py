#!/usr/bin/env python3
"""PR timeline visualization tool for Gitea.

Queries one or more Gitea repos for closed/merged PRs and produces:
- Text report grouped by day
- Standalone HTML file with inline SVG bar chart

Stdlib only, suitable for running from CI or a dev shell.
"""

import argparse
import collections
import datetime as dt
import html
import json
import sys
import urllib.parse
import urllib.request
from typing import Dict, List, Tuple


def fetch_all_prs(base_url: str, repo: str) -> List[dict]:
    """Fetch all closed PRs for a repo, handling simple page-based pagination.

    This uses Gitea's `/repos/{owner}/{repo}/pulls` endpoint.
    We request `state=closed` and collect until a page returns no items.
    """

    prs: List[dict] = []
    page = 1
    per_page = 50

    while True:
        path = f"/api/v1/repos/{repo}/pulls?state=closed&sort=recentupdate&direction=desc&limit={per_page}&page={page}"
        url = urllib.parse.urljoin(base_url.rstrip("/"), path)
        try:
            with urllib.request.urlopen(url) as resp:  # nosec - internal trusted URL
                data = json.load(resp)
        except Exception as exc:  # pragma: no cover - runtime safeguard
            print(f"error: failed to fetch PRs for {repo} page {page}: {exc}", file=sys.stderr)
            break

        if not data:
            break

        prs.extend(data)

        # Stop when fewer than requested items are returned (no more pages).
        if len(data) < per_page:
            break

        page += 1

    return prs


def group_prs_by_day(prs_by_repo: Dict[str, List[dict]]) -> Tuple[Dict[str, int], Dict[str, Dict[str, int]]]:
    """Group PRs by merged/closed date (YYYY-MM-DD).

    Returns:
        total_by_day: {date: total_pr_count}
        by_repo_by_day: {date: {repo: count}}
    """

    total_by_day: Dict[str, int] = collections.Counter()
    by_repo_by_day: Dict[str, Dict[str, int]] = {}

    for repo, prs in prs_by_repo.items():
        for pr in prs:
            # Prefer merged_at when available; fall back to closed_at.
            ts = pr.get("merged_at") or pr.get("closed_at")
            if not ts:
                continue
            try:
                # Gitea timestamps are RFC3339; split at 'T' for date.
                day = ts.split("T", 1)[0]
            except Exception:
                continue

            total_by_day[day] += 1
            if day not in by_repo_by_day:
                by_repo_by_day[day] = {}
            by_repo_by_day[day][repo] = by_repo_by_day[day].get(repo, 0) + 1

    return dict(total_by_day), by_repo_by_day


def print_text_report(total_by_day: Dict[str, int]) -> None:
    """Print text timeline to stdout, newest first.

    Example line: `2026-03-15: █████ 5 PRs`
    """

    if not total_by_day:
        print("No PRs found.")
        return

    # Sort by date descending
    for day in sorted(total_by_day.keys(), reverse=True):
        count = total_by_day[day]
        bar = "".join("█" for _ in range(min(count, 40)))  # cap width for terminal
        print(f"{day}: {bar} {count} PRs")


def build_html(
    total_by_day: Dict[str, int],
    by_repo_by_day: Dict[str, Dict[str, int]],
    repos: List[str],
    output_path: str,
) -> None:
    """Generate standalone HTML file with inline SVG bar chart.

    Each day is a horizontal stacked bar, colored by repo.
    """

    days = sorted(total_by_day.keys())
    if not days:
        html_content = """<!DOCTYPE html>
<html>
<head><meta charset=\"utf-8\"><title>Hirdforge Development Timeline</title></head>
<body><h1>Hirdforge Development Timeline</h1><p>No PRs found.</p></body></html>"""
        with open(output_path, "w", encoding="utf-8") as f:
            f.write(html_content)
        return

    max_count = max(total_by_day.values())
    total_prs = sum(total_by_day.values())

    width = 800
    bar_height = 18
    gap = 6
    height = len(days) * (bar_height + gap) + 40

    # Simple color palette per repo (repeat if more repos)
    palette = [
        "#1f77b4",
        "#ff7f0e",
        "#2ca02c",
        "#d62728",
        "#9467bd",
        "#8c564b",
        "#e377c2",
    ]
    repo_colors: Dict[str, str] = {}
    for idx, repo in enumerate(repos):
        repo_colors[repo] = palette[idx % len(palette)]

    # Build SVG rows
    svg_rows: List[str] = []
    y = 30
    for day in days:
        count = total_by_day[day]
        if count <= 0:
            y += bar_height + gap
            continue
        x = 0
        day_data = by_repo_by_day.get(day, {})
        for repo in repos:
            part = day_data.get(repo, 0)
            if part <= 0:
                continue
            segment_width = int(width * (part / max_count))
            if segment_width <= 0:
                continue
            label = f"{day} — {repo}: {part} PRs"
            svg_rows.append(
                f'<g><title>{html.escape(label)}</title>'
                f'<rect x="{x}" y="{y}" width="{segment_width}" height="{bar_height}" '
                f'fill="{repo_colors[repo]}" /></g>'
            )
            x += segment_width

        # Day label on the left
        svg_rows.append(
            f'<text x="0" y="{y - 4}" font-size="10" fill="#333">{html.escape(day)}</text>'
        )
        y += bar_height + gap

    # Legend
    legend_items = []
    lx = 10
    ly = height - 15
    for repo in repos:
        color = repo_colors[repo]
        esc_repo = html.escape(repo)
        legend_items.append(
            f'<rect x="{lx}" y="{ly - 10}" width="10" height="10" fill="{color}" />'
            f'<text x="{lx + 15}" y="{ly}" font-size="11" fill="#333">{esc_repo}</text>'
        )
        lx += 180

    svg_content = "".join(svg_rows + legend_items)

    title = "Hirdforge Development Timeline"
    html_doc = f"""<!DOCTYPE html>
<html>
<head>
  <meta charset=\"utf-8\" />
  <title>{html.escape(title)}</title>
  <style>
    body {{ font-family: -apple-system, BlinkMacSystemFont, system-ui, sans-serif; margin: 20px; }}
    h1 {{ margin-bottom: 0; }}
    .subtitle {{ color: #555; margin-top: 4px; }}
    svg {{ border: 1px solid #ddd; background: #fafafa; }}
  </style>
</head>
<body>
  <h1>{html.escape(title)}</h1>
  <div class=\"subtitle\">Total PRs: {total_prs}</div>
  <svg width=\"{width}\" height=\"{height}\" viewBox=\"0 0 {width} {height}\" xmlns=\"http://www.w3.org/2000/svg\">
    {svg_content}
  </svg>
</body>
</html>
"""

    with open(output_path, "w", encoding="utf-8") as f:
        f.write(html_doc)


def parse_args(argv: List[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Generate PR timeline from Gitea.")
    parser.add_argument(
        "--gitea-url",
        default="http://203.0.113.26:30637",
        help="Base URL for Gitea (default: %(default)s)",
    )
    parser.add_argument(
        "--repos",
        default="gitea_admin/project_valhalla,kit/valhalla-infra,kit/hirdforge-personas",
        help="Comma-separated list of owner/repo names",
    )
    parser.add_argument(
        "--output",
        default="pr-timeline.html",
        help="Output HTML file path (default: %(default)s)",
    )
    return parser.parse_args(argv)


def main(argv: List[str]) -> int:
    args = parse_args(argv)
    repos = [r.strip() for r in args.repos.split(",") if r.strip()]
    if not repos:
        print("error: at least one repo must be specified via --repos", file=sys.stderr)
        return 1

    prs_by_repo: Dict[str, List[dict]] = {}
    for repo in repos:
        prs_by_repo[repo] = fetch_all_prs(args.gitea_url, repo)

    total_by_day, by_repo_by_day = group_prs_by_day(prs_by_repo)
    print_text_report(total_by_day)
    build_html(total_by_day, by_repo_by_day, repos, args.output)
    return 0


if __name__ == "__main__":  # pragma: no cover - CLI entry point
    sys.exit(main(sys.argv[1:]))
