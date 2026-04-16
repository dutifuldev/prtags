package permissions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
