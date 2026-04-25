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
