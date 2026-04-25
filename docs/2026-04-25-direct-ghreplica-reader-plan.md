---
title: Unified ghreplica Reader Plan
date: 2026-04-25
status: revised
---

# Unified ghreplica Reader Plan

## Summary

`prtags` should use the shared Postgres database as the integration boundary
with `ghreplica`.

The first direct-reader cutover removed the internal HTTP hop, but it kept
`target_projections` as a small PR/issue metadata cache. That is no longer the
clean target architecture. Once `prtags` and `ghreplica` share one database,
`prtags` should read PR and issue metadata directly from the configured
`ghreplica` mirror schema.

This deprecates the separate `prtags` Postgres database deployment topology.
It does not deprecate the `prtags` data model, tables, schema, or ownership of
group data.

The model is:

- `ghreplica` owns mirrored GitHub data
- `prtags` owns groups, annotations, comment sync, and search indexes
- `prtags` imports the `ghreplica/mirror` Go package for typed mirror reads
- both systems keep separate database schemas inside the same Postgres database
- `target_projections` is removed because it duplicates mirror data

This keeps the product boundary explicit while removing both the internal
network hop and the redundant PR/issue projection cache.

## Goals

- let `prtags` enrich group members by reading mirrored GitHub objects directly
  from the shared database
- avoid duplicating `ghreplica` table definitions and GitHub-shaped conversion
  logic in `prtags`
- keep `ghreplica` independently runnable and useful as a standalone mirror
- keep `group get` refs-only by default
- make `include=metadata` hydrate directly from the mirror schema
- remove `target_projections` and `target_projection_refresh`
- make the shared database topology explicit and safe to migrate
- keep real derived indexes, such as `search_documents` and `embeddings`

## Non-Goals

- do not merge the `ghreplica` and `prtags` repositories
- do not move group, annotation, or comment-sync behavior into `ghreplica`
- do not make `ghreplica` depend on `prtags`
- do not remove the public `ghreplica` HTTP API
- do not make `group get` block on live GitHub or live `ghreplica` HTTP calls
- do not move `prtags` groups, annotations, or comments into `ghreplica`

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
`mirror.NewSchemaReader(db, configuredMirrorSchema)` for mirror reads.

The dependency direction should be one-way:

```text
prtags -> ghreplica/mirror
ghreplica -> no prtags dependency
```

`prtags` should store only curation and derived indexes:

- groups
- group members
- group comment sync targets
- field definitions
- field values
- events and event refs
- repository access grants
- search documents
- embeddings
- River tables
- schema migrations

`prtags` should not store generic copies of GitHub PR/issue display metadata.
That data already belongs to `ghreplica`.

## Runtime Behavior

`prtags` should keep refs-only group reads as the default:

- `group get` is refs-only by default
- metadata remains opt-in through `?include=metadata` and `--include-metadata`

When metadata is requested:

- `prtags` loads group members from `prtags.group_members`
- `prtags` reads matching PR or issue rows from the configured `ghreplica`
  mirror schema
- response metadata is built from current mirror rows
- if a mirror row is missing, the member remains in the response with a clear
  missing-mirror state or no object summary

The request path may read the mirror database. It must not call GitHub and must
not call the public `ghreplica` HTTP API.

There should be no `target_projection_refresh` jobs.

Comment sync, search result hydration, and annotation target resolution should
use the same mirror-read helper instead of each having its own partial mirror
lookup logic.

`search_documents` and `embeddings` remain materialized because they are search
indexes, not GitHub metadata caches.

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

The schema cutover should be explicit and should remove leftover projection
infrastructure.

Recommended steps:

1. Ensure the shared database already contains the `ghreplica` mirror schema and
   the `prtags` curation schema.
2. Ensure extensions required by either schema exist in the shared database.
   `prtags` currently requires `vector` for the `embeddings` table.
3. Verify the running `prtags` deployment already points at the shared database
   with `PRTAGS_SCHEMA=prtags` and the configured mirror schema.
4. Add a shared mirror metadata reader in `prtags`.
5. Replace `group get --include-metadata` hydration with direct mirror reads.
6. Replace comment sync metadata hydration with direct mirror reads.
7. Replace search result hydration with direct mirror reads.
8. Replace annotation target resolution that only needs PR/issue existence or
   display metadata with direct mirror reads.
9. Delete all `target_projection_refresh` River job types, workers, queues, and
   enqueue paths.
10. Delete the `target_projections` model and all application reads and writes.
11. Add a migration that removes stale `target_projection_refresh` jobs from
    River tables and drops `target_projections`.
12. Update tests so they seed mirror rows and assert metadata changes when the
    mirror rows change.
13. Deploy with all `prtags` HTTP and worker processes stopped or recreated as
    one unit so no old process runs after `target_projections` is dropped.

The migration should not silently move or rewrite production data without an
operator-visible plan.

### Drop Migration

The drop migration should be explicit.

It should:

- delete queued, available, retryable, or scheduled River jobs whose kind is
  `target_projection_refresh`
- remove the `target_projection_refresh` queue row if River keeps one and no
  other job uses it
- drop the `target_projections` table
- not touch `search_documents`
- not touch `embeddings`
- not touch `group_members`
- not touch `group_comment_sync_targets`

The migration should be safe to run after the new binary is deployed. The new
binary must not reference `target_projections`.

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

`prtags` should replace projection-cache metadata calls with direct mirror
reader calls.

The intended shape is:

- initialize a shared GORM database connection
- initialize a `mirror.Reader` with the configured `ghreplica` schema
- pass a mirror metadata reader into services that need PR/issue display data
- keep `group_members` as refs only
- hydrate metadata by reading mirror rows directly
- keep search index writes in `search_documents` and `embeddings`
- keep comment sync state in `group_comment_sync_targets`

The existing `internal/ghreplica` package should shrink to aliases or be
deleted once core code can use `ghreplica/mirror` directly without awkward
coupling.

### Mirror Metadata Helper

Create one internal helper for mirror metadata.

Inputs:

- `github_repository_id`
- target type: `pull_request` or `issue`
- object number

Output:

- title
- state
- HTML URL
- author login
- updated time
- found or missing state

Batch input should be supported so group metadata hydration does not do one
query per member.

The helper should use the configured mirror schema and `ghreplica/mirror` table
definitions. If `mirror.Reader` does not expose the needed batch API, add that
API to `ghreplica/mirror` first instead of duplicating table definitions in
`prtags`.

### Group Reads

`group get` without metadata should continue to read only `prtags` tables.

`group get --include-metadata` should:

- load group and members from `prtags`
- batch read mirror metadata for PR/issue members
- preserve the existing member order
- include object summaries for found mirror rows
- mark or omit summaries for missing mirror rows without failing the whole
  group read

Remove `object_summary_freshness` unless a new direct-read status is genuinely
needed. Freshness was a projection-cache concern.

### Comment Sync

Comment sync should render tables from:

- `prtags.groups`
- `prtags.group_members`
- `prtags.field_values`
- current `ghreplica` mirror rows

It should not depend on `target_projections`.

If a member is missing in the mirror, the comment should still be deterministic.
Use the PR or issue number and omit fields that are unavailable. Do not fail the
entire group comment sync just because one mirror row is missing, unless the
GitHub API write itself fails.

### Search

Keep `search_documents` and `embeddings`.

They are derived indexes for `prtags` annotations and group data. They are not a
generic PR/issue metadata cache.

Search result responses should hydrate display metadata from mirror rows after
the search query returns target keys.

If a mirror row is missing, return the search hit with the target ref and no
object summary.

### Annotation Target Resolution

When an annotation is set on a PR or issue, `prtags` should validate the target
against the mirror schema directly.

It should not create or refresh a `target_projection`.

The target key remains stable:

```text
repo:<github_repository_id>:<target_type>:<number>
```

## Safety Rules

Do not use `search_path` as the only safety mechanism.

Schema-qualified table access should be explicit in code or in the configured
table names passed to the reader.

Each service should only migrate the schema it owns.

`prtags` must not apply `ghreplica` migrations implicitly during normal startup.

`ghreplica` must not apply `prtags` migrations.

No old `prtags` HTTP server or worker process may run after the migration drops
`target_projections`.

The deployment should treat HTTP and worker processes as one release unit for
this cutover.

## Testing

Add tests for:

1. `group get --include-metadata` reads repository, issue, and pull request data
   from mirror rows
2. metadata changes when mirror rows change, without running any refresh job
3. missing mirror objects do not fail refs-only group reads
4. missing mirror objects have deterministic metadata behavior for
   `include=metadata`
5. comment sync renders from direct mirror metadata
6. search result hydration reads mirror metadata directly
7. annotation writes validate PR/issue targets through direct mirror reads
8. schema-qualified reads work when `ghreplica` tables are not in the default
   schema
9. `prtags` tables are created and queried in the configured `prtags` schema
10. no code enqueues `target_projection_refresh`
11. the drop migration removes `target_projections`

Integration tests should use temporary schemas or isolated databases so they do
not depend on a developer machine or a specific deployment.

## Production Verification

Before deployment:

- `ghreplica` health endpoint is healthy
- `prtags` health endpoint is healthy
- `prtags` is already using the shared database
- the configured `prtags` schema contains the expected production tables
- the configured mirror schema contains repository, issue, and pull request rows
- a fresh logical backup exists for the shared database or at least the
  `prtags` schema
- no separate worker process will continue running the old binary

During deployment:

1. Stop all `prtags` HTTP and worker processes.
2. Deploy the new binary and migrations.
3. Start `prtags`.
4. Confirm startup migrations completed.
5. Confirm the container or process is running the expected commit.

After deployment:

- `prtags` health endpoint is healthy
- `prtags` can read an existing group without metadata
- `prtags` can read an existing group with metadata
- metadata reflects current mirror rows
- search text still returns results and hydrates PR/issue display fields
- comment sync can render an existing group without writing duplicate comments
- River has no `target_projection_refresh` jobs
- `target_projections` no longer exists in the `prtags` schema
- logs contain no missing-table errors, panics, or repeated retry loops
- `ghreplica` health endpoint is still healthy
- at least one direct `ghreplica` GitHub-shaped read endpoint still works

The verification should use environment-provided service URLs and database
configuration. It should not depend on hardcoded machine names, cloud project
names, personal paths, or secrets.

## Full Cutover Checklist

Use this checklist for the production cutover.

1. Confirm local repo is clean.
2. Create a branch for the refactor.
3. Implement the direct mirror metadata helper.
4. Replace group metadata hydration.
5. Replace comment sync hydration.
6. Replace search result hydration.
7. Replace annotation target resolution.
8. Remove target projection refresh jobs from River configuration.
9. Remove target projection refresh worker and enqueue code.
10. Remove `TargetProjection` model usage.
11. Add migration to delete stale target projection jobs and drop the table.
12. Update docs and env examples.
13. Run unit tests.
14. Run coverage gate.
15. Run lint.
16. Run doc checks.
17. Run `codex review --base main` and fix P0/P1 issues.
18. Open or update PR.
19. Wait for CI to pass.
20. Merge.
21. Stop all production `prtags` processes.
22. Backup the shared database or `prtags` schema.
23. Deploy from `main`.
24. Verify migrations ran.
25. Smoke test health, group reads, metadata reads, field reads, search, and
    comment rendering.
26. Verify `target_projections` is gone.
27. Verify no target projection jobs remain.
28. Verify production logs are clean.
29. Leave the repo on `main` with a clean worktree.

## Rollback

Keep rollback simple:

- the public `ghreplica` HTTP API remains available
- the shared database schemas remain intact
- the dropped `target_projections` table is derived data and can be rebuilt only
  if a rollback binary still needs it
- if direct reads fail during rollout, restore the previous image and restore
  `target_projections` from backup or from a rebuild script before starting the
  old binary

Rollback should not require moving `prtags` back to a separate database.

## Open Decisions

- whether `prtags` should use one database connection or separate read/write and
  mirror-read connections
- whether missing mirror rows in `include=metadata` should omit summaries or
  include an explicit `missing_mirror` marker
- whether comment sync should fail or degrade when a mirror row is missing
