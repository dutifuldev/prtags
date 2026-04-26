package mirrordb

import (
	"context"
	"database/sql"
	"time"

	"github.com/dutifuldev/ghreplica/mirror"
	"gorm.io/gorm"
)

type Reader struct {
	reader *mirror.Reader
	db     *gorm.DB
	tables mirror.TableNames
}

type Repository = mirror.RepositoryObject
type ObjectRef = mirror.ObjectRef
type ObjectSummary = mirror.ObjectSummary
type ObjectResult = mirror.ObjectResult

func NewReader(reader *mirror.Reader) *Reader {
	return &Reader{reader: reader}
}

func NewSchemaReader(db *gorm.DB, schema string) *Reader {
	return &Reader{
		reader: mirror.NewSchemaReader(db, schema),
		db:     db,
		tables: mirror.SchemaTableNames(schema),
	}
}

func (r *Reader) Repository(ctx context.Context, owner, repo string) (Repository, error) {
	row, err := r.reader.RepositoryByOwnerName(ctx, owner, repo)
	if err != nil {
		return Repository{}, err
	}
	return mirror.RepositoryObjectFromRow(row), nil
}

func (r *Reader) BatchObjects(ctx context.Context, repositoryID int64, objects []ObjectRef) ([]ObjectResult, error) {
	if len(objects) == 0 {
		return []ObjectResult{}, nil
	}
	if r.db != nil {
		return r.batchObjectsJoined(ctx, repositoryID, objects)
	}
	return r.batchObjectsReader(ctx, repositoryID, objects)
}

func (r *Reader) batchObjectsReader(ctx context.Context, repositoryID int64, objects []ObjectRef) ([]ObjectResult, error) {
	issueNumbers, pullNumbers := objectNumbersByType(objects)
	issuesByNumber, err := r.issueSummariesByNumber(ctx, repositoryID, issueNumbers)
	if err != nil {
		return nil, err
	}
	pullsByNumber, err := r.pullSummariesByNumber(ctx, repositoryID, pullNumbers)
	if err != nil {
		return nil, err
	}
	return objectResultsFromSummaries(objects, issuesByNumber, pullsByNumber), nil
}

func (r *Reader) batchObjectsJoined(ctx context.Context, repositoryID int64, objects []ObjectRef) ([]ObjectResult, error) {
	issueNumbers, pullNumbers := objectNumbersByType(objects)
	if len(issueNumbers) == 0 && len(pullNumbers) == 0 {
		return objectResultsFromSummaries(objects, nil, nil), nil
	}

	mirrorRepositoryID, err := r.repositoryMirrorID(ctx, repositoryID)
	if err != nil {
		return nil, err
	}

	issuesByNumber, err := r.issueSummariesByNumberJoined(ctx, mirrorRepositoryID, issueNumbers)
	if err != nil {
		return nil, err
	}
	pullsByNumber, err := r.pullSummariesByNumberJoined(ctx, mirrorRepositoryID, pullNumbers)
	if err != nil {
		return nil, err
	}
	return objectResultsFromSummaries(objects, issuesByNumber, pullsByNumber), nil
}

func objectResultsFromSummaries(objects []ObjectRef, issuesByNumber, pullsByNumber map[int]ObjectSummary) []ObjectResult {
	results := make([]ObjectResult, 0, len(objects))
	for _, object := range objects {
		result := ObjectResult{
			Type:   object.Type,
			Number: object.Number,
		}
		switch object.Type {
		case mirror.ObjectTypeIssue:
			if summary, ok := issuesByNumber[object.Number]; ok {
				result.Found = true
				result.Summary = &summary
			}
		case mirror.ObjectTypePullRequest:
			if summary, ok := pullsByNumber[object.Number]; ok {
				result.Found = true
				result.Summary = &summary
			}
		}
		results = append(results, result)
	}
	return results
}

func objectNumbersByType(objects []ObjectRef) ([]int, []int) {
	issueNumbers := make([]int, 0, len(objects))
	pullNumbers := make([]int, 0, len(objects))
	seenIssues := map[int]struct{}{}
	seenPulls := map[int]struct{}{}
	for _, object := range objects {
		if object.Number <= 0 {
			continue
		}
		switch object.Type {
		case mirror.ObjectTypeIssue:
			if _, ok := seenIssues[object.Number]; ok {
				continue
			}
			seenIssues[object.Number] = struct{}{}
			issueNumbers = append(issueNumbers, object.Number)
		case mirror.ObjectTypePullRequest:
			if _, ok := seenPulls[object.Number]; ok {
				continue
			}
			seenPulls[object.Number] = struct{}{}
			pullNumbers = append(pullNumbers, object.Number)
		}
	}
	return issueNumbers, pullNumbers
}

func (r *Reader) issueSummariesByNumber(ctx context.Context, repositoryID int64, numbers []int) (map[int]ObjectSummary, error) {
	rows, err := r.reader.IssuesByGitHubRepositoryID(ctx, repositoryID, numbers)
	if err != nil {
		return nil, err
	}
	summaries := make(map[int]ObjectSummary, len(rows))
	for _, row := range rows {
		summaries[row.Number] = mirror.SummaryFromIssue(row)
	}
	return summaries, nil
}

func (r *Reader) pullSummariesByNumber(ctx context.Context, repositoryID int64, numbers []int) (map[int]ObjectSummary, error) {
	rows, err := r.reader.PullRequestsByGitHubRepositoryID(ctx, repositoryID, numbers)
	if err != nil {
		return nil, err
	}
	summaries := make(map[int]ObjectSummary, len(rows))
	for _, row := range rows {
		summaries[row.Number] = mirror.SummaryFromPullRequest(row)
	}
	return summaries, nil
}

type objectSummaryRow struct {
	Number      int
	Title       string
	State       string
	HTMLURL     string
	AuthorLogin sql.NullString
	UpdatedAt   time.Time
}

func (r *Reader) repositoryMirrorID(ctx context.Context, githubRepositoryID int64) (uint, error) {
	var row struct {
		ID uint
	}
	err := r.db.WithContext(ctx).
		Table(r.tables.Repositories).
		Select("id").
		Where("github_id = ?", githubRepositoryID).
		First(&row).Error
	return row.ID, err
}

func (r *Reader) issueSummariesByNumberJoined(ctx context.Context, mirrorRepositoryID uint, numbers []int) (map[int]ObjectSummary, error) {
	if len(numbers) == 0 {
		return map[int]ObjectSummary{}, nil
	}
	var rows []objectSummaryRow
	if err := r.db.WithContext(ctx).
		Table(r.tables.Issues+" AS issues").
		Select("issues.number, issues.title, issues.state, issues.html_url, users.login AS author_login, issues.github_updated_at AS updated_at").
		Joins("LEFT JOIN "+r.tables.Users+" AS users ON users.id = issues.author_id").
		Where("issues.repository_id = ? AND issues.is_pull_request = ? AND issues.number IN ?", mirrorRepositoryID, false, numbers).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return summariesFromRows(rows), nil
}

func (r *Reader) pullSummariesByNumberJoined(ctx context.Context, mirrorRepositoryID uint, numbers []int) (map[int]ObjectSummary, error) {
	if len(numbers) == 0 {
		return map[int]ObjectSummary{}, nil
	}
	var rows []objectSummaryRow
	if err := r.db.WithContext(ctx).
		Table(r.tables.PullRequests+" AS pull_requests").
		Select("pull_requests.number, COALESCE(issues.title, '') AS title, pull_requests.state, pull_requests.html_url, users.login AS author_login, pull_requests.github_updated_at AS updated_at").
		Joins("LEFT JOIN "+r.tables.Issues+" AS issues ON issues.id = pull_requests.issue_id").
		Joins("LEFT JOIN "+r.tables.Users+" AS users ON users.id = issues.author_id").
		Where("pull_requests.repository_id = ? AND pull_requests.number IN ?", mirrorRepositoryID, numbers).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return summariesFromRows(rows), nil
}

func summariesFromRows(rows []objectSummaryRow) map[int]ObjectSummary {
	summaries := make(map[int]ObjectSummary, len(rows))
	for _, row := range rows {
		summaries[row.Number] = ObjectSummary{
			Title:       row.Title,
			State:       row.State,
			HTMLURL:     row.HTMLURL,
			AuthorLogin: row.AuthorLogin.String,
			UpdatedAt:   row.UpdatedAt,
		}
	}
	return summaries
}
