// People as the login surface sees them: a member of one tenant with an email, found, created and
// checked by password here; the member's preferences and the attributes ABAC conditions read.
package identity

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

type AuthUser struct {
	ID       uuid.UUID
	TenantID uuid.UUID
	Email    string
}

func authUser(m *Member) *AuthUser {
	return &AuthUser{ID: m.ID, TenantID: m.TenantID, Email: m.Email}
}

// CreateUser adds email to tenantID as a local-login member, creating the person's identity
// (with password) when they have none yet — see AddMember. db must reach the shared
// database (pool, or a transaction whose search_path includes metadata).
func CreateUser(ctx context.Context, db DBTX, tenantID uuid.UUID, email, password string) (*AuthUser, error) {
	m, _, err := AddMember(ctx, db, NewMember{TenantID: tenantID, Email: email, Password: password})
	if err != nil {
		return nil, err
	}
	return authUser(m), nil
}

// FindUserByID looks up one member by id, scoped to the tenant so an id from another tenant can
// never resolve. Returns (nil, nil) when not found.
func FindUserByID(ctx context.Context, db DBTX, tenantID, userID uuid.UUID) (*AuthUser, error) {
	m, err := GetMember(ctx, db, tenantID, userID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return authUser(m), nil
}

// ListTenantUsers returns every active member of the tenant, id+email only — the "pick a user"
// primitive an app-level assignee/reporter picker needs. Any authenticated user may call the route
// this backs — picking who to assign something to isn't an admin action.
func ListTenantUsers(ctx context.Context, db DBTX, tenantID uuid.UUID) ([]*AuthUser, error) {
	out := make([]*AuthUser, 0)
	for offset := 0; ; offset += MaxListLimit {
		page, total, err := ListMembers(ctx, db, tenantID, ListQuery{
			Status: StatusActive, Offset: offset, Limit: MaxListLimit,
		})
		if err != nil {
			return nil, err
		}
		for i := range page {
			out = append(out, authUser(&page[i]))
		}
		if offset+len(page) >= total || len(page) == 0 {
			return out, nil
		}
	}
}

// VerifyCredentials checks email + password and returns the member to sign in as: the member of
// tenantID when it is set, else the person's only (or last used) tenant. (nil, nil) for wrong
// credentials, no usable membership, or a person in several tenants with none chosen — the
// caller cannot tell these apart, by design.
func VerifyCredentials(ctx context.Context, db DBTX, email, password string, tenantID *uuid.UUID) (*AuthUser, error) {
	ident, err := Authenticate(ctx, db, email, password, time.Now())
	if err != nil {
		if errors.Is(err, ErrInvalidCredentials) || errors.Is(err, ErrLocked) || errors.Is(err, ErrDisabled) {
			return nil, nil
		}
		return nil, err
	}
	// A password alone is not enough for someone with a second factor (roadmap 115 M3): this path
	// (HTTP Basic, provider logins built on it) has no second step, so it refuses rather than
	// bypassing MFA. Such a person signs in through /auth/login, and a script uses an API key.
	if on, err := MFAEnabled(ctx, db, ident.ID); err != nil {
		return nil, err
	} else if on {
		return nil, nil
	}
	memberships, err := Memberships(ctx, db, ident.ID)
	if err != nil {
		return nil, err
	}
	m, ok := PickMembership(memberships, tenantID, ident.LastTenantID)
	if !ok {
		return nil, nil
	}
	return &AuthUser{ID: m.MemberID, TenantID: m.TenantID, Email: ident.Email}, nil
}
