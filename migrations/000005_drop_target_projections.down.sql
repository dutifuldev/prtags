CREATE TABLE IF NOT EXISTS target_projections (
    id BIGSERIAL PRIMARY KEY,
    github_repository_id BIGINT NOT NULL,
    repository_owner TEXT NOT NULL,
    repository_name TEXT NOT NULL,
    target_type TEXT NOT NULL,
    object_number INTEGER NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT '',
    author_login TEXT NOT NULL DEFAULT '',
    html_url TEXT NOT NULL DEFAULT '',
    source_updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT idx_target_projections_repo_type_number UNIQUE (github_repository_id, target_type, object_number)
);
