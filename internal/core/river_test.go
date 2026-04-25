package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/dutifuldev/prtags/internal/database"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type jobDispatcherStub struct {
	rebuildCalls   []targetRef
	projectEventID []uint
	reconcileCalls []commentSyncDispatchCall
	rebuildErr     error
	projectErr     error
	reconcileErr   error
}

func (d *jobDispatcherStub) EnqueueRebuildsTx(_ *gorm.DB, _ database.RepositoryProjection, target targetRef, _ time.Time) error {
	d.rebuildCalls = append(d.rebuildCalls, target)
	return d.rebuildErr
}

func (d *jobDispatcherStub) EnqueueGroupCommentProjectTx(_ *gorm.DB, eventID uint) error {
	d.projectEventID = append(d.projectEventID, eventID)
	return d.projectErr
}

func (d *jobDispatcherStub) EnqueueGroupCommentReconcileTx(_ *gorm.DB, syncTargetID uint, desiredRevision int, _ time.Time, verify bool) error {
	d.reconcileCalls = append(d.reconcileCalls, commentSyncDispatchCall{
		SyncTargetID:    syncTargetID,
		DesiredRevision: desiredRevision,
		Verify:          verify,
	})
	return d.reconcileErr
}

func (d *jobDispatcherStub) ImportLegacyIndexJobs(context.Context, *gorm.DB) error { return nil }
func (d *jobDispatcherStub) Start(context.Context) error                           { return nil }
func (d *jobDispatcherStub) Stop(context.Context) error                            { return nil }

func TestRiverArgsKindsAndHelpers(t *testing.T) {
	require.Equal(t, "search_document_rebuild", SearchDocumentRebuildArgs{}.Kind())
	require.Equal(t, "embedding_rebuild", EmbeddingRebuildArgs{}.Kind())
	require.Equal(t, "group_comment_sync_project", GroupCommentProjectArgs{}.Kind())
	require.Equal(t, "group_comment_sync_reconcile", GroupCommentReconcileArgs{}.Kind())
	require.Equal(t, "group_comment_sync_repair", GroupCommentRepairArgs{}.Kind())

	states := uniqueActiveJobOpts().ByState
	require.Contains(t, states, rivertype.JobStateAvailable)
	require.Contains(t, states, rivertype.JobStateRunning)

	policy := &cappedRetryPolicy{capDelay: time.Second}
	next := policy.NextRetry(&rivertype.JobRow{Errors: []rivertype.AttemptError{{}, {}}})
	require.WithinDuration(t, time.Now().UTC().Add(time.Second), next, 250*time.Millisecond)
}

func TestRiverWorkersAndSQLHelpers(t *testing.T) {
	ctx := context.Background()
	service, db, server := newTestService(t)
	defer server.Close()

	tx := db.Begin()
	require.NoError(t, tx.Error)
	defer func() { _ = tx.Rollback().Error }()

	_, err := sqlTxFromGorm(nil)
	require.Error(t, err)
	_, err = sqlTxFromGorm(db)
	require.Error(t, err)
	sqlTx, err := sqlTxFromGorm(tx)
	require.NoError(t, err)
	require.IsType(t, &sql.Tx{}, sqlTx)

	group, err := service.CreateGroup(ctx, permissionsActor(), "acme", "widgets", GroupInput{Kind: "mixed", Title: "Auth"}, "")
	require.NoError(t, err)
	_, err = service.AddGroupMember(ctx, permissionsActor(), group.PublicID, "pull_request", 22, "")
	require.NoError(t, err)
	drainIndexJobs(t, ctx, db, service.indexer)

	searchWorker := &searchDocumentRebuildWorker{indexer: service.indexer}
	require.NoError(t, searchWorker.Work(ctx, &river.Job[SearchDocumentRebuildArgs]{Args: SearchDocumentRebuildArgs{
		RepositoryID: group.GitHubRepositoryID,
		Owner:        group.RepositoryOwner,
		Name:         group.RepositoryName,
		TargetType:   "pull_request",
		TargetKey:    objectTargetKey(group.GitHubRepositoryID, "pull_request", 22),
	}}))

	embeddingWorker := &embeddingRebuildWorker{indexer: service.indexer}
	require.NoError(t, embeddingWorker.Work(ctx, &river.Job[EmbeddingRebuildArgs]{Args: EmbeddingRebuildArgs{
		RepositoryID: group.GitHubRepositoryID,
		Owner:        group.RepositoryOwner,
		Name:         group.RepositoryName,
		TargetType:   "pull_request",
		TargetKey:    objectTargetKey(group.GitHubRepositoryID, "pull_request", 22),
	}}))
}

func TestGroupCommentRiverWorkers(t *testing.T) {
	ctx := context.Background()
	db := openCommentSyncTestDB(t)
	group := seedCommentSyncGroup(t, db)
	dispatcher := &commentSyncDispatcherStub{}
	store, client := newTestGitHubCommentClient(t)
	syncService := NewCommentSyncService(db, testMirrorClient{}, client, dispatcher)

	event := eventForGroup(group.ID)
	require.NoError(t, db.Create(&event).Error)
	require.NoError(t, (&groupCommentProjectWorker{commentSync: syncService}).Work(ctx, &river.Job[GroupCommentProjectArgs]{Args: GroupCommentProjectArgs{EventID: event.ID}}))

	var row database.GroupCommentSyncTarget
	require.NoError(t, db.WithContext(ctx).First(&row).Error)
	reconcileWorker := &groupCommentReconcileWorker{commentSync: syncService}
	require.NoError(t, reconcileWorker.Work(ctx, &river.Job[GroupCommentReconcileArgs]{Args: GroupCommentReconcileArgs{
		SyncTargetID:    row.ID,
		DesiredRevision: row.DesiredRevision,
	}}))
	require.NotEmpty(t, store.list(11))

	require.NoError(t, (&groupCommentRepairWorker{commentSync: syncService}).Work(ctx, &river.Job[GroupCommentRepairArgs]{Args: GroupCommentRepairArgs{GroupID: group.ID}}))
}

func TestGroupCommentReconcileWorkerSnoozesRetryAfter(t *testing.T) {
	ctx := context.Background()
	db := openCommentSyncTestDB(t)
	group := seedCommentSyncGroup(t, db)
	dispatcher := &commentSyncDispatcherStub{}
	_, client := newTestGitHubCommentClientWithHandler(t, func(w http.ResponseWriter, r *http.Request, _ *githubCommentStore) bool {
		if r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widgets/issues/11/comments" {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "slow down"})
			return true
		}
		return false
	})
	syncService := NewCommentSyncService(db, testMirrorClient{}, client, dispatcher)
	_, err := syncService.TriggerGroupSync(ctx, group.PublicID)
	require.NoError(t, err)

	var row database.GroupCommentSyncTarget
	require.NoError(t, db.WithContext(ctx).Where("object_number = ?", 11).First(&row).Error)
	err = (&groupCommentReconcileWorker{commentSync: syncService}).Work(ctx, &river.Job[GroupCommentReconcileArgs]{Args: GroupCommentReconcileArgs{
		SyncTargetID:    row.ID,
		DesiredRevision: row.DesiredRevision,
	}})
	require.Error(t, err)
	require.ErrorContains(t, err, "JobSnoozeError")
}

func TestNewRiverDispatcherAndImportLegacyHelpers(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, database.ApplyTestSchema(db))
	sqlDB, err := db.DB()
	require.NoError(t, err)

	dispatcher, err := NewRiverDispatcher(sqlDB, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, dispatcher)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, dispatcher.Start(ctx))
	require.NoError(t, dispatcher.Stop(context.Background()))

	jobs, err := legacyIndexJobs(context.Background(), db)
	require.NoError(t, err)
	require.Empty(t, jobs)

	now := time.Now().UTC()
	require.NoError(t, db.Create(&database.IndexJob{Kind: "search_document_rebuild", Status: "pending", GitHubRepositoryID: 101, RepositoryOwner: "acme", RepositoryName: "widgets", TargetType: "pull_request", TargetKey: objectTargetKey(101, "pull_request", 22)}).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return markLegacyIndexJobMigrated(tx, 1, now)
	}))
	var job database.IndexJob
	require.NoError(t, db.First(&job, 1).Error)
	require.Equal(t, "succeeded", job.Status)
	require.Equal(t, "migrated_to_river", job.LastError)

	require.ErrorIs(t, dispatcher.importLegacyIndexJob(context.Background(), sqlTxForTest(t, db), database.IndexJob{Kind: "unknown"}), errUnsupportedLegacyJobKind)
}

func TestRiverDispatcherQueueMethods(t *testing.T) {
	ctx := context.Background()
	db := openCommentSyncTestDB(t)
	group := seedCommentSyncGroup(t, db)
	store, client := newTestGitHubCommentClient(t)
	_ = store
	commentSync := NewCommentSyncService(db, testMirrorClient{}, client, &commentSyncDispatcherStub{})
	sqlDB, err := db.DB()
	require.NoError(t, err)

	dispatcher, err := NewRiverDispatcher(sqlDB, nil, commentSync)
	require.NoError(t, err)

	repository := database.RepositoryProjection{GitHubRepositoryID: group.GitHubRepositoryID, Owner: group.RepositoryOwner, Name: group.RepositoryName}
	target := targetRef{
		RepositoryID: group.GitHubRepositoryID,
		Owner:        group.RepositoryOwner,
		Name:         group.RepositoryName,
		TargetType:   "pull_request",
		TargetKey:    objectTargetKey(group.GitHubRepositoryID, "pull_request", 22),
		ObjectNumber: 22,
	}
	require.Error(t, dispatcher.EnqueueRebuildsTx(nil, repository, target, time.Now().UTC()))
	require.Error(t, dispatcher.EnqueueGroupCommentProjectTx(nil, 77))
	require.Error(t, dispatcher.EnqueueGroupCommentReconcileTx(nil, 88, 2, time.Now().UTC(), true))

	var legacy database.IndexJob
	require.NoError(t, db.Create(&database.IndexJob{
		Kind:               "target_projection_refresh",
		Status:             "pending",
		GitHubRepositoryID: group.GitHubRepositoryID,
		RepositoryOwner:    group.RepositoryOwner,
		RepositoryName:     group.RepositoryName,
		TargetType:         "pull_request",
		TargetKey:          objectTargetKey(group.GitHubRepositoryID, "pull_request", 22),
	}).Error)
	require.NoError(t, db.First(&legacy).Error)
	require.NoError(t, dispatcher.ImportLegacyIndexJobs(ctx, db))
	require.NoError(t, db.First(&legacy, legacy.ID).Error)
	require.Equal(t, "succeeded", legacy.Status)

	nilDispatcher, err := NewRiverDispatcher(sqlDB, nil, nil)
	require.NoError(t, err)
	require.NoError(t, nilDispatcher.EnqueueGroupCommentProjectTx(nil, 1))
	require.NoError(t, nilDispatcher.EnqueueGroupCommentReconcileTx(nil, 1, 1, time.Now().UTC(), false))

	tx := db.Begin()
	require.NoError(t, tx.Error)
	err = dispatcher.EnqueueRebuildsTx(tx, repository, target, time.Now().UTC())
	if err != nil {
		require.NotContains(t, err.Error(), "gorm transaction is missing sql tx")
	}
	err = dispatcher.EnqueueGroupCommentProjectTx(tx, 91)
	if err != nil {
		require.NotContains(t, err.Error(), "gorm transaction is missing sql tx")
	}
	err = dispatcher.EnqueueGroupCommentReconcileTx(tx, 92, 2, time.Now().UTC(), true)
	if err != nil {
		require.NotContains(t, err.Error(), "gorm transaction is missing sql tx")
	}
	require.NoError(t, tx.Rollback().Error)

	sqlTx := sqlTxForTest(t, db)
	for _, legacyJob := range []database.IndexJob{
		{Kind: "search_document_rebuild", GitHubRepositoryID: group.GitHubRepositoryID, RepositoryOwner: group.RepositoryOwner, RepositoryName: group.RepositoryName, TargetType: "pull_request", TargetKey: target.TargetKey},
		{Kind: "embedding_rebuild", GitHubRepositoryID: group.GitHubRepositoryID, RepositoryOwner: group.RepositoryOwner, RepositoryName: group.RepositoryName, TargetType: "pull_request", TargetKey: target.TargetKey},
	} {
		err = dispatcher.importLegacyIndexJob(ctx, sqlTx, legacyJob)
		if err != nil {
			require.NotErrorIs(t, err, errUnsupportedLegacyJobKind)
		}
	}
	require.ErrorIs(t, dispatcher.importLegacyIndexJob(ctx, sqlTx, database.IndexJob{Kind: "target_projection_refresh"}), errUnsupportedLegacyJobKind)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return dispatcher.importLegacyIndexJobsTx(ctx, tx, sqlTxForTest(t, db), []database.IndexJob{{Kind: "unknown"}}, time.Now().UTC())
	}))
}

func eventForGroup(groupID uint) database.Event {
	return database.Event{
		GitHubRepositoryID: 101,
		AggregateType:      "group",
		AggregateKey:       "group:steady-otter-k4m2",
		SequenceNo:         1,
		EventType:          "group.updated",
		ActorType:          "user",
		ActorID:            "tester",
		OccurredAt:         time.Now().UTC(),
		PayloadJSON:        []byte(`{"group_id":` + strconv.Itoa(int(groupID)) + `}`),
	}
}

func sqlTxForTest(t *testing.T, db *gorm.DB) *sql.Tx {
	t.Helper()
	tx := db.Begin()
	require.NoError(t, tx.Error)
	t.Cleanup(func() { _ = tx.Rollback().Error })
	sqlTx, err := sqlTxFromGorm(tx)
	require.NoError(t, err)
	return sqlTx
}
