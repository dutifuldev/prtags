# Annotation Fields

This document explains how deployers should define custom annotation fields in `ghanno`.

The short version is: field definitions are runtime data, not Go code.

That means:

- deployers should be able to define fields without recompiling the service
- `ghanno` should store those definitions in normal database tables
- YAML or JSON manifests can exist as a convenient import or bootstrap format
- the database remains the source of truth after import

## Why Runtime Data Is The Right Model

`ghanno` is supposed to be customizable per repo. One repo may want fields like `intent`, `quality`, and `risk`, while another may want `theme`, `decision`, and `owner`. If field definitions lived in Go code, every deployer would need to edit code and redeploy the service just to add or rename a field.

That is the wrong tradeoff. The service should provide a stable set of supported field types and validation rules, while the actual repo-specific field definitions live as data.

## Supported Field Types

The first useful set of field types is small:

- `string`
- `text`
- `boolean`
- `integer`
- `enum`
- `multi_enum`

That is enough to support practical annotations like:

- `intent`
  - `text`
- `quality`
  - `enum`
- `priority`
  - `enum`
- `customer_visible`
  - `boolean`
- `effort_score`
  - `integer`

## Field Capabilities

Every field definition should also declare how it is meant to be used.

The important switches are:

- `is_filterable`
- `is_searchable`
- `is_vectorized`

Those flags tell `ghanno` what derived indexes to build.

The intended meaning is:

- `is_filterable`
  - the field should support normal exact or typed filtering
- `is_searchable`
  - the field should contribute to full-text search
- `is_vectorized`
  - the field should contribute to derived embeddings for semantic search

This is what keeps the system efficient. Instead of trying to search every custom field blindly, `ghanno` knows up front which fields matter for which query path.

## Source Of Truth

The source of truth for field definitions should be the `field_definitions` table in the `ghanno` database.

The source of truth for actual values should be the `field_values` table.

A manifest file is not the source of truth. It is just a convenient way to:

- bootstrap a repo
- import a known configuration
- export a configuration for review or reuse

After import, the database is authoritative.

## Recommended Operator Flow

The clean operator flow is:

1. define fields through the `ghanno` API or CLI
2. store those definitions in `field_definitions`
3. let users write values against PRs, issues, or clusters
4. build derived text indexes and embeddings only for fields that were marked as searchable or vectorized

If manifest support is added, it should just be another way to feed step 1.

## Example Manifest

This is the kind of YAML manifest that `ghanno` should be able to import:

```yaml
repository:
  owner: openclaw
  name: openclaw

fields:
  - name: intent
    object_scope: pull_request
    field_type: text
    is_required: false
    is_filterable: false
    is_searchable: true
    is_vectorized: true

  - name: quality
    object_scope: pull_request
    field_type: enum
    enum_values:
      - low
      - medium
      - high
    is_required: false
    is_filterable: true
    is_searchable: false
    is_vectorized: false

  - name: tracks_customer_issue
    object_scope: issue
    field_type: boolean
    is_required: false
    is_filterable: true
    is_searchable: false
    is_vectorized: false

  - name: theme
    object_scope: cluster
    field_type: text
    is_required: false
    is_filterable: false
    is_searchable: true
    is_vectorized: true
```

Again, the point of this manifest is convenience. `ghanno` should import it into normal field-definition rows instead of trying to read manifests on every request.

## Query Model

The way to keep custom annotations queryable is:

- store typed values in normal columns
- index the fields marked as filterable
- build full-text documents from the fields marked as searchable
- build embeddings from the fields marked as vectorized

So if a deployer defines:

- `intent` as searchable and vectorized
- `quality` as filterable

then `ghanno` can support:

- exact filters like “show high-quality PRs”
- text search like “find mentions of flaky auth retries”
- vector search like “find PRs with similar intent”

without turning the whole system into an unstructured blob store.
