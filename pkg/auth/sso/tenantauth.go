// Package tenantauth is tenant-selectable auth providers — distinct from the low-code platform's
// own admin auth, this is how a tenant's own users authenticate. A tenant can enable more than one
// provider at once, so AuthProviderKind is a discriminant tenant config picks from, not an
// interface every provider must uniformly implement — Local/Basic/OIDC/OAuth2 take genuinely
// different inputs.
// Bearer (JWT) itself is not a provider here — it's the session mechanism every successful login
// (regardless of provider) ends in (jwks.MintRSAJWT), verified per-request by the HTTP auth
// layer, unchanged by this package.
// No HTTP, no business-entity knowledge — a plain library, same shape as permission.
package sso

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/metap-org/metap/pkg/auth/identity"
)

// DBTX is satisfied by *pgxpool.Pool and pgx.Tx.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type AuthProviderKind int

const (
	ProviderLocal AuthProviderKind = iota
	ProviderBasic
	ProviderOidc
	// ProviderOAuth2: plain OAuth2 authorization-code login — distinct from ProviderOidc (see
	// oauth2_login.go's doc comment): no discovery, no id_token, identity resolved by calling a
	// configured userinfo endpoint with the obtained access token instead of decoding a JWT.
	ProviderOAuth2
)

func (k AuthProviderKind) String() string {
	switch k {
	case ProviderLocal:
		return "local"
	case ProviderBasic:
		return "basic"
	case ProviderOidc:
		return "oidc"
	default:
		return "oauth2"
	}
}

func ParseAuthProviderKind(s string) (AuthProviderKind, bool) {
	switch s {
	case "local":
		return ProviderLocal, true
	case "basic":
		return ProviderBasic, true
	case "oidc":
		return ProviderOidc, true
	case "oauth2":
		return ProviderOAuth2, true
	default:
		return 0, false
	}
}

// LocalPasswordProvider: the only provider that's a thin wrapper over
// identity.VerifyCredentials. Also backs "basic" (HTTP Basic auth verifies the exact same
// password credential, only the HTTP transport that carries it differs) — one verifier, two ways
// a client presents it.
type LocalPasswordProvider struct{}

func (LocalPasswordProvider) Kind() AuthProviderKind { return ProviderLocal }

// Verify returns the member to sign in as — in tenantID when set, else the person's only or last
// used tenant — or (nil, nil); see identity.VerifyCredentials.
func (LocalPasswordProvider) Verify(
	ctx context.Context,
	db DBTX,
	email, password string,
	tenantID *uuid.UUID,
) (*identity.AuthUser, error) {
	return identity.VerifyCredentials(ctx, db, email, password, tenantID)
}

// EnabledProviders: which providers a tenant currently has enabled. An empty result means
// "local only", not "nothing works" — callers must treat it that way to match the Router's
// unregistered-tenant fallback for a tenant that predates the tenant_auth_configs migration.
func EnabledProviders(ctx context.Context, db DBTX, tenantID uuid.UUID) ([]AuthProviderKind, error) {
	rows, err := db.Query(
		ctx,
		"SELECT provider_kind FROM tenant_auth_configs WHERE tenant_id = $1 AND enabled = true",
		tenantID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var kinds []AuthProviderKind
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if k, ok := ParseAuthProviderKind(raw); ok {
			kinds = append(kinds, k)
		}
	}
	return kinds, rows.Err()
}

func authUserOf(m *identity.Member) *identity.AuthUser {
	if m == nil {
		return nil
	}
	return &identity.AuthUser{ID: m.ID, TenantID: m.TenantID, Email: m.Email}
}

// FindExternalUser: a user JIT-provisioned (or previously linked) by a prior login through an
// external-identity provider ("oidc" or "oauth2") for this tenant — looked up by
// externalSubject (the IdP's stable subject identifier), never by email, since email can change
// at the IdP but the subject does not. Returns (nil, nil) when not found.
func FindExternalUser(
	ctx context.Context,
	db DBTX,
	tenantID uuid.UUID,
	provider, externalSubject string,
) (*identity.AuthUser, error) {
	m, err := identity.FindExternalMember(ctx, db, tenantID, provider, externalSubject)
	if err != nil {
		return nil, err
	}
	return authUserOf(m), nil
}

func FindOidcUser(
	ctx context.Context,
	db DBTX,
	tenantID uuid.UUID,
	externalSubject string,
) (*identity.AuthUser, error) {
	return FindExternalUser(ctx, db, tenantID, "oidc", externalSubject)
}

// JitProvisionExternalUser: first-ever login through an external-identity provider for this
// (tenantID, provider, externalSubject) — auto-creates the member (JIT provisioning, no admin
// pre-creation required), joining the identity that already has this email if there is one. No
// password is set, and no role is assigned: a JIT-provisioned user starts with zero roles, same
// deny-by-default posture PermissionService already applies to any roleless caller.
func JitProvisionExternalUser(
	ctx context.Context,
	db DBTX,
	tenantID uuid.UUID,
	provider, email, externalSubject string,
) (*identity.AuthUser, error) {
	m, _, err := identity.AddMember(ctx, db, identity.NewMember{
		TenantID: tenantID, Email: email, AuthProvider: provider, ExternalSubject: externalSubject,
	})
	if err != nil {
		return nil, err
	}
	return authUserOf(m), nil
}

func JitProvisionOidcUser(
	ctx context.Context,
	db DBTX,
	tenantID uuid.UUID,
	email, externalSubject string,
) (*identity.AuthUser, error) {
	return JitProvisionExternalUser(ctx, db, tenantID, "oidc", email, externalSubject)
}
