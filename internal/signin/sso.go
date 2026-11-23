package signin

import (
	"context"
	"errors"
	"github.com/metap-org/metap/pkg/permission/rolestore"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/identity"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/tenantauth"
)

// SSOPolicy is what a tenant's provider settings decide once somebody has been verified, whichever
// protocol verified them.
type SSOPolicy struct {
	Provider    string // the external-subject namespace: "oidc" or "saml"
	DefaultRole string
	Mapping     tenantauth.RoleMapping
	// Allowed says whether an email address may sign in to the tenant (its domain restriction).
	Allowed func(address string) bool
}

// errSSOEmailUnverified: the provider did not vouch for the address, so it may not be used to find,
// link or create a member.
var errSSOEmailUnverified = errors.New("the identity provider has not verified the email address")

// errSSODomainUnverified: the workspace has not proved it owns the address's domain, so the
// provider's say-so is not enough to find, link or create a member with that address.
var errSSODomainUnverified = errors.New("the workspace has not verified the email domain")

// FinishSSO turns a verified identity into a session: domain restriction, member lookup or
// creation, role sync, then the session. No second factor follows: the identity provider is it.
func (s *Service) FinishSSO(ctx context.Context, tenant uuid.UUID, p SSOPolicy, who *tenantauth.VerifiedIdentity, c Client) (*Grant, error) {
	if !p.Allowed(who.Email) {
		return nil, serviceErr(http.StatusForbidden, errcode.SSOFailed, "This email address is not allowed to sign in to this workspace.", nil)
	}
	member, err := s.ssoMember(ctx, tenant, p, who)
	switch {
	case errors.Is(err, errSSOEmailUnverified):
		return nil, serviceErr(http.StatusForbidden, errcode.SSOFailed,
			"The identity provider has not verified this email address, so it cannot be used to sign in.", nil)
	case errors.Is(err, errSSODomainUnverified):
		return nil, serviceErr(http.StatusForbidden, errcode.SSOFailed,
			"This workspace has not verified that it owns the domain of your email address.", nil)
	case err != nil:
		return nil, IdentityError(err)
	}
	if member.Status != identity.StatusActive {
		return nil, serviceErr(http.StatusForbidden, errcode.AccountDisabled, "This account is disabled.", nil)
	}
	if err := s.syncSSORoles(ctx, tenant, member, p.Mapping, who.Roles); err != nil {
		slog.Error("sso: syncing roles from the identity provider", "tenant_id", tenant, "error", err)
		return nil, serviceErr(http.StatusInternalServerError, errcode.SSOFailed, "Your roles could not be applied. Try again.", nil)
	}
	return s.Issue(ctx, member.IdentityID, identity.Membership{MemberID: member.ID, TenantID: tenant}, c)
}

// syncSSORoles brings the roles the provider's claim decides (the mapping's values) in line with
// the claim, at every sign-in. Roles outside the map are left alone, and the owner keeps theirs:
// the provider cannot lock the person who is the way back in out of their own workspace.
func (s *Service) syncSSORoles(ctx context.Context, tenant uuid.UUID, member *identity.Member, mapping tenantauth.RoleMapping, claimValues []string) error {
	managed := mapping.ManagedRoles()
	if mapping.Claim == "" || len(managed) == 0 {
		return nil
	}
	want := mapping.MappedRoles(claimValues)
	have, err := rolestore.GetRolesForUser(ctx, s.d.Pool, tenant, member.ID)
	if err != nil {
		return err
	}
	held := map[string]bool{}
	for _, role := range have {
		held[role] = true
	}
	owner, err := identity.OwnerID(ctx, s.d.Pool, tenant)
	if err != nil {
		return err
	}
	for _, role := range managed {
		switch {
		case want[role] && !held[role]:
			if err := rolestore.AssignRole(ctx, s.d.Pool, tenant, member.ID, role, nil); err != nil {
				return err
			}
		case !want[role] && held[role] && owner != member.ID:
			if err := rolestore.RevokeRole(ctx, s.d.Pool, tenant, member.ID, role); err != nil {
				return err
			}
		}
	}
	return nil
}

// ssoMember finds the member an IdP identity signs in as: the one already tied to this subject, an
// existing member with that address (tied now), or a new one provisioned with the default role.
func (s *Service) ssoMember(ctx context.Context, tenant uuid.UUID, p SSOPolicy, who *tenantauth.VerifiedIdentity) (*identity.Member, error) {
	if m, err := identity.FindExternalMember(ctx, s.d.Pool, tenant, p.Provider, who.ExternalSubject); err != nil || m != nil {
		return m, err
	}
	// From here on the address is what identifies the person, so the provider must vouch for it.
	if !who.EmailVerified {
		return nil, errSSOEmailUnverified
	}
	if ok, err := tenantauth.DomainVerifiedFor(ctx, s.d.Pool, tenant, who.Email); err != nil {
		return nil, err
	} else if !ok {
		return nil, errSSODomainUnverified
	}
	if m, err := identity.MemberByEmail(ctx, s.d.Pool, tenant, who.Email); err == nil {
		if m.AuthProvider == identity.ServiceAccountProvider {
			return nil, errors.New("not a person")
		}
		if err := identity.LinkExternalSubject(ctx, s.d.Pool, tenant, m.ID, p.Provider, who.ExternalSubject); err != nil {
			return nil, err
		}
		return m, nil
	} else if !errors.Is(err, identity.ErrNotFound) {
		return nil, err
	}
	m, _, err := identity.AddMember(ctx, s.d.Pool, identity.NewMember{TenantID: tenant, Email: who.Email,
		AuthProvider: p.Provider, ExternalSubject: who.ExternalSubject})
	if err != nil {
		return nil, err
	}
	if p.DefaultRole != "" {
		if err := rolestore.AssignRole(ctx, s.d.Pool, tenant, m.ID, p.DefaultRole, nil); err != nil {
			return nil, err
		}
	}
	return m, nil
}
