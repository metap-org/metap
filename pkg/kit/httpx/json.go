package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// DefaultMaxBodyBytes bounds a JSON request body unless the caller asks for another limit. An
// unbounded body is how one bad client takes a service down.
const DefaultMaxBodyBytes int64 = 1 << 20

// ErrBodyTooLarge is returned by DecodeJSON when the request body exceeds the limit.
var ErrBodyTooLarge = errors.New("request body too large")

// WriteJSON writes v as a JSON response with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError writes the standard error envelope. code is a stable machine-readable identifier
// (snake_case), message is for a human.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, map[string]ErrorBody{"error": {Code: code, Message: message}})
}

// DecodeJSON decodes the request body into dst, reading at most maxBytes (DefaultMaxBodyBytes
// when maxBytes <= 0). It returns ErrBodyTooLarge for an oversized body and the decoder's error
// otherwise. Unknown fields are accepted; a caller that wants them rejected decodes itself.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBodyBytes
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return ErrBodyTooLarge
		}
		return err
	}
	return nil
}

// DecodeJSONOrFail is DecodeJSON that also writes the failure response (413 payload_too_large,
// or 400 invalid_request_body) and reports whether the caller may continue.
func DecodeJSONOrFail(w http.ResponseWriter, r *http.Request, dst any) bool {
	err := DecodeJSON(w, r, dst, 0)
	switch {
	case err == nil:
		return true
	case errors.Is(err, ErrBodyTooLarge):
		WriteError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "Request body is too large.")
	case errors.Is(err, io.EOF):
		WriteError(w, http.StatusBadRequest, "invalid_request_body", "Request body is empty.")
	default:
		WriteError(w, http.StatusBadRequest, "invalid_request_body", "Invalid request body.")
	}
	return false
}
