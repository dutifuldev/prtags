# Current State

This document describes the current shipped state of `PRtags`.

It is intentionally narrower than the production implementation plan. The goal here is to make it easy to see what already exists today without reading future-tense design material.

## Live Service

Current public instance:

- `https://prtags.dutiful.dev`

`PRtags` runs as a separate service next to `ghreplica` and uses its own Postgres database.

## Core Product

`PRtags` currently supports:

- repo-defined typed metadata fields for pull requests, issues, and groups
- idempotent field setup through `field ensure`
- filtered field inspection through `field list`
- group creation, update, and membership
- annotations on pull requests, issues, and groups
- explicit annotation clearing for pull requests, issues, and groups
- exact filtering over typed field values
- full-text search over searchable fields
- similarity search over vectorized fields
- background indexing for derived search data

## CLI Annotation Workflow

The current CLI supports explicit set, get, and clear flows for annotations.

Examples:

```bash
prtags annotation pr set -R dutifuldev/ghreplica 25 \
  intent="Add a mirror-backed batch object read endpoint for downstream tools"

prtags annotation pr get -R dutifuldev/ghreplica 25

prtags annotation pr clear -R dutifuldev/ghreplica 25 intent
```

The `clear` command removes the field value from the target. It does not write an empty string.

## Group Identity

Groups use public string IDs in the API and CLI.

The external group ID format is:

- two-word petname
- plus `4` lowercase base36 characters of entropy

Example:

- `coherent-skunk-mbll`

The numeric database ID stays internal.

## Group Reads

`group get` returns refs only by default.

The flow is:

1. `PRtags` reads the group and its member refs from its own database.
2. if metadata is not requested, `PRtags` returns those refs directly.
3. if metadata is requested, `PRtags` reads member PR and issue summaries from the shared `ghreplica` mirror tables.
4. `PRtags` returns the member refs plus a small `object_summary` when mirror metadata exists.

Metadata is opt-in:

- CLI: `prtags group get <group-id> --include-metadata`
- HTTP: `GET /v1/groups/:id?include=metadata`

Returned member metadata currently includes:

- `title`
- `state`
- `html_url`
- `author_login`
- `updated_at`

`group get` does not return projection freshness metadata because PR and issue summaries now come directly from the mirror tables.

## Group List Shape

`group list` keeps the lighter default shape.

It currently returns:

- group metadata
- `member_count`
- `member_counts`

It does not expand member summaries by default.

## Authentication

`PRtags` is CLI-first.

The current interactive login path is:

- GitHub OAuth device flow

The current CLI auth commands are:

- `prtags auth login`
- `prtags auth status`
- `prtags auth logout`

Current token resolution order:

1. `PRTAGS_GITHUB_TOKEN`
2. locally stored device-flow token
3. `GITHUB_TOKEN`
4. `GH_TOKEN`

The local auth file stores:

- the GitHub user
- the granted scopes
- the token

with restrictive file permissions.

## Dependency Boundary

`ghreplica` remains the source of truth for mirrored GitHub content.

`PRtags` owns:

- groups
- field definitions
- field values
- search documents
- embeddings

`PRtags` does not own:

- PR titles and bodies
- issue titles and bodies
- reviews
- comments
- Git-backed change truth

## Current Docs

For the longer-term design and planned future work, use:

- [Production Implementation Plan](./2026-04-16-production-implementation-plan.md)
- [Data Model](./DATA_MODEL.md)
- [Annotation Fields](./ANNOTATION_FIELDS.md)
- [Public IDs](./PUBLIC_IDS.md)
- [JSend](./JSEND.md)
