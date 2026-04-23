package ghreplica

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGetRepository(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/v1/github/repos/openclaw/openclaw" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":1,"name":"openclaw","full_name":"openclaw/openclaw","html_url":"https://github.com/openclaw/openclaw","visibility":"public","private":false,"owner":{"login":"openclaw"}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL + "/")
	repo, err := client.GetRepository(context.Background(), "openclaw", "openclaw")
	if err != nil {
		t.Fatalf("GetRepository returned error: %v", err)
	}
	if repo.FullName != "openclaw/openclaw" {
		t.Fatalf("expected full name to decode, got %q", repo.FullName)
	}
	if repo.Owner.Login != "openclaw" {
		t.Fatalf("expected owner login to decode, got %q", repo.Owner.Login)
	}
}

func TestRequestJSONReturnsStatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer server.Close()

	client := NewClient(server.URL)
	err := client.requestJSON(context.Background(), http.MethodGet, "/broken", nil, &Repository{})
	if err == nil {
		t.Fatal("expected requestJSON to fail")
	}
	if !strings.Contains(err.Error(), "status 400") {
		t.Fatalf("expected status in error, got %v", err)
	}
}

func TestBatchGetObjects(t *testing.T) {
	updatedAt := time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC).Format(time.RFC3339)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/github-ext/repos/openclaw/openclaw/objects/batch" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("expected json content-type, got %q", got)
		}
		_, _ = w.Write([]byte(`{"results":[{"type":"issue","number":1,"found":true,"object":{"id":11,"number":1,"title":"Issue title","state":"open","html_url":"https://github.com/openclaw/openclaw/issues/1","updated_at":"` + updatedAt + `","user":{"login":"alice"}}},{"type":"pull_request","number":2,"found":true,"object":{"id":22,"number":2,"title":"PR title","state":"closed","html_url":"https://github.com/openclaw/openclaw/pull/2","updated_at":"` + updatedAt + `","user":{"login":"bob"}}},{"type":"issue","number":3,"found":false,"object":null}]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL)
	results, err := client.BatchGetObjects(context.Background(), "openclaw", "openclaw", []BatchObjectRef{
		{Type: "issue", Number: 1},
		{Type: "pull_request", Number: 2},
		{Type: "issue", Number: 3},
	})
	if err != nil {
		t.Fatalf("BatchGetObjects returned error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	if results[0].Summary == nil || results[0].Summary.AuthorLogin != "alice" {
		t.Fatalf("expected decoded issue summary, got %#v", results[0].Summary)
	}
	if results[1].Summary == nil || results[1].Summary.Title != "PR title" {
		t.Fatalf("expected decoded PR summary, got %#v", results[1].Summary)
	}
	if results[2].Found || results[2].Summary != nil {
		t.Fatalf("expected missing object to stay empty, got %#v", results[2])
	}
}

func TestBatchGetObjectsEmpty(t *testing.T) {
	client := NewClient("https://example.com")
	results, err := client.BatchGetObjects(context.Background(), "openclaw", "openclaw", nil)
	if err != nil {
		t.Fatalf("BatchGetObjects returned error: %v", err)
	}
	if results != nil {
		t.Fatalf("expected nil result for empty batch, got %#v", results)
	}
}

func TestDecodeBatchObjectSummaryRejectsUnsupportedType(t *testing.T) {
	_, err := decodeBatchObjectSummary("commit", []byte(`{"id":1}`))
	if err == nil {
		t.Fatal("expected unsupported type error")
	}
	if !strings.Contains(err.Error(), "unsupported batch object type") {
		t.Fatalf("unexpected error: %v", err)
	}
}
