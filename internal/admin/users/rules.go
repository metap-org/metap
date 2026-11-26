package users

import (
	"slices"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
)

// AuditEntity is the audit-trail entity user administration records under (the record is the member).
const AuditEntity = kernel.AuditEntityUsers

func fail(status uint16, code, msg string) error { return kernel.Err(status, code, msg) }

// view is a member as the API shows them. invitation is when their pending invitation ends (nil: none).
// Pure.
func view(m identity.Member, roles []string, scoped []permission.ScopedRole, shared, isOwner bool, invitation *identity.Invitation) *User {
	u := &User{IsOwner: isOwner,
		Id: m.ID.String(), Email: m.Email, DisplayName: m.DisplayName, Status: m.Status,
		Roles: roles, AuthProvider: m.AuthProvider, HasPassword: m.HasPassword,
		LastLoginAt: kernel.FormatTime(m.LastLoginAt), CreatedAt: kernel.FormatTime(&m.CreatedAt),
		SharedIdentity: shared,
	}
	if u.Roles == nil {
		u.Roles = []string{}
	}
	for _, sr := range scoped {
		u.ScopedRoles = append(u.ScopedRoles, &ScopedRole{Role: sr.Role, ScopeType: sr.ScopeType, ScopeId: sr.ScopeID})
	}
	if m.InvitedBy != nil {
		u.InvitedBy = m.InvitedBy.String()
	}
	if invitation != nil {
		u.InvitationExpiresAt = kernel.FormatTime(&invitation.ExpiresAt)
	}
	return u
}

// scopedView is a member's scoped roles as the API shows them. Pure.
func scopedView(in []permission.ScopedRole) []*ScopedRole {
	var out []*ScopedRole
	for _, sr := range in {
		out = append(out, &ScopedRole{Role: sr.Role, ScopeType: sr.ScopeType, ScopeId: sr.ScopeID})
	}
	return out
}

func sameScoped(a, b []*ScopedRole) bool {
	return slices.EqualFunc(a, b, func(x, y *ScopedRole) bool {
		return x.GetRole() == y.GetRole() && x.GetScopeType() == y.GetScopeType() && x.GetScopeId() == y.GetScopeId()
	})
}

// scopedForAudit lists scoped roles as "role@type:id". Pure.
func scopedForAudit(in []*ScopedRole) []string {
	out := make([]string, len(in))
	for i, r := range in {
		out[i] = r.GetRole() + "@" + r.GetScopeType() + ":" + r.GetScopeId()
	}
	return out
}

// otherAdmins is who would still hold admin if id did not, and whether id held it at all. Pure.
func otherAdmins(admins []uuid.UUID, id uuid.UUID) (others []uuid.UUID, wasAdmin bool) {
	if !slices.Contains(admins, id) {
		return nil, false
	}
	return slices.DeleteFunc(slices.Clone(admins), func(a uuid.UUID) bool { return a == id }), true
}

// passwordRequired is the 400 for a new account made without a password. Pure.
func passwordRequired() error {
	return &crud.ServiceError{Status: kernel.StatusBadRequest, Code: errcode.WeakPassword,
		Message: "A password is required for a new account, or invite the person instead.", FieldErrors: map[string][]string{"password": {"required for a new account"}}}
}

func selfModification(what string) error {
	return fail(kernel.StatusConflict, errcode.SelfModification, "You cannot "+what+" yourself.")
}

func lastAdmin() error {
	return fail(kernel.StatusConflict, errcode.LastAdmin, "The tenant must keep at least one active admin.")
}

func sharedPerson(what string) error {
	return fail(kernel.StatusConflict, errcode.Forbidden, "This person also belongs to another tenant; only they can change their "+what+".")
}

func scopeInvalid(err error) error {
	return &crud.ServiceError{Status: kernel.StatusBadRequest, Code: errcode.ValidationFailed,
		Message: err.Error(), FieldErrors: map[string][]string{"scope": {err.Error()}}}
}
