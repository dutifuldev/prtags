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

type Identity struct {
	GitHubUserID int64
	GitHubLogin  string
}

type Checker interface {
	CanWrite(ctx context.Context, actor Actor, owner, repo string) (bool, error)
}

type IdentityResolver interface {
	ResolveIdentity(ctx context.Context, actor Actor) (Identity, error)
}

type AllowAllChecker struct{}

func (AllowAllChecker) CanWrite(context.Context, Actor, string, string) (bool, error) {
	return true, nil
}

type GitHubChecker struct {
	mu              sync.Mutex
	permissionByKey map[string]cachedPermission
	identityByKey   map[string]cachedIdentity
	ttl             time.Duration
}

type cachedPermission struct {
	allowed   bool
	expiresAt time.Time
}

type cachedIdentity struct {
	identity  Identity
	expiresAt time.Time
}

func NewGitHubChecker(ttl time.Duration) *GitHubChecker {
	if ttl <= 0 {
		ttl = 2 * time.Minute
	}
	return &GitHubChecker{
		permissionByKey: make(map[string]cachedPermission),
		identityByKey:   make(map[string]cachedIdentity),
		ttl:             ttl,
	}
}

func (c *GitHubChecker) CanWrite(ctx context.Context, actor Actor, owner, repo string) (bool, error) {
	if actorToken(actor) == "" {
		return false, nil
	}

	cacheKey := permissionCacheKey(actor, owner, repo)
	now := time.Now().UTC()

	c.mu.Lock()
	if cached, ok := c.permissionByKey[cacheKey]; ok && cached.expiresAt.After(now) {
		c.mu.Unlock()
		return cached.allowed, nil
	}
	c.mu.Unlock()

	client, err := c.clientForActor(ctx, actor)
	if err != nil {
		return false, err
	}
	repository, resp, err := client.Repositories.Get(ctx, owner, repo)
	if err != nil {
		if isPermissionDeniedResponse(resp) {
			return false, nil
		}
		return false, err
	}

	allowed := false
	if perms := repository.GetPermissions(); perms != nil {
		allowed = perms["admin"] || perms["maintain"] || perms["push"]
	}

	c.mu.Lock()
	c.permissionByKey[cacheKey] = cachedPermission{
		allowed:   allowed,
		expiresAt: now.Add(c.ttl),
	}
	c.mu.Unlock()

	return allowed, nil
}

func (c *GitHubChecker) ResolveIdentity(ctx context.Context, actor Actor) (Identity, error) {
	if actorToken(actor) == "" {
		return Identity{}, nil
	}

	cacheKey := actorCacheKey(actor)
	now := time.Now().UTC()

	c.mu.Lock()
	if cached, ok := c.identityByKey[cacheKey]; ok && cached.expiresAt.After(now) {
		c.mu.Unlock()
		return cached.identity, nil
	}
	c.mu.Unlock()

	client, err := c.clientForActor(ctx, actor)
	if err != nil {
		return Identity{}, err
	}
	viewer, resp, err := client.Users.Get(ctx, "")
	if err != nil {
		if isPermissionDeniedResponse(resp) {
			return Identity{}, nil
		}
		return Identity{}, err
	}

	identity := Identity{
		GitHubUserID: viewer.GetID(),
		GitHubLogin:  strings.TrimSpace(viewer.GetLogin()),
	}

	c.mu.Lock()
	c.identityByKey[cacheKey] = cachedIdentity{
		identity:  identity,
		expiresAt: now.Add(c.ttl),
	}
	c.mu.Unlock()

	return identity, nil
}

func (c *GitHubChecker) clientForActor(ctx context.Context, actor Actor) (*github.Client, error) {
	httpClient := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: actorToken(actor)}))
	client := github.NewClient(httpClient)
	if baseURL := strings.TrimSpace(os.Getenv("GITHUB_API_URL")); baseURL != "" {
		enterpriseClient, err := client.WithEnterpriseURLs(baseURL, baseURL)
		if err != nil {
			return nil, err
		}
		client = enterpriseClient
	}
	return client, nil
}

func actorToken(actor Actor) string {
	return strings.TrimSpace(actor.Token)
}

func actorCacheKey(actor Actor) string {
	if strings.TrimSpace(actor.ID) != "" {
		return actor.ID
	}
	return actorToken(actor)
}

func permissionCacheKey(actor Actor, owner, repo string) string {
	return actorCacheKey(actor) + "|" + owner + "/" + repo
}

func isPermissionDeniedResponse(resp *github.Response) bool {
	if resp == nil {
		return false
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return true
	default:
		return false
	}
}
