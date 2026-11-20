//go:build integration

package adminapi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/control"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/identity"
	"github.com/metap-org/metap/pkg/jwks"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
)

// testClient groups the services the way the tests address them (they were gRPC clients).
type testClient struct {
	Admin   *UserAdminService
	Roles   *RoleAdminService
	Account *AccountService
	Tenants *TenantAdminService
	Audit   *AuditAdminService
	Support *SupportAccessService
	Owner   *OwnerService
	SSO     *SsoAdminService
}

func newTestClient(cfg IdentityConfig) *testClient {
	return &testClient{
		Admin: NewUserAdminService(cfg), Roles: NewRoleAdminService(cfg), Account: NewAccountService(cfg),
		Tenants: NewTenantAdminService(cfg), Audit: NewAuditAdminService(cfg), Support: NewSupportAccessService(cfg),
		Owner: NewOwnerService(cfg), SSO: NewSsoAdminService(cfg),
	}
}

// testAuthConfig is what authenticating a test's bearer token needs.
type testAuthConfig struct {
	Verifier               *jwks.TokenVerifier
	Router                 *control.Router
	AuthContextEntity      string
	Metadata               *atomic.Pointer[metadata.MetadataRegistry]
	ContextAttributesCache *control.ContextAttributesCache
}

// activeAuth is the current harness's (tests in this package do not run in parallel).
var activeAuth testAuthConfig

func unauthenticated(msg string) error {
	return crud.NewServiceErrorMsg(http.StatusUnauthorized, errcode.Unauthorized, msg)
}

// authenticateToken resolves a bearer token the way the HTTP layer does: an API key, or a JWT
// (with its session, when it names one).
func authenticateToken(ctx context.Context, config testAuthConfig, token string) (*permission.RequestContext, error) {
	if identity.IsAPIKey(token) {
		rc, err := control.ResolveAPIKeyContext(ctx, config.Router, token, time.Now(),
			config.AuthContextEntity, config.Metadata, config.ContextAttributesCache)
		switch {
		case errors.Is(err, identity.ErrSessionRevoked):
			return nil, crud.NewServiceErrorMsg(401, errcode.SessionRevoked, "This API key was revoked.")
		case errors.Is(err, identity.ErrDisabled):
			return nil, crud.NewServiceErrorMsg(401, errcode.AccountDisabled, "This account is disabled.")
		case err != nil:
			return nil, unauthenticated("invalid or expired API key")
		}
		rc.ForwardedBearerToken = token
		return rc, nil
	}
	claims, err := jwks.DecodeWithVerifier(ctx, token, config.Verifier, nil)
	if err != nil {
		return nil, unauthenticated("invalid or expired token")
	}
	tenantID, err1 := uuid.Parse(claims.TenantID)
	userID, err2 := uuid.Parse(claims.Subject)
	if err1 != nil || err2 != nil {
		return nil, unauthenticated("token is missing required claims")
	}
	functionID := ""
	if claims.FunctionID != nil {
		functionID = *claims.FunctionID
	}
	rc, err := control.ResolveRequestContext(ctx, config.Router, tenantID, userID, functionID, claims.IssuedAt,
		config.AuthContextEntity, config.Metadata, config.ContextAttributesCache)
	switch {
	case errors.Is(err, identity.ErrSessionRevoked):
		return nil, crud.NewServiceErrorMsg(401, errcode.SessionRevoked, "Your session was revoked, sign in again.")
	case errors.Is(err, identity.ErrDisabled):
		return nil, crud.NewServiceErrorMsg(401, errcode.AccountDisabled, "This account is disabled.")
	case err != nil:
		return nil, unauthenticated("failed to resolve roles")
	}
	if err := control.ApplySessionClaim(ctx, config.Router, rc, claims.SessionID, time.Now()); err != nil {
		return nil, crud.NewServiceErrorMsg(401, errcode.SessionRevoked, "Your session was ended, sign in again.")
	}
	if claims.Scope != nil {
		clientID := ""
		hasClientID := claims.ClientID != nil
		if hasClientID {
			clientID = *claims.ClientID
		}
		rc.ApplyOAuthGrant(*claims.Scope, clientID, hasClientID)
	}
	rc.ForwardedBearerToken = token
	return rc, nil
}

// CallContext is ctx calling as the bearer of token, authenticated at each call.
func CallContext(ctx context.Context, token string) context.Context {
	auth := activeAuth
	return WithAuthenticator(ctx, func(ctx context.Context) (*permission.RequestContext, error) {
		return authenticateToken(ctx, auth, token)
	})
}

// scimAuth authenticates a SCIM request's bearer token.
func (h *identityHarness) scimAuth(r *http.Request) (*permission.RequestContext, error) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return authenticateToken(r.Context(), h.auth, token)
}

// statusOf is the HTTP status a failed call answers with (0 on success, 500 for a non-service error).
func statusOf(err error) int {
	if err == nil {
		return 0
	}
	var se *crud.ServiceError
	if errors.As(err, &se) {
		return int(se.Status)
	}
	return http.StatusInternalServerError
}

func genRSAKeyPair(t *testing.T) (privatePEM string, publicKey *rsa.PublicKey) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return string(privPEM), &key.PublicKey
}

func testRouterFor(pool *pgxpool.Pool) *control.Router {
	registry := control.NewPostgresTenantRegistry(pool)
	return control.NewRouter(pool, control.NewRegistryCache(registry), control.NewMemorySecretStore())
}

// asServiceError is the *crud.ServiceError err carries (nil when it is not one).
func asServiceError(err error) *crud.ServiceError {
	var se *crud.ServiceError
	if errors.As(err, &se) {
		return se
	}
	return nil
}
