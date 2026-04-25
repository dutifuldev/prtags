package core

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
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
	mu           sync.Mutex
	calls        []commentSyncDispatchCall
	reconcileErr error
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
	return d.reconcileErr
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
	syncService := NewCommentSyncService(db, testMirrorClient{}, client, dispatcher)

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
	require.Contains(t, issueComments[0].Body, "[#11*](https://github.com/acme/widgets/issues/11)")
	require.Contains(t, issueComments[0].Body, "[#22](https://github.com/acme/widgets/pull/22)")
	require.Contains(t, issueComments[0].Body, "`*` This issue")

	prComments := store.list(22)
	require.Len(t, prComments, 1)
	require.Contains(t, prComments[0].Body, "[#22*](https://github.com/acme/widgets/pull/22)")
	require.Contains(t, prComments[0].Body, "[#11](https://github.com/acme/widgets/issues/11)")
	require.Contains(t, prComments[0].Body, "`*` This PR")

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
	syncService := NewCommentSyncService(db, testMirrorClient{}, client, dispatcher)

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
	syncService := NewCommentSyncService(db, testMirrorClient{}, client, dispatcher)

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

func TestCommentSyncMarksPermissionDeniedWithoutHammeringGitHub(t *testing.T) {
	ctx := context.Background()
	db := openCommentSyncTestDB(t)
	group := seedCommentSyncGroup(t, db)
	dispatcher := &commentSyncDispatcherStub{}
	_, client := newTestGitHubCommentClientWithHandler(t, func(w http.ResponseWriter, r *http.Request, _ *githubCommentStore) bool {
		if strings.HasPrefix(r.URL.Path, "/repos/acme/widgets/issues/") {
			w.WriteHeader(http.StatusForbidden)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"message": "Resource not accessible by integration",
			}))
			return true
		}
		return false
	})
	syncService := NewCommentSyncService(db, testMirrorClient{}, client, dispatcher)

	_, err := syncService.TriggerGroupSync(ctx, group.PublicID)
	require.NoError(t, err)

	var row database.GroupCommentSyncTarget
	require.NoError(t, db.WithContext(ctx).Where("object_type = ? AND object_number = ?", "issue", 11).First(&row).Error)

	require.NoError(t, syncService.Reconcile(ctx, row.ID, row.DesiredRevision, false))

	require.NoError(t, db.WithContext(ctx).First(&row, row.ID).Error)
	require.Equal(t, "permission_denied", row.LastErrorKind)
	require.NotNil(t, row.LastErrorAt)
	require.Contains(t, row.LastError, "Resource not accessible by integration")
}

func TestCommentSyncDeletesDuplicateManagedComments(t *testing.T) {
	ctx := context.Background()
	db := openCommentSyncTestDB(t)
	group := seedCommentSyncGroup(t, db)
	dispatcher := &commentSyncDispatcherStub{}
	store, client := newTestGitHubCommentClient(t)
	syncService := NewCommentSyncService(db, testMirrorClient{}, client, dispatcher)

	_, err := syncService.TriggerGroupSync(ctx, group.PublicID)
	require.NoError(t, err)

	var row database.GroupCommentSyncTarget
	require.NoError(t, db.WithContext(ctx).Where("object_type = ? AND object_number = ?", "issue", 11).First(&row).Error)

	marker := markerForTarget(group.PublicID, group.GitHubRepositoryID, row.ObjectType, row.ObjectNumber)
	store.create(11, marker+"\nold duplicate a")
	store.create(11, marker+"\nold duplicate b")

	require.NoError(t, syncService.Reconcile(ctx, row.ID, row.DesiredRevision, false))

	issueComments := store.list(11)
	require.Len(t, issueComments, 1)
	require.Contains(t, issueComments[0].Body, "Title: Auth reliability")
	require.Contains(t, issueComments[0].Body, "`*` This issue")
}

func TestCommentSyncRetryableFailureSucceedsLater(t *testing.T) {
	ctx := context.Background()
	db := openCommentSyncTestDB(t)
	group := seedCommentSyncGroup(t, db)
	dispatcher := &commentSyncDispatcherStub{}
	var failCreateOnce sync.Once
	store, client := newTestGitHubCommentClientWithHandler(t, func(w http.ResponseWriter, r *http.Request, _ *githubCommentStore) bool {
		if r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widgets/issues/11/comments" {
			failed := false
			failCreateOnce.Do(func() {
				failed = true
			})
			if failed {
				w.WriteHeader(http.StatusInternalServerError)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"message": "temporary github failure"}))
				return true
			}
		}
		return false
	})
	syncService := NewCommentSyncService(db, testMirrorClient{}, client, dispatcher)

	_, err := syncService.TriggerGroupSync(ctx, group.PublicID)
	require.NoError(t, err)

	var row database.GroupCommentSyncTarget
	require.NoError(t, db.WithContext(ctx).Where("object_type = ? AND object_number = ?", "issue", 11).First(&row).Error)

	err = syncService.Reconcile(ctx, row.ID, row.DesiredRevision, false)
	require.Error(t, err)

	var failedRow database.GroupCommentSyncTarget
	require.NoError(t, db.WithContext(ctx).First(&failedRow, row.ID).Error)
	require.Equal(t, "", failedRow.LastErrorKind)
	require.Contains(t, failedRow.LastError, "temporary github failure")
	require.NotNil(t, failedRow.LastErrorAt)
	require.Nil(t, failedRow.GitHubCommentID)

	require.NoError(t, syncService.Reconcile(ctx, row.ID, row.DesiredRevision, false))

	var succeededRow database.GroupCommentSyncTarget
	require.NoError(t, db.WithContext(ctx).First(&succeededRow, row.ID).Error)
	require.Equal(t, succeededRow.DesiredRevision, succeededRow.AppliedRevision)
	require.Empty(t, succeededRow.LastError)
	require.Empty(t, succeededRow.LastErrorKind)
	require.Nil(t, succeededRow.LastErrorAt)
	require.Len(t, store.list(11), 1)
}

func TestCommentSyncHelperPaths(t *testing.T) {
	ctx := context.Background()
	db := openCommentSyncTestDB(t)
	group := seedCommentSyncGroup(t, db)
	dispatcher := &commentSyncDispatcherStub{}
	store, client := newTestGitHubCommentClient(t)
	syncService := NewCommentSyncService(db, testMirrorClient{}, client, nil)
	require.True(t, syncService.Enabled())
	syncService.SetDispatcher(dispatcher)

	event := eventForGroup(group.ID)
	require.NoError(t, db.Create(&event).Error)
	require.NoError(t, syncService.ProjectEvent(ctx, event.ID))
	require.ErrorIs(t, syncService.ProjectEvent(ctx, 0), gorm.ErrRecordNotFound)

	var rows []database.GroupCommentSyncTarget
	require.NoError(t, db.Order("object_number ASC").Find(&rows).Error)
	require.Len(t, rows, 2)

	row := rows[0]
	row.GitHubCommentID = int64Ptr(4040)
	require.False(t, isPermissionDenied(nil, false))
	require.True(t, isPermissionDenied(&githubapi.Error{StatusCode: http.StatusForbidden}, false))
	require.True(t, isPermissionDenied(&githubapi.Error{StatusCode: http.StatusNotFound}, false))
	require.False(t, isPermissionDenied(&githubapi.Error{StatusCode: http.StatusNotFound}, true))
	require.Equal(t, "https://github.com/acme/widgets/issues/11", issueURL("acme", "widgets", "issue", 11, ""))
	require.Equal(t, "fallback", issueURL("acme", "widgets", "issue", 11, " fallback "))
	require.Equal(t, "hello \\| world", markdownCell(" hello | world "))

	handled, err := syncService.handleDeleteCommentError(ctx, &row, &githubapi.Error{StatusCode: http.StatusNotFound})
	require.False(t, handled)
	require.NoError(t, err)
	require.Nil(t, row.GitHubCommentID)

	row.GitHubCommentID = int64Ptr(4041)
	handled, err = syncService.handleDeleteCommentError(ctx, &row, &githubapi.Error{StatusCode: http.StatusForbidden, Message: "Resource not accessible by integration"})
	require.True(t, handled)
	require.NoError(t, err)

	row.GitHubCommentID = int64Ptr(4042)
	handled, err = syncService.handleDeleteCommentError(ctx, &row, errors.New("boom"))
	require.True(t, handled)
	require.ErrorContains(t, err, "boom")

	marker := markerForTarget(group.PublicID, group.GitHubRepositoryID, "issue", 11)
	first := store.create(11, marker+"\nfirst")
	_ = store.create(11, marker+"\nsecond")
	managed, duplicates, err := syncService.findManagedComments(ctx, group, 11, marker)
	require.NoError(t, err)
	require.NotNil(t, managed)
	require.Equal(t, first.ID, managed.ID)
	require.Len(t, duplicates, 1)

	require.NoError(t, syncService.deleteExtraComments(ctx, group, []githubapi.IssueComment{
		{ID: 99999},
		{ID: managed.ID},
	}))
	require.NotEmpty(t, store.list(11))

	body, _, shouldExist, err := syncService.renderCommentBody(ctx, group, "issue", 11)
	require.NoError(t, err)
	require.True(t, shouldExist)
	require.Contains(t, body, "Related work from PRtags group")
	require.Contains(t, body, "`*` This issue")

	require.NoError(t, db.Where("object_number = ?", 22).Delete(&database.GroupMember{}).Error)
	body, _, shouldExist, err = syncService.renderCommentBody(ctx, group, "issue", 11)
	require.NoError(t, err)
	require.False(t, shouldExist)
	require.Empty(t, body)
}

func TestCommentSyncReconcileVerifyRecreatesMissingComment(t *testing.T) {
	ctx := context.Background()
	db := openCommentSyncTestDB(t)
	group := seedCommentSyncGroup(t, db)
	dispatcher := &commentSyncDispatcherStub{}
	store, client := newTestGitHubCommentClient(t)
	syncService := NewCommentSyncService(db, testMirrorClient{}, client, dispatcher)

	_, err := syncService.TriggerGroupSync(ctx, group.PublicID)
	require.NoError(t, err)

	var row database.GroupCommentSyncTarget
	require.NoError(t, db.WithContext(ctx).Where("object_type = ? AND object_number = ?", "issue", 11).First(&row).Error)
	require.NoError(t, db.Model(&database.GroupCommentSyncTarget{}).Where("id = ?", row.ID).Update("github_comment_id", 99999).Error)

	require.NoError(t, syncService.Reconcile(ctx, row.ID, row.DesiredRevision, true))

	comments := store.list(11)
	require.Len(t, comments, 1)
	require.Contains(t, comments[0].Body, "Title: Auth reliability")
}

func TestCommentSyncReconcileUpdatesExistingManagedComment(t *testing.T) {
	ctx := context.Background()
	db := openCommentSyncTestDB(t)
	group := seedCommentSyncGroup(t, db)
	dispatcher := &commentSyncDispatcherStub{}
	store, client := newTestGitHubCommentClient(t)
	syncService := NewCommentSyncService(db, testMirrorClient{}, client, dispatcher)

	_, err := syncService.TriggerGroupSync(ctx, group.PublicID)
	require.NoError(t, err)

	var row database.GroupCommentSyncTarget
	require.NoError(t, db.WithContext(ctx).Where("object_type = ? AND object_number = ?", "issue", 11).First(&row).Error)
	marker := markerForTarget(group.PublicID, group.GitHubRepositoryID, row.ObjectType, row.ObjectNumber)
	existing := store.create(11, marker+"\noutdated")

	require.NoError(t, syncService.Reconcile(ctx, row.ID, row.DesiredRevision, false))

	comments := store.list(11)
	require.Len(t, comments, 1)
	require.Equal(t, existing.ID, comments[0].ID)
	require.Contains(t, comments[0].Body, "Related work from PRtags group")
}

func TestCommentSyncReconcileDeletePathRemovesManagedComments(t *testing.T) {
	ctx := context.Background()
	db := openCommentSyncTestDB(t)
	group := seedCommentSyncGroup(t, db)
	dispatcher := &commentSyncDispatcherStub{}
	store, client := newTestGitHubCommentClient(t)
	syncService := NewCommentSyncService(db, testMirrorClient{}, client, dispatcher)

	_, err := syncService.TriggerGroupSync(ctx, group.PublicID)
	require.NoError(t, err)

	var row database.GroupCommentSyncTarget
	require.NoError(t, db.WithContext(ctx).Where("object_type = ? AND object_number = ?", "issue", 11).First(&row).Error)
	marker := markerForTarget(group.PublicID, group.GitHubRepositoryID, row.ObjectType, row.ObjectNumber)
	comment := store.create(11, marker+"\nmanaged")
	store.create(11, marker+"\nduplicate")
	require.NoError(t, db.Model(&database.GroupCommentSyncTarget{}).Where("id = ?", row.ID).Updates(map[string]any{
		"github_comment_id": comment.ID,
		"desired_deleted":   true,
		"desired_revision":  row.DesiredRevision + 1,
	}).Error)

	require.NoError(t, syncService.Reconcile(ctx, row.ID, row.DesiredRevision+1, false))
	require.Empty(t, store.list(11))
}

func TestCommentSyncDisabledPaths(t *testing.T) {
	ctx := context.Background()
	db := openCommentSyncTestDB(t)
	group := seedCommentSyncGroup(t, db)
	syncService := NewCommentSyncService(db, testMirrorClient{}, nil, nil)
	require.False(t, syncService.Enabled())

	_, err := syncService.TriggerGroupSync(ctx, group.PublicID)
	var fail *FailError
	require.ErrorAs(t, err, &fail)
	require.Equal(t, 503, fail.StatusCode)

	require.NoError(t, syncService.ProjectEvent(ctx, 1))
	require.NoError(t, syncService.Repair(ctx, 0))
}

func TestCommentSyncReconcileShortCircuitAndCanonicalPaths(t *testing.T) {
	ctx := context.Background()
	db := openCommentSyncTestDB(t)
	group := seedCommentSyncGroup(t, db)
	dispatcher := &commentSyncDispatcherStub{}
	store, client := newTestGitHubCommentClient(t)
	syncService := NewCommentSyncService(db, testMirrorClient{}, client, dispatcher)

	_, err := syncService.TriggerGroupSync(ctx, group.PublicID)
	require.NoError(t, err)

	var row database.GroupCommentSyncTarget
	require.NoError(t, db.WithContext(ctx).Where("object_type = ? AND object_number = ?", "issue", 11).First(&row).Error)
	body, hash, shouldExist, err := syncService.renderCommentBody(ctx, group, row.ObjectType, row.ObjectNumber)
	require.NoError(t, err)
	require.True(t, shouldExist)

	require.NoError(t, db.Model(&database.GroupCommentSyncTarget{}).Where("id = ?", row.ID).Updates(map[string]any{
		"comment_body_hash": hash,
		"applied_revision":  row.DesiredRevision,
	}).Error)
	require.NoError(t, syncService.Reconcile(ctx, row.ID, row.DesiredRevision, false))
	require.Empty(t, store.list(11))

	comment := store.create(11, body)
	marker := markerForTarget(group.PublicID, group.GitHubRepositoryID, row.ObjectType, row.ObjectNumber)
	comment.Body = marker + "\nold body"
	updated, ok := store.update(comment.ID, comment.Body)
	require.True(t, ok)
	require.Contains(t, updated.Body, "old body")

	require.NoError(t, db.Model(&database.GroupCommentSyncTarget{}).Where("id = ?", row.ID).Updates(map[string]any{
		"github_comment_id": nil,
		"comment_body_hash": "",
		"applied_revision":  0,
	}).Error)
	require.NoError(t, syncService.Reconcile(ctx, row.ID, row.DesiredRevision, false))

	comments := store.list(11)
	require.Len(t, comments, 1)
	require.Equal(t, comment.ID, comments[0].ID)
	require.Contains(t, comments[0].Body, "Related work from PRtags group")
}

func TestCommentSyncDeleteAndVerifyErrorBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("verify permission denied marks sync row", func(t *testing.T) {
		db := openCommentSyncTestDB(t)
		group := seedCommentSyncGroup(t, db)
		dispatcher := &commentSyncDispatcherStub{}
		_, client := newTestGitHubCommentClientWithHandler(t, func(w http.ResponseWriter, r *http.Request, _ *githubCommentStore) bool {
			if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/acme/widgets/issues/comments/") {
				w.WriteHeader(http.StatusForbidden)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"message": "Resource not accessible by integration"}))
				return true
			}
			return false
		})
		syncService := NewCommentSyncService(db, testMirrorClient{}, client, dispatcher)

		_, err := syncService.TriggerGroupSync(ctx, group.PublicID)
		require.NoError(t, err)

		var row database.GroupCommentSyncTarget
		require.NoError(t, db.WithContext(ctx).Where("object_type = ? AND object_number = ?", "issue", 11).First(&row).Error)
		require.NoError(t, db.Model(&database.GroupCommentSyncTarget{}).Where("id = ?", row.ID).Update("github_comment_id", 99999).Error)

		require.NoError(t, syncService.Reconcile(ctx, row.ID, row.DesiredRevision, true))
		require.NoError(t, db.WithContext(ctx).First(&row, row.ID).Error)
		require.Equal(t, "permission_denied", row.LastErrorKind)
	})

	t.Run("delete path ignores missing comment and succeeds", func(t *testing.T) {
		db := openCommentSyncTestDB(t)
		group := seedCommentSyncGroup(t, db)
		dispatcher := &commentSyncDispatcherStub{}
		store, client := newTestGitHubCommentClientWithHandler(t, func(w http.ResponseWriter, r *http.Request, _ *githubCommentStore) bool {
			if r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/issues/comments/99999") {
				http.NotFound(w, r)
				return true
			}
			return false
		})
		syncService := NewCommentSyncService(db, testMirrorClient{}, client, dispatcher)

		_, err := syncService.TriggerGroupSync(ctx, group.PublicID)
		require.NoError(t, err)

		var row database.GroupCommentSyncTarget
		require.NoError(t, db.WithContext(ctx).Where("object_type = ? AND object_number = ?", "issue", 11).First(&row).Error)
		require.NoError(t, db.Model(&database.GroupCommentSyncTarget{}).Where("id = ?", row.ID).Updates(map[string]any{
			"github_comment_id": 99999,
			"desired_deleted":   true,
			"desired_revision":  row.DesiredRevision + 1,
		}).Error)

		require.NoError(t, syncService.Reconcile(ctx, row.ID, row.DesiredRevision+1, false))
		require.Empty(t, store.list(11))
		require.NoError(t, db.WithContext(ctx).First(&row, row.ID).Error)
		require.Equal(t, row.DesiredRevision, row.AppliedRevision)
	})

	t.Run("delete duplicate failure marks sync failed", func(t *testing.T) {
		db := openCommentSyncTestDB(t)
		group := seedCommentSyncGroup(t, db)
		dispatcher := &commentSyncDispatcherStub{}
		store, client := newTestGitHubCommentClientWithHandler(t, func(w http.ResponseWriter, r *http.Request, store *githubCommentStore) bool {
			if r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/issues/comments/1001") {
				w.WriteHeader(http.StatusInternalServerError)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"message": "delete failed"}))
				return true
			}
			return false
		})
		syncService := NewCommentSyncService(db, testMirrorClient{}, client, dispatcher)

		_, err := syncService.TriggerGroupSync(ctx, group.PublicID)
		require.NoError(t, err)

		var row database.GroupCommentSyncTarget
		require.NoError(t, db.WithContext(ctx).Where("object_type = ? AND object_number = ?", "issue", 11).First(&row).Error)
		marker := markerForTarget(group.PublicID, group.GitHubRepositoryID, row.ObjectType, row.ObjectNumber)
		managed := store.create(11, marker+"\nmanaged")
		_ = store.create(11, marker+"\nduplicate")
		require.NoError(t, db.Model(&database.GroupCommentSyncTarget{}).Where("id = ?", row.ID).Updates(map[string]any{
			"github_comment_id": managed.ID,
			"desired_deleted":   true,
			"desired_revision":  row.DesiredRevision + 1,
		}).Error)

		err = syncService.Reconcile(ctx, row.ID, row.DesiredRevision+1, false)
		require.Error(t, err)
		require.NoError(t, db.WithContext(ctx).First(&row, row.ID).Error)
		require.Contains(t, row.LastError, "delete failed")
	})
}

func TestCommentSyncAdditionalReconcileBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("canonical matching body marks success without update", func(t *testing.T) {
		db := openCommentSyncTestDB(t)
		group := seedCommentSyncGroup(t, db)
		dispatcher := &commentSyncDispatcherStub{}
		store, client := newTestGitHubCommentClient(t)
		syncService := NewCommentSyncService(db, testMirrorClient{}, client, dispatcher)

		_, err := syncService.TriggerGroupSync(ctx, group.PublicID)
		require.NoError(t, err)

		var row database.GroupCommentSyncTarget
		require.NoError(t, db.WithContext(ctx).Where("object_type = ? AND object_number = ?", "issue", 11).First(&row).Error)
		body, hash, _, err := syncService.renderCommentBody(ctx, group, row.ObjectType, row.ObjectNumber)
		require.NoError(t, err)
		comment := store.create(11, body)
		require.NoError(t, db.Model(&database.GroupCommentSyncTarget{}).Where("id = ?", row.ID).Updates(map[string]any{
			"github_comment_id": comment.ID,
			"comment_body_hash": "",
		}).Error)

		require.NoError(t, syncService.Reconcile(ctx, row.ID, row.DesiredRevision, false))
		require.Len(t, store.list(11), 1)

		require.NoError(t, db.WithContext(ctx).First(&row, row.ID).Error)
		require.Equal(t, hash, row.CommentBodyHash)
	})

	t.Run("projection helper updates rows and schedules reconcile", func(t *testing.T) {
		db := openCommentSyncTestDB(t)
		group := seedCommentSyncGroup(t, db)
		dispatcher := &commentSyncDispatcherStub{}
		_, client := newTestGitHubCommentClient(t)
		syncService := NewCommentSyncService(db, testMirrorClient{}, client, dispatcher)

		_, err := syncService.TriggerGroupSync(ctx, group.PublicID)
		require.NoError(t, err)

		var rows []database.GroupCommentSyncTarget
		require.NoError(t, db.WithContext(ctx).Order("object_number ASC").Find(&rows).Error)
		require.Len(t, rows, 2)

		now := time.Now().UTC()
		scheduledAt := now.Add(10 * time.Second)
		require.NoError(t, syncService.updateCommentSyncTarget(db, rows[0].ID, rows[0].DesiredRevision+1, true, rows[0].TargetKey, now, scheduledAt))

		current := map[string]database.GroupMember{
			commentSyncTargetIdentity("issue", 11): {
				GroupID:            group.ID,
				GitHubRepositoryID: group.GitHubRepositoryID,
				ObjectType:         "issue",
				ObjectNumber:       11,
				TargetKey:          objectTargetKey(group.GitHubRepositoryID, "issue", 11),
			},
		}
		affected, err := syncService.deleteMissingCommentSyncTargets(db, current, rows, now, scheduledAt)
		require.NoError(t, err)
		require.Equal(t, 1, affected)
		require.NotEmpty(t, dispatcher.snapshot())
	})

	t.Run("list comments permission denied marks sync row", func(t *testing.T) {
		db := openCommentSyncTestDB(t)
		group := seedCommentSyncGroup(t, db)
		dispatcher := &commentSyncDispatcherStub{}
		_, client := newTestGitHubCommentClientWithHandler(t, func(w http.ResponseWriter, r *http.Request, _ *githubCommentStore) bool {
			if r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/issues/11/comments" {
				w.WriteHeader(http.StatusForbidden)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"message": "Resource not accessible by integration"}))
				return true
			}
			return false
		})
		syncService := NewCommentSyncService(db, testMirrorClient{}, client, dispatcher)

		_, err := syncService.TriggerGroupSync(ctx, group.PublicID)
		require.NoError(t, err)

		var row database.GroupCommentSyncTarget
		require.NoError(t, db.WithContext(ctx).Where("object_type = ? AND object_number = ?", "issue", 11).First(&row).Error)
		require.NoError(t, syncService.Reconcile(ctx, row.ID, row.DesiredRevision, false))
		require.NoError(t, db.WithContext(ctx).First(&row, row.ID).Error)
		require.Equal(t, "permission_denied", row.LastErrorKind)
	})

	t.Run("update permission denied marks sync row", func(t *testing.T) {
		db := openCommentSyncTestDB(t)
		group := seedCommentSyncGroup(t, db)
		dispatcher := &commentSyncDispatcherStub{}
		store, client := newTestGitHubCommentClientWithHandler(t, func(w http.ResponseWriter, r *http.Request, _ *githubCommentStore) bool {
			if r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/issues/comments/1000") {
				w.WriteHeader(http.StatusForbidden)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"message": "Resource not accessible by integration"}))
				return true
			}
			return false
		})
		syncService := NewCommentSyncService(db, testMirrorClient{}, client, dispatcher)

		_, err := syncService.TriggerGroupSync(ctx, group.PublicID)
		require.NoError(t, err)

		var row database.GroupCommentSyncTarget
		require.NoError(t, db.WithContext(ctx).Where("object_type = ? AND object_number = ?", "issue", 11).First(&row).Error)
		marker := markerForTarget(group.PublicID, group.GitHubRepositoryID, row.ObjectType, row.ObjectNumber)
		comment := store.create(11, marker+"\noutdated")
		require.Equal(t, int64(1000), comment.ID)

		require.NoError(t, syncService.Reconcile(ctx, row.ID, row.DesiredRevision, false))
		require.NoError(t, db.WithContext(ctx).First(&row, row.ID).Error)
		require.Equal(t, "permission_denied", row.LastErrorKind)
	})

	t.Run("update 404 recreates a replacement comment", func(t *testing.T) {
		db := openCommentSyncTestDB(t)
		group := seedCommentSyncGroup(t, db)
		dispatcher := &commentSyncDispatcherStub{}
		store, client := newTestGitHubCommentClientWithHandler(t, func(w http.ResponseWriter, r *http.Request, _ *githubCommentStore) bool {
			if r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/issues/comments/1000") {
				http.NotFound(w, r)
				return true
			}
			return false
		})
		syncService := NewCommentSyncService(db, testMirrorClient{}, client, dispatcher)

		_, err := syncService.TriggerGroupSync(ctx, group.PublicID)
		require.NoError(t, err)

		var row database.GroupCommentSyncTarget
		require.NoError(t, db.WithContext(ctx).Where("object_type = ? AND object_number = ?", "issue", 11).First(&row).Error)
		marker := markerForTarget(group.PublicID, group.GitHubRepositoryID, row.ObjectType, row.ObjectNumber)
		store.create(11, marker+"\noutdated")

		require.NoError(t, syncService.Reconcile(ctx, row.ID, row.DesiredRevision, false))
		require.Len(t, store.list(11), 2)
		require.NoError(t, db.WithContext(ctx).First(&row, row.ID).Error)
		require.NotNil(t, row.GitHubCommentID)
		require.Equal(t, int64(1001), *row.GitHubCommentID)
	})
}

func TestCommentSyncProjectAndRepairBranches(t *testing.T) {
	ctx := context.Background()
	db := openCommentSyncTestDB(t)
	group := seedCommentSyncGroup(t, db)
	dispatcher := &commentSyncDispatcherStub{reconcileErr: errors.New("queue reconcile failed")}
	_, client := newTestGitHubCommentClient(t)
	syncService := NewCommentSyncService(db, testMirrorClient{}, client, dispatcher)

	_, err := syncService.TriggerGroupSync(ctx, "missing-group")
	require.ErrorIs(t, err, ErrNotFound)

	require.NoError(t, db.Create(&database.Event{
		GitHubRepositoryID: group.GitHubRepositoryID,
		AggregateType:      "repository",
		AggregateKey:       "repo:101",
		SequenceNo:         1,
		EventType:          "repository.updated",
		ActorType:          "user",
		ActorID:            "tester",
		PayloadJSON:        []byte(`{"group_id":0}`),
		OccurredAt:         time.Now().UTC(),
	}).Error)
	var nonGroup database.Event
	require.NoError(t, db.Last(&nonGroup).Error)
	require.NoError(t, syncService.ProjectEvent(ctx, nonGroup.ID))

	require.NoError(t, db.Create(&database.Event{
		GitHubRepositoryID: group.GitHubRepositoryID,
		AggregateType:      "group",
		AggregateKey:       groupTargetKey(group.PublicID),
		SequenceNo:         2,
		EventType:          "group.updated",
		ActorType:          "user",
		ActorID:            "tester",
		PayloadJSON:        []byte(`{"group_id":0}`),
		OccurredAt:         time.Now().UTC(),
	}).Error)
	var zeroGroup database.Event
	require.NoError(t, db.Last(&zeroGroup).Error)
	require.NoError(t, syncService.ProjectEvent(ctx, zeroGroup.ID))

	require.NoError(t, db.Create(&database.Event{
		GitHubRepositoryID: group.GitHubRepositoryID,
		AggregateType:      "group",
		AggregateKey:       groupTargetKey(group.PublicID),
		SequenceNo:         3,
		EventType:          "group.updated",
		ActorType:          "user",
		ActorID:            "tester",
		PayloadJSON:        []byte(`{bad`),
		OccurredAt:         time.Now().UTC(),
	}).Error)
	var badJSON database.Event
	require.NoError(t, db.Last(&badJSON).Error)
	require.Error(t, syncService.ProjectEvent(ctx, badJSON.ID))

	require.NoError(t, db.Create(&database.GroupCommentSyncTarget{
		GroupID:            group.ID,
		GitHubRepositoryID: group.GitHubRepositoryID,
		ObjectType:         "issue",
		ObjectNumber:       11,
		TargetKey:          objectTargetKey(group.GitHubRepositoryID, "issue", 11),
		DesiredRevision:    2,
		LastErrorAt:        timePtr(time.Now().UTC()),
	}).Error)
	require.ErrorContains(t, syncService.Repair(ctx, group.ID), "queue reconcile failed")

	disabled := NewCommentSyncService(db, testMirrorClient{}, nil, nil)
	require.NoError(t, disabled.Reconcile(ctx, 999, 1, false))
}

func TestCommentSyncReconcileFailureBranches(t *testing.T) {
	ctx := context.Background()
	db := openCommentSyncTestDB(t)
	group := seedCommentSyncGroup(t, db)
	dispatcher := &commentSyncDispatcherStub{}
	_, client := newTestGitHubCommentClientWithHandler(t, func(w http.ResponseWriter, r *http.Request, _ *githubCommentStore) bool {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/acme/widgets/issues/comments/"):
			w.WriteHeader(http.StatusInternalServerError)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"message": "lookup failed"}))
			return true
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/issues/11/comments":
			w.WriteHeader(http.StatusInternalServerError)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"message": "list failed"}))
			return true
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widgets/issues/11/comments":
			w.WriteHeader(http.StatusForbidden)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"message": "Resource not accessible by integration"}))
			return true
		}
		return false
	})
	syncService := NewCommentSyncService(db, testMirrorClient{}, client, dispatcher)

	_, err := syncService.TriggerGroupSync(ctx, group.PublicID)
	require.NoError(t, err)

	var row database.GroupCommentSyncTarget
	require.NoError(t, db.Where("object_type = ? AND object_number = ?", "issue", 11).First(&row).Error)

	require.NoError(t, db.Model(&database.GroupCommentSyncTarget{}).Where("id = ?", row.ID).Update("github_comment_id", 99999).Error)
	err = syncService.Reconcile(ctx, row.ID, row.DesiredRevision, true)
	require.Error(t, err)
	require.NoError(t, db.First(&row, row.ID).Error)
	require.Contains(t, row.LastError, "lookup failed")

	require.NoError(t, db.Model(&database.GroupCommentSyncTarget{}).Where("id = ?", row.ID).Updates(map[string]any{
		"github_comment_id": nil,
		"last_error":        "",
		"last_error_kind":   "",
		"last_error_at":     nil,
	}).Error)
	err = syncService.Reconcile(ctx, row.ID, row.DesiredRevision, false)
	require.Error(t, err)
	require.NoError(t, db.First(&row, row.ID).Error)
	require.Contains(t, row.LastError, "list failed")
}

func TestCommentSyncRenderAndDeleteHelperBranches(t *testing.T) {
	ctx := context.Background()
	db := openNamedCommentSyncTestDB(t, "render-a")
	group := seedCommentSyncGroup(t, db)
	dispatcher := &commentSyncDispatcherStub{}
	_, client := newTestGitHubCommentClientWithHandler(t, func(w http.ResponseWriter, r *http.Request, _ *githubCommentStore) bool {
		if r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/issues/comments/1000") {
			w.WriteHeader(http.StatusInternalServerError)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"message": "delete failed"}))
			return true
		}
		if r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/issues/11/comments" {
			w.WriteHeader(http.StatusForbidden)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"message": "Resource not accessible by integration"}))
			return true
		}
		return false
	})
	syncService := NewCommentSyncService(db, testMirrorClient{}, client, dispatcher)

	noDispatch := NewCommentSyncService(db, testMirrorClient{}, client, nil)
	affected, err := noDispatch.projectGroupTx(db, group.ID)
	require.NoError(t, err)
	require.Zero(t, affected)

	_, err = syncService.TriggerGroupSync(ctx, group.PublicID)
	require.NoError(t, err)
	require.NoError(t, db.Where("object_number = ?", 22).Delete(&database.GroupMember{}).Error)
	_, err = syncService.TriggerGroupSync(ctx, group.PublicID)
	require.NoError(t, err)

	body, _, shouldExist, err := syncService.renderCommentBody(ctx, group, "issue", 11)
	require.NoError(t, err)
	require.False(t, shouldExist)
	require.Empty(t, body)

	db2 := openNamedCommentSyncTestDB(t, "render-b")
	group2 := seedCommentSyncGroup(t, db2)
	store, client2 := newTestGitHubCommentClient(t)
	syncService2 := NewCommentSyncService(db2, testMirrorClient{}, client2, &commentSyncDispatcherStub{})
	require.NoError(t, db2.Create(&database.GroupCommentSyncTarget{
		GroupID:            group2.ID,
		GitHubRepositoryID: group2.GitHubRepositoryID,
		ObjectType:         "issue",
		ObjectNumber:       11,
		TargetKey:          objectTargetKey(group2.GitHubRepositoryID, "issue", 11),
		DesiredRevision:    2,
		DesiredDeleted:     true,
		GitHubCommentID:    int64Ptr(store.create(11, "managed").ID),
	}).Error)
	var deleteRow database.GroupCommentSyncTarget
	require.NoError(t, db2.First(&deleteRow).Error)
	err = syncService2.reconcileDelete(ctx, group2, &deleteRow)
	require.NoError(t, err)

	db3 := openNamedCommentSyncTestDB(t, "render-c")
	group3 := seedCommentSyncGroup(t, db3)
	store, badDeleteClient := newTestGitHubCommentClientWithHandler(t, func(w http.ResponseWriter, r *http.Request, _ *githubCommentStore) bool {
		if r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/issues/comments/1000") {
			w.WriteHeader(http.StatusInternalServerError)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"message": "delete failed"}))
			return true
		}
		return false
	})
	badDeleteSync := NewCommentSyncService(db3, testMirrorClient{}, badDeleteClient, &commentSyncDispatcherStub{})
	require.NoError(t, db3.Create(&database.GroupCommentSyncTarget{
		GroupID:            group3.ID,
		GitHubRepositoryID: group3.GitHubRepositoryID,
		ObjectType:         "issue",
		ObjectNumber:       11,
		TargetKey:          objectTargetKey(group3.GitHubRepositoryID, "issue", 11),
		DesiredRevision:    2,
		DesiredDeleted:     true,
		GitHubCommentID:    int64Ptr(1000),
	}).Error)
	store.create(11, markerForTarget(group3.PublicID, group3.GitHubRepositoryID, "issue", 11)+"\nmanaged")
	require.NoError(t, db3.First(&deleteRow).Error)
	err = badDeleteSync.reconcileDelete(ctx, group3, &deleteRow)
	require.Error(t, err)

	require.Equal(t, "https://github.com/acme/widgets/pull/22", issueURL("acme", "widgets", "pull_request", 22, ""))
	require.Equal(t, "", markdownCell("   "))
	require.True(t, isPermissionDenied(&githubapi.Error{Message: "Resource not accessible by integration"}, true))
}

func TestCommentSyncMoreReconcileBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("missing row and missing group return errors", func(t *testing.T) {
		db := openCommentSyncTestDB(t)
		group := seedCommentSyncGroup(t, db)
		_, client := newTestGitHubCommentClient(t)
		syncService := NewCommentSyncService(db, testMirrorClient{}, client, &commentSyncDispatcherStub{})

		require.Error(t, syncService.Reconcile(ctx, 99999, 1, false))

		require.NoError(t, db.Create(&database.GroupCommentSyncTarget{
			GroupID:            group.ID + 999,
			GitHubRepositoryID: group.GitHubRepositoryID,
			ObjectType:         "issue",
			ObjectNumber:       11,
			TargetKey:          objectTargetKey(group.GitHubRepositoryID, "issue", 11),
			DesiredRevision:    1,
		}).Error)
		var row database.GroupCommentSyncTarget
		require.NoError(t, db.Last(&row).Error)
		require.ErrorIs(t, syncService.Reconcile(ctx, row.ID, row.DesiredRevision, false), ErrNotFound)
	})

	t.Run("render, short-circuit, delete-extra, update, and create branches", func(t *testing.T) {
		db := openNamedCommentSyncTestDB(t, "render-a")
		group := seedCommentSyncGroup(t, db)
		dispatcher := &commentSyncDispatcherStub{}
		var store *githubCommentStore
		_, client := newTestGitHubCommentClient(t)
		syncService := NewCommentSyncService(db, testMirrorClient{}, client, dispatcher)

		_, err := syncService.TriggerGroupSync(ctx, group.PublicID)
		require.NoError(t, err)

		var row database.GroupCommentSyncTarget
		require.NoError(t, db.Where("object_type = ? AND object_number = ?", "issue", 11).First(&row).Error)

		syncService = NewCommentSyncService(db, testMirrorClient{behavior: batchBehavior{fail: true}}, client, dispatcher)
		err = syncService.Reconcile(ctx, row.ID, row.DesiredRevision, false)
		require.Error(t, err)
		require.NoError(t, db.First(&row, row.ID).Error)
		require.NotEmpty(t, row.LastError)

		db = openNamedCommentSyncTestDB(t, "render-b")
		group = seedCommentSyncGroup(t, db)
		store, client = newTestGitHubCommentClient(t)
		syncService = NewCommentSyncService(db, testMirrorClient{}, client, dispatcher)
		_, err = syncService.TriggerGroupSync(ctx, group.PublicID)
		require.NoError(t, err)
		require.NoError(t, db.Where("object_type = ? AND object_number = ?", "issue", 11).First(&row).Error)

		body, hash, _, err := syncService.renderCommentBody(ctx, group, row.ObjectType, row.ObjectNumber)
		require.NoError(t, err)
		comment := store.create(11, body)
		require.NoError(t, db.Model(&database.GroupCommentSyncTarget{}).Where("id = ?", row.ID).Updates(map[string]any{
			"github_comment_id": comment.ID,
			"comment_body_hash": hash,
		}).Error)
		require.NoError(t, syncService.Reconcile(ctx, row.ID, row.DesiredRevision, false))

		require.NoError(t, db.Where("group_id = ? AND object_number = ?", group.ID, 22).Delete(&database.GroupMember{}).Error)
		require.NoError(t, syncService.Reconcile(ctx, row.ID, row.DesiredRevision+1, false))

		db = openNamedCommentSyncTestDB(t, "render-c")
		group = seedCommentSyncGroup(t, db)
		store, client = newTestGitHubCommentClientWithHandler(t, func(w http.ResponseWriter, r *http.Request, _ *githubCommentStore) bool {
			if r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/issues/comments/1001") {
				w.WriteHeader(http.StatusInternalServerError)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"message": "delete failed"}))
				return true
			}
			return false
		})
		syncService = NewCommentSyncService(db, testMirrorClient{}, client, dispatcher)
		_, err = syncService.TriggerGroupSync(ctx, group.PublicID)
		require.NoError(t, err)
		require.NoError(t, db.Where("object_type = ? AND object_number = ?", "issue", 11).First(&row).Error)
		marker := markerForTarget(group.PublicID, group.GitHubRepositoryID, row.ObjectType, row.ObjectNumber)
		store.create(11, marker+"\nduplicate-a")
		store.create(11, marker+"\nduplicate-b")
		err = syncService.Reconcile(ctx, row.ID, row.DesiredRevision, false)
		require.Error(t, err)

		db = openNamedCommentSyncTestDB(t, "render-d")
		group = seedCommentSyncGroup(t, db)
		store, client = newTestGitHubCommentClientWithHandler(t, func(w http.ResponseWriter, r *http.Request, store *githubCommentStore) bool {
			if r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/issues/comments/1000") {
				w.WriteHeader(http.StatusInternalServerError)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"message": "update failed"}))
				return true
			}
			return false
		})
		syncService = NewCommentSyncService(db, testMirrorClient{}, client, dispatcher)
		_, err = syncService.TriggerGroupSync(ctx, group.PublicID)
		require.NoError(t, err)
		require.NoError(t, db.Where("object_type = ? AND object_number = ?", "issue", 11).First(&row).Error)
		store.create(11, markerForTarget(group.PublicID, group.GitHubRepositoryID, row.ObjectType, row.ObjectNumber)+"\noutdated")
		err = syncService.Reconcile(ctx, row.ID, row.DesiredRevision, false)
		require.Error(t, err)

		db = openNamedCommentSyncTestDB(t, "render-e")
		group = seedCommentSyncGroup(t, db)
		_, client = newTestGitHubCommentClientWithHandler(t, func(w http.ResponseWriter, r *http.Request, _ *githubCommentStore) bool {
			if r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widgets/issues/11/comments" {
				w.WriteHeader(http.StatusForbidden)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"message": "Resource not accessible by integration"}))
				return true
			}
			return false
		})
		syncService = NewCommentSyncService(db, testMirrorClient{}, client, dispatcher)
		_, err = syncService.TriggerGroupSync(ctx, group.PublicID)
		require.NoError(t, err)
		require.NoError(t, db.Where("object_type = ? AND object_number = ?", "issue", 11).First(&row).Error)
		require.NoError(t, syncService.Reconcile(ctx, row.ID, row.DesiredRevision, false))
		require.NoError(t, db.First(&row, row.ID).Error)
		require.Equal(t, "permission_denied", row.LastErrorKind)
	})
}

func openCommentSyncTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, database.ApplyTestSchema(db))
	return db
}

func openNamedCommentSyncTestDB(t *testing.T, suffix string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name()+"-"+suffix, "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, database.ApplyTestSchema(db))
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
	return group
}

func newTestGitHubCommentClient(t *testing.T) (*githubCommentStore, *githubapi.Client) {
	return newTestGitHubCommentClientWithHandler(t, nil)
}

func newTestGitHubCommentClientWithHandler(t *testing.T, handler func(http.ResponseWriter, *http.Request, *githubCommentStore) bool) (*githubCommentStore, *githubapi.Client) {
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
		if writeInstallationAccessToken(t, w, r) {
			return
		}
		if handler != nil && handler(w, r, store) {
			return
		}
		if writeIssueCommentList(t, w, r, store) {
			return
		}
		if writeIssueCommentCreate(t, w, r, store) {
			return
		}
		if writeIssueCommentByID(t, w, r, store) {
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	client := githubapi.NewClient(server.URL, githubapi.AuthConfig{
		AppID:          "42",
		InstallationID: "123",
		PrivateKeyPEM:  string(keyPEM),
	})
	return store, client
}

func writeInstallationAccessToken(t *testing.T, w http.ResponseWriter, r *http.Request) bool {
	t.Helper()
	if r.Method != http.MethodPost || r.URL.Path != "/app/installations/123/access_tokens" {
		return false
	}
	require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
		"token":      "installation-token",
		"expires_at": time.Now().UTC().Add(30 * time.Minute),
	}))
	return true
}

func writeIssueCommentList(t *testing.T, w http.ResponseWriter, r *http.Request, store *githubCommentStore) bool {
	t.Helper()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/issues/11/comments":
		require.NoError(t, json.NewEncoder(w).Encode(store.list(11)))
		return true
	case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/issues/22/comments":
		require.NoError(t, json.NewEncoder(w).Encode(store.list(22)))
		return true
	default:
		return false
	}
}

func writeIssueCommentCreate(t *testing.T, w http.ResponseWriter, r *http.Request, store *githubCommentStore) bool {
	t.Helper()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widgets/issues/11/comments":
		return writeIssueCommentCreateBody(t, w, r, store, 11)
	case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widgets/issues/22/comments":
		return writeIssueCommentCreateBody(t, w, r, store, 22)
	default:
		return false
	}
}

func writeIssueCommentCreateBody(t *testing.T, w http.ResponseWriter, r *http.Request, store *githubCommentStore, issueNumber int) bool {
	t.Helper()
	var payload map[string]string
	require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
	require.NoError(t, json.NewEncoder(w).Encode(store.create(issueNumber, payload["body"])))
	return true
}

func writeIssueCommentByID(t *testing.T, w http.ResponseWriter, r *http.Request, store *githubCommentStore) bool {
	t.Helper()
	if !strings.HasPrefix(r.URL.Path, "/repos/acme/widgets/issues/comments/") {
		return false
	}
	commentID, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/repos/acme/widgets/issues/comments/"), 10, 64)
	require.NoError(t, err)
	switch r.Method {
	case http.MethodPatch:
		var payload map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		comment, ok := store.update(commentID, payload["body"])
		if !ok {
			http.NotFound(w, r)
			return true
		}
		require.NoError(t, json.NewEncoder(w).Encode(comment))
	case http.MethodGet:
		comment, ok := store.get(commentID)
		if !ok {
			http.NotFound(w, r)
			return true
		}
		require.NoError(t, json.NewEncoder(w).Encode(comment))
	case http.MethodDelete:
		if !store.delete(commentID) {
			http.NotFound(w, r)
			return true
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		return false
	}
	return true
}
