package core

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dutifuldev/prtags/internal/database"
	"github.com/dutifuldev/prtags/internal/embedding"
	"github.com/dutifuldev/prtags/internal/ghreplica"
	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"
)

type Indexer struct {
	db        *gorm.DB
	ghreplica mirrorClient
	embedding embedding.Provider
	owner     string
	leaseTTL  time.Duration
}

type TextSearchResult struct {
	TargetType    string                    `json:"target_type"`
	ID            string                    `json:"id,omitempty"`
	TargetKey     string                    `json:"target_key"`
	Score         float64                   `json:"score"`
	ObjectSummary *GroupMemberObjectSummary `json:"object_summary,omitempty"`
	Annotations   map[string]any            `json:"annotations,omitempty"`
}

func NewIndexer(db *gorm.DB, gh mirrorClient, provider embedding.Provider) *Indexer {
	return &Indexer{
		db:        db,
		ghreplica: gh,
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
	limit, targetTypes = normalizeSearchRequest(limit, targetTypes)
	rows, err := s.searchTextRows(ctx, repository.GitHubRepositoryID, query, targetTypes, limit)
	if err != nil {
		return nil, err
	}
	return s.resolveSearchResults(ctx, repository.GitHubRepositoryID, rows)
}

func (s *Service) SearchSimilar(ctx context.Context, owner, repo, query string, targetTypes []string, limit int) ([]TextSearchResult, error) {
	repository, err := s.EnsureRepository(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	limit, targetTypes = normalizeSearchRequest(limit, targetTypes)
	vectorValues, err := s.indexer.embedding.Embed(ctx, query)
	if err != nil {
		return nil, err
	}
	rows, err := s.searchSimilarRows(ctx, repository.GitHubRepositoryID, vectorValues, targetTypes, limit)
	if err != nil {
		return nil, err
	}
	return s.resolveSearchResults(ctx, repository.GitHubRepositoryID, rows)
}

func (s *Service) buildSearchResult(ctx context.Context, repositoryID int64, targetType, targetKey string, score float64) (TextSearchResult, error) {
	result := TextSearchResult{
		TargetType: targetType,
		TargetKey:  targetKey,
		Score:      score,
	}

	if targetType == "group" {
		return s.populateGroupSearchResult(ctx, repositoryID, targetKey, result)
	}
	return s.populateObjectSearchResult(ctx, repositoryID, targetType, targetKey, result)
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
	parts, sourceUpdatedAt, err := i.baseSearchParts(ctx, repositoryID, targetType, targetKey)
	if err != nil {
		return "", time.Time{}, err
	}
	values, err := i.loadFieldValues(ctx, repositoryID, targetType, targetKey)
	if err != nil {
		return "", time.Time{}, err
	}
	parts, sourceUpdatedAt = appendSearchableFieldValues(parts, sourceUpdatedAt, values)
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
	if s.dispatcher != nil {
		return s.dispatcher.EnqueueRebuildsTx(tx, repository, target, sourceUpdatedAt)
	}
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

type scoredSearchTarget struct {
	TargetType string
	TargetKey  string
	Score      float64
}

func normalizeSearchRequest(limit int, targetTypes []string) (int, []string) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if len(targetTypes) == 0 {
		targetTypes = []string{"pull_request", "issue", "group"}
	}
	return limit, targetTypes
}

func (s *Service) searchTextRows(ctx context.Context, repositoryID int64, query string, targetTypes []string, limit int) ([]scoredSearchTarget, error) {
	if s.db.Name() == "postgres" {
		return s.searchTextRowsPostgres(ctx, repositoryID, query, targetTypes, limit)
	}
	return s.searchTextRowsFallback(ctx, repositoryID, query, targetTypes, limit)
}

func (s *Service) searchTextRowsPostgres(ctx context.Context, repositoryID int64, query string, targetTypes []string, limit int) ([]scoredSearchTarget, error) {
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
		`, query, repositoryID, targetTypes, query, limit).Rows()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanScoredSearchRows(rows)
}

func (s *Service) searchTextRowsFallback(ctx context.Context, repositoryID int64, query string, targetTypes []string, limit int) ([]scoredSearchTarget, error) {
	var documents []database.SearchDocument
	if err := s.db.WithContext(ctx).
		Where("github_repository_id = ? AND target_type IN ?", repositoryID, targetTypes).
		Order("indexed_at DESC").
		Find(&documents).Error; err != nil {
		return nil, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil, nil
	}
	results := make([]scoredSearchTarget, 0, len(documents))
	for _, document := range documents {
		score := fallbackTextScore(query, document.SearchText)
		if score <= 0 {
			continue
		}
		results = append(results, scoredSearchTarget{
			TargetType: document.TargetType,
			TargetKey:  document.TargetKey,
			Score:      score,
		})
	}
	sortScoredSearchTargets(results)
	return trimScoredSearchTargets(results, limit), nil
}

func (s *Service) searchSimilarRows(ctx context.Context, repositoryID int64, queryVector []float32, targetTypes []string, limit int) ([]scoredSearchTarget, error) {
	if s.db.Name() == "postgres" {
		return s.searchSimilarRowsPostgres(ctx, repositoryID, queryVector, targetTypes, limit)
	}
	return s.searchSimilarRowsFallback(ctx, repositoryID, queryVector, targetTypes, limit)
}

func (s *Service) searchSimilarRowsPostgres(ctx context.Context, repositoryID int64, queryVector []float32, targetTypes []string, limit int) ([]scoredSearchTarget, error) {
	vector := pgvector.NewVector(queryVector)
	rows, err := s.db.WithContext(ctx).
		Raw(`
			SELECT github_repository_id, target_type, target_key, (1 - (embedding <=> ?)) AS score
			FROM embeddings
			WHERE github_repository_id = ?
			  AND embedding_model = ?
			  AND target_type IN ?
			ORDER BY embedding <=> ?
			LIMIT ?
		`, vector, repositoryID, s.indexer.embedding.Model(), targetTypes, vector, limit).Rows()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanScoredSearchRows(rows)
}

func (s *Service) searchSimilarRowsFallback(ctx context.Context, repositoryID int64, queryVector []float32, targetTypes []string, limit int) ([]scoredSearchTarget, error) {
	var embeddings []database.Embedding
	if err := s.db.WithContext(ctx).
		Where("github_repository_id = ? AND embedding_model = ? AND target_type IN ?", repositoryID, s.indexer.embedding.Model(), targetTypes).
		Find(&embeddings).Error; err != nil {
		return nil, err
	}
	results := make([]scoredSearchTarget, 0, len(embeddings))
	for _, row := range embeddings {
		score := cosineSimilarity(queryVector, row.Embedding.Slice())
		results = append(results, scoredSearchTarget{
			TargetType: row.TargetType,
			TargetKey:  row.TargetKey,
			Score:      score,
		})
	}
	sortScoredSearchTargets(results)
	return trimScoredSearchTargets(results, limit), nil
}

func scanScoredSearchRows(rows *sql.Rows) ([]scoredSearchTarget, error) {
	results := []scoredSearchTarget{}
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
		results = append(results, scoredSearchTarget{
			TargetType: targetType,
			TargetKey:  targetKey,
			Score:      score,
		})
	}
	return results, nil
}

func (s *Service) resolveSearchResults(ctx context.Context, repositoryID int64, rows []scoredSearchTarget) ([]TextSearchResult, error) {
	results := make([]TextSearchResult, 0, len(rows))
	summaries, err := s.searchObjectSummaries(ctx, repositoryID, rows)
	if err != nil {
		return nil, err
	}
	annotations, err := s.getAnnotationsForTargetKeys(ctx, repositoryID, annotationTargetsForSearchRows(rows))
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		result := TextSearchResult{
			TargetType: row.TargetType,
			TargetKey:  row.TargetKey,
			Score:      row.Score,
		}
		if row.TargetType == "group" {
			result, err = s.populateGroupSearchResultWithAnnotations(ctx, row.TargetKey, result, annotations)
		} else {
			result, err = s.populateObjectSearchResultWithHydration(row.TargetType, row.TargetKey, result, summaries, annotations)
		}
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

func (s *Service) searchObjectSummaries(ctx context.Context, repositoryID int64, rows []scoredSearchTarget) (map[string]GroupMemberObjectSummary, error) {
	refs := make([]ghreplica.ObjectRef, 0, len(rows))
	for _, row := range rows {
		if row.TargetType != "pull_request" && row.TargetType != "issue" {
			continue
		}
		number, ok := objectNumberFromTargetKey(row.TargetKey)
		if !ok {
			continue
		}
		refs = append(refs, ghreplica.ObjectRef{Type: row.TargetType, Number: number})
	}
	if len(refs) == 0 {
		return map[string]GroupMemberObjectSummary{}, nil
	}
	return s.mirrorObjectSummaries(ctx, repositoryID, refs)
}

func (s *Service) populateObjectSearchResult(ctx context.Context, repositoryID int64, targetType, targetKey string, result TextSearchResult) (TextSearchResult, error) {
	number, ok := objectNumberFromTargetKey(targetKey)
	if !ok {
		return result, nil
	}
	summaries, err := s.mirrorObjectSummaries(ctx, repositoryID, []ghreplica.ObjectRef{{Type: targetType, Number: number}})
	if err != nil {
		return TextSearchResult{}, err
	}
	if summary, ok := summaries[targetKey]; ok {
		result.ObjectSummary = &summary
	}
	annotations, err := s.getAnnotationsForTarget(ctx, targetType, repositoryID, number, nil)
	if err != nil {
		return TextSearchResult{}, err
	}
	result.Annotations = annotations
	return result, nil
}

func (s *Service) populateObjectSearchResultWithHydration(targetType, targetKey string, result TextSearchResult, summaries map[string]GroupMemberObjectSummary, annotations map[string]map[string]any) (TextSearchResult, error) {
	if _, ok := objectNumberFromTargetKey(targetKey); !ok {
		return result, nil
	}
	if summary, ok := summaries[targetKey]; ok {
		result.ObjectSummary = &summary
	}
	result.Annotations = annotations[annotationMapKey(targetType, targetKey)]
	return result, nil
}

func (s *Service) populateGroupSearchResult(ctx context.Context, repositoryID int64, targetKey string, result TextSearchResult) (TextSearchResult, error) {
	groupPublicID, ok := groupPublicIDFromTargetKey(targetKey)
	if !ok {
		return result, nil
	}
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
	return result, nil
}

func (s *Service) populateGroupSearchResultWithAnnotations(ctx context.Context, targetKey string, result TextSearchResult, annotations map[string]map[string]any) (TextSearchResult, error) {
	groupPublicID, ok := groupPublicIDFromTargetKey(targetKey)
	if !ok {
		return result, nil
	}
	group, err := s.lookupGroupByPublicID(ctx, groupPublicID)
	if err != nil {
		return TextSearchResult{}, translateDBError(err)
	}
	result.ID = group.PublicID
	result.Annotations = annotations[annotationMapKey("group", groupTargetKey(group.PublicID))]
	return result, nil
}

func (i *Indexer) baseSearchParts(ctx context.Context, repositoryID int64, targetType, targetKey string) ([]string, time.Time, error) {
	if targetType == "group" {
		return i.groupSearchParts(ctx, targetKey)
	}
	return i.objectSearchParts(ctx, repositoryID, targetType, targetKey)
}

func (i *Indexer) objectSearchParts(ctx context.Context, repositoryID int64, targetType, targetKey string) ([]string, time.Time, error) {
	number, ok := objectNumberFromTargetKey(targetKey)
	if !ok {
		return nil, time.Now().UTC(), nil
	}
	results, err := i.ghreplica.BatchGetObjects(ctx, repositoryID, []ghreplica.ObjectRef{{Type: targetType, Number: number}})
	if err != nil {
		return nil, time.Now().UTC(), err
	}
	if len(results) == 0 || !results[0].Found || results[0].Summary == nil {
		return nil, time.Now().UTC(), nil
	}
	parts := []string{}
	if strings.TrimSpace(results[0].Summary.Title) != "" {
		parts = append(parts, results[0].Summary.Title)
	}
	return parts, results[0].Summary.UpdatedAt.UTC(), nil
}

func (i *Indexer) groupSearchParts(ctx context.Context, targetKey string) ([]string, time.Time, error) {
	groupPublicID, ok := groupPublicIDFromTargetKey(targetKey)
	if !ok {
		return nil, time.Now().UTC(), nil
	}
	var group database.Group
	err := i.db.WithContext(ctx).Where("public_id = ?", groupPublicID).First(&group).Error
	if err != nil {
		return nil, time.Now().UTC(), nil
	}
	parts := []string{}
	if strings.TrimSpace(group.Title) != "" {
		parts = append(parts, group.Title)
	}
	if strings.TrimSpace(group.Description) != "" {
		parts = append(parts, group.Description)
	}
	return parts, group.UpdatedAt.UTC(), nil
}

func appendSearchableFieldValues(parts []string, sourceUpdatedAt time.Time, values []database.FieldValue) ([]string, time.Time) {
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
	return parts, sourceUpdatedAt
}

func fallbackTextScore(query, text string) float64 {
	text = strings.ToLower(strings.TrimSpace(text))
	if query == "" || text == "" {
		return 0
	}
	return float64(strings.Count(text, query))
}

func sortScoredSearchTargets(results []scoredSearchTarget) {
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			if results[i].TargetType == results[j].TargetType {
				return results[i].TargetKey < results[j].TargetKey
			}
			return results[i].TargetType < results[j].TargetType
		}
		return results[i].Score > results[j].Score
	})
}

func trimScoredSearchTargets(results []scoredSearchTarget, limit int) []scoredSearchTarget {
	if limit >= len(results) {
		return results
	}
	return results[:limit]
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot float64
	var normA float64
	var normB float64
	for i := range a {
		dot += float64(a[i] * b[i])
		normA += float64(a[i] * a[i])
		normB += float64(b[i] * b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
