package core

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dutifuldev/prtags/internal/database"
	"github.com/dutifuldev/prtags/internal/embedding"
	"github.com/dutifuldev/prtags/internal/permissions"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAddGroupMemberConcurrentDistinctPostgres(t *testing.T) {
	ctx := context.Background()
	service, db := newPostgresTestServiceWithBatchOptions(t, batchBehavior{})

	actor := permissions.Actor{Type: "user", ID: "tester"}
	group, err := service.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{
		Kind:  "pull_request",
		Title: "Concurrent add verification",
	}, "")
	require.NoError(t, err)

	objectNumbers := []int{22, 23, 24, 25, 26, 27}
	results := runConcurrentAddGroupMembers(ctx, service, actor, group.PublicID, objectNumbers)
	for _, result := range results {
		require.NoError(t, result.err)
	}

	var members []database.GroupMember
	require.NoError(t, db.WithContext(ctx).
		Where("group_id = ?", group.ID).
		Order("object_number ASC").
		Find(&members).Error)
	require.Len(t, members, len(objectNumbers))
	for i, member := range members {
		require.Equal(t, objectNumbers[i], member.ObjectNumber)
	}

	var events []database.Event
	require.NoError(t, db.WithContext(ctx).
		Where("aggregate_type = ? AND aggregate_key = ?", "group", groupTargetKey(group.PublicID)).
		Order("sequence_no ASC").
		Find(&events).Error)
	require.Len(t, events, len(objectNumbers)+1)
	for i, event := range events {
		require.Equal(t, i+1, event.SequenceNo)
	}
	memberAddedCount := 0
	for _, event := range events {
		if event.EventType == "group.member_added" {
			memberAddedCount++
		}
	}
	require.Equal(t, len(objectNumbers), memberAddedCount)
}

func TestAddGroupMemberConcurrentDuplicatePostgres(t *testing.T) {
	ctx := context.Background()
	service, db := newPostgresTestServiceWithBatchOptions(t, batchBehavior{})

	actor := permissions.Actor{Type: "user", ID: "tester"}
	group, err := service.CreateGroup(ctx, actor, "acme", "widgets", GroupInput{
		Kind:  "pull_request",
		Title: "Duplicate add verification",
	}, "")
	require.NoError(t, err)

	results := runConcurrentAddGroupMembers(ctx, service, actor, group.PublicID, []int{22, 22, 22, 22, 22, 22})

	successCount := 0
	conflictCount := 0
	for _, result := range results {
		if result.err == nil {
			successCount++
			continue
		}
		var fail *FailError
		require.ErrorAs(t, result.err, &fail)
		require.Equal(t, 409, fail.StatusCode)
		require.Equal(t, "group member already exists", fail.Message)
		require.NotContains(t, strings.ToLower(result.err.Error()), "25p02")
		conflictCount++
	}
	require.Equal(t, 1, successCount)
	require.Equal(t, len(results)-1, conflictCount)

	var members []database.GroupMember
	require.NoError(t, db.WithContext(ctx).
		Where("group_id = ?", group.ID).
		Find(&members).Error)
	require.Len(t, members, 1)
	require.Equal(t, 22, members[0].ObjectNumber)

	var events []database.Event
	require.NoError(t, db.WithContext(ctx).
		Where("aggregate_type = ? AND aggregate_key = ?", "group", groupTargetKey(group.PublicID)).
		Find(&events).Error)
	require.Len(t, events, 2)
}

func TestAddGroupMemberConcurrentCrossGroupDuplicatePostgres(t *testing.T) {
	ctx := context.Background()
	service, db := newPostgresTestServiceWithBatchOptions(t, batchBehavior{})

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

	type result struct {
		groupPublicID string
		err           error
	}
	start := make(chan struct{})
	results := make([]result, 2)
	groupIDs := []string{first.PublicID, second.PublicID}
	var wg sync.WaitGroup
	for i, groupPublicID := range groupIDs {
		wg.Add(1)
		go func(index int, id string) {
			defer wg.Done()
			<-start
			_, err := service.AddGroupMember(ctx, actor, id, "pull_request", 22, "")
			results[index] = result{groupPublicID: id, err: err}
		}(i, groupPublicID)
	}
	close(start)
	wg.Wait()

	successCount := 0
	conflictCount := 0
	for _, result := range results {
		if result.err == nil {
			successCount++
			continue
		}
		var fail *FailError
		require.ErrorAs(t, result.err, &fail)
		require.Equal(t, 409, fail.StatusCode)
		require.Equal(t, "target already belongs to another group", fail.Message)
		details, ok := fail.Data.(groupMemberConflictDetails)
		require.True(t, ok)
		require.Contains(t, []string{first.PublicID, second.PublicID}, details.GroupPublicID)
		require.NotContains(t, strings.ToLower(result.err.Error()), "25p02")
		conflictCount++
	}
	require.Equal(t, 1, successCount)
	require.Equal(t, 1, conflictCount)

	var members []database.GroupMember
	require.NoError(t, db.WithContext(ctx).
		Where("github_repository_id = ? AND object_type = ? AND object_number = ?", first.GitHubRepositoryID, "pull_request", 22).
		Find(&members).Error)
	require.Len(t, members, 1)
	require.Contains(t, []uint{first.ID, second.ID}, members[0].GroupID)
}

type addGroupMemberResult struct {
	number int
	err    error
}

func runConcurrentAddGroupMembers(ctx context.Context, service *Service, actor permissions.Actor, groupPublicID string, objectNumbers []int) []addGroupMemberResult {
	start := make(chan struct{})
	results := make([]addGroupMemberResult, len(objectNumbers))
	var wg sync.WaitGroup

	for i, objectNumber := range objectNumbers {
		wg.Add(1)
		go func(index int, n int) {
			defer wg.Done()
			<-start
			_, err := service.AddGroupMember(ctx, actor, groupPublicID, "pull_request", n, "")
			results[index] = addGroupMemberResult{number: n, err: err}
		}(i, objectNumber)
	}

	close(start)
	wg.Wait()
	return results
}

func newPostgresTestServiceWithBatchOptions(t *testing.T, behavior batchBehavior) (*Service, *gorm.DB) {
	t.Helper()

	adminURL := strings.TrimSpace(os.Getenv("PRTAGS_TEST_DATABASE_URL"))
	if adminURL == "" {
		t.Skip("PRTAGS_TEST_DATABASE_URL is not set")
	}

	databaseName := fmt.Sprintf("prtags_test_%d", time.Now().UnixNano())
	adminDB, err := database.Open(adminURL)
	require.NoError(t, err)

	adminSQLDB, err := adminDB.DB()
	require.NoError(t, err)
	require.NoError(t, adminSQLDB.Ping())
	_, err = adminSQLDB.ExecContext(context.Background(), "CREATE DATABASE "+databaseName)
	require.NoError(t, err)

	testURL, err := replaceDatabaseName(adminURL, databaseName)
	require.NoError(t, err)

	db, err := database.Open(testURL)
	require.NoError(t, err)
	require.NoError(t, database.RunMigrations(db))

	ghClient := testMirrorClient{behavior: behavior}
	indexer := NewIndexer(db, ghClient, embedding.NewLocalHashProvider("local-hash@1", database.EmbeddingDimensions))
	service := NewService(db, ghClient, permissions.AllowAllChecker{}, indexer)

	t.Cleanup(func() {
		sqlDB, err := db.DB()
		require.NoError(t, err)
		require.NoError(t, sqlDB.Close())

		_, err = adminSQLDB.ExecContext(context.Background(), `
			SELECT pg_terminate_backend(pid)
			FROM pg_stat_activity
			WHERE datname = $1 AND pid <> pg_backend_pid()
		`, databaseName)
		require.NoError(t, err)
		_, err = adminSQLDB.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+databaseName)
		require.NoError(t, err)
		require.NoError(t, adminSQLDB.Close())
	})

	return service, db
}

func replaceDatabaseName(rawURL string, databaseName string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	parsed.Path = "/" + databaseName
	return parsed.String(), nil
}
