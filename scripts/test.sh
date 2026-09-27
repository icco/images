#!/bin/sh
set -eu

# Measure the service logic, excluding command-line startup/shutdown wiring.
# This retains icco/go-template's 80% coverage floor for gateway and engine.
go test -race -coverpkg=./internal/... -coverprofile=coverage.out ./...
total=$(go tool cover -func=coverage.out | awk '/^total:/ {print $3}' | tr -d '%')
threshold=${COVERAGE_THRESHOLD:-80}
echo "Service coverage: ${total}% (threshold: ${threshold}%)"
awk -v c="$total" -v t="$threshold" 'BEGIN { if (c+0 < t+0) exit 1 }'
go vet ./...
