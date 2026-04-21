DROP INDEX IF EXISTS idx_group_members_target;

CREATE UNIQUE INDEX IF NOT EXISTS idx_group_members_unique_target
    ON group_members(github_repository_id, object_type, object_number);
