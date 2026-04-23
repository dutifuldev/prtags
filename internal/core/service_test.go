package core

import (
	"context"
	"encoding/json"
	"errors"
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
	require.Nil(t, members[0].ObjectFreshness)
}

func TestGetGroupUsesCurrentCachedProjectionWithoutBatchFetch(t *testing.T) {
	ctx := context.Background()
	var batchCalls atomic.Int32
	service, _, server := newTestServiceWithBatchOptions(t, batchBehavior{
		delay: time.Second,
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
	require.Zero(t, batchCalls.Load())
	require.Equal(t, "Retry ACP turns safely", members[0].ObjectSummary.Title)
	require.Equal(t, "bob", members[0].ObjectSummary.AuthorLogin)
	require.NotNil(t, members[0].ObjectFreshness)
	require.Equal(t, "current", members[0].ObjectFreshness.State)
	require.Equal(t, "target_projection", members[0].ObjectFreshness.Source)
	require.Equal(t, "Auth retries are flaky", members[1].ObjectSummary.Title)
	require.Equal(t, "alice", members[1].ObjectSummary.AuthorLogin)
	require.NotNil(t, members[1].ObjectFreshness)
	require.Equal(t, "current", members[1].ObjectFreshness.State)
	require.Equal(t, "target_projection", members[1].ObjectFreshness.Source)
}

func TestGetGroupEnqueuesRefreshWhenProjectionMissing(t *testing.T) {
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
		ObjectNumber:       22,
		TargetKey:          objectTargetKey(group.GitHubRepositoryID, "pull_request", 22),
		AddedBy:            actor.ID,
		AddedAt:            time.Now().UTC(),
	}
	require.NoError(t, db.WithContext(ctx).Create(&member).Error)

	_, members, _, err := service.GetGroup(ctx, group.PublicID, GetGroupOptions{IncludeMetadata: true})
	require.NoError(t, err)
	require.Len(t, members, 1)
	require.Nil(t, members[0].ObjectSummary)
	require.NotNil(t, members[0].ObjectFreshness)
	require.Equal(t, "missing", members[0].ObjectFreshness.State)
	require.Equal(t, "missing_projection", members[0].ObjectFreshness.Source)

	var jobs []database.IndexJob
	require.NoError(t, db.WithContext(ctx).Where("kind = ?", indexJobKindTargetProjectionRefresh).Find(&jobs).Error)
	require.Len(t, jobs, 1)
	require.Equal(t, "pending", jobs[0].Status)

	drainIndexJobs(t, ctx, db, service.indexer)

	var projection database.TargetProjection
	require.NoError(t, db.WithContext(ctx).
		Where("github_repository_id = ? AND target_type = ? AND object_number = ?", group.GitHubRepositoryID, "pull_request", 22).
		First(&projection).Error)
	require.Equal(t, "Retry ACP turns safely", projection.Title)

	_, members, _, err = service.GetGroup(ctx, group.PublicID, GetGroupOptions{IncludeMetadata: true})
	require.NoError(t, err)
	require.Len(t, members, 1)
	require.NotNil(t, members[0].ObjectSummary)
	require.Equal(t, "Retry ACP turns safely", members[0].ObjectSummary.Title)
	require.Equal(t, "current", members[0].ObjectFreshness.State)
	require.Equal(t, "target_projection", members[0].ObjectFreshness.Source)
}

func TestGetGroupMarksStaleProjectionAndEnqueuesRefresh(t *testing.T) {
	ctx := context.Background()
	service, db, server := newTestService(t)
	defer server.Close()

	actor := permissions.Actor{Type: "user", ID: "tester"}
	group, err := service.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{
		Kind:  "pull_request",
		Title: "Auth work",
	}, "")
	require.NoError(t, err)

	_, err = service.AddGroupMember(ctx, actor, group.PublicID, "pull_request", 22, "")
	require.NoError(t, err)
	drainIndexJobs(t, ctx, db, service.indexer)

	staleAt := time.Now().UTC().Add(-2 * targetProjectionFreshnessTTL)
	require.NoError(t, db.WithContext(ctx).
		Model(&database.TargetProjection{}).
		Where("github_repository_id = ? AND target_type = ? AND object_number = ?", group.GitHubRepositoryID, "pull_request", 22).
		Update("fetched_at", staleAt).Error)

	_, members, _, err := service.GetGroup(ctx, group.PublicID, GetGroupOptions{IncludeMetadata: true})
	require.NoError(t, err)
	require.Len(t, members, 1)
	require.NotNil(t, members[0].ObjectSummary)
	require.Equal(t, "stale", members[0].ObjectFreshness.State)
	require.Equal(t, "target_projection", members[0].ObjectFreshness.Source)

	var jobs []database.IndexJob
	require.NoError(t, db.WithContext(ctx).
		Where("kind = ? AND status = ?", indexJobKindTargetProjectionRefresh, "pending").
		Find(&jobs).Error)
	require.Len(t, jobs, 1)
}

func TestAddGroupMemberDoesNotBlockOnProjectionFetch(t *testing.T) {
	ctx := context.Background()
	service, db, server := newTestServiceWithBatchOptions(t, batchBehavior{
		objectDelay: time.Second,
	})
	defer server.Close()

	actor := permissions.Actor{Type: "user", ID: "tester"}
	group, err := service.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{
		Kind:  "pull_request",
		Title: "Auth work",
	}, "")
	require.NoError(t, err)

	start := time.Now()
	member, err := service.AddGroupMember(ctx, actor, group.PublicID, "pull_request", 22, "")
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.Equal(t, 22, member.ObjectNumber)
	require.Less(t, elapsed, 250*time.Millisecond)

	var projectionCount int64
	require.NoError(t, db.WithContext(ctx).
		Model(&database.TargetProjection{}).
		Where("github_repository_id = ? AND target_type = ? AND object_number = ?", group.GitHubRepositoryID, "pull_request", 22).
		Count(&projectionCount).Error)
	require.Zero(t, projectionCount)

	var refreshJobs []database.IndexJob
	require.NoError(t, db.WithContext(ctx).Where("kind = ?", indexJobKindTargetProjectionRefresh).Find(&refreshJobs).Error)
	require.Len(t, refreshJobs, 1)

	drainIndexJobs(t, ctx, db, service.indexer)

	require.NoError(t, db.WithContext(ctx).
		Model(&database.TargetProjection{}).
		Where("github_repository_id = ? AND target_type = ? AND object_number = ?", group.GitHubRepositoryID, "pull_request", 22).
		Count(&projectionCount).Error)
	require.EqualValues(t, 1, projectionCount)
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

func newTestServiceWithBatchBehaviorAndChecker(t *testing.T, behavior batchBehavior, checker permissions.Checker) (*Service, *gorm.DB, *httptest.Server) {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, database.ApplyTestSchema(db))

	server := newTestGHReplicaServer(t, behavior)

	ghClient := ghreplica.NewClient(server.URL)
	indexer := NewIndexer(db, ghClient, embedding.NewLocalHashProvider("local-hash@1", database.EmbeddingDimensions))
	service := NewService(db, ghClient, checker, indexer)
	return service, db, server
}

func newTestGHReplicaServer(t *testing.T, behavior batchBehavior) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/github/repos/acme/widgets":
			_, _ = w.Write([]byte(`{
				"id": 101,
				"name": "widgets",
				"full_name": "acme/widgets",
				"html_url": "https://github.com/acme/widgets",
				"visibility": "public",
				"private": false,
				"owner": {"login": "acme"}
			}`))
		case "/v1/github/repos/acme/widgets/pulls/22":
			if behavior.objectDelay > 0 {
				time.Sleep(behavior.objectDelay)
			}
			_, _ = w.Write([]byte(`{
				"id": 2022,
				"number": 22,
				"title": "Retry ACP turns safely",
				"state": "open",
				"html_url": "https://github.com/acme/widgets/pull/22",
				"updated_at": "2026-04-16T12:00:00Z",
				"user": {"login": "bob"}
			}`))
		case "/v1/github/repos/acme/widgets/issues/11":
			if behavior.objectDelay > 0 {
				time.Sleep(behavior.objectDelay)
			}
			_, _ = w.Write([]byte(`{
				"id": 1111,
				"number": 11,
				"title": "Auth retries are flaky",
				"state": "open",
				"html_url": "https://github.com/acme/widgets/issues/11",
				"updated_at": "2026-04-16T12:00:00Z",
				"user": {"login": "alice"}
			}`))
		case "/v1/github-ext/repos/acme/widgets/objects/batch":
			if behavior.calls != nil {
				behavior.calls.Add(1)
			}
			if behavior.delay > 0 {
				time.Sleep(behavior.delay)
			}
			if behavior.fail {
				http.Error(w, `{"error":"batch unavailable"}`, http.StatusBadGateway)
				return
			}
			var input struct {
				Objects []ghreplica.BatchObjectRef `json:"objects"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&input))

			results := make([]map[string]any, 0, len(input.Objects))
			for _, object := range input.Objects {
				switch {
				case object.Type == "pull_request" && object.Number == 22:
					results = append(results, map[string]any{
						"type":   object.Type,
						"number": object.Number,
						"found":  true,
						"object": map[string]any{
							"id":         2022,
							"number":     22,
							"title":      "Retry ACP turns safely (batched)",
							"state":      "open",
							"html_url":   "https://github.com/acme/widgets/pull/22",
							"updated_at": "2026-04-16T13:00:00Z",
							"user":       map[string]any{"login": "bob"},
						},
					})
				case object.Type == "issue" && object.Number == 11:
					results = append(results, map[string]any{
						"type":   object.Type,
						"number": object.Number,
						"found":  true,
						"object": map[string]any{
							"id":         1111,
							"number":     11,
							"title":      "Auth retries are flaky (batched)",
							"state":      "open",
							"html_url":   "https://github.com/acme/widgets/issues/11",
							"updated_at": "2026-04-16T13:00:00Z",
							"user":       map[string]any{"login": "alice"},
						},
					})
				default:
					results = append(results, map[string]any{
						"type":   object.Type,
						"number": object.Number,
						"found":  false,
					})
				}
			}
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"results": results}))
		default:
			http.NotFound(w, r)
		}
	}))
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
