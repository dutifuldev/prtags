# Data Model

`prtags` should treat `ghreplica` as the source of truth for GitHub objects and store only references plus curation data.

The core idea is to support:

- groups of pull requests
- groups of issues
- annotations on individual pull requests and issues

## Design Rules

The model should follow a few simple rules.

First, `prtags` should not duplicate full PR or issue content. It should store references to mirrored GitHub objects and keep only the metadata that belongs to the curation layer.

Second, shared group membership should be the association model. If a pull request and an issue belong together, they should be in the same group. `prtags` should not add a separate group-to-group relationship system unless a later product requirement clearly needs it.

Third, the model should allow stricter group kinds now without blocking more flexible mixed groups later.

## Referenced Objects

Because `prtags` is a separate service, it cannot rely on database joins into `ghreplica`. So every curated object should be referenced in a stable, explicit way.

The canonical shape should be:

- `github_repository_id`
- `object_type`
  - `pull_request`
  - `issue`
- `object_number`

This should be the canonical identity model. `prtags` should base GitHub-native object identity on stable IDs that do not change when a repository is renamed or transferred.

For repositories, that means:

- `github_repository_id` is the source of truth
- `repository_owner`
- `repository_name`

For pull requests and issues, that means:

- `github_repository_id`
- `object_type`
- `object_number`

Human-friendly locators can still exist as derived or cached fields. For example:

- `repository_owner = "openclaw"`
- `repository_name = "openclaw"`
- display key `openclaw/openclaw#59883`

Those are useful for logs, URLs, CLI output, and caches, but they should not be the source of truth.

Optionally, `prtags` can also cache a small local projection for display and search purposes, such as title, state, author, and updated time, but that projection should stay clearly separate from the source-of-truth reference.

## Core Tables

### `groups`

This is the main container table.

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
  - for example `open`, `closed`, `archived`
- `created_by`
- `created_at`
- `updated_at`

`kind` keeps the semantics clear. A `pull_request` group should only contain PRs. An `issue` group should only contain issues. A `mixed` group can be introduced later if needed without redesigning the whole model.

The public group identity should not be the bare numeric database ID. Groups should have a separate public ID that follows the rules in [PUBLIC_IDS.md](./PUBLIC_IDS.md). At the storage layer that field can be named `public_id`, but the API and CLI should expose it as the group's `id`.

### `group_members`

This table represents group membership only.

Suggested columns:

- `id`
- `group_id`
- `github_repository_id`
- `repository_owner`
- `repository_name`
- `object_type`
- `object_number`
- `added_by`
- `added_at`

This table answers the question: “which mirrored GitHub objects are inside this group?”

### `field_definitions`

This table defines repo-level custom metadata fields.

These definitions should be runtime data stored in the database, not Go code. Go should only define the supported field types, validation rules, and indexing behavior. If deployers want to create or change fields, they should do that through `prtags`'s API or CLI, or by importing a manifest that gets written into these tables.

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
- `github_repository_id`
- `target_id`
  - group ID for local objects
  - or a stable synthetic key for referenced PRs/issues built from stable GitHub identity
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

The best model is not full event-sourcing. `prtags` should keep normal current-state tables like `groups`, `group_members`, and `field_values` for fast reads, and also append one durable event row for each meaningful change.

Suggested columns:

- `id`
- `github_repository_id`
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
## Why One Generic Group Model Is Better

The elegant part of this design is that there is only one group concept. `prtags` does not need a separate PR-group subsystem and a separate issue-group subsystem. Instead, the same group model works for both, and `kind` keeps the rules understandable.

That means:

- PR groups and issue groups look structurally the same
- membership stays simple
- shared membership is the association layer
- future mixed groups remain possible

## Query Model

The query model should stay simple at first.

The important queries are:

- list groups for a repo
- fetch one group with its members
- list all groups containing a given PR or issue
- filter PRs, issues, or groups by custom metadata fields

If vector search is added later, it should be a derived layer over selected annotation fields such as intent, notes, or summaries. That should be stored separately from the core curation model so the base CRUD layer remains simple and predictable.

The same principle applies to text and vector search over annotations:

- field definitions declare whether a field is filterable, searchable, or vectorized
- canonical field values stay in the normal metadata tables
- derived text indexes and embeddings are built from the fields marked for those purposes

That keeps the customizable layer flexible without making the data impossible to query efficiently.

## Recommended First Scope

The thinnest useful version of `prtags` should start with:

- `groups`
- `group_members`
- `field_definitions`
- `field_values`
- `events`
- `event_refs`

That is enough to support:

- creating PR groups
- creating issue groups
- putting PRs and issues that belong together in the same group
- attaching repo-defined metadata like `intent` or `quality`
- recording an immutable history for those changes

That should be the foundation before adding embeddings, vector search, or richer projections.
