package main

import (
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
