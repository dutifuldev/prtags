# PRtags

`PRtags` is a customizable annotation and grouping layer on top of mirrored GitHub data.

`PRtags` is meant to sit next to `ghreplica`, not replace it. `ghreplica` stays responsible for mirroring GitHub objects and serving Git-backed change and search truth, while `PRtags` stores human-added structure such as groups, typed annotations, intent, quality judgments, and other repo-specific metadata.

The project is intentionally opinionated about boundaries. GitHub-native content like PR titles, issue bodies, reviews, and comments should continue to come from `ghreplica`. `PRtags` should only add curation data on top: group membership, field definitions, field values, and derived indexes for text and similarity search over those annotations.

On top of that core curation model, `PRtags` already adds a few practical capabilities:

- repo-defined typed metadata fields for pull requests, issues, and groups
- user-created groups with explicit membership
- exact filtering over typed annotation values
- full-text search over fields marked searchable
- similarity search over fields marked vectorized
- a JSend-based API and CLI for scripted workflows

Current public instance:

- `https://prtags.dutiful.dev`

## Why

Teams usually end up wanting more structure than GitHub gives them by default. They want to group related PRs and issues, tag work with intent or quality signals, and later search across that extra metadata. If every downstream tool invents its own sidecar tables and naming conventions for that, the result is fragmented and hard to trust.

`PRtags` exists to make that curation layer explicit. It gives deployers a place to define annotation fields at runtime, lets users attach those fields to PRs, issues, and groups, and keeps the extra metadata separate from the mirrored GitHub source of truth. The goal is not to copy GitHub into a second system. The goal is to build a stable layer of human-added structure on top of the mirror.

## API Surface

`PRtags` currently exposes one JSON API under `/v1/...`, but it is split into two practical shapes:

- `/v1/repos/:owner/:repo/...`
  - repo-scoped operations such as field definitions, repo group creation/listing, PR and issue annotations, target filtering, and search
- `/v1/groups/:id/...`
  - group-specific reads and writes such as group updates, membership, and group annotations

This API is intentionally not GitHub-compatible. Unlike `ghreplica`, `PRtags` is not mirroring GitHub-native resources. It is exposing product-specific curation data. All JSON responses use JSend envelopes so clients can treat success, validation failures, and server errors consistently.

In practice, the current product already covers a meaningful first slice: field-definition CRUD, manifest import and export, group CRUD, group membership, annotations on PRs, issues, and groups, exact filtering on typed annotation values, full-text search over searchable fields, and similarity search over vectorized fields.

## Quick Examples

The easiest way to understand the boundary is to separate repo configuration from day-to-day curation work.

### Define Repo Fields

Before users can annotate anything, the repo defines which fields exist and what they mean.

From the CLI:

```bash
prtags field create -R dutifuldev/ghreplica \
  --name intent \
  --display-name "Intent" \
  --scope pull_request \
  --type text \
  --searchable \
  --vectorized

prtags field create -R dutifuldev/ghreplica \
  --name quality \
  --scope pull_request \
  --type enum \
  --enum-values low,medium,high \
  --filterable
```

From the API:

```bash
curl -fsS http://127.0.0.1:8081/v1/repos/dutifuldev/ghreplica/fields \
  -H 'Content-Type: application/json' \
  -H 'X-Actor: local-dev' \
  -d '{
    "name": "intent",
    "display_name": "Intent",
    "object_scope": "pull_request",
    "field_type": "text",
    "is_searchable": true,
    "is_vectorized": true
  }' | jq
```

These calls create runtime field definitions in the `PRtags` database. They do not change `ghreplica`, and they do not require editing Go code.

### Curate Work

Once fields exist, users can create groups, attach PRs or issues to them, and annotate both the individual objects and the group itself.

From the CLI:

```bash
GROUP_ID=$(prtags group create -R dutifuldev/ghreplica \
  --kind mixed \
  --title "Rename hardening work" \
  --description "Repository rename safety and follow-up cleanup" \
  --server http://127.0.0.1:8081 | jq -r '.data.id')

prtags group add-pr "$GROUP_ID" 23 --server http://127.0.0.1:8081
prtags annotation pr set -R dutifuldev/ghreplica 23 intent="harden rename handling" quality=high
prtags annotation group set "$GROUP_ID" summary="rename hardening, reused-name safety, and refresh cleanup"
```

From the API:

```bash
curl -fsS http://127.0.0.1:8081/v1/repos/dutifuldev/ghreplica/groups \
  -H 'Content-Type: application/json' \
  -H 'X-Actor: local-dev' \
  -d '{
    "kind": "mixed",
    "title": "Rename hardening work",
    "description": "Repository rename safety and follow-up cleanup",
    "status": "open"
  }' | jq

curl -fsS http://127.0.0.1:8081/v1/repos/dutifuldev/ghreplica/pulls/23/annotations \
  -H 'Content-Type: application/json' \
  -H 'X-Actor: local-dev' \
  -d '{
    "intent": "harden rename handling",
    "quality": "high"
  }' | jq

curl -fsS http://127.0.0.1:8081/v1/repos/dutifuldev/ghreplica/groups | jq '.data[0].id'
```

The important distinction is that `PRtags` stores the curation data, while the underlying PR and issue content still comes from `ghreplica`.

## Search

Search in `PRtags` is intentionally split into two different capabilities because they answer different questions.

Exact filtering is for typed metadata. Use `prtags targets filter` when the question is “which PRs or issues have this exact annotation value?” That path is meant for fields like `quality`, `priority`, or other filterable enum, boolean, integer, or string values.

Text search is for searchable annotation text. Use `prtags search text` when the question is “where does this annotation wording appear?” That search works over the fields a repo explicitly marked as searchable, not over raw GitHub discussion text.

Similarity search is for vectorized annotation text. Use `prtags search similar` when the question is “what else looks semantically close to this?” The current implementation uses a local hash embedding provider by default, which is good enough for local development and smoke testing. The long-term design leaves room for a stronger embedding provider later without changing the core data model.

## Dependency Model

`PRtags` depends on `ghreplica`.

`ghreplica` remains the source of truth for mirrored repositories, pull requests, and issues. `PRtags` resolves repo and object identity through `ghreplica`, uses stable GitHub-backed identifiers so renames do not break object identity, and derives write permissions from GitHub repo access. That means `PRtags` is not trying to become a second GitHub mirror. It is a curation layer over a mirror that already exists.

This split is important operationally too. `PRtags` owns its own database, jobs, search documents, and embeddings. It should not share a database with `ghreplica`, and it should not copy full PR or issue content unless it is maintaining a small explicit projection for display or indexing purposes.

For group reads, `PRtags` enriches member references on the server side. It calls `ghreplica`'s batch object-read extension internally, resolves the referenced PRs and issues in one request, and returns a small `object_summary` for each member rather than making the CLI orchestrate multiple services. The CLI keeps calling only `PRtags`.

## Authentication

`PRtags` is CLI-first, so the intended interactive login flow is GitHub OAuth device flow, not browser-first sessions.

The pinned direction is:

- GitHub OAuth App for `PRtags` user login
- GitHub.com as the provider
- `read:org repo` as the default scope set
- bearer-token auth kept as a fallback for scripts and automation

That keeps the auth story clean. Human users can log in once through the CLI and let `PRtags` reuse the stored token, while scripts can continue sending an explicit `Authorization: Bearer ...` token. A browser callback route is still reserved for future web login at `https://prtags.dutiful.dev/auth/github/callback`, but that is not the main auth path for the initial product.

## Local Development

The local development loop is straightforward. Start a Postgres instance, point `PRtags` at a running `ghreplica`, and run the API:

```bash
docker run --rm --name prtags-postgres \
  -e POSTGRES_PASSWORD=prtags \
  -e POSTGRES_DB=prtags \
  -p 55432:5432 \
  pgvector/pgvector:pg16

export DATABASE_URL='postgres://postgres:prtags@127.0.0.1:55432/prtags?sslmode=disable'
export GHREPLICA_BASE_URL='https://ghreplica.dutiful.dev'
export ALLOW_UNAUTH_WRITES=true
go run ./cmd/prtags serve
```

By default the server listens on `:8081`, runs migrations on startup, and starts the background indexing worker.

Once the server is up, these are the most useful manual operations:

```bash
go run ./cmd/prtags field create -R dutifuldev/ghreplica --name intent --scope pull_request --type text --searchable --vectorized
go run ./cmd/prtags group create -R dutifuldev/ghreplica --kind mixed --title "Rename hardening work"
go run ./cmd/prtags annotation pr set -R dutifuldev/ghreplica 23 intent="harden rename handling"
go run ./cmd/prtags search text -R dutifuldev/ghreplica "rename hardening"
go run ./cmd/prtags worker run-once
```

The CLI automatically reads `PRTAGS_GITHUB_TOKEN`, `GITHUB_TOKEN`, or `GH_TOKEN` for authenticated writes. In local development, if you run the server with `ALLOW_UNAUTH_WRITES=true`, it will also accept `X-Actor` and default to a `local-dev` actor when that header is missing.

If you want to sanity-check a local instance quickly, these endpoints are usually enough:

- `GET http://127.0.0.1:8081/healthz`
- `GET http://127.0.0.1:8081/readyz`
- `GET http://127.0.0.1:8081/v1/repos/dutifuldev/ghreplica/fields`

## Local Build And Install

If you only want the CLI locally, build `prtags` directly from this repo:

```bash
cd /home/bob/repos/prtags
go build -o /tmp/prtags ./cmd/prtags
```

You can then run commands like:

```bash
/tmp/prtags field list -R dutifuldev/ghreplica
/tmp/prtags group list -R dutifuldev/ghreplica
/tmp/prtags search text -R dutifuldev/ghreplica "rename hardening"
```

This is the simplest local install path when you only need the client.

## Deployment

If you want to run `PRtags` yourself, think of deployment as standing up a second service next to `ghreplica`, not as extending the `ghreplica` process directly.

At minimum you need:

- a separate Postgres database for `PRtags`
- network access to a running `ghreplica` instance
- GitHub-authenticated requests for write operations if you want real permission enforcement
- a decision about the embedding provider and model you want to use beyond local development defaults

The basic shape is:

1. create the `PRtags` database
2. point `PRtags` at `ghreplica`
3. run migrations
4. start the API
5. verify health, readiness, and a few repo-scoped operations

The clean deployment boundary is:

- separate repo
- separate service
- separate database
- same VM is fine at first
- separate domain is preferred once you expose it publicly

## Docs

The deeper design details live in the docs:

- [Data Model](docs/DATA_MODEL.md)
- [Annotation Fields](docs/ANNOTATION_FIELDS.md)
- [Production Implementation Plan](docs/2026-04-16-production-implementation-plan.md)
- [JSend](docs/JSEND.md)
