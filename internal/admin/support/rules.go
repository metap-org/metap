package support

import (
	"errors"
	"time"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/kit/errcode"
)

// AuditEntity is the audit-trail entity support grants are recorded under.
const AuditEntity = "metadata.support_grants"

// view is a grant as the API shows it, with whether it is active at now. Pure.
func view(g identity.SupportGrant, now time.Time) *SupportGrant {
	out := &SupportGrant{Id: g.ID.String(), Reason: g.Reason, Role: g.Role, GrantedBy: g.GrantedBy.String(),
		ExpiresAt: kernel.FormatTime(&g.ExpiresAt), RevokedAt: kernel.FormatTime(g.RevokedAt), CreatedAt: kernel.FormatTime(&g.CreatedAt),
		Active: g.Active(now)}
	for _, s := range g.Sessions {
		started := s.StartedAt
		out.Sessions = append(out.Sessions, &SupportSessionInfo{MemberId: s.MemberID.String(),
			StartedByEmail: s.StartedByEmail, StartedAt: kernel.FormatTime(&started)})
	}
	return out
}

// sessionExpiry is when a support session's key ends: the grant's end, but never later than the
// longest a session may last from now (identity.SupportSessionMaxHours). Pure.
func sessionExpiry(grantEnds, now time.Time) time.Time {
	expires := now.Add(time.Duration(identity.SupportSessionMaxHours) * time.Hour)
	if grantEnds.Before(expires) {
		return grantEnds
	}
	return expires
}

// mapErr maps the errors of grants onto the platform envelope.
func mapErr(err error) error {
	switch {
	case errors.Is(err, identity.ErrNotFound):
		return kernel.Err(kernel.StatusNotFound, errcode.NotFound, "Support grant not found.")
	case errors.Is(err, identity.ErrSupportGrantInactive):
		return kernel.Err(kernel.StatusConflict, errcode.Forbidden, "The tenant has no active support grant.")
	case errors.Is(err, identity.ErrSupportReason):
		return kernel.Err(kernel.StatusBadRequest, errcode.ValidationFailed, err.Error())
	}
	return err
}
