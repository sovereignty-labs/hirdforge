#!/usr/bin/env python3
import argparse
import json
import statistics
import sys
import threading
import time
import urllib.request


def parse_args():
    p = argparse.ArgumentParser(description="Load test the gateway message endpoint")
    p.add_argument("--gateway-url", required=True)
    p.add_argument("--agent", required=True)
    p.add_argument("--concurrency", type=int, default=1)
    p.add_argument("--requests", type=int, default=1)
    p.add_argument("--message", required=True)
    args = p.parse_args()
    if args.concurrency < 1 or args.requests < 1:
        p.error("--concurrency and --requests must be >= 1")
    return args


def percentile(values, pct):
    if not values:
        return 0.0
    ordered = sorted(values)
    index = min(len(ordered) - 1, max(0, int((pct / 100.0) * len(ordered) + 0.5) - 1))
    return ordered[index]


def worker(base_url, agent, message, jobs, results, lock):
    while True:
        with lock:
            if not jobs:
                return
            req_id = jobs.pop(0)
        payload = json.dumps({"agent": agent, "message": message}).encode()
        req = urllib.request.Request(
            base_url.rstrip("/") + "/api/v1/message",
            data=payload,
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        start = time.time()
        ok = False
        detail = ""
        try:
            with urllib.request.urlopen(req, timeout=30) as resp:
                body = resp.read().decode("utf-8", errors="replace")
                ok = 200 <= resp.status < 300
                detail = body.strip() or "empty response"
        except Exception as exc:
            detail = str(exc)
        elapsed = (time.time() - start) * 1000.0
        with lock:
            results.append({"id": req_id, "ok": ok, "ms": elapsed, "detail": detail})


def summarize(results):
    times = [r["ms"] for r in results]
    successes = sum(1 for r in results if r["ok"])
    count = len(results)
    print("\nSummary")
    print(f"total: {count}")
    print(f"successes: {successes}")
    print(f"failures: {count - successes}")
    print(f"success_rate: {(successes / count) * 100:.2f}%")
    print(f"avg_ms: {statistics.mean(times):.2f}")
    print(f"p50_ms: {percentile(times, 50):.2f}")
    print(f"p95_ms: {percentile(times, 95):.2f}")
    print(f"max_ms: {max(times):.2f}")


def main():
    args = parse_args()
    jobs = list(range(1, args.requests + 1))
    results = []
    lock = threading.Lock()
    threads = []
    for _ in range(min(args.concurrency, args.requests)):
        t = threading.Thread(
            target=worker,
            args=(args.gateway_url, args.agent, args.message, jobs, results, lock),
        )
        t.start()
        threads.append(t)
    for t in threads:
        t.join()
    results.sort(key=lambda r: r["id"])
    for result in results:
        status = "OK" if result["ok"] else "FAIL"
        print(f"[{result['id']}] {status} {result['ms']:.2f}ms {result['detail']}")
    if not results:
        print("No requests executed", file=sys.stderr)
        return 1
    summarize(results)
    return 0 if all(r["ok"] for r in results) else 1


if __name__ == "__main__":
    sys.exit(main())
