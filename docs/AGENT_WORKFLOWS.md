# Agent Workflows

This document describes practical `PRtags` CLI flows that agents can use directly.

The goal is to keep the operational path short and idempotent.

## Ensure A PR Intent Field

If an agent needs to write PR intent, it should first ensure that the repo has an `intent` field for pull requests.

```bash
prtags field ensure -R dutifuldev/ghreplica \
  --name intent \
  --display-name "Intent" \
  --scope pull_request \
  --type text \
  --searchable \
  --vectorized
```

`field ensure` is the preferred setup path for agents because it is idempotent:

- if the field does not exist, it is created
- if the field already exists with the requested shape, the command returns a `noop`
- if the field exists but needs mutable updates, the command updates it

The returned JSON includes an `action` field:

- `created`
- `updated`
- `noop`

## Set PR Intent

Once the field exists, an agent can attach an intent value to a pull request:

```bash
prtags annotation pr set -R dutifuldev/ghreplica 25 \
  intent="Add a mirror-backed batch object read endpoint for downstream tools"
```

## Read PR Intent Back

To verify the write:

```bash
prtags annotation pr get -R dutifuldev/ghreplica 25
```

That returns the current annotations for the PR, including `intent` when present.

## Clear A PR Annotation

If an agent needs to remove a value entirely, use the explicit clear command:

```bash
prtags annotation pr clear -R dutifuldev/ghreplica 25 intent
```

That removes the field value from the PR. It does not write an empty string.

The same pattern works for issues and groups:

```bash
prtags annotation issue clear -R dutifuldev/ghreplica 11 quality
prtags annotation group clear coherent-skunk-mbll summary
```

## Search By Intent

If the field is searchable, agents can find PRs by intent wording:

```bash
prtags search text -R dutifuldev/ghreplica "batch object read endpoint"
```

## Inspect Fields Cleanly

To inspect the repo field setup:

```bash
prtags field list -R dutifuldev/ghreplica --scope pull_request --format table
```

Useful filters:

- `--scope`
- `--type`
- `--name`
- `--active-only`

Useful formats:

- `--format json`
- `--format table`
- `--format auto`

`auto` chooses table output on an interactive terminal and JSON otherwise.

## Recommended Agent Sequence

For the common PR-intent workflow, the recommended sequence is:

1. `prtags field ensure ...`
2. `prtags annotation pr set ...`
3. `prtags annotation pr get ...`
4. if needed, `prtags annotation pr clear ...`
5. optionally `prtags search text ...`

That keeps agents from having to manually branch on whether the field already exists.
