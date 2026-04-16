package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClientDoJSONAddsAuthorizationHeader(t *testing.T) {
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

func TestExtractJSendData(t *testing.T) {
	raw, err := ExtractJSendData([]byte(`{"status":"success","data":{"version":"v1","fields":[]}}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"version":"v1","fields":[]}`, string(raw))
}
