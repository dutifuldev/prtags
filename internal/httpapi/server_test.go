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

func TestAPIEndToEndFlow(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	stub := newStubGHReplica(t)
	indexer := core.NewIndexer(db, embedding.NewLocalHashProvider("local-hash@1", database.EmbeddingDimensions))
	service := core.NewService(db, ghreplica.NewClient(stub.URL), permissions.AllowAllChecker{}, indexer)
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

	group := postJSON(t, server.Echo(), http.MethodGet, fmt.Sprintf("/v1/groups/%s", groupID), nil, http.StatusOK)
	require.Contains(t, group, `"Auth reliability"`)
	require.Contains(t, group, fmt.Sprintf(`"id":"%s"`, groupID))
	require.Contains(t, group, `"object_summary"`)
	require.Contains(t, group, `"Retry ACP turns safely (batched)"`)
	require.Contains(t, group, `"https://github.com/acme/widgets/pull/22"`)

	var events int64
	require.NoError(t, db.WithContext(ctx).Model(&database.Event{}).Count(&events).Error)
	require.Greater(t, events, int64(0))
}

func TestManifestImportExport(t *testing.T) {
	db := openTestDB(t)
	stub := newStubGHReplica(t)
	indexer := core.NewIndexer(db, embedding.NewLocalHashProvider("local-hash@1", database.EmbeddingDimensions))
	service := core.NewService(db, ghreplica.NewClient(stub.URL), permissions.AllowAllChecker{}, indexer)
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
	stub := newStubGHReplica(t)
	indexer := core.NewIndexer(db, embedding.NewLocalHashProvider("local-hash@1", database.EmbeddingDimensions))
	service := core.NewService(db, ghreplica.NewClient(stub.URL), permissions.AllowAllChecker{}, indexer)
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

func openTestDB(t *testing.T) *gorm.DB {
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
	return db
}

func newStubGHReplica(t *testing.T) *httptest.Server {
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
