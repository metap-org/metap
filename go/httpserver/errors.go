package httpserver

import (
	"errors"
	"net/http"

	"github.com/metap-org/metap-go/control"
	"github.com/metap-org/metap-go/runtime"
)

// writeServiceError and writeInternalError are thin call-site conveniences over
// runtime.ServiceErrorResponse/InternalErrorResponse — kept as local names so route handlers
// read the same as the Rust originals' service_error_response/internal_error_response calls.
func writeServiceError(w http.ResponseWriter, status int, errorCode, message string, fieldErrors map[string][]string) {
	runtime.ServiceErrorResponse(w, status, errorCode, message, fieldErrors)
}

func writeInternalError(w http.ResponseWriter, err error) {
	runtime.InternalErrorResponse(w, err)
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

// writeCrudResult writes a crud.ServiceResult[T]'s error half as the standard envelope — the
// REST handlers' one shared tail after a non-2xx crud.ServiceResult comes back.
func writeCrudServiceError(w http.ResponseWriter, status uint16, errorCode string, message *string, fieldErrors map[string][]string) {
	msg := ""
	if message != nil {
		msg = *message
	}
	writeServiceError(w, int(status), errorCode, msg, fieldErrors)
}
