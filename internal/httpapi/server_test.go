package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dutifuldev/prtags/internal/core"
	"github.com/dutifuldev/prtags/internal/database"
	"github.com/dutifuldev/prtags/internal/embedding"
	ghreplica "github.com/dutifuldev/prtags/internal/ghreplica"
	"github.com/dutifuldev/prtags/internal/httpapi"
	"github.com/dutifuldev/prtags/internal/permissions"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type stubMirrorClient struct {
	fail bool
}

func (c stubMirrorClient) GetRepository(context.Context, string, string) (ghreplica.Repository, error) {
	if c.fail {
		return ghreplica.Repository{}, fmt.Errorf("mirror unavailable")
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

func (c stubMirrorClient) GetIssue(context.Context, string, string, int) (ghreplica.Issue, error) {
	if c.fail {
		return ghreplica.Issue{}, fmt.Errorf("mirror unavailable")
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

func (c stubMirrorClient) GetPullRequest(context.Context, string, string, int) (ghreplica.PullRequest, error) {
	if c.fail {
		return ghreplica.PullRequest{}, fmt.Errorf("mirror unavailable")
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

func (c stubMirrorClient) BatchGetObjects(_ context.Context, _ int64, objects []ghreplica.ObjectRef) ([]ghreplica.ObjectResult, error) {
	if c.fail {
		return nil, fmt.Errorf("mirror unavailable")
	}
	results := make([]ghreplica.ObjectResult, 0, len(objects))
	for _, object := range objects {
		result := ghreplica.ObjectResult{Type: object.Type, Number: object.Number}
		summary := ghreplica.ObjectSummary{
			Title:       fmt.Sprintf("%s %d", strings.ReplaceAll(object.Type, "_", " "), object.Number),
			State:       "open",
			AuthorLogin: "alice",
			UpdatedAt:   time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC),
		}
		if object.Type == "pull_request" {
			summary.Title = "Retry ACP turns safely"
			summary.AuthorLogin = "bob"
			summary.HTMLURL = fmt.Sprintf("https://github.com/acme/widgets/pull/%d", object.Number)
		} else {
			summary.Title = "Auth retries are flaky"
			summary.HTMLURL = fmt.Sprintf("https://github.com/acme/widgets/issues/%d", object.Number)
		}
		result.Found = true
		result.Summary = &summary
		results = append(results, result)
	}
	return results, nil
}

func TestAPIEndToEndFlow(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	ghClient := stubMirrorClient{}
	indexer := core.NewIndexer(db, ghClient, embedding.NewLocalHashProvider("local-hash@1", database.EmbeddingDimensions))
	service := core.NewService(db, ghClient, permissions.AllowAllChecker{}, indexer)
	server := httpapi.NewServer(db, service, true)

	postJSON(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/fields", map[string]any{
		"name":          "intent",
		"object_scope":  "pull_request",
		"field_type":    "text",
		"is_searchable": true,
		"is_vectorized": true,
	}, http.StatusCreated)
	postJSON(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/fields", map[string]any{
		"name":          "quality",
		"object_scope":  "pull_request",
		"field_type":    "enum",
		"enum_values":   []string{"low", "medium", "high"},
		"is_filterable": true,
	}, http.StatusCreated)
	postJSON(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/fields", map[string]any{
		"name":          "theme",
		"object_scope":  "group",
		"field_type":    "text",
		"is_searchable": true,
		"is_vectorized": true,
	}, http.StatusCreated)

	groupRaw := postJSON(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/groups", map[string]any{
		"kind":        "pull_request",
		"title":       "Auth reliability",
		"description": "Track auth retry fixes",
	}, http.StatusCreated)
	groupID := extractPathString(t, groupRaw, "data.id")

	postJSON(t, server.Echo(), http.MethodPost, fmt.Sprintf("/v1/groups/%s/members", groupID), map[string]any{
		"object_type":   "pull_request",
		"object_number": 22,
	}, http.StatusCreated)

	postJSON(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/pulls/22/annotations", map[string]any{
		"intent":  "Retry flaky auth ACP turns more safely",
		"quality": "high",
	}, http.StatusOK)

	postJSON(t, server.Echo(), http.MethodPost, fmt.Sprintf("/v1/groups/%s/annotations", groupID), map[string]any{
		"theme": "auth retry reliability",
	}, http.StatusOK)

	drainIndexJobs(t, ctx, db, indexer)

	var documents int64
	var embeddings int64
	require.NoError(t, db.WithContext(ctx).Model(&database.SearchDocument{}).Count(&documents).Error)
	require.NoError(t, db.WithContext(ctx).Model(&database.Embedding{}).Count(&embeddings).Error)
	require.GreaterOrEqual(t, documents, int64(2))
	require.GreaterOrEqual(t, embeddings, int64(2))

	filtered := postJSON(t, server.Echo(), http.MethodGet, "/v1/repos/acme/widgets/targets?target_type=pull_request&field=quality&value=high", nil, http.StatusOK)
	require.Contains(t, filtered, `"target_type":"pull_request"`)

	listedGroups := postJSON(t, server.Echo(), http.MethodGet, "/v1/repos/acme/widgets/groups", nil, http.StatusOK)
	require.Contains(t, listedGroups, `"member_count":1`)
	require.Contains(t, listedGroups, `"member_counts":{"pull_request":1}`)

	group := postJSON(t, server.Echo(), http.MethodGet, fmt.Sprintf("/v1/groups/%s", groupID), nil, http.StatusOK)
	require.Contains(t, group, `"Auth reliability"`)
	require.Contains(t, group, fmt.Sprintf(`"id":"%s"`, groupID))
	require.NotContains(t, group, `"object_summary"`)
	require.NotContains(t, group, `"object_summary_freshness"`)

	groupWithMetadata := postJSON(t, server.Echo(), http.MethodGet, fmt.Sprintf("/v1/groups/%s?include=metadata", groupID), nil, http.StatusOK)
	require.Contains(t, groupWithMetadata, `"object_summary"`)
	require.NotContains(t, groupWithMetadata, `"object_summary_freshness"`)
	require.Contains(t, groupWithMetadata, `"Retry ACP turns safely"`)
	require.Contains(t, groupWithMetadata, `"https://github.com/acme/widgets/pull/22"`)

	var events int64
	require.NoError(t, db.WithContext(ctx).Model(&database.Event{}).Count(&events).Error)
	require.Greater(t, events, int64(0))
}

func TestAPIAddGroupMemberRejectsTargetAlreadyInAnotherGroup(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	ghClient := stubMirrorClient{}
	indexer := core.NewIndexer(db, ghClient, embedding.NewLocalHashProvider("local-hash@1", database.EmbeddingDimensions))
	service := core.NewService(db, ghClient, permissions.AllowAllChecker{}, indexer)
	server := httpapi.NewServer(db, service, true)

	firstRaw := postJSON(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/groups", map[string]any{
		"kind":  "pull_request",
		"title": "First group",
	}, http.StatusCreated)
	firstID := extractPathString(t, firstRaw, "data.id")

	secondRaw := postJSON(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/groups", map[string]any{
		"kind":  "pull_request",
		"title": "Second group",
	}, http.StatusCreated)
	secondID := extractPathString(t, secondRaw, "data.id")

	postJSON(t, server.Echo(), http.MethodPost, fmt.Sprintf("/v1/groups/%s/members", firstID), map[string]any{
		"object_type":   "pull_request",
		"object_number": 22,
	}, http.StatusCreated)

	conflict := postJSON(t, server.Echo(), http.MethodPost, fmt.Sprintf("/v1/groups/%s/members", secondID), map[string]any{
		"object_type":   "pull_request",
		"object_number": 22,
	}, http.StatusConflict)

	require.Contains(t, conflict, `"message":"target already belongs to another group"`)
	require.Contains(t, conflict, fmt.Sprintf(`"group_public_id":"%s"`, firstID))

	var members []database.GroupMember
	require.NoError(t, db.WithContext(ctx).
		Where("github_repository_id = ? AND object_type = ? AND object_number = ?", int64(101), "pull_request", 22).
		Find(&members).Error)
	require.Len(t, members, 1)
}

func TestManifestImportExport(t *testing.T) {
	db := openTestDB(t)
	ghClient := stubMirrorClient{}
	indexer := core.NewIndexer(db, ghClient, embedding.NewLocalHashProvider("local-hash@1", database.EmbeddingDimensions))
	service := core.NewService(db, ghClient, permissions.AllowAllChecker{}, indexer)
	server := httpapi.NewServer(db, service, true)

	postJSON(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/fields/import", map[string]any{
		"version": "v1",
		"fields": []map[string]any{
			{
				"name":          "intent",
				"object_scope":  "pull_request",
				"field_type":    "text",
				"is_searchable": true,
				"is_vectorized": true,
			},
			{
				"name":          "priority",
				"object_scope":  "issue",
				"field_type":    "enum",
				"enum_values":   []string{"low", "high"},
				"is_filterable": true,
			},
		},
	}, http.StatusOK)

	exported := postJSON(t, server.Echo(), http.MethodGet, "/v1/repos/acme/widgets/fields/export", nil, http.StatusOK)
	require.Contains(t, exported, `"intent"`)
	require.Contains(t, exported, `"priority"`)
}

func TestAPIUpdateAndArchiveFlow(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	ghClient := stubMirrorClient{}
	indexer := core.NewIndexer(db, ghClient, embedding.NewLocalHashProvider("local-hash@1", database.EmbeddingDimensions))
	service := core.NewService(db, ghClient, permissions.AllowAllChecker{}, indexer)
	server := httpapi.NewServer(db, service, true)

	intentRaw := postJSON(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/fields", map[string]any{
		"name":          "intent",
		"object_scope":  "pull_request",
		"field_type":    "text",
		"is_searchable": true,
		"is_vectorized": true,
	}, http.StatusCreated)
	intentID := uint(extractPathNumber(t, intentRaw, "data.id"))

	groupRaw := postJSON(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/groups", map[string]any{
		"kind":        "pull_request",
		"title":       "Auth reliability",
		"description": "Track auth retry fixes",
	}, http.StatusCreated)
	groupID := extractPathString(t, groupRaw, "data.id")

	postJSON(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/pulls/22/annotations", map[string]any{
		"intent": "Retry flaky auth ACP turns more safely",
	}, http.StatusOK)

	postJSON(t, server.Echo(), http.MethodPatch, fmt.Sprintf("/v1/repos/acme/widgets/fields/%d", intentID), map[string]any{
		"display_name":         "Intent Summary",
		"expected_row_version": 1,
	}, http.StatusOK)

	var field database.FieldDefinition
	require.NoError(t, db.WithContext(ctx).First(&field, intentID).Error)
	require.Equal(t, "Intent Summary", field.DisplayName)
	require.Equal(t, 2, field.RowVersion)

	conflict := postJSON(t, server.Echo(), http.MethodPatch, fmt.Sprintf("/v1/repos/acme/widgets/fields/%d", intentID), map[string]any{
		"display_name":         "Should fail",
		"expected_row_version": 1,
	}, http.StatusConflict)
	require.Contains(t, conflict, `"row version conflict"`)

	postJSON(t, server.Echo(), http.MethodPatch, fmt.Sprintf("/v1/groups/%s", groupID), map[string]any{
		"title":                "Auth reliability updates",
		"status":               "in_progress",
		"expected_row_version": 1,
	}, http.StatusOK)

	var group database.Group
	require.NoError(t, db.WithContext(ctx).Where("public_id = ?", groupID).First(&group).Error)
	require.Equal(t, "Auth reliability updates", group.Title)
	require.Equal(t, "in_progress", group.Status)
	require.Equal(t, 2, group.RowVersion)

	postJSON(t, server.Echo(), http.MethodPost, fmt.Sprintf("/v1/repos/acme/widgets/fields/%d/archive", intentID), map[string]any{
		"expected_row_version": 2,
	}, http.StatusOK)

	require.NoError(t, db.WithContext(ctx).First(&field, intentID).Error)
	require.NotNil(t, field.ArchivedAt)
	require.Equal(t, 3, field.RowVersion)

	drainIndexJobs(t, ctx, db, indexer)
	var documents []database.SearchDocument
	require.NoError(t, db.WithContext(ctx).Where("target_key = ?", "repo:101:pull_request:22").Find(&documents).Error)
	require.Len(t, documents, 1)
	require.NotContains(t, documents[0].SearchText, "intent:")
}

func TestAPIListGroupCommentSyncTargets(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	ghClient := stubMirrorClient{fail: true}
	indexer := core.NewIndexer(db, ghClient, embedding.NewLocalHashProvider("local-hash@1", database.EmbeddingDimensions))
	service := core.NewService(db, ghClient, permissions.AllowAllChecker{}, indexer)
	server := httpapi.NewServer(db, service, true)

	repository := database.RepositoryProjection{
		GitHubRepositoryID: 101,
		Owner:              "acme",
		Name:               "widgets",
		FullName:           "acme/widgets",
		HTMLURL:            "https://github.com/acme/widgets",
		FetchedAt:          time.Now().UTC(),
	}
	require.NoError(t, db.WithContext(ctx).Create(&repository).Error)

	group := database.Group{
		PublicID:           "steady-otter-k4m2",
		GitHubRepositoryID: repository.GitHubRepositoryID,
		RepositoryOwner:    repository.Owner,
		RepositoryName:     repository.Name,
		Kind:               "mixed",
		Title:              "Auth reliability",
		Status:             "open",
		CreatedBy:          "tester",
		UpdatedBy:          "tester",
		RowVersion:         1,
	}
	require.NoError(t, db.WithContext(ctx).Create(&group).Error)
	require.NoError(t, db.WithContext(ctx).Create(&database.GroupCommentSyncTarget{
		GitHubRepositoryID: group.GitHubRepositoryID,
		GroupID:            group.ID,
		ObjectType:         "pull_request",
		ObjectNumber:       22,
		TargetKey:          "repo:101:pull_request:22",
		DesiredRevision:    3,
		AppliedRevision:    1,
		LastErrorKind:      "permission_denied",
		LastError:          "Resource not accessible by integration",
		LastErrorAt:        timePtr(time.Now().UTC()),
	}).Error)

	raw := postJSON(t, server.Echo(), http.MethodGet, "/v1/repos/acme/widgets/group-comment-sync-targets", nil, http.StatusOK)
	require.Contains(t, raw, `"group_id":"`+group.PublicID+`"`)
	require.Contains(t, raw, `"group_title":"Auth reliability"`)
	require.Contains(t, raw, `"object_type":"pull_request"`)
	require.Contains(t, raw, `"object_number":22`)
	require.Contains(t, raw, `"state":"failed"`)
	require.Contains(t, raw, `"last_error_kind":"permission_denied"`)
}

func TestAPIHandlerErrorBranches(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	ghClient := stubMirrorClient{}
	indexer := core.NewIndexer(db, ghClient, embedding.NewLocalHashProvider("local-hash@1", database.EmbeddingDimensions))
	service := core.NewService(db, ghClient, permissions.AllowAllChecker{}, indexer)
	server := httpapi.NewServer(db, service, true)

	postJSON(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/fields", map[string]any{
		"name":         "intent",
		"object_scope": "pull_request",
		"field_type":   "text",
	}, http.StatusCreated)
	groupRaw := postJSON(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/groups", map[string]any{
		"kind":  "pull_request",
		"title": "Auth reliability",
	}, http.StatusCreated)
	groupID := extractPathString(t, groupRaw, "data.id")
	memberRaw := postJSON(t, server.Echo(), http.MethodPost, fmt.Sprintf("/v1/groups/%s/members", groupID), map[string]any{
		"object_type":   "pull_request",
		"object_number": 22,
	}, http.StatusCreated)
	memberID := extractPathNumber(t, memberRaw, "data.id")

	badField := postJSON(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/fields", map[string]any{}, http.StatusBadRequest)
	require.Contains(t, badField, `"field name is required"`)

	notFoundArchive := postJSON(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/fields/999/archive", map[string]any{}, http.StatusNotFound)
	require.Contains(t, notFoundArchive, `"not found"`)

	badImport := postJSON(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/fields/import", map[string]any{"version": "v1"}, http.StatusBadRequest)
	require.Contains(t, badImport, `"manifest has no fields"`)

	badGroup := postJSON(t, server.Echo(), http.MethodPatch, fmt.Sprintf("/v1/groups/%s", groupID), map[string]any{}, http.StatusBadRequest)
	require.Contains(t, badGroup, `"no group updates provided"`)

	removeOK := postJSON(t, server.Echo(), http.MethodDelete, fmt.Sprintf("/v1/groups/%s/members/%d", groupID, memberID), nil, http.StatusOK)
	require.Contains(t, removeOK, `"success"`)

	removeMissing := postJSON(t, server.Echo(), http.MethodDelete, fmt.Sprintf("/v1/groups/%s/members/%d", groupID, memberID), nil, http.StatusNotFound)
	require.Contains(t, removeMissing, `"not found"`)

	search := postJSON(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/search/text", map[string]any{
		"query": "",
	}, http.StatusOK)
	require.Contains(t, search, `"status":"success"`)

	require.NoError(t, db.WithContext(ctx).Model(&database.Event{}).Where("aggregate_type = ?", "group").Count(new(int64)).Error)
}

func TestAPIAdditionalHandlerBranches(t *testing.T) {
	db := openTestDB(t)
	ghClient := stubMirrorClient{}
	indexer := core.NewIndexer(db, ghClient, embedding.NewLocalHashProvider("local-hash@1", database.EmbeddingDimensions))
	service := core.NewService(db, ghClient, permissions.AllowAllChecker{}, indexer)
	server := httpapi.NewServer(db, service, true)

	fieldRaw := postJSON(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/fields", map[string]any{
		"name":         "theme",
		"object_scope": "group",
		"field_type":   "text",
	}, http.StatusCreated)
	fieldID := extractPathNumber(t, fieldRaw, "data.id")
	groupRaw := postJSON(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/groups", map[string]any{
		"kind":  "mixed",
		"title": "Auth reliability",
	}, http.StatusCreated)
	groupID := extractPathString(t, groupRaw, "data.id")

	list := postJSON(t, server.Echo(), http.MethodGet, "/v1/repos/acme/widgets/fields", nil, http.StatusOK)
	require.Contains(t, list, `"theme"`)
	exported := postJSON(t, server.Echo(), http.MethodGet, "/v1/repos/acme/widgets/fields/export", nil, http.StatusOK)
	require.Contains(t, exported, `"version":"v1"`)

	updated := postJSON(t, server.Echo(), http.MethodPatch, "/v1/repos/acme/widgets/fields/"+fmt.Sprint(fieldID), map[string]any{
		"display_name":         "Theme",
		"expected_row_version": 1,
	}, http.StatusOK)
	require.Contains(t, updated, `"Theme"`)

	gotGroup := postJSON(t, server.Echo(), http.MethodGet, "/v1/groups/"+groupID, nil, http.StatusOK)
	require.Contains(t, gotGroup, groupID)

	groupAnnotations := postJSON(t, server.Echo(), http.MethodPost, "/v1/groups/"+groupID+"/annotations", map[string]any{
		"theme": "reliability",
	}, http.StatusOK)
	require.Contains(t, groupAnnotations, `"theme":"reliability"`)
	fetchedGroupAnnotations := postJSON(t, server.Echo(), http.MethodGet, "/v1/groups/"+groupID+"/annotations", nil, http.StatusOK)
	require.Contains(t, fetchedGroupAnnotations, `"theme":"reliability"`)

	memberRaw := postJSON(t, server.Echo(), http.MethodPost, "/v1/groups/"+groupID+"/members", map[string]any{
		"object_type":   "pull_request",
		"object_number": 22,
	}, http.StatusCreated)
	require.Contains(t, memberRaw, `"object_number":22`)

	annotations := postJSON(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/pulls/22/annotations", map[string]any{
		"theme": nil,
	}, http.StatusBadRequest)
	require.Contains(t, annotations, `"unknown field"`)
}

func TestAPIAnnotationSearchAndMembershipHandlers(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	ghClient := stubMirrorClient{}
	indexer := core.NewIndexer(db, ghClient, embedding.NewLocalHashProvider("local-hash@1", database.EmbeddingDimensions))
	service := core.NewService(db, ghClient, permissions.AllowAllChecker{}, indexer)
	server := httpapi.NewServer(db, service, true)

	postJSON(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/fields", map[string]any{
		"name":          "intent",
		"object_scope":  "pull_request",
		"field_type":    "text",
		"is_searchable": true,
		"is_vectorized": true,
	}, http.StatusCreated)
	postJSON(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/fields", map[string]any{
		"name":          "severity",
		"object_scope":  "issue",
		"field_type":    "text",
		"is_searchable": true,
		"is_vectorized": true,
	}, http.StatusCreated)
	postJSON(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/fields", map[string]any{
		"name":          "theme",
		"object_scope":  "group",
		"field_type":    "text",
		"is_searchable": true,
		"is_vectorized": true,
	}, http.StatusCreated)

	groupRaw := postJSON(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/groups", map[string]any{
		"kind":  "mixed",
		"title": "Search coverage group",
	}, http.StatusCreated)
	groupID := extractPathString(t, groupRaw, "data.id")

	prRaw := postJSON(t, server.Echo(), http.MethodPost, "/v1/groups/"+groupID+"/members", map[string]any{
		"object_type":   "pull_request",
		"object_number": 22,
	}, http.StatusCreated)
	postJSON(t, server.Echo(), http.MethodPost, "/v1/groups/"+groupID+"/members", map[string]any{
		"object_type":   "issue",
		"object_number": 11,
	}, http.StatusCreated)
	prMemberID := extractPathNumber(t, prRaw, "data.id")

	postJSON(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/pulls/22/annotations", map[string]any{
		"intent": "retry auth safely",
	}, http.StatusOK)
	postJSON(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/issues/11/annotations", map[string]any{
		"severity": "critical auth outage",
	}, http.StatusOK)
	postJSON(t, server.Echo(), http.MethodPost, "/v1/groups/"+groupID+"/annotations", map[string]any{
		"theme": "auth recovery work",
	}, http.StatusOK)

	fields := postJSON(t, server.Echo(), http.MethodGet, "/v1/repos/acme/widgets/fields", nil, http.StatusOK)
	require.Contains(t, fields, `"intent"`)
	require.Contains(t, fields, `"severity"`)
	require.Contains(t, fields, `"theme"`)

	prAnnotations := postJSON(t, server.Echo(), http.MethodGet, "/v1/repos/acme/widgets/pulls/22/annotations", nil, http.StatusOK)
	require.Contains(t, prAnnotations, `"retry auth safely"`)

	issueAnnotations := postJSON(t, server.Echo(), http.MethodGet, "/v1/repos/acme/widgets/issues/11/annotations", nil, http.StatusOK)
	require.Contains(t, issueAnnotations, `"critical auth outage"`)

	groupAnnotations := postJSON(t, server.Echo(), http.MethodGet, "/v1/groups/"+groupID+"/annotations", nil, http.StatusOK)
	require.Contains(t, groupAnnotations, `"auth recovery work"`)

	drainIndexJobs(t, ctx, db, indexer)

	searchText := postJSON(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/search/text", map[string]any{
		"query":        "auth",
		"target_types": []string{"pull_request", "issue", "group"},
		"limit":        5,
	}, http.StatusOK)
	require.Contains(t, searchText, `"target_type":"pull_request"`)
	require.Contains(t, searchText, `"target_type":"issue"`)
	require.Contains(t, searchText, `"target_type":"group"`)

	searchSimilar := postJSON(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/search/similar", map[string]any{
		"query":        "critical auth outage",
		"target_types": []string{"pull_request", "issue", "group"},
		"limit":        5,
	}, http.StatusOK)
	require.Contains(t, searchSimilar, `"target_type":"issue"`)

	removeRaw := postJSON(t, server.Echo(), http.MethodDelete, fmt.Sprintf("/v1/groups/%s/members/%d", groupID, prMemberID), nil, http.StatusOK)
	require.Contains(t, removeRaw, `"removed":true`)

	var group database.Group
	require.NoError(t, db.WithContext(ctx).Where("public_id = ?", groupID).First(&group).Error)
	var members []database.GroupMember
	require.NoError(t, db.WithContext(ctx).Where("group_id = ?", group.ID).Find(&members).Error)
	require.Len(t, members, 1)
	require.Equal(t, "issue", members[0].ObjectType)

	syncRaw := postJSON(t, server.Echo(), http.MethodPost, "/v1/groups/"+groupID+"/sync-comments", nil, http.StatusServiceUnavailable)
	require.Contains(t, syncRaw, `"github comment sync is not configured"`)
}

func TestAPIInvalidBodiesAndParams(t *testing.T) {
	db := openTestDB(t)
	ghClient := stubMirrorClient{}
	indexer := core.NewIndexer(db, ghClient, embedding.NewLocalHashProvider("local-hash@1", database.EmbeddingDimensions))
	service := core.NewService(db, ghClient, permissions.AllowAllChecker{}, indexer)
	server := httpapi.NewServer(db, service, true)

	groupRaw := postJSON(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/groups", map[string]any{
		"kind":  "mixed",
		"title": "Bad input coverage",
	}, http.StatusCreated)
	groupID := extractPathString(t, groupRaw, "data.id")

	invalidBodyRequest(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/groups", "{", http.StatusBadRequest)
	invalidBodyRequest(t, server.Echo(), http.MethodPost, "/v1/groups/"+groupID+"/annotations", "{", http.StatusBadRequest)
	invalidBodyRequest(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/search/text", "{", http.StatusBadRequest)
	invalidBodyRequest(t, server.Echo(), http.MethodPost, "/v1/repos/acme/widgets/search/similar", "{", http.StatusBadRequest)
	invalidBodyRequest(t, server.Echo(), http.MethodPatch, "/v1/repos/acme/widgets/fields/not-a-number", "{}", http.StatusBadRequest)
	invalidBodyRequest(t, server.Echo(), http.MethodDelete, "/v1/groups/"+groupID+"/members/not-a-number", "", http.StatusBadRequest)

	missingGroupAnnotations := postJSON(t, server.Echo(), http.MethodGet, "/v1/groups/missing-group/annotations", nil, http.StatusNotFound)
	require.Contains(t, missingGroupAnnotations, `"not found"`)
}

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, database.ApplyTestSchema(db))
	return db
}

func invalidBodyRequest(t *testing.T, e *echo.Echo, method, path, body string, status int) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, status, rec.Code, rec.Body.String())
}

func timePtr(value time.Time) *time.Time {
	return &value
}

func postJSON(t *testing.T, e *echo.Echo, method, path string, payload any, expectedStatus int) string {
	t.Helper()

	reader := bytes.NewReader(nil)
	if payload != nil {
		raw, err := json.Marshal(payload)
		require.NoError(t, err)
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Actor", "tester")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, expectedStatus, rec.Code, rec.Body.String())
	return rec.Body.String()
}

func extractPathNumber(t *testing.T, raw, path string) int64 {
	t.Helper()
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &payload))
	current := any(payload)
	for _, part := range strings.Split(path, ".") {
		object := current.(map[string]any)
		next := object[part]
		if next == nil {
			for key, value := range object {
				if strings.EqualFold(key, part) {
					next = value
					break
				}
			}
		}
		current = next
	}
	return int64(current.(float64))
}

func extractPathString(t *testing.T, raw, path string) string {
	t.Helper()
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &payload))
	current := any(payload)
	for _, part := range strings.Split(path, ".") {
		object := current.(map[string]any)
		next := object[part]
		if next == nil {
			for key, value := range object {
				if strings.EqualFold(key, part) {
					next = value
					break
				}
			}
		}
		current = next
	}
	return current.(string)
}

func drainIndexJobs(t *testing.T, ctx context.Context, db *gorm.DB, indexer *core.Indexer) {
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
