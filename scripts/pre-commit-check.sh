#!/bin/sh
# Pre-commit validation for Valhalla agents
# Usage: ./scripts/pre-commit-check.sh [--dir <path>]
# Exit 0 if all pass, 1 if any fail

WORK_DIR="${PWD}"
CHECKS_PASSED=0
TOTAL_CHECKS=7

[ "$1" = "--dir" ] && [ -n "$2" ] && WORK_DIR="$2"
cd "$WORK_DIR" || exit 1

echo "=== Pre-commit Validation ==="
echo "Directory: $WORK_DIR"

# 1. Merge conflict markers
conflict_count=$(git grep -cE '^(<<<<<<|>>>>>>|======)$' -- '*.go' '*.py' '*.yaml' '*.yml' '*.sh' '*.md' 2>/dev/null || echo "0")
if [ "$conflict_count" -eq 0 ]; then
    echo "[PASS] No merge conflict markers"; CHECKS_PASSED=$((CHECKS_PASSED + 1))
else
    echo "[FAIL] Found $conflict_count merge conflict markers"
fi

# 2. Files larger than 500KB staged
large_files=$(git diff --cached --name-only | while IFS= read -r f; do
    size=$(git show ":$f" 2>/dev/null | wc -c)
    [ "$size" -gt 512000 ] && echo "$f ($size bytes)"
done)
if [ -z "$large_files" ]; then
    echo "[PASS] No files >500KB staged"; CHECKS_PASSED=$((CHECKS_PASSED + 1))
else
    echo "[FAIL] Large files staged:"; echo "$large_files"
fi

# 3. Go vet on staged .go files
go_files=$(git diff --cached --name-only | grep -E '\.go$' || echo "")
if [ -n "$go_files" ]; then
    if go vet ./... 2>&1; then
        echo "[PASS] go vet passed"; CHECKS_PASSED=$((CHECKS_PASSED + 1))
    else
        echo "[FAIL] go vet found issues"
    fi
else
    echo "[PASS] No .go files staged, skipping go vet"; CHECKS_PASSED=$((CHECKS_PASSED + 1))
fi

# 4. Python py_compile on staged .py files
py_files=$(git diff --cached --name-only | grep -E '\.py$' || echo "")
if [ -n "$py_files" ]; then
    py_errors=0
    for pyf in $py_files; do
        [ -f "$pyf" ] && python3 -m py_compile "$pyf" 2>/dev/null || { echo "  [FAIL] $pyf"; py_errors=1; }
    done
    [ "$py_errors" -eq 0 ] && { echo "[PASS] All .py files OK"; CHECKS_PASSED=$((CHECKS_PASSED + 1)); } \
        || echo "[FAIL] Some .py files have errors"
else
    echo "[PASS] No .py files staged"; CHECKS_PASSED=$((CHECKS_PASSED + 1))
fi

# 5. YAML tabs check
yaml_files=$(git diff --cached --name-only | grep -E '\.ya?ml$' || echo "")
if [ -n "$yaml_files" ]; then
    tab_errors=0
    for yf in $yaml_files; do
        [ -f "$yf" ] && grep -q '	' "$yf" 2>/dev/null && { echo "  [FAIL] $yf has tabs"; tab_errors=1; }
    done
    [ "$tab_errors" -eq 0 ] && { echo "[PASS] No tabs in YAML"; CHECKS_PASSED=$((CHECKS_PASSED + 1)); } \
        || echo "[FAIL] Tabs found in YAML"
else
    echo "[PASS] No YAML files staged"; CHECKS_PASSED=$((CHECKS_PASSED + 1))
fi

# 6. Branch name (not main)
current_branch=$(git rev-parse --abbrev-ref HEAD)
if [ "$current_branch" != "main" ]; then
    echo "[PASS] Branch '$current_branch' (not main)"; CHECKS_PASSED=$((CHECKS_PASSED + 1))
else
    echo "[FAIL] Cannot commit directly to main"
fi

# 7. Binary files staged
binary_files=$(git diff --cached --name-only | grep -iE '\.(pyc|exe|bin|so)$' || echo "")
if [ -z "$binary_files" ]; then
    echo "[PASS] No binary files staged"; CHECKS_PASSED=$((CHECKS_PASSED + 1))
else
    echo "[FAIL] Binary files staged:"; echo "$binary_files"
fi

# Summary
echo ""
echo "=== Summary: $CHECKS_PASSED/$TOTAL_CHECKS passed ==="
[ "$CHECKS_PASSED" -eq "$TOTAL_CHECKS" ] && exit 0 || exit 1
