#!/usr/bin/env python3
"""
Per-agent API cost estimator.

Queries the Gateway /api/v1/agents endpoint, maps each agent's model to
a cost-per-token estimate, and prints a summary table.
"""
import json
import sys
import urllib.request
import urllib.error


# ---------------------------------------------------------------------------
# Cost model (per 1K input tokens / 1K output tokens)
# ---------------------------------------------------------------------------
COSTS = {
    "gpt-4o":          (0.005,   0.015),
    "gpt-4o-mini":     (0.00015, 0.0006),
    "gpt-4-turbo":     (0.010,   0.030),
    "gpt-3.5-turbo":   (0.0005,  0.0015),
    "claude-3-5-sonnet": (0.003,  0.015),
    "claude-3-opus":   (0.015,   0.075),
    "claude-3-haiku":  (0.00025, 0.00125),
    "gemini-1.5-flash": (0.000075, 0.0003),
    "gemini-1.5-pro":  (0.00125, 0.005),
    "deepseek-chat":   (0.0001,  0.0003),
    "local":           (0.0,     0.0),   # no API cost
    "local-llama":     (0.0,     0.0),
    "valhalla-llm":    (0.0,     0.0),
    "codex":           (0.0,     0.0),   # bundled / flat-rate
}

DEFAULT_COST = (0.010, 0.030)   # fallback: treat as GPT-4 if unknown


def fetch_agents(gateway_url: str) -> list:
    url = f"{gateway_url.rstrip('/')}/api/v1/agents"
    try:
        with urllib.request.urlopen(url, timeout=10) as resp:
            data = json.loads(resp.read())
    except urllib.error.URLError as exc:
        print(f"ERROR: could not reach Gateway at {url}: {exc}", file=sys.stderr)
        sys.exit(1)
    if isinstance(data, dict) and "agents" in data:
        return data["agents"]
    return data if isinstance(data, list) else []


def estimate_cost(model: str, input_tokens: int = 100_000, output_tokens: int = 50_000):
    """Return estimated cost for a default benchmark run (100K in / 50K out)."""
    key = model.lower()
    for known, cost_pair in COSTS.items():
        if known in key:
            inp, out = cost_pair
            break
    else:
        inp, out = DEFAULT_COST

    return (inp * input_tokens / 1000) + (out * output_tokens / 1000)


def print_table(agents: list):
    header = f"{'Agent':<20} {'Model':<25} {'Est. Cost / 100K tokens':>22}"
    sep = "-" * len(header)
    print(header)
    print(sep)

    total = 0.0
    for ag in sorted(agents, key=lambda x: x.get("name", "")):
        name  = ag.get("name", "unknown")
        model = ag.get("model", ag.get("llm_model", "unknown"))
        cost  = estimate_cost(model)
        total += cost
        flag  = "  ⚠ unknown model" if cost == 0.0 else ""
        print(f"{name:<20} {model:<25} ${cost:>21.4f}{flag}")

    print(sep)
    print(f"{'(Totals are for a 100K input / 50K output benchmark run)':^67}")
    print(f"{'Total estimated':<45} ${total:>21.4f}")


def main():
    gateway_url = sys.argv[1] if len(sys.argv) > 1 else "http://gateway.valhalla.svc:8080"
    agents = fetch_agents(gateway_url)
    if not agents:
        print("WARNING: no agents returned from Gateway.", file=sys.stderr)
        sys.exit(0)
    print_table(agents)


if __name__ == "__main__":
    main()
