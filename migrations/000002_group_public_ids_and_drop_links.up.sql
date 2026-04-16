ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS public_id TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS idx_groups_public_id
    ON groups(public_id);

DROP TABLE IF EXISTS group_links;
