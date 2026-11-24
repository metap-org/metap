// Role lookup and request-context resolution for every protected route. Basic auth (Authorization:
// Basic) and the metap-jwks trust-root dual-dispatch are deferred (not needed to exercise entity
// CRUD, the scope of this wave) — only Bearer and the HttpOnly session cookie are wired.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/metap-org/metap/internal/access"
	"github.com/metap-org/metap/internal/authctx"
	"github.com/metap-org/metap/pkg/auth/jwks"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/kit/httpx"
	"github.com/metap-org/metap/pkg/kit/httpx/router"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/kit/standard"
	"github.com/metap-org/metap/pkg/permission"
)

type authError struct {
	status  int
	code    string // "" = derived from status
	message string
}

func unauthorized(message string) *authError {
	return &authError{status: http.StatusUnauthorized, message: message}
}
func forbidden(message string) *authError {
	return &authError{status: http.StatusForbidden, message: message}
}

func (e *authError) respond(w http.ResponseWriter) {
	code := e.code
	if code == "" {
		code = errcode.Unauthorized
		if e.status == http.StatusForbidden {
			code = errcode.Forbidden
		}
	}
	w.Header().Set(standard.HeaderContentType, standard.MediaTypeJSON)
	w.WriteHeader(e.status)
	body, _ := json.Marshal(map[string]any{"error": map[string]string{"code": code, "message": e.message}})
	_, _ = w.Write(body)
}

// authenticate resolves the caller. An explicit Authorization header always
// wins when present; otherwise it falls back to the HttpOnly session cookie (the shape a browser
// request takes), CSRF-checked for any state-changing method.
func authenticate(ctx context.Context, state *AppState, r *http.Request) (*permission.RequestContext, *authError) {
	var token string
	header := r.Header.Get(standard.HeaderAuthorization)
	if header != "" {
		t, ok := httpx.BearerToken(header)
		if !ok {
			return nil, unauthorized("Missing or invalid authorization header.")
		}
		token = t
	} else {
		sessionCookie, err := r.Cookie(httpx.SessionCookieName)
		if err != nil || sessionCookie.Value == "" {
			return nil, unauthorized("Missing or invalid authorization header.")
		}
		if httpx.RequiresCSRFCheck(r.Method) {
			csrfCookie, cErr := r.Cookie(httpx.CSRFCookieName)
			csrfValue, csrfOK := "", false
			if cErr == nil {
				csrfValue, csrfOK = csrfCookie.Value, true
			}
			csrfHeader := r.Header.Get(httpx.CSRFHeaderName)
			if !httpx.CSRFMatches(csrfValue, csrfHeader, csrfOK, csrfHeader != "") {
				return nil, unauthorized("Missing or invalid CSRF token.")
			}
		}
		token = sessionCookie.Value
	}

	// An API key (roadmap 115 M5) authenticates as its member: no JWT, no OAuth grant.
	if identity.IsAPIKey(token) {
		rc, err := authctx.ResolveAPIKeyContext(ctx, state.Router, token, time.Now(),
			state.AuthContextEntity, state.Metadata, state.ContextAttributesCache, state.RolesCache)
		switch {
		case errors.Is(err, identity.ErrAPIKeyInvalid):
			return nil, unauthorized("Invalid or expired API key.")
		case errors.Is(err, identity.ErrSessionRevoked):
			return nil, &authError{status: http.StatusUnauthorized, code: errcode.SessionRevoked, message: "This API key was revoked."}
		case errors.Is(err, identity.ErrDisabled):
			return nil, &authError{status: http.StatusUnauthorized, code: errcode.AccountDisabled, message: "This account is disabled."}
		case err != nil:
			return nil, unauthorized("Failed to resolve roles.")
		}
		rc.ForwardedBearerToken = token
		return rc, nil
	}

	// 20s leeway (project owner decision) — no revocation list exists, so exp+leeway is the
	// only bound on how long a leaked token stays usable past its stated expiry.
	claims, err := state.VerifyToken(r.Context(), token, jwks.DefaultLeeway)
	if err != nil {
		return nil, unauthorized("Invalid or expired token.")
	}

	tenantID, err := uuid.Parse(claims.TenantID)
	if err != nil {
		return nil, unauthorized("Token is missing required claims.")
	}
	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return nil, unauthorized("Token is missing required claims.")
	}

	functionID := ""
	if claims.FunctionID != nil {
		functionID = *claims.FunctionID
	}
	rc, err := authctx.ResolveRequestContext(ctx, state.Router, tenantID, userID, functionID, claims.IssuedAt,
		state.AuthContextEntity, state.Metadata, state.ContextAttributesCache, state.RolesCache)
	switch {
	case errors.Is(err, identity.ErrSessionRevoked):
		return nil, &authError{status: http.StatusUnauthorized, code: errcode.SessionRevoked, message: "Your session was revoked, sign in again."}
	case errors.Is(err, identity.ErrDisabled):
		return nil, &authError{status: http.StatusUnauthorized, code: errcode.AccountDisabled, message: "This account is disabled."}
	case err != nil:
		return nil, unauthorized("Failed to resolve roles.")
	}
	if err := authctx.ApplySessionClaim(ctx, state.Router, rc, claims.SessionID, time.Now()); err != nil {
		if errors.Is(err, identity.ErrSessionRevoked) {
			return nil, &authError{status: http.StatusUnauthorized, code: errcode.SessionRevoked, message: "Your session was ended, sign in again."}
		}
		return nil, unauthorized("Failed to check the session.")
	}

	// A token minted by the OAuth2 token endpoint (a later wave) carries a granted scope/client
	// — ApplyOAuthGrant records it and PermissionService enforces it. An ordinary session token
	// has no scope claim, so this is a no-op for every login path wired in this wave.
	if claims.Scope != nil {
		clientID := ""
		hasClientID := claims.ClientID != nil
		if hasClientID {
			clientID = *claims.ClientID
		}
		rc.ApplyOAuthGrant(*claims.Scope, clientID, hasClientID)
	}
	// Calls this request makes to sibling services (recordclient) carry the caller's own
	// token, so they run as the caller — no service ever mints a token on someone's behalf.
	rc.ForwardedBearerToken = token

	return rc, nil
}

type callerKey struct{}

// RC is the authenticated caller of a request that passed through Authenticated, Admin or
// PlatformAdmin. Handlers behind those middleware read it instead of authenticating again.
func RC(r *http.Request) *permission.RequestContext {
	rc, _ := r.Context().Value(callerKey{}).(*permission.RequestContext)
	return rc
}

func withCaller(r *http.Request, rc *permission.RequestContext) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), callerKey{}, rc))
}

// Access labels of the route groups (router.Route.Labels); every registered route must sit in one.
const (
	LabelPublic        = "public"         // no credentials needed (health, login, discovery)
	LabelAuthenticated = "authenticated"  // any signed-in caller
	LabelAdmin         = "admin"          // tenant admin
	LabelPlatformAdmin = "platform-admin" // cross-tenant admin
	LabelSelfAuth      = "self-auth"      // authenticates inside (a bearer of another kind: SCIM, typed operations, OAuth2 client auth)
)

// Authenticated refuses a request with no valid session, token or API key and puts the caller in
// the request (RC).
func Authenticated(state *AppState) router.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rc, authErr := authenticate(r.Context(), state, r)
			if authErr != nil {
				authErr.respond(w)
				return
			}
			next.ServeHTTP(w, withCaller(r, rc))
		})
	}
}

// Admin is Authenticated plus the tenant admin role (and, for an OAuth2 token, the admin scope).
func Admin(state *AppState) router.Middleware { return atLevel(state, access.Admin) }

// PlatformAdmin is the cross-tenant admin gate: the token's tenant is tenancy.PlatformTenantID
// and the resolved roles include platform_admin.
func PlatformAdmin(state *AppState) router.Middleware { return atLevel(state, access.PlatformAdmin) }

// atLevel is Authenticated plus whatever level asks of the caller (internal/access decides).
func atLevel(state *AppState, level access.Level) router.Middleware {
	return func(next http.Handler) http.Handler {
		return Authenticated(state)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if why := access.Refusal(level, RC(r)); why != "" {
				forbidden(why).respond(w)
				return
			}
			next.ServeHTTP(w, r)
		}))
	}
}

// Access are the access groups of a router, each labelled so a test can list any route that
// sits in none.
type Access struct {
	Public, Authenticated, Admin, PlatformAdmin, SelfAuth *router.Router
}

// AccessGroups derives the labelled groups from r.
func AccessGroups(r *router.Router, state *AppState) Access {
	return Access{
		Public:        r.Labelled(LabelPublic),
		Authenticated: r.Labelled(LabelAuthenticated, Authenticated(state)),
		Admin:         r.Labelled(LabelAdmin, Admin(state)),
		PlatformAdmin: r.Labelled(LabelPlatformAdmin, PlatformAdmin(state)),
		SelfAuth:      r.Labelled(LabelSelfAuth),
	}
}

// requireAuth is for a handler that cannot sit behind the middleware: it returns the caller the
// middleware already resolved, else authenticates and answers the failure itself.
func requireAuth(w http.ResponseWriter, r *http.Request, state *AppState) (*permission.RequestContext, bool) {
	if rc := RC(r); rc != nil {
		return rc, true
	}
	rc, authErr := authenticate(r.Context(), state, r)
	if authErr != nil {
		authErr.respond(w)
		return nil, false
	}
	return rc, true
}

func requireAdmin(w http.ResponseWriter, r *http.Request, state *AppState) (*permission.RequestContext, bool) {
	return requireAt(w, r, state, access.Admin)
}

func requirePlatformAdmin(w http.ResponseWriter, r *http.Request, state *AppState) (*permission.RequestContext, bool) {
	return requireAt(w, r, state, access.PlatformAdmin)
}

func requireAt(w http.ResponseWriter, r *http.Request, state *AppState, level access.Level) (*permission.RequestContext, bool) {
	rc, ok := requireAuth(w, r, state)
	if !ok {
		return nil, false
	}
	if why := access.Refusal(level, rc); why != "" {
		forbidden(why).respond(w)
		return nil, false
	}
	return rc, true
}
