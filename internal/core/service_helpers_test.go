package core

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/dutifuldev/prtags/internal/database"
	"github.com/dutifuldev/prtags/internal/permissions"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type checkerErrorStub struct {
	allowed     bool
	canWriteErr error
	identity    permissions.Identity
	resolveErr  error
}

func (c checkerErrorStub) CanWrite(context.Context, permissions.Actor, string, string) (bool, error) {
	return c.allowed, c.canWriteErr
}

func (c checkerErrorStub) ResolveIdentity(context.Context, permissions.Actor) (permissions.Identity, error) {
	return c.identity, c.resolveErr
}

type checkerOnlyStub struct {
	allowed bool
	err     error
}

func (c checkerOnlyStub) CanWrite(context.Context, permissions.Actor, string, string) (bool, error) {
	return c.allowed, c.err
}

func TestServiceHelperValidationAndConflicts(t *testing.T) {
	t.Run("fail error string", func(t *testing.T) {
		require.Equal(t, "boom", (&FailError{Message: "boom"}).Error())
	})

	t.Run("field validation", func(t *testing.T) {
		require.Error(t, validateFieldDefinitionInput(FieldDefinitionInput{}))
		require.NoError(t, validateFieldDefinitionInput(FieldDefinitionInput{
			Name:        "intent",
			ObjectScope: "pull_request",
			FieldType:   "text",
		}))
		require.Error(t, validateFieldDefinitionInput(FieldDefinitionInput{
			Name:        "quality",
			ObjectScope: "pull_request",
			FieldType:   "enum",
		}))
	})

	t.Run("patch validation", func(t *testing.T) {
		require.Error(t, validateFieldDefinitionPatchInput(FieldDefinitionPatchInput{}))
		require.NoError(t, validateFieldDefinitionPatchInput(FieldDefinitionPatchInput{DisplayName: stringPtr("Intent")}))
		require.Error(t, validateGroupPatchInput(GroupPatchInput{}))
		require.NoError(t, validateGroupPatchInput(GroupPatchInput{Title: stringPtr("Updated")}))
	})

	t.Run("group validation", func(t *testing.T) {
		require.Error(t, validateGroupInput(GroupInput{}))
		require.NoError(t, validateGroupInput(GroupInput{Kind: "mixed", Title: "Auth"}))
		require.Error(t, validateMemberType("pull_request", "issue"))
		require.NoError(t, validateMemberType("mixed", "issue"))
	})

	t.Run("row version helpers", func(t *testing.T) {
		err := ensureExpectedRowVersion(2, intPtr(3))
		var fail *FailError
		require.ErrorAs(t, err, &fail)
		require.NoError(t, ensureExpectedRowVersion(2, nil))
	})

	t.Run("db conflict detection", func(t *testing.T) {
		require.False(t, isEventSequenceConflict(nil))
		require.True(t, isEventSequenceConflict(errors.New("duplicate key on idx_events_aggregate_sequence")))
		require.False(t, isGroupPublicIDConflict(nil))
		require.True(t, isGroupPublicIDConflict(errors.New("duplicate key on idx_groups_public_id")))
		require.True(t, isGroupMemberConflict(errors.New("unique constraint failed: group_members.github_repository_id, group_members.object_type, group_members.object_number")))
	})
}

func TestServiceHelperConversionsAndEnums(t *testing.T) {
	enumRaw := datatypes.JSON([]byte(`["low","high","bug"]`))

	stringValue, clear, err := convertAnnotationValue(database.FieldDefinition{FieldType: "string"}, " hello ")
	require.NoError(t, err)
	require.False(t, clear)
	require.Equal(t, "hello", stringValue.APIValue)

	boolValue, clear, err := convertAnnotationValue(database.FieldDefinition{FieldType: "boolean"}, true)
	require.NoError(t, err)
	require.False(t, clear)
	require.Equal(t, true, boolValue.APIValue)

	intValue, clear, err := convertAnnotationValue(database.FieldDefinition{FieldType: "integer"}, float64(3))
	require.NoError(t, err)
	require.False(t, clear)
	require.EqualValues(t, 3, intValue.APIValue)
	_, _, err = convertAnnotationValue(database.FieldDefinition{FieldType: "integer"}, 3.5)
	require.Error(t, err)

	enumValue, clear, err := convertAnnotationValue(database.FieldDefinition{FieldType: "enum", EnumValuesJSON: enumRaw}, "high")
	require.NoError(t, err)
	require.False(t, clear)
	require.Equal(t, "high", enumValue.APIValue)
	_, _, err = convertAnnotationValue(database.FieldDefinition{FieldType: "enum", EnumValuesJSON: enumRaw}, "missing")
	require.Error(t, err)

	multiValue, clear, err := convertAnnotationValue(database.FieldDefinition{FieldType: "multi_enum", EnumValuesJSON: enumRaw}, []any{"bug", "high"})
	require.NoError(t, err)
	require.False(t, clear)
	require.Equal(t, []string{"bug", "high"}, multiValue.APIValue)
	_, _, err = convertAnnotationValue(database.FieldDefinition{FieldType: "multi_enum", EnumValuesJSON: enumRaw}, "bad")
	require.Error(t, err)

	nullValue, clear, err := convertAnnotationValue(database.FieldDefinition{FieldType: "text"}, nil)
	require.NoError(t, err)
	require.True(t, clear)
	require.Equal(t, convertedValue{}, nullValue)

	require.True(t, enumAllowed(enumRaw, "high"))
	require.False(t, enumAllowed(enumRaw, "missing"))
	ok, err := multiEnumContains(datatypes.JSON([]byte(`["bug","auth"]`)), "auth")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "intent", normalizeFieldName("Intent"))
	require.Equal(t, "intent", displayName(FieldDefinitionInput{Name: "Intent"}))
	require.Equal(t, []string{"a", "b"}, normalizeEnumValues([]string{"b", "a", "b", ""}))
	require.Equal(t, "open", defaultStatus(""))
	require.Equal(t, 0, intValueOrZero(nil))
}

func TestServiceHelperDBBackedPaths(t *testing.T) {
	ctx := context.Background()
	service, db, server := newTestService(t)
	defer server.Close()

	field := database.FieldDefinition{
		GitHubRepositoryID: 101,
		RepositoryOwner:    "acme",
		RepositoryName:     "widgets",
		Name:               "quality",
		DisplayName:        "Quality",
		ObjectScope:        "pull_request",
		FieldType:          "enum",
		EnumValuesJSON:     datatypes.JSON([]byte(`["low","high"]`)),
		CreatedBy:          "tester",
		UpdatedBy:          "tester",
		RowVersion:         1,
	}
	require.NoError(t, db.Create(&field).Error)
	enumValue := "high"
	require.NoError(t, db.Create(&database.FieldValue{
		FieldDefinitionID:  field.ID,
		GitHubRepositoryID: 101,
		RepositoryOwner:    "acme",
		RepositoryName:     "widgets",
		TargetType:         "pull_request",
		ObjectNumber:       intPtr(22),
		TargetKey:          objectTargetKey(101, "pull_request", 22),
		EnumValue:          &enumValue,
		UpdatedBy:          "tester",
	}).Error)

	require.Error(t, service.ensureEnumValuesCompatible(ctx, field, []string{"low"}))
	require.NoError(t, service.ensureEnumValuesCompatible(ctx, field, []string{"low", "high"}))

	rowErr := staleRowVersionError(db, &database.FieldDefinition{}, field.ID, 5)
	var fail *FailError
	require.ErrorAs(t, rowErr, &fail)
	require.Equal(t, 409, fail.StatusCode)

	group, err := service.CreateGroup(ctx, permissionsActor(), "acme", "widgets", GroupInput{Kind: "pull_request", Title: "Auth"}, "")
	require.NoError(t, err)
	member, err := service.AddGroupMember(ctx, permissionsActor(), group.PublicID, "pull_request", 22, "")
	require.NoError(t, err)
	conflictErr := service.translateGroupMemberConflictTx(db, group, database.GroupMember{
		GitHubRepositoryID: group.GitHubRepositoryID,
		ObjectType:         member.ObjectType,
		ObjectNumber:       member.ObjectNumber,
	})
	require.ErrorAs(t, conflictErr, &fail)
	require.Equal(t, 409, fail.StatusCode)
	require.Equal(t, "group member already exists", fail.Message)

	locked := lockEventAggregateTx(db, "group", groupTargetKey(group.PublicID))
	require.NoError(t, locked)
	require.Equal(t, ErrNotFound, translateDBError(gorm.ErrRecordNotFound))
	require.Equal(t, "repo:101:pull_request:22", objectTargetKey(101, "pull_request", 22))
}

func TestMirrorSummaryHelpers(t *testing.T) {
	now := time.Now().UTC()
	summary := objectSummaryFromMirror(testObjectSummary("pull_request", 22))
	summary.UpdatedAt = now
	member := database.GroupMember{
		ID:                 7,
		GitHubRepositoryID: 101,
		ObjectType:         "pull_request",
		ObjectNumber:       22,
		TargetKey:          objectTargetKey(101, "pull_request", 22),
		AddedBy:            "tester",
		AddedAt:            now,
	}
	view := groupMemberViewFromModel(member, summary, true)
	require.NotNil(t, view.ObjectSummary)
	require.Equal(t, "Retry ACP turns safely", view.ObjectSummary.Title)
	require.Nil(t, groupMemberViewFromModel(member, summary, false).ObjectSummary)

	members := []database.GroupMember{
		{ObjectType: "pull_request", ObjectNumber: 22},
		{ObjectType: "issue", ObjectNumber: 11},
		{ObjectType: "pull_request", ObjectNumber: 22},
		{ObjectType: "group", ObjectNumber: 1},
	}
	refs := mirrorObjectRefsForMembers(members)
	require.Len(t, refs, 3)
}

func TestServiceHelperDirectPaths(t *testing.T) {
	ctx := context.Background()
	service, db, server := newTestService(t)
	defer server.Close()

	commentSyncDB := openCommentSyncTestDB(t)
	store, githubClient := newTestGitHubCommentClient(t)
	_ = store
	commentSync := NewCommentSyncService(commentSyncDB, testMirrorClient{}, githubClient, &commentSyncDispatcherStub{})
	service.SetJobDispatcher(nil)
	service.SetCommentSync(commentSync)

	updates := map[string]any{}
	require.NoError(t, applyFieldDisplayNameUpdate(updates, stringPtr(" Intent ")))
	require.Equal(t, "Intent", updates["display_name"])
	require.Error(t, applyFieldDisplayNameUpdate(updates, stringPtr(" ")))
	applyOptionalFieldUpdate(updates, "is_required", boolPtr(true))
	require.Equal(t, true, updates["is_required"])

	enumRaw := datatypes.JSON([]byte(`["low","high","bug"]`))
	field := database.FieldDefinition{
		GitHubRepositoryID: 101,
		RepositoryOwner:    "acme",
		RepositoryName:     "widgets",
		Name:               "severity",
		DisplayName:        "Severity",
		ObjectScope:        "pull_request",
		FieldType:          "enum",
		EnumValuesJSON:     enumRaw,
		CreatedBy:          "tester",
		UpdatedBy:          "tester",
		RowVersion:         1,
	}
	require.NoError(t, db.Create(&field).Error)
	require.NoError(t, db.Create(&database.FieldValue{
		FieldDefinitionID:  field.ID,
		GitHubRepositoryID: 101,
		RepositoryOwner:    "acme",
		RepositoryName:     "widgets",
		TargetType:         "pull_request",
		ObjectNumber:       intPtr(22),
		TargetKey:          objectTargetKey(101, "pull_request", 22),
		EnumValue:          stringPtr("high"),
		UpdatedBy:          "tester",
	}).Error)

	fieldUpdates, err := service.fieldDefinitionUpdates(ctx, permissionsActor(), field, FieldDefinitionPatchInput{
		DisplayName: stringPtr("Severity Label"),
		EnumValues:  &[]string{"low", "high", "bug"},
		IsRequired:  boolPtr(true),
	})
	require.NoError(t, err)
	require.Contains(t, fieldUpdates, "display_name")
	require.Contains(t, fieldUpdates, "enum_values_json")

	require.Error(t, service.applyFieldEnumUpdate(ctx, map[string]any{}, database.FieldDefinition{FieldType: "text"}, &[]string{"a"}))
	require.Error(t, service.applyFieldEnumUpdate(ctx, map[string]any{}, database.FieldDefinition{FieldType: "enum"}, &[]string{}))
	require.NoError(t, validateMultiEnumCompatibility(map[string]struct{}{"bug": {}, "high": {}}, datatypes.JSON(`["bug","high"]`)))
	require.Error(t, validateMultiEnumCompatibility(map[string]struct{}{"bug": {}}, datatypes.JSON(`["bug","high"]`)))
	require.Error(t, validateMultiEnumCompatibility(map[string]struct{}{"bug": {}}, datatypes.JSON(`{bad`)))

	for _, tc := range []database.FieldValue{
		{StringValue: stringPtr("s")},
		{TextValue: stringPtr("t")},
		{BoolValue: boolPtr(true)},
		{IntValue: int64Ptr(9)},
		{EnumValue: stringPtr("high")},
		{MultiEnumJSON: datatypes.JSON(`["bug"]`)},
	} {
		require.NotNil(t, fieldValueToAPI(tc))
	}
	require.Nil(t, fieldValueToAPI(database.FieldValue{}))
	require.Error(t, validateEnumCompatibility("enum", map[string]struct{}{"low": {}}, database.FieldValue{EnumValue: stringPtr("high")}))
	require.Nil(t, validateEnumCompatibility("text", nil, database.FieldValue{}))

	require.Equal(t, datatypes.JSON(`["a","b"]`), manifestEnumValuesJSON(FieldDefinitionInput{EnumValues: []string{"b", "a"}}))
	manifestUpdates := manifestFieldUpdates(permissionsActor(), FieldDefinitionInput{
		Name:         "severity",
		DisplayName:  "Severity",
		EnumValues:   []string{"bug"},
		IsRequired:   true,
		IsFilterable: true,
		IsSearchable: true,
		IsVectorized: true,
		SortOrder:    9,
	})
	require.Equal(t, "tester", manifestUpdates["updated_by"])
	require.Contains(t, manifestUpdates, "enum_values_json")

	require.NoError(t, validateManifestFieldType(database.FieldDefinition{FieldType: "enum"}, "enum"))
	require.Error(t, validateManifestFieldType(database.FieldDefinition{Name: "severity", ObjectScope: "pull_request", FieldType: "enum"}, "text"))
}

func TestServiceHelperDBBackedOperations(t *testing.T) {
	ctx := context.Background()
	service, db, server := newTestService(t)
	defer server.Close()

	repository := database.RepositoryProjection{GitHubRepositoryID: 101, Owner: "acme", Name: "widgets"}
	actor := permissionsActor()

	field := database.FieldDefinition{
		GitHubRepositoryID: repository.GitHubRepositoryID,
		RepositoryOwner:    repository.Owner,
		RepositoryName:     repository.Name,
		Name:               "intent",
		DisplayName:        "Intent",
		ObjectScope:        "pull_request",
		FieldType:          "text",
		CreatedBy:          actor.ID,
		UpdatedBy:          actor.ID,
		RowVersion:         1,
	}
	require.NoError(t, db.Create(&field).Error)
	target := targetRef{
		RepositoryID: repository.GitHubRepositoryID,
		Owner:        repository.Owner,
		Name:         repository.Name,
		TargetType:   "pull_request",
		TargetKey:    objectTargetKey(repository.GitHubRepositoryID, "pull_request", 22),
		ObjectNumber: 22,
	}

	converted, clear, err := convertAnnotationValue(field, "hello")
	require.NoError(t, err)
	require.False(t, clear)
	annotations := map[string]any{}
	tx := db.Begin()
	require.NoError(t, tx.Error)
	require.NoError(t, service.setAnnotationValueTx(tx, repository, target, actor, field, converted, "intent", "idem-1", annotations))
	require.Equal(t, "hello", annotations["intent"])
	require.NoError(t, service.clearAnnotationValueTx(tx, repository, target, actor, field, "intent", "idem-2", annotations))
	require.Nil(t, annotations["intent"])
	require.NoError(t, tx.Commit().Error)

	var values []database.FieldValue
	require.NoError(t, db.Where("field_definition_id = ?", field.ID).Find(&values).Error)
	require.Len(t, values, 0)

	model := annotationFieldValueModel(repository, target, actor, field.ID, convertedValue{StringValue: stringPtr("value"), APIValue: "value", MultiEnumJSON: datatypes.JSON(`[]`)})
	require.Equal(t, repository.Owner, model.RepositoryOwner)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		_, err := service.createManifestFieldTx(tx, repository, actor, FieldDefinitionInput{
			Name:         "severity",
			DisplayName:  "Severity",
			ObjectScope:  "pull_request",
			FieldType:    "enum",
			EnumValues:   []string{"bug"},
			IsFilterable: true,
		}, "manifest-create")
		return err
	}))

	var existing database.FieldDefinition
	require.NoError(t, db.Where("name = ?", "severity").First(&existing).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		updated, err := service.updateManifestFieldTx(ctx, tx, repository, actor, FieldDefinitionInput{
			Name:         "severity",
			DisplayName:  "Severity v2",
			ObjectScope:  "pull_request",
			FieldType:    "enum",
			EnumValues:   []string{"bug"},
			IsFilterable: true,
			SortOrder:    4,
		}, existing, "manifest-update")
		require.NoError(t, err)
		require.Equal(t, "Severity v2", updated.DisplayName)
		return nil
	}))

	targets := []targetRef{}
	require.NoError(t, db.Create(&database.FieldValue{
		FieldDefinitionID:  field.ID,
		GitHubRepositoryID: repository.GitHubRepositoryID,
		RepositoryOwner:    repository.Owner,
		RepositoryName:     repository.Name,
		TargetType:         "pull_request",
		ObjectNumber:       intPtr(22),
		TargetKey:          target.TargetKey,
		UpdatedBy:          actor.ID,
	}).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return service.collectTargetsForFieldTx(tx, field.ID, &targets)
	}))
	require.Len(t, targets, 1)

	updated := annotationFieldValueModel(repository, target, actor, field.ID, convertedValue{StringValue: stringPtr("new"), APIValue: "new"})
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return upsertAnnotationFieldValueTx(tx, updated, convertedValue{StringValue: stringPtr("new"), APIValue: "new"}, actor.ID)
	}))

	group, err := service.CreateGroup(ctx, actor, repository.Owner, repository.Name, GroupInput{Kind: "mixed", Title: "Auth group"}, "")
	require.NoError(t, err)
	require.NoError(t, db.Create(&database.GroupCommentSyncTarget{
		GroupID:            group.ID,
		GitHubRepositoryID: group.GitHubRepositoryID,
		ObjectType:         "pull_request",
		ObjectNumber:       22,
		TargetKey:          target.TargetKey,
		DesiredRevision:    2,
		AppliedRevision:    1,
		LastError:          "boom",
		LastErrorKind:      "permission_denied",
		LastErrorAt:        timePtr(time.Now().UTC()),
	}).Error)

	rows, err := service.ListGroupCommentSyncTargets(ctx, actor, repository.Owner, repository.Name)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "failed", rows[0].State)
	repoProjection, err := service.lookupRepositoryProjection(ctx, repository.Owner, repository.Name)
	require.NoError(t, err)
	require.Equal(t, repository.Owner, repoProjection.Owner)
}

func TestFilterAndEventHelpers(t *testing.T) {
	service, db, server := newTestService(t)
	defer server.Close()

	query, err := applyFieldValueFilter(db.Model(&database.FieldValue{}), "boolean", "true")
	require.NoError(t, err)
	require.NotNil(t, query)
	_, err = applyFieldValueFilter(db.Model(&database.FieldValue{}), "integer", "bad")
	require.Error(t, err)
	query, err = applyFieldValueFilter(db.Model(&database.FieldValue{}), "multi_enum", "bug")
	require.NoError(t, err)
	require.NotNil(t, query)

	event := newEventRecord(eventInput{
		RepositoryID:  101,
		AggregateType: "group",
		AggregateKey:  "group:test",
		EventType:     "group.updated",
		Actor:         permissionsActor(),
		Payload:       map[string]any{"ok": true},
		Metadata:      map[string]any{"source": "test"},
	}, 1, []byte(`{"ok":true}`), []byte(`{"source":"test"}`))
	require.Equal(t, 1, event.SequenceNo)
	require.Equal(t, "tester", event.ActorID)

	tx := db.Begin()
	require.NoError(t, tx.Error)
	defer func() { _ = tx.Rollback().Error }()
	inserted, retry, err := insertEventWithSavepoint(tx, 1, event)
	require.NoError(t, err)
	require.False(t, retry)
	require.NotZero(t, inserted.ID)
	require.NoError(t, insertEventRefs(tx, inserted.ID, []eventRefInput{{Role: "group", Type: "group", Key: "group:test"}}))

	require.NoError(t, lockEventAggregateTx(tx, "group", "group:test"))
	require.NoError(t, service.appendEventTx(tx, eventInput{
		RepositoryID:  101,
		AggregateType: "group",
		AggregateKey:  "group:test",
		EventType:     "group.updated",
		Actor:         permissionsActor(),
		Payload:       map[string]any{"group_id": 1},
	}))

	var count int64
	require.NoError(t, tx.Model(&database.Event{}).Where("aggregate_key = ?", "group:test").Count(&count).Error)
	require.GreaterOrEqual(t, count, int64(2))

	conflictEvent := database.Event{
		GitHubRepositoryID: 101,
		AggregateType:      "group",
		AggregateKey:       "group:test",
		SequenceNo:         1,
		EventType:          "group.updated",
		ActorType:          "user",
		ActorID:            "tester",
		OccurredAt:         time.Now().UTC(),
	}
	conflicted, retry, err := insertEventWithSavepoint(tx, 2, conflictEvent)
	require.NoError(t, err)
	require.True(t, retry)
	require.Equal(t, uint(0), conflicted.ID)

	require.NoError(t, insertEventRefs(tx, 1, nil))
	require.True(t, isEventSequenceConflict(gorm.ErrDuplicatedKey))
	require.Equal(t, ErrNotFound, translateDBError(gorm.ErrRecordNotFound))
	require.Equal(t, sql.ErrNoRows, translateDBError(sql.ErrNoRows))
}

func TestFieldAndGroupTransactionHelpers(t *testing.T) {
	ctx := context.Background()
	service, db, server := newTestService(t)
	defer server.Close()

	repository := database.RepositoryProjection{GitHubRepositoryID: 101, Owner: "acme", Name: "widgets"}
	actor := permissionsActor()

	field := database.FieldDefinition{
		GitHubRepositoryID: repository.GitHubRepositoryID,
		RepositoryOwner:    repository.Owner,
		RepositoryName:     repository.Name,
		Name:               "intent",
		DisplayName:        "Intent",
		ObjectScope:        "pull_request",
		FieldType:          "text",
		CreatedBy:          actor.ID,
		UpdatedBy:          actor.ID,
		RowVersion:         1,
	}
	require.NoError(t, db.Create(&field).Error)

	fieldTx := db.Begin()
	require.NoError(t, fieldTx.Error)
	fieldUpdates := map[string]any{
		"display_name": "Intent Summary",
		"updated_by":   actor.ID,
		"updated_at":   time.Now().UTC(),
		"row_version":  gorm.Expr("row_version + 1"),
	}
	require.NoError(t, service.updateFieldDefinitionTx(fieldTx, &field, repository, actor, intPtr(1), fieldUpdates, "field-update"))
	require.NoError(t, fieldTx.Commit().Error)
	require.Equal(t, "Intent Summary", field.DisplayName)
	require.Equal(t, 2, field.RowVersion)

	require.Error(t, applyFieldDefinitionUpdatesTx(db, field.ID, intPtr(1), map[string]any{"display_name": "stale"}))

	archiveTx := db.Begin()
	require.NoError(t, archiveTx.Error)
	require.NoError(t, service.archiveFieldDefinitionTx(archiveTx, &field, repository, actor, intPtr(2), "field-archive", time.Now().UTC()))
	require.NoError(t, archiveTx.Commit().Error)
	require.NotNil(t, field.ArchivedAt)

	group := database.Group{
		PublicID:           "swift-heron-t6q1",
		GitHubRepositoryID: repository.GitHubRepositoryID,
		RepositoryOwner:    repository.Owner,
		RepositoryName:     repository.Name,
		Kind:               "mixed",
		Title:              "Original title",
		Status:             "open",
		CreatedBy:          actor.ID,
		UpdatedBy:          actor.ID,
		RowVersion:         1,
	}

	groupTx := db.Begin()
	require.NoError(t, groupTx.Error)
	require.NoError(t, service.createGroupTx(groupTx, &group, repository, actor, "group-create"))
	require.NoError(t, groupTx.Commit().Error)
	require.NotZero(t, group.ID)

	updates, err := groupUpdates(actor, GroupPatchInput{
		Title:  stringPtr(" Updated title "),
		Status: stringPtr(" closed "),
	})
	require.NoError(t, err)
	updateTx := db.Begin()
	require.NoError(t, updateTx.Error)
	require.NoError(t, service.updateGroupTx(updateTx, &group, repository, actor, intPtr(1), updates, "group-update"))
	require.NoError(t, updateTx.Commit().Error)
	require.Equal(t, "Updated title", group.Title)
	require.Equal(t, "closed", group.Status)
	require.Equal(t, 2, group.RowVersion)

	_, err = groupUpdates(actor, GroupPatchInput{Title: stringPtr(" ")})
	require.Error(t, err)
	_, err = groupUpdates(actor, GroupPatchInput{Status: stringPtr(" ")})
	require.Error(t, err)
	require.Error(t, applyGroupUpdatesTx(db, group.ID, intPtr(1), map[string]any{"title": "stale"}))

	member := database.GroupMember{
		GroupID:            group.ID,
		GitHubRepositoryID: group.GitHubRepositoryID,
		ObjectType:         "issue",
		ObjectNumber:       11,
		TargetKey:          objectTargetKey(group.GitHubRepositoryID, "issue", 11),
		AddedBy:            actor.ID,
		AddedAt:            time.Now().UTC(),
	}
	memberTx := db.Begin()
	require.NoError(t, memberTx.Error)
	require.NoError(t, insertGroupMemberTx(memberTx, group, &member, func(*gorm.DB, database.Group, database.GroupMember) error { return nil }))
	require.NoError(t, memberTx.Commit().Error)

	var translated bool
	conflictMember := database.GroupMember{
		GroupID:            group.ID,
		GitHubRepositoryID: group.GitHubRepositoryID,
		ObjectType:         "issue",
		ObjectNumber:       11,
		TargetKey:          objectTargetKey(group.GitHubRepositoryID, "issue", 11),
		AddedBy:            actor.ID,
		AddedAt:            time.Now().UTC(),
	}
	conflictTx := db.Begin()
	require.NoError(t, conflictTx.Error)
	err = insertGroupMemberTx(conflictTx, group, &conflictMember, func(*gorm.DB, database.Group, database.GroupMember) error {
		translated = true
		return ErrForbidden
	})
	require.ErrorIs(t, err, ErrForbidden)
	require.True(t, translated)
	require.NoError(t, conflictTx.Rollback().Error)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return service.addGroupMemberTx(tx, group, repository, actor, &database.GroupMember{
			GroupID:            group.ID,
			GitHubRepositoryID: group.GitHubRepositoryID,
			ObjectType:         "pull_request",
			ObjectNumber:       22,
			TargetKey:          objectTargetKey(group.GitHubRepositoryID, "pull_request", 22),
			AddedBy:            actor.ID,
			AddedAt:            time.Now().UTC(),
		}, "group-member-added")
	}))

	var events []database.Event
	require.NoError(t, db.WithContext(ctx).Where("aggregate_type IN ?", []string{"field_definition", "group"}).Find(&events).Error)
	require.NotEmpty(t, events)
}

func TestDispatcherBackedQueueHelpers(t *testing.T) {
	ctx := context.Background()
	service, _, server := newTestService(t)
	defer server.Close()

	dispatcher := &jobDispatcherStub{}
	service.SetJobDispatcher(dispatcher)

	repository := database.RepositoryProjection{GitHubRepositoryID: 101, Owner: "acme", Name: "widgets"}
	require.NoError(t, service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return service.enqueueRebuildsTx(tx, repository, targetRef{
			RepositoryID: 101,
			Owner:        "acme",
			Name:         "widgets",
			TargetType:   "pull_request",
			TargetKey:    objectTargetKey(101, "pull_request", 22),
			ObjectNumber: 22,
		}, time.Now().UTC())
	}))
	require.Len(t, dispatcher.rebuildCalls, 1)
}

func TestServiceLowCoverageHelperBranches(t *testing.T) {
	ctx := context.Background()
	service, db, server := newTestService(t)
	defer server.Close()

	actor := permissionsActor()
	repository := database.RepositoryProjection{GitHubRepositoryID: 101, Owner: "acme", Name: "widgets"}

	require.NoError(t, validateMemberType("pull_request", "pull_request"))
	require.NoError(t, validateMemberType("issue", "issue"))
	require.NoError(t, validateMemberType("mixed", "issue"))
	require.NoError(t, validateMemberType("mixed", "pull_request"))
	require.Error(t, validateMemberType("pull_request", "issue"))
	require.Error(t, validateMemberType("mixed", "group"))

	limit, types := normalizeSearchRequest(0, nil)
	require.Equal(t, 20, limit)
	require.Equal(t, []string{"pull_request", "issue", "group"}, types)
	limit, types = normalizeSearchRequest(5, []string{"issue"})
	require.Equal(t, 5, limit)
	require.Equal(t, []string{"issue"}, types)

	require.Error(t, validateRepositoryAccessGrantInput(RepositoryAccessGrantInput{}))
	require.Error(t, validateRepositoryAccessGrantInput(RepositoryAccessGrantInput{
		GitHubUserID:          1,
		GitHubLogin:           "alice",
		Role:                  "reader",
		GrantedByGitHubUserID: 2,
		GrantedByGitHubLogin:  "bob",
	}))

	field := database.FieldDefinition{
		GitHubRepositoryID: repository.GitHubRepositoryID,
		RepositoryOwner:    repository.Owner,
		RepositoryName:     repository.Name,
		Name:               "intent",
		DisplayName:        "Intent",
		ObjectScope:        "pull_request",
		FieldType:          "text",
		CreatedBy:          actor.ID,
		UpdatedBy:          actor.ID,
		RowVersion:         1,
	}
	require.NoError(t, db.Create(&field).Error)
	require.NoError(t, db.Create(&database.FieldValue{
		FieldDefinitionID:  field.ID,
		GitHubRepositoryID: repository.GitHubRepositoryID,
		RepositoryOwner:    repository.Owner,
		RepositoryName:     repository.Name,
		TargetType:         "pull_request",
		ObjectNumber:       intPtr(22),
		TargetKey:          objectTargetKey(repository.GitHubRepositoryID, "pull_request", 22),
		StringValue:        stringPtr("auth"),
		UpdatedBy:          actor.ID,
	}).Error)

	group := database.Group{
		PublicID:           "lively-crane-r8n5",
		GitHubRepositoryID: repository.GitHubRepositoryID,
		RepositoryOwner:    repository.Owner,
		RepositoryName:     repository.Name,
		Kind:               "mixed",
		Title:              "Low coverage helpers",
		Status:             "open",
		CreatedBy:          actor.ID,
		UpdatedBy:          actor.ID,
		RowVersion:         1,
	}
	require.NoError(t, db.Create(&group).Error)
	require.NoError(t, db.Create(&database.GroupMember{
		GroupID:            group.ID,
		GitHubRepositoryID: group.GitHubRepositoryID,
		ObjectType:         "pull_request",
		ObjectNumber:       22,
		TargetKey:          objectTargetKey(group.GitHubRepositoryID, "pull_request", 22),
		AddedBy:            actor.ID,
		AddedAt:            time.Now().UTC(),
	}).Error)

	errorDispatcher := &jobDispatcherStub{rebuildErr: errors.New("queue rebuild failed")}
	service.SetJobDispatcher(errorDispatcher)

	tx := db.Begin()
	require.NoError(t, tx.Error)
	failingGroup := database.Group{
		PublicID:           "ready-gull-a2b4",
		GitHubRepositoryID: repository.GitHubRepositoryID,
		RepositoryOwner:    repository.Owner,
		RepositoryName:     repository.Name,
		Kind:               "mixed",
		Title:              "Fail create",
		Status:             "open",
		CreatedBy:          actor.ID,
		UpdatedBy:          actor.ID,
		RowVersion:         1,
	}
	require.ErrorContains(t, service.createGroupTx(tx, &failingGroup, repository, actor, "create-fail"), "queue rebuild failed")
	require.NoError(t, tx.Rollback().Error)

	tx = db.Begin()
	require.NoError(t, tx.Error)
	duplicateGroup := database.Group{
		PublicID:           group.PublicID,
		GitHubRepositoryID: repository.GitHubRepositoryID,
		RepositoryOwner:    repository.Owner,
		RepositoryName:     repository.Name,
		Kind:               "mixed",
		Title:              "Duplicate",
		Status:             "open",
		CreatedBy:          actor.ID,
		UpdatedBy:          actor.ID,
		RowVersion:         1,
	}
	require.Error(t, service.createGroupTx(tx, &duplicateGroup, repository, actor, "group-create-dup"))
	require.NoError(t, tx.Rollback().Error)

	tx = db.Begin()
	require.NoError(t, tx.Error)
	require.ErrorContains(t, service.updateGroupTx(tx, &group, repository, actor, intPtr(1), map[string]any{
		"title":       "Still failing",
		"updated_by":  actor.ID,
		"updated_at":  time.Now().UTC(),
		"row_version": gorm.Expr("row_version + 1"),
	}, "update-fail"), "queue rebuild failed")
	require.NoError(t, tx.Rollback().Error)

	tx = db.Begin()
	require.NoError(t, tx.Error)
	require.ErrorContains(t, service.updateFieldDefinitionTx(tx, &field, repository, actor, intPtr(1), map[string]any{
		"display_name": "Intent v2",
		"updated_by":   actor.ID,
		"updated_at":   time.Now().UTC(),
		"row_version":  gorm.Expr("row_version + 1"),
	}, "field-update-fail"), "queue rebuild failed")
	require.NoError(t, tx.Rollback().Error)

	tx = db.Begin()
	require.NoError(t, tx.Error)
	require.ErrorContains(t, service.archiveFieldDefinitionTx(tx, &field, repository, actor, intPtr(1), "field-archive-fail", time.Now().UTC()), "queue rebuild failed")
	require.NoError(t, tx.Rollback().Error)

	filtered, ok, err := service.filteredTargetResult(ctx, repository.GitHubRepositoryID, "text", "auth", database.FieldValue{
		GitHubRepositoryID: repository.GitHubRepositoryID,
		TargetType:         "pull_request",
		ObjectNumber:       intPtr(22),
		TargetKey:          objectTargetKey(repository.GitHubRepositoryID, "pull_request", 22),
		StringValue:        stringPtr("auth"),
	})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 22, filtered.ObjectNumber)

	resolved, err := service.resolveTarget(ctx, repository, "pull_request", 22, nil)
	require.NoError(t, err)
	require.Equal(t, "pull_request", resolved.TargetType)
	require.False(t, resolved.SourceUpdatedAt().IsZero())

	_, ok, err = service.filteredTargetResult(ctx, repository.GitHubRepositoryID, "multi_enum", "missing", database.FieldValue{
		GitHubRepositoryID: repository.GitHubRepositoryID,
		TargetType:         "pull_request",
		ObjectNumber:       intPtr(22),
		TargetKey:          objectTargetKey(repository.GitHubRepositoryID, "pull_request", 22),
		MultiEnumJSON:      datatypes.JSON(`["bug"]`),
	})
	require.NoError(t, err)
	require.False(t, ok)

	groupValue := database.FieldValue{
		GitHubRepositoryID: repository.GitHubRepositoryID,
		TargetType:         "group",
		TargetKey:          groupTargetKey(group.PublicID),
		GroupID:            &group.ID,
		StringValue:        stringPtr("auth"),
	}
	groupFiltered, ok, err := service.filteredTargetResult(ctx, repository.GitHubRepositoryID, "text", "auth", groupValue)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, group.PublicID, groupFiltered.ID)

	groupResolved, err := service.resolveTarget(ctx, repository, "group", 0, &group.ID)
	require.NoError(t, err)
	require.Equal(t, "group", groupResolved.TargetType)
	_, err = service.resolveTarget(ctx, repository, "group", 0, nil)
	require.Error(t, err)
	_, err = service.resolveTarget(ctx, repository, "group", 0, uintPtr(99999))
	require.Error(t, err)
	groupAnnotations, err := service.getAnnotationsForTarget(ctx, "group", repository.GitHubRepositoryID, 0, &group.ID)
	require.NoError(t, err)
	require.Empty(t, groupAnnotations)
	_, err = service.getAnnotationsForTarget(ctx, "group", repository.GitHubRepositoryID, 0, uintPtr(99999))
	require.Error(t, err)

	tx = db.Begin()
	require.NoError(t, tx.Error)
	missingField := field
	missingField.ID = 99999
	require.Error(t, service.updateFieldDefinitionTx(tx, &missingField, repository, actor, nil, map[string]any{
		"display_name": "missing",
	}, "field-update-missing"))
	require.NoError(t, tx.Rollback().Error)

	tx = db.Begin()
	require.NoError(t, tx.Error)
	missingGroup := group
	missingGroup.ID = 99999
	require.Error(t, service.updateGroupTx(tx, &missingGroup, repository, actor, nil, map[string]any{
		"title": "missing",
	}, "group-update-missing"))
	require.NoError(t, tx.Rollback().Error)

	tx = db.Begin()
	require.NoError(t, tx.Error)
	require.NoError(t, service.enqueueFieldTargetRebuildsTx(tx, repository, 99999, time.Now().UTC()))
	require.NoError(t, tx.Rollback().Error)

	memberDispatcher := &jobDispatcherStub{rebuildErr: errors.New("queue rebuild failed")}
	service.SetJobDispatcher(memberDispatcher)
	tx = db.Begin()
	require.NoError(t, tx.Error)
	require.ErrorContains(t, service.addGroupMemberTx(tx, group, repository, actor, &database.GroupMember{
		GroupID:            group.ID,
		GitHubRepositoryID: group.GitHubRepositoryID,
		ObjectType:         "issue",
		ObjectNumber:       33,
		TargetKey:          objectTargetKey(group.GitHubRepositoryID, "issue", 33),
		AddedBy:            actor.ID,
		AddedAt:            time.Now().UTC(),
	}, "group-member-rebuild-fail"), "queue rebuild failed")
	require.NoError(t, tx.Rollback().Error)

	tx = db.Begin()
	require.NoError(t, tx.Error)
	missingField.ID = 99998
	require.Error(t, service.archiveFieldDefinitionTx(tx, &missingField, repository, actor, nil, "field-archive-missing", time.Now().UTC()))
	require.NoError(t, tx.Rollback().Error)
}

func TestManifestAndAnnotationHelperBranches(t *testing.T) {
	ctx := context.Background()
	service, db, server := newTestService(t)
	defer server.Close()

	repository := database.RepositoryProjection{GitHubRepositoryID: 101, Owner: "acme", Name: "widgets"}
	actor := permissionsActor()

	field, found, err := loadManifestFieldTx(db, repository.GitHubRepositoryID, FieldDefinitionInput{
		Name:        "missing",
		ObjectScope: "pull_request",
	})
	require.NoError(t, err)
	require.False(t, found)
	require.Zero(t, field.ID)

	var created database.FieldDefinition
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var err error
		created, err = service.createManifestFieldTx(tx, repository, actor, FieldDefinitionInput{
			Name:         "priority",
			DisplayName:  "Priority",
			ObjectScope:  "pull_request",
			FieldType:    "enum",
			EnumValues:   []string{"low", "high"},
			IsFilterable: true,
		}, "manifest-create-direct")
		return err
	}))

	loaded, found, err := loadManifestFieldTx(db, repository.GitHubRepositoryID, FieldDefinitionInput{
		Name:        "priority",
		ObjectScope: "pull_request",
	})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, created.ID, loaded.ID)

	var model database.FieldDefinition
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var err error
		model, err = service.applyManifestFieldTx(ctx, tx, repository, actor, FieldDefinitionInput{
			Name:        "summary",
			ObjectScope: "issue",
			FieldType:   "text",
		}, database.FieldDefinition{}, false, "manifest-apply-create")
		return err
	}))
	require.Equal(t, "summary", model.Name)

	var updated database.FieldDefinition
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var err error
		updated, err = service.applyManifestFieldTx(ctx, tx, repository, actor, FieldDefinitionInput{
			Name:         "priority",
			DisplayName:  "Priority v2",
			ObjectScope:  "pull_request",
			FieldType:    "enum",
			EnumValues:   []string{"low", "high"},
			IsFilterable: true,
		}, loaded, true, "manifest-apply-update")
		return err
	}))
	require.Equal(t, "Priority v2", updated.DisplayName)

	require.NoError(t, service.ensureManifestEnumCompatibility(ctx, database.FieldDefinition{
		FieldType: "text",
	}, FieldDefinitionInput{EnumValues: []string{"ignored"}}))

	textField, err := service.CreateFieldDefinition(ctx, actor, "acme", "widgets", FieldDefinitionInput{
		Name:        "intent",
		ObjectScope: "pull_request",
		FieldType:   "text",
	}, "")
	require.NoError(t, err)
	require.NoError(t, db.Create(&database.FieldValue{
		FieldDefinitionID:  textField.ID,
		GitHubRepositoryID: repository.GitHubRepositoryID,
		RepositoryOwner:    repository.Owner,
		RepositoryName:     repository.Name,
		TargetType:         "pull_request",
		ObjectNumber:       intPtr(22),
		TargetKey:          objectTargetKey(repository.GitHubRepositoryID, "pull_request", 22),
		StringValue:        stringPtr("auth"),
		UpdatedBy:          actor.ID,
	}).Error)

	annotations, err := service.GetAnnotations(ctx, repository.Owner, repository.Name, "pull_request", 22, nil)
	require.NoError(t, err)
	require.Equal(t, "auth", annotations.Annotations["intent"])

	intValue, err := annotationIntegerValue(float64(3))
	require.NoError(t, err)
	require.EqualValues(t, 3, intValue)
	_, err = annotationIntegerValue(3.2)
	require.Error(t, err)
}

func TestPermissionRepositoryAndGrantHelperBranches(t *testing.T) {
	ctx := context.Background()
	service, db, server := newTestService(t)
	defer server.Close()

	repository, err := service.EnsureRepository(ctx, "acme", "widgets")
	require.NoError(t, err)
	require.Equal(t, int64(101), repository.GitHubRepositoryID)

	repository, err = service.EnsureRepository(ctx, "acme", "widgets")
	require.NoError(t, err)
	require.Equal(t, "widgets", repository.Name)

	badService := NewService(db, testMirrorClient{behavior: batchBehavior{fail: true}}, permissions.AllowAllChecker{}, service.indexer)
	_, err = badService.EnsureRepository(ctx, "acme", "widgets")
	require.Error(t, err)

	actor := permissionsActor()
	repo := database.RepositoryProjection{GitHubRepositoryID: 101, Owner: "acme", Name: "widgets"}

	plainService := NewService(db, service.ghreplica, checkerOnlyStub{}, service.indexer)
	require.ErrorIs(t, plainService.requireWrite(ctx, actor, repo), ErrForbidden)

	errService := NewService(db, service.ghreplica, checkerErrorStub{canWriteErr: errors.New("perm backend down")}, service.indexer)
	require.ErrorContains(t, errService.requireWrite(ctx, actor, repo), "perm backend down")

	resolveErrService := NewService(db, service.ghreplica, checkerErrorStub{resolveErr: errors.New("identity lookup failed")}, service.indexer)
	require.ErrorContains(t, resolveErrService.requireWrite(ctx, actor, repo), "identity lookup failed")

	zeroIdentityService := NewService(db, service.ghreplica, checkerErrorStub{identity: permissions.Identity{}}, service.indexer)
	require.ErrorIs(t, zeroIdentityService.requireWrite(ctx, actor, repo), ErrForbidden)

	noGrantService := NewService(db, service.ghreplica, checkerErrorStub{identity: permissions.Identity{GitHubUserID: 55, GitHubLogin: "alice"}}, service.indexer)
	require.ErrorIs(t, noGrantService.requireWrite(ctx, actor, repo), ErrForbidden)

	require.NoError(t, db.Create(&database.RepositoryAccessGrant{
		GitHubRepositoryID:    repo.GitHubRepositoryID,
		GitHubUserID:          55,
		GitHubLogin:           "alice",
		Role:                  repositoryAccessGrantRoleWriter,
		GrantedByGitHubUserID: 1,
		GrantedByGitHubLogin:  "bob",
	}).Error)
	require.NoError(t, noGrantService.requireWrite(ctx, actor, repo))

	require.NoError(t, db.Create(&database.RepositoryAccessGrant{
		GitHubRepositoryID:    repo.GitHubRepositoryID,
		GitHubUserID:          77,
		GitHubLogin:           "zoe",
		Role:                  repositoryAccessGrantRoleWriter,
		GrantedByGitHubUserID: 1,
		GrantedByGitHubLogin:  "bob",
	}).Error)

	grants, err := service.ListRepositoryAccessGrants(ctx, "acme", "widgets")
	require.NoError(t, err)
	require.Len(t, grants, 2)
	require.Equal(t, "alice", grants[0].GitHubLogin)
	require.Equal(t, "zoe", grants[1].GitHubLogin)

	_, err = badService.ListRepositoryAccessGrants(ctx, "acme", "widgets")
	require.Error(t, err)
}

func TestEventMirrorAndConflictHelperBranches(t *testing.T) {
	ctx := context.Background()
	service, db, server := newTestService(t)
	defer server.Close()

	repository := database.RepositoryProjection{GitHubRepositoryID: 101, Owner: "acme", Name: "widgets"}
	actor := permissionsActor()

	issueRef, err := service.resolveTarget(ctx, repository, "issue", 11, nil)
	require.NoError(t, err)
	require.Equal(t, objectTargetKey(repository.GitHubRepositoryID, "issue", 11), issueRef.TargetKey)

	pullRef, err := service.resolveTarget(ctx, repository, "pull_request", 22, nil)
	require.NoError(t, err)
	require.Equal(t, objectTargetKey(repository.GitHubRepositoryID, "pull_request", 22), pullRef.TargetKey)

	_, err = service.resolveTarget(ctx, repository, "group", 1, nil)
	require.Error(t, err)

	badService := NewService(db, testMirrorClient{behavior: batchBehavior{fail: true}}, permissions.AllowAllChecker{}, service.indexer)
	_, err = badService.resolveTarget(ctx, repository, "issue", 11, nil)
	require.Error(t, err)

	emptySummaries, err := service.mirrorObjectSummaries(ctx, repository.GitHubRepositoryID, mirrorObjectRefsForMembers([]database.GroupMember{{ObjectType: "group", ObjectNumber: 1}}))
	require.NoError(t, err)
	require.Empty(t, emptySummaries)

	summaries, err := service.mirrorObjectSummaries(ctx, repository.GitHubRepositoryID, mirrorObjectRefsForMembers([]database.GroupMember{
		{ObjectType: "issue", ObjectNumber: 11},
		{ObjectType: "pull_request", ObjectNumber: 22},
	}))
	require.NoError(t, err)
	require.Len(t, summaries, 2)

	notFoundMember, owner, found, err := lookupGroupMemberConflictTx(db, repository.GitHubRepositoryID, "issue", 999)
	require.NoError(t, err)
	require.False(t, found)
	require.Zero(t, notFoundMember.ID)
	require.Zero(t, owner.ID)

	group := database.Group{
		PublicID:           "brisk-finch-p2m4",
		GitHubRepositoryID: repository.GitHubRepositoryID,
		RepositoryOwner:    repository.Owner,
		RepositoryName:     repository.Name,
		Kind:               "mixed",
		Title:              "Conflicts",
		Status:             "open",
		CreatedBy:          actor.ID,
		UpdatedBy:          actor.ID,
		RowVersion:         1,
	}
	require.NoError(t, db.Create(&group).Error)
	member := database.GroupMember{
		GroupID:            group.ID,
		GitHubRepositoryID: repository.GitHubRepositoryID,
		ObjectType:         "issue",
		ObjectNumber:       11,
		TargetKey:          objectTargetKey(repository.GitHubRepositoryID, "issue", 11),
		AddedBy:            actor.ID,
		AddedAt:            time.Now().UTC(),
	}
	require.NoError(t, db.Create(&member).Error)

	foundMember, foundOwner, found, err := lookupGroupMemberConflictTx(db, repository.GitHubRepositoryID, "issue", 11)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, group.ID, foundMember.GroupID)
	require.Equal(t, group.PublicID, foundOwner.PublicID)

	conflictErr := service.translateGroupMemberConflictTx(db, group, database.GroupMember{
		GitHubRepositoryID: repository.GitHubRepositoryID,
		ObjectType:         "pull_request",
		ObjectNumber:       999,
	})
	var fail *FailError
	require.ErrorAs(t, conflictErr, &fail)
	require.Equal(t, "group member already exists", fail.Message)

	noSchemaDB, err := gorm.Open(sqlite.Open("file:"+t.Name()+"-noschema?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	tx := noSchemaDB.Begin()
	require.NoError(t, tx.Error)
	_, err = service.insertEventWithSequenceRetry(tx, eventInput{
		RepositoryID:  101,
		AggregateType: "group",
		AggregateKey:  "group:test",
		EventType:     "group.updated",
		Actor:         actor,
	}, nil, nil)
	require.Error(t, err)
	require.Error(t, insertEventRefs(tx, 1, []eventRefInput{{Role: "member", Type: "issue", Key: "repo:101:issue:11"}}))
	require.NoError(t, tx.Rollback().Error)
}

func TestPostgresEventHelperBranches(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)

	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)

	mock.ExpectBegin()
	tx := db.Begin()
	require.NoError(t, tx.Error)

	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtext\(\$\d+\), hashtext\(\$\d+\)\)`).
		WithArgs("group", "group:test").
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, lockEventAggregateTx(tx, "group", "group:test"))

	mock.ExpectRollback()
	require.NoError(t, tx.Rollback().Error)
	mock.ExpectClose()
	require.NoError(t, sqlDB.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCommentSyncLowLevelHelperBranches(t *testing.T) {
	ctx := context.Background()
	db := openCommentSyncTestDB(t)
	group := seedCommentSyncGroup(t, db)
	dispatcher := &commentSyncDispatcherStub{}
	_, client := newTestGitHubCommentClient(t)
	syncService := NewCommentSyncService(db, testMirrorClient{}, client, dispatcher)

	loadedGroup, members, existing, err := syncService.loadGroupCommentState(db, group.ID)
	require.NoError(t, err)
	require.Equal(t, group.ID, loadedGroup.ID)
	require.Len(t, members, 2)
	require.Empty(t, existing)

	err = syncService.createCommentSyncTarget(db, database.GroupMember{
		GroupID:            group.ID,
		GitHubRepositoryID: group.GitHubRepositoryID,
		ObjectType:         "issue",
		ObjectNumber:       33,
		TargetKey:          objectTargetKey(group.GitHubRepositoryID, "issue", 33),
	}, time.Now().UTC(), time.Now().UTC().Add(time.Second))
	require.NoError(t, err)

	var created database.GroupCommentSyncTarget
	require.NoError(t, db.Where("group_id = ? AND object_number = ?", group.ID, 33).First(&created).Error)
	require.Equal(t, 1, created.DesiredRevision)

	require.NoError(t, syncService.updateCommentSyncTarget(db, created.ID, 2, true, created.TargetKey, time.Now().UTC(), time.Now().UTC().Add(time.Second)))
	require.NoError(t, db.First(&created, created.ID).Error)
	require.True(t, created.DesiredDeleted)
	require.Equal(t, 2, created.DesiredRevision)

	noSchemaDB, err := gorm.Open(sqlite.Open("file:"+t.Name()+"-comment-sync-noschema?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	noSchemaService := NewCommentSyncService(noSchemaDB, testMirrorClient{}, client, dispatcher)
	err = noSchemaService.createCommentSyncTarget(noSchemaDB, database.GroupMember{
		GroupID:            1,
		GitHubRepositoryID: 101,
		ObjectType:         "issue",
		ObjectNumber:       44,
		TargetKey:          objectTargetKey(101, "issue", 44),
	}, time.Now().UTC(), time.Now().UTC().Add(time.Second))
	require.Error(t, err)
	err = noSchemaService.updateCommentSyncTarget(noSchemaDB, 1, 2, false, objectTargetKey(101, "issue", 44), time.Now().UTC(), time.Now().UTC().Add(time.Second))
	require.Error(t, err)

	_, _, _, err = noSchemaService.loadGroupCommentState(noSchemaDB, 999)
	require.Error(t, err)

	affected, err := syncService.markAllCommentSyncTargetsDeleted(db, []database.GroupCommentSyncTarget{created}, time.Now().UTC(), time.Now().UTC().Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, 1, affected)

	var deleted database.GroupCommentSyncTarget
	require.NoError(t, db.First(&deleted, created.ID).Error)
	require.True(t, deleted.DesiredDeleted)

	require.NoError(t, syncService.Reconcile(ctx, deleted.ID, deleted.AppliedRevision, false))
}

func TestRepositoryAccessGrantErrorBranches(t *testing.T) {
	ctx := context.Background()
	service, db, server := newTestService(t)
	defer server.Close()

	badService := NewService(db, testMirrorClient{behavior: batchBehavior{fail: true}}, permissions.AllowAllChecker{}, service.indexer)
	_, err := badService.UpsertRepositoryAccessGrant(ctx, "acme", "widgets", RepositoryAccessGrantInput{
		GitHubUserID:          1,
		GitHubLogin:           "alice",
		GrantedByGitHubUserID: 2,
		GrantedByGitHubLogin:  "bob",
	})
	require.Error(t, err)

	require.Error(t, validateRepositoryAccessGrantInput(RepositoryAccessGrantInput{
		GitHubUserID:          1,
		GitHubLogin:           "",
		GrantedByGitHubUserID: 2,
		GrantedByGitHubLogin:  "bob",
	}))
	require.Error(t, validateRepositoryAccessGrantInput(RepositoryAccessGrantInput{
		GitHubUserID:          1,
		GitHubLogin:           "alice",
		GrantedByGitHubUserID: 0,
		GrantedByGitHubLogin:  "bob",
	}))
	require.Error(t, validateRepositoryAccessGrantInput(RepositoryAccessGrantInput{
		GitHubUserID:          1,
		GitHubLogin:           "alice",
		GrantedByGitHubUserID: 2,
		GrantedByGitHubLogin:  "",
	}))

	_, err = service.EnsureRepository(ctx, "acme", "widgets")
	require.NoError(t, err)
	require.NoError(t, db.Migrator().DropTable(&database.RepositoryAccessGrant{}))

	_, err = service.UpsertRepositoryAccessGrant(ctx, "acme", "widgets", RepositoryAccessGrantInput{
		GitHubUserID:          1,
		GitHubLogin:           "alice",
		GrantedByGitHubUserID: 2,
		GrantedByGitHubLogin:  "bob",
	})
	require.Error(t, err)

	_, err = service.ListRepositoryAccessGrants(ctx, "acme", "widgets")
	require.Error(t, err)
	require.Error(t, service.DeleteRepositoryAccessGrant(ctx, "acme", "widgets", 1))
	_, err = service.hasRepositoryWriteGrant(ctx, 101, 1)
	require.Error(t, err)
}

func permissionsActor() permissions.Actor {
	return permissions.Actor{Type: "user", ID: "tester"}
}

func boolPtr(value bool) *bool {
	return &value
}

func uintPtr(value uint) *uint {
	return &value
}
