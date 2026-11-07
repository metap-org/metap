package runtime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServiceErrorResponseUsesDefaultMessage(t *testing.T) {
	w := httptest.NewRecorder()
	ServiceErrorResponse(w, http.StatusNotFound, "entity_not_found", "", nil)
	require.Equal(t, http.StatusNotFound, w.Code, "got status %d, want 404", w.Code)
	var body map[string]map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, "entity_not_found", body["error"]["code"], "got code %v", body["error"]["code"])
	require.Equal(t, "Entity not found.", body["error"]["message"], "got message %v", body["error"]["message"])
}

func TestServiceErrorResponseUsesCustomMessage(t *testing.T) {
	w := httptest.NewRecorder()
	ServiceErrorResponse(w, http.StatusBadRequest, "validation_failed", "bad field", nil)
	var body map[string]map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	require.Equal(t, "bad field", body["error"]["message"], "got message %v", body["error"]["message"])
}

func TestServiceErrorResponseIncludesFieldErrors(t *testing.T) {
	w := httptest.NewRecorder()
	ServiceErrorResponse(w, http.StatusBadRequest, "validation_failed", "", map[string][]string{"name": {"required"}})
	var body map[string]map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	fieldErrors := body["error"]["fieldErrors"].(map[string]any)
	names := fieldErrors["name"].([]any)
	require.Equal(t, "required", names[0], "got %v", names)
}

func TestInternalErrorResponseHidesDetails(t *testing.T) {
	w := httptest.NewRecorder()
	InternalErrorResponse(w, errDBConnectionFailed)
	require.Equal(t, http.StatusInternalServerError, w.Code, "got status %d", w.Code)
	require.NotContains(t, w.Body.String(), "db connection", "internal error details must not leak to the client")
}

var errDBConnectionFailed = &testError{"db connection failed"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }
