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
2. Each write appends a durable local event in the same transaction as the group change.
3. A River projector consumes those events in order and updates one desired sync row per `(group, target)`.
4. A River reconcile worker reads the latest desired state for that row and creates, updates, or deletes the managed GitHub comment.
5. A repair worker periodically rechecks failed, drifted, or missing comments and requeues reconciliation.

This means a group with three members produces three managed comments:

- one on member A listing members B and C
- one on member B listing members A and C
- one on member C listing members A and B

That is better than building one giant per-target summary across all groups because one group change should not force unrelated groups on the same target to be recomputed and rewritten together.

## Comment Shape

The comment should be readable by humans and recognizable by the service.

Recommended structure:

1. Hidden machine marker in an HTML comment
2. Group-level metadata above the table
3. One basic Markdown table for related targets

Example shape:

```md
<!-- prtags:group-comment v1 group_id=coherent-skunk-mbll target_type=pull_request target_number=24 -->

Related work from PRtags group `coherent-skunk-mbll`

Name: Repository Rename Follow-up
Status: open

| Number | Title |
| --- | --- |
| [#12](https://github.com/example/repo/issues/12) | Repository rename breaks cached lookups |
| [#31](https://github.com/example/repo/pull/31) | Fix rename follow-up cleanup |
| [#35](https://github.com/example/repo/pull/35) | Harden repository identity lookups |
```

The visible body should not be raw JSON.

The machine marker is enough to identify the comment if local sync state is lost and a repair pass needs to search for it.

The rule for what belongs in the table should be explicit:

- fields that map to each related issue or pull request go in the table
- group-level fields stay outside the table

For v1, the table should only include:

- `Number`
- `Title`

`Number` should be the GitHub link target.

The group name and other shared metadata should appear above the table rather than repeating on every row.

Row ordering should be stable.

Recommended first rule:

- sort by object number ascending

## Storage

`prtags` should add a table for GitHub comment sync state.

Suggested table name:

- `group_comment_sync_targets`

Suggested columns:

- `id`
- `github_repository_id`
- `group_id`
- `object_type`
- `object_number`
- `target_key`
- `desired_revision`
- `applied_revision`
- `desired_deleted`
- `github_comment_id`
- `comment_body_hash`
- `last_synced_at`
- `last_error`
- `last_error_at`
- `created_at`
- `updated_at`

Uniqueness should be:

- one row per `(group_id, object_type, object_number)`

This table is the main mapping between local group membership and the managed GitHub comment.

It should represent desired outbound state, not just the last successful GitHub write.

That means:

- `desired_revision` is bumped whenever the expected comment state changes
- `applied_revision` records the newest revision successfully applied to GitHub
- `desired_deleted` marks that the managed comment should be removed from GitHub

This is what makes retries, stale job skipping, and repair safe.

## Job Model

`prtags` should migrate all background work to River in one go.

Comment sync should run as a River job, and the existing background work should move to River at the same time.

Suggested River jobs:

- `group_comment_sync_project`
- `group_comment_sync_reconcile`
- `group_comment_sync_repair`

Existing job kinds that should also move:

- `target_projection_refresh`
- `search_document_rebuild`
- `embedding_rebuild`

Suggested job args:

- projector:
  - `event_id`
- reconcile:
  - `sync_target_id`
  - `desired_revision`
  - scheduled with a short debounce delay
- repair:
  - optional `github_repository_id`
  - optional `group_id`

This is a better fit than extending the current custom `index_jobs` worker further.

The important point is that `prtags` should not keep two queue systems.

Running River for comment sync while keeping the current custom worker for indexing would work, but it would not be the cleanest production shape.

The current worker is fine for local derived indexing, but GitHub comment sync is a different class of work:

- it talks to an external API
- it needs stronger retry behavior
- it needs clearer failure tracking
- it will likely need repair and replay later

Using River for all background work avoids keeping one homegrown queue system and one real queue system side by side.

The important job-behavior rule is coalescing.

If one group changes many times quickly, `prtags` should not try to apply every intermediate GitHub write in order.

Instead:

- the projector updates the sync row to the newest `desired_revision`
- reconcile jobs are keyed by `sync_target_id`
- reconcile jobs are scheduled with a short debounce window
- the reconcile worker always loads the latest row state before rendering
- stale jobs whose `desired_revision` is older than the current row are skipped

That gives one final GitHub write for the newest wanted state instead of a burst of obsolete writes.

The debounce window should be short but intentional.

Recommended first value:

- 5 to 15 seconds before reconcile work becomes runnable

That is long enough to collapse fast group edits into one GitHub write without making the feature feel stale.

## Retry And Failure Tracking

Comment sync should be explicit about success and failure.

Each River job should keep the normal job lifecycle state, attempts, scheduling, and error history.

That allows:

- automatic retries after GitHub API failures
- safe worker recovery after crashes
- visibility into whether GitHub is out of sync with local `prtags` state
- one retry model for both indexing work and comment sync work

The `group_comment_sync_targets` row should also keep the last known sync result for the target mapping itself.

Stale retries must never win.

The reconcile worker should:

1. load the sync target row
2. skip the job if its `desired_revision` is older than the row's current `desired_revision`
3. skip the job if its `desired_revision` is already less than or equal to `applied_revision`
4. otherwise apply the newest desired state and advance `applied_revision`

## Rate Limiting And Fairness

GitHub writes should drain slowly and predictably even if local group state changes quickly.

`prtags` should not let River push comment updates to GitHub as fast as workers can dequeue them.

Recommended rules:

- cap reconcile concurrency per GitHub installation or per repository
- back off aggressively on GitHub secondary rate limits, `403`, and `429`
- add jitter to retries so many failed writes do not resume together
- keep repair jobs lower priority than fresh reconcile jobs
- prefer dropping obsolete intermediate writes in favor of the newest desired state

This keeps one noisy repository or one burst of group changes from flooding GitHub with comment traffic.

The goal is:

- local writes stay fast
- desired state updates can happen immediately
- GitHub writes are paced on purpose

## Trigger Rules

The durable trigger should be the local `events` table.

Group writes should save local state and append events in one transaction.

The projector then derives desired GitHub comment state from those events.

These local changes should result in comment sync:

- `group.created`
  - only if the group already has members
- `group.updated`
  - if the comment body includes group title, description, or status
- `group.member_added`
  - sync the new member target
  - sync all existing member targets in the same group
- `group.member_removed`
  - mark the removed target comment as `desired_deleted`
  - sync the remaining member targets

Group annotation changes should enqueue comment sync only if those annotations are intentionally included in the rendered comment body.

The first version can skip group annotations entirely and only render membership-based related links.

This event-driven path is better than enqueueing GitHub jobs directly from request handlers because it does not lose sync if a write succeeds and the process crashes before a GitHub job is queued.

## GitHub API Surface

The simplest write surface is issue comments.

That works for both issues and pull requests because pull requests use the issue-comment surface for normal timeline comments.

So the River worker should use:

- create issue comment
- update issue comment
- delete issue comment

It does not need pull-request review comments.

## Managed Comment Policy

Managed comments should be owned by `prtags`.

That means:

- if a user manually edits a managed comment, the next successful sync overwrites it with the canonical rendered body
- `prtags` should not try to preserve mixed human and machine edits inside the managed comment

This is the cleanest long-term rule.

Trying to preserve partial manual edits would make repair and reconciliation much less predictable.

## Repair Policy

Repair should be an explicit part of the design, not a best-effort afterthought.

The repair worker should:

- requeue failed sync rows
- recreate missing managed comments
- detect deleted comments and recreate them
- detect duplicate managed comments by hidden marker
- keep one canonical comment, update it, and delete the extras
- run at lower priority than fresh reconcile work

This gives `prtags` a clear path to recover after crashes, operator mistakes, or temporary GitHub API failures.

## Authentication Choice

`prtags` already has a GitHub OAuth app for user login.

That is enough for a user-triggered comment API call, but it is the wrong fit for background sync.

The River worker should use a separate GitHub App dedicated to comment sync.

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
- `prtags` already has a clear background-work boundary

That means the clean implementation path is:

1. add River to `prtags`
2. migrate existing background job work from the custom worker onto River
3. add GitHub comment sync state storage
4. add a River worker with a `github_group_comment_sync` handler
5. render and reconcile one managed comment per `(group, target)`

## First Implementation Scope

The smallest shippable version should:

- support only issue and pull request group members
- render membership links only
- create one managed comment per `(group, target)`
- update on membership changes
- delete on membership removal
- track failures and retry
- overwrite manual edits to managed comments
- repair missing and duplicate managed comments

It should not initially:

- include arbitrary group annotations in the body
- try to sync historical comments written outside `prtags`
- support many different GitHub write identities

## Recommendation

Implement GitHub comment sync as a derived outbound projection owned by `prtags`.

Use one managed issue comment per group per target.

Track the GitHub comment ID locally.

Run all background work through River, including existing indexing work and the new comment sync.

Authenticate the worker with a separate GitHub App, while keeping the existing OAuth app for user login.
