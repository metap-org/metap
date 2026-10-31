package runtime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServiceErrorResponseUsesDefaultMessage(t *testing.T) {
	w := httptest.NewRecorder()
	ServiceErrorResponse(w, http.StatusNotFound, "entity_not_found", "", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("got status %d, want 404", w.Code)
	}
	var body map[string]map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"]["code"] != "entity_not_found" {
		t.Fatalf("got code %v", body["error"]["code"])
	}
	if body["error"]["message"] != "Entity not found." {
		t.Fatalf("got message %v", body["error"]["message"])
	}
}

func TestServiceErrorResponseUsesCustomMessage(t *testing.T) {
	w := httptest.NewRecorder()
	ServiceErrorResponse(w, http.StatusBadRequest, "validation_failed", "bad field", nil)
	var body map[string]map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["error"]["message"] != "bad field" {
		t.Fatalf("got message %v", body["error"]["message"])
	}
}

func TestServiceErrorResponseIncludesFieldErrors(t *testing.T) {
	w := httptest.NewRecorder()
	ServiceErrorResponse(w, http.StatusBadRequest, "validation_failed", "", map[string][]string{"name": {"required"}})
	var body map[string]map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	fieldErrors := body["error"]["fieldErrors"].(map[string]any)
	names := fieldErrors["name"].([]any)
	if names[0] != "required" {
		t.Fatalf("got %v", names)
	}
}

func TestInternalErrorResponseHidesDetails(t *testing.T) {
	w := httptest.NewRecorder()
	InternalErrorResponse(w, errDBConnectionFailed)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("got status %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "db connection") {
		t.Fatal("internal error details must not leak to the client")
	}
}

var errDBConnectionFailed = &testError{"db connection failed"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }
