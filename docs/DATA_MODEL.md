# Data Model

`ghanno` should treat `ghreplica` as the source of truth for GitHub objects and store only references plus curation data.

The core idea is to support:

- groups of pull requests
- groups of issues
- links between groups
- annotations on individual pull requests and issues

## Design Rules

The model should follow a few simple rules.

First, `ghanno` should not duplicate full PR or issue content. It should store references to mirrored GitHub objects and keep only the metadata that belongs to the curation layer.

Second, membership and relationship should be separate concepts. A group contains members. A group may also be related to another group. Those are not the same thing and should not be represented by the same table.

Third, the model should allow stricter group kinds now without blocking more flexible mixed groups later.

## Referenced Objects

Because `ghanno` is a separate service, it cannot rely on database joins into `ghreplica`. So every curated object should be referenced in a stable, explicit way.

The simplest shape is:

- `repository_owner`
- `repository_name`
- `object_type`
  - `pull_request`
  - `issue`
- `object_number`

This should be the canonical identity model. `ghanno` should store those values as separate columns because that is easier to validate, index, and query than a packed string key.

If a display-friendly identifier is useful, it should be derived from those columns. For example:

- `openclaw/openclaw#59883`

That kind of packed key is fine for logs, URLs, CLI output, or cache keys, but it should not be the source of truth.

Optionally, `ghanno` can also cache a small local projection for display and search purposes, such as title, state, author, and updated time, but that projection should stay clearly separate from the source-of-truth reference.

## Core Tables

### `groups`

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

`kind` keeps the semantics clear. A `pull_request` group should only contain PRs. An `issue` group should only contain issues. A `mixed` group can be introduced later if needed without redesigning the whole model.

### `group_members`

This table represents group membership only.

Suggested columns:

- `id`
- `group_id`
- `repository_owner`
- `repository_name`
- `object_type`
- `object_number`
- `added_by`
- `added_at`

This table answers the question: “which mirrored GitHub objects are inside this group?”

### `group_links`

This table represents relationships between groups.

Suggested columns:

- `id`
- `from_group_id`
- `to_group_id`
- `relationship_type`
  - `implements`
  - `tracks`
  - `duplicates`
  - `related`
  - `blocked_by`
- `created_by`
- `created_at`

This table answers the question: “how does one group relate to another group?”

That is the clean way to model:

- a PR group connected to an issue group
- one issue group duplicating another
- two PR groups solving related parts of the same problem

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
- `created_at`
- `updated_at`

This is what lets each repo customize the curation layer without turning everything into unqueryable free-form JSON.

The important rule is:

- the database is the source of truth for field definitions
- a YAML or JSON manifest is only a convenient bootstrap or import format
- Go code should not be the place where repo-specific annotation fields are declared

### `field_values`

This table stores actual metadata values for objects or groups.

Suggested columns:

- `id`
- `field_definition_id`
- `target_type`
  - `pull_request`
  - `issue`
  - `group`
- `target_id`
  - group ID for local objects
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

### `events`

This table should record the immutable audit history for the curation layer.

The best model is not full event-sourcing. `ghanno` should keep normal current-state tables like `groups`, `group_members`, `group_links`, and `field_values` for fast reads, and also append one durable event row for each meaningful change.

Suggested columns:

- `id`
- `repository_owner`
- `repository_name`
- `aggregate_type`
  - `group`
  - `pull_request`
  - `issue`
  - `field_definition`
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

The important idea is that every event has one primary subject:

- `aggregate_type`
- `aggregate_key`

and an ordered sequence number within that aggregate.

That gives each object a stable history without making normal reads depend on replaying events.

### `event_refs`

This table should hold additional related objects for an event.

Suggested columns:

- `event_id`
- `ref_role`
- `ref_type`
- `ref_key`

This is what keeps the event model general without forcing every event into one awkward payload shape.

Examples:

- setting a PR annotation
  - aggregate = the PR
  - ref = the field definition
- adding a PR to a group
  - aggregate = the group
  - ref = the PR member
- linking two groups
  - aggregate = the source group
  - ref = the destination group

## Why One Generic Group Model Is Better

The elegant part of this design is that there is only one group concept. `ghanno` does not need a separate PR-group subsystem and a separate issue-group subsystem. Instead, the same group model works for both, and `kind` keeps the rules understandable.

That means:

- PR groups and issue groups look structurally the same
- membership stays simple
- group-to-group links stay generic
- future mixed groups remain possible

## Query Model

The query model should stay simple at first.

The important queries are:

- list groups for a repo
- fetch one group with its members
- list all groups containing a given PR or issue
- list links from one group to related groups
- filter PRs, issues, or groups by custom metadata fields

If vector search is added later, it should be a derived layer over selected annotation fields such as intent, notes, or summaries. That should be stored separately from the core curation model so the base CRUD layer remains simple and predictable.

The same principle applies to text and vector search over annotations:

- field definitions declare whether a field is filterable, searchable, or vectorized
- canonical field values stay in the normal metadata tables
- derived text indexes and embeddings are built from the fields marked for those purposes

That keeps the customizable layer flexible without making the data impossible to query efficiently.

## Recommended First Scope

The thinnest useful version of `ghanno` should start with:

- `groups`
- `group_members`
- `group_links`
- `field_definitions`
- `field_values`
- `events`
- `event_refs`

That is enough to support:

- creating PR groups
- creating issue groups
- linking PR groups to issue groups
- attaching repo-defined metadata like `intent` or `quality`
- recording an immutable history for those changes

That should be the foundation before adding embeddings, vector search, or richer projections.
