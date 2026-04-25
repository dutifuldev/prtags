---
title: Direct ghreplica Reader Plan
date: 2026-04-25
status: proposed
---

# Direct ghreplica Reader Plan

## Summary

`prtags` should stop calling the `ghreplica` HTTP API for internal metadata
reads once both systems can read from the same Postgres database.

This deprecates the separate `prtags` Postgres database deployment topology.
It does not deprecate the `prtags` data model, tables, schema, or ownership of
group data.

The model is:

- `ghreplica` owns mirrored GitHub data
- `prtags` owns groups, annotations, projections, and comment sync
- `prtags` imports the `ghreplica/mirror` Go package for typed mirror reads
- both systems keep separate database schemas inside the same Postgres database

This keeps the product boundary explicit while removing the internal network hop.

## Goals

- let `prtags` enrich group members by reading mirrored GitHub objects directly
- avoid duplicating `ghreplica` table definitions and GitHub-shaped conversion
  logic in `prtags`
- keep `ghreplica` independently runnable and useful as a standalone mirror
- keep `prtags` cache-first for normal group reads
- make the shared database topology explicit and safe to migrate

## Non-Goals

- do not merge the `ghreplica` and `prtags` repositories
- do not move group, annotation, or comment-sync behavior into `ghreplica`
- do not make `ghreplica` depend on `prtags`
- do not remove the public `ghreplica` HTTP API
- do not make `group get` block on live GitHub or live `ghreplica` HTTP calls

## Target Architecture

The shared database should use explicit schemas:

- configured mirror schema for mirrored GitHub tables
- `prtags` schema for curation tables

The intended topology is one Postgres database with both schemas in it.
That is required for normal SQL joins between `prtags` group tables and
`ghreplica` mirror tables.

The topology with one Postgres database for `ghreplica` and a different
Postgres database for `prtags` is deprecated.

`ghreplica` should continue to own its migrations and table definitions for the
mirror schema.

`prtags` should continue to own its migrations and table definitions for the
curation schema.

`prtags` should import `github.com/dutifuldev/ghreplica/mirror` and use
`mirror.NewSchemaReader(db, "ghreplica")` for mirror reads.

The dependency direction should be one-way:

```text
prtags -> ghreplica/mirror
ghreplica -> no prtags dependency
```

## Runtime Behavior

`prtags` should keep its existing read behavior:

- `group get` is refs-only by default
- metadata remains opt-in through `?include=metadata` and `--include-metadata`
- metadata reads use cached `target_projections`
- missing or stale projections enqueue background refresh jobs

The direct reader should be used by projection refresh and other background
paths that need mirrored GitHub object data.

Request handlers should not rebuild projections synchronously unless a specific
endpoint is intentionally designed for repair or operator use.

## Configuration

Use one normal Postgres connection for `prtags`.

That connection should point at the shared database, not at a separate
`prtags`-only database.

The connection must have:

- read and write access to the `prtags` schema
- read access to the `ghreplica` schema

The schema names should be configurable.

The shared deployment should set:

- `PRTAGS_SCHEMA=prtags` in the shared deployment
- `GHREPLICA_SCHEMA=public` for the current mirror schema

The code default can remain `public` for local and test databases that do not
use explicit schemas.

Tests should be able to override both schema names.

## Migration Strategy

The schema cutover should be explicit.

Recommended steps:

1. Ensure `ghreplica` can run with its tables under a configured schema.
2. Ensure `prtags` can run with its tables under a configured schema.
3. Create one shared database if one does not already exist.
4. Create both schemas in the shared database.
5. Ensure extensions required by either schema exist in the shared database.
   `prtags` currently requires `vector` for the `embeddings` table.
6. Apply `ghreplica` migrations into the `ghreplica` schema.
7. Apply `prtags` migrations into the `prtags` schema.
8. Move all existing `prtags` data from the old `prtags` database into the
   `prtags` schema when upgrading an existing deployment.
9. Verify table counts and key row counts match between the old `prtags`
   database and the new `prtags` schema.
10. Point `prtags` at the shared database.
11. Point `prtags` projection refresh code at `mirror.NewSchemaReader`.
12. Stop using the internal `ghreplica` HTTP client for mirror metadata reads.
13. Remove dead HTTP-client code after the direct reader path is fully covered.

The migration should not silently move or rewrite production data without an
operator-visible plan.

## Application Changes

### ghreplica

`ghreplica` should expose the reusable mirror package as the supported read
surface for Go consumers.

The package should include:

- stable row models for mirrored GitHub resources
- schema-aware table names
- typed readers for repositories, issues, pull requests, and users
- conversion helpers for GitHub-shaped objects and summaries

The public HTTP API should remain available and GitHub-compatible.

### prtags

`prtags` should replace internal mirror metadata calls with direct reader calls.

The intended shape is:

- initialize a shared GORM database connection
- initialize a `mirror.Reader` with the configured `ghreplica` schema
- pass that reader into projection refresh services
- keep projection cache writes in `prtags` tables
- keep HTTP handlers reading `target_projections` first

The existing `internal/ghreplica` package should shrink to aliases or be
deleted once all HTTP calls are gone.

## Safety Rules

Do not use `search_path` as the only safety mechanism.

Schema-qualified table access should be explicit in code or in the configured
table names passed to the reader.

Each service should only migrate the schema it owns.

`prtags` must not apply `ghreplica` migrations implicitly during normal startup.

`ghreplica` must not apply `prtags` migrations.

## Testing

Add tests for:

1. `prtags` projection refresh reads repository, issue, and pull request data
   through `mirror.Reader`
2. missing mirror objects still produce a retryable or visible projection error
3. stale projection refresh does not block normal group reads
4. schema-qualified reads work when `ghreplica` tables are not in the default
   schema
5. `prtags` tables are created and queried in the configured `prtags` schema

Integration tests should use temporary schemas or isolated databases so they do
not depend on a developer machine or a specific deployment.

## Production Verification

Before removing the HTTP-client fallback, verify:

- `ghreplica` health endpoint is healthy
- `prtags` health endpoint is healthy
- `prtags` can read an existing group without metadata
- `prtags` can read an existing group with metadata
- projection refresh succeeds for at least one pull request and one issue
- no request path performs unexpected live GitHub or HTTP mirror calls

The verification should use environment-provided service URLs and database
configuration. It should not depend on hardcoded machine names, cloud project
names, personal paths, or secrets.

## Rollback

Keep rollback simple:

- the public `ghreplica` HTTP API remains available
- the direct reader change should be isolated behind service initialization
- if direct reads fail during rollout, restore the previous projection refresh
  path and keep the schemas intact

Rollback should not require dropping schemas or rewriting data.

## Open Decisions

- whether to keep a temporary HTTP fallback during the first production rollout
- whether `prtags` should use one database connection or separate read/write and
  mirror-read connections
- whether schema creation belongs in deployment setup or a dedicated operator
  command
