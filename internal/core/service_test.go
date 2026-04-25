package core

import (
	"context"
	crand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dutifuldev/prtags/internal/database"
	"github.com/dutifuldev/prtags/internal/embedding"
	ghreplica "github.com/dutifuldev/prtags/internal/ghreplica"
	"github.com/dutifuldev/prtags/internal/permissions"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type grantTestChecker struct {
	allowed  bool
	identity permissions.Identity
}

func (c grantTestChecker) CanWrite(context.Context, permissions.Actor, string, string) (bool, error) {
	return c.allowed, nil
}

func (c grantTestChecker) ResolveIdentity(context.Context, permissions.Actor) (permissions.Identity, error) {
	return c.identity, nil
}

func TestImportManifestRejectsFieldTypeChange(t *testing.T) {
	ctx := context.Background()
	service, _, server := newTestService(t)
	defer server.Close()

	actor := permissions.Actor{Type: "user", ID: "tester"}
	_, err := service.CreateFieldDefinition(ctx, actor, "acme", "widgets", FieldDefinitionInput{
		Name:        "intent",
		ObjectScope: "pull_request",
		FieldType:   "text",
	}, "")
	require.NoError(t, err)

	_, err = service.ImportManifest(ctx, actor, "acme", "widgets", Manifest{
		Version: "v1",
		Fields: []FieldDefinitionInput{{
			Name:        "intent",
			ObjectScope: "pull_request",
			FieldType:   "boolean",
		}},
	}, "")
	require.Error(t, err)

	var fail *FailError
	require.True(t, errors.As(err, &fail))
	require.Equal(t, 409, fail.StatusCode)
}

func TestImportManifestRejectsEnumRemovalThatOrphansStoredValues(t *testing.T) {
	ctx := context.Background()
	service, _, server := newTestService(t)
	defer server.Close()

	actor := permissions.Actor{Type: "user", ID: "tester"}
	_, err := service.CreateFieldDefinition(ctx, actor, "acme", "widgets", FieldDefinitionInput{
		Name:         "quality",
		ObjectScope:  "pull_request",
		FieldType:    "enum",
		EnumValues:   []string{"low", "high"},
		IsFilterable: true,
	}, "")
	require.NoError(t, err)

	_, err = service.SetAnnotations(ctx, actor, "acme", "widgets", "pull_request", 22, nil, map[string]any{
		"quality": "high",
	}, "")
	require.NoError(t, err)

	_, err = service.ImportManifest(ctx, actor, "acme", "widgets", Manifest{
		Version: "v1",
		Fields: []FieldDefinitionInput{{
			Name:         "quality",
			ObjectScope:  "pull_request",
			FieldType:    "enum",
			EnumValues:   []string{"low"},
			IsFilterable: true,
		}},
	}, "")
	require.Error(t, err)

	var fail *FailError
	require.True(t, errors.As(err, &fail))
	require.Equal(t, 409, fail.StatusCode)
}

func TestFilterTargetsUsesMatchingScopeAndSupportsMultiEnum(t *testing.T) {
	ctx := context.Background()
	service, _, server := newTestService(t)
	defer server.Close()

	actor := permissions.Actor{Type: "user", ID: "tester"}
	_, err := service.CreateFieldDefinition(ctx, actor, "acme", "widgets", FieldDefinitionInput{
		Name:         "priority",
		ObjectScope:  "issue",
		FieldType:    "enum",
		EnumValues:   []string{"low", "high"},
		IsFilterable: true,
	}, "")
	require.NoError(t, err)
	_, err = service.CreateFieldDefinition(ctx, actor, "acme", "widgets", FieldDefinitionInput{
		Name:         "priority",
		ObjectScope:  "pull_request",
		FieldType:    "enum",
		EnumValues:   []string{"low", "high"},
		IsFilterable: true,
	}, "")
	require.NoError(t, err)
	_, err = service.CreateFieldDefinition(ctx, actor, "acme", "widgets", FieldDefinitionInput{
		Name:         "labels",
		ObjectScope:  "pull_request",
		FieldType:    "multi_enum",
		EnumValues:   []string{"auth", "bug"},
		IsFilterable: true,
	}, "")
	require.NoError(t, err)

	_, err = service.SetAnnotations(ctx, actor, "acme", "widgets", "pull_request", 22, nil, map[string]any{
		"priority": "high",
		"labels":   []any{"bug", "auth"},
	}, "")
	require.NoError(t, err)

	priorityResults, err := service.FilterTargets(ctx, "acme", "widgets", "pull_request", "priority", "high")
	require.NoError(t, err)
	require.Len(t, priorityResults, 1)
	require.Equal(t, 22, priorityResults[0].ObjectNumber)

	labelResults, err := service.FilterTargets(ctx, "acme", "widgets", "pull_request", "labels", "auth")
	require.NoError(t, err)
	require.Len(t, labelResults, 1)
	require.Equal(t, 22, labelResults[0].ObjectNumber)
}

func TestSetAnnotationsRejectsFractionalInteger(t *testing.T) {
	ctx := context.Background()
	service, _, server := newTestService(t)
	defer server.Close()

	actor := permissions.Actor{Type: "user", ID: "tester"}
	_, err := service.CreateFieldDefinition(ctx, actor, "acme", "widgets", FieldDefinitionInput{
		Name:        "score",
		ObjectScope: "pull_request",
		FieldType:   "integer",
	}, "")
	require.NoError(t, err)

	_, err = service.SetAnnotations(ctx, actor, "acme", "widgets", "pull_request", 22, nil, map[string]any{
		"score": 3.7,
	}, "")
	require.Error(t, err)

	var fail *FailError
	require.True(t, errors.As(err, &fail))
	require.Equal(t, 400, fail.StatusCode)
}

func TestAnnotationSetResultJSONUsesSnakeCase(t *testing.T) {
	raw, err := json.Marshal(AnnotationSetResult{
		TargetKey:   "repo:101:pull_request:22",
		Annotations: map[string]any{"intent": "hello"},
	})
	require.NoError(t, err)
	require.Contains(t, string(raw), `"target_key"`)
	require.Contains(t, string(raw), `"annotations"`)
	require.NotContains(t, string(raw), `"TargetKey"`)
	require.NotContains(t, string(raw), `"Annotations"`)
}

func TestListGroupsReturnsMemberCounts(t *testing.T) {
	ctx := context.Background()
	service, _, server := newTestService(t)
	defer server.Close()

	actor := permissions.Actor{Type: "user", ID: "tester"}
	group, err := service.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{
		Kind:        "mixed",
		Title:       "Auth work",
		Description: "Track auth fixes",
	}, "")
	require.NoError(t, err)

	_, err = service.AddGroupMember(ctx, actor, group.PublicID, "pull_request", 22, "")
	require.NoError(t, err)
	_, err = service.AddGroupMember(ctx, actor, group.PublicID, "issue", 11, "")
	require.NoError(t, err)

	groups, err := service.ListGroups(ctx, "acme", "widgets")
	require.NoError(t, err)
	require.Len(t, groups, 1)
	require.Equal(t, 2, groups[0].MemberCount)
	require.Equal(t, 1, groups[0].MemberCounts["pull_request"])
	require.Equal(t, 1, groups[0].MemberCounts["issue"])
}

func TestGetGroupOmitsMetadataByDefault(t *testing.T) {
	ctx := context.Background()
	service, _, server := newTestService(t)
	defer server.Close()

	actor := permissions.Actor{Type: "user", ID: "tester"}
	group, err := service.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{
		Kind:        "mixed",
		Title:       "Auth work",
		Description: "Track auth fixes",
	}, "")
	require.NoError(t, err)

	_, err = service.AddGroupMember(ctx, actor, group.PublicID, "pull_request", 22, "")
	require.NoError(t, err)
	drainIndexJobs(t, ctx, service.db, service.indexer)

	_, members, _, err := service.GetGroup(ctx, group.PublicID, GetGroupOptions{})
	require.NoError(t, err)
	require.Len(t, members, 1)
	require.Nil(t, members[0].ObjectSummary)
}

func TestGetGroupLoadsMirrorMetadataDirectly(t *testing.T) {
	ctx := context.Background()
	var batchCalls atomic.Int32
	service, _, server := newTestServiceWithBatchOptions(t, batchBehavior{
		calls: &batchCalls,
	})
	defer server.Close()

	actor := permissions.Actor{Type: "user", ID: "tester"}
	group, err := service.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{
		Kind:        "mixed",
		Title:       "Auth work",
		Description: "Track auth fixes",
	}, "")
	require.NoError(t, err)

	_, err = service.AddGroupMember(ctx, actor, group.PublicID, "pull_request", 22, "")
	require.NoError(t, err)
	_, err = service.AddGroupMember(ctx, actor, group.PublicID, "issue", 11, "")
	require.NoError(t, err)
	drainIndexJobs(t, ctx, service.db, service.indexer)

	_, members, _, err := service.GetGroup(ctx, group.PublicID, GetGroupOptions{IncludeMetadata: true})
	require.NoError(t, err)
	require.Len(t, members, 2)
	require.Greater(t, batchCalls.Load(), int32(0))
	require.Equal(t, "Retry ACP turns safely", members[0].ObjectSummary.Title)
	require.Equal(t, "bob", members[0].ObjectSummary.AuthorLogin)
	require.Equal(t, "Auth retries are flaky", members[1].ObjectSummary.Title)
	require.Equal(t, "alice", members[1].ObjectSummary.AuthorLogin)
}

func TestGetGroupOmitsMissingMirrorMetadata(t *testing.T) {
	ctx := context.Background()
	service, db, server := newTestService(t)
	defer server.Close()

	actor := permissions.Actor{Type: "user", ID: "tester"}
	group, err := service.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{
		Kind:  "pull_request",
		Title: "Auth work",
	}, "")
	require.NoError(t, err)

	member := database.GroupMember{
		GroupID:            group.ID,
		GitHubRepositoryID: group.GitHubRepositoryID,
		ObjectType:         "pull_request",
		ObjectNumber:       999,
		TargetKey:          objectTargetKey(group.GitHubRepositoryID, "pull_request", 999),
		AddedBy:            actor.ID,
		AddedAt:            time.Now().UTC(),
	}
	require.NoError(t, db.WithContext(ctx).Create(&member).Error)

	_, members, _, err := service.GetGroup(ctx, group.PublicID, GetGroupOptions{IncludeMetadata: true})
	require.NoError(t, err)
	require.Len(t, members, 1)
	require.Nil(t, members[0].ObjectSummary)

	var refreshJobs []database.IndexJob
	require.NoError(t, db.WithContext(ctx).Where("kind = ?", "target_projection_refresh").Find(&refreshJobs).Error)
	require.Empty(t, refreshJobs)
}

func TestAddGroupMemberDoesNotCreateProjectionRefreshJobs(t *testing.T) {
	ctx := context.Background()
	service, db, server := newTestService(t)
	defer server.Close()

	actor := permissions.Actor{Type: "user", ID: "tester"}
	group, err := service.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{
		Kind:  "pull_request",
		Title: "Auth work",
	}, "")
	require.NoError(t, err)

	member, err := service.AddGroupMember(ctx, actor, group.PublicID, "pull_request", 22, "")
	require.NoError(t, err)
	require.Equal(t, 22, member.ObjectNumber)

	var refreshJobs []database.IndexJob
	require.NoError(t, db.WithContext(ctx).Where("kind = ?", "target_projection_refresh").Find(&refreshJobs).Error)
	require.Empty(t, refreshJobs)
}

func TestAddGroupMemberRejectsMissingMirrorTarget(t *testing.T) {
	ctx := context.Background()
	service, db, server := newTestService(t)
	defer server.Close()

	actor := permissions.Actor{Type: "user", ID: "tester"}
	group, err := service.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{
		Kind:  "pull_request",
		Title: "Auth work",
	}, "")
	require.NoError(t, err)

	_, err = service.AddGroupMember(ctx, actor, group.PublicID, "pull_request", 999, "")
	require.ErrorIs(t, err, ErrNotFound)

	var count int64
	require.NoError(t, db.WithContext(ctx).Model(&database.GroupMember{}).Where("group_id = ?", group.ID).Count(&count).Error)
	require.Zero(t, count)
}

func TestAddGroupMemberRejectsTargetAlreadyOwnedByAnotherGroup(t *testing.T) {
	ctx := context.Background()
	service, db, server := newTestService(t)
	defer server.Close()

	actor := permissions.Actor{Type: "user", ID: "tester"}
	first, err := service.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{
		Kind:  "pull_request",
		Title: "First group",
	}, "")
	require.NoError(t, err)
	second, err := service.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{
		Kind:  "pull_request",
		Title: "Second group",
	}, "")
	require.NoError(t, err)

	_, err = service.AddGroupMember(ctx, actor, first.PublicID, "pull_request", 22, "")
	require.NoError(t, err)

	_, err = service.AddGroupMember(ctx, actor, second.PublicID, "pull_request", 22, "")
	require.Error(t, err)

	var fail *FailError
	require.ErrorAs(t, err, &fail)
	require.Equal(t, 409, fail.StatusCode)
	require.Equal(t, "target already belongs to another group", fail.Message)
	require.Equal(t, groupMemberConflictDetails{GroupPublicID: first.PublicID}, fail.Data)

	var members []database.GroupMember
	require.NoError(t, db.WithContext(ctx).
		Where("github_repository_id = ? AND object_type = ? AND object_number = ?", first.GitHubRepositoryID, "pull_request", 22).
		Find(&members).Error)
	require.Len(t, members, 1)
	require.Equal(t, first.ID, members[0].GroupID)
}

func TestAddGroupMemberStillRejectsDuplicateInSameGroup(t *testing.T) {
	ctx := context.Background()
	service, _, server := newTestService(t)
	defer server.Close()

	actor := permissions.Actor{Type: "user", ID: "tester"}
	group, err := service.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{
		Kind:  "pull_request",
		Title: "Only group",
	}, "")
	require.NoError(t, err)

	_, err = service.AddGroupMember(ctx, actor, group.PublicID, "pull_request", 22, "")
	require.NoError(t, err)

	_, err = service.AddGroupMember(ctx, actor, group.PublicID, "pull_request", 22, "")
	require.Error(t, err)

	var fail *FailError
	require.ErrorAs(t, err, &fail)
	require.Equal(t, 409, fail.StatusCode)
	require.Equal(t, "group member already exists", fail.Message)
}

func TestRemoveGroupMemberReleasesTargetForAnotherGroup(t *testing.T) {
	ctx := context.Background()
	service, db, server := newTestService(t)
	defer server.Close()

	actor := permissions.Actor{Type: "user", ID: "tester"}
	first, err := service.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{
		Kind:  "pull_request",
		Title: "First group",
	}, "")
	require.NoError(t, err)
	second, err := service.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{
		Kind:  "pull_request",
		Title: "Second group",
	}, "")
	require.NoError(t, err)

	member, err := service.AddGroupMember(ctx, actor, first.PublicID, "pull_request", 22, "")
	require.NoError(t, err)
	require.NoError(t, service.RemoveGroupMember(ctx, actor, first.PublicID, member.ID, ""))

	_, err = service.AddGroupMember(ctx, actor, second.PublicID, "pull_request", 22, "")
	require.NoError(t, err)

	var members []database.GroupMember
	require.NoError(t, db.WithContext(ctx).
		Where("github_repository_id = ? AND object_type = ? AND object_number = ?", first.GitHubRepositoryID, "pull_request", 22).
		Order("group_id ASC").
		Find(&members).Error)
	require.Len(t, members, 1)
	require.Equal(t, second.ID, members[0].GroupID)
}

func TestCreateGroupFallsBackToRepositoryAccessGrant(t *testing.T) {
	ctx := context.Background()
	service, db, server := newTestServiceWithChecker(t, grantTestChecker{
		allowed: false,
		identity: permissions.Identity{
			GitHubUserID: 7937614,
			GitHubLogin:  "dutifulbob",
		},
	})
	defer server.Close()

	actor := permissions.Actor{Type: "github", ID: "token-digest", Token: "token"}

	_, err := service.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{
		Kind:  "mixed",
		Title: "Auth work",
	}, "")
	require.ErrorIs(t, err, ErrForbidden)

	grant, err := service.UpsertRepositoryAccessGrant(ctx, "acme", "widgets", RepositoryAccessGrantInput{
		GitHubUserID:          7937614,
		GitHubLogin:           "dutifulbob",
		Role:                  "writer",
		GrantedByGitHubUserID: 7937614,
		GrantedByGitHubLogin:  "dutifulbob",
	})
	require.NoError(t, err)
	require.EqualValues(t, 101, grant.GitHubRepositoryID)

	var stored database.RepositoryAccessGrant
	require.NoError(t, db.WithContext(ctx).First(&stored, grant.ID).Error)
	require.Equal(t, "writer", stored.Role)

	group, err := service.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{
		Kind:  "mixed",
		Title: "Auth work",
	}, "")
	require.NoError(t, err)
	require.Equal(t, "Auth work", group.Title)
}

func TestDeleteRepositoryAccessGrantRemovesFallbackWrite(t *testing.T) {
	ctx := context.Background()
	service, _, server := newTestServiceWithChecker(t, grantTestChecker{
		allowed: false,
		identity: permissions.Identity{
			GitHubUserID: 7937614,
			GitHubLogin:  "dutifulbob",
		},
	})
	defer server.Close()

	actor := permissions.Actor{Type: "github", ID: "token-digest", Token: "token"}

	_, err := service.UpsertRepositoryAccessGrant(ctx, "acme", "widgets", RepositoryAccessGrantInput{
		GitHubUserID:          7937614,
		GitHubLogin:           "dutifulbob",
		Role:                  "writer",
		GrantedByGitHubUserID: 7937614,
		GrantedByGitHubLogin:  "dutifulbob",
	})
	require.NoError(t, err)

	err = service.DeleteRepositoryAccessGrant(ctx, "acme", "widgets", 7937614)
	require.NoError(t, err)

	_, err = service.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{
		Kind:  "mixed",
		Title: "Auth work",
	}, "")
	require.ErrorIs(t, err, ErrForbidden)
}

func TestFieldLifecycleAndGroupUpdates(t *testing.T) {
	ctx := context.Background()
	service, _, server := newTestService(t)
	defer server.Close()

	actor := permissions.Actor{Type: "user", ID: "tester"}
	created, err := service.CreateFieldDefinition(ctx, actor, "acme", "widgets", FieldDefinitionInput{
		Name:         "priority",
		DisplayName:  "Priority",
		ObjectScope:  "pull_request",
		FieldType:    "enum",
		EnumValues:   []string{"low", "high"},
		IsFilterable: true,
	}, "")
	require.NoError(t, err)

	fields, err := service.ListFieldDefinitions(ctx, "acme", "widgets")
	require.NoError(t, err)
	require.Len(t, fields, 1)
	require.Equal(t, "priority", fields[0].Name)

	updated, err := service.UpdateFieldDefinition(ctx, actor, "acme", "widgets", created.ID, FieldDefinitionPatchInput{
		DisplayName:        stringPtr("Priority Score"),
		ExpectedRowVersion: intPtr(1),
	}, "")
	require.NoError(t, err)
	require.Equal(t, "Priority Score", updated.DisplayName)
	require.Equal(t, 2, updated.RowVersion)

	archived, err := service.ArchiveFieldDefinition(ctx, actor, "acme", "widgets", created.ID, intPtr(2), "")
	require.NoError(t, err)
	require.NotNil(t, archived.ArchivedAt)
	require.Equal(t, 3, archived.RowVersion)

	exported, err := service.ExportManifest(ctx, "acme", "widgets")
	require.NoError(t, err)
	require.Len(t, exported.Fields, 1)
	require.Equal(t, "priority", exported.Fields[0].Name)

	group, err := service.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{
		Kind:        "mixed",
		Title:       "Original title",
		Description: "first",
	}, "")
	require.NoError(t, err)

	group, err = service.UpdateGroup(ctx, actor, group.PublicID, GroupPatchInput{
		Title:              stringPtr("Updated title"),
		Description:        stringPtr("updated description"),
		Status:             stringPtr("closed"),
		ExpectedRowVersion: intPtr(1),
	}, "")
	require.NoError(t, err)
	require.Equal(t, "Updated title", group.Title)
	require.Equal(t, "closed", group.Status)

	_, err = service.SyncGroupComments(ctx, actor, group.PublicID)
	require.Error(t, err)
	var fail *FailError
	require.ErrorAs(t, err, &fail)
	require.Equal(t, 503, fail.StatusCode)
	require.Equal(t, "github comment sync is not configured", fail.Message)
}

func TestGetAnnotationsForPullRequestIssueAndGroup(t *testing.T) {
	ctx := context.Background()
	service, _, server := newTestService(t)
	defer server.Close()

	actor := permissions.Actor{Type: "user", ID: "tester"}
	_, err := service.CreateFieldDefinition(ctx, actor, "acme", "widgets", FieldDefinitionInput{
		Name:        "intent",
		ObjectScope: "pull_request",
		FieldType:   "text",
	}, "")
	require.NoError(t, err)
	_, err = service.CreateFieldDefinition(ctx, actor, "acme", "widgets", FieldDefinitionInput{
		Name:        "severity",
		ObjectScope: "issue",
		FieldType:   "text",
	}, "")
	require.NoError(t, err)
	_, err = service.CreateFieldDefinition(ctx, actor, "acme", "widgets", FieldDefinitionInput{
		Name:        "theme",
		ObjectScope: "group",
		FieldType:   "text",
	}, "")
	require.NoError(t, err)

	group, err := service.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{
		Kind:  "mixed",
		Title: "Auth work",
	}, "")
	require.NoError(t, err)

	_, err = service.SetAnnotations(ctx, actor, "acme", "widgets", "pull_request", 22, nil, map[string]any{"intent": "retry auth"}, "")
	require.NoError(t, err)
	_, err = service.SetAnnotations(ctx, actor, "acme", "widgets", "issue", 11, nil, map[string]any{"severity": "critical"}, "")
	require.NoError(t, err)
	_, err = service.SetAnnotations(ctx, actor, "acme", "widgets", "group", 0, &group.ID, map[string]any{"theme": "reliability"}, "")
	require.NoError(t, err)

	pullAnnotations, err := service.GetAnnotations(ctx, "acme", "widgets", "pull_request", 22, nil)
	require.NoError(t, err)
	require.Equal(t, "retry auth", pullAnnotations.Annotations["intent"])

	issueAnnotations, err := service.GetAnnotations(ctx, "acme", "widgets", "issue", 11, nil)
	require.NoError(t, err)
	require.Equal(t, "critical", issueAnnotations.Annotations["severity"])

	groupAnnotations, err := service.GetAnnotations(ctx, "acme", "widgets", "group", 0, &group.ID)
	require.NoError(t, err)
	require.Equal(t, "reliability", groupAnnotations.Annotations["theme"])
}

func TestSearchTextAndSimilarReturnIndexedResults(t *testing.T) {
	ctx := context.Background()
	service, db, server := newTestService(t)
	defer server.Close()

	actor := permissions.Actor{Type: "user", ID: "tester"}
	_, err := service.CreateFieldDefinition(ctx, actor, "acme", "widgets", FieldDefinitionInput{
		Name:         "intent",
		ObjectScope:  "pull_request",
		FieldType:    "text",
		IsSearchable: true,
		IsVectorized: true,
	}, "")
	require.NoError(t, err)
	_, err = service.CreateFieldDefinition(ctx, actor, "acme", "widgets", FieldDefinitionInput{
		Name:         "severity",
		ObjectScope:  "issue",
		FieldType:    "text",
		IsSearchable: true,
		IsVectorized: true,
	}, "")
	require.NoError(t, err)
	_, err = service.CreateFieldDefinition(ctx, actor, "acme", "widgets", FieldDefinitionInput{
		Name:         "theme",
		ObjectScope:  "group",
		FieldType:    "text",
		IsSearchable: true,
		IsVectorized: true,
	}, "")
	require.NoError(t, err)

	group, err := service.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{
		Kind:  "mixed",
		Title: "Auth group",
	}, "")
	require.NoError(t, err)
	_, err = service.AddGroupMember(ctx, actor, group.PublicID, "pull_request", 22, "")
	require.NoError(t, err)
	_, err = service.AddGroupMember(ctx, actor, group.PublicID, "issue", 11, "")
	require.NoError(t, err)

	_, err = service.SetAnnotations(ctx, actor, "acme", "widgets", "pull_request", 22, nil, map[string]any{"intent": "retry auth safely"}, "")
	require.NoError(t, err)
	_, err = service.SetAnnotations(ctx, actor, "acme", "widgets", "issue", 11, nil, map[string]any{"severity": "critical auth outage"}, "")
	require.NoError(t, err)
	_, err = service.SetAnnotations(ctx, actor, "acme", "widgets", "group", 0, &group.ID, map[string]any{"theme": "auth recovery"}, "")
	require.NoError(t, err)

	drainIndexJobs(t, ctx, db, service.indexer)

	textResults, err := service.SearchText(ctx, "acme", "widgets", "auth", []string{"pull_request", "issue", "group"}, 10)
	require.NoError(t, err)
	require.Len(t, textResults, 3)

	similarResults, err := service.SearchSimilar(ctx, "acme", "widgets", "critical auth outage", []string{"pull_request", "issue", "group"}, 10)
	require.NoError(t, err)
	require.Len(t, similarResults, 3)
}

func TestListRepositoryAccessGrantsReturnsStoredGrants(t *testing.T) {
	ctx := context.Background()
	service, _, server := newTestServiceWithChecker(t, grantTestChecker{
		allowed: false,
		identity: permissions.Identity{
			GitHubUserID: 1,
			GitHubLogin:  "grantor",
		},
	})
	defer server.Close()

	_, err := service.UpsertRepositoryAccessGrant(ctx, "acme", "widgets", RepositoryAccessGrantInput{
		GitHubUserID:          2,
		GitHubLogin:           "writer",
		Role:                  "writer",
		GrantedByGitHubUserID: 1,
		GrantedByGitHubLogin:  "grantor",
	})
	require.NoError(t, err)

	grants, err := service.ListRepositoryAccessGrants(ctx, "acme", "widgets")
	require.NoError(t, err)
	require.Len(t, grants, 1)
	require.Equal(t, "writer", grants[0].GitHubLogin)
}

func TestFieldDefinitionLifecycleMethods(t *testing.T) {
	ctx := context.Background()
	service, db, server := newTestService(t)
	defer server.Close()

	actor := permissionsActor()
	textField, err := service.CreateFieldDefinition(ctx, actor, "acme", "widgets", FieldDefinitionInput{
		Name:         "intent",
		ObjectScope:  "pull_request",
		FieldType:    "text",
		IsSearchable: true,
	}, "")
	require.NoError(t, err)

	enumField, err := service.CreateFieldDefinition(ctx, actor, "acme", "widgets", FieldDefinitionInput{
		Name:         "severity",
		ObjectScope:  "pull_request",
		FieldType:    "enum",
		EnumValues:   []string{"low", "high"},
		IsFilterable: true,
	}, "")
	require.NoError(t, err)

	fields, err := service.ListFieldDefinitions(ctx, "acme", "widgets")
	require.NoError(t, err)
	require.Len(t, fields, 2)

	updated, err := service.UpdateFieldDefinition(ctx, actor, "acme", "widgets", enumField.ID, FieldDefinitionPatchInput{
		DisplayName:        stringPtr("Severity"),
		EnumValues:         &[]string{"low", "high", "critical"},
		IsRequired:         boolPtr(true),
		IsFilterable:       boolPtr(true),
		IsSearchable:       boolPtr(true),
		IsVectorized:       boolPtr(true),
		SortOrder:          intPtr(7),
		ExpectedRowVersion: intPtr(enumField.RowVersion),
	}, "")
	require.NoError(t, err)
	require.Equal(t, "Severity", updated.DisplayName)
	require.Equal(t, 7, updated.SortOrder)

	archived, err := service.ArchiveFieldDefinition(ctx, actor, "acme", "widgets", textField.ID, intPtr(textField.RowVersion), "")
	require.NoError(t, err)
	require.NotNil(t, archived.ArchivedAt)

	archivedAgain, err := service.ArchiveFieldDefinition(ctx, actor, "acme", "widgets", textField.ID, nil, "")
	require.NoError(t, err)
	require.NotNil(t, archivedAgain.ArchivedAt)

	manifest, err := service.ExportManifest(ctx, "acme", "widgets")
	require.NoError(t, err)
	require.Len(t, manifest.Fields, 2)
	_, err = service.ImportManifest(ctx, actor, "acme", "widgets", Manifest{}, "")
	require.Error(t, err)

	var eventCount int64
	require.NoError(t, db.WithContext(ctx).Model(&database.Event{}).Count(&eventCount).Error)
	require.Greater(t, eventCount, int64(0))
}

func TestGroupAnnotationAndFilterMethods(t *testing.T) {
	ctx := context.Background()
	service, db, server := newTestService(t)
	defer server.Close()

	actor := permissionsActor()
	for _, field := range []FieldDefinitionInput{
		{Name: "intent", ObjectScope: "pull_request", FieldType: "text"},
		{Name: "ready", ObjectScope: "pull_request", FieldType: "boolean", IsFilterable: true},
		{Name: "count", ObjectScope: "pull_request", FieldType: "integer", IsFilterable: true},
		{Name: "severity", ObjectScope: "pull_request", FieldType: "enum", EnumValues: []string{"low", "high"}, IsFilterable: true},
		{Name: "labels", ObjectScope: "pull_request", FieldType: "multi_enum", EnumValues: []string{"bug", "auth"}, IsFilterable: true},
		{Name: "theme", ObjectScope: "group", FieldType: "text"},
	} {
		_, err := service.CreateFieldDefinition(ctx, actor, "acme", "widgets", field, "")
		require.NoError(t, err)
	}

	group, err := service.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{Kind: "pull_request", Title: "Auth fixes"}, "")
	require.NoError(t, err)
	group, err = service.UpdateGroup(ctx, actor, group.PublicID, GroupPatchInput{
		Description:        stringPtr("Track auth fixes"),
		Status:             stringPtr("closed"),
		ExpectedRowVersion: intPtr(group.RowVersion),
	}, "")
	require.NoError(t, err)
	require.Equal(t, "closed", group.Status)

	member, err := service.AddGroupMember(ctx, actor, group.PublicID, "pull_request", 22, "")
	require.NoError(t, err)
	require.Equal(t, 22, member.ObjectNumber)
	err = service.RemoveGroupMember(ctx, actor, group.PublicID, 999, "")
	require.ErrorIs(t, err, ErrNotFound)

	annotations, err := service.SetAnnotations(ctx, actor, "acme", "widgets", "pull_request", 22, nil, map[string]any{
		"intent":   "retry auth safely",
		"ready":    true,
		"count":    int64(2),
		"severity": "high",
		"labels":   []any{"auth", "bug"},
	}, "")
	require.NoError(t, err)
	require.Equal(t, "high", annotations.Annotations["severity"])

	_, err = service.SetAnnotations(ctx, actor, "acme", "widgets", "group", 0, &group.ID, map[string]any{
		"theme": "reliability",
	}, "")
	require.NoError(t, err)

	objectAnnotations, err := service.GetAnnotations(ctx, "acme", "widgets", "pull_request", 22, nil)
	require.NoError(t, err)
	require.Equal(t, true, objectAnnotations.Annotations["ready"])
	_, err = service.SetAnnotations(ctx, actor, "acme", "widgets", "pull_request", 22, nil, map[string]any{"intent": nil}, "")
	require.NoError(t, err)
	objectAnnotations, err = service.GetAnnotations(ctx, "acme", "widgets", "pull_request", 22, nil)
	require.NoError(t, err)
	require.Nil(t, objectAnnotations.Annotations["intent"])
	groupAnnotations, err := service.GetAnnotations(ctx, "acme", "widgets", "group", 0, &group.ID)
	require.NoError(t, err)
	require.Equal(t, "reliability", groupAnnotations.Annotations["theme"])

	_, err = service.SetAnnotations(ctx, actor, "acme", "widgets", "pull_request", 22, nil, map[string]any{"unknown": "x"}, "")
	require.Error(t, err)
	_, err = service.SetAnnotations(ctx, actor, "acme", "widgets", "pull_request", 22, nil, map[string]any{}, "")
	require.Error(t, err)

	for _, tc := range []struct {
		field string
		value string
	}{
		{field: "ready", value: "true"},
		{field: "count", value: "2"},
		{field: "severity", value: "high"},
		{field: "labels", value: "bug"},
	} {
		results, err := service.FilterTargets(ctx, "acme", "widgets", "pull_request", tc.field, tc.value)
		require.NoError(t, err)
		require.NotEmpty(t, results)
	}
	_, err = service.FilterTargets(ctx, "acme", "widgets", "pull_request", "count", "bad")
	require.Error(t, err)

	statuses, err := service.ListGroupCommentSyncTargets(ctx, actor, "acme", "widgets")
	require.NoError(t, err)
	require.Empty(t, statuses)

	_, err = service.SyncGroupComments(ctx, actor, group.PublicID)
	var fail *FailError
	require.ErrorAs(t, err, &fail)
	require.Equal(t, 503, fail.StatusCode)

	require.NoError(t, service.RemoveGroupMember(ctx, actor, group.PublicID, member.ID, ""))

	var members int64
	require.NoError(t, db.WithContext(ctx).Model(&database.GroupMember{}).Where("group_id = ?", group.ID).Count(&members).Error)
	require.Zero(t, members)
}

func TestRepositoryAccessGrantLifecycleMethods(t *testing.T) {
	ctx := context.Background()
	service, _, server := newTestService(t)
	defer server.Close()

	_, err := service.UpsertRepositoryAccessGrant(ctx, "acme", "widgets", RepositoryAccessGrantInput{})
	require.Error(t, err)

	grant, err := service.UpsertRepositoryAccessGrant(ctx, "acme", "widgets", RepositoryAccessGrantInput{
		GitHubUserID:          2,
		GitHubLogin:           "writer",
		Role:                  "writer",
		GrantedByGitHubUserID: 1,
		GrantedByGitHubLogin:  "grantor",
	})
	require.NoError(t, err)
	require.Equal(t, "writer", grant.Role)

	allowed, err := service.hasRepositoryWriteGrant(ctx, 101, 2)
	require.NoError(t, err)
	require.True(t, allowed)

	require.NoError(t, service.DeleteRepositoryAccessGrant(ctx, "acme", "widgets", 2))
	allowed, err = service.hasRepositoryWriteGrant(ctx, 101, 2)
	require.NoError(t, err)
	require.False(t, allowed)
	require.ErrorIs(t, service.DeleteRepositoryAccessGrant(ctx, "acme", "widgets", 2), ErrNotFound)
}

func TestSyncGroupCommentsUsesCommentSyncService(t *testing.T) {
	ctx := context.Background()
	service, db, server := newTestService(t)
	defer server.Close()

	actor := permissionsActor()
	group, err := service.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{Kind: "mixed", Title: "Auth sync"}, "")
	require.NoError(t, err)
	_, err = service.AddGroupMember(ctx, actor, group.PublicID, "pull_request", 22, "")
	require.NoError(t, err)
	_, err = service.AddGroupMember(ctx, actor, group.PublicID, "issue", 11, "")
	require.NoError(t, err)

	dispatcher := &commentSyncDispatcherStub{}
	_, client := newTestGitHubCommentClient(t)
	commentSync := NewCommentSyncService(db, testMirrorClient{}, client, dispatcher)
	service.SetCommentSync(commentSync)

	result, err := service.SyncGroupComments(ctx, actor, group.PublicID)
	require.NoError(t, err)
	require.Equal(t, 2, result.SyncTargetCount)

	statuses, err := service.ListGroupCommentSyncTargets(ctx, actor, "acme", "widgets")
	require.NoError(t, err)
	require.NotEmpty(t, statuses)
}

func TestAccessGrantServiceMethods(t *testing.T) {
	ctx := context.Background()
	service, _, server := newTestService(t)
	defer server.Close()

	_, err := service.UpsertRepositoryAccessGrant(ctx, "acme", "widgets", RepositoryAccessGrantInput{})
	require.Error(t, err)

	grant, err := service.UpsertRepositoryAccessGrant(ctx, "acme", "widgets", RepositoryAccessGrantInput{
		GitHubUserID:          77,
		GitHubLogin:           "writer",
		Role:                  "",
		GrantedByGitHubUserID: 1,
		GrantedByGitHubLogin:  "grantor",
	})
	require.NoError(t, err)
	require.Equal(t, repositoryAccessGrantRoleWriter, grant.Role)

	grants, err := service.ListRepositoryAccessGrants(ctx, "acme", "widgets")
	require.NoError(t, err)
	require.Len(t, grants, 1)

	allowed, err := service.hasRepositoryWriteGrant(ctx, grant.GitHubRepositoryID, grant.GitHubUserID)
	require.NoError(t, err)
	require.True(t, allowed)

	require.NoError(t, service.DeleteRepositoryAccessGrant(ctx, "acme", "widgets", grant.GitHubUserID))
	allowed, err = service.hasRepositoryWriteGrant(ctx, grant.GitHubRepositoryID, grant.GitHubUserID)
	require.NoError(t, err)
	require.False(t, allowed)
	require.ErrorIs(t, service.DeleteRepositoryAccessGrant(ctx, "acme", "widgets", grant.GitHubUserID), ErrNotFound)
}

func TestServiceErrorBranches(t *testing.T) {
	ctx := context.Background()
	service, _, server := newTestService(t)
	defer server.Close()

	actor := permissionsActor()

	_, err := service.CreateFieldDefinition(ctx, actor, "acme", "widgets", FieldDefinitionInput{}, "")
	require.Error(t, err)

	_, err = service.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{}, "")
	require.Error(t, err)

	_, err = service.UpdateGroup(ctx, actor, "missing-group", GroupPatchInput{Title: stringPtr("Updated")}, "")
	require.ErrorIs(t, err, ErrNotFound)

	group, err := service.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{Kind: "pull_request", Title: "Auth"}, "")
	require.NoError(t, err)

	_, err = service.UpdateGroup(ctx, actor, group.PublicID, GroupPatchInput{Title: stringPtr(" ")}, "")
	require.Error(t, err)
	_, err = service.AddGroupMember(ctx, actor, group.PublicID, "issue", 11, "")
	require.Error(t, err)
	err = service.RemoveGroupMember(ctx, actor, group.PublicID, 99999, "")
	require.ErrorIs(t, err, ErrNotFound)

	_, err = service.UpdateFieldDefinition(ctx, actor, "acme", "widgets", 99999, FieldDefinitionPatchInput{DisplayName: stringPtr("Nope")}, "")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = service.ArchiveFieldDefinition(ctx, actor, "acme", "widgets", 99999, nil, "")
	require.ErrorIs(t, err, ErrNotFound)

	_, err = service.SyncGroupComments(ctx, actor, "missing-group")
	require.ErrorIs(t, err, ErrNotFound)

	_, err = service.UpsertRepositoryAccessGrant(ctx, "acme", "widgets", RepositoryAccessGrantInput{})
	require.Error(t, err)
	err = service.DeleteRepositoryAccessGrant(ctx, "acme", "widgets", 0)
	require.Error(t, err)
}

func TestServicePermissionAndAlreadyArchivedBranches(t *testing.T) {
	ctx := context.Background()
	deny := grantTestChecker{}
	service, db, server := newTestServiceWithChecker(t, deny)
	defer server.Close()

	actor := permissionsActor()
	now := time.Now().UTC()
	field := database.FieldDefinition{
		GitHubRepositoryID: 101,
		RepositoryOwner:    "acme",
		RepositoryName:     "widgets",
		Name:               "intent",
		DisplayName:        "Intent",
		ObjectScope:        "pull_request",
		FieldType:          "text",
		CreatedBy:          actor.ID,
		UpdatedBy:          actor.ID,
		RowVersion:         1,
	}
	require.NoError(t, db.Create(&field).Error)
	group := database.Group{
		PublicID:           "tender-robin-j7v2",
		GitHubRepositoryID: 101,
		RepositoryOwner:    "acme",
		RepositoryName:     "widgets",
		Kind:               "pull_request",
		Title:              "Permission denied group",
		Status:             "open",
		CreatedBy:          actor.ID,
		UpdatedBy:          actor.ID,
		RowVersion:         1,
	}
	require.NoError(t, db.Create(&group).Error)

	_, err := service.CreateFieldDefinition(ctx, actor, "acme", "widgets", FieldDefinitionInput{
		Name:        "severity",
		ObjectScope: "pull_request",
		FieldType:   "text",
	}, "")
	require.ErrorIs(t, err, ErrForbidden)

	_, err = service.UpdateFieldDefinition(ctx, actor, "acme", "widgets", field.ID, FieldDefinitionPatchInput{
		DisplayName: stringPtr("Intent v2"),
	}, "")
	require.ErrorIs(t, err, ErrForbidden)

	_, err = service.ArchiveFieldDefinition(ctx, actor, "acme", "widgets", field.ID, nil, "")
	require.ErrorIs(t, err, ErrForbidden)

	_, err = service.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{Kind: "pull_request", Title: "Denied"}, "")
	require.ErrorIs(t, err, ErrForbidden)

	_, err = service.UpdateGroup(ctx, actor, group.PublicID, GroupPatchInput{Title: stringPtr("Nope")}, "")
	require.ErrorIs(t, err, ErrForbidden)

	_, err = service.AddGroupMember(ctx, actor, group.PublicID, "pull_request", 22, "")
	require.ErrorIs(t, err, ErrForbidden)
	err = service.RemoveGroupMember(ctx, actor, group.PublicID, 1, "")
	require.ErrorIs(t, err, ErrForbidden)

	allowService, allowDB, allowServer := newTestService(t)
	defer allowServer.Close()
	archivedField := database.FieldDefinition{
		GitHubRepositoryID: 101,
		RepositoryOwner:    "acme",
		RepositoryName:     "widgets",
		Name:               "summary",
		DisplayName:        "Summary",
		ObjectScope:        "pull_request",
		FieldType:          "text",
		CreatedBy:          actor.ID,
		UpdatedBy:          actor.ID,
		ArchivedAt:         &now,
		RowVersion:         3,
	}
	require.NoError(t, allowDB.Create(&archivedField).Error)
	result, err := allowService.ArchiveFieldDefinition(ctx, actor, "acme", "widgets", archivedField.ID, intPtr(3), "")
	require.NoError(t, err)
	require.NotNil(t, result.ArchivedAt)
	require.Equal(t, 3, result.RowVersion)

	err = allowService.DeleteRepositoryAccessGrant(ctx, "acme", "widgets", 999)
	require.ErrorIs(t, err, ErrNotFound)
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func TestCreateGroupReturnsPublicIDGenerationError(t *testing.T) {
	ctx := context.Background()
	service, _, server := newTestService(t)
	defer server.Close()

	original := crand.Reader
	crand.Reader = errReader{}
	defer func() { crand.Reader = original }()

	_, err := service.CreateGroup(ctx, permissionsActor(), "acme", "widgets", GroupInput{
		Kind:  "mixed",
		Title: "Entropy failure",
	}, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "generate entropy")
}

func TestFieldAndManifestOuterMethodBranches(t *testing.T) {
	ctx := context.Background()
	service, _, server := newTestService(t)
	defer server.Close()

	actor := permissionsActor()

	created, err := service.CreateFieldDefinition(ctx, actor, "acme", "widgets", FieldDefinitionInput{
		Name:        "intent",
		ObjectScope: "pull_request",
		FieldType:   "text",
	}, "")
	require.NoError(t, err)

	_, err = service.CreateFieldDefinition(ctx, actor, "acme", "widgets", FieldDefinitionInput{
		Name:        "intent",
		ObjectScope: "pull_request",
		FieldType:   "text",
	}, "")
	require.Error(t, err)

	_, err = service.UpdateFieldDefinition(ctx, actor, "acme", "widgets", created.ID, FieldDefinitionPatchInput{
		DisplayName:        stringPtr("Intent"),
		ExpectedRowVersion: intPtr(999),
	}, "")
	require.Error(t, err)

	archived, err := service.ArchiveFieldDefinition(ctx, actor, "acme", "widgets", created.ID, intPtr(created.RowVersion), "")
	require.NoError(t, err)
	require.NotNil(t, archived.ArchivedAt)

	manifestFields, err := service.ImportManifest(ctx, actor, "acme", "widgets", Manifest{
		Version: "v1",
		Fields: []FieldDefinitionInput{
			{Name: "priority", ObjectScope: "pull_request", FieldType: "enum", EnumValues: []string{"low", "high"}, IsFilterable: true},
			{Name: "theme", ObjectScope: "group", FieldType: "text"},
		},
	}, "")
	require.NoError(t, err)
	require.Len(t, manifestFields, 2)

	_, err = service.ImportManifest(ctx, actor, "acme", "widgets", Manifest{}, "")
	require.Error(t, err)
	_, err = service.ImportManifest(ctx, actor, "acme", "widgets", Manifest{
		Version: "v1",
		Fields:  []FieldDefinitionInput{{Name: "", ObjectScope: "pull_request", FieldType: "text"}},
	}, "")
	require.Error(t, err)

	denyService, _, denyServer := newTestServiceWithChecker(t, grantTestChecker{})
	defer denyServer.Close()
	_, err = denyService.ImportManifest(ctx, actor, "acme", "widgets", Manifest{
		Version: "v1",
		Fields:  []FieldDefinitionInput{{Name: "intent", ObjectScope: "pull_request", FieldType: "text"}},
	}, "")
	require.ErrorIs(t, err, ErrForbidden)
}

func TestGetAnnotationsHelperBranches(t *testing.T) {
	ctx := context.Background()
	service, db, server := newTestService(t)
	defer server.Close()

	actor := permissionsActor()
	group, err := service.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{Kind: "mixed", Title: "Annotations"}, "")
	require.NoError(t, err)

	field, err := service.CreateFieldDefinition(ctx, actor, "acme", "widgets", FieldDefinitionInput{
		Name:        "theme",
		ObjectScope: "group",
		FieldType:   "text",
	}, "")
	require.NoError(t, err)
	require.NoError(t, db.Create(&database.FieldValue{
		FieldDefinitionID:  field.ID,
		GitHubRepositoryID: group.GitHubRepositoryID,
		RepositoryOwner:    group.RepositoryOwner,
		RepositoryName:     group.RepositoryName,
		TargetType:         "group",
		GroupID:            &group.ID,
		TargetKey:          groupTargetKey(group.PublicID),
		StringValue:        stringPtr("auth"),
		UpdatedBy:          actor.ID,
	}).Error)

	annotations, err := service.getAnnotationsForTarget(ctx, "group", group.GitHubRepositoryID, 0, &group.ID)
	require.NoError(t, err)
	require.Equal(t, "auth", annotations["theme"])

	_, err = service.getAnnotationsForTarget(ctx, "group", group.GitHubRepositoryID, 0, uintPtr(99999))
	require.Error(t, err)
}

func TestServiceBulkOuterErrorBranches(t *testing.T) {
	ctx := context.Background()
	service, db, server := newTestService(t)
	defer server.Close()

	badService := NewService(db, testMirrorClient{behavior: batchBehavior{fail: true}}, permissions.AllowAllChecker{}, service.indexer)
	actor := permissionsActor()

	_, err := badService.CreateFieldDefinition(ctx, actor, "acme", "widgets", FieldDefinitionInput{
		Name:        "intent",
		ObjectScope: "pull_request",
		FieldType:   "text",
	}, "")
	require.Error(t, err)
	_, err = badService.ListFieldDefinitions(ctx, "acme", "widgets")
	require.Error(t, err)
	_, err = badService.UpdateFieldDefinition(ctx, actor, "acme", "widgets", 1, FieldDefinitionPatchInput{DisplayName: stringPtr("Intent")}, "")
	require.Error(t, err)
	_, err = badService.ArchiveFieldDefinition(ctx, actor, "acme", "widgets", 1, nil, "")
	require.Error(t, err)
	_, err = badService.ExportManifest(ctx, "acme", "widgets")
	require.Error(t, err)
	_, err = badService.ImportManifest(ctx, actor, "acme", "widgets", Manifest{
		Version: "v1",
		Fields:  []FieldDefinitionInput{{Name: "intent", ObjectScope: "pull_request", FieldType: "text"}},
	}, "")
	require.Error(t, err)
	_, err = badService.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{Kind: "mixed", Title: "Broken"}, "")
	require.Error(t, err)
	_, err = badService.ListGroups(ctx, "acme", "widgets")
	require.Error(t, err)
	_, err = badService.SetAnnotations(ctx, actor, "acme", "widgets", "pull_request", 22, nil, map[string]any{"intent": "x"}, "")
	require.Error(t, err)
	_, err = badService.GetAnnotations(ctx, "acme", "widgets", "pull_request", 22, nil)
	require.Error(t, err)
	_, err = badService.FilterTargets(ctx, "acme", "widgets", "pull_request", "intent", "x")
	require.Error(t, err)

	group, err := service.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{Kind: "pull_request", Title: "Group"}, "")
	require.NoError(t, err)

	_, _, _, err = service.GetGroup(ctx, "missing-group", GetGroupOptions{})
	require.ErrorIs(t, err, ErrNotFound)
	_, err = service.AddGroupMember(ctx, actor, "missing-group", "pull_request", 22, "")
	require.ErrorIs(t, err, ErrNotFound)
	err = service.RemoveGroupMember(ctx, actor, "missing-group", 1, "")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = service.SyncGroupComments(ctx, actor, "missing-group")
	require.ErrorIs(t, err, ErrNotFound)

	_, err = service.UpdateGroup(ctx, actor, group.PublicID, GroupPatchInput{}, "")
	require.Error(t, err)
	_, err = service.UpdateGroup(ctx, actor, group.PublicID, GroupPatchInput{
		Title:              stringPtr("Updated"),
		ExpectedRowVersion: intPtr(999),
	}, "")
	require.Error(t, err)

	_, err = service.ListGroupCommentSyncTargets(ctx, actor, "missing", "repo")
	require.ErrorIs(t, err, ErrNotFound)
}

func newTestService(t *testing.T) (*Service, *gorm.DB, *httptest.Server) {
	return newTestServiceWithBatchOptions(t, batchBehavior{})
}

func newTestServiceWithChecker(t *testing.T, checker permissions.Checker) (*Service, *gorm.DB, *httptest.Server) {
	return newTestServiceWithBatchBehaviorAndChecker(t, batchBehavior{}, checker)
}

func newTestServiceWithBatchOptions(t *testing.T, behavior batchBehavior) (*Service, *gorm.DB, *httptest.Server) {
	return newTestServiceWithBatchBehaviorAndChecker(t, behavior, permissions.AllowAllChecker{})
}

type batchBehavior struct {
	fail        bool
	delay       time.Duration
	objectDelay time.Duration
	calls       *atomic.Int32
}

type testMirrorClient struct {
	behavior batchBehavior
}

func (c testMirrorClient) GetRepository(context.Context, string, string) (ghreplica.Repository, error) {
	if c.behavior.fail {
		return ghreplica.Repository{}, errors.New("mirror unavailable")
	}
	return ghreplica.Repository{
		ID:         101,
		Name:       "widgets",
		FullName:   "acme/widgets",
		HTMLURL:    "https://github.com/acme/widgets",
		Visibility: "public",
		Private:    false,
		Owner: struct {
			Login string `json:"login"`
		}{Login: "acme"},
	}, nil
}

func (c testMirrorClient) GetIssue(context.Context, string, string, int) (ghreplica.Issue, error) {
	if c.behavior.fail {
		return ghreplica.Issue{}, errors.New("mirror unavailable")
	}
	if c.behavior.objectDelay > 0 {
		time.Sleep(c.behavior.objectDelay)
	}
	return ghreplica.Issue{
		ID:        1111,
		Number:    11,
		Title:     "Auth retries are flaky",
		State:     "open",
		HTMLURL:   "https://github.com/acme/widgets/issues/11",
		UpdatedAt: time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC),
		User:      ghreplica.UserObject{Login: "alice"},
	}, nil
}

func (c testMirrorClient) GetPullRequest(context.Context, string, string, int) (ghreplica.PullRequest, error) {
	if c.behavior.fail {
		return ghreplica.PullRequest{}, errors.New("mirror unavailable")
	}
	if c.behavior.objectDelay > 0 {
		time.Sleep(c.behavior.objectDelay)
	}
	return ghreplica.PullRequest{
		ID:        2022,
		Number:    22,
		Title:     "Retry ACP turns safely",
		State:     "open",
		HTMLURL:   "https://github.com/acme/widgets/pull/22",
		UpdatedAt: time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC),
		User:      ghreplica.UserObject{Login: "bob"},
	}, nil
}

func (c testMirrorClient) BatchGetObjects(_ context.Context, _ int64, objects []ghreplica.ObjectRef) ([]ghreplica.ObjectResult, error) {
	if c.behavior.fail {
		return nil, errors.New("mirror unavailable")
	}
	if c.behavior.calls != nil {
		c.behavior.calls.Add(1)
	}
	if c.behavior.delay > 0 {
		time.Sleep(c.behavior.delay)
	}
	results := make([]ghreplica.ObjectResult, 0, len(objects))
	for _, object := range objects {
		result := ghreplica.ObjectResult{Type: object.Type, Number: object.Number}
		if object.Number == 999 || object.Number <= 0 {
			results = append(results, result)
			continue
		}
		summary := testObjectSummary(object.Type, object.Number)
		result.Found = true
		result.Summary = &summary
		results = append(results, result)
	}
	return results, nil
}

func testObjectSummary(objectType string, number int) ghreplica.ObjectSummary {
	title := fmt.Sprintf("%s %d", strings.ReplaceAll(objectType, "_", " "), number)
	author := "alice"
	if objectType == "pull_request" {
		author = "bob"
	}
	switch {
	case objectType == "issue" && number == 11:
		title = "Auth retries are flaky"
	case objectType == "pull_request" && number == 22:
		title = "Retry ACP turns safely"
	}
	return ghreplica.ObjectSummary{
		Title:       title,
		State:       "open",
		HTMLURL:     testObjectURL(objectType, number),
		AuthorLogin: author,
		UpdatedAt:   time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC),
	}
}

func testObjectURL(objectType string, number int) string {
	if objectType == "pull_request" {
		return fmt.Sprintf("https://github.com/acme/widgets/pull/%d", number)
	}
	return fmt.Sprintf("https://github.com/acme/widgets/issues/%d", number)
}

func newTestServiceWithBatchBehaviorAndChecker(t *testing.T, behavior batchBehavior, checker permissions.Checker) (*Service, *gorm.DB, *httptest.Server) {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, database.ApplyTestSchema(db))

	server := httptest.NewServer(http.NotFoundHandler())

	ghClient := testMirrorClient{behavior: behavior}
	indexer := NewIndexer(db, ghClient, embedding.NewLocalHashProvider("local-hash@1", database.EmbeddingDimensions))
	service := NewService(db, ghClient, checker, indexer)
	return service, db, server
}

func drainIndexJobs(t *testing.T, ctx context.Context, db *gorm.DB, indexer *Indexer) {
	t.Helper()
	for i := 0; i < 16; i++ {
		require.NoError(t, indexer.RunOnce(ctx))
		var pending int64
		require.NoError(t, db.WithContext(ctx).Model(&database.IndexJob{}).Where("status = ?", "pending").Count(&pending).Error)
		if pending == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("index jobs did not drain")
}

func stringPtr(value string) *string {
	return &value
}

func intPtr(value int) *int {
	return &value
}
