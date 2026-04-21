package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dutifuldev/prtags/internal/database"
	"github.com/dutifuldev/prtags/internal/githubapi"
	"gorm.io/gorm"
)

const (
	commentSyncDebounce       = 10 * time.Second
	commentSyncRepairInterval = 24 * time.Hour
	commentSyncStaleAfter     = 24 * time.Hour
)

type commentSyncDispatcher interface {
	EnqueueGroupCommentReconcileTx(tx *gorm.DB, syncTargetID uint, desiredRevision int, scheduledAt time.Time, verify bool) error
}

type CommentSyncService struct {
	db         *gorm.DB
	github     *githubapi.Client
	dispatcher commentSyncDispatcher
}

type GroupCommentSyncResult struct {
	GroupID           string `json:"group_id"`
	SyncTargetCount   int    `json:"sync_target_count"`
	CommentsScheduled int    `json:"comments_scheduled"`
}

func NewCommentSyncService(db *gorm.DB, githubClient *githubapi.Client, dispatcher commentSyncDispatcher) *CommentSyncService {
	return &CommentSyncService{
		db:         db,
		github:     githubClient,
		dispatcher: dispatcher,
	}
}

func (s *CommentSyncService) SetDispatcher(dispatcher commentSyncDispatcher) {
	s.dispatcher = dispatcher
}

func (s *CommentSyncService) Enabled() bool {
	return s != nil && s.github != nil && s.github.Enabled()
}

func (s *CommentSyncService) TriggerGroupSync(ctx context.Context, groupPublicID string) (GroupCommentSyncResult, error) {
	group, err := s.lookupGroupByPublicID(ctx, groupPublicID)
	if err != nil {
		return GroupCommentSyncResult{}, translateDBError(err)
	}
	if !s.Enabled() {
		return GroupCommentSyncResult{}, &FailError{StatusCode: 503, Message: "github comment sync is not configured"}
	}

	result := GroupCommentSyncResult{GroupID: group.PublicID}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		count, err := s.projectGroupTx(tx, group.ID)
		if err != nil {
			return err
		}
		result.SyncTargetCount = count
		result.CommentsScheduled = count
		return nil
	})
	return result, translateDBError(err)
}

func (s *CommentSyncService) ProjectEvent(ctx context.Context, eventID uint) error {
	if !s.Enabled() {
		return nil
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var event database.Event
		if err := tx.First(&event, eventID).Error; err != nil {
			return err
		}
		if event.AggregateType != "group" {
			return nil
		}

		var payload struct {
			GroupID uint `json:"group_id"`
		}
		if err := json.Unmarshal(event.PayloadJSON, &payload); err != nil {
			return err
		}
		if payload.GroupID == 0 {
			return nil
		}
		_, err := s.projectGroupTx(tx, payload.GroupID)
		return err
	})
}

func (s *CommentSyncService) Repair(ctx context.Context, groupID uint) error {
	if !s.Enabled() {
		return nil
	}

	var rows []database.GroupCommentSyncTarget
	query := s.db.WithContext(ctx).Model(&database.GroupCommentSyncTarget{})
	if groupID != 0 {
		query = query.Where("group_id = ?", groupID)
	}

	staleBefore := time.Now().UTC().Add(-commentSyncStaleAfter)
	if err := query.
		Where(
			"(desired_deleted = false AND github_comment_id IS NULL) OR "+
				"(last_error_at IS NOT NULL) OR "+
				"(last_synced_at IS NULL) OR "+
				"(last_synced_at <= ?)",
			staleBefore,
		).
		Order("updated_at ASC").
		Limit(50).
		Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, row := range rows {
			if err := s.dispatcher.EnqueueGroupCommentReconcileTx(tx, row.ID, row.DesiredRevision, time.Now().UTC(), true); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *CommentSyncService) Reconcile(ctx context.Context, syncTargetID uint, desiredRevision int, verify bool) error {
	if !s.Enabled() {
		return nil
	}

	var row database.GroupCommentSyncTarget
	if err := s.db.WithContext(ctx).First(&row, syncTargetID).Error; err != nil {
		return err
	}

	if desiredRevision <= row.AppliedRevision && !verify {
		return nil
	}

	group, err := s.groupByID(ctx, row.GroupID)
	if err != nil {
		return translateDBError(err)
	}

	if row.DesiredDeleted {
		return s.reconcileDelete(ctx, group, &row)
	}

	body, hash, shouldExist, err := s.renderCommentBody(ctx, group, row.ObjectType, row.ObjectNumber)
	if err != nil {
		_ = s.markSyncFailed(ctx, &row, "", err)
		return err
	}
	if !shouldExist {
		return s.reconcileDelete(ctx, group, &row)
	}

	if row.GitHubCommentID != nil && row.CommentBodyHash == hash && !verify {
		return s.markSyncSucceeded(ctx, &row, row.GitHubCommentID, hash)
	}

	if row.GitHubCommentID != nil && verify {
		if _, err := s.github.GetIssueComment(ctx, group.RepositoryOwner, group.RepositoryName, *row.GitHubCommentID); err != nil {
			var apiErr *githubapi.Error
			if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
				row.GitHubCommentID = nil
			} else if isPermissionDenied(apiErr, true) {
				return s.markPermissionDenied(ctx, &row, err)
			} else {
				_ = s.markSyncFailed(ctx, &row, "", err)
				return err
			}
		}
	}

	marker := markerForTarget(group.PublicID, group.GitHubRepositoryID, row.ObjectType, row.ObjectNumber)
	canonical, duplicates, err := s.findManagedComments(ctx, group, row.ObjectNumber, marker)
	if err != nil {
		var apiErr *githubapi.Error
		if errors.As(err, &apiErr) && isPermissionDenied(apiErr, false) {
			return s.markPermissionDenied(ctx, &row, err)
		}
		_ = s.markSyncFailed(ctx, &row, "", err)
		return err
	}
	if len(duplicates) > 0 {
		if err := s.deleteExtraComments(ctx, group, duplicates); err != nil {
			_ = s.markSyncFailed(ctx, &row, "", err)
			return err
		}
	}

	if canonical != nil {
		if row.GitHubCommentID == nil || *row.GitHubCommentID != canonical.ID {
			row.GitHubCommentID = int64Ptr(canonical.ID)
		}
		if canonical.Body == body {
			return s.markSyncSucceeded(ctx, &row, row.GitHubCommentID, hash)
		}
		updated, err := s.github.UpdateIssueComment(ctx, group.RepositoryOwner, group.RepositoryName, canonical.ID, body)
		if err != nil {
			var apiErr *githubapi.Error
			if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
				row.GitHubCommentID = nil
			} else if isPermissionDenied(apiErr, false) {
				return s.markPermissionDenied(ctx, &row, err)
			} else {
				_ = s.markSyncFailed(ctx, &row, "", err)
				return err
			}
		} else {
			return s.markSyncSucceeded(ctx, &row, int64Ptr(updated.ID), hash)
		}
	}

	created, err := s.github.CreateIssueComment(ctx, group.RepositoryOwner, group.RepositoryName, row.ObjectNumber, body)
	if err != nil {
		var apiErr *githubapi.Error
		if errors.As(err, &apiErr) && isPermissionDenied(apiErr, false) {
			return s.markPermissionDenied(ctx, &row, err)
		}
		_ = s.markSyncFailed(ctx, &row, "", err)
		return err
	}
	return s.markSyncSucceeded(ctx, &row, int64Ptr(created.ID), hash)
}

func (s *CommentSyncService) projectGroupTx(tx *gorm.DB, groupID uint) (int, error) {
	if s.dispatcher == nil {
		return 0, nil
	}

	group, err := s.groupByIDTx(tx, groupID)
	if err != nil {
		return 0, err
	}

	var members []database.GroupMember
	if err := tx.Where("group_id = ?", group.ID).Order("object_number ASC, id ASC").Find(&members).Error; err != nil {
		return 0, err
	}

	current := make(map[string]database.GroupMember)
	currentOrder := make([]string, 0, len(members))
	for _, member := range members {
		if member.GitHubRepositoryID != group.GitHubRepositoryID {
			continue
		}
		if member.ObjectType != "issue" && member.ObjectType != "pull_request" {
			continue
		}
		key := member.ObjectType + ":" + strconv.Itoa(member.ObjectNumber)
		current[key] = member
		currentOrder = append(currentOrder, key)
	}

	var existing []database.GroupCommentSyncTarget
	if err := tx.Where("group_id = ?", group.ID).Find(&existing).Error; err != nil {
		return 0, err
	}
	existingByKey := make(map[string]database.GroupCommentSyncTarget, len(existing))
	for _, row := range existing {
		existingByKey[row.ObjectType+":"+strconv.Itoa(row.ObjectNumber)] = row
	}

	now := time.Now().UTC()
	scheduledAt := now.Add(commentSyncDebounce)
	affected := 0

	if len(current) <= 1 {
		for _, row := range existing {
			nextRevision := row.DesiredRevision + 1
			if err := tx.Model(&database.GroupCommentSyncTarget{}).
				Where("id = ?", row.ID).
				Updates(map[string]any{
					"desired_revision": nextRevision,
					"desired_deleted":  true,
					"updated_at":       now,
				}).Error; err != nil {
				return 0, err
			}
			if err := s.dispatcher.EnqueueGroupCommentReconcileTx(tx, row.ID, nextRevision, scheduledAt, false); err != nil {
				return 0, err
			}
			affected++
		}
		return affected, nil
	}

	for _, key := range currentOrder {
		member := current[key]
		if existing, ok := existingByKey[key]; ok {
			nextRevision := existing.DesiredRevision + 1
			if err := tx.Model(&database.GroupCommentSyncTarget{}).
				Where("id = ?", existing.ID).
				Updates(map[string]any{
					"desired_revision": nextRevision,
					"desired_deleted":  false,
					"target_key":       member.TargetKey,
					"updated_at":       now,
				}).Error; err != nil {
				return 0, err
			}
			if err := s.dispatcher.EnqueueGroupCommentReconcileTx(tx, existing.ID, nextRevision, scheduledAt, false); err != nil {
				return 0, err
			}
			affected++
			continue
		}

		row := database.GroupCommentSyncTarget{
			GitHubRepositoryID: group.GitHubRepositoryID,
			GroupID:            group.ID,
			ObjectType:         member.ObjectType,
			ObjectNumber:       member.ObjectNumber,
			TargetKey:          member.TargetKey,
			DesiredRevision:    1,
			AppliedRevision:    0,
			DesiredDeleted:     false,
		}
		if err := tx.Create(&row).Error; err != nil {
			return 0, err
		}
		if err := s.dispatcher.EnqueueGroupCommentReconcileTx(tx, row.ID, row.DesiredRevision, scheduledAt, false); err != nil {
			return 0, err
		}
		affected++
	}

	for _, row := range existing {
		key := row.ObjectType + ":" + strconv.Itoa(row.ObjectNumber)
		if _, ok := current[key]; ok {
			continue
		}
		nextRevision := row.DesiredRevision + 1
		if err := tx.Model(&database.GroupCommentSyncTarget{}).
			Where("id = ?", row.ID).
			Updates(map[string]any{
				"desired_revision": nextRevision,
				"desired_deleted":  true,
				"updated_at":       now,
			}).Error; err != nil {
			return 0, err
		}
		if err := s.dispatcher.EnqueueGroupCommentReconcileTx(tx, row.ID, nextRevision, scheduledAt, false); err != nil {
			return 0, err
		}
		affected++
	}

	return affected, nil
}

func (s *CommentSyncService) reconcileDelete(ctx context.Context, group database.Group, row *database.GroupCommentSyncTarget) error {
	if row.GitHubCommentID != nil {
		if err := s.github.DeleteIssueComment(ctx, group.RepositoryOwner, group.RepositoryName, *row.GitHubCommentID); err != nil {
			var apiErr *githubapi.Error
			if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
				row.GitHubCommentID = nil
			} else if isPermissionDenied(apiErr, false) {
				return s.markPermissionDenied(ctx, row, err)
			} else {
				_ = s.markSyncFailed(ctx, row, "", err)
				return err
			}
		}
	}

	marker := markerForTarget(group.PublicID, group.GitHubRepositoryID, row.ObjectType, row.ObjectNumber)
	managed, duplicates, err := s.findManagedComments(ctx, group, row.ObjectNumber, marker)
	if err != nil {
		var apiErr *githubapi.Error
		if errors.As(err, &apiErr) && isPermissionDenied(apiErr, false) {
			return s.markPermissionDenied(ctx, row, err)
		}
		_ = s.markSyncFailed(ctx, row, "", err)
		return err
	}
	if managed != nil {
		duplicates = append(duplicates, *managed)
	}
	if err := s.deleteExtraComments(ctx, group, duplicates); err != nil {
		_ = s.markSyncFailed(ctx, row, "", err)
		return err
	}
	return s.markSyncSucceeded(ctx, row, nil, "")
}

func (s *CommentSyncService) findManagedComments(ctx context.Context, group database.Group, issueNumber int, marker string) (*githubapi.IssueComment, []githubapi.IssueComment, error) {
	comments, err := s.github.ListIssueCommentsForIssue(ctx, group.RepositoryOwner, group.RepositoryName, issueNumber)
	if err != nil {
		return nil, nil, err
	}
	matches := make([]githubapi.IssueComment, 0, len(comments))
	for _, comment := range comments {
		if strings.Contains(comment.Body, marker) {
			matches = append(matches, comment)
		}
	}
	if len(matches) == 0 {
		return nil, nil, nil
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
	return &matches[0], matches[1:], nil
}

func (s *CommentSyncService) deleteExtraComments(ctx context.Context, group database.Group, comments []githubapi.IssueComment) error {
	for _, comment := range comments {
		if err := s.github.DeleteIssueComment(ctx, group.RepositoryOwner, group.RepositoryName, comment.ID); err != nil {
			var apiErr *githubapi.Error
			if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
				continue
			}
			return err
		}
	}
	return nil
}

func (s *CommentSyncService) renderCommentBody(ctx context.Context, group database.Group, currentType string, currentNumber int) (string, string, bool, error) {
	var members []database.GroupMember
	if err := s.db.WithContext(ctx).
		Where("group_id = ? AND github_repository_id = ? AND object_type IN ?", group.ID, group.GitHubRepositoryID, []string{"issue", "pull_request"}).
		Order("object_number ASC, id ASC").
		Find(&members).Error; err != nil {
		return "", "", false, err
	}

	if len(members) <= 1 {
		return "", "", false, nil
	}

	objectNumbers := make([]int, 0, len(members))
	for _, member := range members {
		objectNumbers = append(objectNumbers, member.ObjectNumber)
	}

	var projections []database.TargetProjection
	if err := s.db.WithContext(ctx).
		Where("github_repository_id = ? AND object_number IN ?", group.GitHubRepositoryID, objectNumbers).
		Find(&projections).Error; err != nil {
		return "", "", false, err
	}
	projectionByKey := make(map[string]database.TargetProjection, len(projections))
	for _, projection := range projections {
		projectionByKey[objectTargetKey(group.GitHubRepositoryID, projection.TargetType, projection.ObjectNumber)] = projection
	}

	lines := []string{
		markerForTarget(group.PublicID, group.GitHubRepositoryID, currentType, currentNumber),
		"",
		fmt.Sprintf("Related work from PRtags group `%s`", group.PublicID),
		"",
		"Title: " + markdownCell(group.Title),
	}
	if status := strings.TrimSpace(group.Status); status != "" && !strings.EqualFold(status, "open") {
		lines = append(lines, "Status: "+markdownCell(status))
	}
	lines = append(lines, "", "| Number | Title |", "| --- | --- |")

	for _, member := range members {
		projection, ok := projectionByKey[member.TargetKey]
		numberLabel := fmt.Sprintf("#%d", member.ObjectNumber)
		if member.ObjectType == currentType && member.ObjectNumber == currentNumber {
			numberLabel += "*"
		}
		numberCell := fmt.Sprintf("[%s](%s)", numberLabel, issueURL(group.RepositoryOwner, group.RepositoryName, member.ObjectType, member.ObjectNumber, projection.HTMLURL))
		title := "Title unavailable"
		if ok && strings.TrimSpace(projection.Title) != "" {
			title = projection.Title
		}
		lines = append(lines, fmt.Sprintf("| %s | %s |", numberCell, markdownCell(title)))
	}
	lines = append(lines, "", selfReferenceFootnote(currentType))

	body := strings.Join(lines, "\n")
	sum := sha256.Sum256([]byte(body))
	return body, hex.EncodeToString(sum[:]), true, nil
}

func (s *CommentSyncService) markSyncSucceeded(ctx context.Context, row *database.GroupCommentSyncTarget, commentID *int64, hash string) error {
	now := time.Now().UTC()
	row.GitHubCommentID = commentID
	row.CommentBodyHash = hash
	row.AppliedRevision = row.DesiredRevision
	row.LastSyncedAt = &now
	row.LastError = ""
	row.LastErrorKind = ""
	row.LastErrorAt = nil
	updates := map[string]any{
		"comment_body_hash": hash,
		"applied_revision":  row.DesiredRevision,
		"last_synced_at":    now,
		"last_error":        "",
		"last_error_kind":   "",
		"last_error_at":     gorm.Expr("NULL"),
		"updated_at":        now,
	}
	if commentID == nil {
		updates["github_comment_id"] = gorm.Expr("NULL")
	} else {
		updates["github_comment_id"] = *commentID
	}
	return s.db.WithContext(ctx).Model(&database.GroupCommentSyncTarget{}).
		Where("id = ?", row.ID).
		Updates(updates).Error
}

func (s *CommentSyncService) markSyncFailed(ctx context.Context, row *database.GroupCommentSyncTarget, kind string, failure error) error {
	now := time.Now().UTC()
	return s.db.WithContext(ctx).Model(&database.GroupCommentSyncTarget{}).
		Where("id = ?", row.ID).
		Updates(map[string]any{
			"last_error":      strings.TrimSpace(failure.Error()),
			"last_error_kind": strings.TrimSpace(kind),
			"last_error_at":   now,
			"updated_at":      now,
		}).Error
}

func (s *CommentSyncService) markPermissionDenied(ctx context.Context, row *database.GroupCommentSyncTarget, failure error) error {
	return s.markSyncFailed(ctx, row, "permission_denied", failure)
}

func (s *CommentSyncService) lookupGroupByPublicID(ctx context.Context, groupPublicID string) (database.Group, error) {
	var group database.Group
	err := s.db.WithContext(ctx).Where("public_id = ?", groupPublicID).First(&group).Error
	return group, err
}

func (s *CommentSyncService) groupByID(ctx context.Context, groupID uint) (database.Group, error) {
	return s.groupByIDTx(s.db.WithContext(ctx), groupID)
}

func (s *CommentSyncService) groupByIDTx(tx *gorm.DB, groupID uint) (database.Group, error) {
	var group database.Group
	err := tx.Where("id = ?", groupID).First(&group).Error
	return group, err
}

func markerForTarget(groupPublicID string, repositoryID int64, targetType string, targetNumber int) string {
	return fmt.Sprintf("<!-- prtags:group-comment v1 group_id=%s repo_id=%d target_type=%s target_number=%d -->", groupPublicID, repositoryID, targetType, targetNumber)
}

func issueURL(owner, repo, targetType string, number int, fallback string) string {
	if strings.TrimSpace(fallback) != "" {
		return strings.TrimSpace(fallback)
	}
	path := "issues"
	if targetType == "pull_request" {
		path = "pull"
	}
	return fmt.Sprintf("https://github.com/%s/%s/%s/%d", owner, repo, path, number)
}

func selfReferenceFootnote(targetType string) string {
	if targetType == "pull_request" {
		return "`*` This PR"
	}
	return "`*` This issue"
}

func markdownCell(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "|", "\\|")
	return value
}

func isPermissionDenied(apiErr *githubapi.Error, lookupOnly bool) bool {
	if apiErr == nil {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(apiErr.Message))
	if apiErr.StatusCode == 403 {
		return true
	}
	if strings.Contains(message, "resource not accessible by integration") {
		return true
	}
	if !lookupOnly && apiErr.StatusCode == 404 {
		return true
	}
	return false
}

func int64Ptr(value int64) *int64 {
	return &value
}
