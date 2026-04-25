package ghreplica

import (
	"context"
	"database/sql"
	"time"

	"github.com/dutifuldev/ghreplica/mirror"
	"gorm.io/gorm"
)

type Client struct {
	reader *mirror.Reader
	db     *gorm.DB
	tables mirror.TableNames
}

type Repository = mirror.RepositoryObject
type Issue = mirror.IssueObject
type PullRequest = mirror.PullRequestObject
type UserObject = mirror.UserObject
type ObjectRef = mirror.ObjectRef
type ObjectSummary = mirror.ObjectSummary
type ObjectResult = mirror.ObjectResult

func NewClient(reader *mirror.Reader) *Client {
	return &Client{reader: reader}
}

func NewSchemaClient(db *gorm.DB, schema string) *Client {
	return &Client{
		reader: mirror.NewSchemaReader(db, schema),
		db:     db,
		tables: mirror.SchemaTableNames(schema),
	}
}

func (c *Client) GetRepository(ctx context.Context, owner, repo string) (Repository, error) {
	row, err := c.reader.RepositoryByOwnerName(ctx, owner, repo)
	if err != nil {
		return Repository{}, err
	}
	return mirror.RepositoryObjectFromRow(row), nil
}

func (c *Client) GetIssue(ctx context.Context, owner, repo string, number int) (Issue, error) {
	repository, err := c.reader.RepositoryByOwnerName(ctx, owner, repo)
	if err != nil {
		return Issue{}, err
	}
	row, err := c.reader.IssueByRepositoryID(ctx, repository.ID, number)
	if err != nil {
		return Issue{}, err
	}
	return mirror.IssueObjectFromRow(row), nil
}

func (c *Client) GetPullRequest(ctx context.Context, owner, repo string, number int) (PullRequest, error) {
	repository, err := c.reader.RepositoryByOwnerName(ctx, owner, repo)
	if err != nil {
		return PullRequest{}, err
	}
	row, err := c.reader.PullRequestByRepositoryID(ctx, repository.ID, number)
	if err != nil {
		return PullRequest{}, err
	}
	return mirror.PullRequestObjectFromRow(row), nil
}

func (c *Client) BatchGetObjects(ctx context.Context, repositoryID int64, objects []ObjectRef) ([]ObjectResult, error) {
	if len(objects) == 0 {
		return []ObjectResult{}, nil
	}
	if c.db != nil {
		return c.batchGetObjectsJoined(ctx, repositoryID, objects)
	}
	return c.batchGetObjectsReader(ctx, repositoryID, objects)
}

func (c *Client) batchGetObjectsReader(ctx context.Context, repositoryID int64, objects []ObjectRef) ([]ObjectResult, error) {
	issueNumbers, pullNumbers := objectNumbersByType(objects)
	issuesByNumber, err := c.issueSummariesByNumber(ctx, repositoryID, issueNumbers)
	if err != nil {
		return nil, err
	}
	pullsByNumber, err := c.pullSummariesByNumber(ctx, repositoryID, pullNumbers)
	if err != nil {
		return nil, err
	}

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
	return results, nil
}

func (c *Client) batchGetObjectsJoined(ctx context.Context, repositoryID int64, objects []ObjectRef) ([]ObjectResult, error) {
	issueNumbers, pullNumbers := objectNumbersByType(objects)
	if len(issueNumbers) == 0 && len(pullNumbers) == 0 {
		return c.objectResultsFromSummaries(repositoryID, objects, nil, nil), nil
	}

	mirrorRepositoryID, err := c.repositoryMirrorID(ctx, repositoryID)
	if err != nil {
		return nil, err
	}

	issuesByNumber, err := c.issueSummariesByNumberJoined(ctx, mirrorRepositoryID, issueNumbers)
	if err != nil {
		return nil, err
	}
	pullsByNumber, err := c.pullSummariesByNumberJoined(ctx, mirrorRepositoryID, pullNumbers)
	if err != nil {
		return nil, err
	}
	return c.objectResultsFromSummaries(repositoryID, objects, issuesByNumber, pullsByNumber), nil
}

func (c *Client) objectResultsFromSummaries(_ int64, objects []ObjectRef, issuesByNumber, pullsByNumber map[int]ObjectSummary) []ObjectResult {
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

func (c *Client) issueSummariesByNumber(ctx context.Context, repositoryID int64, numbers []int) (map[int]ObjectSummary, error) {
	rows, err := c.reader.IssuesByGitHubRepositoryID(ctx, repositoryID, numbers)
	if err != nil {
		return nil, err
	}
	summaries := make(map[int]ObjectSummary, len(rows))
	for _, row := range rows {
		summaries[row.Number] = mirror.SummaryFromIssue(row)
	}
	return summaries, nil
}

func (c *Client) pullSummariesByNumber(ctx context.Context, repositoryID int64, numbers []int) (map[int]ObjectSummary, error) {
	rows, err := c.reader.PullRequestsByGitHubRepositoryID(ctx, repositoryID, numbers)
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

func (c *Client) repositoryMirrorID(ctx context.Context, githubRepositoryID int64) (uint, error) {
	var row struct {
		ID uint
	}
	err := c.db.WithContext(ctx).
		Table(c.tables.Repositories).
		Select("id").
		Where("github_id = ?", githubRepositoryID).
		First(&row).Error
	return row.ID, err
}

func (c *Client) issueSummariesByNumberJoined(ctx context.Context, mirrorRepositoryID uint, numbers []int) (map[int]ObjectSummary, error) {
	if len(numbers) == 0 {
		return map[int]ObjectSummary{}, nil
	}
	var rows []objectSummaryRow
	err := c.db.WithContext(ctx).Raw(`
		SELECT i.number,
		       i.title,
		       i.state,
		       i.html_url,
		       u.login AS author_login,
		       i.github_updated_at AS updated_at
		FROM `+c.tables.Issues+` i
		LEFT JOIN `+c.tables.Users+` u ON u.id = i.author_id
		WHERE i.repository_id = ? AND i.number IN ?
	`, mirrorRepositoryID, numbers).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return summariesFromRows(rows), nil
}

func (c *Client) pullSummariesByNumberJoined(ctx context.Context, mirrorRepositoryID uint, numbers []int) (map[int]ObjectSummary, error) {
	if len(numbers) == 0 {
		return map[int]ObjectSummary{}, nil
	}
	var rows []objectSummaryRow
	err := c.db.WithContext(ctx).Raw(`
		SELECT pr.number,
		       issue.title,
		       pr.state,
		       pr.html_url,
		       u.login AS author_login,
		       pr.github_updated_at AS updated_at
		FROM `+c.tables.PullRequests+` pr
		JOIN `+c.tables.Issues+` issue ON issue.id = pr.issue_id
		LEFT JOIN `+c.tables.Users+` u ON u.id = issue.author_id
		WHERE pr.repository_id = ? AND pr.number IN ?
	`, mirrorRepositoryID, numbers).Scan(&rows).Error
	if err != nil {
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
