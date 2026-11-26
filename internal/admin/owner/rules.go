package owner

import (
	"errors"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/kit/errcode"
)

// AuditEntity is the audit-trail entity owner changes are recorded under (the record is the tenant).
const AuditEntity = kernel.AuditEntityOwner

// oldest is the admin who has been there longest: who owns a tenant that has no owner yet. Pure.
func oldest(admins []identity.Member) identity.Member {
	o := admins[0]
	for _, m := range admins[1:] {
		if m.CreatedAt.Before(o.CreatedAt) {
			o = m
		}
	}
	return o
}

// recoveryView is a recovery as the API shows it. Pure.
func recoveryView(r *identity.OwnerRecovery) *OwnerRecovery {
	return &OwnerRecovery{Id: r.ID.String(), NewOwnerMemberId: r.NewOwnerMember.String(), RequestedByEmail: r.RequestedByEmail,
		Reason: r.Reason, EffectiveAt: kernel.FormatTime(&r.EffectiveAt), CreatedAt: kernel.FormatTime(&r.CreatedAt),
		CompletedAt: kernel.FormatTime(r.CompletedAt)}
}

func fail(status uint16, code, msg string) error { return kernel.Err(status, code, msg) }

// storeErr maps identity's recovery errors onto the API's; anything else is internal.
func storeErr(err error) error {
	switch {
	case errors.Is(err, identity.ErrRecoveryPending):
		return fail(kernel.StatusConflict, errcode.ValidationFailed, err.Error())
	case errors.Is(err, identity.ErrRecoveryNotReady):
		return fail(kernel.StatusConflict, errcode.Forbidden, err.Error())
	case errors.Is(err, identity.ErrNoRecovery), errors.Is(err, identity.ErrNotFound):
		return fail(kernel.StatusNotFound, errcode.NotFound, err.Error())
	case errors.Is(err, identity.ErrSupportReason):
		return fail(kernel.StatusBadRequest, errcode.ValidationFailed, "A reason of 1-500 characters is required.")
	}
	return err
}

func tenantIDErr() error {
	return fail(kernel.StatusBadRequest, errcode.ValidationFailed, "`tenantId` must be a tenant id.")
}

// fieldRequired is the 400 for a missing confirmation field. Pure.
func fieldRequired(field, msg string) error {
	return &crud.ServiceError{Status: kernel.StatusBadRequest, Code: errcode.ValidationFailed, Message: msg,
		FieldErrors: map[string][]string{field: {"required"}}}
}

// stepUpErr maps a failed re-authentication onto the API's. Pure.
func stepUpErr(err error) error {
	if errors.Is(err, identity.ErrInvalidCredentials) {
		return fail(kernel.StatusBadRequest, errcode.WrongPassword, "The password is wrong.")
	}
	return kernel.IdentityErr(err)
}
