---
title: Production Implementation Plan
date: 2026-04-16
status: proposed
---

# Production Implementation Plan

This document turns the current `ghanno` design into a concrete implementation plan for a production-ready system.

The main goal is to build a customizable annotation layer on top of `ghreplica` without polluting the mirrored GitHub model. `ghanno` should store only references to GitHub-native objects plus human-added structure such as groups, links, field definitions, field values, search documents, and embeddings.

## Goals

`ghanno` should support all of the following as first-class concepts:

- groups of pull requests
- groups of issues
- links between groups
- custom metadata on pull requests, issues, and groups
- efficient exact filtering over typed metadata
- efficient full-text search over selected metadata fields
- efficient vector search over selected metadata fields

The design should not hardcode field names like `intent`, `quality`, or `theme`. Repositories should define those fields at runtime, and `ghanno` should build the right indexes from field capabilities.

## Non-Goals

`ghanno` should not:

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

## Storage Layers

The production shape should have three storage layers.

### 1. Canonical Curation Tables

These tables are the real source of truth for `ghanno`:

- `groups`
- `group_members`
- `group_links`
- `field_definitions`
- `field_values`

This layer stores:

- object references
- user-created groups
- relationships between groups
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

- `(repository_owner, repository_name, kind, status)`
- `(repository_owner, repository_name, updated_at desc)`

### `group_members`

`group_members` should hold membership only.

Suggested columns:

- `id`
- `group_id`
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
- index `(repository_owner, repository_name, object_type, object_number)`
- foreign key from `group_id` to `groups(id)`

### `group_links`

`group_links` should hold relationships between groups.

Suggested columns:

- `id`
- `from_group_id`
- `to_group_id`
- `relationship_type`
- `created_by`
- `created_at`

Important constraints and indexes:

- unique `(from_group_id, to_group_id, relationship_type)`
- index `(from_group_id)`
- index `(to_group_id)`

### `field_definitions`

`field_definitions` should define runtime metadata fields per repo.

Suggested columns:

- `id`
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

- unique `(repository_owner, repository_name, name, object_scope)`
- index `(repository_owner, repository_name, object_scope)`

### `field_values`

`field_values` should hold actual annotation values in typed form.

Suggested columns:

- `id`
- `field_definition_id`
- `target_type`
  - `pull_request`
  - `issue`
  - `group`
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
- index `(repository_owner, repository_name, target_type, target_key)`
- partial indexes for filterable value columns where useful:
  - `(field_definition_id, enum_value)`
  - `(field_definition_id, bool_value)`
  - `(field_definition_id, int_value)`
  - `(field_definition_id, string_value)`

## Target Identity

Because `ghanno` is a separate service, it should use stable explicit target identities rather than database joins into `ghreplica`.

The clean shape is:

- `repository_owner`
- `repository_name`
- `target_type`
- `target_key`

Suggested target keys:

- PR: `pull_request:59883`
- issue: `issue:1234`
- group: `group:<local-id>`

This keeps the canonical model consistent across object types and makes derived indexing simpler.

## Derived Search Model

### Search Documents

For each target, `ghanno` should derive one search document from:

- all field values whose definitions are marked `is_searchable`
- optionally small projected GitHub fields later, such as PR title or issue title

The important implementation rule is that field definitions drive inclusion. The code should never assume specific field names.

Suggested columns:

- `id`
- `repository_owner`
- `repository_name`
- `target_type`
- `target_key`
- `search_text`
- `search_vector`
- `source_updated_at`
- `indexed_at`

Important indexes:

- unique `(repository_owner, repository_name, target_type, target_key)`
- GIN on `search_vector`

### Embeddings

For each target, `ghanno` should derive one embedding input text from:

- all field values whose definitions are marked `is_vectorized`

Suggested columns:

- `id`
- `repository_owner`
- `repository_name`
- `target_type`
- `target_key`
- `embedding_text`
- `embedding_model`
- `embedding`
- `source_updated_at`
- `indexed_at`

Important indexes:

- unique `(repository_owner, repository_name, target_type, target_key, embedding_model)`
- vector index appropriate to the chosen vector extension and distance metric

## Rebuild Model

Derived FTS rows and embeddings should be rebuilt asynchronously.

The clean production shape is:

1. canonical writes update `field_values`
2. the write path marks the affected target as needing search rebuild
3. background workers rebuild search documents and embeddings
4. rebuild state is tracked explicitly

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

This is the same operational shape that worked well in `ghreplica`: explicit leases, heartbeats, and resumable derived work.

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

Long term, `ghanno` should support filtered vector search and filtered FTS by combining:

- metadata filters from `field_values`
- semantic candidates from `embeddings`
- optional ranking signals from `search_documents`

That should be implemented as a composed query path, not by collapsing everything into one table.

## API Surface

The production API should be split cleanly.

### Schema Management

- create field definitions
- update field definitions
- archive field definitions
- list field definitions for a repo
- import a field-definition manifest
- export a field-definition manifest

### Group Management

- create a group
- update a group
- list groups
- fetch one group
- add a member to a group
- remove a member from a group
- link one group to another
- unlink groups

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

- `ghanno field create`
- `ghanno field list`
- `ghanno field import`
- `ghanno field export`
- `ghanno group create`
- `ghanno group add-pr`
- `ghanno group add-issue`
- `ghanno group link`
- `ghanno pr set`
- `ghanno issue set`
- `ghanno group set`
- `ghanno search text`
- `ghanno search similar`

## Auditability And History

A production system should not rely only on last-write-wins state.

The durable model should also record change history for:

- field-definition changes
- group creation and membership changes
- annotation value changes

Suggested audit table:

- `events`

Each event should record:

- actor
- action type
- target type
- target identity
- before state where practical
- after state where practical
- created at

The canonical tables remain the current state, and `events` provides history.

## Permissions

The initial implementation may start simple, but the long-term design should leave room for repository-scoped permissions.

At minimum, the model should make it possible later to answer:

- who can define fields for this repo
- who can edit annotations
- who can create or modify groups

The schema should carry `created_by` and `updated_by` fields consistently even before full authorization exists.

## Local Projection Policy

Because `ghanno` is a separate service, it may optionally keep a small projected cache of GitHub object fields for display and search convenience.

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

## Rollout Plan

The implementation should happen in phases.

### Phase 1: Canonical CRUD

Build:

- `groups`
- `group_members`
- `group_links`
- `field_definitions`
- `field_values`

Ship:

- field-definition CRUD
- group CRUD
- annotation CRUD
- exact filter queries

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

## Open Decisions

The core implementation direction is clear, but a few policy details still need explicit decisions:

- whether group and annotation updates are only current-state writes or also event-sourced
- how manifest imports handle destructive changes
- whether projected GitHub fields are in scope for phase 1 or phase 4
- which embedding model and vector extension to standardize on first
- what freshness guarantees search and embeddings should expose to operators

These are important, but they do not block implementation of the durable core model.

## Recommendation

The next concrete step should be Phase 1. Build the canonical schema and CRUD paths first, because that forces the real identity model, group model, field-definition model, and typed-value model to become concrete. Full-text search and vector search should follow as explicit derived layers on top of that canonical core, not as a shortcut around it.
