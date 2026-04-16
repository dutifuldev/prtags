package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dutifuldev/prtags/internal/database"
	"github.com/dutifuldev/prtags/internal/embedding"
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
	require.NoError(t, db.AutoMigrate(&database.IndexJob{}))

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

	indexer := NewIndexer(db, embedding.NewLocalHashProvider("local-hash@1", database.EmbeddingDimensions))
	require.NoError(t, indexer.recoverStaleJobs(context.Background()))

	var job database.IndexJob
	require.NoError(t, db.First(&job).Error)
	require.Equal(t, "pending", job.Status)
	require.Equal(t, "", job.LeaseOwner)
	require.Nil(t, job.HeartbeatAt)
	require.NotNil(t, job.NextAttemptAt)
	require.Equal(t, "job lease expired", job.LastError)
}
