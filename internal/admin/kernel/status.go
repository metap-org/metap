package kernel

import "net/http"

// The HTTP statuses a slice's rules answer with. A slice's rules.go may not import net/http (depguard
// slice-rules-pure: a decision is not a transport), so they name the status through here; the values are
// the standard ones and, like http.Status*, untyped.
const (
	StatusBadRequest          = http.StatusBadRequest
	StatusForbidden           = http.StatusForbidden
	StatusNotFound            = http.StatusNotFound
	StatusConflict            = http.StatusConflict
	StatusUnprocessableEntity = http.StatusUnprocessableEntity
)
