CREATE TABLE IF NOT EXISTS group_comment_sync_targets (
    id BIGSERIAL PRIMARY KEY,
    github_repository_id BIGINT NOT NULL,
    group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    object_type TEXT NOT NULL,
    object_number INTEGER NOT NULL,
    target_key TEXT NOT NULL,
    desired_revision INTEGER NOT NULL DEFAULT 0,
    applied_revision INTEGER NOT NULL DEFAULT 0,
    desired_deleted BOOLEAN NOT NULL DEFAULT FALSE,
    github_comment_id BIGINT NULL,
    comment_body_hash TEXT NOT NULL DEFAULT '',
    last_synced_at TIMESTAMPTZ NULL,
    last_error_kind TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    last_error_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT idx_group_comment_sync_unique UNIQUE (group_id, object_type, object_number)
);

CREATE INDEX IF NOT EXISTS idx_group_comment_sync_revision
    ON group_comment_sync_targets(desired_revision, applied_revision);

CREATE INDEX IF NOT EXISTS idx_group_comment_sync_repo_updated
    ON group_comment_sync_targets(github_repository_id, updated_at DESC);

CREATE INDEX IF NOT EXISTS idx_group_comment_sync_comment_id
    ON group_comment_sync_targets(github_comment_id);
