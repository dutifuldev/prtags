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

func (c *Client) get(ctx context.Context, path string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
		return fmt.Errorf("ghreplica GET %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return json.NewDecoder(resp.Body).Decode(target)
}
