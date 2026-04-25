---
name: prtags
description: Explain prtags and guide the CLI and API usage for groups, annotations, field definitions, and group-read behavior. Use when work is specifically about prtags behavior or the prtags side of a ghreplica-integrated workflow.
---

# prtags Skill

Use this skill when you need to explain what `prtags` is, show how to use the `prtags` CLI, or guide someone through group, annotation, and field-definition workflows in this repository.

## What prtags is

`prtags` is a curation layer on top of mirrored GitHub objects.

It depends on `ghreplica`.

`ghreplica` remains the GitHub-shaped mirror for repositories, issues, pull requests, reviews, comments, and git-backed change truth.

`prtags` adds:

- groups
- group membership
- field definitions
- annotation values
- search and similarity indexes over curated metadata
- outbound GitHub comment sync state

## Boundary rule

Keep this boundary explicit:

- mirrored GitHub-native content should come from `ghreplica`
- curation and grouping behavior belongs in `prtags`

If a behavior is specific to grouping, annotations, or target metadata, prefer implementing it in `prtags`, not in `ghreplica`.

## Group read behavior

`group get` is refs-only by default.

Metadata is opt-in:

- CLI: `--include-metadata`
- HTTP: `?include=metadata`

When metadata is requested, `prtags` returns:

- `object_summary`

When metadata is not requested, those fields should be omitted, not `null`.

## Group metadata behavior

Metadata reads should use direct reads from the shared `ghreplica` mirror tables.

If mirror metadata is missing:

- omit `object_summary` for that member
- do not create a local PR or issue metadata cache
- do not queue a metadata refresh job

## Group write behavior

`group add-pr` and `group add-issue` should validate the target against the shared `ghreplica` mirror tables.

They should not create local PR or issue metadata projection rows.

## CLI patterns

Use:

```bash
prtags group get <group-id>
prtags group get <group-id> --include-metadata
prtags group list -R owner/repo
prtags group add-pr <group-id> <pr-number>
prtags group add-issue <group-id> <issue-number>
prtags annotation pr set -R owner/repo <number> field=value
prtags annotation issue get -R owner/repo <number>
prtags field create -R owner/repo --name priority --scope pull_request --type enum
```

## Performance measurement rule

When checking whether a `prtags` fetch is actually fast:

1. measure the direct HTTP endpoint first
2. measure the exact CLI command separately
3. if the CLI uses `go run`, do not confuse local Go toolchain cold-start cost with server latency
4. prefer repeated samples and report both cold and warmed behavior when they differ materially

For group reads, the most useful checks are:

- `GET /v1/groups/:id`
- `GET /v1/groups/:id?include=metadata`
- `prtags group get <id>`
- `prtags group get <id> --include-metadata`
