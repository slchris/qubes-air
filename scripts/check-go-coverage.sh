#!/usr/bin/env bash
# Enforce the repository-wide Go coverage floor.
#
# The Makefile and CI both call this, so the threshold and the way the profile
# is read cannot drift apart. The profile is produced by
# `go test -race -coverprofile=coverage.out ./...`.
set -euo pipefail

coverage_file="${1:-coverage.out}"
minimum="${GO_COVERAGE_MIN:-61.0}"

# `go tool cover` resolves the profile's file names relative to the module, so
# run from the directory that produced it.
case "$coverage_file" in
  */*)
    cd "$(dirname "$coverage_file")"
    coverage_file="$(basename "$coverage_file")"
    ;;
esac

coverage=$(go tool cover -func="$coverage_file" | awk '/^total:/ {gsub(/%/, "", $NF); print $NF}')
if [ -z "$coverage" ]; then
  echo "cannot read a Go coverage total from $coverage_file" >&2
  exit 1
fi

awk -v actual="$coverage" -v minimum="$minimum" 'BEGIN {
  if (actual + 0 < minimum + 0) {
    printf "Go coverage %.1f%% is below the %.1f%% floor\n", actual, minimum
    exit 1
  }
  printf "Go coverage: %.1f%% (floor %.1f%%)\n", actual, minimum
}'
