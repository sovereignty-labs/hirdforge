#!/usr/bin/env bash
# Builder-agent FULL-FLOW benchmark (Phase 2 P2.0 — the acceptance instrument).
#
# The edit-only run.sh scores a refactor in place; it never exercises the leg
# that actually broke in the Phase-1 dogfood: git-commit(push) -> create-pr.
# This runner scores the WHOLE flow against a throwaway remote, mechanically:
#
#   clean = a real PR was observed on the throwaway remote (fakegitea) AND the
#           branch that PR points at carries a correct refactor
#           (tests green + duplication gone + test file untouched + gofmt clean)
#
# Per round it stands up a bare origin, pre-clones it into the agent workspace,
# runs the repo's real cmd/agent loop against a LiteLLM lane with the git-commit
# and create-pr tools enabled, then inspects ground truth. No live Gitea, no
# network: fakegitea (bench/builder/fakegitea) is the PR sink and enforces the
# real "head branch must exist" 404, so an accepted PR is a production-plausible
# PR. The agent's own claim of success is never consulted (D-GATE principle).
#
# Usage:  LITELLM_KEY=sk-... ./run-flow.sh <lane> [runs] [litellm_url]
#
# Results: appends CSV rows to results/<lane>-flow.csv and prints a summary.
set -u

BENCH_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$BENCH_DIR/../.." && pwd)"
LANE="${1:?usage: run-flow.sh <lane> [runs] [litellm_url]}"
RUNS="${2:-10}"
LITELLM_URL="${3:-http://localhost:4000}"
: "${LITELLM_KEY:?set LITELLM_KEY}"

# git-commit rewrites origin to a credentialed URL only when a Gitea token is
# resolvable; the throwaway origin is a local bare repo with no auth, so a stray
# token in the environment would clobber the working origin and break the push.
# Keep the flow hermetic.
unset GITEA_TOKEN GITEA_REVIEWERS_TOKEN

PORT="${BENCH_PORT:-18081}"
GITEA_PORT="${BENCH_GITEA_PORT:-18090}"
MAX_ROUNDS="${BENCH_MAX_ROUNDS:-80}"
TASK_TIMEOUT_SECS="${BENCH_TASK_TIMEOUT:-1800}"
RESULTS_DIR="$BENCH_DIR/results"
mkdir -p "$RESULTS_DIR"
CSV="$RESULTS_DIR/$LANE-flow.csv"
[ -f "$CSV" ] || echo "timestamp,lane,round,task_status,pr_created,branch_pushed,tests_green,dup_gone,testfile_untouched,gofmt_clean,clean,duration_secs" > "$CSV"

command -v go >/dev/null || { echo "go not in PATH"; exit 1; }
command -v git >/dev/null || { echo "git not in PATH"; exit 1; }

AGENT_BIN="$BENCH_DIR/.agent-bin"
FAKE_BIN="$BENCH_DIR/.fakegitea-bin"
echo "building agent binary..."
(cd "$REPO_ROOT" && go build -o "$AGENT_BIN" ./cmd/agent) || exit 1
echo "building fakegitea binary..."
(cd "$REPO_ROOT" && go build -o "$FAKE_BIN" ./bench/builder/fakegitea) || exit 1

for p in "$PORT" "$((PORT + 1))" "$GITEA_PORT"; do
  if ss -tln | grep -qE ":$p\b"; then
    echo "ERROR: port $p already in use — kill the stray process first"
    exit 1
  fi
done

TEST_SHA_REF="$(sha256sum "$BENCH_DIR/fixture/metrics_test.go" | cut -d' ' -f1)"
CLEAN_COUNT=0

# seed_origin builds a bare throwaway remote holding the fixture package on main.
seed_origin() {
  local origin="$1" seed
  git init --bare -q -b main "$origin"
  seed="$(mktemp -d /tmp/bench-flow-seed.XXXXXX)"
  cp "$BENCH_DIR/fixture/metrics.go" "$BENCH_DIR/fixture/metrics_test.go" "$seed/"
  printf 'module benchfixture\n\ngo 1.25\n' > "$seed/go.mod"
  git -C "$seed" init -q -b main
  git -C "$seed" config user.email bench@hirdforge.local
  git -C "$seed" config user.name "Bench Seed"
  git -C "$seed" add -A
  git -C "$seed" commit -q -m "seed benchfixture"
  git -C "$seed" push -q "$origin" main
  rm -rf "$seed"
}

for ROUND in $(seq 1 "$RUNS"); do
  ROUND_TMP="$(mktemp -d /tmp/bench-flow.XXXXXX)"
  ORIGIN="$ROUND_TMP/benchfixture.git"
  WS="$ROUND_TMP/ws"
  RECORD="$ROUND_TMP/prs.jsonl"
  mkdir -p "$WS"
  : > "$RECORD"

  seed_origin "$ORIGIN"
  # The agent's workspace holds the repo as ./benchfixture, cloned from origin.
  git clone -q "$ORIGIN" "$WS/benchfixture"
  git -C "$WS/benchfixture" config user.email agent@valhalla.local
  git -C "$WS/benchfixture" config user.name "Bench Builder"

  "$FAKE_BIN" -addr "127.0.0.1:$GITEA_PORT" -origin "$ORIGIN" -record "$RECORD" \
    >"$ROUND_TMP/fakegitea.log" 2>&1 &
  FAKE_PID=$!
  for _ in $(seq 1 30); do
    curl -sf -m 2 "http://127.0.0.1:$GITEA_PORT/_health" >/dev/null && break
    sleep 0.5
  done

  "$AGENT_BIN" \
    -port "$PORT" \
    -grpc-port "$((PORT + 1))" \
    -workspace "$WS" \
    -inference-url "$LITELLM_URL" \
    -api-key "$LITELLM_KEY" \
    -model "$LANE" \
    -model-template qwen \
    -tools exec,read,write,edit,git-commit,create-pr \
    -gitea-url "http://127.0.0.1:$GITEA_PORT" \
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
    -d "$(python3 -c "import json;print(json.dumps({'content':open('$BENCH_DIR/flow-task.md').read(),'from':'bench'}))")" \
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
  kill "$FAKE_PID" 2>/dev/null; wait "$FAKE_PID" 2>/dev/null

  # --- Mechanical scoring: ground truth on the throwaway remote. ---
  PR_CREATED=0; PR_HEAD=""
  if [ -s "$RECORD" ]; then
    PR_CREATED=1
    # The head of the newest recorded PR is the branch under test.
    PR_HEAD=$(tail -n1 "$RECORD" | python3 -c "import json,sys;print(json.load(sys.stdin).get('head',{}).get('ref',''))" 2>/dev/null || echo "")
  fi

  # A non-main branch on the origin means the push leg succeeded, PR or not.
  BRANCH_PUSHED=0
  if git --git-dir="$ORIGIN" for-each-ref --format='%(refname:short)' refs/heads \
       | grep -qvx main; then
    BRANCH_PUSHED=1
  fi

  TESTS=0; DUP=0; UNTOUCHED=0; FMT=0
  if [ "$PR_CREATED" = 1 ] && [ -n "$PR_HEAD" ]; then
    CHECK="$ROUND_TMP/check"
    if git clone -q --branch "$PR_HEAD" "$ORIGIN" "$CHECK" 2>/dev/null; then
      (cd "$CHECK" && go test ./... >/dev/null 2>&1) && TESTS=1
      DUP_COUNT=$(grep -c 'TrimSpace(strings.ToLower' "$CHECK/metrics.go" 2>/dev/null || echo 99)
      [ "$DUP_COUNT" -le 1 ] && DUP=1
      [ "$(sha256sum "$CHECK/metrics_test.go" 2>/dev/null | cut -d' ' -f1)" = "$TEST_SHA_REF" ] && UNTOUCHED=1
      [ -z "$(gofmt -l "$CHECK"/*.go 2>/dev/null)" ] && FMT=1
    fi
  fi

  CLEAN=0
  [ "$PR_CREATED" = 1 ] && [ "$TESTS" = 1 ] && [ "$DUP" = 1 ] && [ "$UNTOUCHED" = 1 ] && [ "$FMT" = 1 ] && CLEAN=1
  if [ "$CLEAN" = 1 ]; then
    CLEAN_COUNT=$((CLEAN_COUNT + 1))
  else
    cp -r "$ROUND_TMP" "$RESULTS_DIR/failed-$LANE-flow-r$ROUND-$(date +%H%M%S)" 2>/dev/null
  fi

  echo "$(date -Is),$LANE,$ROUND,$STATUS,$PR_CREATED,$BRANCH_PUSHED,$TESTS,$DUP,$UNTOUCHED,$FMT,$CLEAN,$DUR" >> "$CSV"
  echo "round $ROUND/$RUNS: status=$STATUS pr=$PR_CREATED branch=$BRANCH_PUSHED tests=$TESTS dup_gone=$DUP untouched=$UNTOUCHED gofmt=$FMT clean=$CLEAN (${DUR}s)"
  rm -rf "$ROUND_TMP"
done

echo "=== $LANE (full flow): $CLEAN_COUNT/$RUNS clean ==="
