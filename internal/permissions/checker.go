package permissions

import (
	"context"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v74/github"
	"golang.org/x/oauth2"
)

type Actor struct {
	Type  string
	ID    string
	Token string
}

type Checker interface {
	CanWrite(ctx context.Context, actor Actor, owner, repo string) (bool, error)
}

type AllowAllChecker struct{}

func (AllowAllChecker) CanWrite(context.Context, Actor, string, string) (bool, error) {
	return true, nil
}

type GitHubChecker struct {
	mu    sync.Mutex
	cache map[string]cachedPermission
	ttl   time.Duration
}

type cachedPermission struct {
	allowed   bool
	expiresAt time.Time
}

func NewGitHubChecker(ttl time.Duration) *GitHubChecker {
	if ttl <= 0 {
		ttl = 2 * time.Minute
	}
	return &GitHubChecker{
		cache: make(map[string]cachedPermission),
		ttl:   ttl,
	}
}

func (c *GitHubChecker) CanWrite(ctx context.Context, actor Actor, owner, repo string) (bool, error) {
	token := strings.TrimSpace(actor.Token)
	if token == "" {
		return false, nil
	}

	cacheKey := actor.ID + "|" + owner + "/" + repo
	now := time.Now().UTC()

	c.mu.Lock()
	if cached, ok := c.cache[cacheKey]; ok && cached.expiresAt.After(now) {
		c.mu.Unlock()
		return cached.allowed, nil
	}
	c.mu.Unlock()

	httpClient := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}))
	client := github.NewClient(httpClient)
	if baseURL := strings.TrimSpace(os.Getenv("GITHUB_API_URL")); baseURL != "" {
		enterpriseClient, err := client.WithEnterpriseURLs(baseURL, baseURL)
		if err != nil {
			return false, err
		}
		client = enterpriseClient
	}
	repository, resp, err := client.Repositories.Get(ctx, owner, repo)
	if err != nil {
		if resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound) {
			return false, nil
		}
		return false, err
	}

	allowed := false
	if perms := repository.GetPermissions(); perms != nil {
		allowed = perms["admin"] || perms["maintain"] || perms["push"]
	}

	c.mu.Lock()
	c.cache[cacheKey] = cachedPermission{
		allowed:   allowed,
		expiresAt: now.Add(c.ttl),
	}
	c.mu.Unlock()

	return allowed, nil
}
