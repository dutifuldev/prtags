package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dutifuldev/prtags/internal/auth"
	"github.com/stretchr/testify/require"
)

func TestAuthStatusReportsMissingToken(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("PRTAGS_CONFIG_DIR", tempDir)

	stdout, stderr, err := runCLI(t, "https://prtags.dutiful.dev", "auth", "status")
	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "Not logged in.")
}

func TestAuthStatusReportsStoredToken(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("PRTAGS_CONFIG_DIR", tempDir)
	writeStoredToken(t, tempDir, auth.StoredToken{
		Version:      "v1",
		Provider:     "github",
		AccessToken:  "gho_test",
		TokenType:    "bearer",
		Scope:        "repo read:org",
		UserLogin:    "dutifulbob",
		UserID:       7937614,
		OAuthBaseURL: "https://github.com",
		APIBaseURL:   "https://api.github.com",
		SavedAt:      time.Now().UTC(),
	})

	stdout, stderr, err := runCLI(t, "https://prtags.dutiful.dev", "auth", "status")
	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "Logged in as dutifulbob")
	require.Contains(t, stdout, "Scopes: repo read:org")
}

func TestAuthLogoutDeletesStoredToken(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("PRTAGS_CONFIG_DIR", tempDir)
	writeStoredToken(t, tempDir, auth.StoredToken{
		Version:      "v1",
		Provider:     "github",
		AccessToken:  "gho_test",
		TokenType:    "bearer",
		Scope:        "repo read:org",
		UserLogin:    "dutifulbob",
		UserID:       7937614,
		OAuthBaseURL: "https://github.com",
		APIBaseURL:   "https://api.github.com",
		SavedAt:      time.Now().UTC(),
	})

	stdout, stderr, err := runCLI(t, "https://prtags.dutiful.dev", "auth", "logout")
	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "Removed stored token")

	statusOut, statusErr, err := runCLI(t, "https://prtags.dutiful.dev", "auth", "status")
	require.NoError(t, err, statusErr)
	require.True(t, strings.Contains(statusOut, "Not logged in."))
}

func TestAuthLoginRoundTrip(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("PRTAGS_CONFIG_DIR", tempDir)

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/login/device/code":
			_ = json.NewEncoder(w).Encode(auth.DeviceCodeResponse{
				DeviceCode:      "device-123",
				UserCode:        "ABCD-EFGH",
				VerificationURI: server.URL + "/login/device",
				ExpiresIn:       60,
				Interval:        0,
			})
		case "/login/oauth/access_token":
			_ = json.NewEncoder(w).Encode(auth.AccessTokenResponse{
				AccessToken: "gho_test",
				TokenType:   "bearer",
				Scope:       "repo read:org",
			})
		case "/user":
			_ = json.NewEncoder(w).Encode(auth.Viewer{Login: "dutifulbob", ID: 7937614})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	t.Setenv("PRTAGS_GITHUB_OAUTH_BASE_URL", server.URL)
	t.Setenv("PRTAGS_GITHUB_API_URL", server.URL)

	stdout, stderr, err := runCLI(t, "https://prtags.dutiful.dev", "auth", "login", "--client-id", "client-123", "--scope", "repo read:org")
	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "Open "+server.URL+"/login/device")
	require.Contains(t, stdout, "Logged in as dutifulbob")

	token, err := auth.LoadStoredToken()
	require.NoError(t, err)
	require.Equal(t, "gho_test", token.AccessToken)
	require.Equal(t, "dutifulbob", token.UserLogin)
}
