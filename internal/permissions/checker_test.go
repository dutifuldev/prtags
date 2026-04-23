package permissions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v74/github"
	"github.com/stretchr/testify/require"
)

func TestGitHubCheckerMissingTokenDeniesWithoutError(t *testing.T) {
	checker := NewGitHubChecker(0)

	allowed, err := checker.CanWrite(context.Background(), Actor{Type: "github", ID: "user-without-token"}, "acme", "widgets")
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestGitHubCheckerTreatsNotFoundAndUnauthorizedAsDenied(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/repos/acme/widgets") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	t.Setenv("GITHUB_API_URL", server.URL+"/")

	checker := NewGitHubChecker(0)
	allowed, err := checker.CanWrite(context.Background(), Actor{Type: "github", ID: "tester", Token: "token"}, "acme", "widgets")
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestGitHubCheckerResolvesIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/user") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":7937614,"login":"dutifulbob"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	t.Setenv("GITHUB_API_URL", server.URL+"/")

	checker := NewGitHubChecker(0)
	identity, err := checker.ResolveIdentity(context.Background(), Actor{Type: "github", ID: "tester", Token: "token"})
	require.NoError(t, err)
	require.EqualValues(t, 7937614, identity.GitHubUserID)
	require.Equal(t, "dutifulbob", identity.GitHubLogin)
}

func TestPermissionHelpersAndCaching(t *testing.T) {
	allowed, err := (AllowAllChecker{}).CanWrite(context.Background(), Actor{}, "acme", "widgets")
	require.NoError(t, err)
	require.True(t, allowed)

	require.Equal(t, "token", actorToken(Actor{Token: " token "}))
	require.Equal(t, "actor-id", actorCacheKey(Actor{ID: "actor-id", Token: "token"}))
	require.Equal(t, "token", actorCacheKey(Actor{Token: "token"}))
	require.Equal(t, "actor-id|acme/widgets", permissionCacheKey(Actor{ID: "actor-id"}, "acme", "widgets"))
	require.True(t, isPermissionDeniedResponse(&github.Response{Response: &http.Response{StatusCode: http.StatusForbidden}}))
	require.False(t, isPermissionDeniedResponse(&github.Response{Response: &http.Response{StatusCode: http.StatusOK}}))
}

func TestGitHubCheckerCachesPermissions(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/repos/acme/widgets") {
			requests++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"permissions":{"push":true}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	t.Setenv("GITHUB_API_URL", server.URL+"/")
	checker := NewGitHubChecker(time.Hour)

	actor := Actor{Type: "github", ID: "tester", Token: "token"}
	first, err := checker.CanWrite(context.Background(), actor, "acme", "widgets")
	require.NoError(t, err)
	require.True(t, first)

	second, err := checker.CanWrite(context.Background(), actor, "acme", "widgets")
	require.NoError(t, err)
	require.True(t, second)
	require.Equal(t, 1, requests)
}
