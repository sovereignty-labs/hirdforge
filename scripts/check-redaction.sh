#!/usr/bin/env bash
# Generic leak heuristics for portfolio publishes.
# Exit 1 if suspicious patterns found.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

EXCLUDES=(
  --exclude-dir=.git
  --exclude='check-redaction.sh'
  --exclude='SANITIZATION.md'
  --exclude='.gitignore'
  --exclude='LICENSE'
)

fail=0

if grep -RInE "${EXCLUDES[@]}" \
  '\b10\.(0|1|2|10|20|100)\.[0-9]+\.[0-9]+\b|\b192\.168\.[0-9]+\.[0-9]+\b|\b172\.(1[6-9]|2[0-9]|3[0-1])\.[0-9]+\.[0-9]+\b' \
  . 2>/dev/null; then
  echo "FAIL: private IPv4-looking addresses found (use 203.0.113.x or \${PLACEHOLDER})"
  fail=1
fi

if grep -RInE "${EXCLUDES[@]}" \
  '\.lan\b|BEGIN (RSA |OPENSSH |EC )?PRIVATE KEY|sealedsecrets?\.bitnami\.com|x-access-token' \
  . 2>/dev/null; then
  echo "FAIL: internal DNS or key/sealed-secret material"
  fail=1
fi

if grep -RInE "${EXCLUDES[@]}" \
  'client-certificate-data:|client-key-data:' \
  . 2>/dev/null; then
  echo "FAIL: kubeconfig credential material"
  fail=1
fi

if grep -RInE "${EXCLUDES[@]}" \
  'password:[[:space:]]*["\x27]?[^"\x27\s#]{8,}|token:[[:space:]]*["\x27]?[A-Za-z0-9._-]{20,}' \
  . 2>/dev/null; then
  echo "FAIL: password/token assignment-like lines"
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then
  echo "Redaction check failed."
  exit 1
fi
echo "OK: generic redaction heuristics clean."
