package ghreplica

import (
	"context"

	"github.com/dutifuldev/ghreplica/mirror"
	"gorm.io/gorm"
)

type Client struct {
	reader *mirror.Reader
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
	return NewClient(mirror.NewSchemaReader(db, schema))
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
