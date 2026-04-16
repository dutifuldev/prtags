CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS repository_projections (
    id BIGSERIAL PRIMARY KEY,
    github_repository_id BIGINT NOT NULL UNIQUE,
    owner TEXT NOT NULL,
    name TEXT NOT NULL,
    full_name TEXT NOT NULL,
    html_url TEXT NOT NULL DEFAULT '',
    visibility TEXT NOT NULL DEFAULT '',
    private BOOLEAN NOT NULL DEFAULT FALSE,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_repository_projections_full_name
    ON repository_projections(full_name);

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

CREATE TABLE IF NOT EXISTS groups (
    id BIGSERIAL PRIMARY KEY,
    github_repository_id BIGINT NOT NULL,
    repository_owner TEXT NOT NULL,
    repository_name TEXT NOT NULL,
    kind TEXT NOT NULL,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'open',
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL,
    row_version INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    archived_at TIMESTAMPTZ NULL
);

CREATE INDEX IF NOT EXISTS idx_groups_repo_kind_status
    ON groups(github_repository_id, kind, status);

CREATE INDEX IF NOT EXISTS idx_groups_repo_updated
    ON groups(github_repository_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS group_members (
    id BIGSERIAL PRIMARY KEY,
    group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    github_repository_id BIGINT NOT NULL,
    object_type TEXT NOT NULL,
    object_number INTEGER NOT NULL,
    target_key TEXT NOT NULL,
    added_by TEXT NOT NULL,
    added_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT idx_group_members_unique UNIQUE (group_id, object_type, object_number)
);

CREATE INDEX IF NOT EXISTS idx_group_members_target
    ON group_members(github_repository_id, object_type, object_number);

CREATE INDEX IF NOT EXISTS idx_group_members_target_key
    ON group_members(target_key);

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

CREATE TABLE IF NOT EXISTS field_definitions (
    id BIGSERIAL PRIMARY KEY,
    github_repository_id BIGINT NOT NULL,
    repository_owner TEXT NOT NULL,
    repository_name TEXT NOT NULL,
    name TEXT NOT NULL,
    display_name TEXT NOT NULL,
    object_scope TEXT NOT NULL,
    field_type TEXT NOT NULL,
    enum_values_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    is_required BOOLEAN NOT NULL DEFAULT FALSE,
    is_filterable BOOLEAN NOT NULL DEFAULT FALSE,
    is_searchable BOOLEAN NOT NULL DEFAULT FALSE,
    is_vectorized BOOLEAN NOT NULL DEFAULT FALSE,
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL,
    row_version INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    archived_at TIMESTAMPTZ NULL,
    CONSTRAINT idx_field_definitions_repo_name_scope UNIQUE (github_repository_id, name, object_scope)
);

CREATE INDEX IF NOT EXISTS idx_field_definitions_repo_scope
    ON field_definitions(github_repository_id, object_scope);

CREATE TABLE IF NOT EXISTS field_values (
    id BIGSERIAL PRIMARY KEY,
    field_definition_id BIGINT NOT NULL REFERENCES field_definitions(id) ON DELETE CASCADE,
    github_repository_id BIGINT NOT NULL,
    repository_owner TEXT NOT NULL,
    repository_name TEXT NOT NULL,
    target_type TEXT NOT NULL,
    object_number INTEGER NULL,
    group_id BIGINT NULL REFERENCES groups(id) ON DELETE CASCADE,
    target_key TEXT NOT NULL,
    string_value TEXT NULL,
    text_value TEXT NULL,
    bool_value BOOLEAN NULL,
    int_value BIGINT NULL,
    enum_value TEXT NULL,
    multi_enum_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    updated_by TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT idx_field_values_definition_target UNIQUE (field_definition_id, target_type, target_key)
);

CREATE INDEX IF NOT EXISTS idx_field_values_target
    ON field_values(github_repository_id, target_type, target_key);

CREATE INDEX IF NOT EXISTS idx_field_values_definition_enum
    ON field_values(field_definition_id, enum_value)
    WHERE enum_value IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_field_values_definition_bool
    ON field_values(field_definition_id, bool_value)
    WHERE bool_value IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_field_values_definition_int
    ON field_values(field_definition_id, int_value)
    WHERE int_value IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_field_values_definition_string
    ON field_values(field_definition_id, string_value)
    WHERE string_value IS NOT NULL;

CREATE TABLE IF NOT EXISTS events (
    id BIGSERIAL PRIMARY KEY,
    github_repository_id BIGINT NOT NULL,
    aggregate_type TEXT NOT NULL,
    aggregate_key TEXT NOT NULL,
    sequence_no INTEGER NOT NULL,
    event_type TEXT NOT NULL,
    actor_type TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    request_id TEXT NOT NULL DEFAULT '',
    idempotency_key TEXT NOT NULL DEFAULT '',
    schema_version INTEGER NOT NULL DEFAULT 1,
    payload_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT idx_events_aggregate_sequence UNIQUE (aggregate_type, aggregate_key, sequence_no)
);

CREATE INDEX IF NOT EXISTS idx_events_event_type
    ON events(event_type);

CREATE TABLE IF NOT EXISTS event_refs (
    id BIGSERIAL PRIMARY KEY,
    event_id BIGINT NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    ref_role TEXT NOT NULL,
    ref_type TEXT NOT NULL,
    ref_key TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_event_refs_event_id
    ON event_refs(event_id);

CREATE TABLE IF NOT EXISTS search_documents (
    id BIGSERIAL PRIMARY KEY,
    github_repository_id BIGINT NOT NULL,
    repository_owner TEXT NOT NULL,
    repository_name TEXT NOT NULL,
    target_type TEXT NOT NULL,
    target_key TEXT NOT NULL,
    search_text TEXT NOT NULL DEFAULT '',
    source_updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    indexed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT idx_search_documents_target UNIQUE (github_repository_id, target_type, target_key)
);

CREATE INDEX IF NOT EXISTS idx_search_documents_fts
    ON search_documents USING GIN (to_tsvector('simple', search_text));

CREATE TABLE IF NOT EXISTS embeddings (
    id BIGSERIAL PRIMARY KEY,
    github_repository_id BIGINT NOT NULL,
    repository_owner TEXT NOT NULL,
    repository_name TEXT NOT NULL,
    target_type TEXT NOT NULL,
    target_key TEXT NOT NULL,
    embedding_text TEXT NOT NULL DEFAULT '',
    embedding_model TEXT NOT NULL,
    embedding vector(128) NOT NULL,
    source_updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    indexed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT idx_embeddings_target_model UNIQUE (github_repository_id, target_type, target_key, embedding_model)
);

CREATE INDEX IF NOT EXISTS idx_embeddings_vector
    ON embeddings USING hnsw (embedding vector_cosine_ops);

CREATE TABLE IF NOT EXISTS index_jobs (
    id BIGSERIAL PRIMARY KEY,
    kind TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    github_repository_id BIGINT NOT NULL,
    repository_owner TEXT NOT NULL,
    repository_name TEXT NOT NULL,
    target_type TEXT NOT NULL,
    target_key TEXT NOT NULL,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    lease_owner TEXT NOT NULL DEFAULT '',
    heartbeat_at TIMESTAMPTZ NULL,
    next_attempt_at TIMESTAMPTZ NULL,
    last_error TEXT NOT NULL DEFAULT '',
    source_updated_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_index_jobs_status_next_attempt
    ON index_jobs(status, next_attempt_at, id);

CREATE INDEX IF NOT EXISTS idx_index_jobs_target
    ON index_jobs(github_repository_id, target_type, target_key);
