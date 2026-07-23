#!/bin/sh
# Go pre-commit validation for hirdforge
# Runs go vet, go build on all binaries (agent, gateway, lockbox), and gofmt checks
# Usage: ./scripts/go-precommit.sh [--all]
# Exit 0 if all pass, non-zero if any fail

set -e

FAILURES=0

echo "=== Go Pre-commit Validation ==="

# 1. go vet on all packages
echo "[1/4] Running go vet..."
if go vet ./... 2>&1; then
    echo "      [PASS] go vet passed"
else
    echo "      [FAIL] go vet found issues"
    FAILURES=$((FAILURES + 1))
fi

# 2. go build agent
echo "[2/4] Building agent..."
if go build -o /tmp/valhalla-agent ./cmd/agent 2>&1; then
    echo "      [PASS] agent built successfully"
    rm -f /tmp/valhalla-agent
else
    echo "      [FAIL] agent build failed"
    FAILURES=$((FAILURES + 1))
fi

# 3. go build gateway
echo "[3/4] Building gateway..."
if go build -o /tmp/valhalla-gateway ./cmd/gateway 2>&1; then
    echo "      [PASS] gateway built successfully"
    rm -f /tmp/valhalla-gateway
else
    echo "      [FAIL] gateway build failed"
    FAILURES=$((FAILURES + 1))
fi

# 4. go build lockbox
echo "[4/4] Building lockbox..."
if go build -o /tmp/valhalla-lockbox ./cmd/lockbox 2>&1; then
    echo "      [PASS] lockbox built successfully"
    rm -f /tmp/valhalla-lockbox
else
    echo "      [FAIL] lockbox build failed"
    FAILURES=$((FAILURES + 1))
fi

# 5. gofmt check
echo "[5/4] Running gofmt check..."
UNFORMATTED=$(gofmt -l .)
if [ -z "$UNFORMATTED" ]; then
    echo "      [PASS] All files are gofmt'd"
else
    echo "      [FAIL] Files need formatting:"
    echo "$UNFORMATTED"
    FAILURES=$((FAILURES + 1))
fi

# Summary
echo ""
if [ "$FAILURES" -eq 0 ]; then
    echo "=== All checks passed ==="
    exit 0
else
    echo "=== $FAILURES check(s) failed ==="
    exit 1
fi
