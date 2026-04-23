package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDeviceFlowRoundTrip(t *testing.T) {
	var polls int
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/login/device/code":
			require.Equal(t, http.MethodPost, r.Method)
			_ = json.NewEncoder(w).Encode(DeviceCodeResponse{
				DeviceCode:      "device-123",
				UserCode:        "ABCD-EFGH",
				VerificationURI: server.URL + "/login/device",
				ExpiresIn:       60,
				Interval:        1,
			})
		case "/login/oauth/access_token":
			polls++
			if polls == 1 {
				_ = json.NewEncoder(w).Encode(AccessTokenResponse{
					Error:            "authorization_pending",
					ErrorDescription: "still waiting",
				})
				return
			}
			_ = json.NewEncoder(w).Encode(AccessTokenResponse{
				AccessToken: "gho_test",
				TokenType:   "bearer",
				Scope:       "read:org,repo",
			})
		case "/user":
			require.Equal(t, "Bearer gho_test", r.Header.Get("Authorization"))
			_ = json.NewEncoder(w).Encode(Viewer{
				Login: "bob",
				ID:    42,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := Config{
		ClientID:     "client-123",
		Scope:        "read:org repo",
		OAuthBaseURL: server.URL,
		APIBaseURL:   server.URL,
		HTTPClient:   server.Client(),
	}

	device, err := cfg.StartDeviceFlow(context.Background())
	require.NoError(t, err)
	require.Equal(t, "device-123", device.DeviceCode)

	token, err := cfg.PollAccessToken(context.Background(), device.DeviceCode, 0, time.Minute)
	require.NoError(t, err)
	require.Equal(t, "gho_test", token.AccessToken)

	viewer, err := cfg.GetViewer(context.Background(), token.AccessToken)
	require.NoError(t, err)
	require.Equal(t, "bob", viewer.Login)
	require.EqualValues(t, 42, viewer.ID)
}

func TestStoredTokenRoundTrip(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("PRTAGS_CONFIG_DIR", tempDir)

	path, err := SaveStoredToken(StoredToken{
		AccessToken: "gho_saved",
		TokenType:   "bearer",
		Scope:       "read:org,repo",
		UserLogin:   "bob",
		UserID:      42,
		ClientID:    "client-123",
	})
	require.NoError(t, err)
	require.Equal(t, filepath.Join(tempDir, "auth.json"), path)

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	token, err := LoadStoredToken()
	require.NoError(t, err)
	require.Equal(t, "gho_saved", token.AccessToken)
	require.Equal(t, "bob", token.UserLogin)

	require.NoError(t, DeleteStoredToken())
	_, err = LoadStoredToken()
	require.Error(t, err)
}

func TestAuthHelperDefaultsAndPolling(t *testing.T) {
	t.Setenv("PRTAGS_GITHUB_OAUTH_CLIENT_ID", "env-client")
	t.Setenv("PRTAGS_GITHUB_OAUTH_SCOPE", "repo")
	t.Setenv("PRTAGS_GITHUB_OAUTH_BASE_URL", "https://oauth.example")
	t.Setenv("PRTAGS_GITHUB_API_URL", "https://api.example")

	cfg := DefaultConfig()
	require.Equal(t, "env-client", cfg.ClientID)
	require.Equal(t, "repo", cfg.Scope)
	require.Equal(t, "https://oauth.example", cfg.OAuthBaseURL)
	require.Equal(t, "https://api.example", cfg.APIBaseURL)
	require.NotNil(t, cfg.HTTPClient)

	t.Setenv("LAST_ENV", "fallback")
	require.Equal(t, "fallback", firstNonEmptyEnv("MISSING_ENV", "OTHER_ENV", "LAST_ENV"))

	done, interval, err := nextPollAction(AccessTokenResponse{AccessToken: "gho"}, time.Second)
	require.NoError(t, err)
	require.True(t, done)
	require.Equal(t, time.Second, interval)

	done, interval, err = nextPollAction(AccessTokenResponse{Error: "slow_down"}, time.Second)
	require.NoError(t, err)
	require.False(t, done)
	require.Equal(t, 6*time.Second, interval)

	done, interval, err = nextPollAction(AccessTokenResponse{Error: "authorization_pending"}, 500*time.Millisecond)
	require.NoError(t, err)
	require.False(t, done)
	require.Equal(t, 500*time.Millisecond, interval)

	_, _, err = nextPollAction(AccessTokenResponse{Error: "access_denied", ErrorDescription: "nope"}, time.Second)
	require.ErrorContains(t, err, "device authorization denied")

	require.NoError(t, deviceFlowPreflight(context.Background(), time.Now().UTC().Add(time.Second)))
	require.Error(t, deviceFlowPreflight(context.Background(), time.Now().UTC().Add(-time.Second)))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, waitForNextPoll(ctx, time.Second))
	require.NoError(t, waitForNextPoll(context.Background(), 0))
}

func TestAuthViewerAndTokenHelpers(t *testing.T) {
	t.Run("stored token path uses config dir", func(t *testing.T) {
		tempDir := t.TempDir()
		t.Setenv("PRTAGS_CONFIG_DIR", tempDir)
		path, err := StoredTokenPath()
		require.NoError(t, err)
		require.Equal(t, filepath.Join(tempDir, "auth.json"), path)
	})

	t.Run("load stored token rejects blank access token", func(t *testing.T) {
		tempDir := t.TempDir()
		t.Setenv("PRTAGS_CONFIG_DIR", tempDir)
		raw, err := json.Marshal(StoredToken{Version: "v1"})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(tempDir, "auth.json"), raw, 0o600))
		_, err = LoadStoredToken()
		require.ErrorContains(t, err, "missing access_token")
	})

	t.Run("get viewer surfaces api failures", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("upstream sad"))
		}))
		defer server.Close()

		cfg := Config{APIBaseURL: server.URL, HTTPClient: server.Client()}.withDefaults()
		_, err := cfg.GetViewer(context.Background(), "gho")
		require.ErrorContains(t, err, "github user lookup failed")
	})

	t.Run("post form helper decodes errors", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("bad form"))
		}))
		defer server.Close()

		cfg := Config{HTTPClient: server.Client()}
		err := cfg.postFormJSON(context.Background(), server.URL, nil, &map[string]any{})
		require.ErrorContains(t, err, "bad form")
	})

	t.Run("wait for next poll handles deadline", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		err := waitForNextPoll(ctx, time.Millisecond)
		require.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled))
	})
}
