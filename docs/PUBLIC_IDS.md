# Public IDs

`prtags` should distinguish between internal database IDs and public object IDs.

The internal IDs exist for storage efficiency and joins. The public IDs exist for API responses, CLI output, copied references, bookmarks, and anything users are expected to see directly.

## Rules

The rules should be:

- keep internal numeric primary keys for database tables
- do not rely on bare internal numeric IDs as the main external identity
- give user-facing objects a stable public ID format
- use different public ID strategies depending on the object type

## Internal IDs

The internal database shape should stay simple:

- `bigint` or integer primary keys for local tables
- normal foreign keys between local tables

Those IDs are good for:

- joins
- indexes
- internal migrations
- implementation details

They are not ideal for public APIs because they are easy to confuse with GitHub issue and pull request numbers.

## Group Public IDs

Groups should use a human-readable public ID.

The public group ID format should be:

- two-word petname
- plus a short entropy suffix

Recommended shape:

- `silent-river-7k2m`
- `amber-fox-q91d`

The default production rule should be:

- generate a two-word petname using a curated word list
- append `4` lowercase base36 characters of random entropy
- join everything with hyphens

This gives groups IDs that are:

- readable
- easy to say and paste
- clearly different from GitHub PR or issue numbers
- large enough in practice once the entropy suffix is included

Uniqueness should be enforced by the database with a unique index, and the application should retry generation on collision.

## Other Public IDs

For other public IDs, `prtags` should use UUIDs.

This should apply to public objects that may be exposed later but do not need a human-readable petname-style identifier.

Examples include:

- field definitions, if they need a stable public ID
- saved searches, if that feature is added later
- other first-class `prtags` objects that users may need to reference directly

The production rule should be:

- groups get petname-plus-entropy IDs
- other public IDs use UUIDs

## Objects That Do Not Need Public IDs

Many tables should stay internal-only and should not get a separate public ID at all.

Examples:

- group membership rows
- field values
- event rows
- background jobs
- derived search documents
- embedding rows

Those objects are implementation details, not user-facing resources.

## API And CLI Behavior

The API and CLI should prefer public IDs whenever an object is user-facing.

That means:

- group endpoints should move toward public group IDs instead of bare numeric IDs
- CLI output should show the public group ID
- internal numeric IDs should be treated as implementation details

If both are present during migration, the public ID should be the primary displayed identifier.
