package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"time"

	"github.com/dutifuldev/prtags/internal/database"
	"github.com/dutifuldev/prtags/internal/githubapi"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverdatabasesql"
	"github.com/riverqueue/river/rivertype"
	"gorm.io/gorm"
)

const (
	queueTargetProjectionRefresh = "target_projection_refresh"
	queueSearchDocumentRebuild   = "search_document_rebuild"
	queueEmbeddingRebuild        = "embedding_rebuild"
	queueGroupCommentProject     = "group_comment_project"
	queueGroupCommentReconcile   = "group_comment_reconcile"
	queueGroupCommentRepair      = "group_comment_repair"
	defaultRetryCap              = 30 * time.Minute
)

type JobDispatcher interface {
	EnqueueTargetProjectionRefreshTx(tx *gorm.DB, group database.Group, target targetRef) error
	EnqueueRebuildsTx(tx *gorm.DB, repository database.RepositoryProjection, target targetRef, sourceUpdatedAt time.Time) error
	EnqueueGroupCommentProjectTx(tx *gorm.DB, eventID uint) error
	EnqueueGroupCommentReconcileTx(tx *gorm.DB, syncTargetID uint, desiredRevision int, scheduledAt time.Time, verify bool) error
	ImportLegacyIndexJobs(ctx context.Context, db *gorm.DB) error
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

type RiverDispatcher struct {
	client      *river.Client[*sql.Tx]
	indexer     *Indexer
	commentSync *CommentSyncService
}

type TargetProjectionRefreshArgs struct {
	RepositoryID int64  `json:"repository_id" river:"unique"`
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	TargetType   string `json:"target_type" river:"unique"`
	TargetKey    string `json:"target_key" river:"unique"`
}

func (TargetProjectionRefreshArgs) Kind() string { return indexJobKindTargetProjectionRefresh }

type SearchDocumentRebuildArgs struct {
	RepositoryID int64  `json:"repository_id" river:"unique"`
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	TargetType   string `json:"target_type" river:"unique"`
	TargetKey    string `json:"target_key" river:"unique"`
}

func (SearchDocumentRebuildArgs) Kind() string { return "search_document_rebuild" }

type EmbeddingRebuildArgs struct {
	RepositoryID int64  `json:"repository_id" river:"unique"`
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	TargetType   string `json:"target_type" river:"unique"`
	TargetKey    string `json:"target_key" river:"unique"`
}

func (EmbeddingRebuildArgs) Kind() string { return "embedding_rebuild" }

type GroupCommentProjectArgs struct {
	EventID uint `json:"event_id" river:"unique"`
}

func (GroupCommentProjectArgs) Kind() string { return "group_comment_sync_project" }

type GroupCommentReconcileArgs struct {
	SyncTargetID    uint `json:"sync_target_id" river:"unique"`
	DesiredRevision int  `json:"desired_revision"`
	Verify          bool `json:"verify"`
}

func (GroupCommentReconcileArgs) Kind() string { return "group_comment_sync_reconcile" }

type GroupCommentRepairArgs struct {
	GroupID uint `json:"group_id,omitempty"`
}

func (GroupCommentRepairArgs) Kind() string { return "group_comment_sync_repair" }

type targetProjectionRefreshWorker struct {
	river.WorkerDefaults[TargetProjectionRefreshArgs]
	indexer *Indexer
}

func (w *targetProjectionRefreshWorker) Work(ctx context.Context, job *river.Job[TargetProjectionRefreshArgs]) error {
	return w.indexer.refreshTargetProjection(ctx, database.IndexJob{
		GitHubRepositoryID: job.Args.RepositoryID,
		RepositoryOwner:    job.Args.Owner,
		RepositoryName:     job.Args.Name,
		TargetType:         job.Args.TargetType,
		TargetKey:          job.Args.TargetKey,
	})
}

type searchDocumentRebuildWorker struct {
	river.WorkerDefaults[SearchDocumentRebuildArgs]
	indexer *Indexer
}

func (w *searchDocumentRebuildWorker) Work(ctx context.Context, job *river.Job[SearchDocumentRebuildArgs]) error {
	return w.indexer.rebuildSearchDocument(ctx, database.IndexJob{
		GitHubRepositoryID: job.Args.RepositoryID,
		RepositoryOwner:    job.Args.Owner,
		RepositoryName:     job.Args.Name,
		TargetType:         job.Args.TargetType,
		TargetKey:          job.Args.TargetKey,
	})
}

type embeddingRebuildWorker struct {
	river.WorkerDefaults[EmbeddingRebuildArgs]
	indexer *Indexer
}

func (w *embeddingRebuildWorker) Work(ctx context.Context, job *river.Job[EmbeddingRebuildArgs]) error {
	return w.indexer.rebuildEmbedding(ctx, database.IndexJob{
		GitHubRepositoryID: job.Args.RepositoryID,
		RepositoryOwner:    job.Args.Owner,
		RepositoryName:     job.Args.Name,
		TargetType:         job.Args.TargetType,
		TargetKey:          job.Args.TargetKey,
	})
}

type groupCommentProjectWorker struct {
	river.WorkerDefaults[GroupCommentProjectArgs]
	commentSync *CommentSyncService
}

func (w *groupCommentProjectWorker) Work(ctx context.Context, job *river.Job[GroupCommentProjectArgs]) error {
	return w.commentSync.ProjectEvent(ctx, job.Args.EventID)
}

type groupCommentReconcileWorker struct {
	river.WorkerDefaults[GroupCommentReconcileArgs]
	commentSync *CommentSyncService
}

func (w *groupCommentReconcileWorker) Work(ctx context.Context, job *river.Job[GroupCommentReconcileArgs]) error {
	err := w.commentSync.Reconcile(ctx, job.Args.SyncTargetID, job.Args.DesiredRevision, job.Args.Verify)
	if err == nil {
		return nil
	}
	var apiErr *githubapi.Error
	if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
		return river.JobSnooze(apiErr.RetryAfter)
	}
	return err
}

type groupCommentRepairWorker struct {
	river.WorkerDefaults[GroupCommentRepairArgs]
	commentSync *CommentSyncService
}

func (w *groupCommentRepairWorker) Work(ctx context.Context, job *river.Job[GroupCommentRepairArgs]) error {
	return w.commentSync.Repair(ctx, job.Args.GroupID)
}

func NewRiverDispatcher(sqlDB *sql.DB, indexer *Indexer, commentSync *CommentSyncService) (*RiverDispatcher, error) {
	workers := river.NewWorkers()
	river.AddWorker(workers, &targetProjectionRefreshWorker{indexer: indexer})
	river.AddWorker(workers, &searchDocumentRebuildWorker{indexer: indexer})
	river.AddWorker(workers, &embeddingRebuildWorker{indexer: indexer})

	periodicJobs := []*river.PeriodicJob{}
	queues := map[string]river.QueueConfig{
		queueTargetProjectionRefresh: {MaxWorkers: 2},
		queueSearchDocumentRebuild:   {MaxWorkers: 2},
		queueEmbeddingRebuild:        {MaxWorkers: 1},
	}

	if commentSync != nil && commentSync.Enabled() {
		river.AddWorker(workers, &groupCommentProjectWorker{commentSync: commentSync})
		river.AddWorker(workers, &groupCommentReconcileWorker{commentSync: commentSync})
		river.AddWorker(workers, &groupCommentRepairWorker{commentSync: commentSync})
		queues[queueGroupCommentProject] = river.QueueConfig{MaxWorkers: 1}
		queues[queueGroupCommentReconcile] = river.QueueConfig{MaxWorkers: 1}
		queues[queueGroupCommentRepair] = river.QueueConfig{MaxWorkers: 1}
		periodicJobs = append(periodicJobs, river.NewPeriodicJob(
			river.PeriodicInterval(commentSyncRepairInterval),
			func() (river.JobArgs, *river.InsertOpts) {
				return GroupCommentRepairArgs{}, &river.InsertOpts{
					Queue: queueGroupCommentRepair,
				}
			},
			&river.PeriodicJobOpts{ID: "group_comment_sync_repair"},
		))
	}

	client, err := river.NewClient(riverdatabasesql.New(sqlDB), &river.Config{
		FetchPollInterval: 100 * time.Millisecond,
		JobTimeout:        2 * time.Minute,
		MaxAttempts:       15,
		PeriodicJobs:      periodicJobs,
		Queues:            queues,
		RetryPolicy:       &cappedRetryPolicy{capDelay: defaultRetryCap},
		Workers:           workers,
	})
	if err != nil {
		return nil, err
	}

	return &RiverDispatcher{
		client:      client,
		indexer:     indexer,
		commentSync: commentSync,
	}, nil
}

func (d *RiverDispatcher) Start(ctx context.Context) error {
	return d.client.Start(ctx)
}

func (d *RiverDispatcher) Stop(ctx context.Context) error {
	return d.client.Stop(ctx)
}

func (d *RiverDispatcher) EnqueueTargetProjectionRefreshTx(tx *gorm.DB, group database.Group, target targetRef) error {
	if target.TargetType != "pull_request" && target.TargetType != "issue" {
		return nil
	}
	if target.TargetKey == "" {
		return nil
	}
	sqlTx, err := sqlTxFromGorm(tx)
	if err != nil {
		return err
	}
	_, err = d.client.InsertTx(tx.Statement.Context, sqlTx, TargetProjectionRefreshArgs{
		RepositoryID: group.GitHubRepositoryID,
		Owner:        group.RepositoryOwner,
		Name:         group.RepositoryName,
		TargetType:   target.TargetType,
		TargetKey:    target.TargetKey,
	}, &river.InsertOpts{
		Queue:      queueTargetProjectionRefresh,
		UniqueOpts: uniqueActiveJobOpts(),
	})
	return err
}

func (d *RiverDispatcher) EnqueueRebuildsTx(tx *gorm.DB, repository database.RepositoryProjection, target targetRef, sourceUpdatedAt time.Time) error {
	sqlTx, err := sqlTxFromGorm(tx)
	if err != nil {
		return err
	}
	searchArgs := SearchDocumentRebuildArgs{
		RepositoryID: repository.GitHubRepositoryID,
		Owner:        repository.Owner,
		Name:         repository.Name,
		TargetType:   target.TargetType,
		TargetKey:    target.TargetKey,
	}
	if _, err := d.client.InsertTx(tx.Statement.Context, sqlTx, searchArgs, &river.InsertOpts{
		Queue:      queueSearchDocumentRebuild,
		UniqueOpts: uniqueActiveJobOpts(),
	}); err != nil {
		return err
	}
	_, err = d.client.InsertTx(tx.Statement.Context, sqlTx, EmbeddingRebuildArgs{
		RepositoryID: repository.GitHubRepositoryID,
		Owner:        repository.Owner,
		Name:         repository.Name,
		TargetType:   target.TargetType,
		TargetKey:    target.TargetKey,
	}, &river.InsertOpts{
		Queue:      queueEmbeddingRebuild,
		UniqueOpts: uniqueActiveJobOpts(),
	})
	return err
}

func (d *RiverDispatcher) EnqueueGroupCommentProjectTx(tx *gorm.DB, eventID uint) error {
	if d.commentSync == nil || !d.commentSync.Enabled() {
		return nil
	}
	sqlTx, err := sqlTxFromGorm(tx)
	if err != nil {
		return err
	}
	_, err = d.client.InsertTx(tx.Statement.Context, sqlTx, GroupCommentProjectArgs{EventID: eventID}, &river.InsertOpts{
		Queue:      queueGroupCommentProject,
		UniqueOpts: uniqueActiveJobOpts(),
	})
	return err
}

func (d *RiverDispatcher) EnqueueGroupCommentReconcileTx(tx *gorm.DB, syncTargetID uint, desiredRevision int, scheduledAt time.Time, verify bool) error {
	if d.commentSync == nil || !d.commentSync.Enabled() {
		return nil
	}
	sqlTx, err := sqlTxFromGorm(tx)
	if err != nil {
		return err
	}
	_, err = d.client.InsertTx(tx.Statement.Context, sqlTx, GroupCommentReconcileArgs{
		SyncTargetID:    syncTargetID,
		DesiredRevision: desiredRevision,
		Verify:          verify,
	}, &river.InsertOpts{
		Queue:       queueGroupCommentReconcile,
		ScheduledAt: scheduledAt,
		UniqueOpts:  uniqueActiveJobOpts(),
	})
	return err
}

func (d *RiverDispatcher) ImportLegacyIndexJobs(ctx context.Context, db *gorm.DB) error {
	var jobs []database.IndexJob
	if err := db.WithContext(ctx).
		Where("status IN ?", []string{"pending", "processing"}).
		Order("id ASC").
		Find(&jobs).Error; err != nil {
		return err
	}
	if len(jobs) == 0 {
		return nil
	}

	now := time.Now().UTC()
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		sqlTx, err := sqlTxFromGorm(tx)
		if err != nil {
			return err
		}

		for _, job := range jobs {
			var insertErr error
			switch job.Kind {
			case indexJobKindTargetProjectionRefresh:
				_, insertErr = d.client.InsertTx(ctx, sqlTx, TargetProjectionRefreshArgs{
					RepositoryID: job.GitHubRepositoryID,
					Owner:        job.RepositoryOwner,
					Name:         job.RepositoryName,
					TargetType:   job.TargetType,
					TargetKey:    job.TargetKey,
				}, &river.InsertOpts{Queue: queueTargetProjectionRefresh, UniqueOpts: uniqueActiveJobOpts()})
			case "search_document_rebuild":
				_, insertErr = d.client.InsertTx(ctx, sqlTx, SearchDocumentRebuildArgs{
					RepositoryID: job.GitHubRepositoryID,
					Owner:        job.RepositoryOwner,
					Name:         job.RepositoryName,
					TargetType:   job.TargetType,
					TargetKey:    job.TargetKey,
				}, &river.InsertOpts{Queue: queueSearchDocumentRebuild, UniqueOpts: uniqueActiveJobOpts()})
			case "embedding_rebuild":
				_, insertErr = d.client.InsertTx(ctx, sqlTx, EmbeddingRebuildArgs{
					RepositoryID: job.GitHubRepositoryID,
					Owner:        job.RepositoryOwner,
					Name:         job.RepositoryName,
					TargetType:   job.TargetType,
					TargetKey:    job.TargetKey,
				}, &river.InsertOpts{Queue: queueEmbeddingRebuild, UniqueOpts: uniqueActiveJobOpts()})
			default:
				continue
			}
			if insertErr != nil {
				return insertErr
			}
			if err := tx.Model(&database.IndexJob{}).
				Where("id = ?", job.ID).
				Updates(map[string]any{
					"status":       "succeeded",
					"last_error":   "migrated_to_river",
					"lease_owner":  "",
					"heartbeat_at": nil,
					"updated_at":   now,
				}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func sqlTxFromGorm(tx *gorm.DB) (*sql.Tx, error) {
	if tx == nil || tx.Statement == nil || tx.Statement.ConnPool == nil {
		return nil, fmt.Errorf("gorm transaction is missing sql tx")
	}
	sqlTx, ok := tx.Statement.ConnPool.(*sql.Tx)
	if !ok {
		return nil, fmt.Errorf("gorm transaction does not expose *sql.Tx (got %T)", tx.Statement.ConnPool)
	}
	return sqlTx, nil
}

func uniqueActiveJobOpts() river.UniqueOpts {
	return river.UniqueOpts{
		ByArgs: true,
		ByState: []rivertype.JobState{
			rivertype.JobStateAvailable,
			rivertype.JobStatePending,
			rivertype.JobStateRetryable,
			rivertype.JobStateRunning,
			rivertype.JobStateScheduled,
		},
	}
}

type cappedRetryPolicy struct {
	capDelay time.Duration
}

func (p *cappedRetryPolicy) NextRetry(job *rivertype.JobRow) time.Time {
	now := time.Now().UTC()
	attempt := len(job.Errors) + 1

	maxSeconds := p.capDelay.Seconds()
	if maxSeconds <= 0 {
		maxSeconds = defaultRetryCap.Seconds()
	}

	retrySeconds := math.Pow(float64(attempt), 4)
	retrySeconds = min(retrySeconds, maxSeconds)
	retrySeconds += retrySeconds * (rand.Float64()*0.2 - 0.1)
	retrySeconds = min(retrySeconds, maxSeconds)
	if retrySeconds < 0 {
		retrySeconds = 0
	}

	return now.Add(time.Duration(retrySeconds * float64(time.Second)))
}
