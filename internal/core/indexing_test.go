package core

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/dutifuldev/prtags/internal/database"
	"github.com/dutifuldev/prtags/internal/embedding"
	"github.com/dutifuldev/prtags/internal/permissions"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type failingEmbeddingProvider struct{}

func (failingEmbeddingProvider) Model() string   { return "failing" }
func (failingEmbeddingProvider) Dimensions() int { return database.EmbeddingDimensions }
func (failingEmbeddingProvider) Embed(context.Context, string) ([]float32, error) {
	return nil, errors.New("embed failed")
}

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

	indexer := NewIndexer(db, testMirrorClient{}, embedding.NewLocalHashProvider("local-hash@1", database.EmbeddingDimensions))
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

func TestIndexerHelperPaths(t *testing.T) {
	ctx := context.Background()
	service, db, server := newTestService(t)
	defer server.Close()

	indexer := service.indexer
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	indexer.Start(canceled, time.Millisecond)

	require.NoError(t, db.Create(&database.IndexJob{
		Kind:               "unknown",
		Status:             "pending",
		GitHubRepositoryID: 101,
		RepositoryOwner:    "acme",
		RepositoryName:     "widgets",
		TargetType:         "pull_request",
		TargetKey:          objectTargetKey(101, "pull_request", 22),
	}).Error)
	require.NoError(t, indexer.RunOnce(ctx))

	var failed database.IndexJob
	require.NoError(t, db.First(&failed).Error)
	require.Equal(t, "pending", failed.Status)
	require.Contains(t, failed.LastError, "unknown job kind")

	sqlDB, err := db.DB()
	require.NoError(t, err)
	rows, err := sqlDB.QueryContext(ctx, `SELECT 101 AS github_repository_id, 'pull_request' AS target_type, 'repo:101:pull_request:22' AS target_key, 0.9 AS score`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()

	scored, err := scanScoredSearchRows(rows)
	require.NoError(t, err)
	require.Len(t, scored, 1)
	require.Equal(t, "pull_request", scored[0].TargetType)

	_, err = service.searchTextRowsPostgres(ctx, 101, "auth", []string{"pull_request"}, 5)
	require.Error(t, err)
	_, err = service.searchSimilarRowsPostgres(ctx, 101, []float32{1, 2}, []string{"pull_request"}, 5)
	require.Error(t, err)
	require.NoError(t, indexer.markJobFailed(ctx, failed.ID, sql.ErrNoRows))
}

func TestIndexerFailureAndSearchHelperBranches(t *testing.T) {
	ctx := context.Background()
	service, db, server := newTestService(t)
	defer server.Close()

	indexer := service.indexer
	require.Error(t, indexer.refreshTargetProjection(ctx, database.IndexJob{
		Kind:               indexJobKindTargetProjectionRefresh,
		GitHubRepositoryID: 101,
		RepositoryOwner:    "acme",
		RepositoryName:     "widgets",
		TargetType:         "pull_request",
		TargetKey:          "bad-key",
	}))
	require.Error(t, indexer.refreshTargetProjection(ctx, database.IndexJob{
		Kind:               indexJobKindTargetProjectionRefresh,
		GitHubRepositoryID: 101,
		RepositoryOwner:    "acme",
		RepositoryName:     "widgets",
		TargetType:         "group",
		TargetKey:          "group:test",
	}))

	textQuery, err := service.searchTextRows(ctx, 101, "auth", []string{"pull_request"}, 5)
	require.NoError(t, err)
	require.NotNil(t, textQuery)

	similarQuery, err := service.searchSimilarRows(ctx, 101, []float32{1, 2}, []string{"pull_request"}, 5)
	require.NoError(t, err)
	require.NotNil(t, similarQuery)

	var noProjection database.TargetProjection
	require.NoError(t, db.Create(&database.Group{
		PublicID:           "quiet-otter-z9x2",
		GitHubRepositoryID: 101,
		RepositoryOwner:    "acme",
		RepositoryName:     "widgets",
		Kind:               "mixed",
		Title:              "No projection",
		Status:             "open",
		CreatedBy:          "tester",
		UpdatedBy:          "tester",
		RowVersion:         1,
	}).Error)
	var group database.Group
	require.NoError(t, db.Where("public_id = ?", "quiet-otter-z9x2").First(&group).Error)

	result, err := service.populateObjectSearchResult(ctx, 101, "pull_request", "repo:101:pull_request:999", TextSearchResult{TargetType: "pull_request", TargetKey: "repo:101:pull_request:999"})
	require.NoError(t, err)
	require.Nil(t, result.Projection)

	groupResult, err := service.populateGroupSearchResult(ctx, 101, groupTargetKey(group.PublicID), TextSearchResult{TargetType: "group", TargetKey: groupTargetKey(group.PublicID)})
	require.NoError(t, err)
	require.Equal(t, group.PublicID, groupResult.ID)
	require.Empty(t, noProjection.HTMLURL)
}

func TestIndexerPostgresSearchHelpers(t *testing.T) {
	ctx := context.Background()
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)

	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	indexer := NewIndexer(db, testMirrorClient{}, embedding.NewLocalHashProvider("local-hash@1", database.EmbeddingDimensions))
	service := NewService(db, testMirrorClient{}, permissions.AllowAllChecker{}, indexer)

	mock.ExpectQuery("(?s).*FROM search_documents.*").
		WillReturnRows(sqlmock.NewRows([]string{"github_repository_id", "target_type", "target_key", "score"}).
			AddRow(101, "pull_request", "repo:101:pull_request:22", 0.91))

	textRows, err := service.searchTextRowsPostgres(ctx, 101, "auth", []string{"pull_request"}, 5)
	require.NoError(t, err)
	require.Len(t, textRows, 1)
	require.Equal(t, "pull_request", textRows[0].TargetType)

	mock.ExpectQuery("(?s).*FROM search_documents.*").
		WillReturnRows(sqlmock.NewRows([]string{"github_repository_id", "target_type", "target_key", "score"}).
			AddRow(101, "pull_request", "repo:101:pull_request:22", 0.77))
	textRows, err = service.searchTextRows(ctx, 101, "auth", []string{"pull_request"}, 5)
	require.NoError(t, err)
	require.Len(t, textRows, 1)

	mock.ExpectQuery("(?s).*FROM embeddings.*").
		WillReturnRows(sqlmock.NewRows([]string{"github_repository_id", "target_type", "target_key", "score"}).
			AddRow(101, "issue", "repo:101:issue:11", 0.88))

	similarRows, err := service.searchSimilarRowsPostgres(ctx, 101, []float32{1, 2}, []string{"issue"}, 3)
	require.NoError(t, err)
	require.Len(t, similarRows, 1)
	require.Equal(t, "issue", similarRows[0].TargetType)

	mock.ExpectQuery("(?s).*FROM embeddings.*").
		WillReturnRows(sqlmock.NewRows([]string{"github_repository_id", "target_type", "target_key", "score"}).
			AddRow(101, "issue", "repo:101:issue:11", 0.66))
	similarRows, err = service.searchSimilarRows(ctx, 101, []float32{1, 2}, []string{"issue"}, 3)
	require.NoError(t, err)
	require.Len(t, similarRows, 1)

	mock.ExpectQuery("SELECT 101 AS github_repository_id").
		WillReturnRows(sqlmock.NewRows([]string{"github_repository_id", "target_type", "target_key", "score"}).
			AddRow(101, "pull_request", "repo:101:pull_request:22", "bad"))
	badRows, err := sqlDB.QueryContext(ctx, `SELECT 101 AS github_repository_id, 'pull_request' AS target_type, 'repo:101:pull_request:22' AS target_key, 'bad' AS score`)
	require.NoError(t, err)
	defer func() { require.NoError(t, badRows.Close()) }()
	_, err = scanScoredSearchRows(badRows)
	require.Error(t, err)

	require.NoError(t, sqlDB.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectionRefreshJobHelpers(t *testing.T) {
	ctx := context.Background()
	service, db, server := newTestService(t)
	defer server.Close()

	group := database.Group{
		PublicID:           "sleek-hound-v3m1",
		GitHubRepositoryID: 101,
		RepositoryOwner:    "acme",
		RepositoryName:     "widgets",
		Kind:               "mixed",
		Title:              "Projection refresh",
		Status:             "open",
		CreatedBy:          "tester",
		UpdatedBy:          "tester",
		RowVersion:         1,
	}
	require.NoError(t, db.Create(&group).Error)

	target := targetRef{
		RepositoryID: group.GitHubRepositoryID,
		Owner:        group.RepositoryOwner,
		Name:         group.RepositoryName,
		TargetType:   "pull_request",
		TargetKey:    objectTargetKey(group.GitHubRepositoryID, "pull_request", 22),
		ObjectNumber: 22,
		Projection: &database.TargetProjection{
			SourceUpdatedAt: time.Now().UTC().Add(-time.Minute),
		},
	}

	require.NoError(t, service.enqueueTargetProjectionRefreshJobs(ctx, group, []targetRef{
		target,
		target,
		{TargetType: "group", TargetKey: groupTargetKey(group.PublicID)},
	}))

	var jobs []database.IndexJob
	require.NoError(t, db.WithContext(ctx).Where("kind = ?", indexJobKindTargetProjectionRefresh).Find(&jobs).Error)
	require.Len(t, jobs, 1)
	require.NotNil(t, jobs[0].NextAttemptAt)

	exists, err := projectionRefreshJobExists(db, group.GitHubRepositoryID, target)
	require.NoError(t, err)
	require.True(t, exists)

	require.NoError(t, service.createProjectionRefreshJob(db, group, targetRef{
		RepositoryID: group.GitHubRepositoryID,
		Owner:        group.RepositoryOwner,
		Name:         group.RepositoryName,
		TargetType:   "issue",
		TargetKey:    objectTargetKey(group.GitHubRepositoryID, "issue", 11),
		ObjectNumber: 11,
		Projection: &database.TargetProjection{
			SourceUpdatedAt: time.Now().UTC(),
		},
	}, time.Now().UTC()))

	exists, err = projectionRefreshJobExists(db, group.GitHubRepositoryID, targetRef{TargetType: "issue", TargetKey: objectTargetKey(group.GitHubRepositoryID, "issue", 11)})
	require.NoError(t, err)
	require.True(t, exists)
}

func TestIndexerClaimAndProcessSuccessPaths(t *testing.T) {
	ctx := context.Background()
	service, db, server := newTestService(t)
	defer server.Close()

	require.NoError(t, db.Create(&database.IndexJob{
		Kind:               "search_document_rebuild",
		Status:             "pending",
		GitHubRepositoryID: 101,
		RepositoryOwner:    "acme",
		RepositoryName:     "widgets",
		TargetType:         "pull_request",
		TargetKey:          objectTargetKey(101, "pull_request", 22),
	}).Error)

	job, claimed, err := service.indexer.claimNextJob(ctx)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, "processing", job.Status)

	require.NoError(t, service.indexer.processJob(ctx, job))

	var stored database.IndexJob
	require.NoError(t, db.First(&stored, job.ID).Error)
	require.Equal(t, "succeeded", stored.Status)

	_, claimed, err = service.indexer.claimNextJob(ctx)
	require.NoError(t, err)
	require.False(t, claimed)

	rows := trimScoredSearchTargets([]scoredSearchTarget{
		{TargetKey: "a", Score: 1},
		{TargetKey: "b", Score: 2},
	}, 1)
	require.Len(t, rows, 1)

	refreshJob := database.IndexJob{
		Kind:               indexJobKindTargetProjectionRefresh,
		Status:             "pending",
		GitHubRepositoryID: 101,
		RepositoryOwner:    "acme",
		RepositoryName:     "widgets",
		TargetType:         "pull_request",
		TargetKey:          objectTargetKey(101, "pull_request", 22),
	}
	require.NoError(t, service.indexer.processJob(ctx, refreshJob))

	embeddingJob := database.IndexJob{
		Kind:               "embedding_rebuild",
		Status:             "pending",
		GitHubRepositoryID: 101,
		RepositoryOwner:    "acme",
		RepositoryName:     "widgets",
		TargetType:         "pull_request",
		TargetKey:          objectTargetKey(101, "pull_request", 22),
	}
	require.NoError(t, service.indexer.processJob(ctx, embeddingJob))
}

func TestIndexerAdditionalHelperAndErrorBranches(t *testing.T) {
	ctx := context.Background()
	service, db, server := newTestService(t)
	defer server.Close()

	badService := NewService(db, testMirrorClient{behavior: batchBehavior{fail: true}}, permissions.AllowAllChecker{}, service.indexer)
	_, err := badService.SearchText(ctx, "acme", "widgets", "auth", nil, 0)
	require.Error(t, err)
	_, err = badService.SearchSimilar(ctx, "acme", "widgets", "auth", nil, 0)
	require.Error(t, err)

	failingIndexer := NewIndexer(db, testMirrorClient{}, failingEmbeddingProvider{})
	failingService := NewService(db, testMirrorClient{}, permissions.AllowAllChecker{}, failingIndexer)
	_, err = failingService.SearchSimilar(ctx, "acme", "widgets", "auth", []string{"pull_request"}, 5)
	require.ErrorContains(t, err, "embed failed")

	idleIndexer := NewIndexer(db, testMirrorClient{}, embedding.NewLocalHashProvider("local-hash@1", database.EmbeddingDimensions))
	require.NoError(t, idleIndexer.RunOnce(ctx))

	missingJob := database.IndexJob{
		ID:                 9999,
		Kind:               indexJobKindTargetProjectionRefresh,
		Status:             "pending",
		GitHubRepositoryID: 101,
		RepositoryOwner:    "acme",
		RepositoryName:     "widgets",
		TargetType:         "group",
		TargetKey:          "group:test",
	}
	require.NoError(t, service.indexer.processJob(ctx, missingJob))

	badRefreshJob := database.IndexJob{
		ID:                 10000,
		Kind:               indexJobKindTargetProjectionRefresh,
		Status:             "pending",
		GitHubRepositoryID: 101,
		RepositoryOwner:    "acme",
		RepositoryName:     "widgets",
		TargetType:         "pull_request",
		TargetKey:          "bad-key",
	}
	require.NoError(t, service.indexer.processJob(ctx, badRefreshJob))

	group, err := service.CreateGroup(ctx, permissionsActor(), "acme", "widgets", GroupInput{Kind: "mixed", Title: "Search group", Description: "search desc"}, "")
	require.NoError(t, err)

	parts, ts, err := service.indexer.groupSearchParts(ctx, groupTargetKey(group.PublicID))
	require.NoError(t, err)
	require.NotEmpty(t, parts)
	require.False(t, ts.IsZero())

	parts, ts, err = service.indexer.groupSearchParts(ctx, "group:missing")
	require.NoError(t, err)
	require.Empty(t, parts)
	require.False(t, ts.IsZero())

	parts, ts, err = service.indexer.groupSearchParts(ctx, "repo:bad")
	require.NoError(t, err)
	require.Empty(t, parts)
	require.False(t, ts.IsZero())

	parts, ts, err = service.indexer.objectSearchParts(ctx, 101, "pull_request", "repo:101:pull_request:999")
	require.NoError(t, err)
	require.Empty(t, parts)
	require.False(t, ts.IsZero())

	parts, ts, err = service.indexer.objectSearchParts(ctx, 101, "pull_request", "bad-key")
	require.NoError(t, err)
	require.Empty(t, parts)
	require.False(t, ts.IsZero())

	_, _, err = service.indexer.buildSearchText(ctx, 101, "pull_request", objectTargetKey(101, "pull_request", 22))
	require.NoError(t, err)
	_, _, err = service.indexer.buildEmbeddingText(ctx, 101, "pull_request", objectTargetKey(101, "pull_request", 22))
	require.NoError(t, err)

	emptyResults, err := service.searchTextRowsFallback(ctx, 101, "   ", []string{"pull_request"}, 5)
	require.NoError(t, err)
	require.Nil(t, emptyResults)

	require.Equal(t, 0.0, fallbackTextScore("", "text"))
	require.Equal(t, 0.0, fallbackTextScore("auth", ""))
	require.Equal(t, 0.0, cosineSimilarity([]float32{}, []float32{1}))
	require.Equal(t, 0.0, cosineSimilarity([]float32{1, 2}, []float32{1}))
	require.Equal(t, 0.0, cosineSimilarity([]float32{0, 0}, []float32{1, 2}))

	require.Equal(t, []scoredSearchTarget{{TargetKey: "a", Score: 1}}, trimScoredSearchTargets([]scoredSearchTarget{{TargetKey: "a", Score: 1}}, 10))

	_, ok := objectNumberFromTargetKey("bad")
	require.False(t, ok)
	_, ok = objectNumberFromTargetKey("repo:101:pull_request:not-a-number")
	require.False(t, ok)
	_, ok = groupPublicIDFromTargetKey("repo:101:group:1")
	require.False(t, ok)
	_, ok = groupPublicIDFromTargetKey("group:   ")
	require.False(t, ok)

	noProjection, err := service.populateObjectSearchResult(ctx, 101, "pull_request", "bad-key", TextSearchResult{TargetType: "pull_request", TargetKey: "bad-key"})
	require.NoError(t, err)
	require.Nil(t, noProjection.Projection)

	noGroup, err := service.populateGroupSearchResult(ctx, 101, "bad-key", TextSearchResult{TargetType: "group", TargetKey: "bad-key"})
	require.NoError(t, err)
	require.Empty(t, noGroup.ID)

	noGroup, err = service.populateGroupSearchResult(ctx, 101, "group:missing", TextSearchResult{TargetType: "group", TargetKey: "group:missing"})
	require.ErrorIs(t, err, ErrNotFound)

	_, err = service.resolveSearchResults(ctx, 101, []scoredSearchTarget{{TargetType: "group", TargetKey: "group:missing", Score: 1}})
	require.ErrorIs(t, err, ErrNotFound)
}
