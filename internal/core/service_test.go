package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dutifuldev/prtags/internal/database"
	"github.com/dutifuldev/prtags/internal/embedding"
	ghreplica "github.com/dutifuldev/prtags/internal/ghreplica"
	"github.com/dutifuldev/prtags/internal/permissions"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

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

func TestGetGroupEnrichesMembersWithBatchSummaries(t *testing.T) {
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

	_, members, _, err := service.GetGroup(ctx, group.PublicID)
	require.NoError(t, err)
	require.Len(t, members, 2)
	require.Equal(t, "Retry ACP turns safely (batched)", members[0].ObjectSummary.Title)
	require.Equal(t, "bob", members[0].ObjectSummary.AuthorLogin)
	require.Equal(t, "Auth retries are flaky (batched)", members[1].ObjectSummary.Title)
	require.Equal(t, "alice", members[1].ObjectSummary.AuthorLogin)
}

func TestGetGroupFallsBackToCachedProjectionWhenBatchReadFails(t *testing.T) {
	ctx := context.Background()
	service, _, server := newTestServiceWithBatchFailure(t)
	defer server.Close()

	actor := permissions.Actor{Type: "user", ID: "tester"}
	group, err := service.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{
		Kind:  "pull_request",
		Title: "Auth work",
	}, "")
	require.NoError(t, err)

	_, err = service.AddGroupMember(ctx, actor, group.PublicID, "pull_request", 22, "")
	require.NoError(t, err)

	_, members, _, err := service.GetGroup(ctx, group.PublicID)
	require.NoError(t, err)
	require.Len(t, members, 1)
	require.NotNil(t, members[0].ObjectSummary)
	require.Equal(t, "Retry ACP turns safely", members[0].ObjectSummary.Title)
	require.Equal(t, "https://github.com/acme/widgets/pull/22", members[0].ObjectSummary.HTMLURL)
}

func newTestService(t *testing.T) (*Service, *gorm.DB, *httptest.Server) {
	return newTestServiceWithBatchBehavior(t, false)
}

func newTestServiceWithBatchFailure(t *testing.T) (*Service, *gorm.DB, *httptest.Server) {
	return newTestServiceWithBatchBehavior(t, true)
}

func newTestServiceWithBatchBehavior(t *testing.T, failBatch bool) (*Service, *gorm.DB, *httptest.Server) {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&database.RepositoryProjection{},
		&database.TargetProjection{},
		&database.Group{},
		&database.GroupMember{},
		&database.FieldDefinition{},
		&database.FieldValue{},
		&database.Event{},
		&database.EventRef{},
		&database.SearchDocument{},
		&database.Embedding{},
		&database.IndexJob{},
	))

	server := newTestGHReplicaServer(t, failBatch)

	indexer := NewIndexer(db, embedding.NewLocalHashProvider("local-hash@1", database.EmbeddingDimensions))
	service := NewService(db, ghreplica.NewClient(server.URL), permissions.AllowAllChecker{}, indexer)
	return service, db, server
}

func newTestGHReplicaServer(t *testing.T, failBatch bool) *httptest.Server {
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
			if failBatch {
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
