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

Groups use public string IDs in the API and CLI. Those IDs are human-readable two-word petnames plus a short entropy suffix, for example `coherent-skunk-mbll`, rather than bare numeric database IDs.

## Quick Examples

The easiest way to understand the boundary is to separate repo configuration from day-to-day curation work.

### Define Repo Fields

Before users can annotate anything, the repo defines which fields exist and what they mean.

From the CLI:

```bash
prtags field ensure -R dutifuldev/ghreplica \
  --name intent \
  --display-name "Intent" \
  --scope pull_request \
  --type text \
  --searchable \
  --vectorized

prtags field ensure -R dutifuldev/ghreplica \
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

By default, `group get` returns member refs only. If a caller wants cached PR or issue metadata too, it must opt in with `--include-metadata` in the CLI or `?include=metadata` in the HTTP API. `group list` keeps the lighter default shape and returns `member_count` plus `member_counts` by type.

### Agent Intent Workflow

For the common agent workflow of attaching intent to a PR, the practical flow is:

```bash
prtags field ensure -R dutifuldev/ghreplica \
  --name intent \
  --display-name "Intent" \
  --scope pull_request \
  --type text \
  --searchable \
  --vectorized

prtags annotation pr set -R dutifuldev/ghreplica 25 \
  intent="Add a mirror-backed batch object read endpoint for downstream tools"

prtags annotation pr get -R dutifuldev/ghreplica 25
prtags search text -R dutifuldev/ghreplica "batch object read endpoint"
```

`field ensure` is the idempotent setup path. It creates the field if it is missing and updates it if the live field definition has drifted from the requested shape.

If an agent needs to remove an annotation entirely, use the explicit `clear` command rather than writing an empty string:

```bash
prtags annotation pr clear -R dutifuldev/ghreplica 25 intent
prtags annotation issue clear -R dutifuldev/ghreplica 11 quality
prtags annotation group clear coherent-skunk-mbll summary
```

That removes the field value from the target. It is not the same as setting the value to `""` or `"null"` as text.

## Search

Search in `PRtags` is intentionally split into two different capabilities because they answer different questions.

Exact filtering is for typed metadata. Use `prtags targets filter` when the question is “which PRs or issues have this exact annotation value?” That path is meant for fields like `quality`, `priority`, or other filterable enum, boolean, integer, or string values.

Text search is for searchable annotation text. Use `prtags search text` when the question is “where does this annotation wording appear?” That search works over the fields a repo explicitly marked as searchable, not over raw GitHub discussion text.

Similarity search is for vectorized annotation text. Use `prtags search similar` when the question is “what else looks semantically close to this?” The current implementation uses a local hash embedding provider by default, which is good enough for local development and smoke testing. The long-term design leaves room for a stronger embedding provider later without changing the core data model.

## Dependency Model

`PRtags` depends on `ghreplica`.

`ghreplica` remains the source of truth for mirrored repositories, pull requests, and issues. `PRtags` resolves repo and object identity through `ghreplica`, uses stable GitHub-backed identifiers so renames do not break object identity, and derives write permissions from GitHub repo access. That means `PRtags` is not trying to become a second GitHub mirror. It is a curation layer over a mirror that already exists.

This split is important operationally too. `PRtags` owns its own schema, jobs, search documents, and embeddings. It shares a Postgres database with `ghreplica` so the two systems can join mirrored GitHub data with curation data. It should not copy PR or issue display metadata into a second local projection cache.

For group reads, `PRtags` returns refs by default. When metadata is requested, `PRtags` enriches member references by reading the shared `ghreplica` mirror tables directly. `group list` keeps the lighter default shape and returns `member_count` plus `member_counts` by type. The CLI keeps calling only `PRtags`.

A simplified refs-only `group get` member looks like:

```json
{
  "object_type": "pull_request",
  "object_number": 24,
  "target_key": "repo:123:pull_request:24"
}
```

With `--include-metadata` or `?include=metadata`, a member can also include:

```json
{
  "object_type": "pull_request",
  "object_number": 24,
  "object_summary": {
    "title": "Fix repository rename hardening name reuse regressions",
    "state": "closed",
    "html_url": "https://github.com/dutifuldev/ghreplica/pull/24",
    "author_login": "dutifulbob"
  }
}
```

## Authentication

`PRtags` is CLI-first, so the intended interactive login flow is GitHub OAuth device flow, not browser-first sessions.

The pinned direction is:

- GitHub OAuth App for `PRtags` user login
- GitHub.com as the provider
- `read:org repo` as the default scope set
- bearer-token auth kept as a fallback for scripts and automation

That keeps the auth story clean. Human users can log in once through the CLI and let `PRtags` reuse the stored token, while scripts can continue sending an explicit `Authorization: Bearer ...` token. A browser callback route is still reserved for future web login at `https://prtags.dutiful.dev/auth/github/callback`, but that is not the main auth path for the initial product.

The current CLI auth commands are:

```bash
prtags auth login
prtags auth status
prtags auth logout
```

The CLI resolves auth in this order:

1. `PRTAGS_GITHUB_TOKEN`
2. locally stored device-flow token
3. `GITHUB_TOKEN`
4. `GH_TOKEN`

## Local Development

The local development loop needs a Postgres database that contains both the
`ghreplica` mirror schema and the `PRtags` schema. For repo-scoped commands, the
mirror schema must already have the repositories, issues, and pull requests you
want to reference.

```bash
docker run --rm --name prtags-postgres \
  -e POSTGRES_PASSWORD=prtags \
  -e POSTGRES_DB=prtags \
  -p 55432:5432 \
  pgvector/pgvector:pg16

docker exec prtags-postgres \
  psql -U postgres -d prtags -c 'CREATE SCHEMA prtags;'

export DATABASE_URL='postgres://postgres:prtags@127.0.0.1:55432/prtags?sslmode=disable'
export DB_MAX_OPEN_CONNS=5
export DB_MAX_IDLE_CONNS=2
export DB_CONN_MAX_IDLE_TIME=5m
export DB_CONN_MAX_LIFETIME=30m
export PRTAGS_SCHEMA=prtags
export GHREPLICA_SCHEMA=public
export ALLOW_UNAUTH_WRITES=true
go run ./cmd/prtags serve
```

By default the server listens on `:8081`, runs migrations on startup, and starts the background indexing worker.

A fresh database like the one above is enough to test process startup and basic
health checks. To run the repo examples below, first run or restore `ghreplica`
against the same database so the configured mirror schema contains matching
GitHub data.

If you want to test outbound group comments locally, also set `GITHUB_APP_ID`, `GITHUB_APP_INSTALLATION_ID`, and either `GITHUB_APP_PRIVATE_KEY_PEM` or `GITHUB_APP_PRIVATE_KEY_PATH`. In production, prefer the mounted private key path and keep the containing directory readable by the container user only.

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

If you want the released CLI, the easiest install path is:

```bash
curl -fsSL https://raw.githubusercontent.com/dutifuldev/prtags/main/scripts/install-prtags.sh | bash
```

That script detects Linux versus macOS, picks the right release archive, and installs `prtags` into `/usr/local/bin` when possible or `~/.local/bin` otherwise.

If you want to install a specific version:

```bash
curl -fsSL https://raw.githubusercontent.com/dutifuldev/prtags/main/scripts/install-prtags.sh | bash -s -- --version v0.1.0
```

If you want to install into a custom directory:

```bash
curl -fsSL https://raw.githubusercontent.com/dutifuldev/prtags/main/scripts/install-prtags.sh | bash -s -- --bin-dir "$HOME/.local/bin"
```

If you only want the CLI locally, build `prtags` directly from this repo:

```bash
cd /home/bob/repos/prtags
go build -o /tmp/prtags ./cmd/prtags
```

You can then run commands like:

```bash
/tmp/prtags field list -R dutifuldev/ghreplica
/tmp/prtags group list -R dutifuldev/ghreplica
/tmp/prtags group get coherent-skunk-mbll
/tmp/prtags group get coherent-skunk-mbll --include-metadata
/tmp/prtags search text -R dutifuldev/ghreplica "rename hardening"
```

This is the simplest local install path when you only need the client.

## Deployment

If you want to run `PRtags` yourself, think of deployment as standing up a second service next to `ghreplica`, not as extending the `ghreplica` process directly.

The deployment uses one shared Postgres database with separate
schemas:

- configured mirror schema for mirrored GitHub data
- `prtags` schema for groups, annotations, search indexes, and jobs

That shared-database topology is what allows normal SQL joins between `PRtags`
groups and `ghreplica` mirror tables. It deprecates the separate `PRtags`
database deployment shape, but not the `PRtags` tables or data model.

At minimum you need:

- a shared Postgres database with `ghreplica` and `prtags` schemas
- a running `ghreplica` service writing mirror data into that database
- GitHub-authenticated requests for write operations if you want real permission enforcement
- a decision about the embedding provider and model you want to use beyond local development defaults

The basic shape is:

1. create the shared database schemas
2. point `PRtags` at the shared database
3. run migrations
4. start the API
5. verify health, readiness, and a few repo-scoped operations

The clean deployment boundary is:

- separate repo
- separate service
- separate schema in the shared database
- same VM is fine at first
- separate domain is preferred once you expose it publicly

## Docs

The deeper design details live in the docs:

- [Agent Workflows](docs/AGENT_WORKFLOWS.md)
- [Current State](docs/CURRENT_STATE.md)
- [Data Model](docs/DATA_MODEL.md)
- [Annotation Fields](docs/ANNOTATION_FIELDS.md)
- [Production Implementation Plan](docs/2026-04-16-production-implementation-plan.md)
- [JSend](docs/JSEND.md)
