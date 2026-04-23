#!/usr/bin/env bash

set -euo pipefail

minimum_coverage="49.0"
tmpdir="$(mktemp -d)"
coverfile="$(mktemp)"

cleanup() {
  rm -rf "$tmpdir"
  rm -f "$coverfile"
}
trap cleanup EXIT

env -u GITHUB_TOKEN -u GH_TOKEN -u PRTAGS_GITHUB_TOKEN \
  PRTAGS_CONFIG_DIR="$tmpdir" \
  go test ./... -coverprofile="$coverfile"

total="$(
  go tool cover -func="$coverfile" |
    awk '/^total:/ {print substr($3, 1, length($3)-1)}'
)"

printf 'Total coverage: %s%%\n' "$total"

awk -v total="$total" -v minimum="$minimum_coverage" \
  'BEGIN { exit !(total + 0 >= minimum + 0) }'
