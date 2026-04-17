package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dutifuldev/prtags/internal/jsend"
	"github.com/stretchr/testify/require"
)

func TestAnnotationPRClearSendsNullPayload(t *testing.T) {
	t.Helper()
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/repos/acme/widgets/pulls/42/annotations":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			_ = json.NewEncoder(w).Encode(jsend.Success(map[string]any{
				"intent": nil,
			}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	_, stderr, err := runCLI(t, server.URL, "annotation", "pr", "clear", "-R", "acme/widgets", "42", "intent")
	require.NoError(t, err, stderr)
	require.Equal(t, map[string]any{"intent": nil}, payload)
}

func TestAnnotationIssueClearSendsNullPayloadForMultipleFields(t *testing.T) {
	t.Helper()
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/repos/acme/widgets/issues/7/annotations":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			_ = json.NewEncoder(w).Encode(jsend.Success(map[string]any{
				"intent":  nil,
				"quality": nil,
			}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	_, stderr, err := runCLI(t, server.URL, "annotation", "issue", "clear", "-R", "acme/widgets", "7", "intent", "quality")
	require.NoError(t, err, stderr)
	require.Equal(t, map[string]any{
		"intent":  nil,
		"quality": nil,
	}, payload)
}

func TestAnnotationGroupClearSendsNullPayload(t *testing.T) {
	t.Helper()
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/groups/steady-otter-k4m2/annotations":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			_ = json.NewEncoder(w).Encode(jsend.Success(map[string]any{
				"summary": nil,
			}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	_, stderr, err := runCLI(t, server.URL, "annotation", "group", "clear", "steady-otter-k4m2", "summary")
	require.NoError(t, err, stderr)
	require.Equal(t, map[string]any{"summary": nil}, payload)
}

func TestParseAnnotationKeysRejectsBlankValues(t *testing.T) {
	t.Helper()
	_, err := parseAnnotationKeys([]string{"intent", " "})
	require.ErrorContains(t, err, "annotation key is required")
}
