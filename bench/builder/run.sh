#!/usr/bin/env bash
# Builder-agent local-model benchmark (BUILDER_HARNESS §5 / D-AUTONOMY #2).
#
# Runs the 14-function pure-helper extraction refactor N times against a
# LiteLLM lane and scores each run MECHANICALLY:
#   clean = tests green + duplication gone + test file untouched + gofmt clean
#
# Usage:  LITELLM_KEY=sk-... ./run.sh <lane> [runs] [litellm_url]
#   lane         LiteLLM model/lane name (e.g. qwen, qwen-reserved)
#   runs         number of rounds (default 10)
#   litellm_url  base URL (default http://localhost:4000)
#
# Results: appends CSV rows to results/<lane>.csv and prints a summary.
set -u

BENCH_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$BENCH_DIR/../.." && pwd)"
LANE="${1:?usage: run.sh <lane> [runs] [litellm_url]}"
RUNS="${2:-10}"
LITELLM_URL="${3:-http://localhost:4000}"
: "${LITELLM_KEY:?set LITELLM_KEY}"

PORT="${BENCH_PORT:-18081}"
MAX_ROUNDS="${BENCH_MAX_ROUNDS:-40}"
TASK_TIMEOUT_SECS="${BENCH_TASK_TIMEOUT:-1800}"
RESULTS_DIR="$BENCH_DIR/results"
mkdir -p "$RESULTS_DIR"
CSV="$RESULTS_DIR/$LANE.csv"
[ -f "$CSV" ] || echo "timestamp,lane,round,task_status,tests_green,dup_gone,testfile_untouched,gofmt_clean,clean,duration_secs" > "$CSV"

command -v go >/dev/null || { echo "go not in PATH"; exit 1; }
AGENT_BIN="$BENCH_DIR/.agent-bin"
echo "building agent binary..."
(cd "$REPO_ROOT" && go build -o "$AGENT_BIN" ./cmd/agent) || exit 1

if ss -tln | grep -qE ":$PORT\b|:$((PORT + 1))\b"; then
  echo "ERROR: port $PORT or $((PORT + 1)) already in use — kill the stray agent first"
  exit 1
fi

TEST_SHA_REF="$(sha256sum "$BENCH_DIR/fixture/metrics_test.go" | cut -d' ' -f1)"

# Pre-flight: the fixture must be in the DUPLICATED "before" state (marker 14x),
# else the refactor is a no-op and every round is a false pass. See run-flow.sh.
FIXTURE_DUP="$(grep -c 'TrimSpace(strings.ToLower' "$BENCH_DIR/fixture/metrics.go")"
if [ "$FIXTURE_DUP" -lt 10 ]; then
  echo "ERROR: fixture/metrics.go has only $FIXTURE_DUP duplicated blocks (expected 14) — the task would be a no-op. Restore the fixture."
  exit 1
fi

CLEAN_COUNT=0

for ROUND in $(seq 1 "$RUNS"); do
  WS="$(mktemp -d /tmp/bench-builder.XXXXXX)"
  cp "$BENCH_DIR/fixture/metrics.go" "$BENCH_DIR/fixture/metrics_test.go" "$WS/"
  printf 'module benchfixture\n\ngo 1.25\n' > "$WS/go.mod"

  "$AGENT_BIN" \
    -port "$PORT" \
    -grpc-port "$((PORT + 1))" \
    -workspace "$WS" \
    -inference-url "$LITELLM_URL" \
    -api-key "$LITELLM_KEY" \
    -model "$LANE" \
    -model-template qwen \
    -tools exec,read,write,edit \
    -soul "$BENCH_DIR/soul.md" \
    -agent-name bench-builder \
    -max-tool-rounds "$MAX_ROUNDS" \
    -inference-timeout 300 \
    >"$WS/agent.log" 2>&1 &
  AGENT_PID=$!

  for _ in $(seq 1 30); do
    curl -sf -m 2 "http://localhost:$PORT/health" >/dev/null && break
    sleep 1
  done

  START=$(date +%s)
  TASK_ID=$(curl -sf -m 10 -X POST "http://localhost:$PORT/tasks/send" \
    -H 'Content-Type: application/json' \
    -d "$(python3 -c "import json;print(json.dumps({'content':open('$BENCH_DIR/task.md').read(),'from':'bench'}))")" \
    | python3 -c "import json,sys;print(json.load(sys.stdin).get('id',''))")

  STATUS="dispatch_failed"
  if [ -n "$TASK_ID" ]; then
    DEADLINE=$((START + TASK_TIMEOUT_SECS))
    while [ "$(date +%s)" -lt "$DEADLINE" ]; do
      STATUS=$(curl -sf -m 10 "http://localhost:$PORT/tasks/$TASK_ID" \
        | python3 -c "import json,sys;print(json.load(sys.stdin).get('status','poll_error'))" 2>/dev/null || echo poll_error)
      case "$STATUS" in completed|failed) break;; esac
      sleep 10
    done
    [ "$STATUS" = "completed" ] || [ "$STATUS" = "failed" ] || STATUS="timeout"
  fi
  DUR=$(( $(date +%s) - START ))
  kill "$AGENT_PID" 2>/dev/null; wait "$AGENT_PID" 2>/dev/null

  # Mechanical scoring — the agent's opinion of its work is not consulted.
  TESTS=0; (cd "$WS" && go test ./... >/dev/null 2>&1) && TESTS=1
  DUP_COUNT=$(grep -c 'TrimSpace(strings.ToLower' "$WS/metrics.go" 2>/dev/null || echo 99)
  DUP=0; [ "$DUP_COUNT" -le 1 ] && DUP=1
  UNTOUCHED=0; [ "$(sha256sum "$WS/metrics_test.go" | cut -d' ' -f1)" = "$TEST_SHA_REF" ] && UNTOUCHED=1
  FMT=0; [ -z "$(gofmt -l "$WS"/*.go 2>/dev/null)" ] && FMT=1
  CLEAN=0; [ "$TESTS" = 1 ] && [ "$DUP" = 1 ] && [ "$UNTOUCHED" = 1 ] && [ "$FMT" = 1 ] && CLEAN=1
  [ "$CLEAN" = 1 ] && CLEAN_COUNT=$((CLEAN_COUNT+1)) || cp -r "$WS" "$RESULTS_DIR/failed-$LANE-r$ROUND-$(date +%H%M%S)" 2>/dev/null

  echo "$(date -Is),$LANE,$ROUND,$STATUS,$TESTS,$DUP,$UNTOUCHED,$FMT,$CLEAN,$DUR" >> "$CSV"
  echo "round $ROUND/$RUNS: status=$STATUS tests=$TESTS dup_gone=$DUP untouched=$UNTOUCHED gofmt=$FMT clean=$CLEAN (${DUR}s)"
  rm -rf "$WS"
done

echo "=== $LANE: $CLEAN_COUNT/$RUNS clean ==="
