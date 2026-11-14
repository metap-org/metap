package httpserver

import (
	"errors"
	"net/http"

	"github.com/metap-org/metap/pkg/control"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/kit/httpx"
)

// writeServiceError and writeInternalError are thin call-site conveniences over
// httpx.ServiceErrorResponse/InternalErrorResponse.
func writeServiceError(w http.ResponseWriter, status int, errorCode, message string, fieldErrors map[string][]string) {
	httpx.ServiceErrorResponse(w, status, errorCode, message, fieldErrors)
}

func writeInternalError(w http.ResponseWriter, err error) {
	httpx.InternalErrorResponse(w, err)
}

// writeRouterUnavailable maps a control.Router.Begin failure to an HTTP response — same mapping
// crud's own routerUnavailable helper uses internally for CrudService's own calls; this is for
// a route handler that calls Router.Begin directly (login, admin routes, a later wave).
func writeRouterUnavailable(w http.ResponseWriter, err error) {
	var routerErr *control.RouterError
	if !errors.As(err, &routerErr) {
		writeInternalError(w, err)
		return
	}
	switch routerErr.Kind {
	case control.ErrTenantSuspended, control.ErrTenantExpired:
		writeServiceError(w, http.StatusForbidden, "tenant_unavailable", "", nil)
	case control.ErrTenantMigrating, control.ErrTenantProvisioning:
		writeServiceError(w, http.StatusServiceUnavailable, "tenant_unavailable", "", nil)
	case control.ErrTenantDeleted:
		writeServiceError(w, http.StatusNotFound, "tenant_not_found", "", nil)
	default:
		writeInternalError(w, err)
	}
}

// writeCrudError answers a CrudService/RecordBackend failure: a *crud.ServiceError keeps its
// status, code, message and field errors in the standard envelope; anything else is logged and
// answered as an internal error (RouterError first — an expected tenant-lifecycle state).
func writeCrudError(w http.ResponseWriter, err error) {
	if se, ok := crud.AsServiceError(err); ok {
		writeServiceError(w, int(se.Status), se.Code, se.Message, se.FieldErrors)
		return
	}
	writeInternalError(w, err)
}

// decodeJSONBody: see DecodeJSONBody. The body is bounded by httpx.DefaultMaxBodyBytes.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	err := httpx.DecodeJSON(w, r, dst, 0)
	switch {
	case err == nil:
		return true
	case errors.Is(err, httpx.ErrBodyTooLarge):
		writeServiceError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "Request body is too large.", nil)
	default:
		writeServiceError(w, http.StatusBadRequest, "validation_failed", "Invalid request body.", nil)
	}
	return false
}
