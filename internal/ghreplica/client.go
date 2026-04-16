package ghreplica

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
}

type Repository struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	FullName   string `json:"full_name"`
	HTMLURL    string `json:"html_url"`
	Visibility string `json:"visibility"`
	Private    bool   `json:"private"`
	Owner      struct {
		Login string `json:"login"`
	} `json:"owner"`
}

type Issue struct {
	ID        int64     `json:"id"`
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	HTMLURL   string    `json:"html_url"`
	UpdatedAt time.Time `json:"updated_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
}

type PullRequest struct {
	ID        int64     `json:"id"`
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	HTMLURL   string    `json:"html_url"`
	UpdatedAt time.Time `json:"updated_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
}

type BatchObjectRef struct {
	Type   string `json:"type"`
	Number int    `json:"number"`
}

type BatchObjectSummary struct {
	Title       string
	State       string
	HTMLURL     string
	AuthorLogin string
	UpdatedAt   time.Time
}

type BatchObjectResult struct {
	Type    string
	Number  int
	Found   bool
	Summary *BatchObjectSummary
}

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

func (c *Client) GetRepository(ctx context.Context, owner, repo string) (Repository, error) {
	var out Repository
	err := c.get(ctx, fmt.Sprintf("/v1/github/repos/%s/%s", owner, repo), &out)
	return out, err
}

func (c *Client) GetIssue(ctx context.Context, owner, repo string, number int) (Issue, error) {
	var out Issue
	err := c.get(ctx, fmt.Sprintf("/v1/github/repos/%s/%s/issues/%d", owner, repo, number), &out)
	return out, err
}

func (c *Client) GetPullRequest(ctx context.Context, owner, repo string, number int) (PullRequest, error) {
	var out PullRequest
	err := c.get(ctx, fmt.Sprintf("/v1/github/repos/%s/%s/pulls/%d", owner, repo, number), &out)
	return out, err
}

func (c *Client) BatchGetObjects(ctx context.Context, owner, repo string, objects []BatchObjectRef) ([]BatchObjectResult, error) {
	if len(objects) == 0 {
		return nil, nil
	}

	var payload struct {
		Objects []BatchObjectRef `json:"objects"`
	}
	payload.Objects = objects

	var response struct {
		Results []struct {
			Type   string          `json:"type"`
			Number int             `json:"number"`
			Found  bool            `json:"found"`
			Object json.RawMessage `json:"object"`
		} `json:"results"`
	}
	if err := c.post(ctx, fmt.Sprintf("/v1/github-ext/repos/%s/%s/objects/batch", owner, repo), payload, &response); err != nil {
		return nil, err
	}

	results := make([]BatchObjectResult, 0, len(response.Results))
	for _, item := range response.Results {
		result := BatchObjectResult{
			Type:   item.Type,
			Number: item.Number,
			Found:  item.Found,
		}
		if item.Found {
			summary, err := decodeBatchObjectSummary(item.Type, item.Object)
			if err != nil {
				return nil, err
			}
			result.Summary = summary
		}
		results = append(results, result)
	}
	return results, nil
}

func (c *Client) get(ctx context.Context, path string, target any) error {
	return c.requestJSON(ctx, http.MethodGet, path, nil, target)
}

func (c *Client) post(ctx context.Context, path string, body any, target any) error {
	return c.requestJSON(ctx, http.MethodPost, path, body, target)
}

func (c *Client) requestJSON(ctx context.Context, method, path string, body any, target any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = strings.NewReader(string(raw))
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
		return fmt.Errorf("ghreplica %s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if target == nil {
		return nil
	}

	return json.NewDecoder(resp.Body).Decode(target)
}

func decodeBatchObjectSummary(objectType string, raw json.RawMessage) (*BatchObjectSummary, error) {
	switch objectType {
	case "issue":
		var issue Issue
		if err := json.Unmarshal(raw, &issue); err != nil {
			return nil, err
		}
		return &BatchObjectSummary{
			Title:       issue.Title,
			State:       issue.State,
			HTMLURL:     issue.HTMLURL,
			AuthorLogin: issue.User.Login,
			UpdatedAt:   issue.UpdatedAt,
		}, nil
	case "pull_request":
		var pull PullRequest
		if err := json.Unmarshal(raw, &pull); err != nil {
			return nil, err
		}
		return &BatchObjectSummary{
			Title:       pull.Title,
			State:       pull.State,
			HTMLURL:     pull.HTMLURL,
			AuthorLogin: pull.User.Login,
			UpdatedAt:   pull.UpdatedAt,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported batch object type %q", objectType)
	}
}
