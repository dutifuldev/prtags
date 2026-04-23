package githubapi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewClientDefaultsAndEnabled(t *testing.T) {
	client := NewClient("", AuthConfig{})
	require.Equal(t, "https://api.github.com", client.baseURL)
	require.False(t, client.Enabled())

	client = NewClient(" https://example.test/api/ ", AuthConfig{
		AppID:          " 1 ",
		InstallationID: " 2 ",
		PrivateKeyPEM:  " pem ",
	})
	require.Equal(t, "https://example.test/api", client.baseURL)
	require.True(t, client.Enabled())
}

func TestCreateAndUpdateIssueComment(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widgets/issues/22/comments":
			_ = json.NewEncoder(w).Encode(IssueComment{ID: 101, Body: "created"})
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/acme/widgets/issues/comments/101":
			_ = json.NewEncoder(w).Encode(IssueComment{ID: 101, Body: "updated"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, AuthConfig{})

	created, err := client.CreateIssueComment(context.Background(), "acme", "widgets", 22, "hello")
	require.NoError(t, err)
	require.EqualValues(t, 101, created.ID)

	updated, err := client.UpdateIssueComment(context.Background(), "acme", "widgets", 101, "hello again")
	require.NoError(t, err)
	require.Equal(t, "updated", updated.Body)
	require.EqualValues(t, 2, requestCount.Load())
}

func TestGetListAndDeleteIssueComment(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/issues/comments/101":
			_ = json.NewEncoder(w).Encode(IssueComment{ID: 101, Body: "loaded"})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/issues/22/comments":
			_ = json.NewEncoder(w).Encode([]IssueComment{{ID: 101, Body: "listed"}})
		case r.Method == http.MethodDelete && r.URL.Path == "/repos/acme/widgets/issues/comments/101":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, AuthConfig{})

	loaded, err := client.GetIssueComment(context.Background(), "acme", "widgets", 101)
	require.NoError(t, err)
	require.Equal(t, "loaded", loaded.Body)

	listed, err := client.ListIssueCommentsForIssue(context.Background(), "acme", "widgets", 22)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, "listed", listed[0].Body)

	require.NoError(t, client.DeleteIssueComment(context.Background(), "acme", "widgets", 101))
	require.EqualValues(t, 3, requestCount.Load())
}

func TestAuthorizationTokenCachesGitHubAppToken(t *testing.T) {
	privateKeyPEM := testRSAPrivateKeyPEM(t)
	var tokenRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/123/access_tokens":
			tokenRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      "ghs_cached",
				"expires_at": time.Now().UTC().Add(10 * time.Minute).Format(time.RFC3339),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, AuthConfig{
		AppID:          "42",
		InstallationID: "123",
		PrivateKeyPEM:  privateKeyPEM,
	})

	first, err := client.authorizationToken(context.Background())
	require.NoError(t, err)
	second, err := client.authorizationToken(context.Background())
	require.NoError(t, err)
	require.Equal(t, "ghs_cached", first)
	require.Equal(t, first, second)
	require.EqualValues(t, 1, tokenRequests.Load())
}

func TestAuthorizationTokenFailsWhenPrivateKeyPathIsUnreadable(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	client := NewClient("https://api.github.com", AuthConfig{
		AppID:          "42",
		InstallationID: "123",
		PrivateKeyPath: dir,
	})

	_, err := client.authorizationToken(context.Background())
	require.Error(t, err)
}

func TestPrivateKeySupportsPEMPathAndRejectsInvalidPEM(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.pem")
	require.NoError(t, os.WriteFile(path, []byte(testRSAPrivateKeyPEM(t)), 0o600))

	client := NewClient("https://api.github.com", AuthConfig{
		AppID:          "42",
		InstallationID: "123",
		PrivateKeyPath: path,
	})
	key, err := client.privateKey()
	require.NoError(t, err)
	require.NotNil(t, key)

	client = NewClient("https://api.github.com", AuthConfig{
		AppID:          "42",
		InstallationID: "123",
		PrivateKeyPEM:  "not-a-pem",
	})
	_, err = client.privateKey()
	require.ErrorContains(t, err, "invalid")
}

func TestWaitWriteTurnHonorsCancelledContext(t *testing.T) {
	client := NewClient("https://api.github.com", AuthConfig{})
	client.lastWriteAt = time.Now().UTC()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := client.waitWriteTurn(ctx)
	require.ErrorIs(t, err, context.Canceled)
}

func TestDecodeHTTPErrorPrefersJSONMessageAndRetryHeaders(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header: http.Header{
			"Retry-After": []string{"9"},
		},
		Body: ioNopCloser(`{"message":"slow down"}`),
	}
	err := decodeHTTPError(resp)
	var apiErr *Error
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "slow down", apiErr.Message)
	require.Equal(t, 9*time.Second, apiErr.RetryAfter)

	resp = &http.Response{
		StatusCode: http.StatusForbidden,
		Header: http.Header{
			"X-Ratelimit-Reset": []string{strconv.FormatInt(time.Now().UTC().Add(24*time.Hour).Unix(), 10)},
		},
		Body: ioNopCloser(`plain text`),
	}
	err = decodeHTTPError(resp)
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "plain text", apiErr.Message)
	require.Greater(t, apiErr.RetryAfter, time.Duration(0))
}

func testRSAPrivateKeyPEM(t *testing.T) string {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	block := &pem.Block{Type: "PRIVATE KEY", Bytes: mustPKCS8PrivateKey(t, privateKey)}
	return string(pem.EncodeToMemory(block))
}

func mustPKCS8PrivateKey(t *testing.T, privateKey *rsa.PrivateKey) []byte {
	t.Helper()
	encoded, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)
	return encoded
}

func ioNopCloser(body string) *readCloser {
	return &readCloser{Reader: strings.NewReader(body)}
}

type readCloser struct {
	*strings.Reader
}

func (r *readCloser) Close() error { return nil }
