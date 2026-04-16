# Data Model

`ghanno` should treat `ghreplica` as the source of truth for GitHub objects and store only references plus curation data.

The core idea is to support:

- clusters of pull requests
- clusters of issues
- links between clusters
- annotations on individual pull requests and issues

## Design Rules

The model should follow a few simple rules.

First, `ghanno` should not duplicate full PR or issue content. It should store references to mirrored GitHub objects and keep only the metadata that belongs to the curation layer.

Second, membership and relationship should be separate concepts. A cluster contains members. A cluster may also be related to another cluster. Those are not the same thing and should not be represented by the same table.

Third, the model should allow stricter cluster kinds now without blocking more flexible mixed clusters later.

## Referenced Objects

Because `ghanno` is a separate service, it cannot rely on database joins into `ghreplica`. So every curated object should be referenced in a stable, explicit way.

The simplest shape is:

- `repository_owner`
- `repository_name`
- `object_type`
  - `pull_request`
  - `issue`
- `object_number`

Optionally, `ghanno` can also cache a small local projection for display and search purposes, such as title, state, author, and updated time, but that projection should stay clearly separate from the source-of-truth reference.

## Core Tables

### `clusters`

This is the main container table.

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
  - for example `open`, `closed`, `archived`
- `created_by`
- `created_at`
- `updated_at`

`kind` keeps the semantics clear. A `pull_request` cluster should only contain PRs. An `issue` cluster should only contain issues. A `mixed` cluster can be introduced later if needed without redesigning the whole model.

### `cluster_members`

This table represents cluster membership only.

Suggested columns:

- `id`
- `cluster_id`
- `object_type`
- `object_number`
- `repository_owner`
- `repository_name`
- `added_by`
- `added_at`

This table answers the question: “which mirrored GitHub objects are inside this cluster?”

### `cluster_links`

This table represents relationships between clusters.

Suggested columns:

- `id`
- `from_cluster_id`
- `to_cluster_id`
- `relationship_type`
  - `implements`
  - `tracks`
  - `duplicates`
  - `related`
  - `blocked_by`
- `created_by`
- `created_at`

This table answers the question: “how does one cluster relate to another cluster?”

That is the clean way to model:

- a PR cluster connected to an issue cluster
- one issue cluster duplicating another
- two PR clusters solving related parts of the same problem

### `field_definitions`

This table defines repo-level custom metadata fields.

These definitions should be runtime data stored in the database, not Go code. Go should only define the supported field types, validation rules, and indexing behavior. If deployers want to create or change fields, they should do that through `ghanno`'s API or CLI, or by importing a manifest that gets written into these tables.

Suggested columns:

- `id`
- `repository_owner`
- `repository_name`
- `name`
- `object_scope`
  - `pull_request`
  - `issue`
  - `cluster`
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
- `created_at`
- `updated_at`

This is what lets each repo customize the curation layer without turning everything into unqueryable free-form JSON.

The important rule is:

- the database is the source of truth for field definitions
- a YAML or JSON manifest is only a convenient bootstrap or import format
- Go code should not be the place where repo-specific annotation fields are declared

### `field_values`

This table stores actual metadata values for objects or clusters.

Suggested columns:

- `id`
- `field_definition_id`
- `target_type`
  - `pull_request`
  - `issue`
  - `cluster`
- `target_id`
  - cluster ID for local objects
  - or a synthetic key for referenced PRs/issues
- `string_value`
- `text_value`
- `bool_value`
- `int_value`
- `enum_value`
- `multi_enum_json`
- `updated_by`
- `updated_at`

The point is to store typed values, not arbitrary blobs, so filtering and indexing stay straightforward.

## Why One Generic Cluster Model Is Better

The elegant part of this design is that there is only one cluster concept. `ghanno` does not need a separate PR-cluster subsystem and a separate issue-cluster subsystem. Instead, the same cluster model works for both, and `kind` keeps the rules understandable.

That means:

- PR clusters and issue clusters look structurally the same
- membership stays simple
- cluster-to-cluster links stay generic
- future mixed clusters remain possible

## Query Model

The query model should stay simple at first.

The important queries are:

- list clusters for a repo
- fetch one cluster with its members
- list all clusters containing a given PR or issue
- list links from one cluster to related clusters
- filter PRs, issues, or clusters by custom metadata fields

If vector search is added later, it should be a derived layer over selected annotation fields such as intent, notes, or summaries. That should be stored separately from the core curation model so the base CRUD layer remains simple and predictable.

The same principle applies to text and vector search over annotations:

- field definitions declare whether a field is filterable, searchable, or vectorized
- canonical field values stay in the normal metadata tables
- derived text indexes and embeddings are built from the fields marked for those purposes

That keeps the customizable layer flexible without making the data impossible to query efficiently.

## Recommended First Scope

The thinnest useful version of `ghanno` should start with:

- `clusters`
- `cluster_members`
- `cluster_links`
- `field_definitions`
- `field_values`

That is enough to support:

- creating PR clusters
- creating issue clusters
- linking PR clusters to issue clusters
- attaching repo-defined metadata like `intent` or `quality`

That should be the foundation before adding embeddings, vector search, or richer projections.
