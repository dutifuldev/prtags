package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dutifuldev/prtags/internal/auth"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestResolveGrantSubjectDefaultsGrantorToStoredToken(t *testing.T) {
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

	cmd := newGrantSubjectCommand()
	require.NoError(t, cmd.Flags().Set("github-user-id", "123"))
	require.NoError(t, cmd.Flags().Set("github-login", "someone-else"))

	subject, err := resolveGrantSubject(cmd)
	require.NoError(t, err)
	require.EqualValues(t, 123, subject.userID)
	require.Equal(t, "someone-else", subject.login)
	require.EqualValues(t, 7937614, subject.grantedByUserID)
	require.Equal(t, "dutifulbob", subject.grantedByLogin)
}

func TestResolveGrantSubjectRequiresGrantorIdentityWithoutStoredToken(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("PRTAGS_CONFIG_DIR", tempDir)

	cmd := newGrantSubjectCommand()
	require.NoError(t, cmd.Flags().Set("github-user-id", "123"))
	require.NoError(t, cmd.Flags().Set("github-login", "someone-else"))

	_, err := resolveGrantSubject(cmd)
	require.Error(t, err)
	require.Contains(t, err.Error(), "grantor identity is required")
}

func TestResolveGrantSubjectUsesExplicitGrantorFlags(t *testing.T) {
	cmd := newGrantSubjectCommand()
	require.NoError(t, cmd.Flags().Set("github-user-id", "123"))
	require.NoError(t, cmd.Flags().Set("github-login", "someone-else"))
	require.NoError(t, cmd.Flags().Set("granted-by-github-user-id", "456"))
	require.NoError(t, cmd.Flags().Set("granted-by-github-login", "operator"))

	subject, err := resolveGrantSubject(cmd)
	require.NoError(t, err)
	require.EqualValues(t, 456, subject.grantedByUserID)
	require.Equal(t, "operator", subject.grantedByLogin)
}

func TestAccessGrantCommandsReachServiceOpen(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("PRTAGS_CONFIG_DIR", tempDir)
	writeStoredToken(t, tempDir, auth.StoredToken{
		Version:     "v1",
		Provider:    "github",
		AccessToken: "gho_test",
		UserLogin:   "dutifulbob",
		UserID:      7937614,
	})
	t.Setenv("DATABASE_URL", "")

	_, _, err := runCLI(t, "https://prtags.dutiful.dev", "access", "grant", "list", "-R", "acme/widgets")
	require.ErrorContains(t, err, "DATABASE_URL is required")

	_, _, err = runCLI(t, "https://prtags.dutiful.dev", "access", "grant", "upsert", "-R", "acme/widgets", "--github-user-id", "123", "--github-login", "writer")
	require.ErrorContains(t, err, "DATABASE_URL is required")

	_, _, err = runCLI(t, "https://prtags.dutiful.dev", "access", "grant", "revoke", "-R", "acme/widgets", "--github-user-id", "123", "--github-login", "writer")
	require.ErrorContains(t, err, "DATABASE_URL is required")
}

func newGrantSubjectCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "grant"}
	cmd.Flags().Int64("github-user-id", 0, "")
	cmd.Flags().String("github-login", "", "")
	cmd.Flags().Int64("granted-by-github-user-id", 0, "")
	cmd.Flags().String("granted-by-github-login", "", "")
	cmd.Flags().Bool("self", false, "")
	return cmd
}

func writeStoredToken(t *testing.T, dir string, token auth.StoredToken) {
	t.Helper()
	raw, err := json.Marshal(token)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "auth.json"), raw, 0o600))
}
