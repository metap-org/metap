//go:build integration

package admin

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/identity"
	"github.com/metap-org/metap/pkg/tenancy"
)

// Roadmap 115 M8: the owner is protected from other admins; ownership moves by the owner's own
// transfer or by a recovery the tenant's admins can cancel during the 72-hour wait.
func TestOwnerIsProtectedAndRecoveryWaits(t *testing.T) {
	h := newIdentityHarness(t)
	tenant := uuid.New()
	owner := h.member(tenant, "owner@acme.io", "admin")
	other := h.member(tenant, "admin2@acme.io", "admin")
	viewer := h.member(tenant, "viewer@acme.io", "viewer")
	asOwner, asOther := h.as(tenant, owner.ID), h.as(tenant, other.ID)
	vendor := h.member(tenancy.PlatformTenantID, "agent@vendor.io", RolePlatformAdmin)
	asVendor := h.as(tenancy.PlatformTenantID, vendor.ID)

	// The oldest admin is the owner, and the user list says so.
	st, err := h.client.Owner.GetOwnerStatus(asOther, &Empty{})
	require.NoError(t, err)
	assert.Equal(t, owner.ID.String(), st.GetOwnerMemberId())
	users, err := h.client.Admin.ListUsers(asOwner, &ListUsersRequest{})
	require.NoError(t, err)
	for _, u := range users.GetUsers() {
		assert.Equal(t, u.GetId() == owner.ID.String(), u.GetIsOwner(), u.GetEmail())
	}

	// Another admin cannot touch the owner.
	oid := &UserIdRequest{Id: owner.ID.String()}
	_, err = h.client.Admin.DisableUser(asOther, oid)
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	_, err = h.client.Admin.RemoveUser(asOther, oid)
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	_, err = h.client.Admin.RevokeRole(asOther, &RoleRequest{Id: owner.ID.String(), Role: "admin"})
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	_, err = h.client.Admin.ResetPassword(asOther, &ResetPasswordRequest{Id: owner.ID.String(), Password: "Another-Passw0rd!"})
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	_, err = h.client.Admin.ResetMfa(asOther, oid)
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	// Ordinary members are still administered as before.
	_, err = h.client.Admin.DisableUser(asOther, &UserIdRequest{Id: viewer.ID.String()})
	assert.NoError(t, err)

	// Only the owner transfers, and only to an active admin.
	_, err = h.client.Owner.TransferOwnership(asOther, &TransferOwnershipRequest{MemberId: other.ID.String()})
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	_, err = h.client.Owner.TransferOwnership(asOwner, &TransferOwnershipRequest{MemberId: viewer.ID.String()})
	assert.Equal(t, http.StatusBadRequest, statusOf(err), "not an admin")

	// Recovery: platform admin only, needs a reason, announces and waits.
	req := &RequestOwnerRecoveryRequest{TenantId: tenant.String(), NewOwnerMemberId: other.ID.String(), Reason: "owner left the company"}
	_, err = h.client.Owner.RequestOwnerRecovery(asOwner, req)
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	_, err = h.client.Owner.RequestOwnerRecovery(asVendor, &RequestOwnerRecoveryRequest{TenantId: tenant.String(), NewOwnerMemberId: other.ID.String()})
	assert.Equal(t, http.StatusBadRequest, statusOf(err), "a reason is required")
	rec, err := h.client.Owner.RequestOwnerRecovery(asVendor, req)
	require.NoError(t, err)
	assert.NotEmpty(t, rec.GetEffectiveAt())
	_, err = h.client.Owner.RequestOwnerRecovery(asVendor, req)
	assert.Equal(t, http.StatusConflict, statusOf(err), "one recovery at a time")
	_, err = h.client.Owner.CompleteOwnerRecovery(asVendor, &TenantIdOnlyRequest{TenantId: tenant.String()})
	assert.Equal(t, http.StatusConflict, statusOf(err), "cannot complete during the 72-hour wait")
	st, err = h.client.Owner.GetOwnerStatus(asOwner, &Empty{})
	require.NoError(t, err)
	assert.NotNil(t, st.GetPendingRecovery(), "the tenant sees the pending recovery")

	// The platform admin sees the same, plus who could take over; nobody else may look.
	_, err = h.client.Owner.PlatformTenantOwner(asOwner, &TenantIdOnlyRequest{TenantId: tenant.String()})
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	view, err := h.client.Owner.PlatformTenantOwner(asVendor, &TenantIdOnlyRequest{TenantId: tenant.String()})
	require.NoError(t, err)
	assert.Equal(t, owner.ID.String(), view.GetOwner().GetOwnerMemberId())
	assert.Equal(t, "owner@acme.io", view.GetOwner().GetOwnerEmail())
	assert.NotNil(t, view.GetOwner().GetPendingRecovery())
	var emails []string
	for _, p := range view.GetPeople() {
		emails = append(emails, p.GetEmail())
		assert.Equal(t, p.GetId() == owner.ID.String(), p.GetIsOwner())
	}
	assert.ElementsMatch(t, []string{"owner@acme.io", "admin2@acme.io"}, emails, "the disabled viewer is not offered")

	// An admin cancels it: nothing changes.
	st, err = h.client.Owner.CancelOwnerRecovery(asOther, &Empty{})
	require.NoError(t, err)
	assert.Nil(t, st.GetPendingRecovery())
	assert.Equal(t, owner.ID.String(), st.GetOwnerMemberId())

	// A second recovery, left alone, completes after the wait and moves ownership.
	_, err = h.client.Owner.RequestOwnerRecovery(asVendor, req)
	require.NoError(t, err)
	_, err = h.pool.Exec(context.Background(),
		`UPDATE metadata.owner_recoveries SET effective_at = now() - interval '1 minute' WHERE tenant_id = $1 AND completed_at IS NULL AND cancelled_at IS NULL`, tenant)
	require.NoError(t, err)
	done, err := h.client.Owner.CompleteOwnerRecovery(asVendor, &TenantIdOnlyRequest{TenantId: tenant.String()})
	require.NoError(t, err)
	assert.NotEmpty(t, done.GetCompletedAt())
	st, err = h.client.Owner.GetOwnerStatus(asOther, &Empty{})
	require.NoError(t, err)
	assert.Equal(t, other.ID.String(), st.GetOwnerMemberId())
	// The old owner is now an ordinary admin, and the new owner is protected from them.
	_, err = h.client.Admin.DisableUser(asOwner, &UserIdRequest{Id: other.ID.String()})
	assert.Equal(t, http.StatusForbidden, statusOf(err))

	// The new owner can hand it back.
	back, err := h.client.Owner.TransferOwnership(asOther, &TransferOwnershipRequest{MemberId: owner.ID.String()})
	require.NoError(t, err)
	assert.Equal(t, owner.ID.String(), back.GetOwnerMemberId())
}

// UC-81: only the owner deletes the workspace, by typing its name; every admin is emailed a link
// that cancels it (the workspace is suspended, so the admin area cannot), usable once.
func TestOwnerDeletesTheWorkspaceAndTheLinkCancelsIt(t *testing.T) {
	h := newIdentityHarness(t)
	tenant := uuid.New()
	require.NoError(t, tenancy.NewPostgresTenantRegistry(h.pool).RegisterShared(context.Background(), tenant, "Acme Ltd", "trial", "active"))
	owner := h.member(tenant, "owner@acme.io", "admin")
	other := h.member(tenant, "admin2@acme.io", "admin")
	asOwner, asOther := h.as(tenant, owner.ID), h.as(tenant, other.ID)
	registry := tenancy.NewPostgresTenantRegistry(h.pool)
	ctx := context.Background()

	// Not the owner; wrong name.
	_, err := h.client.Owner.RequestWorkspaceDeletion(asOther, &RequestWorkspaceDeletionRequest{ConfirmName: "Acme Ltd"})
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	_, err = h.client.Owner.RequestWorkspaceDeletion(asOwner, &RequestWorkspaceDeletionRequest{ConfirmName: "acme"})
	assert.Equal(t, http.StatusBadRequest, statusOf(err))

	// The name alone is not enough: the owner's password is asked for again, and a wrong one counts
	// toward the sign-in lockout (audit 07 #5).
	_, err = h.client.Owner.RequestWorkspaceDeletion(asOwner, &RequestWorkspaceDeletionRequest{ConfirmName: "Acme Ltd"})
	assert.Equal(t, http.StatusBadRequest, statusOf(err), "no password")
	_, err = h.client.Owner.RequestWorkspaceDeletion(asOwner, &RequestWorkspaceDeletionRequest{ConfirmName: "Acme Ltd", Password: "not-it"})
	assert.Equal(t, http.StatusBadRequest, statusOf(err), "wrong password")

	// With a second factor set up, a code is asked for too.
	secret, _, err := identity.BeginMFAEnrollment(ctx, h.pool, owner.IdentityID, "owner@acme.io", time.Now())
	require.NoError(t, err)
	code, err := identity.TOTPAt(secret, time.Now())
	require.NoError(t, err)
	_, err = identity.ConfirmMFAEnrollment(ctx, h.pool, owner.IdentityID, code, time.Now())
	require.NoError(t, err)
	_, err = h.client.Owner.RequestWorkspaceDeletion(asOwner, &RequestWorkspaceDeletionRequest{ConfirmName: "Acme Ltd", Password: "password-1"})
	assert.Equal(t, http.StatusBadRequest, statusOf(err), "no second-factor code")
	_, err = h.client.Owner.RequestWorkspaceDeletion(asOwner, &RequestWorkspaceDeletionRequest{ConfirmName: "Acme Ltd", Password: "password-1", MfaCode: "000000"})
	assert.Equal(t, http.StatusUnauthorized, statusOf(err), "wrong second-factor code")
	// A TOTP step is single use, so the next valid code is a later one.
	code, err = identity.TOTPAt(secret, time.Now().Add(30*time.Second))
	require.NoError(t, err)
	out, err := h.client.Owner.RequestWorkspaceDeletion(asOwner, &RequestWorkspaceDeletionRequest{ConfirmName: "Acme Ltd", Password: "password-1", MfaCode: code})
	require.NoError(t, err)
	assert.NotEmpty(t, out.EffectiveAt)
	assert.Empty(t, out.CancelLink, "the link is in the emails when mail works")
	d, err := registry.Get(ctx, tenant)
	require.NoError(t, err)
	assert.Equal(t, tenancy.StatusSuspended, d.Status)

	// Both admins were emailed the same working link.
	token := cancelTokenFrom(t, h.mail.last(t, "owner@acme.io"))
	assert.Equal(t, token, cancelTokenFrom(t, h.mail.last(t, "admin2@acme.io")))

	_, err = h.client.Owner.RequestWorkspaceDeletion(asOwner, &RequestWorkspaceDeletionRequest{ConfirmName: "Acme Ltd", Password: "password-1"})
	assert.Error(t, err, "already scheduled")

	// The link reopens the workspace once; a second use, or a wrong token, does nothing.
	got, err := identity.ConsumeDeletionCancelToken(ctx, h.pool, "not-a-token", time.Now())
	assert.ErrorIs(t, err, identity.ErrCancelInvalid)
	assert.Equal(t, uuid.Nil, got)
	got, err = identity.ConsumeDeletionCancelToken(ctx, h.pool, token, time.Now())
	require.NoError(t, err)
	assert.Equal(t, tenant, got)
	require.NoError(t, registry.CancelDeletion(ctx, got, time.Now()))
	_, err = identity.ConsumeDeletionCancelToken(ctx, h.pool, token, time.Now())
	assert.ErrorIs(t, err, identity.ErrCancelInvalid, "a link works once")
	d, _ = registry.Get(ctx, tenant)
	assert.Equal(t, tenancy.StatusActive, d.Status)
}

// A link stops working once the deletion would have taken effect.
func TestDeletionCancelTokenExpires(t *testing.T) {
	h := newIdentityHarness(t)
	tenant := uuid.New()
	ctx := context.Background()
	token, err := identity.CreateDeletionCancelToken(ctx, h.pool, tenant, time.Now().Add(-time.Minute))
	require.NoError(t, err)
	_, err = identity.ConsumeDeletionCancelToken(ctx, h.pool, token, time.Now())
	assert.ErrorIs(t, err, identity.ErrCancelInvalid)
}

var cancelToken = regexp.MustCompile(`/cancel-deletion\?token=([^\s&]+)`)

func cancelTokenFrom(t *testing.T, text string) string {
	t.Helper()
	m := cancelToken.FindStringSubmatch(text)
	require.Len(t, m, 2, "a cancel-deletion link")
	tok, err := url.QueryUnescape(m[1])
	require.NoError(t, err)
	return tok
}
