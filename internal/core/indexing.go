package core

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/dutifuldev/prtags/internal/database"
	"github.com/dutifuldev/prtags/internal/embedding"
	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"
)

type Indexer struct {
	db        *gorm.DB
	embedding embedding.Provider
	owner     string
	leaseTTL  time.Duration
}

type TextSearchResult struct {
	TargetType  string                     `json:"target_type"`
	ID          string                     `json:"id,omitempty"`
	TargetKey   string                     `json:"target_key"`
	Score       float64                    `json:"score"`
	Projection  *database.TargetProjection `json:"projection,omitempty"`
	Annotations map[string]any             `json:"annotations,omitempty"`
}

func NewIndexer(db *gorm.DB, provider embedding.Provider) *Indexer {
	return &Indexer{
		db:        db,
		embedding: provider,
		owner:     fmt.Sprintf("worker-%d", time.Now().UnixNano()),
		leaseTTL:  5 * time.Minute,
	}
}

func (s *Service) SearchText(ctx context.Context, owner, repo, query string, targetTypes []string, limit int) ([]TextSearchResult, error) {
	repository, err := s.EnsureRepository(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if len(targetTypes) == 0 {
		targetTypes = []string{"pull_request", "issue", "group"}
	}

	rows, err := s.db.WithContext(ctx).
		Raw(`
			SELECT github_repository_id, target_type, target_key,
			       ts_rank_cd(to_tsvector('simple', search_text), websearch_to_tsquery('simple', ?)) AS score
			FROM search_documents
			WHERE github_repository_id = ?
			  AND target_type IN ?
			  AND to_tsvector('simple', search_text) @@ websearch_to_tsquery('simple', ?)
			ORDER BY score DESC, indexed_at DESC
			LIMIT ?
		`, query, repository.GitHubRepositoryID, targetTypes, query, limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := []TextSearchResult{}
	for rows.Next() {
		var (
			repoID     int64
			targetType string
			targetKey  string
			score      float64
		)
		if err := rows.Scan(&repoID, &targetType, &targetKey, &score); err != nil {
			return nil, err
		}
		result, err := s.buildSearchResult(ctx, repository.GitHubRepositoryID, targetType, targetKey, score)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

func (s *Service) SearchSimilar(ctx context.Context, owner, repo, query string, targetTypes []string, limit int) ([]TextSearchResult, error) {
	repository, err := s.EnsureRepository(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if len(targetTypes) == 0 {
		targetTypes = []string{"pull_request", "issue", "group"}
	}

	vectorValues, err := s.indexer.embedding.Embed(ctx, query)
	if err != nil {
		return nil, err
	}
	queryVector := pgvector.NewVector(vectorValues)

	rows, err := s.db.WithContext(ctx).
		Raw(`
			SELECT github_repository_id, target_type, target_key, (1 - (embedding <=> ?)) AS score
			FROM embeddings
			WHERE github_repository_id = ?
			  AND embedding_model = ?
			  AND target_type IN ?
			ORDER BY embedding <=> ?
			LIMIT ?
		`, queryVector, repository.GitHubRepositoryID, s.indexer.embedding.Model(), targetTypes, queryVector, limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := []TextSearchResult{}
	for rows.Next() {
		var (
			repoID     int64
			targetType string
			targetKey  string
			score      float64
		)
		if err := rows.Scan(&repoID, &targetType, &targetKey, &score); err != nil {
			return nil, err
		}
		result, err := s.buildSearchResult(ctx, repository.GitHubRepositoryID, targetType, targetKey, score)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

func (s *Service) buildSearchResult(ctx context.Context, repositoryID int64, targetType, targetKey string, score float64) (TextSearchResult, error) {
	result := TextSearchResult{
		TargetType: targetType,
		TargetKey:  targetKey,
		Score:      score,
	}

	if targetType != "group" {
		number, ok := objectNumberFromTargetKey(targetKey)
		if ok {
			var projection database.TargetProjection
			err := s.db.WithContext(ctx).Where("github_repository_id = ? AND target_type = ? AND object_number = ?", repositoryID, targetType, number).First(&projection).Error
			if err == nil {
				result.Projection = &projection
				annotations, err := s.getAnnotationsForTarget(ctx, targetType, repositoryID, number, nil)
				if err != nil {
					return TextSearchResult{}, err
				}
				result.Annotations = annotations
			}
		}
	} else {
		groupPublicID, ok := groupPublicIDFromTargetKey(targetKey)
		if ok {
			group, err := s.lookupGroupByPublicID(ctx, groupPublicID)
			if err != nil {
				return TextSearchResult{}, translateDBError(err)
			}
			annotations, err := s.getAnnotationsForTarget(ctx, "group", repositoryID, 0, &group.ID)
			if err != nil {
				return TextSearchResult{}, err
			}
			result.ID = group.PublicID
			result.Annotations = annotations
		}
	}

	return result, nil
}

func (i *Indexer) Start(ctx context.Context, pollInterval time.Duration) {
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		if err := i.RunOnce(ctx); err != nil {
			slog.Error("index worker run failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (i *Indexer) RunOnce(ctx context.Context) error {
	if err := i.recoverStaleJobs(ctx); err != nil {
		return err
	}
	job, ok, err := i.claimNextJob(ctx)
	if err != nil || !ok {
		return err
	}
	return i.processJob(ctx, job)
}

func (i *Indexer) recoverStaleJobs(ctx context.Context) error {
	now := time.Now().UTC()
	staleBefore := now.Add(-i.leaseTTL)
	return i.db.WithContext(ctx).Model(&database.IndexJob{}).
		Where("status = ? AND ((heartbeat_at IS NOT NULL AND heartbeat_at <= ?) OR (heartbeat_at IS NULL AND updated_at <= ?))", "processing", staleBefore, staleBefore).
		Updates(map[string]any{
			"status":          "pending",
			"lease_owner":     "",
			"heartbeat_at":    nil,
			"next_attempt_at": now,
			"last_error":      "job lease expired",
			"updated_at":      now,
		}).Error
}

func (i *Indexer) claimNextJob(ctx context.Context) (database.IndexJob, bool, error) {
	var job database.IndexJob
	now := time.Now().UTC()
	err := i.db.WithContext(ctx).
		Where("status = ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?)", "pending", now).
		Order("created_at ASC, id ASC").
		First(&job).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return database.IndexJob{}, false, nil
		}
		return database.IndexJob{}, false, err
	}

	result := i.db.WithContext(ctx).Model(&database.IndexJob{}).
		Where("id = ? AND status = ?", job.ID, "pending").
		Updates(map[string]any{
			"status":        "processing",
			"attempt_count": gorm.Expr("attempt_count + 1"),
			"lease_owner":   i.owner,
			"heartbeat_at":  now,
			"updated_at":    now,
		})
	if result.Error != nil {
		return database.IndexJob{}, false, result.Error
	}
	if result.RowsAffected == 0 {
		return database.IndexJob{}, false, nil
	}
	job.Status = "processing"
	job.AttemptCount++
	return job, true, nil
}

func (i *Indexer) processJob(ctx context.Context, job database.IndexJob) error {
	switch job.Kind {
	case "search_document_rebuild":
		if err := i.rebuildSearchDocument(ctx, job); err != nil {
			return i.markJobFailed(ctx, job.ID, err)
		}
	case "embedding_rebuild":
		if err := i.rebuildEmbedding(ctx, job); err != nil {
			return i.markJobFailed(ctx, job.ID, err)
		}
	default:
		return i.markJobFailed(ctx, job.ID, fmt.Errorf("unknown job kind %q", job.Kind))
	}
	return i.markJobSucceeded(ctx, job.ID)
}

func (i *Indexer) rebuildSearchDocument(ctx context.Context, job database.IndexJob) error {
	searchText, sourceUpdatedAt, err := i.buildSearchText(ctx, job.GitHubRepositoryID, job.TargetType, job.TargetKey)
	if err != nil {
		return err
	}
	if strings.TrimSpace(searchText) == "" {
		return i.db.WithContext(ctx).Where("github_repository_id = ? AND target_type = ? AND target_key = ?", job.GitHubRepositoryID, job.TargetType, job.TargetKey).Delete(&database.SearchDocument{}).Error
	}

	document := database.SearchDocument{
		GitHubRepositoryID: job.GitHubRepositoryID,
		RepositoryOwner:    job.RepositoryOwner,
		RepositoryName:     job.RepositoryName,
		TargetType:         job.TargetType,
		TargetKey:          job.TargetKey,
		SearchText:         searchText,
		SourceUpdatedAt:    sourceUpdatedAt,
		IndexedAt:          time.Now().UTC(),
	}

	return i.db.WithContext(ctx).
		Where("github_repository_id = ? AND target_type = ? AND target_key = ?", job.GitHubRepositoryID, job.TargetType, job.TargetKey).
		Assign(document).
		FirstOrCreate(&document).Error
}

func (i *Indexer) rebuildEmbedding(ctx context.Context, job database.IndexJob) error {
	embeddingText, sourceUpdatedAt, err := i.buildEmbeddingText(ctx, job.GitHubRepositoryID, job.TargetType, job.TargetKey)
	if err != nil {
		return err
	}
	if strings.TrimSpace(embeddingText) == "" {
		return i.db.WithContext(ctx).
			Where("github_repository_id = ? AND target_type = ? AND target_key = ? AND embedding_model = ?", job.GitHubRepositoryID, job.TargetType, job.TargetKey, i.embedding.Model()).
			Delete(&database.Embedding{}).Error
	}

	vectorValues, err := i.embedding.Embed(ctx, embeddingText)
	if err != nil {
		return err
	}
	model := database.Embedding{
		GitHubRepositoryID: job.GitHubRepositoryID,
		RepositoryOwner:    job.RepositoryOwner,
		RepositoryName:     job.RepositoryName,
		TargetType:         job.TargetType,
		TargetKey:          job.TargetKey,
		EmbeddingText:      embeddingText,
		EmbeddingModel:     i.embedding.Model(),
		Embedding:          pgvector.NewVector(vectorValues),
		SourceUpdatedAt:    sourceUpdatedAt,
		IndexedAt:          time.Now().UTC(),
	}
	return i.db.WithContext(ctx).
		Where("github_repository_id = ? AND target_type = ? AND target_key = ? AND embedding_model = ?", job.GitHubRepositoryID, job.TargetType, job.TargetKey, i.embedding.Model()).
		Assign(model).
		FirstOrCreate(&model).Error
}

func (i *Indexer) buildSearchText(ctx context.Context, repositoryID int64, targetType, targetKey string) (string, time.Time, error) {
	parts := []string{}
	sourceUpdatedAt := time.Now().UTC()

	if targetType != "group" {
		number, ok := objectNumberFromTargetKey(targetKey)
		if ok {
			var projection database.TargetProjection
			err := i.db.WithContext(ctx).Where("github_repository_id = ? AND target_type = ? AND object_number = ?", repositoryID, targetType, number).First(&projection).Error
			if err == nil {
				if strings.TrimSpace(projection.Title) != "" {
					parts = append(parts, projection.Title)
				}
				sourceUpdatedAt = projection.SourceUpdatedAt
			}
		}
	} else {
		groupPublicID, ok := groupPublicIDFromTargetKey(targetKey)
		if ok {
			var group database.Group
			err := i.db.WithContext(ctx).Where("public_id = ?", groupPublicID).First(&group).Error
			if err == nil {
				if strings.TrimSpace(group.Title) != "" {
					parts = append(parts, group.Title)
				}
				if strings.TrimSpace(group.Description) != "" {
					parts = append(parts, group.Description)
				}
				sourceUpdatedAt = group.UpdatedAt.UTC()
			}
		}
	}

	values, err := i.loadFieldValues(ctx, repositoryID, targetType, targetKey)
	if err != nil {
		return "", time.Time{}, err
	}
	for _, value := range values {
		if value.FieldDefinition.ArchivedAt != nil || !value.FieldDefinition.IsSearchable {
			continue
		}
		apiValue := fieldValueToAPI(value)
		if apiValue == nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %v", value.FieldDefinition.Name, apiValue))
		if value.UpdatedAt.After(sourceUpdatedAt) {
			sourceUpdatedAt = value.UpdatedAt
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n")), sourceUpdatedAt, nil
}

func (i *Indexer) buildEmbeddingText(ctx context.Context, repositoryID int64, targetType, targetKey string) (string, time.Time, error) {
	values, err := i.loadFieldValues(ctx, repositoryID, targetType, targetKey)
	if err != nil {
		return "", time.Time{}, err
	}
	parts := []string{}
	sourceUpdatedAt := time.Now().UTC()
	for _, value := range values {
		if value.FieldDefinition.ArchivedAt != nil || !value.FieldDefinition.IsVectorized {
			continue
		}
		apiValue := fieldValueToAPI(value)
		if apiValue == nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %v", value.FieldDefinition.Name, apiValue))
		if value.UpdatedAt.After(sourceUpdatedAt) {
			sourceUpdatedAt = value.UpdatedAt
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n")), sourceUpdatedAt, nil
}

func (i *Indexer) loadFieldValues(ctx context.Context, repositoryID int64, targetType, targetKey string) ([]database.FieldValue, error) {
	var values []database.FieldValue
	err := i.db.WithContext(ctx).Preload("FieldDefinition").
		Where("github_repository_id = ? AND target_type = ? AND target_key = ?", repositoryID, targetType, targetKey).
		Find(&values).Error
	return values, err
}

func (i *Indexer) markJobSucceeded(ctx context.Context, jobID uint) error {
	return i.db.WithContext(ctx).Model(&database.IndexJob{}).
		Where("id = ?", jobID).
		Updates(map[string]any{
			"status":       "succeeded",
			"lease_owner":  "",
			"heartbeat_at": nil,
			"updated_at":   time.Now().UTC(),
			"last_error":   "",
		}).Error
}

func (i *Indexer) markJobFailed(ctx context.Context, jobID uint, failure error) error {
	retryAt := time.Now().UTC().Add(5 * time.Second)
	return i.db.WithContext(ctx).Model(&database.IndexJob{}).
		Where("id = ?", jobID).
		Updates(map[string]any{
			"status":          "pending",
			"lease_owner":     "",
			"next_attempt_at": retryAt,
			"heartbeat_at":    nil,
			"updated_at":      time.Now().UTC(),
			"last_error":      failure.Error(),
		}).Error
}

func (s *Service) enqueueRebuildsTx(tx *gorm.DB, repository database.RepositoryProjection, target targetRef, sourceUpdatedAt time.Time) error {
	for _, kind := range []string{"search_document_rebuild", "embedding_rebuild"} {
		var existing database.IndexJob
		err := tx.Where("kind = ? AND github_repository_id = ? AND target_type = ? AND target_key = ? AND status IN ?", kind, repository.GitHubRepositoryID, target.TargetType, target.TargetKey, []string{"pending", "processing"}).First(&existing).Error
		if err == nil {
			continue
		}
		if err != nil && err != gorm.ErrRecordNotFound {
			return err
		}
		job := database.IndexJob{
			Kind:               kind,
			Status:             "pending",
			GitHubRepositoryID: repository.GitHubRepositoryID,
			RepositoryOwner:    repository.Owner,
			RepositoryName:     repository.Name,
			TargetType:         target.TargetType,
			TargetKey:          target.TargetKey,
			NextAttemptAt:      timePtr(time.Now().UTC()),
			SourceUpdatedAt:    timePtr(sourceUpdatedAt),
		}
		if err := tx.Create(&job).Error; err != nil {
			return err
		}
	}
	return nil
}

func objectNumberFromTargetKey(targetKey string) (int, bool) {
	parts := strings.Split(targetKey, ":")
	if len(parts) != 4 {
		return 0, false
	}
	value, err := strconv.Atoi(parts[3])
	if err != nil {
		return 0, false
	}
	return value, true
}

func groupPublicIDFromTargetKey(targetKey string) (string, bool) {
	if !strings.HasPrefix(targetKey, "group:") {
		return "", false
	}
	value := strings.TrimSpace(strings.TrimPrefix(targetKey, "group:"))
	return value, value != ""
}

func timePtr(value time.Time) *time.Time {
	value = value.UTC()
	return &value
}
