// Typed REST operations (roadmap 116): every operation that is not generic entity CRUD is a huma
// handler whose Go input/output types are its contract. The OpenAPI document is generated from
// those types and served at /metadata/api.json; the entity CRUD document stays /metadata/openapi.json.
//
// One envelope: huma's own errors (bad input, malformed body) and the handlers' errors all come
// out as {"error":{code,message,fieldErrors,details}}, the shape every other route writes. One
// authentication path: the operation's auth middleware runs authenticate(), so bearer tokens, API
// keys and the cookie session with its CSRF check behave exactly as on every other route.
package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/metap-org/metap/pkg/kit/httpx/router"

	"github.com/metap-org/metap/pkg/adminapi"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/kit/httpx"
	"github.com/metap-org/metap/pkg/kit/standard"
	"github.com/metap-org/metap/pkg/permission"
)

// APIDocPath is where the typed operations' OpenAPI document is served (".json" is appended).
const APIDocPath = "/metadata/api"

// apiError is a failure in the standard envelope, as huma writes it.
type apiError struct{ body httpx.ErrorBody }

func (e *apiError) Error() string  { return e.body.Message }
func (e *apiError) GetStatus() int { return e.body.Status }

// MarshalJSON writes the envelope huma serializes as the response body.
func (e *apiError) MarshalJSON() ([]byte, error) { return e.body.MarshalEnvelope(), nil }

// ContentType keeps the body plain JSON (huma would otherwise announce application/problem+json).
func (e *apiError) ContentType(string) string { return standard.MediaTypeJSON }

func newAPIError(status int, code, message string, fieldErrors map[string][]string) *apiError {
	return &apiError{body: httpx.BuildServiceError(status, code, message, fieldErrors)}
}

// apiCodeForStatus names the error a bare status stands for, when huma reports one by status only.
func apiCodeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return errcode.ValidationFailed
	case http.StatusUnauthorized:
		return errcode.Unauthorized
	case http.StatusForbidden:
		return errcode.Forbidden
	case http.StatusNotFound:
		return errcode.NotFound
	case http.StatusRequestEntityTooLarge:
		return "payload_too_large"
	case http.StatusInternalServerError:
		return errcode.InternalError
	default:
		return "request_failed"
	}
}

func init() {
	// huma's errors (input that fails its schema, a malformed body) in the standard envelope: each
	// failed location becomes a fieldErrors entry ("body.name" -> "name", "path.id" -> "id").
	huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
		var fieldErrors map[string][]string
		for _, err := range errs {
			var detail *huma.ErrorDetail
			if !errors.As(err, &detail) {
				continue
			}
			if fieldErrors == nil {
				fieldErrors = map[string][]string{}
			}
			field := detail.Location
			for _, prefix := range []string{"body.", "query.", "path.", "header."} {
				field = strings.TrimPrefix(field, prefix)
			}
			fieldErrors[field] = append(fieldErrors[field], detail.Message)
		}
		if status == http.StatusUnprocessableEntity {
			// Schema validation is a bad request here, as everywhere else in metap.
			status = http.StatusBadRequest
		}
		if status >= 500 {
			msg = "Internal server error."
		}
		return newAPIError(status, apiCodeForStatus(status), msg, fieldErrors)
	}
}

// apiErr maps what a handler's service call returned onto the response: a crud.ServiceError keeps
// its status, code, field errors and details; anything unknown is logged and becomes a bare 500.
func apiErr(err error) error {
	if err == nil {
		return nil
	}
	var ae *apiError
	if errors.As(err, &ae) {
		return ae
	}
	if se, ok := crud.AsServiceError(err); ok {
		body := httpx.BuildServiceError(int(se.Status), se.Code, se.Message, se.FieldErrors)
		body.Details = se.Details
		return &apiError{body: body}
	}
	slog.Error("internal error", standard.LogError, err)
	return newAPIError(http.StatusInternalServerError, errcode.InternalError, "Internal server error.", nil)
}

type requestContextKey struct{}

// callerOf is the authenticated caller an operation registered with authed() runs as.
func callerOf(ctx context.Context) *permission.RequestContext {
	rc, _ := ctx.Value(requestContextKey{}).(*permission.RequestContext)
	return rc
}

// authed is the middleware of every operation that needs a caller: authenticate() exactly as the
// hand-written routes do, refusing in the standard envelope, the caller then in the context.
func authed(state *AppState) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		r, w := humago.Unwrap(ctx)
		rc, authErr := authenticate(r.Context(), state, r)
		if authErr != nil {
			authErr.respond(w)
			return
		}
		next(huma.WithValue(ctx, requestContextKey{}, rc))
	}
}

// newAPI is the typed-operations API on r: OpenAPI at APIDocPath(.json), no docs UI, no
// "$schema" link injected into response bodies.
func newAPI(r *router.Router) huma.API {
	config := huma.DefaultConfig("metap", "1")
	config.OpenAPIPath = APIDocPath
	config.DocsPath = ""
	config.SchemasPath = ""
	config.CreateHooks = nil
	return humago.New(r, config)
}

// mountTypedAPI registers every typed operation.
func mountTypedAPI(r *router.Router, state *AppState) {
	// Each typed operation authenticates through authed() itself, and SCIM through its own
	// bearer; both sit in the self-auth group.
	self := r.Labelled(LabelSelfAuth)
	api := newAPI(self)
	registerSessionOperations(api, state)
	registerPolicyOperations(api, state)
	registerAdminOperations(api, state)
	if state.Identity != nil {
		self.Mount("/scim/v2", adminapi.SCIMHandler(adminapi.NewUserAdminService(*state.Identity), scimAuthenticate(state)))
	}
}
