#!/usr/bin/env python3
"""
autoresearch.py — Automated skill improvement orchestration.

Dispatches a builder agent to produce a test PR, scores it against the
PR workflow checklist, logs results, and either stops (95% pass rate
three rounds in a row) or dispatches the builder to fix the worst
failing skill file.

Usage:
    python3 scripts/autoresearch.py \
        --gateway-url http://gateway.valhalla.svc:8080 \
        --builder val \
        --scorer freya \
        --max-rounds 10
"""

import argparse
import json
import logging
import re
import sys
import time
import urllib.error
import urllib.request
from dataclasses import dataclass, field
from datetime import datetime, timezone
from typing import Optional

# ---------------------------------------------------------------------------
# Logging
# ---------------------------------------------------------------------------

log = logging.getLogger("autoresearch")
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s  %(levelname)-8s  %(message)s",
    datefmt="%Y-%m-%dT%H:%M:%S",
)


# ---------------------------------------------------------------------------
# Dataclasses
# ---------------------------------------------------------------------------

@dataclass
class RoundResult:
    round_num: int
    pr_url: Optional[str]
    pr_number: Optional[int]
    repo: Optional[str]
    score: Optional[dict]
    pass_rate: float
    worst_item: Optional[str]
    build_time_ms: float
    score_time_ms: float
    error: Optional[str] = None


@dataclass
class AutoresearchState:
    results: list = field(default_factory=list)
    consecutive_high_scores: int = 0
    stop_reason: Optional[str] = None

    def last_pass_rate(self) -> float:
        if not self.results:
            return 0.0
        return self.results[-1].pass_rate

    def record(self, result: RoundResult):
        self.results.append(result)
        if result.pass_rate >= 0.95:
            self.consecutive_high_scores += 1
        else:
            self.consecutive_high_scores = 0


# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------

def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser(
        description="Automated skill improvement orchestration",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    p.add_argument(
        "--gateway-url",
        default="http://gateway.valhalla.svc:8080",
        help="Base URL of the Valhalla Gateway (default: http://gateway.valhalla.svc:8080)",
    )
    p.add_argument(
        "--builder",
        required=True,
        help="Agent name to dispatch as the builder (e.g. val, chuck, leif)",
    )
    p.add_argument(
        "--scorer",
        required=True,
        help="Agent name to dispatch as the scorer (e.g. freya)",
    )
    p.add_argument(
        "--max-rounds",
        type=int,
        default=10,
        help="Maximum number of rounds to run before giving up (default: 10)",
    )
    p.add_argument(
        "--target-repo",
        default="kit/hirdforge-personas",
        help="Repo the builder targets for skill-file fixes (default: kit/hirdforge-personas)",
    )
    p.add_argument(
        "--pass-threshold",
        type=float,
        default=0.95,
        help="Pass rate threshold for early-stop (default: 0.95)",
    )
    p.add_argument(
        "--consecutive-wins",
        type=int,
        default=3,
        help="Consecutive rounds above pass-threshold needed to stop (default: 3)",
    )
    return p.parse_args()


# ---------------------------------------------------------------------------
# Gateway — SSE message dispatch
# ---------------------------------------------------------------------------

def _build_message_payload(agent: str, content: str, session_id: str) -> dict:
    return {"agent": agent, "content": content, "session_id": session_id}


def _sse_events(data_bytes: bytes) -> list:
    """
    Parse SSE data bytes into a list of event dictionaries.
    Yields one dict per SSE block: {'event': str, 'data': str}
    """
    events = []
    for line in data_bytes.decode("utf-8", errors="replace").splitlines():
        if line.startswith("event:"):
            events.append({"event": line[6:].strip(), "data": ""})
        elif line.startswith("data:"):
            if events:
                events[-1]["data"] += line[5:].strip()
        elif line.strip() == "":
            # blank line — flush current event
            pass
    return events


def _extract_pr_url_from_sse(events: list) -> Optional[str]:
    """
    Pull a PR URL out of SSE event data.
    Looks for the first JSON payload that contains an html_url or web_url
    field pointing to a pull request.
    """
    for ev in events:
        raw = ev.get("data", "")
        if not raw:
            continue
        # Skip keep-alive newlines
        if raw.strip() == "":
            continue
        try:
            payload = json.loads(raw)
        except json.JSONDecodeError:
            continue
        # payload may be wrapped (e.g. {"response": "...", ...})
        for val in (payload, *(payload.get(k) for k in payload if isinstance(payload.get(k), dict))):
            if isinstance(val, dict):
                url = val.get("html_url") or val.get("web_url") or val.get("url")
                if url and "/pull/" in url:
                    return url
        # Some gateways return a plain string with the PR URL
        if isinstance(payload, str) and "/pull/" in payload:
            return payload
    return None


def dispatch_message(gateway_url: str, agent: str, content: str, session_id: str) -> tuple[Optional[str], Optional[str]]:
    """
    POST a message to the gateway via SSE and return (pr_url, error).

    The gateway SSE stream may contain multiple events. We accumulate all
    data chunks until the stream closes, then scan for the first PR URL.
    """
    url = gateway_url.rstrip("/") + "/api/v1/message"
    body = json.dumps(_build_message_payload(agent, content, session_id)).encode()
    req = urllib.request.Request(
        url,
        data=body,
        headers={"Content-Type": "application/json", "Accept": "text/event-stream"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            raw = resp.read()
    except urllib.error.HTTPError as exc:
        return None, f"HTTP {exc.code}: {exc.reason}"
    except urllib.error.URLError as exc:
        return None, f"connection error: {exc.reason}"

    events = _sse_events(raw)
    if not events:
        return None, "empty SSE stream"
    pr_url = _extract_pr_url_from_sse(events)
    if not pr_url:
        # Return first event data as diagnostic
        first = next((e["data"] for e in events if e["data"]), "")
        return None, f"no PR URL found in SSE; first event: {first[:200]}"
    return pr_url, None


# ---------------------------------------------------------------------------
# Gitea helpers
# ---------------------------------------------------------------------------

def _gitea_get(url: str, token: str) -> dict:
    req = urllib.request.Request(
        url,
        headers={"Authorization": f"token {token}", "Accept": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.loads(resp.read().decode("utf-8"))


def fetch_pr_details(pr_url: str, token: str) -> tuple[Optional[int], Optional[str]]:
    """
    Parse owner/repo and PR number from a PR URL, then return (number, repo).
    """
    # pr_url examples:
    # https://gitea.example.com/owner/repo/pulls/123
    # http://gitea-http.gitea.svc.cluster.local:3000/owner/repo/pulls/123
    m = re.search(r"/([^/]+/[^/]+)/pulls?/(\d+)", pr_url)
    if not m:
        return None, None
    repo = m.group(1)
    number = int(m.group(2))
    return number, repo


# ---------------------------------------------------------------------------
# Scoring prompt (inlined from shared/skills/scoring-prompt.md)
# ---------------------------------------------------------------------------

SCORING_PROMPT_TEMPLATE = """\
You are a PR workflow reviewer. Your task is to evaluate a pull request against the PR workflow scoring checklist and return a JSON score.

## Your Task

1. Read the target PR using the Gitea API:
   - Base URL: `http://gitea-http.gitea.svc.cluster.local:3000/api/v1`
   - Auth header: `Authorization: token $GITEA_TOKEN`
   - Get PR details: `GET /repos/{{owner}}/{{repo}}/pulls/{{pr_number}}`
   - Get the diff: `GET /repos/{{owner}}/{{repo}}/pulls/{{pr_number}}.diff`
   - Get commit list: `GET /repos/{{owner}}/{{repo}}/pulls/{{pr_number}}/commits`

2. Evaluate each checklist item for the PR:

   Checklist items:
   - branch_first:       Did the agent create a feature branch BEFORE editing files?
   - clear_title:        Is the PR title descriptive (e.g., "feat: add X") and not vague?
   - verified_build:     Did the agent run a validation step (compile/lint/syntax check) before committing?
   - reviewed_diff:      Did the agent run `git diff` or equivalent to review changes before pushing?
   - good_commit_msg:    Is the commit message descriptive and action-oriented, not generic?

3. Return a JSON object with the following shape:

{{{{
  "items": {{
    "branch_first": true|false,
    "clear_title": true|false,
    "verified_build": true|false,
    "reviewed_diff": true|false,
    "good_commit_msg": true|false
  }}}},
  "pass_rate": 0.0-1.0,
  "summary": "Brief human-readable summary of the evaluation"
}}}

## Scoring Rules

- Each item is boolean: true (pass) or false (fail).
- pass_rate = count of true items / 5
- A score >= 0.8 (4/5 items) is a PASS.
- Include a short `summary` explaining the overall assessment and any notable failures.

## Context for this review

- **PR number:** {pr_number}
- **Repo:** {repo}
- **Agent session log (if available):** N/A

## Instructions

1. Use `exec` + `curl` to fetch the PR data from Gitea API.
2. Inspect the diff, PR title, and commit messages.
3. Score each checklist item and compute the pass rate.
4. Return the JSON result.

Do NOT approve or merge the PR. Only evaluate and return the score.
"""


def build_scorer_message(pr_url: str, pr_number: int, repo: str) -> str:
    return SCORING_PROMPT_TEMPLATE.format(pr_number=pr_number, repo=repo).replace(
        "{{", "{"
    ).replace("}}", "}")


# ---------------------------------------------------------------------------
# Score parsing
# ---------------------------------------------------------------------------

def parse_score_from_response(response_text: str) -> Optional[dict]:
    """
    Extract JSON score object from an SSE response string that may contain
    additional SSE framing or surrounding text.
    """
    # Find the first {...} block that contains a pass_rate field
    # We try json.loads on the whole string first, then fall back to regex
    try:
        obj = json.loads(response_text)
        if "pass_rate" in obj:
            return obj
    except json.JSONDecodeError:
        pass

    # Search for a JSON object containing pass_rate
    for m in re.finditer(r'\{[^{}]*"pass_rate"[^{}]*\}', response_text):
        try:
            return json.loads(m.group())
        except json.JSONDecodeError:
            continue
    return None


# ---------------------------------------------------------------------------
# Round execution
# ---------------------------------------------------------------------------

def run_builder_round(
    gateway_url: str,
    builder: str,
    round_num: int,
    fix_item: Optional[str] = None,
    target_repo: str = "kit/hirdforge-personas",
) -> tuple[Optional[str], Optional[str], Optional[str]]:
    """
    Dispatch the builder for one round.

    Returns (pr_url, error, fix_instruction).
    fix_instruction is a string describing what the builder should fix
    (extracted from the scorer feedback), or None for a clean test run.
    """
    session_id = f"autoresearch-round-{round_num}"
    if fix_item:
        content = (
            f"You are running autoresearch round {round_num}. "
            f"The scorer identified a failing checklist item: '{fix_item}'. "
            f"Read the relevant skill file(s) in shared/skills/ that govern this item, "
            f"identify what guidance is missing or unclear, and update the skill file "
            f"with improved instructions. Then commit your changes and open a PR on "
            f"{target_repo}. Return the PR URL."
        )
    else:
        content = (
            f"You are running autoresearch round {round_num}. "
            f"Produce a small, realistic code change on {target_repo}: "
            f"fix a typo or clarify a comment in any skill file. "
            f"Follow the full PR workflow: branch first, validate, review diff, commit, push, PR. "
            f"Return the PR URL."
        )

    log.info("Dispatching builder=%s session=%s fix_item=%s", builder, session_id, fix_item)
    pr_url, error = dispatch_message(gateway_url, builder, content, session_id)
    return pr_url, error, content


def run_scorer_round(
    gateway_url: str,
    scorer: str,
    pr_url: str,
    pr_number: int,
    repo: str,
    round_num: int,
) -> tuple[Optional[dict], Optional[str]]:
    """
    Dispatch the scorer to evaluate a PR and return (score_dict, error).
    """
    session_id = f"autoresearch-score-{round_num}"
    content = build_scorer_message(pr_url, pr_number, repo)
    log.info("Dispatching scorer=%s session=%s pr=%s", scorer, session_id, pr_url)

    # The scorer returns an SSE stream; parse it for the JSON score
    raw, error = dispatch_message(gateway_url, scorer, content, session_id)
    if error:
        return None, error

    score = parse_score_from_response(raw)
    if not score:
        return None, f"scorer response did not contain valid score JSON: {raw[:500]}"
    return score, None


# ---------------------------------------------------------------------------
# Report
# ---------------------------------------------------------------------------

def print_summary(state: AutoresearchState, args: argparse.Namespace):
    print("\n" + "=" * 70)
    print("AUTORESEARCH SUMMARY")
    print("=" * 70)
    print(f"Gateway : {args.gateway_url}")
    print(f"Builder : {args.builder}")
    print(f"Scorer  : {args.scorer}")
    print(f"Max rounds: {args.max_rounds}")
    print(f"Pass threshold: {args.pass_threshold}  x {args.consecutive_wins} consecutive → stop")
    print()
    print(f"{'#':>3}  {'PR':<40}  {'Score':>6}  {'Worst item':<20}  {'Error'}")
    print("-" * 70)
    for r in state.results:
        pr = r.pr_url or r.error or "—"
        if len(pr) > 40:
            pr = pr[:37] + "..."
        score_str = f"{r.pass_rate*100:.0f}%" if r.score is not None else "N/A"
        worst = r.worst_item or "—"
        err = "✗" if r.error else "✓"
        print(f"{r.round_num:>3}  {pr:<40}  {score_str:>6}  {worst:<20}  {err}")
    print()
    if state.stop_reason:
        print(f"STOPPED: {state.stop_reason}")
    else:
        print("Completed all rounds without meeting stop condition.")


# ---------------------------------------------------------------------------
# Main loop
# ---------------------------------------------------------------------------

def main() -> int:
    args = parse_args()
    state = AutoresearchState()

    for round_num in range(1, args.max_rounds + 1):
        log.info("===== Round %d / %d =====", round_num, args.max_rounds)
        t0 = time.monotonic()

        # Determine what the builder should do
        fix_item: Optional[str] = None
        if len(state.results) >= 1:
            prev = state.results[-1]
            if prev.score and prev.worst_item:
                fix_item = prev.worst_item
                log.info("Previous worst item: %s — builder will fix skill", fix_item)

        # Build a clean test prompt or a fix prompt
        pr_url, error, _ = run_builder_round(
            args.gateway_url,
            args.builder,
            round_num,
            fix_item=fix_item,
            target_repo=args.target_repo,
        )
        build_ms = (time.monotonic() - t0) * 1000

        if error:
            log.error("Builder round failed: %s", error)
            result = RoundResult(
                round_num=round_num,
                pr_url=None,
                pr_number=None,
                repo=None,
                score=None,
                pass_rate=0.0,
                worst_item=None,
                build_time_ms=build_ms,
                score_time_ms=0.0,
                error=error,
            )
            state.record(result)
            continue

        # Extract PR number and repo from URL
        pr_number, repo = fetch_pr_details(pr_url)
        if not pr_number or not repo:
            log.error("Could not parse PR URL: %s", pr_url)
            result = RoundResult(
                round_num=round_num,
                pr_url=pr_url,
                pr_number=None,
                repo=None,
                score=None,
                pass_rate=0.0,
                worst_item=None,
                build_time_ms=build_ms,
                score_time_ms=0.0,
                error=f"unparseable PR URL: {pr_url}",
            )
            state.record(result)
            continue

        # Score the PR
        t1 = time.monotonic()
        score, score_err = run_scorer_round(
            args.gateway_url,
            args.scorer,
            pr_url,
            pr_number,
            repo,
            round_num,
        )
        score_ms = (time.monotonic() - t1) * 1000

        if score_err:
            log.error("Scorer round failed: %s", score_err)
            result = RoundResult(
                round_num=round_num,
                pr_url=pr_url,
                pr_number=pr_number,
                repo=repo,
                score=None,
                pass_rate=0.0,
                worst_item=None,
                build_time_ms=build_ms,
                score_time_ms=score_ms,
                error=score_err,
            )
            state.record(result)
            continue

        pass_rate = score.get("pass_rate", 0.0)
        items = score.get("items", {})
        # Identify worst item: first failing item by checklist order
        worst = None
        for key in ("branch_first", "clear_title", "verified_build", "reviewed_diff", "good_commit_msg"):
            if not items.get(key):
                worst = key
                break

        log.info(
            "Round %d result: pass_rate=%.0f%% worst=%s summary=%s",
            round_num,
            pass_rate * 100,
            worst or "none",
            score.get("summary", ""),
        )

        result = RoundResult(
            round_num=round_num,
            pr_url=pr_url,
            pr_number=pr_number,
            repo=repo,
            score=score,
            pass_rate=pass_rate,
            worst_item=worst,
            build_time_ms=build_ms,
            score_time_ms=score_ms,
            error=None,
        )
        state.record(result)

        # Check stop condition
        if state.consecutive_high_scores >= args.consecutive_wins:
            state.stop_reason = (
                f"Pass rate ≥{args.pass_threshold*100:.0f}% for "
                f"{state.consecutive_high_scores} consecutive rounds"
            )
            log.info("STOP CONDITION MET: %s", state.stop_reason)
            break

    print_summary(state, args)

    # Write results log as JSON
    ts = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S")
    log_path = f"autoresearch-{ts}.json"
    with open(log_path, "w") as f:
        json.dump(
            {
                "args": vars(args),
                "stop_reason": state.stop_reason,
                "results": [
                    {
                        "round": r.round_num,
                        "pr_url": r.pr_url,
                        "pr_number": r.pr_number,
                        "repo": r.repo,
                        "pass_rate": r.pass_rate,
                        "worst_item": r.worst_item,
                        "score": r.score,
                        "build_time_ms": round(r.build_time_ms, 1),
                        "score_time_ms": round(r.score_time_ms, 1),
                        "error": r.error,
                    }
                    for r in state.results
                ],
            },
            f,
            indent=2,
        )
    log.info("Results written to %s", log_path)

    return 0


if __name__ == "__main__":
    sys.exit(main())
