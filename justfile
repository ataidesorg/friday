# List available recipes.
default:
    @just --list

# Run every quality gate (format, vet, lint, test, vulnerabilities, secrets).
check *args:
    ./scripts/check.sh {{args}}

# Reformat all Go source in place.
fmt:
    gofmt -w .

# Run the test suite with the race detector and coverage.
test:
    go test -race -cover ./...

# Fuzz every Fuzz* target for 10 seconds each.
fuzz:
    #!/usr/bin/env bash
    set -euo pipefail
    for pkg in $(go list ./...); do
        for fn in $(go test -list '^Fuzz' "$pkg" 2>/dev/null | grep -E '^Fuzz'); do
            echo "==> fuzzing $fn ($pkg)"
            go test -run '^$' -fuzz "^${fn}$" -fuzztime 10s "$pkg"
        done
    done

# Build the friday binary into bin/.
build:
    go build -o bin/friday ./cmd/friday
