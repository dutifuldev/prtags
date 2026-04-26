#!/usr/bin/env bash

set -euo pipefail

minimum_total_coverage="80.0"
declare -A package_minimums=(
  ["./internal/core"]="90.0"
  ["./internal/httpapi"]="85.0"
  ["./internal/githubapi"]="80.0"
  ["./internal/mirrordb"]="80.0"
)
tmpdir="$(mktemp -d)"
coverfile="$(mktemp)"
package_coverfile="$(mktemp)"

cleanup() {
  rm -rf "$tmpdir"
  rm -f "$coverfile"
  rm -f "$package_coverfile"
}
trap cleanup EXIT

run_clean_go_test() {
  env -u GITHUB_TOKEN -u GH_TOKEN -u PRTAGS_GITHUB_TOKEN \
    PRTAGS_CONFIG_DIR="$tmpdir" \
    go test "$@"
}

coverage_total() {
  go tool cover -func="$1" |
    awk '/^total:/ {print substr($3, 1, length($3)-1)}'
}

run_clean_go_test ./... -coverprofile="$coverfile"

total="$(coverage_total "$coverfile")"

printf 'Total coverage: %s%%\n' "$total"

awk -v total="$total" -v minimum="$minimum_total_coverage" \
  'BEGIN { exit !(total + 0 >= minimum + 0) }'

for pkg in "${!package_minimums[@]}"; do
  minimum="${package_minimums[$pkg]}"
  run_clean_go_test "$pkg" -coverprofile="$package_coverfile" >/dev/null
  package_total="$(coverage_total "$package_coverfile")"
  printf '%s coverage: %s%%\n' "$pkg" "$package_total"
  awk -v total="$package_total" -v minimum="$minimum" \
    'BEGIN { exit !(total + 0 >= minimum + 0) }'
done
