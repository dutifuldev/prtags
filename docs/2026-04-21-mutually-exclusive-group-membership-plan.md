---
title: Mutually Exclusive Group Membership Plan
date: 2026-04-21
status: accepted
---

# Mutually Exclusive Group Membership Plan

## Goal

Make `prtags` enforce that a GitHub issue or pull request can belong to only one
group at a time within a repository.

This means the same target identity:

- `github_repository_id`
- `object_type`
- `object_number`

must not appear in more than one row across `group_members`.

## Recommended v1

Use a direct database uniqueness rule on the target identity in
`group_members`.

Add a unique index on:

- `github_repository_id`
- `object_type`
- `object_number`

Keep the existing per-group uniqueness rule too:

- `group_id`
- `object_type`
- `object_number`

This is the simplest and strongest version of the feature:

- the rule is enforced in the database, not only in app code
- concurrent writes cannot create split-brain membership
- the implementation stays small
- the behavior is easy to explain

## Why not a claims table in v1

A separate claims table is more flexible, but it is only worth the extra moving
parts if `prtags` needs one of these later:

- archived groups should release claims automatically
- some group kinds should be exclusive and others should not
- one target should be allowed in multiple groups under specific rules

That flexibility is not needed for the basic product rule. If the product
decision is simply "one target can be in only one group", then the database
unique index is the cleaner implementation.

## Product Behavior

### Add member

When a user tries to add a PR or issue to a group:

- if the target is not in any group, the add succeeds
- if the target is already in the same group, return the existing duplicate
  conflict behavior
- if the target is already in a different group, return `409 Conflict`

The error should be explicit and human-readable, for example:

`target already belongs to another group`

If practical, the error payload should also include the owning group's public
ID so the CLI and API can point the operator to the existing group.

### Remove member

When a member is removed from a group, its target identity becomes available for
another group immediately.

### Existing reads

No read-path behavior needs to change. `group get`, list endpoints, and comment
sync all continue to read `group_members` normally.

## Schema Change

Add a new SQL migration that creates a unique index on the target identity in
`group_members`.

The intended production index shape is:

```sql
create unique index idx_group_members_unique_target
  on group_members (github_repository_id, object_type, object_number);
```

The exact migration should follow the existing migration style in `prtags`.

## Application Changes

### Service layer

Update `AddGroupMember` in `internal/core/service.go` so it treats the new
unique-index violation as a first-class business conflict.

Behavior:

- keep the existing same-group duplicate conflict
- add a distinct cross-group conflict branch
- return a clear `FailError{StatusCode: 409, ...}`

The service should not rely on a preflight read for correctness. The database
constraint should remain the source of truth under concurrency.

### Error translation

If needed, split the current conflict helper into:

- same-group duplicate membership conflict
- target-already-owned-by-another-group conflict

That keeps API behavior precise and keeps tests readable.

### CLI and HTTP behavior

No new surface is required.

The existing:

- `group add-pr`
- `group add-issue`
- HTTP add-member paths

should simply surface the new `409` message.

## Data Migration and Rollout

Before applying the unique index in production, check whether any target already
belongs to multiple groups.

### Preflight query

Run a query like:

```sql
select
  github_repository_id,
  object_type,
  object_number,
  count(*) as membership_count
from group_members
group by github_repository_id, object_type, object_number
having count(*) > 1;
```

### If duplicates exist

Do not apply the unique index blindly.

Instead:

1. inspect the duplicate memberships
2. decide which group should keep each target
3. remove the extra memberships
4. then apply the migration

This should be an explicit operator step, not an automatic silent cleanup.

## Tests

Add coverage for:

1. adding the same target twice to the same group still returns the current
   duplicate-member conflict
2. adding the same target to a different group returns the new cross-group
   conflict
3. concurrent attempts to add the same target to different groups allow only one
   winner under Postgres
4. removing a member from one group allows it to be added to another group
5. HTTP and CLI surfaces return or display the new conflict message correctly

The Postgres concurrency tests are the most important proof that the rule is
really database-enforced.

## Documentation Updates

Update:

- `docs/DATA_MODEL.md`
- any CLI or API docs that mention group membership behavior

The docs should state clearly that group membership is exclusive by target
identity within a repository.

## Non-Goals for v1

- allowing exclusivity only for some group kinds
- archived-group exceptions
- automatic reassignment from one group to another
- a claims table
- cross-repository deduplication

## Follow-up if product rules get more complex

If `prtags` later needs conditional exclusivity, replace the direct unique index
with a dedicated ownership table, for example `group_member_claims`, and make
that table the exclusive target-claim authority.

That should be treated as a later refactor, not mixed into the initial rollout.
