# TESTING

## Purpose

This document covers how `prtags` tests, coverage checks, and lint-like checks
are expected to work.

It focuses on:

- the commands contributors should run locally
- the checks CI runs today
- the difference between current enforced checks and optional quality gates

## Current CI

The main CI workflow lives in `.github/workflows/ci.yml`.

Today it enforces:

- SimpleDoc document checks
- `gofmt`
- `go vet ./...`
- `go test ./...`
- coverage floor through `scripts/check-go-coverage.sh`
- `golangci-lint` with a focused lint and complexity ruleset
- snapshot release build validation through GoReleaser

CI currently enforces:

- a minimum total coverage floor of `80.0%`
- `golangci-lint`
- cyclomatic complexity through `cyclop`
- cognitive complexity through `gocognit`

## Unit tests

`prtags` uses normal Go `*_test.go` files and the standard `testing` package.

The most important default test command is:

```sh
go test ./...
```

When making a focused change, it is fine to run a narrower package set first,
then finish with the full repo test pass before shipping.

Examples:

```sh
go test ./internal/core ./internal/httpapi
go test ./cmd/prtags
go test ./...
```

Tests are part of the definition of done.

New behavior should normally ship with tests in the same PR.

Bug fixes should normally ship with a regression test that would have caught the
bug before the fix.

## Coverage

The standard Go coverage flow is:

1. run tests with `-coverprofile`
2. inspect the profile with `go tool cover`

Example:

```sh
go test ./... -coverprofile=cover.out
go tool cover -func=cover.out
go tool cover -html=cover.out
```

The `go tool cover -func` report ends with a `total:` line that reports overall
statement coverage.

### Clean coverage runs

Some local environments may have GitHub auth tokens or a populated
`PRTAGS_CONFIG_DIR`. Those can affect CLI tests that intentionally check auth
header behavior.

For a clean repo-wide coverage run, prefer:

```sh
tmpdir=$(mktemp -d)
env -u GITHUB_TOKEN -u GH_TOKEN -u PRTAGS_GITHUB_TOKEN \
  PRTAGS_CONFIG_DIR="$tmpdir" \
  go test ./... -coverprofile=cover.out
go tool cover -func=cover.out
```

### Optional hard threshold checks

`prtags` currently enforces a minimum total coverage floor in CI through
`scripts/check-go-coverage.sh`.

That script parses the `total:` line from `go tool cover -func`.

Example 100% assertion:

```sh
go test ./... -coverprofile=cover.out
total=$(go tool cover -func=cover.out | awk '/^total:/ {print substr($3, 1, length($3)-1)}')
awk -v total="$total" 'BEGIN { exit !(total == 100.0) }'
```

That style is conventional, and it is the model `prtags` uses in CI.

### Coverage targets

The quality goal is high coverage on the code that carries product and
operational risk.

- Strong repo-wide target: `80%+`

For `prtags`, the goal is not blind `100%` coverage across the whole repo.

Preferred long-term package targets:

- `internal/core`: `90%+`
- `internal/httpapi`: `85%+`
- `internal/githubapi`: `80%+`
- `internal/ghreplica`: `80%+`
- small utility packages: near `100%` when practical

Coverage should be raised deliberately, package by package, with useful tests.
Do not add low-value tests only to move the total percentage.

## Formatting and lint-like checks

The current local baseline before opening or merging a PR is:

```sh
gofmt -w .
go vet ./...
go test ./...
./scripts/check-go-coverage.sh
/home/bob/go/bin/golangci-lint run
```

If you want a CI-shaped local pass that mirrors the current workflow more
closely, run:

```sh
npx --yes @simpledoc/simpledoc@0.1.6 check
find . -type f -name '*.go' -not -path './vendor/*' | xargs gofmt -l
go vet ./...
go test ./...
./scripts/check-go-coverage.sh
/home/bob/go/bin/golangci-lint run
```

The `gofmt -l` command should print nothing when formatting is clean.

## Test types

`prtags` should keep test intent clear.

- Unit tests cover small pieces of logic in isolation and should stay fast.
- Integration tests cover real package boundaries such as HTTP handlers,
  database behavior, or client interactions.
- Smoke or end-to-end checks verify that the built application still works at a
  high level, but they should stay smaller in number than unit and integration
  tests.

The default `go test ./...` path should stay reliable and fast enough to run
often during normal development and in CI.

For this repository:

- A unit test should usually exercise one small piece of logic mostly in
  memory.
- An integration test should usually exercise a real boundary such as SQL,
  migrations, HTTP request handling, or client behavior.

## Determinism

Tests should be deterministic.

That means they should avoid dependence on:

- live network access
- uncontrolled wall clock time
- random behavior without a fixed seed
- shared mutable local state across tests

If a test needs time, randomness, or external boundaries, control them
explicitly in the test setup.

Flaky tests are not acceptable in the default test suite.

If a test is flaky, fix it, quarantine it from the normal path, or remove it.

## Mocks and boundaries

Use mocks or stubs at real boundaries.

Typical valid boundaries in `prtags` include:

- GitHub API access
- `ghreplica` client behavior
- time
- database boundaries when a smaller isolated test is appropriate
- background job dispatch or worker behavior

Do not mock internal business logic only to make a unit test easier to write.

Prefer testing real code paths inside `prtags` unless the test is intentionally
isolating an external dependency or boundary.

## Regression tests

Production bugs with a clear reproduction should get a regression test.

If a bug fix cannot be covered by an automated test, the PR should make that
gap explicit.

## Complexity metrics

`prtags` currently gates on complexity metrics through `golangci-lint`.

The initial enforced complexity linters are:

- `cyclop` for cyclomatic complexity
- `gocognit` for cognitive complexity

That is a better first step than introducing a hard CRAP metric gate.

### Complexity targets

Aim for:

- `cyclop: 10`
- `gocognit: 15`

The ideal target should be treated as a design goal, not a reason to
over-factor otherwise clear code into too many tiny helpers.

## Practical expectations

- Add tests for new behavior and regressions.
- Prefer targeted tests over coverage-chasing.
- Keep tests fast, readable, and independent.
- Prefer explicit local setup over hidden shared fixtures.
- Treat flaky tests as real failures, not normal background noise.
- Use coverage reports to find important blind spots, not to justify weak
  tests that only hit lines.
- Keep repo-wide checks cheap enough that they are run frequently.
- Treat coverage as a signal, not the goal. The goal is confidence that the
  important behavior really works.
- Coverage and complexity thresholds should be tightened deliberately over time,
  not raised accidentally by tool churn.
- Slow or environment-heavy tests should not degrade the normal `go test ./...`
  path.
