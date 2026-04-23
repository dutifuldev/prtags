package jsend

import "testing"

func TestSuccess(t *testing.T) {
	env := Success(map[string]any{"ok": true})

	if env["status"] != "success" {
		t.Fatalf("expected success status, got %#v", env["status"])
	}
	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected map payload, got %#v", env["data"])
	}
	if data["ok"] != true {
		t.Fatalf("expected payload to round-trip, got %#v", data["ok"])
	}
}

func TestFail(t *testing.T) {
	env := Fail("bad request")

	if env["status"] != "fail" {
		t.Fatalf("expected fail status, got %#v", env["status"])
	}
	if env["data"] != "bad request" {
		t.Fatalf("expected fail payload, got %#v", env["data"])
	}
}

func TestErrorOmitsNilData(t *testing.T) {
	env := Error("boom", nil)

	if env["status"] != "error" {
		t.Fatalf("expected error status, got %#v", env["status"])
	}
	if env["message"] != "boom" {
		t.Fatalf("expected error message, got %#v", env["message"])
	}
	if _, ok := env["data"]; ok {
		t.Fatalf("expected nil data to be omitted, got %#v", env["data"])
	}
}

func TestErrorIncludesData(t *testing.T) {
	env := Error("boom", map[string]any{"field": "title"})

	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected map payload, got %#v", env["data"])
	}
	if data["field"] != "title" {
		t.Fatalf("expected error data to round-trip, got %#v", data["field"])
	}
}
