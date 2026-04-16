package database

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestEnsureGroupPublicIDsBackfillsRelatedKeys(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&Group{},
		&FieldDefinition{},
		&FieldValue{},
		&Event{},
		&EventRef{},
		&SearchDocument{},
		&Embedding{},
		&IndexJob{},
	))

	group := Group{
		GitHubRepositoryID: 101,
		RepositoryOwner:    "acme",
		RepositoryName:     "widgets",
		Kind:               "mixed",
		Title:              "Auth reliability",
		Status:             "open",
		CreatedBy:          "tester",
		UpdatedBy:          "tester",
		RowVersion:         1,
	}
	require.NoError(t, db.Create(&group).Error)

	oldKey := "group:1"
	now := time.Now().UTC()
	field := FieldDefinition{
		GitHubRepositoryID: 101,
		RepositoryOwner:    "acme",
		RepositoryName:     "widgets",
		Name:               "theme",
		DisplayName:        "theme",
		ObjectScope:        "group",
		FieldType:          "text",
		CreatedBy:          "tester",
		UpdatedBy:          "tester",
		RowVersion:         1,
	}
	require.NoError(t, db.Create(&field).Error)
	require.NoError(t, db.Create(&FieldValue{
		FieldDefinitionID:  field.ID,
		GitHubRepositoryID: 101,
		RepositoryOwner:    "acme",
		RepositoryName:     "widgets",
		TargetType:         "group",
		GroupID:            &group.ID,
		TargetKey:          oldKey,
		UpdatedBy:          "tester",
	}).Error)
	require.NoError(t, db.Create(&Event{
		GitHubRepositoryID: 101,
		AggregateType:      "group",
		AggregateKey:       oldKey,
		SequenceNo:         1,
		EventType:          "group.created",
		ActorType:          "user",
		ActorID:            "tester",
		OccurredAt:         now,
	}).Error)
	require.NoError(t, db.Create(&EventRef{
		EventID: 1,
		RefRole: "group",
		RefType: "group",
		RefKey:  oldKey,
	}).Error)
	require.NoError(t, db.Create(&SearchDocument{
		GitHubRepositoryID: 101,
		RepositoryOwner:    "acme",
		RepositoryName:     "widgets",
		TargetType:         "group",
		TargetKey:          oldKey,
		SearchText:         "Auth reliability",
		SourceUpdatedAt:    now,
		IndexedAt:          now,
	}).Error)
	require.NoError(t, db.Create(&Embedding{
		GitHubRepositoryID: 101,
		RepositoryOwner:    "acme",
		RepositoryName:     "widgets",
		TargetType:         "group",
		TargetKey:          oldKey,
		EmbeddingText:      "Auth reliability",
		EmbeddingModel:     "local-hash@1",
		Embedding:          pgvector.NewVector(make([]float32, EmbeddingDimensions)),
		SourceUpdatedAt:    now,
		IndexedAt:          now,
	}).Error)
	require.NoError(t, db.Create(&IndexJob{
		Kind:               "search_document_rebuild",
		Status:             "pending",
		GitHubRepositoryID: 101,
		RepositoryOwner:    "acme",
		RepositoryName:     "widgets",
		TargetType:         "group",
		TargetKey:          oldKey,
		CreatedAt:          now,
		UpdatedAt:          now,
	}).Error)

	require.NoError(t, EnsureGroupPublicIDs(context.Background(), db))

	var refreshed Group
	require.NoError(t, db.First(&refreshed, group.ID).Error)
	require.Regexp(t, regexp.MustCompile(`^[a-z0-9]+-[a-z0-9]+-[a-z0-9]{4}$`), refreshed.PublicID)
	newKey := "group:" + refreshed.PublicID

	var value FieldValue
	require.NoError(t, db.First(&value).Error)
	require.Equal(t, newKey, value.TargetKey)

	var event Event
	require.NoError(t, db.First(&event).Error)
	require.Equal(t, newKey, event.AggregateKey)

	var ref EventRef
	require.NoError(t, db.First(&ref).Error)
	require.Equal(t, newKey, ref.RefKey)

	var searchDocument SearchDocument
	require.NoError(t, db.First(&searchDocument).Error)
	require.Equal(t, newKey, searchDocument.TargetKey)

	var embedding Embedding
	require.NoError(t, db.First(&embedding).Error)
	require.Equal(t, newKey, embedding.TargetKey)

	var job IndexJob
	require.NoError(t, db.First(&job).Error)
	require.Equal(t, newKey, job.TargetKey)
}
