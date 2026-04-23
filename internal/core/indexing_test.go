package core

import (
	"context"
	"strings"
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

func TestRecoverStaleJobsRequeuesExpiredProcessingJobs(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, database.ApplyTestSchema(db))

	stale := time.Now().UTC().Add(-10 * time.Minute)
	require.NoError(t, db.Create(&database.IndexJob{
		Kind:               "search_document_rebuild",
		Status:             "processing",
		GitHubRepositoryID: 101,
		RepositoryOwner:    "acme",
		RepositoryName:     "widgets",
		TargetType:         "pull_request",
		TargetKey:          "repo:101:pull_request:22",
		LeaseOwner:         "worker-old",
		HeartbeatAt:        &stale,
	}).Error)

	indexer := NewIndexer(db, ghreplica.NewClient("http://127.0.0.1"), embedding.NewLocalHashProvider("local-hash@1", database.EmbeddingDimensions))
	require.NoError(t, indexer.recoverStaleJobs(context.Background()))

	var job database.IndexJob
	require.NoError(t, db.First(&job).Error)
	require.Equal(t, "pending", job.Status)
	require.Equal(t, "", job.LeaseOwner)
	require.Nil(t, job.HeartbeatAt)
	require.NotNil(t, job.NextAttemptAt)
	require.Equal(t, "job lease expired", job.LastError)
}

func TestBuildSearchResultLoadsProjectionAndAnnotations(t *testing.T) {
	ctx := context.Background()
	service, _, server := newTestService(t)
	defer server.Close()

	actor := permissions.Actor{Type: "user", ID: "tester"}
	_, err := service.CreateFieldDefinition(ctx, actor, "acme", "widgets", FieldDefinitionInput{
		Name:         "intent",
		ObjectScope:  "pull_request",
		FieldType:    "text",
		IsSearchable: true,
	}, "")
	require.NoError(t, err)

	_, err = service.SetAnnotations(ctx, actor, "acme", "widgets", "pull_request", 22, nil, map[string]any{
		"intent": "retry auth safely",
	}, "")
	require.NoError(t, err)

	result, err := service.buildSearchResult(ctx, 101, "pull_request", objectTargetKey(101, "pull_request", 22), 0.75)
	require.NoError(t, err)
	require.NotNil(t, result.Projection)
	require.Equal(t, 22, result.Projection.ObjectNumber)
	require.Equal(t, "retry auth safely", result.Annotations["intent"])
	require.Equal(t, 0.75, result.Score)
}

func TestBuildSearchResultLoadsGroupAnnotations(t *testing.T) {
	ctx := context.Background()
	service, _, server := newTestService(t)
	defer server.Close()

	actor := permissions.Actor{Type: "user", ID: "tester"}
	_, err := service.CreateFieldDefinition(ctx, actor, "acme", "widgets", FieldDefinitionInput{
		Name:         "theme",
		ObjectScope:  "group",
		FieldType:    "text",
		IsSearchable: true,
	}, "")
	require.NoError(t, err)

	group, err := service.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{
		Kind:  "mixed",
		Title: "Auth recovery",
	}, "")
	require.NoError(t, err)

	_, err = service.SetAnnotations(ctx, actor, "acme", "widgets", "group", 0, &group.ID, map[string]any{
		"theme": "reliability",
	}, "")
	require.NoError(t, err)

	result, err := service.buildSearchResult(ctx, 101, "group", groupTargetKey(group.PublicID), 0.9)
	require.NoError(t, err)
	require.Equal(t, group.PublicID, result.ID)
	require.Equal(t, "reliability", result.Annotations["theme"])
}
