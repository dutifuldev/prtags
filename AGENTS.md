# AGENTS.md

## Product Boundary

`prtags` depends on `ghreplica`.

`ghreplica` is the GitHub-shaped mirror.

`prtags` is the curation layer on top of mirrored GitHub objects.

That boundary should stay explicit in both implementation and explanations.

- mirrored GitHub resources should come from `ghreplica`
- groups, annotations, field definitions, search indexes, and outbound comment state belong to `prtags`
- if `prtags` needs product-specific behavior, implement it in `prtags`, not in `ghreplica`

## Read Behavior

`group get` is refs-only by default.

Metadata is opt-in:

- CLI: `--include-metadata`
- HTTP: `?include=metadata`

Metadata reads should use direct reads from the shared `ghreplica` mirror tables.

## Write Behavior

Group membership writes should validate that the target exists in the shared `ghreplica` mirror tables.

They should not create local PR or issue metadata projection rows.

## Documentation Convention

Follow SimpleDoc for repository documentation.

- General, non-dated documents should use capitalized filenames with underscores.
- Dated documents should live under `docs/` and use ISO date prefixes with lowercase kebab-case filenames.
