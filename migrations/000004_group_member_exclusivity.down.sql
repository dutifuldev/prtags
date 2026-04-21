DROP INDEX IF EXISTS idx_group_members_unique_target;

CREATE INDEX IF NOT EXISTS idx_group_members_target
    ON group_members(github_repository_id, object_type, object_number);
