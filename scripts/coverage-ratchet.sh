#!/bin/sh
# Go coverage baseline + non-regression ratchet for hirdforge.
#
# Phase 0 (testing-validation-autonomy spec) closeout: records a per-package
# coverage baseline and enforces non-regression in CI.
#
# Usage:
#   scripts/coverage-ratchet.sh generate   # rewrite .quality/coverage-baseline.txt
#   scripts/coverage-ratchet.sh check      # (default) fail if any package regressed
#
# Method:
#   - Coverage is measured only over packages that have test files
#     (`go list -f '{{if .TestGoFiles}}...'`). Packages with no tests are
#     skipped because instrumenting them requires the `covdata` tool, which is
#     absent from some local toolchains and would break `-coverprofile` there.
#   - The baseline stores the measured per-package coverage (one decimal) plus a
#     repo-wide total.
#   - The ratchet enforces non-regression with a fixed tolerance: a package
#     passes when current >= baseline - TOLERANCE (default 0.5pp). This absorbs
#     the sub-percent run-to-run jitter observed in cmd/agent (the combined
#     coverprofile reports 19.9% or 20.0% across runs) and minor toolchain/env
#     differences, while still catching real regressions (a drop of >0.5pp). The
#     tolerance is ~5x the observed jitter. To tighten a baseline after raising
#     real coverage, re-run `generate`.
#
# Exit 0 if all packages hold their baseline floor; 1 on regression.

set -eu

BASELINE_FILE=".quality/coverage-baseline.txt"
MODE="${1:-check}"
TOLERANCE="${COVERAGE_TOLERANCE:-0.5}"

# Resolve repo root so the script works from any CWD.
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# Packages that actually have tests; coverage is meaningful only for these.
test_pkgs() {
    go list -f '{{if .TestGoFiles}}{{.ImportPath}}{{end}}' ./...
}

# Emit "<import-path> <coverage-float>" lines for every test package, plus a
# trailing "total <coverage-float>" line aggregated across the same set.
measure() {
    pkgs="$(test_pkgs)"
    [ -n "$pkgs" ] || { echo "no packages with tests found" >&2; exit 1; }

    profile="$(mktemp)"
    # shellcheck disable=SC2086
    go test $pkgs -count=1 -coverprofile="$profile" 2>/dev/null \
        | awk '/coverage:/ {
                 for (i = 1; i <= NF; i++)
                   if ($i == "coverage:") { sub(/%/, "", $(i+1)); print $2, $(i+1) }
               }'
    total="$(go tool cover -func="$profile" | awk '/^total:/ {sub(/%/, "", $NF); print $NF}')"
    rm -f "$profile"
    echo "total $total"
}

case "$MODE" in
generate)
    {
        echo "# Go per-package coverage baseline — Phase 0 closeout."
        echo "# Regenerate with: scripts/coverage-ratchet.sh generate"
        echo "# Ratchet enforces current >= baseline - 0.5pp by default (see script header)."
        echo "# format: <import-path|total> <coverage-percent>"
        measure
    } > "$BASELINE_FILE"
    echo "Wrote $BASELINE_FILE"
    ;;

check)
    [ -f "$BASELINE_FILE" ] || { echo "ERROR: $BASELINE_FILE not found; run 'generate' first" >&2; exit 1; }

    current="$(mktemp)"
    measure > "$current"

    failures=0
    # Iterate baseline entries (skip comments/blanks) and compare against current.
    while read -r pkg base _rest; do
        case "$pkg" in ''|\#*) continue ;; esac
        cur="$(awk -v p="$pkg" '$1 == p {print $2; exit}' "$current")"
        if [ -z "$cur" ]; then
            echo "MISSING: $pkg present in baseline but produced no coverage now"
            failures=$((failures + 1))
            continue
        fi
        # Pass when current >= baseline - TOLERANCE.
        verdict="$(awk -v c="$cur" -v b="$base" -v t="$TOLERANCE" 'BEGIN {print (c + 0 >= b - t) ? "ok" : "regressed"}')"
        floor="$(awk -v b="$base" -v t="$TOLERANCE" 'BEGIN{printf "%.1f", b - t}')"
        if [ "$verdict" = "ok" ]; then
            echo "OK:         $pkg ${cur}% (baseline ${base}%, min ${floor}%)"
        else
            echo "REGRESSION: $pkg ${cur}% < min ${floor}% (baseline ${base}%, tolerance ${TOLERANCE}pp)"
            failures=$((failures + 1))
        fi
    done < "$BASELINE_FILE"
    rm -f "$current"

    if [ "$failures" -gt 0 ]; then
        echo ""
        echo "ERROR: $failures coverage regression(s). Raise coverage, or if intentional"
        echo "       re-baseline with: scripts/coverage-ratchet.sh generate"
        exit 1
    fi
    echo ""
    echo "All packages hold their coverage baseline."
    ;;

*)
    echo "usage: $0 [generate|check]" >&2
    exit 2
    ;;
esac
