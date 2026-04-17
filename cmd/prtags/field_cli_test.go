package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dutifuldev/prtags/internal/jsend"
	"github.com/stretchr/testify/require"
)

func TestFieldEnsureCreatesMissingField(t *testing.T) {
	t.Helper()
	var createPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/repos/acme/widgets/fields":
			_ = json.NewEncoder(w).Encode(jsend.Success([]any{}))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/repos/acme/widgets/fields":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&createPayload))
			_ = json.NewEncoder(w).Encode(jsend.Success(map[string]any{
				"id":               9,
				"name":             "intent",
				"display_name":     "intent",
				"object_scope":     "pull_request",
				"field_type":       "text",
				"enum_values_json": []string{},
				"is_searchable":    true,
				"is_vectorized":    true,
			}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stdout, stderr, err := runCLI(t, server.URL, "field", "ensure", "-R", "acme/widgets", "--name", "intent", "--scope", "pull_request", "--type", "text", "--searchable", "--vectorized")
	require.NoError(t, err, stderr)
	require.Equal(t, "intent", createPayload["name"])
	require.Equal(t, "pull_request", createPayload["object_scope"])
	require.Equal(t, "text", createPayload["field_type"])
	require.Equal(t, true, createPayload["is_searchable"])
	require.Equal(t, true, createPayload["is_vectorized"])

	var envelope struct {
		Status string `json:"status"`
		Data   struct {
			Name   string `json:"name"`
			Action string `json:"action"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &envelope))
	require.Equal(t, "success", envelope.Status)
	require.Equal(t, "intent", envelope.Data.Name)
	require.Equal(t, "created", envelope.Data.Action)
}

func TestFieldEnsureNoopWhenFieldAlreadyMatches(t *testing.T) {
	t.Helper()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/repos/acme/widgets/fields":
			_ = json.NewEncoder(w).Encode(jsend.Success([]map[string]any{
				{
					"id":               9,
					"name":             "intent",
					"display_name":     "intent",
					"object_scope":     "pull_request",
					"field_type":       "text",
					"enum_values_json": []string{},
					"is_searchable":    true,
					"is_vectorized":    true,
				},
			}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stdout, stderr, err := runCLI(t, server.URL, "field", "ensure", "-R", "acme/widgets", "--name", "intent", "--scope", "pull_request", "--type", "text", "--searchable", "--vectorized")
	require.NoError(t, err, stderr)
	require.Equal(t, 1, requests)
	require.Contains(t, stdout, `"action": "noop"`)
}

func TestFieldEnsureUpdatesExistingFieldWhenShapeDiffers(t *testing.T) {
	t.Helper()
	var patchPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/repos/acme/widgets/fields":
			_ = json.NewEncoder(w).Encode(jsend.Success([]map[string]any{
				{
					"id":               9,
					"name":             "intent",
					"display_name":     "intent",
					"object_scope":     "pull_request",
					"field_type":       "text",
					"enum_values_json": []string{},
					"is_searchable":    false,
					"is_vectorized":    false,
				},
			}))
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/repos/acme/widgets/fields/9":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&patchPayload))
			_ = json.NewEncoder(w).Encode(jsend.Success(map[string]any{
				"id":               9,
				"name":             "intent",
				"display_name":     "intent",
				"object_scope":     "pull_request",
				"field_type":       "text",
				"enum_values_json": []string{},
				"is_searchable":    true,
				"is_vectorized":    true,
			}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stdout, stderr, err := runCLI(t, server.URL, "field", "ensure", "-R", "acme/widgets", "--name", "intent", "--scope", "pull_request", "--type", "text", "--searchable", "--vectorized")
	require.NoError(t, err, stderr)
	require.Equal(t, true, patchPayload["is_searchable"])
	require.Equal(t, true, patchPayload["is_vectorized"])
	require.Contains(t, stdout, `"action": "updated"`)
}

func TestFieldListSupportsFilteringAndTableOutput(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/repos/acme/widgets/fields":
			_ = json.NewEncoder(w).Encode(jsend.Success([]map[string]any{
				{
					"id":            9,
					"name":          "intent",
					"display_name":  "Intent",
					"object_scope":  "pull_request",
					"field_type":    "text",
					"is_searchable": true,
					"is_vectorized": true,
					"is_filterable": false,
					"is_required":   false,
				},
				{
					"id":            10,
					"name":          "priority",
					"display_name":  "Priority",
					"object_scope":  "issue",
					"field_type":    "enum",
					"is_filterable": true,
					"is_searchable": false,
					"is_vectorized": false,
					"is_required":   false,
				},
			}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stdout, stderr, err := runCLI(t, server.URL, "field", "list", "-R", "acme/widgets", "--scope", "pull_request", "--format", "table")
	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "ID")
	require.Contains(t, stdout, "NAME")
	require.Contains(t, stdout, "intent")
	require.Contains(t, stdout, "searchable,vectorized")
	require.NotContains(t, stdout, "priority")
}

func runCLI(t *testing.T, serverURL string, args ...string) (string, string, error) {
	t.Helper()
	root := newRootCommand()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"--server", serverURL}, args...))
	err := root.Execute()
	return stdout.String(), stderr.String(), err
}
