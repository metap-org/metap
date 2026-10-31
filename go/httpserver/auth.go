// Role lookup and request-context resolution for every protected route — mirrors metap-http's
// Rust auth.rs. Basic auth (Authorization: Basic) and the metap-jwks trust-root dual-dispatch
// are deferred (not needed to exercise entity CRUD, the scope of this wave) — only Bearer and
// the HttpOnly session cookie are wired.
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap-go/control"
	"github.com/metap-org/metap-go/permission"
	"github.com/metap-org/metap-go/runtime"
)

type authError struct {
	status  int
	message string
}

func unauthorized(message string) *authError {
	return &authError{status: http.StatusUnauthorized, message: message}
}
func forbidden(message string) *authError {
	return &authError{status: http.StatusForbidden, message: message}
}

func (e *authError) respond(w http.ResponseWriter) {
	code := "unauthorized"
	if e.status == http.StatusForbidden {
		code = "forbidden"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.status)
	body, _ := json.Marshal(map[string]any{"error": map[string]string{"code": code, "message": e.message}})
	_, _ = w.Write(body)
}

// authenticate mirrors AuthContext::from_request_parts. An explicit Authorization header always
// wins when present; otherwise it falls back to the HttpOnly session cookie (the shape a browser
// request takes), CSRF-checked for any state-changing method.
func authenticate(ctx context.Context, state *AppState, r *http.Request) (*permission.RequestContext, *authError) {
	var token string
	header := r.Header.Get("Authorization")
	if header != "" {
		t, ok := runtime.ParseBearer(header)
		if !ok {
			return nil, unauthorized("Missing or invalid authorization header.")
		}
		token = t
	} else {
		sessionCookie, err := r.Cookie(runtime.SessionCookieName)
		if err != nil || sessionCookie.Value == "" {
			return nil, unauthorized("Missing or invalid authorization header.")
		}
		if runtime.RequiresCSRFCheck(r.Method) {
			csrfCookie, cErr := r.Cookie(runtime.CSRFCookieName)
			csrfValue, csrfOK := "", false
			if cErr == nil {
				csrfValue, csrfOK = csrfCookie.Value, true
			}
			csrfHeader := r.Header.Get(runtime.CSRFHeaderName)
			if !runtime.CSRFMatches(csrfValue, csrfHeader, csrfOK, csrfHeader != "") {
				return nil, unauthorized("Missing or invalid CSRF token.")
			}
		}
		token = sessionCookie.Value
	}

	// 20s leeway (project owner decision) — no revocation list exists, so exp+leeway is the
	// only bound on how long a leaked token stays usable past its stated expiry.
	claims, err := state.VerifyToken(token, 20*time.Second)
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
	rc, err := control.ResolveRequestContext(ctx, state.Router, tenantID, userID, functionID,
		state.AuthContextEntity, state.Metadata, state.ContextAttributesCache)
	if err != nil {
		return nil, unauthorized("Failed to resolve roles.")
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

	return rc, nil
}

// requireAuth authenticates r and writes the error response itself on failure — the common case
// every REST entity CRUD handler uses.
func requireAuth(w http.ResponseWriter, r *http.Request, state *AppState) (*permission.RequestContext, bool) {
	rc, authErr := authenticate(r.Context(), state, r)
	if authErr != nil {
		authErr.respond(w)
		return nil, false
	}
	return rc, true
}

// requireAdmin is the extractor every future /admin/* route uses instead of requireAuth, so the
// gate can't be forgotten on a future handler the way a per-handler check could be.
func requireAdmin(w http.ResponseWriter, r *http.Request, state *AppState) (*permission.RequestContext, bool) {
	rc, ok := requireAuth(w, r, state)
	if !ok {
		return nil, false
	}
	if !rc.IsAdmin() {
		forbidden("This action requires the admin role.").respond(w)
		return nil, false
	}
	if !rc.ScopePermitsAdminSurface() {
		forbidden("This action requires the `admin` OAuth2 scope.").respond(w)
		return nil, false
	}
	return rc, true
}

// requirePlatformAdmin is the cross-tenant admin gate — a request passes when its token's
// tenantId is control.PlatformTenantID and the resolved roles include "platform_admin".
func requirePlatformAdmin(w http.ResponseWriter, r *http.Request, state *AppState) (*permission.RequestContext, bool) {
	rc, ok := requireAuth(w, r, state)
	if !ok {
		return nil, false
	}
	isPlatformTenant := rc.TenantID == control.PlatformTenantID.String()
	hasPlatformAdminRole := false
	for _, role := range rc.Roles {
		if role == "platform_admin" {
			hasPlatformAdminRole = true
			break
		}
	}
	if !isPlatformTenant || !hasPlatformAdminRole {
		forbidden("This action requires the platform_admin role.").respond(w)
		return nil, false
	}
	if !rc.ScopePermitsAdminSurface() {
		forbidden("This action requires the `admin` OAuth2 scope.").respond(w)
		return nil, false
	}
	return rc, true
}
