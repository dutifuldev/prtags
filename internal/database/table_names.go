package database

const (
	RepositoryProjectionsTable   = "repository_projections"
	RepositoryAccessGrantsTable  = "repository_access_grants"
	GroupsTable                  = "groups"
	GroupMembersTable            = "group_members"
	FieldDefinitionsTable        = "field_definitions"
	FieldValuesTable             = "field_values"
	EventsTable                  = "events"
	EventRefsTable               = "event_refs"
	SearchDocumentsTable         = "search_documents"
	EmbeddingsTable              = "embeddings"
	IndexJobsTable               = "index_jobs"
	GroupCommentSyncTargetsTable = "group_comment_sync_targets"
)

func (RepositoryProjection) TableName() string   { return RepositoryProjectionsTable }
func (RepositoryAccessGrant) TableName() string  { return RepositoryAccessGrantsTable }
func (Group) TableName() string                  { return GroupsTable }
func (GroupMember) TableName() string            { return GroupMembersTable }
func (FieldDefinition) TableName() string        { return FieldDefinitionsTable }
func (FieldValue) TableName() string             { return FieldValuesTable }
func (Event) TableName() string                  { return EventsTable }
func (EventRef) TableName() string               { return EventRefsTable }
func (SearchDocument) TableName() string         { return SearchDocumentsTable }
func (Embedding) TableName() string              { return EmbeddingsTable }
func (IndexJob) TableName() string               { return IndexJobsTable }
func (GroupCommentSyncTarget) TableName() string { return GroupCommentSyncTargetsTable }
