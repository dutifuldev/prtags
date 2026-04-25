package ghreplica

import (
	"context"
	"testing"
	"time"

	"github.com/dutifuldev/ghreplica/mirror"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestClientReadsMirrorObjects(t *testing.T) {
	db := openMirrorTestDB(t)
	seedMirrorRows(t, db)

	client := NewClient(mirror.NewReader(db))
	repository, err := client.GetRepository(context.Background(), "openclaw", "openclaw")
	require.NoError(t, err)
	require.EqualValues(t, 1103012935, repository.ID)
	require.Equal(t, "openclaw/openclaw", repository.FullName)
	require.Equal(t, "openclaw", repository.Owner.Login)

	issue, err := client.GetIssue(context.Background(), "openclaw", "openclaw", 11)
	require.NoError(t, err)
	require.Equal(t, "Issue title", issue.Title)
	require.Equal(t, "octocat", issue.User.Login)

	pull, err := client.GetPullRequest(context.Background(), "openclaw", "openclaw", 22)
	require.NoError(t, err)
	require.Equal(t, "Pull title", pull.Title)
	require.Equal(t, "open", pull.State)
	require.Equal(t, "octocat", pull.User.Login)
}

func TestClientReturnsMissingMirrorErrors(t *testing.T) {
	db := openMirrorTestDB(t)
	client := NewClient(mirror.NewReader(db))

	_, err := client.GetRepository(context.Background(), "missing", "repo")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestClientReturnsMissingIssueAndPullRequestErrors(t *testing.T) {
	db := openMirrorTestDB(t)
	seedMirrorRows(t, db)
	client := NewClient(mirror.NewReader(db))

	_, err := client.GetIssue(context.Background(), "openclaw", "openclaw", 999)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	_, err = client.GetPullRequest(context.Background(), "openclaw", "openclaw", 999)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestNewSchemaClientReadsConfiguredSchema(t *testing.T) {
	db := openMirrorTestDB(t)
	seedMirrorRows(t, db)

	client := NewSchemaClient(db, "main")
	repository, err := client.GetRepository(context.Background(), "openclaw", "openclaw")
	require.NoError(t, err)
	require.Equal(t, "openclaw/openclaw", repository.FullName)
}

func openMirrorTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&mirror.User{}, &mirror.Repository{}, &mirror.Issue{}, &mirror.PullRequest{}))
	return db
}

func seedMirrorRows(t *testing.T, db *gorm.DB) {
	t.Helper()
	now := time.Date(2026, 4, 25, 8, 0, 0, 0, time.UTC)
	owner := mirror.User{GitHubID: 1, Login: "openclaw"}
	require.NoError(t, db.Create(&owner).Error)
	author := mirror.User{GitHubID: 2, Login: "octocat"}
	require.NoError(t, db.Create(&author).Error)
	repository := mirror.Repository{
		GitHubID:   1103012935,
		OwnerID:    &owner.ID,
		OwnerLogin: "openclaw",
		Name:       "openclaw",
		FullName:   "openclaw/openclaw",
		HTMLURL:    "https://github.com/openclaw/openclaw",
		Visibility: "public",
	}
	require.NoError(t, db.Create(&repository).Error)
	issue := mirror.Issue{
		RepositoryID:    repository.ID,
		GitHubID:        11,
		Number:          11,
		Title:           "Issue title",
		State:           "open",
		AuthorID:        &author.ID,
		HTMLURL:         "https://github.com/openclaw/openclaw/issues/11",
		GitHubUpdatedAt: now,
	}
	require.NoError(t, db.Create(&issue).Error)
	pullIssue := mirror.Issue{
		RepositoryID:    repository.ID,
		GitHubID:        22,
		Number:          22,
		Title:           "Pull title",
		State:           "open",
		AuthorID:        &author.ID,
		HTMLURL:         "https://github.com/openclaw/openclaw/pull/22",
		GitHubUpdatedAt: now,
		IsPullRequest:   true,
	}
	require.NoError(t, db.Create(&pullIssue).Error)
	pull := mirror.PullRequest{
		IssueID:         pullIssue.ID,
		RepositoryID:    repository.ID,
		GitHubID:        22,
		Number:          22,
		State:           "open",
		HTMLURL:         "https://github.com/openclaw/openclaw/pull/22",
		GitHubUpdatedAt: now,
	}
	require.NoError(t, db.Create(&pull).Error)
}
