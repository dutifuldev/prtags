package database

import (
	"time"

	"github.com/pgvector/pgvector-go"
	"gorm.io/datatypes"
)

const EmbeddingDimensions = 128

type RepositoryProjection struct {
	ID                 uint      `gorm:"primaryKey" json:"id"`
	GitHubRepositoryID int64     `gorm:"column:github_repository_id;uniqueIndex" json:"github_repository_id"`
	Owner              string    `json:"owner"`
	Name               string    `json:"name"`
	FullName           string    `gorm:"index" json:"full_name"`
	HTMLURL            string    `json:"html_url"`
	Visibility         string    `json:"visibility"`
	Private            bool      `json:"private"`
	FetchedAt          time.Time `json:"fetched_at"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type RepositoryAccessGrant struct {
	ID                    uint      `gorm:"primaryKey" json:"id"`
	GitHubRepositoryID    int64     `gorm:"column:github_repository_id;uniqueIndex:idx_repository_access_grants_repo_user,priority:1;index" json:"github_repository_id"`
	GitHubUserID          int64     `gorm:"column:github_user_id;uniqueIndex:idx_repository_access_grants_repo_user,priority:2;index" json:"github_user_id"`
	GitHubLogin           string    `gorm:"column:github_login" json:"github_login"`
	Role                  string    `json:"role"`
	GrantedByGitHubUserID int64     `gorm:"column:granted_by_github_user_id" json:"granted_by_github_user_id"`
	GrantedByGitHubLogin  string    `gorm:"column:granted_by_github_login" json:"granted_by_github_login"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

type Group struct {
	ID                 uint       `gorm:"primaryKey" json:"-"`
	PublicID           string     `gorm:"column:public_id;uniqueIndex" json:"id"`
	GitHubRepositoryID int64      `gorm:"column:github_repository_id;index:idx_groups_repo_kind_status,priority:1;index:idx_groups_repo_updated,priority:1" json:"github_repository_id"`
	RepositoryOwner    string     `json:"repository_owner"`
	RepositoryName     string     `json:"repository_name"`
	Kind               string     `gorm:"index:idx_groups_repo_kind_status,priority:2" json:"kind"`
	Title              string     `json:"title"`
	Description        string     `json:"description"`
	Status             string     `gorm:"index:idx_groups_repo_kind_status,priority:3" json:"status"`
	CreatedBy          string     `json:"created_by"`
	UpdatedBy          string     `json:"updated_by"`
	RowVersion         int        `json:"row_version"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `gorm:"index:idx_groups_repo_updated,priority:2,sort:desc" json:"updated_at"`
	ArchivedAt         *time.Time `json:"archived_at,omitempty"`
}

type GroupMember struct {
	ID                 uint      `gorm:"primaryKey" json:"id"`
	GroupID            uint      `gorm:"uniqueIndex:idx_group_members_unique,priority:1;index" json:"-"`
	GitHubRepositoryID int64     `gorm:"column:github_repository_id;uniqueIndex:idx_group_members_unique_target,priority:1" json:"github_repository_id"`
	ObjectType         string    `gorm:"uniqueIndex:idx_group_members_unique,priority:2;uniqueIndex:idx_group_members_unique_target,priority:2" json:"object_type"`
	ObjectNumber       int       `gorm:"uniqueIndex:idx_group_members_unique,priority:3;uniqueIndex:idx_group_members_unique_target,priority:3" json:"object_number"`
	TargetKey          string    `gorm:"index" json:"target_key"`
	AddedBy            string    `json:"added_by"`
	AddedAt            time.Time `json:"added_at"`
}

type FieldDefinition struct {
	ID                 uint           `gorm:"primaryKey" json:"id"`
	GitHubRepositoryID int64          `gorm:"column:github_repository_id;uniqueIndex:idx_field_definitions_repo_name_scope,priority:1;index:idx_field_definitions_repo_scope,priority:1" json:"github_repository_id"`
	RepositoryOwner    string         `json:"repository_owner"`
	RepositoryName     string         `json:"repository_name"`
	Name               string         `gorm:"uniqueIndex:idx_field_definitions_repo_name_scope,priority:2" json:"name"`
	DisplayName        string         `json:"display_name"`
	ObjectScope        string         `gorm:"uniqueIndex:idx_field_definitions_repo_name_scope,priority:3;index:idx_field_definitions_repo_scope,priority:2" json:"object_scope"`
	FieldType          string         `json:"field_type"`
	EnumValuesJSON     datatypes.JSON `gorm:"column:enum_values_json;type:jsonb" json:"enum_values_json"`
	IsRequired         bool           `json:"is_required"`
	IsFilterable       bool           `json:"is_filterable"`
	IsSearchable       bool           `json:"is_searchable"`
	IsVectorized       bool           `json:"is_vectorized"`
	SortOrder          int            `json:"sort_order"`
	CreatedBy          string         `json:"created_by"`
	UpdatedBy          string         `json:"updated_by"`
	RowVersion         int            `json:"row_version"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
	ArchivedAt         *time.Time     `json:"archived_at,omitempty"`
}

type FieldValue struct {
	ID                 uint            `gorm:"primaryKey" json:"id"`
	FieldDefinitionID  uint            `gorm:"uniqueIndex:idx_field_values_definition_target,priority:1;index" json:"field_definition_id"`
	FieldDefinition    FieldDefinition `json:"field_definition,omitempty"`
	GitHubRepositoryID int64           `gorm:"column:github_repository_id;index:idx_field_values_target,priority:1" json:"github_repository_id"`
	RepositoryOwner    string          `json:"repository_owner"`
	RepositoryName     string          `json:"repository_name"`
	TargetType         string          `gorm:"uniqueIndex:idx_field_values_definition_target,priority:2;index:idx_field_values_target,priority:2" json:"target_type"`
	ObjectNumber       *int            `json:"object_number,omitempty"`
	GroupID            *uint           `json:"group_id,omitempty"`
	TargetKey          string          `gorm:"uniqueIndex:idx_field_values_definition_target,priority:3;index:idx_field_values_target,priority:3" json:"target_key"`
	StringValue        *string         `json:"string_value,omitempty"`
	TextValue          *string         `json:"text_value,omitempty"`
	BoolValue          *bool           `json:"bool_value,omitempty"`
	IntValue           *int64          `json:"int_value,omitempty"`
	EnumValue          *string         `json:"enum_value,omitempty"`
	MultiEnumJSON      datatypes.JSON  `gorm:"column:multi_enum_json;type:jsonb" json:"multi_enum_json"`
	UpdatedBy          string          `json:"updated_by"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

type Event struct {
	ID                 uint           `gorm:"primaryKey" json:"id"`
	GitHubRepositoryID int64          `gorm:"column:github_repository_id;index" json:"github_repository_id"`
	AggregateType      string         `gorm:"uniqueIndex:idx_events_aggregate_sequence,priority:1;index" json:"aggregate_type"`
	AggregateKey       string         `gorm:"uniqueIndex:idx_events_aggregate_sequence,priority:2;index" json:"aggregate_key"`
	SequenceNo         int            `gorm:"uniqueIndex:idx_events_aggregate_sequence,priority:3" json:"sequence_no"`
	EventType          string         `gorm:"index" json:"event_type"`
	ActorType          string         `json:"actor_type"`
	ActorID            string         `json:"actor_id"`
	RequestID          string         `json:"request_id"`
	IdempotencyKey     string         `json:"idempotency_key"`
	SchemaVersion      int            `json:"schema_version"`
	PayloadJSON        datatypes.JSON `gorm:"type:jsonb" json:"payload_json"`
	MetadataJSON       datatypes.JSON `gorm:"type:jsonb" json:"metadata_json"`
	OccurredAt         time.Time      `json:"occurred_at"`
	CreatedAt          time.Time      `json:"created_at"`
}

type EventRef struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	EventID uint   `gorm:"index" json:"event_id"`
	RefRole string `json:"ref_role"`
	RefType string `json:"ref_type"`
	RefKey  string `json:"ref_key"`
}

type SearchDocument struct {
	ID                 uint      `gorm:"primaryKey" json:"id"`
	GitHubRepositoryID int64     `gorm:"column:github_repository_id;uniqueIndex:idx_search_documents_target,priority:1;index" json:"github_repository_id"`
	RepositoryOwner    string    `json:"repository_owner"`
	RepositoryName     string    `json:"repository_name"`
	TargetType         string    `gorm:"uniqueIndex:idx_search_documents_target,priority:2;index" json:"target_type"`
	TargetKey          string    `gorm:"uniqueIndex:idx_search_documents_target,priority:3;index" json:"target_key"`
	SearchText         string    `json:"search_text"`
	SourceUpdatedAt    time.Time `json:"source_updated_at"`
	IndexedAt          time.Time `json:"indexed_at"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type Embedding struct {
	ID                 uint            `gorm:"primaryKey" json:"id"`
	GitHubRepositoryID int64           `gorm:"column:github_repository_id;uniqueIndex:idx_embeddings_target_model,priority:1;index" json:"github_repository_id"`
	RepositoryOwner    string          `json:"repository_owner"`
	RepositoryName     string          `json:"repository_name"`
	TargetType         string          `gorm:"uniqueIndex:idx_embeddings_target_model,priority:2;index" json:"target_type"`
	TargetKey          string          `gorm:"uniqueIndex:idx_embeddings_target_model,priority:3;index" json:"target_key"`
	EmbeddingText      string          `json:"embedding_text"`
	EmbeddingModel     string          `gorm:"uniqueIndex:idx_embeddings_target_model,priority:4" json:"embedding_model"`
	Embedding          pgvector.Vector `gorm:"type:vector(128)" json:"embedding"`
	SourceUpdatedAt    time.Time       `json:"source_updated_at"`
	IndexedAt          time.Time       `json:"indexed_at"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

type IndexJob struct {
	ID                 uint       `gorm:"primaryKey" json:"id"`
	Kind               string     `gorm:"index" json:"kind"`
	Status             string     `gorm:"index" json:"status"`
	GitHubRepositoryID int64      `gorm:"column:github_repository_id;index" json:"github_repository_id"`
	RepositoryOwner    string     `json:"repository_owner"`
	RepositoryName     string     `json:"repository_name"`
	TargetType         string     `gorm:"index" json:"target_type"`
	TargetKey          string     `gorm:"index" json:"target_key"`
	AttemptCount       int        `json:"attempt_count"`
	LeaseOwner         string     `json:"lease_owner"`
	HeartbeatAt        *time.Time `gorm:"index" json:"heartbeat_at,omitempty"`
	NextAttemptAt      *time.Time `gorm:"index" json:"next_attempt_at,omitempty"`
	LastError          string     `json:"last_error"`
	SourceUpdatedAt    *time.Time `json:"source_updated_at,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

type GroupCommentSyncTarget struct {
	ID                 uint       `gorm:"primaryKey" json:"id"`
	GitHubRepositoryID int64      `gorm:"column:github_repository_id;index:idx_group_comment_sync_repo_updated,priority:1" json:"github_repository_id"`
	GroupID            uint       `gorm:"uniqueIndex:idx_group_comment_sync_unique,priority:1;index" json:"group_id"`
	ObjectType         string     `gorm:"uniqueIndex:idx_group_comment_sync_unique,priority:2;index" json:"object_type"`
	ObjectNumber       int        `gorm:"uniqueIndex:idx_group_comment_sync_unique,priority:3;index" json:"object_number"`
	TargetKey          string     `gorm:"index" json:"target_key"`
	DesiredRevision    int        `gorm:"index:idx_group_comment_sync_revision,priority:1" json:"desired_revision"`
	AppliedRevision    int        `gorm:"index:idx_group_comment_sync_revision,priority:2" json:"applied_revision"`
	DesiredDeleted     bool       `json:"desired_deleted"`
	GitHubCommentID    *int64     `gorm:"column:github_comment_id;index" json:"github_comment_id,omitempty"`
	CommentBodyHash    string     `json:"comment_body_hash"`
	LastSyncedAt       *time.Time `json:"last_synced_at,omitempty"`
	LastErrorKind      string     `json:"last_error_kind"`
	LastError          string     `json:"last_error"`
	LastErrorAt        *time.Time `json:"last_error_at,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `gorm:"index:idx_group_comment_sync_repo_updated,priority:2,sort:desc" json:"updated_at"`
}
