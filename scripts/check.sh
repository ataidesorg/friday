#!/usr/bin/env bash
# Runs every quality gate a fresh clone needs: format, vet, static analysis,
# lint, tests, vulnerability scan, and secret scan — in that order. Stops at
# the first gate that fails, so a non-zero exit always attributes to one gate.
#
# Usage: scripts/check.sh [--strict] [--no-gitleaks]
#
#   --strict       treat a missing optional tool (staticcheck, golangci-lint,
#                  govulncheck, gitleaks) as a failure instead of a warning.
#                  Implied automatically when CI=1.
#   --no-gitleaks  skip the gitleaks gate (CI runs gitleaks as its own job).
set -euo pipefail

cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

strict=0
no_gitleaks=0

for arg in "$@"; do
  case "$arg" in
    --strict) strict=1 ;;
    --no-gitleaks) no_gitleaks=1 ;;
    -h|--help)
      echo "usage: $(basename "$0") [--strict] [--no-gitleaks]"
      exit 0
      ;;
    *)
      echo "check.sh: unknown flag: $arg" >&2
      echo "usage: $(basename "$0") [--strict] [--no-gitleaks]" >&2
      exit 2
      ;;
  esac
done

if [ "${CI:-}" = "1" ]; then
  strict=1
fi

if ! command -v go >/dev/null 2>&1; then
  echo "check.sh: 'go' not found in PATH; the Go toolchain is required" >&2
  exit 1
fi

# require_tool NAME reports whether an optional tool's gate should run.
# Missing + --strict (or CI=1) is a hard failure; missing otherwise is a
# warning, and the gate is skipped.
require_tool() {
  local name="$1"
  if command -v "$name" >/dev/null 2>&1; then
    return 0
  fi
  if [ "$strict" -eq 1 ]; then
    echo "check.sh: required tool not found: $name (--strict)" >&2
    exit 1
  fi
  echo "check.sh: WARNING: $name not found on PATH, skipping this gate" >&2
  return 1
}

echo "==> gofmt -l ."
unformatted="$(gofmt -l .)"
if [ -n "$unformatted" ]; then
  echo "check.sh: gofmt: the following files are not formatted:" >&2
  echo "$unformatted" >&2
  exit 1
fi

echo "==> go vet ./..."
go vet ./...

if require_tool staticcheck; then
  echo "==> staticcheck ./..."
  staticcheck ./...
fi

if require_tool golangci-lint; then
  echo "==> golangci-lint run ./..."
  golangci-lint run ./...
fi

echo "==> go test -race -cover ./..."
go test -race -cover ./...

if require_tool govulncheck; then
  echo "==> govulncheck ./..."
  govulncheck ./...
fi

echo "==> scripts/check-links.sh"
./scripts/check-links.sh

if [ "$no_gitleaks" -eq 1 ]; then
  echo "==> gitleaks (skipped: --no-gitleaks)"
elif require_tool gitleaks; then
  echo "==> gitleaks detect --no-git --source . --redact"
  gitleaks detect --no-git --source . --redact
fi

echo "check.sh: all gates passed"
