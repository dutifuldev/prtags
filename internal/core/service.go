package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dutifuldev/prtags/internal/database"
	ghreplica "github.com/dutifuldev/prtags/internal/ghreplica"
	"github.com/dutifuldev/prtags/internal/permissions"
	"github.com/dutifuldev/prtags/internal/publicid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

var (
	ErrNotFound  = &FailError{StatusCode: 404, Message: "not found"}
	ErrForbidden = &FailError{StatusCode: 403, Message: "forbidden"}
)

type FailError struct {
	StatusCode int
	Message    string
	Data       any
}

func (e *FailError) Error() string {
	return e.Message
}

type Service struct {
	db          *gorm.DB
	ghreplica   mirrorClient
	permission  permissions.Checker
	indexer     *Indexer
	dispatcher  JobDispatcher
	commentSync *CommentSyncService
}

type mirrorClient interface {
	GetRepository(ctx context.Context, owner, repo string) (ghreplica.Repository, error)
	GetIssue(ctx context.Context, owner, repo string, number int) (ghreplica.Issue, error)
	GetPullRequest(ctx context.Context, owner, repo string, number int) (ghreplica.PullRequest, error)
	BatchGetObjects(ctx context.Context, repositoryID int64, objects []ghreplica.ObjectRef) ([]ghreplica.ObjectResult, error)
}

type groupMemberConflictDetails struct {
	GroupPublicID string `json:"group_public_id"`
}

type FieldDefinitionInput struct {
	Name         string   `json:"name" yaml:"name"`
	DisplayName  string   `json:"display_name" yaml:"display_name"`
	ObjectScope  string   `json:"object_scope" yaml:"object_scope"`
	FieldType    string   `json:"field_type" yaml:"field_type"`
	EnumValues   []string `json:"enum_values" yaml:"enum_values"`
	IsRequired   bool     `json:"is_required" yaml:"is_required"`
	IsFilterable bool     `json:"is_filterable" yaml:"is_filterable"`
	IsSearchable bool     `json:"is_searchable" yaml:"is_searchable"`
	IsVectorized bool     `json:"is_vectorized" yaml:"is_vectorized"`
	SortOrder    int      `json:"sort_order" yaml:"sort_order"`
}

type FieldDefinitionPatchInput struct {
	DisplayName        *string   `json:"display_name"`
	EnumValues         *[]string `json:"enum_values"`
	IsRequired         *bool     `json:"is_required"`
	IsFilterable       *bool     `json:"is_filterable"`
	IsSearchable       *bool     `json:"is_searchable"`
	IsVectorized       *bool     `json:"is_vectorized"`
	SortOrder          *int      `json:"sort_order"`
	ExpectedRowVersion *int      `json:"expected_row_version"`
}

type GroupInput struct {
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status"`
}

type GroupPatchInput struct {
	Title              *string `json:"title"`
	Description        *string `json:"description"`
	Status             *string `json:"status"`
	ExpectedRowVersion *int    `json:"expected_row_version"`
}

type Manifest struct {
	Version string                 `json:"version" yaml:"version"`
	Fields  []FieldDefinitionInput `json:"fields" yaml:"fields"`
}

type AnnotationSetResult struct {
	TargetKey   string         `json:"target_key"`
	Annotations map[string]any `json:"annotations"`
}

type TargetFilterResult struct {
	TargetType    string                    `json:"target_type"`
	ObjectNumber  int                       `json:"object_number,omitempty"`
	ID            string                    `json:"id,omitempty"`
	TargetKey     string                    `json:"target_key"`
	ObjectSummary *GroupMemberObjectSummary `json:"object_summary,omitempty"`
	Annotations   map[string]any            `json:"annotations"`
}

type GroupListView struct {
	database.Group
	MemberCount  int            `json:"member_count"`
	MemberCounts map[string]int `json:"member_counts"`
}

type GroupMemberObjectSummary struct {
	Title       string    `json:"title"`
	State       string    `json:"state"`
	HTMLURL     string    `json:"html_url"`
	AuthorLogin string    `json:"author_login"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type GroupMemberView struct {
	ID                 uint                      `json:"id"`
	GitHubRepositoryID int64                     `json:"github_repository_id"`
	ObjectType         string                    `json:"object_type"`
	ObjectNumber       int                       `json:"object_number"`
	TargetKey          string                    `json:"target_key"`
	AddedBy            string                    `json:"added_by"`
	AddedAt            time.Time                 `json:"added_at"`
	ObjectSummary      *GroupMemberObjectSummary `json:"object_summary,omitempty"`
}

type GroupCommentSyncTargetStatusView struct {
	GroupID         string     `json:"group_id"`
	GroupTitle      string     `json:"group_title"`
	ObjectType      string     `json:"object_type"`
	ObjectNumber    int        `json:"object_number"`
	TargetKey       string     `json:"target_key"`
	DesiredRevision int        `json:"desired_revision"`
	AppliedRevision int        `json:"applied_revision"`
	DesiredDeleted  bool       `json:"desired_deleted"`
	State           string     `json:"state"`
	LastErrorKind   string     `json:"last_error_kind"`
	LastError       string     `json:"last_error"`
	LastErrorAt     *time.Time `json:"last_error_at,omitempty"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type GetGroupOptions struct {
	IncludeMetadata bool
}

func NewService(db *gorm.DB, gh mirrorClient, checker permissions.Checker, indexer *Indexer) *Service {
	return &Service{
		db:         db,
		ghreplica:  gh,
		permission: checker,
		indexer:    indexer,
	}
}

func (s *Service) SetJobDispatcher(dispatcher JobDispatcher) {
	s.dispatcher = dispatcher
}

func (s *Service) SetCommentSync(commentSync *CommentSyncService) {
	s.commentSync = commentSync
}

func (s *Service) EnsureRepository(ctx context.Context, owner, repo string) (database.RepositoryProjection, error) {
	timer := database.StartQueryStep(ctx, "repo_ensure")
	defer timer.Done()

	repository, err := s.ghreplica.GetRepository(ctx, owner, repo)
	if err != nil {
		return database.RepositoryProjection{}, err
	}

	model := repositoryProjectionFromMirror(repository, time.Now().UTC())

	if err := s.db.WithContext(ctx).Where("github_repository_id = ?", repository.ID).Assign(model).FirstOrCreate(&model).Error; err != nil {
		return database.RepositoryProjection{}, err
	}
	return model, nil
}

func (s *Service) readRepositoryProjection(ctx context.Context, owner, repo string) (database.RepositoryProjection, error) {
	timer := database.StartQueryStep(ctx, "repo_read")
	defer timer.Done()

	mirrorRepository, err := s.ghreplica.GetRepository(ctx, owner, repo)
	if err != nil {
		return database.RepositoryProjection{}, translateDBError(err)
	}
	if _, err := s.lookupRepositoryProjectionByGitHubID(ctx, mirrorRepository.ID); err != nil {
		return database.RepositoryProjection{}, translateDBError(err)
	}
	return repositoryProjectionFromMirror(mirrorRepository, time.Now().UTC()), nil
}

func repositoryProjectionFromMirror(repository ghreplica.Repository, fetchedAt time.Time) database.RepositoryProjection {
	return database.RepositoryProjection{
		GitHubRepositoryID: repository.ID,
		Owner:              repository.Owner.Login,
		Name:               repository.Name,
		FullName:           repository.FullName,
		HTMLURL:            repository.HTMLURL,
		Visibility:         repository.Visibility,
		Private:            repository.Private,
		FetchedAt:          fetchedAt,
	}
}

func (s *Service) requireWrite(ctx context.Context, actor permissions.Actor, repo database.RepositoryProjection) error {
	allowed, err := s.permission.CanWrite(ctx, actor, repo.Owner, repo.Name)
	if err != nil {
		return err
	}
	if allowed {
		return nil
	}

	resolver, ok := s.permission.(permissions.IdentityResolver)
	if !ok {
		return ErrForbidden
	}
	identity, err := resolver.ResolveIdentity(ctx, actor)
	if err != nil {
		return err
	}
	if identity.GitHubUserID == 0 {
		return ErrForbidden
	}

	granted, err := s.hasRepositoryWriteGrant(ctx, repo.GitHubRepositoryID, identity.GitHubUserID)
	if err != nil {
		return err
	}
	if !granted {
		return ErrForbidden
	}
	return nil
}

func (s *Service) CreateFieldDefinition(ctx context.Context, actor permissions.Actor, owner, repo string, input FieldDefinitionInput, idempotencyKey string) (database.FieldDefinition, error) {
	repository, err := s.EnsureRepository(ctx, owner, repo)
	if err != nil {
		return database.FieldDefinition{}, err
	}
	if err := s.requireWrite(ctx, actor, repository); err != nil {
		return database.FieldDefinition{}, err
	}
	if err := validateFieldDefinitionInput(input); err != nil {
		return database.FieldDefinition{}, err
	}

	enumValues, _ := json.Marshal(normalizeEnumValues(input.EnumValues))
	model := database.FieldDefinition{
		GitHubRepositoryID: repository.GitHubRepositoryID,
		RepositoryOwner:    repository.Owner,
		RepositoryName:     repository.Name,
		Name:               normalizeFieldName(input.Name),
		DisplayName:        displayName(input),
		ObjectScope:        input.ObjectScope,
		FieldType:          input.FieldType,
		EnumValuesJSON:     datatypes.JSON(enumValues),
		IsRequired:         input.IsRequired,
		IsFilterable:       input.IsFilterable,
		IsSearchable:       input.IsSearchable,
		IsVectorized:       input.IsVectorized,
		SortOrder:          input.SortOrder,
		CreatedBy:          actor.ID,
		UpdatedBy:          actor.ID,
		RowVersion:         1,
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&model).Error; err != nil {
			return err
		}
		return s.appendEventTx(tx, eventInput{
			RepositoryID:   repository.GitHubRepositoryID,
			AggregateType:  "field_definition",
			AggregateKey:   fieldAggregateKey(repository.GitHubRepositoryID, model.Name, model.ObjectScope),
			EventType:      "field_definition.created",
			Actor:          actor,
			IdempotencyKey: idempotencyKey,
			Payload: map[string]any{
				"field_definition_id": model.ID,
				"name":                model.Name,
				"object_scope":        model.ObjectScope,
			},
		})
	})
	if err != nil {
		return database.FieldDefinition{}, translateDBError(err)
	}
	return model, nil
}

func (s *Service) ListFieldDefinitions(ctx context.Context, owner, repo string) ([]database.FieldDefinition, error) {
	repository, err := s.readRepositoryProjection(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	var fields []database.FieldDefinition
	timer := database.StartQueryStep(ctx, "fields_list")
	defer timer.Done()
	err = s.db.WithContext(ctx).
		Where("github_repository_id = ?", repository.GitHubRepositoryID).
		Order("sort_order ASC, name ASC").
		Find(&fields).Error
	return fields, err
}

func (s *Service) UpdateFieldDefinition(ctx context.Context, actor permissions.Actor, owner, repo string, fieldID uint, input FieldDefinitionPatchInput, idempotencyKey string) (database.FieldDefinition, error) {
	repository, err := s.EnsureRepository(ctx, owner, repo)
	if err != nil {
		return database.FieldDefinition{}, err
	}
	if err := s.requireWrite(ctx, actor, repository); err != nil {
		return database.FieldDefinition{}, err
	}
	if err := validateFieldDefinitionPatchInput(input); err != nil {
		return database.FieldDefinition{}, err
	}

	var field database.FieldDefinition
	if err := s.db.WithContext(ctx).
		Where("id = ? AND github_repository_id = ?", fieldID, repository.GitHubRepositoryID).
		First(&field).Error; err != nil {
		return database.FieldDefinition{}, translateDBError(err)
	}
	if err := ensureExpectedRowVersion(field.RowVersion, input.ExpectedRowVersion); err != nil {
		return database.FieldDefinition{}, err
	}
	updates, err := s.fieldDefinitionUpdates(ctx, actor, field, input)
	if err != nil {
		return database.FieldDefinition{}, err
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.updateFieldDefinitionTx(tx, &field, repository, actor, input.ExpectedRowVersion, updates, idempotencyKey)
	})
	if err != nil {
		return database.FieldDefinition{}, translateDBError(err)
	}
	return field, nil
}

func (s *Service) ArchiveFieldDefinition(ctx context.Context, actor permissions.Actor, owner, repo string, fieldID uint, expectedRowVersion *int, idempotencyKey string) (database.FieldDefinition, error) {
	repository, err := s.EnsureRepository(ctx, owner, repo)
	if err != nil {
		return database.FieldDefinition{}, err
	}
	if err := s.requireWrite(ctx, actor, repository); err != nil {
		return database.FieldDefinition{}, err
	}

	var field database.FieldDefinition
	if err := s.db.WithContext(ctx).
		Where("id = ? AND github_repository_id = ?", fieldID, repository.GitHubRepositoryID).
		First(&field).Error; err != nil {
		return database.FieldDefinition{}, translateDBError(err)
	}
	if err := ensureExpectedRowVersion(field.RowVersion, expectedRowVersion); err != nil {
		return database.FieldDefinition{}, err
	}
	if field.ArchivedAt != nil {
		return field, nil
	}

	now := time.Now().UTC()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.archiveFieldDefinitionTx(tx, &field, repository, actor, expectedRowVersion, idempotencyKey, now)
	})
	if err != nil {
		return database.FieldDefinition{}, translateDBError(err)
	}
	return field, nil
}

func (s *Service) fieldDefinitionUpdates(ctx context.Context, actor permissions.Actor, field database.FieldDefinition, input FieldDefinitionPatchInput) (map[string]any, error) {
	updates := map[string]any{
		"updated_by":  actor.ID,
		"updated_at":  time.Now().UTC(),
		"row_version": gorm.Expr("row_version + 1"),
	}
	if err := applyFieldDisplayNameUpdate(updates, input.DisplayName); err != nil {
		return nil, err
	}
	applyOptionalFieldUpdate(updates, "is_required", input.IsRequired)
	applyOptionalFieldUpdate(updates, "is_filterable", input.IsFilterable)
	applyOptionalFieldUpdate(updates, "is_searchable", input.IsSearchable)
	applyOptionalFieldUpdate(updates, "is_vectorized", input.IsVectorized)
	applyOptionalFieldUpdate(updates, "sort_order", input.SortOrder)
	if err := s.applyFieldEnumUpdate(ctx, updates, field, input.EnumValues); err != nil {
		return nil, err
	}
	return updates, nil
}

func applyFieldDisplayNameUpdate(updates map[string]any, displayName *string) error {
	if displayName == nil {
		return nil
	}
	value := strings.TrimSpace(*displayName)
	if value == "" {
		return &FailError{StatusCode: 400, Message: "display_name cannot be blank"}
	}
	updates["display_name"] = value
	return nil
}

func applyOptionalFieldUpdate[T any](updates map[string]any, key string, value *T) {
	if value != nil {
		updates[key] = *value
	}
}

func (s *Service) applyFieldEnumUpdate(ctx context.Context, updates map[string]any, field database.FieldDefinition, enumValues *[]string) error {
	if enumValues == nil {
		return nil
	}
	if field.FieldType != "enum" && field.FieldType != "multi_enum" {
		return &FailError{StatusCode: 400, Message: "enum_values can only be updated for enum fields"}
	}
	normalized := normalizeEnumValues(*enumValues)
	if len(normalized) == 0 {
		return &FailError{StatusCode: 400, Message: "enum_values are required"}
	}
	if err := s.ensureEnumValuesCompatible(ctx, field, normalized); err != nil {
		return err
	}
	raw, _ := json.Marshal(normalized)
	updates["enum_values_json"] = datatypes.JSON(raw)
	return nil
}

func (s *Service) updateFieldDefinitionTx(tx *gorm.DB, field *database.FieldDefinition, repository database.RepositoryProjection, actor permissions.Actor, expectedRowVersion *int, updates map[string]any, idempotencyKey string) error {
	if err := applyFieldDefinitionUpdatesTx(tx, field.ID, expectedRowVersion, updates); err != nil {
		return err
	}
	if err := tx.First(field, field.ID).Error; err != nil {
		return err
	}
	if err := s.appendFieldDefinitionEventTx(tx, repository.GitHubRepositoryID, "field_definition.updated", actor, idempotencyKey, *field); err != nil {
		return err
	}
	return s.enqueueFieldTargetRebuildsTx(tx, repository, field.ID, time.Now().UTC())
}

func (s *Service) archiveFieldDefinitionTx(tx *gorm.DB, field *database.FieldDefinition, repository database.RepositoryProjection, actor permissions.Actor, expectedRowVersion *int, idempotencyKey string, now time.Time) error {
	if err := applyFieldDefinitionUpdatesTx(tx, field.ID, expectedRowVersion, map[string]any{
		"archived_at": now,
		"updated_by":  actor.ID,
		"updated_at":  now,
		"row_version": gorm.Expr("row_version + 1"),
	}); err != nil {
		return err
	}
	if err := tx.First(field, field.ID).Error; err != nil {
		return err
	}
	if err := s.appendFieldDefinitionEventTx(tx, repository.GitHubRepositoryID, "field_definition.archived", actor, idempotencyKey, *field); err != nil {
		return err
	}
	return s.enqueueFieldTargetRebuildsTx(tx, repository, field.ID, now)
}

func applyFieldDefinitionUpdatesTx(tx *gorm.DB, fieldID uint, expectedRowVersion *int, updates map[string]any) error {
	query := tx.Model(&database.FieldDefinition{}).Where("id = ?", fieldID)
	if expectedRowVersion != nil {
		query = query.Where("row_version = ?", *expectedRowVersion)
	}
	result := query.Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if expectedRowVersion != nil && result.RowsAffected == 0 {
		return staleRowVersionError(tx, &database.FieldDefinition{}, fieldID, *expectedRowVersion)
	}
	return nil
}

func (s *Service) appendFieldDefinitionEventTx(tx *gorm.DB, repositoryID int64, eventType string, actor permissions.Actor, idempotencyKey string, field database.FieldDefinition) error {
	return s.appendEventTx(tx, eventInput{
		RepositoryID:   repositoryID,
		AggregateType:  "field_definition",
		AggregateKey:   fieldAggregateKey(repositoryID, field.Name, field.ObjectScope),
		EventType:      eventType,
		Actor:          actor,
		IdempotencyKey: idempotencyKey,
		Payload: map[string]any{
			"field_definition_id": field.ID,
			"name":                field.Name,
			"object_scope":        field.ObjectScope,
		},
	})
}

func (s *Service) enqueueFieldTargetRebuildsTx(tx *gorm.DB, repository database.RepositoryProjection, fieldDefinitionID uint, sourceUpdatedAt time.Time) error {
	var targets []targetRef
	if err := s.collectTargetsForFieldTx(tx, fieldDefinitionID, &targets); err != nil {
		return err
	}
	for _, target := range targets {
		if err := s.enqueueRebuildsTx(tx, repository, target, sourceUpdatedAt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) ExportManifest(ctx context.Context, owner, repo string) (Manifest, error) {
	fields, err := s.ListFieldDefinitions(ctx, owner, repo)
	if err != nil {
		return Manifest{}, err
	}

	manifest := Manifest{
		Version: "v1",
		Fields:  make([]FieldDefinitionInput, 0, len(fields)),
	}
	for _, field := range fields {
		manifest.Fields = append(manifest.Fields, fieldToInput(field))
	}
	return manifest, nil
}

//nolint:gocognit // Manifest import is a single transactional workflow with create-or-update branching.
func (s *Service) ImportManifest(ctx context.Context, actor permissions.Actor, owner, repo string, manifest Manifest, idempotencyKey string) ([]database.FieldDefinition, error) {
	repository, err := s.EnsureRepository(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	if err := s.requireWrite(ctx, actor, repository); err != nil {
		return nil, err
	}
	if len(manifest.Fields) == 0 {
		return nil, &FailError{StatusCode: 400, Message: "manifest has no fields"}
	}

	for _, field := range manifest.Fields {
		if err := validateFieldDefinitionInput(field); err != nil {
			return nil, err
		}
	}

	var out []database.FieldDefinition
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, input := range manifest.Fields {
			existing, found, err := loadManifestFieldTx(tx, repository.GitHubRepositoryID, input)
			if err != nil {
				return err
			}
			model, err := s.applyManifestFieldTx(ctx, tx, repository, actor, input, existing, found, idempotencyKey)
			if err != nil {
				return err
			}
			out = append(out, model)
		}
		return nil
	})
	return out, err
}

func loadManifestFieldTx(tx *gorm.DB, repositoryID int64, input FieldDefinitionInput) (database.FieldDefinition, bool, error) {
	name := normalizeFieldName(input.Name)
	var existing database.FieldDefinition
	err := tx.Where("github_repository_id = ? AND name = ? AND object_scope = ?", repositoryID, name, input.ObjectScope).First(&existing).Error
	switch {
	case err == nil:
		return existing, true, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return database.FieldDefinition{}, false, nil
	default:
		return database.FieldDefinition{}, false, err
	}
}

func (s *Service) applyManifestFieldTx(ctx context.Context, tx *gorm.DB, repository database.RepositoryProjection, actor permissions.Actor, input FieldDefinitionInput, existing database.FieldDefinition, found bool, idempotencyKey string) (database.FieldDefinition, error) {
	if !found {
		return s.createManifestFieldTx(tx, repository, actor, input, idempotencyKey)
	}
	return s.updateManifestFieldTx(ctx, tx, repository, actor, input, existing, idempotencyKey)
}

func (s *Service) createManifestFieldTx(tx *gorm.DB, repository database.RepositoryProjection, actor permissions.Actor, input FieldDefinitionInput, idempotencyKey string) (database.FieldDefinition, error) {
	enumValuesJSON := manifestEnumValuesJSON(input)
	model := database.FieldDefinition{
		GitHubRepositoryID: repository.GitHubRepositoryID,
		RepositoryOwner:    repository.Owner,
		RepositoryName:     repository.Name,
		Name:               normalizeFieldName(input.Name),
		DisplayName:        displayName(input),
		ObjectScope:        input.ObjectScope,
		FieldType:          input.FieldType,
		EnumValuesJSON:     enumValuesJSON,
		IsRequired:         input.IsRequired,
		IsFilterable:       input.IsFilterable,
		IsSearchable:       input.IsSearchable,
		IsVectorized:       input.IsVectorized,
		SortOrder:          input.SortOrder,
		CreatedBy:          actor.ID,
		UpdatedBy:          actor.ID,
		RowVersion:         1,
	}
	if err := tx.Create(&model).Error; err != nil {
		return database.FieldDefinition{}, err
	}
	if err := s.appendFieldDefinitionEventTx(tx, repository.GitHubRepositoryID, "field_definition.created", actor, idempotencyKey, model); err != nil {
		return database.FieldDefinition{}, err
	}
	return model, nil
}

func (s *Service) updateManifestFieldTx(ctx context.Context, tx *gorm.DB, repository database.RepositoryProjection, actor permissions.Actor, input FieldDefinitionInput, existing database.FieldDefinition, idempotencyKey string) (database.FieldDefinition, error) {
	if err := validateManifestFieldType(existing, input.FieldType); err != nil {
		return database.FieldDefinition{}, err
	}
	if err := s.ensureManifestEnumCompatibility(ctx, existing, input); err != nil {
		return database.FieldDefinition{}, err
	}
	if err := tx.Model(&database.FieldDefinition{}).Where("id = ?", existing.ID).Updates(manifestFieldUpdates(actor, input)).Error; err != nil {
		return database.FieldDefinition{}, err
	}
	if err := tx.First(&existing, existing.ID).Error; err != nil {
		return database.FieldDefinition{}, err
	}
	if err := s.appendFieldDefinitionEventTx(tx, repository.GitHubRepositoryID, "field_definition.updated", actor, idempotencyKey, existing); err != nil {
		return database.FieldDefinition{}, err
	}
	if err := s.enqueueFieldTargetRebuildsTx(tx, repository, existing.ID, time.Now().UTC()); err != nil {
		return database.FieldDefinition{}, err
	}
	return existing, nil
}

func manifestEnumValuesJSON(input FieldDefinitionInput) datatypes.JSON {
	raw, _ := json.Marshal(normalizeEnumValues(input.EnumValues))
	return datatypes.JSON(raw)
}

func validateManifestFieldType(existing database.FieldDefinition, requestedType string) error {
	if existing.FieldType == requestedType {
		return nil
	}
	return &FailError{
		StatusCode: 409,
		Message:    "field_type cannot change for an existing field",
		Data: map[string]any{
			"field":              existing.Name,
			"object_scope":       existing.ObjectScope,
			"current_field_type": existing.FieldType,
			"requested_type":     requestedType,
		},
	}
}

func (s *Service) ensureManifestEnumCompatibility(ctx context.Context, existing database.FieldDefinition, input FieldDefinitionInput) error {
	if existing.FieldType != "enum" && existing.FieldType != "multi_enum" {
		return nil
	}
	return s.ensureEnumValuesCompatible(ctx, existing, normalizeEnumValues(input.EnumValues))
}

func manifestFieldUpdates(actor permissions.Actor, input FieldDefinitionInput) map[string]any {
	return map[string]any{
		"display_name":     displayName(input),
		"enum_values_json": manifestEnumValuesJSON(input),
		"is_required":      input.IsRequired,
		"is_filterable":    input.IsFilterable,
		"is_searchable":    input.IsSearchable,
		"is_vectorized":    input.IsVectorized,
		"sort_order":       input.SortOrder,
		"updated_by":       actor.ID,
		"row_version":      gorm.Expr("row_version + 1"),
	}
}

func (s *Service) CreateGroup(ctx context.Context, actor permissions.Actor, owner, repo string, input GroupInput, idempotencyKey string) (database.Group, error) {
	repository, err := s.EnsureRepository(ctx, owner, repo)
	if err != nil {
		return database.Group{}, err
	}
	if err := s.requireWrite(ctx, actor, repository); err != nil {
		return database.Group{}, err
	}
	if err := validateGroupInput(input); err != nil {
		return database.Group{}, err
	}

	group := database.Group{
		GitHubRepositoryID: repository.GitHubRepositoryID,
		RepositoryOwner:    repository.Owner,
		RepositoryName:     repository.Name,
		Kind:               input.Kind,
		Title:              strings.TrimSpace(input.Title),
		Description:        strings.TrimSpace(input.Description),
		Status:             defaultStatus(input.Status),
		CreatedBy:          actor.ID,
		UpdatedBy:          actor.ID,
		RowVersion:         1,
	}
	for attempts := 0; attempts < 20; attempts++ {
		group.PublicID, err = publicid.NewGroupID()
		if err != nil {
			return database.Group{}, err
		}
		err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return s.createGroupTx(tx, &group, repository, actor, idempotencyKey)
		})
		if err == nil {
			return group, nil
		}
		if !isGroupPublicIDConflict(err) {
			return database.Group{}, translateDBError(err)
		}
	}
	return database.Group{}, &FailError{StatusCode: 409, Message: "could not allocate group id"}
}

func (s *Service) UpdateGroup(ctx context.Context, actor permissions.Actor, groupPublicID string, input GroupPatchInput, idempotencyKey string) (database.Group, error) {
	if err := validateGroupPatchInput(input); err != nil {
		return database.Group{}, err
	}

	group, err := s.lookupGroupByPublicID(ctx, groupPublicID)
	if err != nil {
		return database.Group{}, translateDBError(err)
	}
	repository := database.RepositoryProjection{
		GitHubRepositoryID: group.GitHubRepositoryID,
		Owner:              group.RepositoryOwner,
		Name:               group.RepositoryName,
	}
	if err := s.requireWrite(ctx, actor, repository); err != nil {
		return database.Group{}, err
	}
	if err := ensureExpectedRowVersion(group.RowVersion, input.ExpectedRowVersion); err != nil {
		return database.Group{}, err
	}

	updates, err := groupUpdates(actor, input)
	if err != nil {
		return database.Group{}, err
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.updateGroupTx(tx, &group, repository, actor, input.ExpectedRowVersion, updates, idempotencyKey)
	})
	if err != nil {
		return database.Group{}, translateDBError(err)
	}
	return group, nil
}

func (s *Service) ListGroups(ctx context.Context, owner, repo string) ([]GroupListView, error) {
	repository, err := s.readRepositoryProjection(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	var groups []database.Group
	err = s.db.WithContext(ctx).
		Where("github_repository_id = ?", repository.GitHubRepositoryID).
		Order("updated_at DESC, id DESC").
		Find(&groups).Error
	if err != nil {
		return nil, err
	}

	counts, err := s.listGroupMemberCounts(ctx, groups)
	if err != nil {
		return nil, err
	}

	views := make([]GroupListView, 0, len(groups))
	for _, group := range groups {
		view := GroupListView{
			Group:        group,
			MemberCounts: map[string]int{},
		}
		if count, ok := counts[group.ID]; ok {
			view.MemberCount = count.Total
			view.MemberCounts = count.ByType
		}
		views = append(views, view)
	}
	return views, nil
}

func (s *Service) GetGroup(ctx context.Context, groupPublicID string, options GetGroupOptions) (database.Group, []GroupMemberView, map[string]any, error) {
	group, err := s.lookupGroupByPublicID(ctx, groupPublicID)
	if err != nil {
		return database.Group{}, nil, nil, translateDBError(err)
	}

	var members []database.GroupMember
	membersTimer := database.StartQueryStep(ctx, "group_members")
	if err := s.db.WithContext(ctx).Where("group_id = ?", group.ID).Order("id ASC").Find(&members).Error; err != nil {
		membersTimer.Done()
		return database.Group{}, nil, nil, err
	}
	membersTimer.Done()

	memberViews := make([]GroupMemberView, 0, len(members))
	if options.IncludeMetadata {
		memberViews, err = s.enrichGroupMembers(ctx, group, members)
		if err != nil {
			return database.Group{}, nil, nil, err
		}
	} else {
		for _, member := range members {
			memberViews = append(memberViews, groupMemberViewFromModel(member, GroupMemberObjectSummary{}, false))
		}
	}

	annotations, err := s.getAnnotationsForTarget(ctx, "group", group.GitHubRepositoryID, 0, &group.ID)
	return group, memberViews, annotations, err
}

func (s *Service) AddGroupMember(ctx context.Context, actor permissions.Actor, groupPublicID string, objectType string, objectNumber int, idempotencyKey string) (database.GroupMember, error) {
	group, err := s.lookupGroupByPublicID(ctx, groupPublicID)
	if err != nil {
		return database.GroupMember{}, translateDBError(err)
	}
	repository := database.RepositoryProjection{
		GitHubRepositoryID: group.GitHubRepositoryID,
		Owner:              group.RepositoryOwner,
		Name:               group.RepositoryName,
	}
	if err := s.requireWrite(ctx, actor, repository); err != nil {
		return database.GroupMember{}, err
	}
	if err := validateMemberType(group.Kind, objectType); err != nil {
		return database.GroupMember{}, err
	}
	target, err := s.resolveTarget(ctx, repository, objectType, objectNumber, nil)
	if err != nil {
		return database.GroupMember{}, err
	}
	member := database.GroupMember{
		GroupID:            group.ID,
		GitHubRepositoryID: group.GitHubRepositoryID,
		ObjectType:         target.TargetType,
		ObjectNumber:       target.ObjectNumber,
		TargetKey:          target.TargetKey,
		AddedBy:            actor.ID,
		AddedAt:            time.Now().UTC(),
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.addGroupMemberTx(tx, group, repository, actor, &member, idempotencyKey)
	})
	return member, translateDBError(err)
}

func (s *Service) createGroupTx(tx *gorm.DB, group *database.Group, repository database.RepositoryProjection, actor permissions.Actor, idempotencyKey string) error {
	if err := tx.Create(group).Error; err != nil {
		return err
	}
	if err := s.appendGroupEventTx(tx, repository.GitHubRepositoryID, "group.created", actor, idempotencyKey, *group, map[string]any{
		"group_id":        group.ID,
		"group_public_id": group.PublicID,
		"title":           group.Title,
		"kind":            group.Kind,
	}, nil); err != nil {
		return err
	}
	return s.enqueueRebuildsTx(tx, repository, groupTargetRef(*group), time.Now().UTC())
}

func groupUpdates(actor permissions.Actor, input GroupPatchInput) (map[string]any, error) {
	updates := map[string]any{
		"updated_by":  actor.ID,
		"updated_at":  time.Now().UTC(),
		"row_version": gorm.Expr("row_version + 1"),
	}
	if input.Title != nil {
		value := strings.TrimSpace(*input.Title)
		if value == "" {
			return nil, &FailError{StatusCode: 400, Message: "group title is required"}
		}
		updates["title"] = value
	}
	if input.Description != nil {
		updates["description"] = strings.TrimSpace(*input.Description)
	}
	if input.Status != nil {
		value := strings.TrimSpace(*input.Status)
		if value == "" {
			return nil, &FailError{StatusCode: 400, Message: "group status is required"}
		}
		updates["status"] = value
	}
	return updates, nil
}

func (s *Service) updateGroupTx(tx *gorm.DB, group *database.Group, repository database.RepositoryProjection, actor permissions.Actor, expectedRowVersion *int, updates map[string]any, idempotencyKey string) error {
	if err := applyGroupUpdatesTx(tx, group.ID, expectedRowVersion, updates); err != nil {
		return err
	}
	if err := tx.First(group, group.ID).Error; err != nil {
		return err
	}
	if err := s.appendGroupEventTx(tx, group.GitHubRepositoryID, "group.updated", actor, idempotencyKey, *group, map[string]any{
		"group_id":        group.ID,
		"group_public_id": group.PublicID,
		"title":           group.Title,
		"status":          group.Status,
	}, nil); err != nil {
		return err
	}
	return s.enqueueRebuildsTx(tx, repository, groupTargetRef(*group), time.Now().UTC())
}

func applyGroupUpdatesTx(tx *gorm.DB, groupID uint, expectedRowVersion *int, updates map[string]any) error {
	query := tx.Model(&database.Group{}).Where("id = ?", groupID)
	if expectedRowVersion != nil {
		query = query.Where("row_version = ?", *expectedRowVersion)
	}
	result := query.Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if expectedRowVersion != nil && result.RowsAffected == 0 {
		return staleRowVersionError(tx, &database.Group{}, groupID, *expectedRowVersion)
	}
	return nil
}

func (s *Service) addGroupMemberTx(tx *gorm.DB, group database.Group, repository database.RepositoryProjection, actor permissions.Actor, member *database.GroupMember, idempotencyKey string) error {
	if err := insertGroupMemberTx(tx, group, member, s.translateGroupMemberConflictTx); err != nil {
		return err
	}
	memberRef := targetRef{
		RepositoryID: group.GitHubRepositoryID,
		Owner:        group.RepositoryOwner,
		Name:         group.RepositoryName,
		TargetType:   member.ObjectType,
		TargetKey:    member.TargetKey,
		ObjectNumber: member.ObjectNumber,
	}
	if err := s.appendGroupEventTx(tx, group.GitHubRepositoryID, "group.member_added", actor, idempotencyKey, group, map[string]any{
		"group_id":        group.ID,
		"group_public_id": group.PublicID,
		"object_type":     member.ObjectType,
		"object_number":   member.ObjectNumber,
	}, []eventRefInput{{Role: "member", Type: member.ObjectType, Key: member.TargetKey}}); err != nil {
		return err
	}
	if err := s.enqueueRebuildsTx(tx, repository, groupTargetRef(group), time.Now().UTC()); err != nil {
		return err
	}
	return s.enqueueRebuildsTx(tx, repository, memberRef, time.Now().UTC())
}

func insertGroupMemberTx(tx *gorm.DB, group database.Group, member *database.GroupMember, conflictTranslator func(*gorm.DB, database.Group, database.GroupMember) error) error {
	const memberInsertSavepoint = "group_member_insert"
	if err := tx.SavePoint(memberInsertSavepoint).Error; err != nil {
		return err
	}
	if err := tx.Create(member).Error; err != nil {
		if isGroupMemberConflict(err) {
			if rollbackErr := tx.RollbackTo(memberInsertSavepoint).Error; rollbackErr != nil {
				return rollbackErr
			}
			return conflictTranslator(tx, group, *member)
		}
		return err
	}
	return nil
}

func (s *Service) appendGroupEventTx(tx *gorm.DB, repositoryID int64, eventType string, actor permissions.Actor, idempotencyKey string, group database.Group, payload map[string]any, refs []eventRefInput) error {
	return s.appendEventTx(tx, eventInput{
		RepositoryID:   repositoryID,
		AggregateType:  "group",
		AggregateKey:   groupTargetKey(group.PublicID),
		EventType:      eventType,
		Actor:          actor,
		IdempotencyKey: idempotencyKey,
		Payload:        payload,
		Refs:           refs,
	})
}

func groupTargetRef(group database.Group) targetRef {
	return targetRef{
		RepositoryID: group.GitHubRepositoryID,
		Owner:        group.RepositoryOwner,
		Name:         group.RepositoryName,
		TargetType:   "group",
		TargetKey:    groupTargetKey(group.PublicID),
		GroupID:      &group.ID,
	}
}

func (s *Service) RemoveGroupMember(ctx context.Context, actor permissions.Actor, groupPublicID string, memberID uint, idempotencyKey string) error {
	group, err := s.lookupGroupByPublicID(ctx, groupPublicID)
	if err != nil {
		return translateDBError(err)
	}
	repository := database.RepositoryProjection{
		GitHubRepositoryID: group.GitHubRepositoryID,
		Owner:              group.RepositoryOwner,
		Name:               group.RepositoryName,
	}
	if err := s.requireWrite(ctx, actor, repository); err != nil {
		return err
	}

	var member database.GroupMember
	if err := s.db.WithContext(ctx).Where("group_id = ? AND id = ?", group.ID, memberID).First(&member).Error; err != nil {
		return translateDBError(err)
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&database.GroupMember{}, memberID).Error; err != nil {
			return err
		}
		if err := s.appendEventTx(tx, eventInput{
			RepositoryID:   group.GitHubRepositoryID,
			AggregateType:  "group",
			AggregateKey:   groupTargetKey(group.PublicID),
			EventType:      "group.member_removed",
			Actor:          actor,
			IdempotencyKey: idempotencyKey,
			Payload: map[string]any{
				"group_id":        group.ID,
				"group_public_id": group.PublicID,
				"member_id":       memberID,
			},
			Refs: []eventRefInput{{Role: "member", Type: member.ObjectType, Key: member.TargetKey}},
		}); err != nil {
			return err
		}
		return s.enqueueRebuildsTx(tx, repository, targetRef{
			RepositoryID: group.GitHubRepositoryID,
			Owner:        group.RepositoryOwner,
			Name:         group.RepositoryName,
			TargetType:   "group",
			TargetKey:    groupTargetKey(group.PublicID),
			GroupID:      &group.ID,
		}, time.Now().UTC())
	})
}

func (s *Service) SyncGroupComments(ctx context.Context, actor permissions.Actor, groupPublicID string) (GroupCommentSyncResult, error) {
	group, err := s.lookupGroupByPublicID(ctx, groupPublicID)
	if err != nil {
		return GroupCommentSyncResult{}, translateDBError(err)
	}
	repository := database.RepositoryProjection{
		GitHubRepositoryID: group.GitHubRepositoryID,
		Owner:              group.RepositoryOwner,
		Name:               group.RepositoryName,
	}
	if err := s.requireWrite(ctx, actor, repository); err != nil {
		return GroupCommentSyncResult{}, err
	}
	if s.commentSync == nil {
		return GroupCommentSyncResult{}, &FailError{StatusCode: 503, Message: "github comment sync is not configured"}
	}
	return s.commentSync.TriggerGroupSync(ctx, groupPublicID)
}

func (s *Service) ListGroupCommentSyncTargets(ctx context.Context, actor permissions.Actor, owner, repo string) ([]GroupCommentSyncTargetStatusView, error) {
	repository, err := s.readRepositoryProjection(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	if err := s.requireWrite(ctx, actor, repository); err != nil {
		return nil, err
	}

	type syncTargetRow struct {
		database.GroupCommentSyncTarget
		GroupPublicID string `gorm:"column:group_public_id"`
		GroupTitle    string `gorm:"column:group_title"`
	}

	var rows []syncTargetRow
	if err := s.db.WithContext(ctx).
		Table("group_comment_sync_targets").
		Select("group_comment_sync_targets.*, groups.public_id AS group_public_id, groups.title AS group_title").
		Joins("JOIN groups ON groups.id = group_comment_sync_targets.group_id").
		Where("group_comment_sync_targets.github_repository_id = ?", repository.GitHubRepositoryID).
		Where("(group_comment_sync_targets.last_error_at IS NOT NULL) OR (group_comment_sync_targets.desired_revision > group_comment_sync_targets.applied_revision)").
		Order("CASE WHEN group_comment_sync_targets.last_error_at IS NULL THEN 1 ELSE 0 END ASC").
		Order("group_comment_sync_targets.last_error_at DESC").
		Order("group_comment_sync_targets.updated_at DESC").
		Order("groups.public_id ASC").
		Order("group_comment_sync_targets.object_number ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}

	views := make([]GroupCommentSyncTargetStatusView, 0, len(rows))
	for _, row := range rows {
		state := "pending"
		if row.LastErrorAt != nil {
			state = "failed"
		}
		views = append(views, GroupCommentSyncTargetStatusView{
			GroupID:         row.GroupPublicID,
			GroupTitle:      row.GroupTitle,
			ObjectType:      row.ObjectType,
			ObjectNumber:    row.ObjectNumber,
			TargetKey:       row.TargetKey,
			DesiredRevision: row.DesiredRevision,
			AppliedRevision: row.AppliedRevision,
			DesiredDeleted:  row.DesiredDeleted,
			State:           state,
			LastErrorKind:   row.LastErrorKind,
			LastError:       row.LastError,
			LastErrorAt:     row.LastErrorAt,
			UpdatedAt:       row.UpdatedAt,
		})
	}
	return views, nil
}

func (s *Service) lookupRepositoryProjection(ctx context.Context, owner, repo string) (database.RepositoryProjection, error) {
	var repository database.RepositoryProjection
	err := s.db.WithContext(ctx).
		Where("owner = ? AND name = ?", strings.TrimSpace(owner), strings.TrimSpace(repo)).
		First(&repository).Error
	return repository, err
}

func (s *Service) lookupRepositoryProjectionByGitHubID(ctx context.Context, githubRepositoryID int64) (database.RepositoryProjection, error) {
	var repository database.RepositoryProjection
	err := s.db.WithContext(ctx).
		Where("github_repository_id = ?", githubRepositoryID).
		First(&repository).Error
	return repository, err
}

//nolint:gocognit // Annotation writes need to stay in one transactional workflow to preserve validation and event ordering.
func (s *Service) SetAnnotations(ctx context.Context, actor permissions.Actor, owner, repo, targetType string, objectNumber int, groupID *uint, values map[string]any, idempotencyKey string) (AnnotationSetResult, error) {
	repository, err := s.EnsureRepository(ctx, owner, repo)
	if err != nil {
		return AnnotationSetResult{}, err
	}
	if err := s.requireWrite(ctx, actor, repository); err != nil {
		return AnnotationSetResult{}, err
	}
	target, err := s.resolveTarget(ctx, repository, targetType, objectNumber, groupID)
	if err != nil {
		return AnnotationSetResult{}, err
	}
	if len(values) == 0 {
		return AnnotationSetResult{}, &FailError{StatusCode: 400, Message: "no annotations provided"}
	}

	result := AnnotationSetResult{
		TargetKey:   target.TargetKey,
		Annotations: map[string]any{},
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		definitions, err := s.loadFieldDefinitionsTx(tx, repository.GitHubRepositoryID, target.ApplicableScope())
		if err != nil {
			return err
		}
		byName := fieldDefinitionsByName(definitions)
		for rawField, rawValue := range values {
			if err := s.applyAnnotationValueTx(tx, repository, target, actor, byName, rawField, rawValue, idempotencyKey, result.Annotations); err != nil {
				return err
			}
		}

		return s.enqueueRebuildsTx(tx, repository, target, target.SourceUpdatedAt())
	})
	if err != nil {
		return AnnotationSetResult{}, translateDBError(err)
	}
	return result, nil
}

func fieldDefinitionsByName(definitions []database.FieldDefinition) map[string]database.FieldDefinition {
	byName := make(map[string]database.FieldDefinition, len(definitions))
	for _, definition := range definitions {
		byName[definition.Name] = definition
	}
	return byName
}

func (s *Service) applyAnnotationValueTx(
	tx *gorm.DB,
	repository database.RepositoryProjection,
	target targetRef,
	actor permissions.Actor,
	byName map[string]database.FieldDefinition,
	rawField string,
	rawValue any,
	idempotencyKey string,
	annotations map[string]any,
) error {
	fieldName := normalizeFieldName(rawField)
	definition, ok := byName[fieldName]
	if !ok {
		return &FailError{StatusCode: 400, Message: "unknown field", Data: map[string]any{"field": fieldName}}
	}
	converted, clearValue, err := convertAnnotationValue(definition, rawValue)
	if err != nil {
		return err
	}
	if clearValue {
		return s.clearAnnotationValueTx(tx, repository, target, actor, definition, fieldName, idempotencyKey, annotations)
	}
	return s.setAnnotationValueTx(tx, repository, target, actor, definition, converted, fieldName, idempotencyKey, annotations)
}

func (s *Service) clearAnnotationValueTx(
	tx *gorm.DB,
	repository database.RepositoryProjection,
	target targetRef,
	actor permissions.Actor,
	definition database.FieldDefinition,
	fieldName string,
	idempotencyKey string,
	annotations map[string]any,
) error {
	if err := tx.Where("field_definition_id = ? AND target_type = ? AND target_key = ?", definition.ID, target.TargetType, target.TargetKey).
		Delete(&database.FieldValue{}).Error; err != nil {
		return err
	}
	annotations[fieldName] = nil
	return s.appendFieldValueEventTx(tx, repository.GitHubRepositoryID, target, actor, "field_value.cleared", definition, nil, idempotencyKey)
}

func (s *Service) setAnnotationValueTx(
	tx *gorm.DB,
	repository database.RepositoryProjection,
	target targetRef,
	actor permissions.Actor,
	definition database.FieldDefinition,
	converted convertedValue,
	fieldName string,
	idempotencyKey string,
	annotations map[string]any,
) error {
	model := annotationFieldValueModel(repository, target, actor, definition.ID, converted)
	if err := upsertAnnotationFieldValueTx(tx, model, converted, actor.ID); err != nil {
		return err
	}
	annotations[fieldName] = converted.APIValue
	return s.appendFieldValueEventTx(tx, repository.GitHubRepositoryID, target, actor, "field_value.set", definition, converted.APIValue, idempotencyKey)
}

func annotationFieldValueModel(repository database.RepositoryProjection, target targetRef, actor permissions.Actor, fieldDefinitionID uint, converted convertedValue) database.FieldValue {
	return database.FieldValue{
		FieldDefinitionID:  fieldDefinitionID,
		GitHubRepositoryID: repository.GitHubRepositoryID,
		RepositoryOwner:    repository.Owner,
		RepositoryName:     repository.Name,
		TargetType:         target.TargetType,
		ObjectNumber:       target.ObjectNumberPtr(),
		GroupID:            target.GroupID,
		TargetKey:          target.TargetKey,
		UpdatedBy:          actor.ID,
		StringValue:        converted.StringValue,
		TextValue:          converted.TextValue,
		BoolValue:          converted.BoolValue,
		IntValue:           converted.IntValue,
		EnumValue:          converted.EnumValue,
		MultiEnumJSON:      converted.MultiEnumJSON,
	}
}

func upsertAnnotationFieldValueTx(tx *gorm.DB, model database.FieldValue, converted convertedValue, actorID string) error {
	var existing database.FieldValue
	err := tx.Where("field_definition_id = ? AND target_type = ? AND target_key = ?", model.FieldDefinitionID, model.TargetType, model.TargetKey).First(&existing).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return tx.Create(&model).Error
	case err != nil:
		return err
	default:
		return tx.Model(&database.FieldValue{}).Where("id = ?", existing.ID).Updates(map[string]any{
			"updated_by":      actorID,
			"string_value":    converted.StringValue,
			"text_value":      converted.TextValue,
			"bool_value":      converted.BoolValue,
			"int_value":       converted.IntValue,
			"enum_value":      converted.EnumValue,
			"multi_enum_json": converted.MultiEnumJSON,
			"updated_at":      time.Now().UTC(),
		}).Error
	}
}

func (s *Service) appendFieldValueEventTx(tx *gorm.DB, repositoryID int64, target targetRef, actor permissions.Actor, eventType string, definition database.FieldDefinition, value any, idempotencyKey string) error {
	payload := map[string]any{
		"field_definition_id": definition.ID,
		"field_name":          definition.Name,
	}
	if eventType == "field_value.set" {
		payload["value"] = value
	}
	return s.appendEventTx(tx, eventInput{
		RepositoryID:   repositoryID,
		AggregateType:  target.TargetType,
		AggregateKey:   target.TargetKey,
		EventType:      eventType,
		Actor:          actor,
		IdempotencyKey: idempotencyKey,
		Payload:        payload,
		Refs:           []eventRefInput{{Role: "field_definition", Type: "field_definition", Key: fieldAggregateKey(repositoryID, definition.Name, definition.ObjectScope)}},
	})
}

func (s *Service) GetAnnotations(ctx context.Context, owner, repo, targetType string, objectNumber int, groupID *uint) (AnnotationSetResult, error) {
	repository, err := s.readRepositoryProjection(ctx, owner, repo)
	if err != nil {
		return AnnotationSetResult{}, err
	}
	target, err := s.resolveTarget(ctx, repository, targetType, objectNumber, groupID)
	if err != nil {
		return AnnotationSetResult{}, err
	}
	annotations, err := s.getAnnotationsForTarget(ctx, target.TargetType, repository.GitHubRepositoryID, objectNumber, groupID)
	if err != nil {
		return AnnotationSetResult{}, err
	}
	return AnnotationSetResult{
		TargetKey:   target.TargetKey,
		Annotations: annotations,
	}, nil
}

func (s *Service) FilterTargets(ctx context.Context, owner, repo, targetType, fieldName string, rawValue string) ([]TargetFilterResult, error) {
	repository, err := s.readRepositoryProjection(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	definition, filterValue, err := s.filterDefinition(ctx, repository.GitHubRepositoryID, targetType, fieldName, rawValue)
	if err != nil {
		return nil, err
	}
	values, err := s.filteredFieldValues(ctx, definition, targetType, filterValue)
	if err != nil {
		return nil, err
	}
	return s.buildFilteredTargets(ctx, repository.GitHubRepositoryID, definition.FieldType, filterValue, values)
}

func (s *Service) filterDefinition(ctx context.Context, repositoryID int64, targetType, fieldName, rawValue string) (database.FieldDefinition, string, error) {
	name := normalizeFieldName(fieldName)
	definition, err := s.lookupFilterDefinition(ctx, repositoryID, name, targetType)
	if err != nil {
		return database.FieldDefinition{}, "", err
	}
	return definition, strings.TrimSpace(rawValue), nil
}

func (s *Service) lookupFilterDefinition(ctx context.Context, repositoryID int64, fieldName, targetType string) (database.FieldDefinition, error) {
	definition, err := s.lookupScopedFilterDefinition(ctx, repositoryID, fieldName, targetType)
	if err == nil || !errors.Is(err, gorm.ErrRecordNotFound) {
		return definition, translateDBError(err)
	}
	return s.lookupScopedFilterDefinition(ctx, repositoryID, fieldName, "all")
}

func (s *Service) lookupScopedFilterDefinition(ctx context.Context, repositoryID int64, fieldName, scope string) (database.FieldDefinition, error) {
	var definition database.FieldDefinition
	err := s.db.WithContext(ctx).
		Where("github_repository_id = ? AND name = ? AND archived_at IS NULL AND is_filterable = ? AND object_scope = ?", repositoryID, fieldName, true, scope).
		First(&definition).Error
	return definition, err
}

func (s *Service) filteredFieldValues(ctx context.Context, definition database.FieldDefinition, targetType, filterValue string) ([]database.FieldValue, error) {
	query, err := applyFieldValueFilter(s.db.WithContext(ctx).Model(&database.FieldValue{}).Where("field_definition_id = ? AND target_type = ?", definition.ID, targetType), definition.FieldType, filterValue)
	if err != nil {
		return nil, err
	}
	var values []database.FieldValue
	if err := query.Order("target_key ASC").Find(&values).Error; err != nil {
		return nil, err
	}
	return values, nil
}

func applyFieldValueFilter(query *gorm.DB, fieldType, filterValue string) (*gorm.DB, error) {
	switch fieldType {
	case "boolean":
		return query.Where("bool_value = ?", strings.EqualFold(filterValue, "true")), nil
	case "integer":
		parsed, err := strconv.ParseInt(filterValue, 10, 64)
		if err != nil {
			return nil, &FailError{StatusCode: 400, Message: "invalid integer filter"}
		}
		return query.Where("int_value = ?", parsed), nil
	case "enum":
		return query.Where("enum_value = ?", filterValue), nil
	case "multi_enum":
		return query, nil
	default:
		return query.Where("COALESCE(string_value, text_value, enum_value, '') = ?", filterValue), nil
	}
}

func (s *Service) buildFilteredTargets(ctx context.Context, repositoryID int64, fieldType, filterValue string, values []database.FieldValue) ([]TargetFilterResult, error) {
	matchedValues, err := matchingFieldValues(fieldType, filterValue, values)
	if err != nil {
		return nil, err
	}
	summaries, err := s.filteredTargetSummaries(ctx, repositoryID, matchedValues)
	if err != nil {
		return nil, err
	}
	annotations, err := s.getAnnotationsForTargetKeys(ctx, repositoryID, annotationTargetsForFieldValues(matchedValues))
	if err != nil {
		return nil, err
	}
	groups, err := s.groupsByID(ctx, groupIDsForFieldValues(matchedValues))
	if err != nil {
		return nil, err
	}

	results := make([]TargetFilterResult, 0, len(matchedValues))
	for _, value := range matchedValues {
		result, err := filteredTargetResultFromHydration(value, summaries, annotations, groups)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

func (s *Service) filteredTargetResult(ctx context.Context, repositoryID int64, fieldType, filterValue string, value database.FieldValue) (TargetFilterResult, bool, error) {
	results, err := s.buildFilteredTargets(ctx, repositoryID, fieldType, filterValue, []database.FieldValue{value})
	if err != nil {
		return TargetFilterResult{}, false, err
	}
	if len(results) == 0 {
		return TargetFilterResult{}, false, nil
	}
	return results[0], true, nil
}

func matchingFieldValues(fieldType, filterValue string, values []database.FieldValue) ([]database.FieldValue, error) {
	if fieldType != "multi_enum" {
		return values, nil
	}
	matched := make([]database.FieldValue, 0, len(values))
	for _, value := range values {
		matches, err := multiEnumContains(value.MultiEnumJSON, filterValue)
		if err != nil {
			return nil, err
		}
		if matches {
			matched = append(matched, value)
		}
	}
	return matched, nil
}

func (s *Service) filteredTargetSummaries(ctx context.Context, repositoryID int64, values []database.FieldValue) (map[string]GroupMemberObjectSummary, error) {
	refs := make([]ghreplica.ObjectRef, 0, len(values))
	for _, value := range values {
		if value.TargetType == "group" || value.ObjectNumber == nil {
			continue
		}
		refs = append(refs, ghreplica.ObjectRef{Type: value.TargetType, Number: *value.ObjectNumber})
	}
	if len(refs) == 0 {
		return map[string]GroupMemberObjectSummary{}, nil
	}
	return s.mirrorObjectSummaries(ctx, repositoryID, refs)
}

func groupIDsForFieldValues(values []database.FieldValue) []uint {
	ids := make([]uint, 0, len(values))
	seen := map[uint]struct{}{}
	for _, value := range values {
		if value.TargetType != "group" || value.GroupID == nil {
			continue
		}
		id := *value.GroupID
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func groupPublicIDsForSearchRows(rows []scoredSearchTarget) []string {
	ids := make([]string, 0, len(rows))
	seen := map[string]struct{}{}
	for _, row := range rows {
		if row.TargetType != "group" {
			continue
		}
		id, ok := groupPublicIDFromTargetKey(row.TargetKey)
		if !ok {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func (s *Service) groupsByID(ctx context.Context, ids []uint) (map[uint]database.Group, error) {
	if len(ids) == 0 {
		return map[uint]database.Group{}, nil
	}
	timer := database.StartQueryStep(ctx, "groups_batch")
	defer timer.Done()

	var groups []database.Group
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&groups).Error; err != nil {
		return nil, err
	}
	byID := make(map[uint]database.Group, len(groups))
	for _, group := range groups {
		byID[group.ID] = group
	}
	return byID, nil
}

func (s *Service) groupsByPublicID(ctx context.Context, publicIDs []string) (map[string]database.Group, error) {
	if len(publicIDs) == 0 {
		return map[string]database.Group{}, nil
	}
	timer := database.StartQueryStep(ctx, "groups_batch")
	defer timer.Done()

	var groups []database.Group
	if err := s.db.WithContext(ctx).Where("public_id IN ?", publicIDs).Find(&groups).Error; err != nil {
		return nil, err
	}
	byID := make(map[string]database.Group, len(groups))
	for _, group := range groups {
		byID[group.PublicID] = group
	}
	return byID, nil
}

func filteredTargetResultFromHydration(value database.FieldValue, summaries map[string]GroupMemberObjectSummary, annotations map[string]map[string]any, groups map[uint]database.Group) (TargetFilterResult, error) {
	result := TargetFilterResult{
		TargetType:   value.TargetType,
		ObjectNumber: intValueOrZero(value.ObjectNumber),
		TargetKey:    value.TargetKey,
		Annotations:  annotations[annotationMapKey(value.TargetType, value.TargetKey)],
	}
	if result.Annotations == nil {
		result.Annotations = map[string]any{}
	}
	if summary, ok := summaries[value.TargetKey]; ok {
		result.ObjectSummary = &summary
	}
	if value.TargetType == "group" && value.GroupID != nil {
		group, ok := groups[*value.GroupID]
		if !ok {
			return TargetFilterResult{}, ErrNotFound
		}
		result.ID = group.PublicID
	}
	return result, nil
}

func (s *Service) getAnnotationsForTarget(ctx context.Context, targetType string, repositoryID int64, objectNumber int, groupID *uint) (map[string]any, error) {
	timer := database.StartQueryStep(ctx, "annotations_single")
	defer timer.Done()

	targetKey := objectTargetKey(repositoryID, targetType, objectNumber)
	if targetType == "group" && groupID != nil {
		group, err := s.lookupGroupByID(ctx, *groupID)
		if err != nil {
			return nil, translateDBError(err)
		}
		targetKey = groupTargetKey(group.PublicID)
	}

	var values []database.FieldValue
	if err := s.db.WithContext(ctx).Preload("FieldDefinition").
		Where("github_repository_id = ? AND target_type = ? AND target_key = ?", repositoryID, targetType, targetKey).
		Order("field_definition_id ASC").
		Find(&values).Error; err != nil {
		return nil, err
	}

	annotations := map[string]any{}
	for _, value := range values {
		annotations[value.FieldDefinition.Name] = fieldValueToAPI(value)
	}
	return annotations, nil
}

type annotationTarget struct {
	targetType string
	targetKey  string
}

func annotationTargetsForSearchRows(rows []scoredSearchTarget) []annotationTarget {
	targets := make([]annotationTarget, 0, len(rows))
	for _, row := range rows {
		if row.TargetKey == "" {
			continue
		}
		targets = append(targets, annotationTarget{targetType: row.TargetType, targetKey: row.TargetKey})
	}
	return targets
}

func annotationTargetsForFieldValues(values []database.FieldValue) []annotationTarget {
	targets := make([]annotationTarget, 0, len(values))
	for _, value := range values {
		if value.TargetKey == "" {
			continue
		}
		targets = append(targets, annotationTarget{targetType: value.TargetType, targetKey: value.TargetKey})
	}
	return targets
}

func (s *Service) getAnnotationsForTargetKeys(ctx context.Context, repositoryID int64, targets []annotationTarget) (map[string]map[string]any, error) {
	filters := collectAnnotationTargetFilters(targets)
	if len(filters.targetKeys) == 0 {
		return filters.result, nil
	}
	timer := database.StartQueryStep(ctx, "annotations_batch")
	defer timer.Done()

	var values []database.FieldValue
	if err := s.db.WithContext(ctx).Preload("FieldDefinition").
		Where("github_repository_id = ? AND target_type IN ? AND target_key IN ?", repositoryID, filters.targetTypes, filters.targetKeys).
		Order("target_key ASC, field_definition_id ASC").
		Find(&values).Error; err != nil {
		return nil, err
	}
	for _, value := range values {
		mapKey := annotationMapKey(value.TargetType, value.TargetKey)
		if _, ok := filters.wanted[mapKey]; !ok {
			continue
		}
		if filters.result[mapKey] == nil {
			filters.result[mapKey] = map[string]any{}
		}
		filters.result[mapKey][value.FieldDefinition.Name] = fieldValueToAPI(value)
	}
	return filters.result, nil
}

type annotationTargetFilters struct {
	result      map[string]map[string]any
	wanted      map[string]struct{}
	targetKeys  []string
	targetTypes []string
}

func collectAnnotationTargetFilters(targets []annotationTarget) annotationTargetFilters {
	filters := annotationTargetFilters{
		result:      make(map[string]map[string]any, len(targets)),
		wanted:      map[string]struct{}{},
		targetKeys:  make([]string, 0, len(targets)),
		targetTypes: make([]string, 0, len(targets)),
	}
	seenKeys := map[string]struct{}{}
	seenTypes := map[string]struct{}{}
	for _, target := range targets {
		if target.targetType == "" || target.targetKey == "" {
			continue
		}
		mapKey := annotationMapKey(target.targetType, target.targetKey)
		filters.result[mapKey] = map[string]any{}
		filters.wanted[mapKey] = struct{}{}
		if _, ok := seenTypes[target.targetType]; !ok {
			seenTypes[target.targetType] = struct{}{}
			filters.targetTypes = append(filters.targetTypes, target.targetType)
		}
		if _, ok := seenKeys[target.targetKey]; !ok {
			seenKeys[target.targetKey] = struct{}{}
			filters.targetKeys = append(filters.targetKeys, target.targetKey)
		}
	}
	return filters
}

func annotationMapKey(targetType, targetKey string) string {
	return targetType + "\x00" + targetKey
}

func (s *Service) resolveTarget(ctx context.Context, repository database.RepositoryProjection, targetType string, objectNumber int, groupID *uint) (targetRef, error) {
	switch targetType {
	case "pull_request", "issue":
		summaries, err := s.mirrorObjectSummaries(ctx, repository.GitHubRepositoryID, []ghreplica.ObjectRef{{Type: targetType, Number: objectNumber}})
		if err != nil {
			return targetRef{}, err
		}
		summary, ok := summaries[objectTargetKey(repository.GitHubRepositoryID, targetType, objectNumber)]
		if !ok {
			return targetRef{}, ErrNotFound
		}
		return targetRef{
			RepositoryID:         repository.GitHubRepositoryID,
			Owner:                repository.Owner,
			Name:                 repository.Name,
			TargetType:           targetType,
			TargetKey:            objectTargetKey(repository.GitHubRepositoryID, targetType, objectNumber),
			ObjectNumber:         objectNumber,
			SourceUpdatedAtValue: summary.UpdatedAt,
		}, nil
	case "group":
		if groupID == nil || *groupID == 0 {
			return targetRef{}, &FailError{StatusCode: 400, Message: "group_id is required"}
		}
		group, err := s.lookupGroupByID(ctx, *groupID)
		if err != nil {
			return targetRef{}, translateDBError(err)
		}
		return targetRef{
			RepositoryID: repository.GitHubRepositoryID,
			Owner:        repository.Owner,
			Name:         repository.Name,
			TargetType:   "group",
			TargetKey:    groupTargetKey(group.PublicID),
			GroupID:      groupID,
		}, nil
	default:
		return targetRef{}, &FailError{StatusCode: 400, Message: "unsupported target_type"}
	}
}

func (s *Service) enrichGroupMembers(ctx context.Context, group database.Group, members []database.GroupMember) ([]GroupMemberView, error) {
	refs := mirrorObjectRefsForMembers(members)
	summaries, err := s.mirrorObjectSummaries(ctx, group.GitHubRepositoryID, refs)
	if err != nil {
		return nil, err
	}

	views := make([]GroupMemberView, 0, len(members))
	for _, member := range members {
		summary, ok := summaries[member.TargetKey]
		views = append(views, groupMemberViewFromModel(member, summary, ok))
	}
	return views, nil
}

type groupMemberCounts struct {
	Total  int
	ByType map[string]int
}

type groupMemberCountRow struct {
	GroupID    uint
	ObjectType string
	Count      int64
}

func (s *Service) listGroupMemberCounts(ctx context.Context, groups []database.Group) (map[uint]groupMemberCounts, error) {
	if len(groups) == 0 {
		return map[uint]groupMemberCounts{}, nil
	}

	groupIDs := make([]uint, 0, len(groups))
	for _, group := range groups {
		groupIDs = append(groupIDs, group.ID)
	}

	var rows []groupMemberCountRow
	if err := s.db.WithContext(ctx).
		Model(&database.GroupMember{}).
		Select("group_id, object_type, COUNT(*) AS count").
		Where("group_id IN ?", groupIDs).
		Group("group_id, object_type").
		Find(&rows).Error; err != nil {
		return nil, err
	}

	counts := make(map[uint]groupMemberCounts, len(groupIDs))
	for _, row := range rows {
		entry := counts[row.GroupID]
		if entry.ByType == nil {
			entry.ByType = map[string]int{}
		}
		entry.ByType[row.ObjectType] = int(row.Count)
		entry.Total += int(row.Count)
		counts[row.GroupID] = entry
	}
	return counts, nil
}

func mirrorObjectRefsForMembers(members []database.GroupMember) []ghreplica.ObjectRef {
	refs := make([]ghreplica.ObjectRef, 0, len(members))
	for _, member := range members {
		if member.ObjectType != "pull_request" && member.ObjectType != "issue" {
			continue
		}
		refs = append(refs, ghreplica.ObjectRef{Type: member.ObjectType, Number: member.ObjectNumber})
	}
	return refs
}

func (s *Service) mirrorObjectSummaries(ctx context.Context, repositoryID int64, refs []ghreplica.ObjectRef) (map[string]GroupMemberObjectSummary, error) {
	timer := database.StartQueryStep(ctx, "mirror_batch_objects")
	defer timer.Done()

	results, err := s.ghreplica.BatchGetObjects(ctx, repositoryID, refs)
	if err != nil {
		return nil, err
	}
	summaries := make(map[string]GroupMemberObjectSummary, len(results))
	for _, result := range results {
		if !result.Found || result.Summary == nil {
			continue
		}
		summaries[objectTargetKey(repositoryID, result.Type, result.Number)] = objectSummaryFromMirror(*result.Summary)
	}
	return summaries, nil
}

func objectSummaryFromMirror(summary ghreplica.ObjectSummary) GroupMemberObjectSummary {
	return GroupMemberObjectSummary{
		Title:       summary.Title,
		State:       summary.State,
		HTMLURL:     summary.HTMLURL,
		AuthorLogin: summary.AuthorLogin,
		UpdatedAt:   summary.UpdatedAt,
	}
}

func groupMemberViewFromModel(member database.GroupMember, summary GroupMemberObjectSummary, found bool) GroupMemberView {
	view := GroupMemberView{
		ID:                 member.ID,
		GitHubRepositoryID: member.GitHubRepositoryID,
		ObjectType:         member.ObjectType,
		ObjectNumber:       member.ObjectNumber,
		TargetKey:          member.TargetKey,
		AddedBy:            member.AddedBy,
		AddedAt:            member.AddedAt,
	}
	if found {
		view.ObjectSummary = &summary
	}
	return view
}

func (s *Service) loadFieldDefinitionsTx(tx *gorm.DB, repositoryID int64, scope string) ([]database.FieldDefinition, error) {
	var definitions []database.FieldDefinition
	err := tx.Where("github_repository_id = ? AND archived_at IS NULL AND object_scope IN ?", repositoryID, []string{scope, "all"}).
		Order("sort_order ASC, name ASC").
		Find(&definitions).Error
	return definitions, err
}

func (s *Service) collectTargetsForFieldTx(tx *gorm.DB, fieldDefinitionID uint, out *[]targetRef) error {
	var values []database.FieldValue
	if err := tx.Where("field_definition_id = ?", fieldDefinitionID).Find(&values).Error; err != nil {
		return err
	}
	for _, value := range values {
		ref := targetRef{
			RepositoryID: value.GitHubRepositoryID,
			Owner:        value.RepositoryOwner,
			Name:         value.RepositoryName,
			TargetType:   value.TargetType,
			TargetKey:    value.TargetKey,
			GroupID:      value.GroupID,
			ObjectNumber: intValueOrZero(value.ObjectNumber),
		}
		*out = append(*out, ref)
	}
	return nil
}

func (s *Service) appendEventTx(tx *gorm.DB, input eventInput) error {
	payloadJSON, _ := json.Marshal(input.Payload)
	metadataJSON, _ := json.Marshal(input.Metadata)
	if err := lockEventAggregateTx(tx, input.AggregateType, input.AggregateKey); err != nil {
		return err
	}
	event, err := s.insertEventWithSequenceRetry(tx, input, payloadJSON, metadataJSON)
	if err != nil {
		return err
	}
	if err := insertEventRefs(tx, event.ID, input.Refs); err != nil {
		return err
	}
	if s.dispatcher != nil && input.AggregateType == "group" {
		return s.dispatcher.EnqueueGroupCommentProjectTx(tx, event.ID)
	}
	return nil
}

func (s *Service) insertEventWithSequenceRetry(tx *gorm.DB, input eventInput, payloadJSON, metadataJSON []byte) (database.Event, error) {
	for attempts := 0; attempts < 5; attempts++ {
		nextSequence, err := nextEventSequence(tx, input.AggregateType, input.AggregateKey)
		if err != nil {
			return database.Event{}, err
		}
		event := newEventRecord(input, nextSequence, payloadJSON, metadataJSON)
		inserted, retry, err := insertEventWithSavepoint(tx, attempts, event)
		if err != nil {
			return database.Event{}, err
		}
		if retry {
			continue
		}
		return inserted, nil
	}
	return database.Event{}, &FailError{StatusCode: 409, Message: "event sequence conflict"}
}

func nextEventSequence(tx *gorm.DB, aggregateType, aggregateKey string) (int, error) {
	var nextSequence int
	err := tx.Model(&database.Event{}).
		Select("COALESCE(MAX(sequence_no), 0) + 1").
		Where("aggregate_type = ? AND aggregate_key = ?", aggregateType, aggregateKey).
		Scan(&nextSequence).Error
	return nextSequence, err
}

func newEventRecord(input eventInput, nextSequence int, payloadJSON, metadataJSON []byte) database.Event {
	return database.Event{
		GitHubRepositoryID: input.RepositoryID,
		AggregateType:      input.AggregateType,
		AggregateKey:       input.AggregateKey,
		SequenceNo:         nextSequence,
		EventType:          input.EventType,
		ActorType:          input.Actor.Type,
		ActorID:            input.Actor.ID,
		RequestID:          input.RequestID,
		IdempotencyKey:     input.IdempotencyKey,
		SchemaVersion:      1,
		PayloadJSON:        datatypes.JSON(payloadJSON),
		MetadataJSON:       datatypes.JSON(metadataJSON),
		OccurredAt:         time.Now().UTC(),
	}
}

func insertEventWithSavepoint(tx *gorm.DB, attempts int, event database.Event) (database.Event, bool, error) {
	savepoint := fmt.Sprintf("event_sequence_retry_%d", attempts)
	if err := tx.SavePoint(savepoint).Error; err != nil {
		return database.Event{}, false, err
	}
	if err := tx.Create(&event).Error; err != nil {
		if isEventSequenceConflict(err) {
			if rollbackErr := tx.RollbackTo(savepoint).Error; rollbackErr != nil {
				return database.Event{}, false, rollbackErr
			}
			return database.Event{}, true, nil
		}
		return database.Event{}, false, err
	}
	return event, false, nil
}

func insertEventRefs(tx *gorm.DB, eventID uint, refs []eventRefInput) error {
	for _, ref := range refs {
		if err := tx.Create(&database.EventRef{
			EventID: eventID,
			RefRole: ref.Role,
			RefType: ref.Type,
			RefKey:  ref.Key,
		}).Error; err != nil {
			return err
		}
	}
	return nil
}

type eventInput struct {
	RepositoryID   int64
	AggregateType  string
	AggregateKey   string
	EventType      string
	Actor          permissions.Actor
	RequestID      string
	IdempotencyKey string
	Payload        map[string]any
	Metadata       map[string]any
	Refs           []eventRefInput
}

type eventRefInput struct {
	Role string
	Type string
	Key  string
}

type targetRef struct {
	RepositoryID         int64
	Owner                string
	Name                 string
	TargetType           string
	TargetKey            string
	ObjectNumber         int
	GroupID              *uint
	SourceUpdatedAtValue time.Time
}

func (t targetRef) ApplicableScope() string {
	return t.TargetType
}

func (t targetRef) ObjectNumberPtr() *int {
	if t.TargetType == "group" {
		return nil
	}
	value := t.ObjectNumber
	return &value
}

func (t targetRef) SourceUpdatedAt() time.Time {
	if !t.SourceUpdatedAtValue.IsZero() {
		return t.SourceUpdatedAtValue.UTC()
	}
	return time.Now().UTC()
}

type convertedValue struct {
	StringValue   *string
	TextValue     *string
	BoolValue     *bool
	IntValue      *int64
	EnumValue     *string
	MultiEnumJSON datatypes.JSON
	APIValue      any
}

func validateFieldDefinitionInput(input FieldDefinitionInput) error {
	name := normalizeFieldName(input.Name)
	if name == "" {
		return &FailError{StatusCode: 400, Message: "field name is required"}
	}
	switch input.ObjectScope {
	case "pull_request", "issue", "group", "all":
	default:
		return &FailError{StatusCode: 400, Message: "invalid object_scope"}
	}
	switch input.FieldType {
	case "string", "text", "boolean", "integer", "enum", "multi_enum":
	default:
		return &FailError{StatusCode: 400, Message: "invalid field_type"}
	}
	if (input.FieldType == "enum" || input.FieldType == "multi_enum") && len(normalizeEnumValues(input.EnumValues)) == 0 {
		return &FailError{StatusCode: 400, Message: "enum_values are required"}
	}
	return nil
}

func validateFieldDefinitionPatchInput(input FieldDefinitionPatchInput) error {
	if input.DisplayName == nil &&
		input.EnumValues == nil &&
		input.IsRequired == nil &&
		input.IsFilterable == nil &&
		input.IsSearchable == nil &&
		input.IsVectorized == nil &&
		input.SortOrder == nil {
		return &FailError{StatusCode: 400, Message: "no field updates provided"}
	}
	return nil
}

func validateGroupInput(input GroupInput) error {
	switch input.Kind {
	case "pull_request", "issue", "mixed":
	default:
		return &FailError{StatusCode: 400, Message: "invalid group kind"}
	}
	if strings.TrimSpace(input.Title) == "" {
		return &FailError{StatusCode: 400, Message: "group title is required"}
	}
	return nil
}

func validateMemberType(kind, objectType string) error {
	switch kind {
	case "pull_request":
		if objectType != "pull_request" {
			return &FailError{StatusCode: 400, Message: "pull_request groups can only contain pull_request members"}
		}
	case "issue":
		if objectType != "issue" {
			return &FailError{StatusCode: 400, Message: "issue groups can only contain issue members"}
		}
	case "mixed":
		if objectType != "pull_request" && objectType != "issue" {
			return &FailError{StatusCode: 400, Message: "mixed groups can only contain pull_request or issue members"}
		}
	default:
		return &FailError{StatusCode: 400, Message: "invalid group kind"}
	}
	return nil
}

func validateGroupPatchInput(input GroupPatchInput) error {
	if input.Title == nil && input.Description == nil && input.Status == nil {
		return &FailError{StatusCode: 400, Message: "no group updates provided"}
	}
	return nil
}

func ensureExpectedRowVersion(current int, expected *int) error {
	if expected == nil {
		return nil
	}
	if *expected != current {
		return &FailError{
			StatusCode: 409,
			Message:    "row version conflict",
			Data: map[string]any{
				"expected_row_version": *expected,
				"current_row_version":  current,
			},
		}
	}
	return nil
}

func staleRowVersionError(tx *gorm.DB, model any, id uint, expected int) error {
	current := expected
	row := struct {
		RowVersion int
	}{}
	if err := tx.Model(model).Select("row_version").Where("id = ?", id).Take(&row).Error; err == nil {
		current = row.RowVersion
	}
	return &FailError{
		StatusCode: 409,
		Message:    "row version conflict",
		Data: map[string]any{
			"expected_row_version": expected,
			"current_row_version":  current,
		},
	}
}

func isEventSequenceConflict(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "idx_events_aggregate_sequence") ||
		strings.Contains(text, "events.aggregate_type") ||
		strings.Contains(text, "duplicate key")
}

func (s *Service) ensureEnumValuesCompatible(ctx context.Context, definition database.FieldDefinition, allowed []string) error {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, value := range allowed {
		allowedSet[value] = struct{}{}
	}

	var values []database.FieldValue
	if err := s.db.WithContext(ctx).Where("field_definition_id = ?", definition.ID).Find(&values).Error; err != nil {
		return err
	}
	for _, value := range values {
		if err := validateEnumCompatibility(definition.FieldType, allowedSet, value); err != nil {
			return err
		}
	}
	return nil
}

func convertAnnotationValue(definition database.FieldDefinition, raw any) (convertedValue, bool, error) {
	if raw == nil {
		return convertedValue{}, true, nil
	}

	base := convertedValue{MultiEnumJSON: datatypes.JSON([]byte("[]"))}
	switch definition.FieldType {
	case "string":
		return convertStringAnnotation(base, raw)
	case "text":
		return convertTextAnnotation(base, raw)
	case "boolean":
		return convertBoolAnnotation(base, raw)
	case "integer":
		return convertIntegerAnnotation(base, raw)
	case "enum":
		return convertEnumAnnotation(base, definition.EnumValuesJSON, raw)
	case "multi_enum":
		return convertMultiEnumAnnotation(base, definition.EnumValuesJSON, raw)
	default:
		return convertedValue{}, false, &FailError{StatusCode: 400, Message: "unsupported field type"}
	}
}

func validateEnumCompatibility(fieldType string, allowedSet map[string]struct{}, value database.FieldValue) error {
	switch fieldType {
	case "enum":
		return validateEnumValueCompatibility(allowedSet, value.EnumValue)
	case "multi_enum":
		return validateMultiEnumCompatibility(allowedSet, value.MultiEnumJSON)
	default:
		return nil
	}
}

func validateEnumValueCompatibility(allowedSet map[string]struct{}, enumValue *string) error {
	if enumValue == nil {
		return nil
	}
	if _, ok := allowedSet[*enumValue]; ok {
		return nil
	}
	return &FailError{StatusCode: 409, Message: "enum_values would invalidate existing annotations"}
}

func validateMultiEnumCompatibility(allowedSet map[string]struct{}, raw datatypes.JSON) error {
	var existing []string
	if err := json.Unmarshal(raw, &existing); err != nil {
		return err
	}
	for _, candidate := range existing {
		if _, ok := allowedSet[candidate]; !ok {
			return &FailError{StatusCode: 409, Message: "enum_values would invalidate existing annotations"}
		}
	}
	return nil
}

func convertStringAnnotation(base convertedValue, raw any) (convertedValue, bool, error) {
	value := strings.TrimSpace(fmt.Sprint(raw))
	base.StringValue = &value
	base.APIValue = value
	return base, false, nil
}

func convertTextAnnotation(base convertedValue, raw any) (convertedValue, bool, error) {
	value := strings.TrimSpace(fmt.Sprint(raw))
	base.TextValue = &value
	base.APIValue = value
	return base, false, nil
}

func convertBoolAnnotation(base convertedValue, raw any) (convertedValue, bool, error) {
	boolValue, ok := raw.(bool)
	if !ok {
		return convertedValue{}, false, &FailError{StatusCode: 400, Message: "expected boolean value"}
	}
	base.BoolValue = &boolValue
	base.APIValue = boolValue
	return base, false, nil
}

func convertIntegerAnnotation(base convertedValue, raw any) (convertedValue, bool, error) {
	value, err := annotationIntegerValue(raw)
	if err != nil {
		return convertedValue{}, false, err
	}
	base.IntValue = &value
	base.APIValue = value
	return base, false, nil
}

func annotationIntegerValue(raw any) (int64, error) {
	switch typed := raw.(type) {
	case float64:
		if math.Trunc(typed) != typed {
			return 0, &FailError{StatusCode: 400, Message: "expected integer value"}
		}
		return int64(typed), nil
	case int:
		return int64(typed), nil
	case int64:
		return typed, nil
	default:
		return 0, &FailError{StatusCode: 400, Message: "expected integer value"}
	}
}

func convertEnumAnnotation(base convertedValue, allowed datatypes.JSON, raw any) (convertedValue, bool, error) {
	value := strings.TrimSpace(fmt.Sprint(raw))
	if !enumAllowed(allowed, value) {
		return convertedValue{}, false, &FailError{StatusCode: 400, Message: "invalid enum value"}
	}
	base.EnumValue = &value
	base.APIValue = value
	return base, false, nil
}

func convertMultiEnumAnnotation(base convertedValue, allowed datatypes.JSON, raw any) (convertedValue, bool, error) {
	values, err := annotationMultiEnumValues(allowed, raw)
	if err != nil {
		return convertedValue{}, false, err
	}
	bytes, _ := json.Marshal(values)
	base.MultiEnumJSON = datatypes.JSON(bytes)
	base.APIValue = values
	return base, false, nil
}

func annotationMultiEnumValues(allowed datatypes.JSON, raw any) ([]string, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, &FailError{StatusCode: 400, Message: "expected array value"}
	}
	values := make([]string, 0, len(items))
	for _, item := range items {
		value := strings.TrimSpace(fmt.Sprint(item))
		if !enumAllowed(allowed, value) {
			return nil, &FailError{StatusCode: 400, Message: "invalid multi_enum value"}
		}
		values = append(values, value)
	}
	sort.Strings(values)
	return values, nil
}

func translateDBError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

func normalizeFieldName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, " ", "_")
	return name
}

func displayName(input FieldDefinitionInput) string {
	if value := strings.TrimSpace(input.DisplayName); value != "" {
		return value
	}
	return normalizeFieldName(input.Name)
}

func normalizeEnumValues(values []string) []string {
	set := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := set[value]; ok {
			continue
		}
		set[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func fieldToInput(field database.FieldDefinition) FieldDefinitionInput {
	var enumValues []string
	_ = json.Unmarshal(field.EnumValuesJSON, &enumValues)
	return FieldDefinitionInput{
		Name:         field.Name,
		DisplayName:  field.DisplayName,
		ObjectScope:  field.ObjectScope,
		FieldType:    field.FieldType,
		EnumValues:   enumValues,
		IsRequired:   field.IsRequired,
		IsFilterable: field.IsFilterable,
		IsSearchable: field.IsSearchable,
		IsVectorized: field.IsVectorized,
		SortOrder:    field.SortOrder,
	}
}

func enumAllowed(raw datatypes.JSON, value string) bool {
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return false
	}
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func multiEnumContains(raw datatypes.JSON, value string) (bool, error) {
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return false, err
	}
	for _, candidate := range values {
		if candidate == value {
			return true, nil
		}
	}
	return false, nil
}

func fieldValueToAPI(value database.FieldValue) any {
	switch {
	case value.StringValue != nil:
		return *value.StringValue
	case value.TextValue != nil:
		return *value.TextValue
	case value.BoolValue != nil:
		return *value.BoolValue
	case value.IntValue != nil:
		return *value.IntValue
	case value.EnumValue != nil:
		return *value.EnumValue
	default:
		var out []string
		if err := json.Unmarshal(value.MultiEnumJSON, &out); err == nil && len(out) > 0 {
			return out
		}
		return nil
	}
}

func objectTargetKey(repositoryID int64, targetType string, objectNumber int) string {
	return fmt.Sprintf("repo:%d:%s:%d", repositoryID, targetType, objectNumber)
}

func groupTargetKey(groupPublicID string) string {
	return "group:" + groupPublicID
}

func fieldAggregateKey(repositoryID int64, name, scope string) string {
	return fmt.Sprintf("repo:%d:field:%s:%s", repositoryID, scope, name)
}

func defaultStatus(status string) string {
	value := strings.TrimSpace(status)
	if value == "" {
		return "open"
	}
	return value
}

func intValueOrZero(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func (s *Service) lookupGroupByID(ctx context.Context, groupID uint) (database.Group, error) {
	timer := database.StartQueryStep(ctx, "group_lookup")
	defer timer.Done()

	var group database.Group
	err := s.db.WithContext(ctx).First(&group, groupID).Error
	return group, err
}

func (s *Service) lookupGroupByPublicID(ctx context.Context, groupPublicID string) (database.Group, error) {
	timer := database.StartQueryStep(ctx, "group_lookup")
	defer timer.Done()

	var group database.Group
	err := s.db.WithContext(ctx).
		Where("public_id = ?", strings.TrimSpace(groupPublicID)).
		First(&group).Error
	return group, err
}

func isGroupPublicIDConflict(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "idx_groups_public_id") ||
		strings.Contains(text, "groups.public_id") ||
		strings.Contains(text, "duplicate key")
}

func isGroupMemberConflict(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "idx_group_members_unique") ||
		strings.Contains(text, "idx_group_members_unique_target") ||
		strings.Contains(text, "group_members_group_id_object_type_object_number_key") ||
		strings.Contains(text, "group_members_github_repository_id_object_type_object_number_key") ||
		strings.Contains(text, "unique constraint failed: group_members.group_id, group_members.object_type, group_members.object_number") ||
		strings.Contains(text, "unique constraint failed: group_members.github_repository_id, group_members.object_type, group_members.object_number") ||
		strings.Contains(text, "duplicate key")
}

func (s *Service) translateGroupMemberConflictTx(tx *gorm.DB, group database.Group, member database.GroupMember) error {
	existing, owner, found, err := lookupGroupMemberConflictTx(tx, member.GitHubRepositoryID, member.ObjectType, member.ObjectNumber)
	if err != nil {
		return err
	}
	if !found {
		return &FailError{StatusCode: 409, Message: "group member already exists"}
	}
	if existing.GroupID == group.ID {
		return &FailError{StatusCode: 409, Message: "group member already exists"}
	}
	return &FailError{
		StatusCode: 409,
		Message:    "target already belongs to another group",
		Data: groupMemberConflictDetails{
			GroupPublicID: owner.PublicID,
		},
	}
}

func lookupGroupMemberConflictTx(tx *gorm.DB, repositoryID int64, objectType string, objectNumber int) (database.GroupMember, database.Group, bool, error) {
	var member database.GroupMember
	err := tx.
		Where("github_repository_id = ? AND object_type = ? AND object_number = ?", repositoryID, objectType, objectNumber).
		First(&member).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return database.GroupMember{}, database.Group{}, false, nil
	}
	if err != nil {
		return database.GroupMember{}, database.Group{}, false, err
	}

	var group database.Group
	err = tx.Where("id = ?", member.GroupID).First(&group).Error
	if err != nil {
		return database.GroupMember{}, database.Group{}, false, err
	}
	return member, group, true, nil
}

func lockEventAggregateTx(tx *gorm.DB, aggregateType string, aggregateKey string) error {
	if tx == nil {
		return nil
	}
	dialector := tx.Dialector
	if dialector == nil {
		return nil
	}
	if dialector.Name() != "postgres" {
		return nil
	}
	return tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?), hashtext(?))", aggregateType, aggregateKey).Error
}
