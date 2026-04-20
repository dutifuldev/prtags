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

const (
	indexJobKindTargetProjectionRefresh = "target_projection_refresh"
	targetProjectionFreshnessTTL        = 15 * time.Minute
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
	db         *gorm.DB
	ghreplica  *ghreplica.Client
	permission permissions.Checker
	indexer    *Indexer
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
	TargetType   string                     `json:"target_type"`
	ObjectNumber int                        `json:"object_number,omitempty"`
	ID           string                     `json:"id,omitempty"`
	TargetKey    string                     `json:"target_key"`
	Projection   *database.TargetProjection `json:"projection,omitempty"`
	Annotations  map[string]any             `json:"annotations"`
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

type GroupMemberObjectFreshness struct {
	State     string     `json:"state"`
	Source    string     `json:"source"`
	FetchedAt *time.Time `json:"fetched_at,omitempty"`
}

type GroupMemberView struct {
	ID                 uint                        `json:"id"`
	GitHubRepositoryID int64                       `json:"github_repository_id"`
	ObjectType         string                      `json:"object_type"`
	ObjectNumber       int                         `json:"object_number"`
	TargetKey          string                      `json:"target_key"`
	AddedBy            string                      `json:"added_by"`
	AddedAt            time.Time                   `json:"added_at"`
	ObjectSummary      *GroupMemberObjectSummary   `json:"object_summary,omitempty"`
	ObjectFreshness    *GroupMemberObjectFreshness `json:"object_summary_freshness,omitempty"`
}

type GetGroupOptions struct {
	IncludeMetadata bool
}

func NewService(db *gorm.DB, gh *ghreplica.Client, checker permissions.Checker, indexer *Indexer) *Service {
	return &Service{
		db:         db,
		ghreplica:  gh,
		permission: checker,
		indexer:    indexer,
	}
}

func (s *Service) EnsureRepository(ctx context.Context, owner, repo string) (database.RepositoryProjection, error) {
	repository, err := s.ghreplica.GetRepository(ctx, owner, repo)
	if err != nil {
		return database.RepositoryProjection{}, err
	}

	model := database.RepositoryProjection{
		GitHubRepositoryID: repository.ID,
		Owner:              repository.Owner.Login,
		Name:               repository.Name,
		FullName:           repository.FullName,
		HTMLURL:            repository.HTMLURL,
		Visibility:         repository.Visibility,
		Private:            repository.Private,
		FetchedAt:          time.Now().UTC(),
	}

	if err := s.db.WithContext(ctx).Where("github_repository_id = ?", repository.ID).Assign(model).FirstOrCreate(&model).Error; err != nil {
		return database.RepositoryProjection{}, err
	}
	return model, nil
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
	repository, err := s.EnsureRepository(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	var fields []database.FieldDefinition
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

	updates := map[string]any{
		"updated_by":  actor.ID,
		"updated_at":  time.Now().UTC(),
		"row_version": gorm.Expr("row_version + 1"),
	}
	if input.DisplayName != nil {
		value := strings.TrimSpace(*input.DisplayName)
		if value == "" {
			return database.FieldDefinition{}, &FailError{StatusCode: 400, Message: "display_name cannot be blank"}
		}
		updates["display_name"] = value
	}
	if input.IsRequired != nil {
		updates["is_required"] = *input.IsRequired
	}
	if input.IsFilterable != nil {
		updates["is_filterable"] = *input.IsFilterable
	}
	if input.IsSearchable != nil {
		updates["is_searchable"] = *input.IsSearchable
	}
	if input.IsVectorized != nil {
		updates["is_vectorized"] = *input.IsVectorized
	}
	if input.SortOrder != nil {
		updates["sort_order"] = *input.SortOrder
	}
	if input.EnumValues != nil {
		if field.FieldType != "enum" && field.FieldType != "multi_enum" {
			return database.FieldDefinition{}, &FailError{StatusCode: 400, Message: "enum_values can only be updated for enum fields"}
		}
		enumValues := normalizeEnumValues(*input.EnumValues)
		if len(enumValues) == 0 {
			return database.FieldDefinition{}, &FailError{StatusCode: 400, Message: "enum_values are required"}
		}
		if err := s.ensureEnumValuesCompatible(ctx, field, enumValues); err != nil {
			return database.FieldDefinition{}, err
		}
		raw, _ := json.Marshal(enumValues)
		updates["enum_values_json"] = datatypes.JSON(raw)
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Model(&database.FieldDefinition{}).Where("id = ?", field.ID)
		if input.ExpectedRowVersion != nil {
			query = query.Where("row_version = ?", *input.ExpectedRowVersion)
		}
		result := query.Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if input.ExpectedRowVersion != nil && result.RowsAffected == 0 {
			return staleRowVersionError(tx, &database.FieldDefinition{}, field.ID, *input.ExpectedRowVersion)
		}
		if err := tx.First(&field, field.ID).Error; err != nil {
			return err
		}
		if err := s.appendEventTx(tx, eventInput{
			RepositoryID:   repository.GitHubRepositoryID,
			AggregateType:  "field_definition",
			AggregateKey:   fieldAggregateKey(repository.GitHubRepositoryID, field.Name, field.ObjectScope),
			EventType:      "field_definition.updated",
			Actor:          actor,
			IdempotencyKey: idempotencyKey,
			Payload: map[string]any{
				"field_definition_id": field.ID,
				"name":                field.Name,
				"object_scope":        field.ObjectScope,
			},
		}); err != nil {
			return err
		}

		var targets []targetRef
		if err := s.collectTargetsForFieldTx(tx, field.ID, &targets); err != nil {
			return err
		}
		for _, target := range targets {
			if err := s.enqueueRebuildsTx(tx, repository, target, time.Now().UTC()); err != nil {
				return err
			}
		}
		return nil
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
		query := tx.Model(&database.FieldDefinition{}).Where("id = ?", field.ID)
		if expectedRowVersion != nil {
			query = query.Where("row_version = ?", *expectedRowVersion)
		}
		result := query.Updates(map[string]any{
			"archived_at": now,
			"updated_by":  actor.ID,
			"updated_at":  now,
			"row_version": gorm.Expr("row_version + 1"),
		})
		if result.Error != nil {
			return result.Error
		}
		if expectedRowVersion != nil && result.RowsAffected == 0 {
			return staleRowVersionError(tx, &database.FieldDefinition{}, field.ID, *expectedRowVersion)
		}
		if err := tx.First(&field, field.ID).Error; err != nil {
			return err
		}
		if err := s.appendEventTx(tx, eventInput{
			RepositoryID:   repository.GitHubRepositoryID,
			AggregateType:  "field_definition",
			AggregateKey:   fieldAggregateKey(repository.GitHubRepositoryID, field.Name, field.ObjectScope),
			EventType:      "field_definition.archived",
			Actor:          actor,
			IdempotencyKey: idempotencyKey,
			Payload: map[string]any{
				"field_definition_id": field.ID,
				"name":                field.Name,
				"object_scope":        field.ObjectScope,
			},
		}); err != nil {
			return err
		}

		var targets []targetRef
		if err := s.collectTargetsForFieldTx(tx, field.ID, &targets); err != nil {
			return err
		}
		for _, target := range targets {
			if err := s.enqueueRebuildsTx(tx, repository, target, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return database.FieldDefinition{}, translateDBError(err)
	}
	return field, nil
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
			name := normalizeFieldName(input.Name)
			var existing database.FieldDefinition
			err := tx.Where("github_repository_id = ? AND name = ? AND object_scope = ?", repository.GitHubRepositoryID, name, input.ObjectScope).First(&existing).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}

			enumValues, _ := json.Marshal(normalizeEnumValues(input.EnumValues))
			if errors.Is(err, gorm.ErrRecordNotFound) {
				model := database.FieldDefinition{
					GitHubRepositoryID: repository.GitHubRepositoryID,
					RepositoryOwner:    repository.Owner,
					RepositoryName:     repository.Name,
					Name:               name,
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
				if err := tx.Create(&model).Error; err != nil {
					return err
				}
				out = append(out, model)
				if err := s.appendEventTx(tx, eventInput{
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
				}); err != nil {
					return err
				}
				continue
			}

			if existing.FieldType != input.FieldType {
				return &FailError{
					StatusCode: 409,
					Message:    "field_type cannot change for an existing field",
					Data: map[string]any{
						"field":              existing.Name,
						"object_scope":       existing.ObjectScope,
						"current_field_type": existing.FieldType,
						"requested_type":     input.FieldType,
					},
				}
			}

			if existing.FieldType == "enum" || existing.FieldType == "multi_enum" {
				if err := s.ensureEnumValuesCompatible(ctx, existing, normalizeEnumValues(input.EnumValues)); err != nil {
					return err
				}
			}

			updates := map[string]any{
				"display_name":     displayName(input),
				"enum_values_json": datatypes.JSON(enumValues),
				"is_required":      input.IsRequired,
				"is_filterable":    input.IsFilterable,
				"is_searchable":    input.IsSearchable,
				"is_vectorized":    input.IsVectorized,
				"sort_order":       input.SortOrder,
				"updated_by":       actor.ID,
				"row_version":      gorm.Expr("row_version + 1"),
			}
			if err := tx.Model(&database.FieldDefinition{}).Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
				return err
			}
			if err := tx.First(&existing, existing.ID).Error; err != nil {
				return err
			}
			out = append(out, existing)
			if err := s.appendEventTx(tx, eventInput{
				RepositoryID:   repository.GitHubRepositoryID,
				AggregateType:  "field_definition",
				AggregateKey:   fieldAggregateKey(repository.GitHubRepositoryID, existing.Name, existing.ObjectScope),
				EventType:      "field_definition.updated",
				Actor:          actor,
				IdempotencyKey: idempotencyKey,
				Payload: map[string]any{
					"field_definition_id": existing.ID,
					"name":                existing.Name,
					"object_scope":        existing.ObjectScope,
				},
			}); err != nil {
				return err
			}

			var targets []targetRef
			if err := s.collectTargetsForFieldTx(tx, existing.ID, &targets); err != nil {
				return err
			}
			for _, target := range targets {
				if err := s.enqueueRebuildsTx(tx, repository, target, time.Now().UTC()); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return out, err
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
			if err := tx.Create(&group).Error; err != nil {
				return err
			}
			if err := s.appendEventTx(tx, eventInput{
				RepositoryID:   repository.GitHubRepositoryID,
				AggregateType:  "group",
				AggregateKey:   groupTargetKey(group.PublicID),
				EventType:      "group.created",
				Actor:          actor,
				IdempotencyKey: idempotencyKey,
				Payload: map[string]any{
					"group_id":        group.ID,
					"group_public_id": group.PublicID,
					"title":           group.Title,
					"kind":            group.Kind,
				},
			}); err != nil {
				return err
			}
			return s.enqueueRebuildsTx(tx, repository, targetRef{
				RepositoryID: repository.GitHubRepositoryID,
				Owner:        repository.Owner,
				Name:         repository.Name,
				TargetType:   "group",
				TargetKey:    groupTargetKey(group.PublicID),
				GroupID:      &group.ID,
			}, time.Now().UTC())
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

	updates := map[string]any{
		"updated_by":  actor.ID,
		"updated_at":  time.Now().UTC(),
		"row_version": gorm.Expr("row_version + 1"),
	}
	if input.Title != nil {
		value := strings.TrimSpace(*input.Title)
		if value == "" {
			return database.Group{}, &FailError{StatusCode: 400, Message: "group title is required"}
		}
		updates["title"] = value
	}
	if input.Description != nil {
		updates["description"] = strings.TrimSpace(*input.Description)
	}
	if input.Status != nil {
		value := strings.TrimSpace(*input.Status)
		if value == "" {
			return database.Group{}, &FailError{StatusCode: 400, Message: "group status is required"}
		}
		updates["status"] = value
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Model(&database.Group{}).Where("id = ?", group.ID)
		if input.ExpectedRowVersion != nil {
			query = query.Where("row_version = ?", *input.ExpectedRowVersion)
		}
		result := query.Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if input.ExpectedRowVersion != nil && result.RowsAffected == 0 {
			return staleRowVersionError(tx, &database.Group{}, group.ID, *input.ExpectedRowVersion)
		}
		if err := tx.First(&group, group.ID).Error; err != nil {
			return err
		}
		if err := s.appendEventTx(tx, eventInput{
			RepositoryID:   group.GitHubRepositoryID,
			AggregateType:  "group",
			AggregateKey:   groupTargetKey(group.PublicID),
			EventType:      "group.updated",
			Actor:          actor,
			IdempotencyKey: idempotencyKey,
			Payload: map[string]any{
				"group_id":        group.ID,
				"group_public_id": group.PublicID,
				"title":           group.Title,
				"status":          group.Status,
			},
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
	if err != nil {
		return database.Group{}, translateDBError(err)
	}
	return group, nil
}

func (s *Service) ListGroups(ctx context.Context, owner, repo string) ([]GroupListView, error) {
	repository, err := s.EnsureRepository(ctx, owner, repo)
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
	if err := s.db.WithContext(ctx).Where("group_id = ?", group.ID).Order("id ASC").Find(&members).Error; err != nil {
		return database.Group{}, nil, nil, err
	}

	memberViews := make([]GroupMemberView, 0, len(members))
	if options.IncludeMetadata {
		memberViews, err = s.enrichGroupMembers(ctx, group, members)
		if err != nil {
			return database.Group{}, nil, nil, err
		}
	} else {
		for _, member := range members {
			memberViews = append(memberViews, groupMemberViewFromModel(member, groupMemberResolution{}))
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
	member := database.GroupMember{
		GroupID:            group.ID,
		GitHubRepositoryID: group.GitHubRepositoryID,
		ObjectType:         objectType,
		ObjectNumber:       objectNumber,
		TargetKey:          objectTargetKey(group.GitHubRepositoryID, objectType, objectNumber),
		AddedBy:            actor.ID,
		AddedAt:            time.Now().UTC(),
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&member).Error; err != nil {
			if isGroupMemberConflict(err) {
				return &FailError{StatusCode: 409, Message: "group member already exists"}
			}
			return err
		}
		if err := s.appendEventTx(tx, eventInput{
			RepositoryID:   group.GitHubRepositoryID,
			AggregateType:  "group",
			AggregateKey:   groupTargetKey(group.PublicID),
			EventType:      "group.member_added",
			Actor:          actor,
			IdempotencyKey: idempotencyKey,
			Payload: map[string]any{
				"group_id":        group.ID,
				"group_public_id": group.PublicID,
				"object_type":     objectType,
				"object_number":   objectNumber,
			},
			Refs: []eventRefInput{{Role: "member", Type: objectType, Key: member.TargetKey}},
		}); err != nil {
			return err
		}
		if err := s.enqueueRebuildsTx(tx, repository, targetRef{
			RepositoryID: group.GitHubRepositoryID,
			Owner:        group.RepositoryOwner,
			Name:         group.RepositoryName,
			TargetType:   "group",
			TargetKey:    groupTargetKey(group.PublicID),
			GroupID:      &group.ID,
		}, time.Now().UTC()); err != nil {
			return err
		}
		if err := s.enqueueTargetProjectionRefreshJobsTx(tx, group, []targetRef{{
			RepositoryID: group.GitHubRepositoryID,
			Owner:        group.RepositoryOwner,
			Name:         group.RepositoryName,
			TargetType:   objectType,
			TargetKey:    member.TargetKey,
			ObjectNumber: objectNumber,
		}}); err != nil {
			return err
		}
		if err := s.enqueueRebuildsTx(tx, repository, targetRef{
			RepositoryID: group.GitHubRepositoryID,
			Owner:        group.RepositoryOwner,
			Name:         group.RepositoryName,
			TargetType:   objectType,
			TargetKey:    member.TargetKey,
			ObjectNumber: objectNumber,
		}, time.Now().UTC()); err != nil {
			return err
		}
		return nil
	})
	return member, translateDBError(err)
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
		byName := map[string]database.FieldDefinition{}
		for _, definition := range definitions {
			byName[definition.Name] = definition
		}

		for rawField, rawValue := range values {
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
				if err := tx.Where("field_definition_id = ? AND target_type = ? AND target_key = ?", definition.ID, target.TargetType, target.TargetKey).
					Delete(&database.FieldValue{}).Error; err != nil {
					return err
				}
				result.Annotations[fieldName] = nil
				if err := s.appendEventTx(tx, eventInput{
					RepositoryID:   repository.GitHubRepositoryID,
					AggregateType:  target.TargetType,
					AggregateKey:   target.TargetKey,
					EventType:      "field_value.cleared",
					Actor:          actor,
					IdempotencyKey: idempotencyKey,
					Payload: map[string]any{
						"field_definition_id": definition.ID,
						"field_name":          definition.Name,
					},
					Refs: []eventRefInput{{Role: "field_definition", Type: "field_definition", Key: fieldAggregateKey(repository.GitHubRepositoryID, definition.Name, definition.ObjectScope)}},
				}); err != nil {
					return err
				}
				continue
			}

			model := database.FieldValue{
				FieldDefinitionID:  definition.ID,
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

			var existing database.FieldValue
			err = tx.Where("field_definition_id = ? AND target_type = ? AND target_key = ?", definition.ID, target.TargetType, target.TargetKey).First(&existing).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if err := tx.Create(&model).Error; err != nil {
					return err
				}
			} else {
				updates := map[string]any{
					"updated_by":      actor.ID,
					"string_value":    converted.StringValue,
					"text_value":      converted.TextValue,
					"bool_value":      converted.BoolValue,
					"int_value":       converted.IntValue,
					"enum_value":      converted.EnumValue,
					"multi_enum_json": converted.MultiEnumJSON,
					"updated_at":      time.Now().UTC(),
				}
				if err := tx.Model(&database.FieldValue{}).Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
					return err
				}
			}

			result.Annotations[fieldName] = converted.APIValue
			if err := s.appendEventTx(tx, eventInput{
				RepositoryID:   repository.GitHubRepositoryID,
				AggregateType:  target.TargetType,
				AggregateKey:   target.TargetKey,
				EventType:      "field_value.set",
				Actor:          actor,
				IdempotencyKey: idempotencyKey,
				Payload: map[string]any{
					"field_definition_id": definition.ID,
					"field_name":          definition.Name,
					"value":               converted.APIValue,
				},
				Refs: []eventRefInput{{Role: "field_definition", Type: "field_definition", Key: fieldAggregateKey(repository.GitHubRepositoryID, definition.Name, definition.ObjectScope)}},
			}); err != nil {
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

func (s *Service) GetAnnotations(ctx context.Context, owner, repo, targetType string, objectNumber int, groupID *uint) (AnnotationSetResult, error) {
	repository, err := s.EnsureRepository(ctx, owner, repo)
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
	repository, err := s.EnsureRepository(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	fieldName = normalizeFieldName(fieldName)

	var definition database.FieldDefinition
	err = s.db.WithContext(ctx).
		Where("github_repository_id = ? AND name = ? AND archived_at IS NULL AND is_filterable = ? AND object_scope = ?", repository.GitHubRepositoryID, fieldName, true, targetType).
		First(&definition).Error
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		if err := s.db.WithContext(ctx).
			Where("github_repository_id = ? AND name = ? AND archived_at IS NULL AND is_filterable = ? AND object_scope = ?", repository.GitHubRepositoryID, fieldName, true, "all").
			First(&definition).Error; err != nil {
			return nil, translateDBError(err)
		}
	}

	query := s.db.WithContext(ctx).Model(&database.FieldValue{}).Where("field_definition_id = ? AND target_type = ?", definition.ID, targetType)
	filterValue := strings.TrimSpace(rawValue)

	switch definition.FieldType {
	case "boolean":
		value := strings.EqualFold(filterValue, "true")
		query = query.Where("bool_value = ?", value)
	case "integer":
		parsed, err := strconv.ParseInt(filterValue, 10, 64)
		if err != nil {
			return nil, &FailError{StatusCode: 400, Message: "invalid integer filter"}
		}
		query = query.Where("int_value = ?", parsed)
	case "enum":
		query = query.Where("enum_value = ?", filterValue)
	case "multi_enum":
		// JSON containment differs across SQLite test runs and Postgres production,
		// so keep the initial query broad and filter the decoded values below.
	default:
		query = query.Where("COALESCE(string_value, text_value, enum_value, '') = ?", filterValue)
	}

	var values []database.FieldValue
	if err := query.Order("target_key ASC").Find(&values).Error; err != nil {
		return nil, err
	}

	results := make([]TargetFilterResult, 0, len(values))
	for _, value := range values {
		if definition.FieldType == "multi_enum" {
			matches, err := multiEnumContains(value.MultiEnumJSON, filterValue)
			if err != nil {
				return nil, err
			}
			if !matches {
				continue
			}
		}

		annotations, err := s.getAnnotationsForTarget(ctx, value.TargetType, repository.GitHubRepositoryID, intValueOrZero(value.ObjectNumber), value.GroupID)
		if err != nil {
			return nil, err
		}

		var projection *database.TargetProjection
		if value.TargetType != "group" && value.ObjectNumber != nil {
			var stored database.TargetProjection
			err := s.db.WithContext(ctx).Where("github_repository_id = ? AND target_type = ? AND object_number = ?", repository.GitHubRepositoryID, value.TargetType, *value.ObjectNumber).First(&stored).Error
			if err == nil {
				projection = &stored
			}
		}

		results = append(results, TargetFilterResult{
			TargetType:   value.TargetType,
			ObjectNumber: intValueOrZero(value.ObjectNumber),
			ID:           "",
			TargetKey:    value.TargetKey,
			Projection:   projection,
			Annotations:  annotations,
		})
		if value.TargetType == "group" && value.GroupID != nil {
			group, err := s.lookupGroupByID(ctx, *value.GroupID)
			if err != nil {
				return nil, translateDBError(err)
			}
			results[len(results)-1].ID = group.PublicID
		}
	}
	return results, nil
}

func (s *Service) getAnnotationsForTarget(ctx context.Context, targetType string, repositoryID int64, objectNumber int, groupID *uint) (map[string]any, error) {
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

func (s *Service) resolveTarget(ctx context.Context, repository database.RepositoryProjection, targetType string, objectNumber int, groupID *uint) (targetRef, error) {
	switch targetType {
	case "pull_request", "issue":
		projection, err := s.ensureTargetProjection(ctx, repository.Owner, repository.Name, repository.GitHubRepositoryID, targetType, objectNumber)
		if err != nil {
			return targetRef{}, err
		}
		return targetRef{
			RepositoryID: repository.GitHubRepositoryID,
			Owner:        repository.Owner,
			Name:         repository.Name,
			TargetType:   targetType,
			TargetKey:    objectTargetKey(repository.GitHubRepositoryID, targetType, objectNumber),
			ObjectNumber: objectNumber,
			Projection:   &projection,
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

func (s *Service) ensureTargetProjection(ctx context.Context, owner, repo string, repositoryID int64, targetType string, number int) (database.TargetProjection, error) {
	now := time.Now().UTC()
	var model database.TargetProjection
	switch targetType {
	case "pull_request":
		pull, err := s.ghreplica.GetPullRequest(ctx, owner, repo, number)
		if err != nil {
			return database.TargetProjection{}, err
		}
		model = database.TargetProjection{
			GitHubRepositoryID: repositoryID,
			RepositoryOwner:    owner,
			RepositoryName:     repo,
			TargetType:         targetType,
			ObjectNumber:       number,
			Title:              pull.Title,
			State:              pull.State,
			AuthorLogin:        pull.User.Login,
			HTMLURL:            pull.HTMLURL,
			SourceUpdatedAt:    pull.UpdatedAt.UTC(),
			FetchedAt:          now,
		}
	case "issue":
		issue, err := s.ghreplica.GetIssue(ctx, owner, repo, number)
		if err != nil {
			return database.TargetProjection{}, err
		}
		model = database.TargetProjection{
			GitHubRepositoryID: repositoryID,
			RepositoryOwner:    owner,
			RepositoryName:     repo,
			TargetType:         targetType,
			ObjectNumber:       number,
			Title:              issue.Title,
			State:              issue.State,
			AuthorLogin:        issue.User.Login,
			HTMLURL:            issue.HTMLURL,
			SourceUpdatedAt:    issue.UpdatedAt.UTC(),
			FetchedAt:          now,
		}
	default:
		return database.TargetProjection{}, &FailError{StatusCode: 400, Message: "unsupported target_type"}
	}
	if err := s.db.WithContext(ctx).
		Where("github_repository_id = ? AND target_type = ? AND object_number = ?", repositoryID, targetType, number).
		Assign(model).
		FirstOrCreate(&model).Error; err != nil {
		return database.TargetProjection{}, err
	}
	return model, nil
}

func (s *Service) enrichGroupMembers(ctx context.Context, group database.Group, members []database.GroupMember) ([]GroupMemberView, error) {
	cachedProjections, err := s.loadCachedGroupMemberProjections(ctx, group.GitHubRepositoryID, members)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	views := make([]GroupMemberView, 0, len(members))
	refreshTargets := make([]targetRef, 0, len(members))
	for _, member := range members {
		var resolution groupMemberResolution
		if projection, ok := cachedProjections[member.TargetKey]; ok {
			resolution = projectionToGroupMemberResolution(now, projection)
			if targetProjectionStale(now, projection) {
				refreshTargets = append(refreshTargets, groupMemberTargetRef(group, member, &projection))
			}
		} else {
			resolution = missingGroupMemberResolution()
			refreshTargets = append(refreshTargets, groupMemberTargetRef(group, member, nil))
		}
		views = append(views, groupMemberViewFromModel(member, resolution))
	}
	if len(refreshTargets) > 0 {
		_ = s.enqueueTargetProjectionRefreshJobs(ctx, group, refreshTargets)
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

type groupMemberResolution struct {
	Summary   *GroupMemberObjectSummary
	Freshness *GroupMemberObjectFreshness
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

func (s *Service) loadCachedGroupMemberProjections(ctx context.Context, repositoryID int64, members []database.GroupMember) (map[string]database.TargetProjection, error) {
	if len(members) == 0 {
		return map[string]database.TargetProjection{}, nil
	}

	targetTypes := make([]string, 0, len(members))
	objectNumbers := make([]int, 0, len(members))
	seenTypes := map[string]struct{}{}
	seenNumbers := map[int]struct{}{}
	for _, member := range members {
		if member.ObjectType != "pull_request" && member.ObjectType != "issue" {
			continue
		}
		if _, ok := seenTypes[member.ObjectType]; !ok {
			seenTypes[member.ObjectType] = struct{}{}
			targetTypes = append(targetTypes, member.ObjectType)
		}
		if _, ok := seenNumbers[member.ObjectNumber]; !ok {
			seenNumbers[member.ObjectNumber] = struct{}{}
			objectNumbers = append(objectNumbers, member.ObjectNumber)
		}
	}
	if len(targetTypes) == 0 || len(objectNumbers) == 0 {
		return map[string]database.TargetProjection{}, nil
	}

	var projections []database.TargetProjection
	if err := s.db.WithContext(ctx).
		Where("github_repository_id = ? AND target_type IN ? AND object_number IN ?", repositoryID, targetTypes, objectNumbers).
		Find(&projections).Error; err != nil {
		return nil, err
	}

	summaries := make(map[string]database.TargetProjection, len(projections))
	for _, projection := range projections {
		summaries[objectTargetKey(repositoryID, projection.TargetType, projection.ObjectNumber)] = projection
	}
	return summaries, nil
}

func (s *Service) enqueueTargetProjectionRefreshJobs(ctx context.Context, group database.Group, targets []targetRef) error {
	return s.enqueueTargetProjectionRefreshJobsDB(s.db.WithContext(ctx), group, targets)
}

func (s *Service) enqueueTargetProjectionRefreshJobsTx(tx *gorm.DB, group database.Group, targets []targetRef) error {
	return s.enqueueTargetProjectionRefreshJobsDB(tx, group, targets)
}

func (s *Service) enqueueTargetProjectionRefreshJobsDB(db *gorm.DB, group database.Group, targets []targetRef) error {
	now := time.Now().UTC()
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if target.TargetType != "pull_request" && target.TargetType != "issue" {
			continue
		}
		if target.TargetKey == "" {
			continue
		}
		dedupeKey := target.TargetType + ":" + target.TargetKey
		if _, ok := seen[dedupeKey]; ok {
			continue
		}
		seen[dedupeKey] = struct{}{}

		var existing database.IndexJob
		err := db.
			Where("kind = ? AND github_repository_id = ? AND target_type = ? AND target_key = ? AND status IN ?", indexJobKindTargetProjectionRefresh, group.GitHubRepositoryID, target.TargetType, target.TargetKey, []string{"pending", "processing"}).
			First(&existing).Error
		if err == nil {
			continue
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		job := database.IndexJob{
			Kind:               indexJobKindTargetProjectionRefresh,
			Status:             "pending",
			GitHubRepositoryID: group.GitHubRepositoryID,
			RepositoryOwner:    group.RepositoryOwner,
			RepositoryName:     group.RepositoryName,
			TargetType:         target.TargetType,
			TargetKey:          target.TargetKey,
			NextAttemptAt:      timePtr(now),
		}
		if target.Projection != nil {
			job.SourceUpdatedAt = timePtr(target.Projection.SourceUpdatedAt)
		}
		if err := db.Create(&job).Error; err != nil {
			return err
		}
	}
	return nil
}

func groupMemberViewFromModel(member database.GroupMember, resolution groupMemberResolution) GroupMemberView {
	return GroupMemberView{
		ID:                 member.ID,
		GitHubRepositoryID: member.GitHubRepositoryID,
		ObjectType:         member.ObjectType,
		ObjectNumber:       member.ObjectNumber,
		TargetKey:          member.TargetKey,
		AddedBy:            member.AddedBy,
		AddedAt:            member.AddedAt,
		ObjectSummary:      resolution.Summary,
		ObjectFreshness:    resolution.Freshness,
	}
}

func projectionToGroupMemberResolution(now time.Time, projection database.TargetProjection) groupMemberResolution {
	fetchedAt := projection.FetchedAt
	state := "current"
	if targetProjectionStale(now, projection) {
		state = "stale"
	}
	return groupMemberResolution{
		Summary: &GroupMemberObjectSummary{
			Title:       projection.Title,
			State:       projection.State,
			HTMLURL:     projection.HTMLURL,
			AuthorLogin: projection.AuthorLogin,
			UpdatedAt:   projection.SourceUpdatedAt,
		},
		Freshness: &GroupMemberObjectFreshness{
			State:     state,
			Source:    "target_projection",
			FetchedAt: &fetchedAt,
		},
	}
}

func missingGroupMemberResolution() groupMemberResolution {
	return groupMemberResolution{
		Freshness: &GroupMemberObjectFreshness{
			State:  "missing",
			Source: "missing_projection",
		},
	}
}

func targetProjectionStale(now time.Time, projection database.TargetProjection) bool {
	if projection.FetchedAt.IsZero() {
		return true
	}
	return now.Sub(projection.FetchedAt.UTC()) > targetProjectionFreshnessTTL
}

func groupMemberTargetRef(group database.Group, member database.GroupMember, projection *database.TargetProjection) targetRef {
	return targetRef{
		RepositoryID: group.GitHubRepositoryID,
		Owner:        group.RepositoryOwner,
		Name:         group.RepositoryName,
		TargetType:   member.ObjectType,
		TargetKey:    member.TargetKey,
		ObjectNumber: member.ObjectNumber,
		Projection:   projection,
	}
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
	var event database.Event
	for attempts := 0; attempts < 5; attempts++ {
		var nextSequence int
		if err := tx.Model(&database.Event{}).
			Select("COALESCE(MAX(sequence_no), 0) + 1").
			Where("aggregate_type = ? AND aggregate_key = ?", input.AggregateType, input.AggregateKey).
			Scan(&nextSequence).Error; err != nil {
			return err
		}

		event = database.Event{
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
		savepoint := fmt.Sprintf("event_sequence_retry_%d", attempts)
		if err := tx.SavePoint(savepoint).Error; err != nil {
			return err
		}
		if err := tx.Create(&event).Error; err != nil {
			if isEventSequenceConflict(err) {
				if rollbackErr := tx.RollbackTo(savepoint).Error; rollbackErr != nil {
					return rollbackErr
				}
				continue
			}
			return err
		}
		goto refs
	}
	return &FailError{StatusCode: 409, Message: "event sequence conflict"}

refs:
	for _, ref := range input.Refs {
		if err := tx.Create(&database.EventRef{
			EventID: event.ID,
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
	RepositoryID int64
	Owner        string
	Name         string
	TargetType   string
	TargetKey    string
	ObjectNumber int
	GroupID      *uint
	Projection   *database.TargetProjection
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
	if t.Projection != nil {
		return t.Projection.SourceUpdatedAt
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
		switch definition.FieldType {
		case "enum":
			if value.EnumValue != nil {
				if _, ok := allowedSet[*value.EnumValue]; !ok {
					return &FailError{StatusCode: 409, Message: "enum_values would invalidate existing annotations"}
				}
			}
		case "multi_enum":
			var existing []string
			if err := json.Unmarshal(value.MultiEnumJSON, &existing); err != nil {
				return err
			}
			for _, candidate := range existing {
				if _, ok := allowedSet[candidate]; !ok {
					return &FailError{StatusCode: 409, Message: "enum_values would invalidate existing annotations"}
				}
			}
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
		value := strings.TrimSpace(fmt.Sprint(raw))
		base.StringValue = &value
		base.APIValue = value
		return base, false, nil
	case "text":
		value := strings.TrimSpace(fmt.Sprint(raw))
		base.TextValue = &value
		base.APIValue = value
		return base, false, nil
	case "boolean":
		boolValue, ok := raw.(bool)
		if !ok {
			return convertedValue{}, false, &FailError{StatusCode: 400, Message: "expected boolean value"}
		}
		base.BoolValue = &boolValue
		base.APIValue = boolValue
		return base, false, nil
	case "integer":
		switch typed := raw.(type) {
		case float64:
			if math.Trunc(typed) != typed {
				return convertedValue{}, false, &FailError{StatusCode: 400, Message: "expected integer value"}
			}
			value := int64(typed)
			base.IntValue = &value
			base.APIValue = value
			return base, false, nil
		case int:
			value := int64(typed)
			base.IntValue = &value
			base.APIValue = value
			return base, false, nil
		case int64:
			base.IntValue = &typed
			base.APIValue = typed
			return base, false, nil
		default:
			return convertedValue{}, false, &FailError{StatusCode: 400, Message: "expected integer value"}
		}
	case "enum":
		value := strings.TrimSpace(fmt.Sprint(raw))
		if !enumAllowed(definition.EnumValuesJSON, value) {
			return convertedValue{}, false, &FailError{StatusCode: 400, Message: "invalid enum value"}
		}
		base.EnumValue = &value
		base.APIValue = value
		return base, false, nil
	case "multi_enum":
		items, ok := raw.([]any)
		if !ok {
			return convertedValue{}, false, &FailError{StatusCode: 400, Message: "expected array value"}
		}
		values := make([]string, 0, len(items))
		for _, item := range items {
			value := strings.TrimSpace(fmt.Sprint(item))
			if !enumAllowed(definition.EnumValuesJSON, value) {
				return convertedValue{}, false, &FailError{StatusCode: 400, Message: "invalid multi_enum value"}
			}
			values = append(values, value)
		}
		sort.Strings(values)
		bytes, _ := json.Marshal(values)
		base.MultiEnumJSON = datatypes.JSON(bytes)
		base.APIValue = values
		return base, false, nil
	default:
		return convertedValue{}, false, &FailError{StatusCode: 400, Message: "unsupported field type"}
	}
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

func uintValueOrZero(value *uint) uint {
	if value == nil {
		return 0
	}
	return *value
}

func (s *Service) lookupGroupByID(ctx context.Context, groupID uint) (database.Group, error) {
	var group database.Group
	err := s.db.WithContext(ctx).First(&group, groupID).Error
	return group, err
}

func (s *Service) lookupGroupByPublicID(ctx context.Context, groupPublicID string) (database.Group, error) {
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
		strings.Contains(text, "group_members_group_id_object_type_object_number_key") ||
		strings.Contains(text, "duplicate key")
}

func lockEventAggregateTx(tx *gorm.DB, aggregateType string, aggregateKey string) error {
	if tx == nil || tx.Dialector == nil {
		return nil
	}
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	return tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?), hashtext(?))", aggregateType, aggregateKey).Error
}
