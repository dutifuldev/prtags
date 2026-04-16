---
title: Production Implementation Plan
date: 2026-04-16
status: proposed
---

# Production Implementation Plan

This document turns the current `prtags` design into a concrete implementation plan for a production-ready system.

The main goal is to build a customizable annotation layer on top of `ghreplica` without polluting the mirrored GitHub model. `prtags` should store only references to GitHub-native objects plus human-added structure such as groups, field definitions, field values, search documents, and embeddings.

## Goals

`prtags` should support all of the following as first-class concepts:

- groups of pull requests
- groups of issues
- custom metadata on pull requests, issues, and groups
- efficient exact filtering over typed metadata
- efficient full-text search over selected metadata fields
- efficient vector search over selected metadata fields

The design should not hardcode field names like `intent`, `quality`, or `theme`. Repositories should define those fields at runtime, and `prtags` should build the right indexes from field capabilities.

## Non-Goals

`prtags` should not:

- replace `ghreplica` as the source of truth for PR and issue content
- duplicate full GitHub objects unnecessarily
- force deployers to change Go code to define repo-specific metadata fields
- mix curation metadata into GitHub-compatible responses

## Core Design Rules

The implementation should follow these rules:

1. Canonical curation data stays separate from derived search and embedding data.
2. Repository-specific field definitions are runtime data in the database.
3. Search behavior is driven by field capabilities, not hardcoded field names.
4. Exact filters, full-text search, and vector search each get their own optimized storage path.
5. `ghreplica` remains the source of truth for mirrored GitHub content.
6. Shared group membership is the main association model for related PRs and issues.

## Implementation Stack

The implementation stack should be pinned down now.

The default backend stack for `PRtags` should be:

- Echo for HTTP routing, middleware, and request handling
- GORM for the main persistence layer and transactional writes
- PostgreSQL as the primary database
- SQL migrations in-repo as the schema source of truth

This does not mean every query should be forced through high-level ORM abstractions. The production rule should be:

- use GORM for the core CRUD model and normal transactional writes
- use explicit SQL, GORM raw queries, or database-specific migration steps for PostgreSQL-native features such as:
  - full-text search indexes
  - vector indexes
  - partial indexes
  - hybrid ranking queries

That keeps the main service code ergonomic while still leaving room for the parts of the system that need direct control over SQL and index behavior.

## Storage Layers

The production shape should have three storage layers.

### 1. Canonical Curation Tables

These tables are the real source of truth for `prtags`:

- `groups`
- `group_members`
- `field_definitions`
- `field_values`

This layer stores:

- object references
- user-created groups
- repo-defined annotation schemas
- typed annotation values

This layer should not store full duplicated PR or issue bodies.

### 2. Derived Full-Text Search Tables

This layer exists only to serve FTS well.

Suggested tables:

- `search_documents`
- optionally `search_document_fields` later if per-field excerpts become important

Each `search_documents` row should represent one annotated object, such as:

- a pull request reference
- an issue reference
- a group

It should contain:

- repository identity
- target type
- target identity
- combined searchable text derived from fields marked `is_searchable`
- a `tsvector`
- freshness metadata

This layer should be rebuilt from canonical data, not written to directly by end users.

### 3. Derived Embedding Tables

This layer exists only for vector search.

Suggested table:

- `embeddings`

Each row should represent one object embedding built from fields marked `is_vectorized`.

It should contain:

- repository identity
- target type
- target identity
- embedding source text
- embedding model identifier
- embedding vector
- freshness metadata

Like the FTS layer, this is derived data and should be rebuildable from the canonical layer.

## Canonical Schema

### `groups`

`groups` should hold user-created group records.

Suggested columns:

- `id`
- `public_id`
- `github_repository_id`
- `repository_owner`
- `repository_name`
- `kind`
  - `pull_request`
  - `issue`
  - `mixed`
- `title`
- `description`
- `status`
- `created_by`
- `created_at`
- `updated_at`

Important indexes:

- unique `(public_id)`
- `(github_repository_id, kind, status)`
- `(github_repository_id, updated_at desc)`

`public_id` should follow the group public-ID rules in [PUBLIC_IDS.md](./PUBLIC_IDS.md), not expose the bare numeric primary key. At the API and CLI layer, the externally exposed field should simply be `id`.

### `group_members`

`group_members` should hold membership only.

Suggested columns:

- `id`
- `group_id`
- `github_repository_id`
- `repository_owner`
- `repository_name`
- `object_type`
  - `pull_request`
  - `issue`
- `object_number`
- `added_by`
- `added_at`

Important constraints and indexes:

- unique `(group_id, object_type, object_number)`
- index `(github_repository_id, object_type, object_number)`
- foreign key from `group_id` to `groups(id)`

### `field_definitions`

`field_definitions` should define runtime metadata fields per repo.

Suggested columns:

- `id`
- `github_repository_id`
- `repository_owner`
- `repository_name`
- `name`
- `object_scope`
  - `pull_request`
  - `issue`
  - `group`
  - `all`
- `field_type`
  - `string`
  - `text`
  - `boolean`
  - `integer`
  - `enum`
  - `multi_enum`
- `enum_values_json`
- `is_required`
- `is_filterable`
- `is_searchable`
- `is_vectorized`
- `sort_order`
- `created_at`
- `updated_at`
- `archived_at`

Important constraints and indexes:

- unique `(github_repository_id, name, object_scope)`
- index `(github_repository_id, object_scope)`

### `field_values`

`field_values` should hold actual annotation values in typed form.

Suggested columns:

- `id`
- `field_definition_id`
- `target_type`
  - `pull_request`
  - `issue`
  - `group`
- `github_repository_id`
- `repository_owner`
- `repository_name`
- `target_key`
  - stable synthetic identifier for the target
- `string_value`
- `text_value`
- `bool_value`
- `int_value`
- `enum_value`
- `multi_enum_json`
- `updated_by`
- `updated_at`

Important constraints and indexes:

- unique `(field_definition_id, target_type, target_key)`
- index `(github_repository_id, target_type, target_key)`
- partial indexes for filterable value columns where useful:
  - `(field_definition_id, enum_value)`
  - `(field_definition_id, bool_value)`
  - `(field_definition_id, int_value)`
  - `(field_definition_id, string_value)`

## Target Identity

Because `prtags` is a separate service, it should use stable explicit target identities rather than database joins into `ghreplica`.

The canonical identity model should be structured columns:

- `github_repository_id`
- `target_type`
- `object_number` for mirrored GitHub objects
- local `group_id` for local group objects

For pull requests and issues, this means the source-of-truth identity is:

- `github_repository_id`
- `target_type`
- `object_number`

That is better than using `owner/name` as the canonical key because repository names can change on rename or transfer. `github_repository_id` stays stable.

A human-friendly key like:

- `openclaw/openclaw#59883`

can still exist, but only as a derived display or cache key, not as the canonical identity.

`repository_owner` and `repository_name` should still be stored where useful for display, API routing, and CLI output, but they should be treated as the current locator, not the source of truth.

For `field_values`, `search_documents`, and `embeddings`, the implementation may still keep a derived `target_key` for convenience. If it does, that key should be generated from the canonical structured identity rather than replacing it.

## Derived Search Model

### Search Documents

For each target, `prtags` should derive one search document from:

- all field values whose definitions are marked `is_searchable`
- optionally small projected GitHub fields later, such as PR title or issue title

The important implementation rule is that field definitions drive inclusion. The code should never assume specific field names.

Suggested columns:

- `id`
- `github_repository_id`
- `repository_owner`
- `repository_name`
- `target_type`
- `target_key`
- `search_text`
- `search_vector`
- `source_updated_at`
- `indexed_at`

Important indexes:

- unique `(github_repository_id, target_type, target_key)`
- GIN on `search_vector`

### Embeddings

For each target, `prtags` should derive one embedding input text from:

- all field values whose definitions are marked `is_vectorized`

The standard production model should be:

- one configured default embedding provider and model at a time
- one explicit model identifier stored on every embedding row
- one rebuild whenever the vectorized source text changes
- one rebuild whenever the configured default model version changes

The code should not try to infer whether an embedding is current from the vector alone. It should compare the embedding row against the canonical source state and the currently configured model identifier.

Suggested columns:

- `id`
- `github_repository_id`
- `repository_owner`
- `repository_name`
- `target_type`
- `target_key`
- `embedding_text`
- `embedding_model`
- `embedding`
- `source_updated_at`
- `indexed_at`

The `embedding_model` value should be explicit and versioned, for example:

- `text-embedding-3-large@2026-04`

That gives the system a stable way to decide whether older rows are still current after a model rollout.

Important indexes:

- unique `(github_repository_id, target_type, target_key, embedding_model)`
- vector index appropriate to the chosen vector extension and distance metric

## Rebuild Model

Derived FTS rows and embeddings should be rebuilt asynchronously.

The clean production shape is:

1. canonical writes update `field_values`
2. the write path marks the affected target as needing search rebuild
3. background workers rebuild search documents and embeddings
4. rebuild state is tracked explicitly

For embeddings specifically, the rebuild triggers should be:

- a write to any field whose definition is marked `is_vectorized`
- a change to field definitions that changes which fields are vectorized
- a configured default embedding-model change
- an explicit repo-level rebuild request

Suggested rebuild-state tables:

- `search_rebuild_queue`
- or a more generic `index_jobs` table

Each queued job should carry:

- repository identity
- target type
- target key
- rebuild kind
  - `search_document`
  - `embedding`
- status
- attempt count
- last error
- lease owner
- heartbeat

Embedding rebuilds should happen in the background, not inline on normal writes or reads. Canonical writes should stay cheap, and vector queries should only read from the finished `embeddings` table.

This is the same operational shape that worked well in `ghreplica`: explicit leases, heartbeats, and resumable derived work.

## Freshness Semantics

Search and embedding freshness should be explicit.

For embeddings, the clean status model is:

- `current`
  - built from the current canonical vectorized text
  - built with the currently configured embedding model identifier
- `stale`
  - exists, but either the source text changed or the configured model changed
- `missing`
  - no embedding row exists for the target and configured model

This should be exposed at least at repo level and ideally also at target level for debugging and rebuild inspection.

## Query Paths

### Exact Metadata Filters

Exact metadata filters should query `field_values` directly using the typed value columns and field-definition IDs.

This is the right path for queries like:

- high-quality PRs
- issues with `customer_visible = true`
- groups with `theme = auth`

### Full-Text Search

FTS should query `search_documents`.

This is the right path for queries like:

- search annotation text for “flaky auth retries”
- search group descriptions and notes
- search PR intent summaries

### Vector Search

Vector search should query `embeddings`.

This is the right path for queries like:

- find PRs with similar intent
- find groups that are semantically close to this issue group

### Hybrid Search

Long term, `prtags` should support filtered vector search and filtered FTS by combining:

- metadata filters from `field_values`
- semantic candidates from `embeddings`
- optional ranking signals from `search_documents`

That should be implemented as a composed query path, not by collapsing everything into one table.

## API Surface

The production API should be split cleanly.

All `PRtags` JSON API responses should use JSend as the response envelope format. The reference for that contract should live in [JSEND.md](JSEND.md).

The practical rule should be:

- `success` for successful reads and writes, with the result under `data`
- `fail` for validation errors, bad input, or unmet preconditions
- `error` for server-side failures

`PRtags` should still use normal HTTP status codes alongside JSend bodies. JSend is the body contract, not a replacement for HTTP semantics.

### Write Semantics

The production write model should be:

- `GET` for reads
- `POST` for creation
- `PATCH` for partial updates to existing resources
- explicit action endpoints for membership operations

That means the API should prefer shapes like:

- `POST /groups`
- `PATCH /groups/{id}`
- `POST /groups/{id}/members`
- `DELETE /groups/{id}/members/{member_id}`

Every mutating request should support an `Idempotency-Key`.

The implementation should also support optimistic concurrency on mutable resources, using a simple version token such as:

- `row_version`
- or `updated_at`

That keeps writes safe under retries and concurrent edits without overcomplicating the first implementation.

### JSend Error Policy

The JSend mapping should be explicit:

- `success`
  - any successful `2xx` response
- `fail`
  - validation errors
  - bad input
  - unmet preconditions
  - permission denials
  - business-level conflicts
  - normal not-found cases inside the product domain
- `error`
  - unexpected server failures
  - dependency outages
  - timeouts
  - queue or rebuild infrastructure failures

The `message` field should stay short and stable.

Machine-readable details should go in `data`.

### Schema Management

- create field definitions
- update field definitions
- archive field definitions
- list field definitions for a repo
- import a field-definition manifest
- export a field-definition manifest

Manifest import should be additive and updating by default. It may create new fields and update allowed properties on existing ones, but it should not silently rename, archive, or delete fields. Destructive lifecycle operations should remain explicit API or CLI actions.

### Field Evolution Rules

Field lifecycle should be conservative.

The production rules should be:

- fields may be created
- fields may be updated in non-destructive ways
- fields may be archived
- fields should not be silently deleted once they have values

For enum fields:

- new enum values may be added
- existing enum values may be deprecated
- enum values should only be removed once they are no longer in use

For required fields:

- making a field required should apply to future writes
- it should not retroactively invalidate existing stored values

For renames:

- rename should mean changing the field label or display name
- the canonical field identity should stay stable
- destructive rename behavior should not be inferred from manifest import

### Group Management

- create a group
- update a group
- list groups
- fetch one group
- add a member to a group
- remove a member from a group

### Annotation Management

- set metadata on a PR
- set metadata on an issue
- set metadata on a group
- get metadata for a target
- list targets by field filter

### Search

- full-text search over annotated targets
- vector similarity search over annotated targets
- hybrid filtered search

## CLI Surface

The CLI should mirror the API cleanly.

Suggested shape:

- `prtags field create`
- `prtags field list`
- `prtags field import`
- `prtags field export`
- `prtags group create`
- `prtags group add-pr`
- `prtags group add-issue`
- `prtags pr set`
- `prtags issue set`
- `prtags group set`
- `prtags search text`
- `prtags search similar`

## Auditability And History

A production system should not rely only on last-write-wins state.

The best shape is:

- current-state tables for normal reads
- one append-only `events` table for immutable history
- one `event_refs` table for additional objects involved in each event

This is better than full event-sourcing for the first real implementation because reads stay simple and fast, but the system still gets auditability, debugging value, rebuild support, and room for undo or replay later.

The event log should cover:

- field-definition changes
- group creation and updates
- group membership changes
- annotation value changes

### Event Vocabulary

The event vocabulary should stay small, boring, and explicit.

The core event types should be:

- `group.created`
- `group.updated`
- `group.member_added`
- `group.member_removed`
- `field_definition.created`
- `field_definition.updated`
- `field_definition.archived`
- `field_value.set`
- `field_value.cleared`

The system should not start with highly specialized one-off event names unless they represent genuinely distinct domain actions.

### `events`

Suggested columns:

- `id`
- `github_repository_id`
- `repository_owner`
- `repository_name`
- `aggregate_type`
- `aggregate_key`
- `sequence_no`
- `event_type`
- `actor_type`
- `actor_id`
- `request_id`
- `idempotency_key`
- `schema_version`
- `payload_json`
- `metadata_json`
- `occurred_at`

Each event should have one primary subject:

- `aggregate_type`
- `aggregate_key`

and a per-aggregate `sequence_no` so the history for each object is ordered cleanly.

### `event_refs`

Suggested columns:

- `event_id`
- `ref_role`
- `ref_type`
- `ref_key`

This lets one event point at other involved objects without turning `payload_json` into an unstructured mess.

Examples:

- setting PR intent
  - aggregate = the PR
  - ref = the field definition
- adding a PR to a group
  - aggregate = the group
  - ref = the PR member

The canonical tables remain the current state, and `events` plus `event_refs` provide immutable history.

## Permissions

The permissions model should be derived from GitHub repository permissions, not from a separate local membership model.

The correct default rule is:

- a user may modify `prtags` data for a repo only if they currently have GitHub write access to that repo

For `prtags`, that means:

- authenticate users with GitHub
- treat the GitHub user identity as the actor identity
- check the user’s permission on the target repository for mutating actions
- allow writes for GitHub permission levels equivalent to `write`, `maintain`, or `admin`

This should be repository-scoped. `prtags` should not try to model organization membership rules on its own.

### Permission Source Of Truth

GitHub should remain the source of truth for repository permissions.

`prtags` should not maintain a separate long-lived synchronization of repo membership. Instead, it should:

- cache permission checks for a short TTL
- re-check GitHub on writes when the cache is missing or expired

That means if someone loses repo write access on GitHub, `prtags` will stop allowing writes once the cached permission result expires.

### Permission Cache

The implementation may keep a small permission cache for efficiency.

Suggested cached fields:

- GitHub user identity
- repository identity
- resolved repo permission
- checked_at
- expires_at

This cache is an optimization only. It is not the source of truth.

### Read Behavior

The write-authorization rule should be pinned down now.

Read behavior can remain simpler at first. The default product choice may be:

- public reads for data that is already being exposed publicly through `ghreplica`
- write access restricted by GitHub repo permission
- private-repo reads only for users who have GitHub read access to that repo

If read restrictions become necessary later, they should follow the same GitHub-derived identity model.

The schema should carry `created_by` and `updated_by` fields consistently even before full authorization exists.

## Authentication

The authentication model should be pinned down separately from the write-permission model.

`PRtags` is CLI-first, so the primary interactive login flow should be GitHub OAuth device flow rather than browser-first session login.

The production choice should be:

- a GitHub OAuth App for human login into `PRtags`
- GitHub.com as the initial provider
- device flow as the primary interactive auth flow for the CLI
- bearer-token auth kept as a scripting and automation fallback

This should stay separate from `ghreplica`'s GitHub App. The GitHub App is for mirroring and repo automation. `PRtags` should use an OAuth App for user authentication.

### Initial OAuth Configuration

The initial pinned configuration should be:

- provider: GitHub.com
- auth mechanism: GitHub OAuth App
- primary interactive flow: device flow
- public callback URL reserved for future browser auth: `https://prtags.dutiful.dev/auth/github/callback`
- requested scopes: `read:org repo`

The reasoning for the initial scopes is:

- `read:org`
  - needed to inspect organization and team membership when that matters for access checks or product behavior
- `repo`
  - needed for private-repo support and for repository-owned resources such as pull requests and issues

This should be treated as the default production scope set. If a later deployment is explicitly public-repo-only, the scope choice can be narrowed then, but the default long-term product stance should be `read:org repo`.

### CLI Auth Commands

The CLI should grow a small explicit auth surface:

- `prtags auth login`
- `prtags auth status`
- `prtags auth logout`

`prtags auth login` should:

- request a device code from GitHub
- show the verification URL and user code
- poll until the user completes authorization
- fetch the authenticated GitHub user
- store the token locally with restrictive file permissions

### Token Storage

The CLI should store the device-flow token locally rather than requiring users to keep exporting tokens manually.

The production shape should be:

- local auth file under the user's config directory
- file mode `0600`
- enough metadata to show the logged-in GitHub user and the granted scopes

The stored token should then be used automatically by the CLI when no explicit environment token is present.

### Auth Resolution Order

The CLI should resolve GitHub auth in this order:

1. `PRTAGS_GITHUB_TOKEN`
2. locally stored device-flow token
3. `GITHUB_TOKEN`
4. `GH_TOKEN`

That keeps explicit overrides working while still making `prtags auth login` useful as the normal day-to-day path.

### Web Login

Browser-based login and session cookies may exist later, but they should not be the first-class auth path for the initial product.

If web login is added later, it should use the same OAuth App and the reserved callback route:

- `/auth/github/login`
- `/auth/github/callback`
- `/auth/logout`

That future web flow should be additive. It should not replace device flow as the main interactive auth story for the CLI.

### Initial Auth Validation Targets

The first real auth validation should happen against `dutifuldev` repositories.

The initial targets should be:

- `dutifuldev/ghreplica`
- `dutifuldev/prtags`

These are the right first auth targets because:

- they are controlled by the same operator who owns the OAuth app
- the expected write permissions are easy to reason about
- they avoid unrelated org-policy noise while the auth flow is still settling

Only after the login flow and write-permission checks are stable there should `PRtags` expand auth testing to other repos and org contexts.

## Local Projection Policy

Because `prtags` is a separate service, it may optionally keep a small projected cache of GitHub object fields for display and search convenience.

That projection should be:

- clearly marked as cached
- small in scope
- rebuildable from `ghreplica`
- never treated as the source of truth

Suggested projected fields:

- title
- state
- author
- updated_at
- html_url

This is useful for list views and result rendering, but it should remain optional and secondary.

The refresh model should be:

- lazy refresh on reads when cached data is missing or obviously stale
- background refresh during indexing and rebuild jobs
- explicit freshness metadata on the cached projection

If `ghreplica` is unavailable, `PRtags` should be allowed to serve cached projected data with an explicit freshness signal instead of failing hard for every read.

The projection should stay intentionally small:

- `title`
- `state`
- `author`
- `updated_at`
- `html_url`

Anything beyond that should need a strong reason.

## Background Job Policy

The production background-job shape should be explicit and shared across rebuild work.

Use one generic `index_jobs` table with:

- `kind`
- `status`
- `attempt_count`
- `lease_owner`
- `heartbeat_at`
- `next_attempt_at`
- `last_error`

Rebuilds should run asynchronously with:

- leases
- heartbeats
- retries
- exponential backoff
- repo-level batching where possible

The first job kinds should be:

- `search_document_rebuild`
- `embedding_rebuild`

Operator-facing status should exist at repo level for:

- queue depth
- in-progress work
- last success
- last error
- freshness state

## Observability

Production readiness requires explicit observability for both canonical and derived layers.

Important metrics:

- field-definition count per repo
- field-value write rate
- search-document rebuild latency
- embedding rebuild latency
- queue depth
- rebuild failures
- vector-search latency
- FTS latency

Important logs:

- field-definition changes
- rebuild job start and finish
- rebuild failures
- manifest import results

## Testing Strategy

The testing strategy should match the layered architecture.

The main rule is:

- test canonical state behavior directly
- test API contracts at the HTTP layer
- test database behavior against real PostgreSQL
- test derived index pipelines separately from canonical writes

### 1. Model And Service Tests

These tests should target the core domain logic without going through the full HTTP stack.

They should cover:

- group creation and updates
- member add and remove
- field-definition creation, update, archive, and manifest import
- field-value set and clear
- event creation and per-aggregate sequencing
- field-evolution rules
  - additive enum changes
  - required-field behavior
  - archive behavior
- permission decisions from cached and refreshed GitHub permission state

The goal is to prove the main product rules without needing a full end-to-end environment for every case.

### 2. API Contract Tests

These tests should hit Echo handlers directly.

They should verify:

- JSend envelopes
- HTTP status codes
- validation failures
- permission failures
- idempotency behavior
- optimistic-concurrency behavior
- stable response shapes for group, field, annotation, and search endpoints

Important cases:

- successful `POST`, `PATCH`, and action calls return `success`
- validation and precondition problems return `fail`
- server-side failures return `error`

### 3. PostgreSQL Integration Tests

These tests should use a real PostgreSQL database.

They should verify:

- uniqueness and foreign-key constraints
- `github_repository_id`-based identity behavior
- rename-safe identity handling
- partial indexes for filterable fields
- full-text search indexes and queries
- vector index behavior once embeddings are implemented
- event ordering and uniqueness constraints
- job leasing and heartbeat behavior

This layer matters because many of the important production guarantees depend on real PostgreSQL semantics, not in-memory mocks.

### 4. Derived Index Tests

Derived index behavior should be tested separately from canonical CRUD.

These tests should cover:

- search-document rebuild after searchable-field changes
- embedding rebuild after vectorized-field changes
- stale versus current freshness transitions
- model-version rebuild behavior
- repo-level freshness reporting
- retry and backoff behavior for failed rebuild jobs

The important production rule is that canonical writes stay cheap while derived work happens asynchronously, so tests should prove that split explicitly.

### 5. Dependency-Behavior Tests

Because `PRtags` depends on `ghreplica`, GitHub auth, and an embedding provider, those integrations should be tested through stubs or fakes.

These tests should cover:

- projection refresh from `ghreplica`
- stale projection fallback when `ghreplica` is unavailable
- GitHub-derived permission checks
- permission-cache expiry behavior
- embedding-provider failure behavior
- idempotent rebuild requeue behavior

The goal is to prove that the service behaves predictably when dependencies are slow, stale, or failing.

### 6. End-To-End Scenarios

The system should also have a smaller number of realistic end-to-end tests.

The first important scenarios are:

- create fields, create a group, add PRs and issues, set annotations, and read them back
- import a manifest, then annotate objects under that schema
- change searchable or vectorized fields and observe derived-index rebuilds
- rename a repo in `ghreplica` and verify `PRtags` still resolves the same stable targets
- remove a user’s GitHub write permission and verify writes stop after permission-cache expiry

These are the tests that prove the whole architecture holds together, not just the individual pieces.

### 7. Rollout Validation

Before calling the system production-ready, validation should include:

- migration smoke tests
- API smoke tests against a deployed environment
- rebuild worker smoke tests
- queue and lease recovery tests
- permission-check smoke tests with real GitHub auth

The rollout check should explicitly verify:

- writes succeed for users with repo write access
- writes fail for users without repo write access
- canonical data remains consistent under retries
- JSend responses match the documented contract
- derived FTS and vector state can become current after writes

The first rollout targets should be explicit:

- start with `dutifuldev/ghreplica`
- then expand to `openclaw/openclaw`

`dutifuldev/ghreplica` is the right first validation target because it is small, controlled, and easy to inspect end to end while the system is still settling.

`openclaw/openclaw` should be the next validation target because it provides the larger and noisier real-world data needed to test projection freshness, search quality, and operational behavior at higher scale.

## Vector Search Defaults

The first production vector stack should be:

- `pgvector` as the PostgreSQL vector extension
- cosine distance as the default similarity metric
- one active embedding model per environment at a time

Embedding text should be built deterministically from vectorized fields in a stable order so rebuilds are reproducible.

## Rollout Plan

The implementation should happen in phases.

### Phase 1: Canonical CRUD

Build:

- `groups`
- `group_members`
- `field_definitions`
- `field_values`
- `events`
- `event_refs`

Ship:

- field-definition CRUD
- group CRUD
- annotation CRUD
- exact filter queries
- immutable audit history for every canonical write

### Phase 2: Derived Full-Text Search

Build:

- `search_documents`
- rebuild queue and worker
- repo-level search freshness/status

Ship:

- text search over annotation fields marked `is_searchable`

### Phase 3: Derived Vector Search

Build:

- `embeddings`
- embedding rebuild pipeline
- model/version tracking
- embedding freshness/status reporting
- vector search endpoints

Ship:

- semantic search over fields marked `is_vectorized`

### Phase 4: Hybrid Search And Projection

Build:

- filtered semantic search
- optional cached GitHub projection
- better ranking and excerpts

Ship:

- higher-quality search UX over real annotated repos

## Remaining Open Decisions

The core implementation direction is now pinned down enough to start building.

The remaining open items are narrower:

- whether cached GitHub projection is needed in phase 1 or can wait until phase 4
- which concrete embedding provider and model identifier to adopt first

Those details matter, but they do not block implementation of the durable core model.

## Recommendation

The next concrete step should be Phase 1. Build the canonical schema and CRUD paths first, because that forces the real identity model, group model, field-definition model, and typed-value model to become concrete. Full-text search and vector search should follow as explicit derived layers on top of that canonical core, not as a shortcut around it.
