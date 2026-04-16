CREATE TABLE IF NOT EXISTS group_links (
    id BIGSERIAL PRIMARY KEY,
    from_group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    to_group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    relationship_type TEXT NOT NULL,
    created_by TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT idx_group_links_unique UNIQUE (from_group_id, to_group_id, relationship_type)
);

CREATE INDEX IF NOT EXISTS idx_group_links_from_group_id
    ON group_links(from_group_id);

CREATE INDEX IF NOT EXISTS idx_group_links_to_group_id
    ON group_links(to_group_id);

DROP INDEX IF EXISTS idx_groups_public_id;

ALTER TABLE groups
    DROP COLUMN IF EXISTS public_id;
