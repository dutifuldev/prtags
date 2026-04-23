package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/dutifuldev/prtags/internal/auth"
	"github.com/stretchr/testify/require"
)

func isolateClientAuthEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("PRTAGS_GITHUB_TOKEN", "")
	t.Setenv("PRTAGS_ACTOR", "")
	t.Setenv("X_ACTOR", "")
	t.Setenv("PRTAGS_CONFIG_DIR", t.TempDir())
}

func TestClientDoJSONAddsAuthorizationHeader(t *testing.T) {
	isolateClientAuthEnv(t)
	t.Setenv("GH_TOKEN", "token-123")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer token-123", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"ok":true}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL)
	_, err := client.DoJSON(context.Background(), http.MethodGet, "/check", nil)
	require.NoError(t, err)
}

func TestClientDoJSONAddsActorHeaderWhenTokenMissing(t *testing.T) {
	isolateClientAuthEnv(t)
	t.Setenv("PRTAGS_ACTOR", "local-dev")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "", r.Header.Get("Authorization"))
		require.Equal(t, "local-dev", r.Header.Get("X-Actor"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"ok":true}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL)
	_, err := client.DoJSON(context.Background(), http.MethodGet, "/check", nil)
	require.NoError(t, err)
}

func TestClientUsesStoredTokenBeforeGenericEnvToken(t *testing.T) {
	isolateClientAuthEnv(t)
	tempDir := t.TempDir()
	t.Setenv("PRTAGS_CONFIG_DIR", tempDir)
	t.Setenv("GH_TOKEN", "env-token")

	raw, err := json.Marshal(auth.StoredToken{
		Version:     "v1",
		Provider:    "github",
		AccessToken: "stored-token",
		TokenType:   "bearer",
		UserLogin:   "bob",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "auth.json"), raw, 0o600))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer stored-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"ok":true}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL)
	_, err = client.DoJSON(context.Background(), http.MethodGet, "/check", nil)
	require.NoError(t, err)
}

func TestClientPrefersExplicitPRTagsTokenOverStoredToken(t *testing.T) {
	isolateClientAuthEnv(t)
	tempDir := t.TempDir()
	t.Setenv("PRTAGS_CONFIG_DIR", tempDir)
	t.Setenv("PRTAGS_GITHUB_TOKEN", "explicit-token")

	raw, err := json.Marshal(auth.StoredToken{
		Version:     "v1",
		Provider:    "github",
		AccessToken: "stored-token",
		TokenType:   "bearer",
		UserLogin:   "bob",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "auth.json"), raw, 0o600))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer explicit-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"ok":true}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL)
	_, err = client.DoJSON(context.Background(), http.MethodGet, "/check", nil)
	require.NoError(t, err)
}

func TestExtractJSendData(t *testing.T) {
	raw, err := ExtractJSendData([]byte(`{"status":"success","data":{"version":"v1","fields":[]}}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"version":"v1","fields":[]}`, string(raw))
}
