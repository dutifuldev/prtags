---
title: GitHub Group Comment Sync Design
date: 2026-04-21
status: proposed
---

# GitHub Group Comment Sync Design

## Summary

`prtags` should mirror group relationships back onto GitHub by managing one issue comment per `(group, target)` pair.

For each issue or pull request in a group, `prtags` should create or update a single managed comment that lists the other related pull requests and issues in that same group.

The source of truth must stay in `prtags`.

GitHub comments should be treated as a derived outbound projection of local group state, not as canonical state and not as part of the request write path.

## Goals

- show group relationships directly on GitHub issues and pull requests
- keep the comment format stable and machine-detectable
- update existing managed comments instead of creating duplicates
- retry failed comment writes safely
- keep normal `prtags` group writes fast and local

## Non-Goals

- do not make GitHub comments the source of truth
- do not add pairwise link state between every issue and pull request
- do not block group writes on live GitHub write success
- do not use request-scoped user tokens for background sync

## Recommended Model

The clean model is:

1. `prtags` stores groups and group membership as it does today.
2. A background sync worker renders the expected GitHub comment body for each affected target.
3. `prtags` compares that rendered body with the last synced body hash.
4. If the body changed, `prtags` updates the existing managed comment or creates it if missing.
5. If a target leaves the group, `prtags` deletes that target's managed comment for that group.

This means a group with three members produces three managed comments:

- one on member A listing members B and C
- one on member B listing members A and C
- one on member C listing members A and B

That is better than building one giant per-target summary across all groups because one group change should not force unrelated groups on the same target to be recomputed and rewritten together.

## Comment Shape

The comment should be readable by humans and recognizable by the service.

Recommended structure:

1. Hidden machine marker in an HTML comment
2. Normal Markdown body
3. Separate sections for related pull requests and related issues

Example shape:

```md
<!-- prtags:group-comment v1 group_id=coherent-skunk-mbll target_type=pull_request target_number=24 -->

Related work from PRtags group `coherent-skunk-mbll`:

Related PRs:
- #31 Fix rename follow-up cleanup
- #35 Harden repository identity lookups

Related Issues:
- #12 Repository rename breaks cached lookups
```

The visible body should not be raw JSON.

The machine marker is enough to identify the comment if local sync state is lost and a repair pass needs to search for it.

## Storage

`prtags` should add a table for GitHub comment sync state.

Suggested table name:

- `github_group_comments`

Suggested columns:

- `id`
- `github_repository_id`
- `group_id`
- `object_type`
- `object_number`
- `target_key`
- `github_comment_id`
- `comment_body_hash`
- `last_synced_at`
- `last_error`
- `created_at`
- `updated_at`

Uniqueness should be:

- one row per `(group_id, object_type, object_number)`

This table is the main mapping between local group membership and the managed GitHub comment.

## Job Model

Comment sync should run as a background job, just like the existing projection and search rebuild work.

Suggested job kind:

- `github_group_comment_sync`

Suggested job payload fields in the existing `index_jobs` table:

- `github_repository_id`
- `repository_owner`
- `repository_name`
- `target_type`
- `target_key`
- enough metadata to resolve the affected group

If the current generic job shape becomes awkward for this, it is acceptable to add a dedicated table later.

For the first implementation, reusing the existing worker and job pattern is the simplest path.

## Retry And Failure Tracking

Comment sync should be explicit about success and failure.

Each job should keep:

- `status`
- `attempt_count`
- `next_attempt_at`
- `last_error`
- `heartbeat_at`

That allows:

- automatic retries after GitHub API failures
- safe worker recovery after crashes
- visibility into whether GitHub is out of sync with local `prtags` state

The `github_group_comments` row should also keep the last known sync result for the target mapping itself.

## Trigger Rules

These local changes should enqueue comment sync:

- `group.created`
  - only if the group already has members
- `group.updated`
  - if the comment body includes group title, description, or status
- `group.member_added`
  - sync the new member target
  - sync all existing member targets in the same group
- `group.member_removed`
  - delete the removed target comment for that group
  - sync the remaining member targets

Group annotation changes should enqueue comment sync only if those annotations are intentionally included in the rendered comment body.

The first version can skip group annotations entirely and only render membership-based related links.

## GitHub API Surface

The simplest write surface is issue comments.

That works for both issues and pull requests because pull requests use the issue-comment surface for normal timeline comments.

So the worker should use:

- create issue comment
- update issue comment
- delete issue comment

It does not need pull-request review comments.

## Authentication Choice

`prtags` already has a GitHub OAuth app for user login.

That is enough for a user-triggered comment API call, but it is the wrong fit for background sync.

The worker should use a separate GitHub App dedicated to comment sync.

Reasons:

- the worker needs a stable service identity
- background retries should not depend on one user's stored OAuth token
- comment ownership should be explicit and consistent
- installation scoping is cleaner than asking one user token to act as the long-lived writer

Recommended split:

- keep the current OAuth app for CLI login and permission checks
- add a GitHub App for outbound comment sync

## Integration Points In Current Code

The current code already has most of the right local building blocks:

- group writes append durable events
- group membership writes already enqueue background work
- `prtags` already has a worker loop and lease-based job processing

That means the clean implementation path is:

1. add comment-sync job enqueueing near the existing group write paths
2. add GitHub comment sync state storage
3. extend the worker with a `github_group_comment_sync` handler
4. render and reconcile one managed comment per `(group, target)`

## First Implementation Scope

The smallest shippable version should:

- support only issue and pull request group members
- render membership links only
- create one managed comment per `(group, target)`
- update on membership changes
- delete on membership removal
- track failures and retry

It should not initially:

- include arbitrary group annotations in the body
- try to sync historical comments written outside `prtags`
- support many different GitHub write identities

## Recommendation

Implement GitHub comment sync as a derived outbound projection owned by `prtags`.

Use one managed issue comment per group per target.

Track the GitHub comment ID locally.

Run sync in the background with retries.

Authenticate the worker with a separate GitHub App, while keeping the existing OAuth app for user login.
