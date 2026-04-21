package core

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dutifuldev/prtags/internal/database"
	"github.com/dutifuldev/prtags/internal/githubapi"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type commentSyncDispatcherStub struct {
	mu    sync.Mutex
	calls []commentSyncDispatchCall
}

type commentSyncDispatchCall struct {
	SyncTargetID    uint
	DesiredRevision int
	Verify          bool
}

func (d *commentSyncDispatcherStub) EnqueueGroupCommentReconcileTx(_ *gorm.DB, syncTargetID uint, desiredRevision int, _ time.Time, verify bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, commentSyncDispatchCall{
		SyncTargetID:    syncTargetID,
		DesiredRevision: desiredRevision,
		Verify:          verify,
	})
	return nil
}

func (d *commentSyncDispatcherStub) snapshot() []commentSyncDispatchCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]commentSyncDispatchCall, len(d.calls))
	copy(out, d.calls)
	return out
}

type githubCommentStore struct {
	mu       sync.Mutex
	nextID   int64
	comments map[int][]githubapi.IssueComment
}

func newGitHubCommentStore() *githubCommentStore {
	return &githubCommentStore{
		nextID:   1000,
		comments: map[int][]githubapi.IssueComment{},
	}
}

func (s *githubCommentStore) list(issueNumber int) []githubapi.IssueComment {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]githubapi.IssueComment, len(s.comments[issueNumber]))
	copy(out, s.comments[issueNumber])
	return out
}

func (s *githubCommentStore) create(issueNumber int, body string) githubapi.IssueComment {
	s.mu.Lock()
	defer s.mu.Unlock()
	comment := githubapi.IssueComment{
		ID:      s.nextID,
		Body:    body,
		HTMLURL: "https://github.com/acme/widgets/issues/comment/" + strconv.FormatInt(s.nextID, 10),
	}
	comment.User.Login = "prtags-comment-sync"
	s.nextID++
	s.comments[issueNumber] = append(s.comments[issueNumber], comment)
	return comment
}

func (s *githubCommentStore) update(commentID int64, body string) (githubapi.IssueComment, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for issueNumber, comments := range s.comments {
		for i, comment := range comments {
			if comment.ID == commentID {
				comment.Body = body
				s.comments[issueNumber][i] = comment
				return comment, true
			}
		}
	}
	return githubapi.IssueComment{}, false
}

func (s *githubCommentStore) get(commentID int64) (githubapi.IssueComment, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, comments := range s.comments {
		for _, comment := range comments {
			if comment.ID == commentID {
				return comment, true
			}
		}
	}
	return githubapi.IssueComment{}, false
}

func (s *githubCommentStore) delete(commentID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for issueNumber, comments := range s.comments {
		for i, comment := range comments {
			if comment.ID == commentID {
				s.comments[issueNumber] = append(comments[:i], comments[i+1:]...)
				return true
			}
		}
	}
	return false
}

func TestCommentSyncTriggerAndReconcileCreatesAndUpdatesComments(t *testing.T) {
	ctx := context.Background()
	db := openCommentSyncTestDB(t)
	group := seedCommentSyncGroup(t, db)
	dispatcher := &commentSyncDispatcherStub{}
	store, client := newTestGitHubCommentClient(t)
	syncService := NewCommentSyncService(db, client, dispatcher)

	result, err := syncService.TriggerGroupSync(ctx, group.PublicID)
	require.NoError(t, err)
	require.Equal(t, group.PublicID, result.GroupID)
	require.Equal(t, 2, result.SyncTargetCount)

	var rows []database.GroupCommentSyncTarget
	require.NoError(t, db.WithContext(ctx).Order("object_number ASC").Find(&rows).Error)
	require.Len(t, rows, 2)
	require.Len(t, dispatcher.snapshot(), 2)

	for _, row := range rows {
		require.NoError(t, syncService.Reconcile(ctx, row.ID, row.DesiredRevision, false))
	}

	issueComments := store.list(11)
	require.Len(t, issueComments, 1)
	require.Contains(t, issueComments[0].Body, "Title: Auth reliability")
	require.Contains(t, issueComments[0].Body, "[#22](https://github.com/acme/widgets/pull/22)")
	require.NotContains(t, issueComments[0].Body, "[#11]")

	prComments := store.list(22)
	require.Len(t, prComments, 1)
	require.Contains(t, prComments[0].Body, "[#11](https://github.com/acme/widgets/issues/11)")
	require.NotContains(t, prComments[0].Body, "[#22]")

	require.NoError(t, db.WithContext(ctx).Model(&database.Group{}).Where("id = ?", group.ID).Updates(map[string]any{
		"title":  "Auth reliability follow-up",
		"status": "closed",
	}).Error)

	result, err = syncService.TriggerGroupSync(ctx, group.PublicID)
	require.NoError(t, err)
	require.Equal(t, 2, result.SyncTargetCount)

	require.NoError(t, db.WithContext(ctx).Order("object_number ASC").Find(&rows).Error)
	for _, row := range rows {
		require.NoError(t, syncService.Reconcile(ctx, row.ID, row.DesiredRevision, false))
	}

	issueComments = store.list(11)
	require.Len(t, issueComments, 1)
	require.Contains(t, issueComments[0].Body, "Title: Auth reliability follow-up")
	require.Contains(t, issueComments[0].Body, "Status: closed")
}

func TestCommentSyncDeletesCommentsWhenGroupBecomesSingleton(t *testing.T) {
	ctx := context.Background()
	db := openCommentSyncTestDB(t)
	group := seedCommentSyncGroup(t, db)
	dispatcher := &commentSyncDispatcherStub{}
	store, client := newTestGitHubCommentClient(t)
	syncService := NewCommentSyncService(db, client, dispatcher)

	_, err := syncService.TriggerGroupSync(ctx, group.PublicID)
	require.NoError(t, err)

	var rows []database.GroupCommentSyncTarget
	require.NoError(t, db.WithContext(ctx).Order("object_number ASC").Find(&rows).Error)
	for _, row := range rows {
		require.NoError(t, syncService.Reconcile(ctx, row.ID, row.DesiredRevision, false))
	}
	require.Len(t, store.list(11), 1)
	require.Len(t, store.list(22), 1)

	require.NoError(t, db.WithContext(ctx).
		Where("group_id = ? AND object_type = ? AND object_number = ?", group.ID, "pull_request", 22).
		Delete(&database.GroupMember{}).Error)

	_, err = syncService.TriggerGroupSync(ctx, group.PublicID)
	require.NoError(t, err)
	require.NoError(t, db.WithContext(ctx).Order("object_number ASC").Find(&rows).Error)
	for _, row := range rows {
		require.NoError(t, syncService.Reconcile(ctx, row.ID, row.DesiredRevision, false))
	}

	require.Empty(t, store.list(11))
	require.Empty(t, store.list(22))
}

func TestCommentSyncReconcileAppliesLatestStateWhenQueuedRevisionIsStale(t *testing.T) {
	ctx := context.Background()
	db := openCommentSyncTestDB(t)
	group := seedCommentSyncGroup(t, db)
	dispatcher := &commentSyncDispatcherStub{}
	store, client := newTestGitHubCommentClient(t)
	syncService := NewCommentSyncService(db, client, dispatcher)

	_, err := syncService.TriggerGroupSync(ctx, group.PublicID)
	require.NoError(t, err)

	var rows []database.GroupCommentSyncTarget
	require.NoError(t, db.WithContext(ctx).Order("object_number ASC").Find(&rows).Error)
	require.Len(t, rows, 2)

	initialRevision := rows[0].DesiredRevision

	require.NoError(t, db.WithContext(ctx).Model(&database.Group{}).Where("id = ?", group.ID).Updates(map[string]any{
		"title": "Auth reliability v2",
	}).Error)
	_, err = syncService.TriggerGroupSync(ctx, group.PublicID)
	require.NoError(t, err)
	require.NoError(t, db.WithContext(ctx).Order("object_number ASC").Find(&rows).Error)
	require.Greater(t, rows[0].DesiredRevision, initialRevision)

	require.NoError(t, syncService.Reconcile(ctx, rows[0].ID, initialRevision, false))

	issueComments := store.list(11)
	require.Len(t, issueComments, 1)
	require.Contains(t, issueComments[0].Body, "Title: Auth reliability v2")

	var refreshed database.GroupCommentSyncTarget
	require.NoError(t, db.WithContext(ctx).First(&refreshed, rows[0].ID).Error)
	require.Equal(t, refreshed.DesiredRevision, refreshed.AppliedRevision)
}

func openCommentSyncTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&database.Group{},
		&database.GroupMember{},
		&database.TargetProjection{},
		&database.GroupCommentSyncTarget{},
	))
	return db
}

func seedCommentSyncGroup(t *testing.T, db *gorm.DB) database.Group {
	t.Helper()
	group := database.Group{
		PublicID:           "steady-otter-k4m2",
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
	require.NoError(t, db.Create(&database.GroupMember{
		GroupID:            group.ID,
		GitHubRepositoryID: group.GitHubRepositoryID,
		ObjectType:         "issue",
		ObjectNumber:       11,
		TargetKey:          objectTargetKey(group.GitHubRepositoryID, "issue", 11),
		AddedBy:            "tester",
		AddedAt:            time.Now().UTC(),
	}).Error)
	require.NoError(t, db.Create(&database.GroupMember{
		GroupID:            group.ID,
		GitHubRepositoryID: group.GitHubRepositoryID,
		ObjectType:         "pull_request",
		ObjectNumber:       22,
		TargetKey:          objectTargetKey(group.GitHubRepositoryID, "pull_request", 22),
		AddedBy:            "tester",
		AddedAt:            time.Now().UTC(),
	}).Error)
	require.NoError(t, db.Create(&database.TargetProjection{
		GitHubRepositoryID: group.GitHubRepositoryID,
		RepositoryOwner:    group.RepositoryOwner,
		RepositoryName:     group.RepositoryName,
		TargetType:         "issue",
		ObjectNumber:       11,
		Title:              "Auth retries are flaky",
		State:              "open",
		HTMLURL:            "https://github.com/acme/widgets/issues/11",
		SourceUpdatedAt:    time.Now().UTC(),
		FetchedAt:          time.Now().UTC(),
	}).Error)
	require.NoError(t, db.Create(&database.TargetProjection{
		GitHubRepositoryID: group.GitHubRepositoryID,
		RepositoryOwner:    group.RepositoryOwner,
		RepositoryName:     group.RepositoryName,
		TargetType:         "pull_request",
		ObjectNumber:       22,
		Title:              "Retry ACP turns safely",
		State:              "open",
		HTMLURL:            "https://github.com/acme/widgets/pull/22",
		SourceUpdatedAt:    time.Now().UTC(),
		FetchedAt:          time.Now().UTC(),
	}).Error)
	return group
}

func newTestGitHubCommentClient(t *testing.T) (*githubCommentStore, *githubapi.Client) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})

	store := newGitHubCommentStore()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/123/access_tokens":
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"token":      "installation-token",
				"expires_at": time.Now().UTC().Add(30 * time.Minute),
			}))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/issues/11/comments":
			require.NoError(t, json.NewEncoder(w).Encode(store.list(11)))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/issues/22/comments":
			require.NoError(t, json.NewEncoder(w).Encode(store.list(22)))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widgets/issues/11/comments":
			var payload map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			require.NoError(t, json.NewEncoder(w).Encode(store.create(11, payload["body"])))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widgets/issues/22/comments":
			var payload map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			require.NoError(t, json.NewEncoder(w).Encode(store.create(22, payload["body"])))
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/repos/acme/widgets/issues/comments/"):
			commentID, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/repos/acme/widgets/issues/comments/"), 10, 64)
			require.NoError(t, err)
			var payload map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			comment, ok := store.update(commentID, payload["body"])
			if !ok {
				http.NotFound(w, r)
				return
			}
			require.NoError(t, json.NewEncoder(w).Encode(comment))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/acme/widgets/issues/comments/"):
			commentID, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/repos/acme/widgets/issues/comments/"), 10, 64)
			require.NoError(t, err)
			comment, ok := store.get(commentID)
			if !ok {
				http.NotFound(w, r)
				return
			}
			require.NoError(t, json.NewEncoder(w).Encode(comment))
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/repos/acme/widgets/issues/comments/"):
			commentID, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/repos/acme/widgets/issues/comments/"), 10, 64)
			require.NoError(t, err)
			if !store.delete(commentID) {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	client := githubapi.NewClient(server.URL, githubapi.AuthConfig{
		AppID:          "42",
		InstallationID: "123",
		PrivateKeyPEM:  string(keyPEM),
	})
	return store, client
}
