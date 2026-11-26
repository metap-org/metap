//go:build integration

package admin

import (
	"context"
	"github.com/metap-org/metap/internal/admin/roles"
	"github.com/metap-org/metap/internal/admin/trail"
	"github.com/metap-org/metap/internal/authctx"
	"github.com/metap-org/metap/pkg/permission/policypg"
	"github.com/metap-org/metap/pkg/permission/rolestore"
	"net/http"
	"net/url"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/action"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/auth/jwks"
	accountmail "github.com/metap-org/metap/pkg/auth/mail"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/testkit"
)

type identityHarness struct {
	t       *testing.T
	pool    *pgxpool.Pool
	privPEM string
	client  *testClient
	mail    *capturedMail
	auth    testAuthConfig
	cfg     IdentityConfig
}

// capturedMail records what the mailer sent (to + raw message).
type capturedMail struct {
	mu   sync.Mutex
	sent [][2]string
}

func (m *capturedMail) send(_ context.Context, _ accountmail.Config, to string, msg []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, [2]string{to, string(msg)})
	return nil
}

func (m *capturedMail) last(t *testing.T, to string) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.sent) - 1; i >= 0; i-- {
		if m.sent[i][0] == to {
			return m.sent[i][1]
		}
	}
	t.Fatalf("no email to %s", to)
	return ""
}

// newIdentityHarness serves the identity services with a capturing mailer; withMailer false leaves
// email unconfigured.
func newIdentityHarness(t *testing.T, withMailer ...bool) *identityHarness {
	pool := testkit.NewPostgres(t)
	privPEM, pubKey := genRSAKeyPair(t)
	var mdPtr atomic.Pointer[metadata.MetadataRegistry]
	mdPtr.Store(metadata.NewMetadataRegistry())
	router := testRouterFor(pool)
	auth := testAuthConfig{
		Verifier:               &jwks.TokenVerifier{Kind: jwks.VerifierStatic, PublicKey: pubKey, Leeway: 20 * time.Second},
		Router:                 router,
		Metadata:               &mdPtr,
		ContextAttributesCache: authctx.NewContextAttributesCache(60 * time.Second),
	}
	mail := &capturedMail{}
	cfg := IdentityConfig{Shared: pool, Router: router, Audit: audit.NewPostgresStore(pool), Sink: action.NewSink(router), PublicURL: "http://portal.test",
		PasswordPolicy: func(ctx context.Context, tenant uuid.UUID) int {
			var n int
			if err := pool.QueryRow(ctx, `SELECT value::int FROM metadata.tenant_configs
				WHERE tenant_id = $1 AND key = 'auth.passwordMinLength'`, tenant).Scan(&n); err != nil {
				return identity.MinPasswordLength
			}
			return n
		},
		APIKeyMaxDays: func(ctx context.Context, tenant uuid.UUID) int {
			var n int
			if err := pool.QueryRow(ctx, `SELECT value::int FROM metadata.tenant_configs
				WHERE tenant_id = $1 AND key = 'auth.apiKeyMaxLifetimeDays'`, tenant).Scan(&n); err != nil {
				return 0
			}
			return n
		}}
	if len(withMailer) == 0 || withMailer[0] {
		cfg.Mailer = accountmail.New(accountmail.Config{Host: "mail", Port: 1025, From: "no-reply@example.com",
			PublicURL: "http://portal.test"}).WithSender(mail.send)
	}
	h := &identityHarness{t: t, pool: pool, privPEM: privPEM, mail: mail, auth: auth, cfg: cfg}
	h.client = newTestClient(cfg)
	activeAuth = auth
	return h
}

func (h *identityHarness) as(tenant, user uuid.UUID) context.Context {
	token, err := jwks.MintRSAJWT(h.privPEM, tenant, user, 3600)
	require.NoError(h.t, err)
	return CallContext(context.Background(), token)
}

func (h *identityHarness) member(tenant uuid.UUID, email string, roles ...string) *identity.Member {
	m, _, err := identity.AddMember(context.Background(), h.pool, identity.NewMember{TenantID: tenant, Email: email, Password: "password-1"})
	require.NoError(h.t, err)
	for _, r := range roles {
		testkit.SeedRole(h.t, h.pool, tenant, m.ID, r)
	}
	return m
}

// policyRole adds a policy naming role, registering the role in the catalog the way the policy
// store does.
func (h *identityHarness) policyRole(tenant uuid.UUID, role string) {
	_, err := h.pool.Exec(context.Background(), `INSERT INTO metadata.policies (tenant_id, entity, action, roles)
		VALUES ($1, 'test.things', 'read', jsonb_build_array($2::text))`, tenant, role)
	require.NoError(h.t, err)
	require.NoError(h.t, rolestore.EnsureRoles(context.Background(), h.pool, tenant, []string{role}, nil))
}

// errCode calls fn with a trailer and returns the ServiceError's code ("" on success).
func errCode(t *testing.T, fn func() error) string {
	t.Helper()
	err := fn()
	if err == nil {
		return ""
	}
	var se *crud.ServiceError
	require.ErrorAs(t, err, &se)
	return se.Code
}

func TestUserAdminLifecycle(t *testing.T) {
	h := newIdentityHarness(t)
	tenant := uuid.New()
	admin := h.member(tenant, "admin@acme.io", "admin")
	h.policyRole(tenant, "analyst")
	ctx := h.as(tenant, admin.ID)

	roles, err := h.client.Roles.List(ctx, &roles.ListRolesRequest{})
	require.NoError(t, err)
	names := map[string]int32{}
	for _, r := range roles.GetRoles() {
		names[r.GetName()] = r.GetUserCount()
	}
	assert.Equal(t, map[string]int32{"admin": 1, "analyst": 0}, names)

	assert.Equal(t, "validation_failed", errCode(t, func() error {
		_, err := h.client.Admin.CreateUser(ctx, &CreateUserRequest{Email: "x@acme.io", Password: "password-1", Roles: []string{"ghost"}})
		return err
	}), "a role outside the catalog is a typo")
	assert.Equal(t, "weak_password", errCode(t, func() error {
		_, err := h.client.Admin.CreateUser(ctx, &CreateUserRequest{Email: "x@acme.io", Password: "short"})
		return err
	}))
	assert.Equal(t, "weak_password", errCode(t, func() error {
		_, err := h.client.Admin.CreateUser(ctx, &CreateUserRequest{Email: "x@acme.io"})
		return err
	}), "a brand-new account needs a password until invitations exist")

	created, err := h.client.Admin.CreateUser(ctx, &CreateUserRequest{
		Email: "bob@acme.io", Password: "bob-password", DisplayName: "Bob", Roles: []string{"analyst"},
	})
	require.NoError(t, err)
	assert.True(t, created.GetCreatedIdentity())
	bob := created.GetUser()
	assert.Equal(t, []string{"analyst"}, bob.GetRoles())
	assert.Equal(t, "Bob", bob.GetDisplayName())
	assert.False(t, bob.GetSharedIdentity())
	bobID := uuid.MustParse(bob.GetId())

	assert.Equal(t, "already_member", errCode(t, func() error {
		_, err := h.client.Admin.CreateUser(ctx, &CreateUserRequest{Email: "BOB@acme.io", Password: "bob-password"})
		return err
	}))

	list, err := h.client.Admin.ListUsers(ctx, &ListUsersRequest{Role: "analyst"})
	require.NoError(t, err)
	require.EqualValues(t, 1, list.GetTotal())
	assert.Equal(t, "bob@acme.io", list.GetUsers()[0].GetEmail())
	list, err = h.client.Admin.ListUsers(ctx, &ListUsersRequest{Search: "acme", Limit: 1})
	require.NoError(t, err)
	assert.EqualValues(t, 2, list.GetTotal())
	assert.Len(t, list.GetUsers(), 1)

	// Bob in a second tenant: still one person, and now this tenant may not reset his password.
	other := uuid.New()
	otherAdmin := h.member(other, "boss@other.io", "admin")
	again, err := h.client.Admin.CreateUser(h.as(other, otherAdmin.ID), &CreateUserRequest{Email: "bob@acme.io", Password: "ignored-pass"})
	require.NoError(t, err)
	assert.False(t, again.GetCreatedIdentity())
	bobGot, err := h.client.Admin.GetUser(ctx, &UserIdRequest{Id: bob.GetId()})
	require.NoError(t, err)
	assert.True(t, bobGot.GetSharedIdentity())
	assert.Equal(t, "forbidden", errCode(t, func() error {
		_, err := h.client.Admin.ResetPassword(ctx, &ResetPasswordRequest{Id: bob.GetId(), Password: "taken-over"})
		return err
	}))
	_, err = identity.Authenticate(context.Background(), h.pool, "bob@acme.io", "bob-password", time.Now())
	require.NoError(t, err, "the second tenant's password was ignored, the first one still works")

	tenants, err := h.client.Account.ListMyTenants(h.as(tenant, bobID), &Empty{})
	require.NoError(t, err)
	assert.Len(t, tenants.GetTenants(), 2)

	// Guards: no self-harm.
	assert.Equal(t, "self_modification", errCode(t, func() error {
		_, err := h.client.Admin.DisableUser(ctx, &UserIdRequest{Id: admin.ID.String()})
		return err
	}))
	assert.Equal(t, "self_modification", errCode(t, func() error {
		_, err := h.client.Admin.RevokeRole(ctx, &RoleRequest{Id: admin.ID.String(), Role: "admin"})
		return err
	}))
	// A non-person admin (service principal) cannot take away the last person admin either.
	svc := uuid.New()
	testkit.SeedRole(t, h.pool, tenant, svc, "admin")
	assert.Equal(t, "last_admin", errCode(t, func() error {
		_, err := h.client.Admin.RemoveUser(h.as(tenant, svc), &UserIdRequest{Id: admin.ID.String()})
		return err
	}))

	// Disable: Bob's existing session dies at once; enable does not resurrect it.
	bobCtx := h.as(tenant, bobID)
	_, err = h.client.Account.GetMe(bobCtx, &Empty{})
	require.NoError(t, err)
	disabled, err := h.client.Admin.DisableUser(ctx, &UserIdRequest{Id: bob.GetId()})
	require.NoError(t, err)
	assert.Equal(t, "disabled", disabled.GetStatus())
	_, err = h.client.Account.GetMe(bobCtx, &Empty{})
	require.Error(t, err)
	_, err = h.client.Admin.EnableUser(ctx, &UserIdRequest{Id: bob.GetId()})
	require.NoError(t, err)

	_, err = h.client.Admin.AssignRole(ctx, &RoleRequest{Id: bob.GetId(), Role: "admin"})
	require.NoError(t, err)
	updated, err := h.client.Admin.UpdateUser(ctx, &UpdateUserRequest{Id: bob.GetId(), DisplayName: "Robert"})
	require.NoError(t, err)
	assert.Equal(t, "Robert", updated.GetDisplayName())
	assert.ElementsMatch(t, []string{"admin", "analyst"}, updated.GetRoles())

	_, err = h.client.Admin.RemoveUser(ctx, &UserIdRequest{Id: bob.GetId()})
	require.NoError(t, err)
	assert.Equal(t, "not_found", errCode(t, func() error {
		_, err := h.client.Admin.GetUser(ctx, &UserIdRequest{Id: bob.GetId()})
		return err
	}))
	gone, err := rolestore.GetRolesForUser(context.Background(), h.pool, tenant, bobID)
	require.NoError(t, err)
	assert.Empty(t, gone, "removing a member drops their roles")

	h.drainAudit()

	hist, err := h.client.Admin.ListUserAudit(ctx, &ListUserAuditRequest{Id: bob.GetId(), Limit: 3})
	require.NoError(t, err)
	require.Len(t, hist.GetEvents(), 3)
	assert.Equal(t, "delete", hist.GetEvents()[0].GetAction(), "newest first")
	assert.Equal(t, admin.ID.String(), hist.GetEvents()[0].GetActorUserId())
	assert.NotEmpty(t, hist.GetNextCursor())
	h.drainAudit()
	rest, err := h.client.Admin.ListUserAudit(ctx, &ListUserAuditRequest{Id: bob.GetId(), Cursor: hist.GetNextCursor()})
	require.NoError(t, err)
	// create, disable, enable, assign admin, rename, remove = 6 entries.
	assert.Len(t, rest.GetEvents(), 3)
	assert.Empty(t, rest.GetNextCursor())
}

func TestUserAdminNeedsTheAdminRole(t *testing.T) {
	h := newIdentityHarness(t)
	tenant := uuid.New()
	plain := h.member(tenant, "plain@acme.io")
	_, err := h.client.Admin.ListUsers(h.as(tenant, plain.ID), &ListUsersRequest{})
	require.Error(t, err)
	se := asServiceError(err)
	assert.EqualValues(t, 403, se.Status)

	me, err := h.client.Account.UpdateProfile(h.as(tenant, plain.ID), &UpdateProfileRequest{DisplayName: "Plain Person"})
	require.NoError(t, err)
	assert.Equal(t, "Plain Person", me.GetDisplayName())
	assert.Equal(t, "plain@acme.io", me.GetEmail())
}

func TestRoleAdminLifecycle(t *testing.T) {
	h := newIdentityHarness(t)
	tenant := uuid.New()
	admin := h.member(tenant, "admin@acme.io", "admin")
	ctx := h.as(tenant, admin.ID)
	bg := context.Background()

	for _, bad := range []string{"Analyst", "1st", "has space", "__everyone__", ""} {
		assert.Equal(t, "validation_failed", errCode(t, func() error {
			_, err := h.client.Roles.Create(ctx, &roles.CreateRoleRequest{Name: bad})
			return err
		}), "name %q", bad)
	}

	created, err := h.client.Roles.Create(ctx, &roles.CreateRoleRequest{Name: "analyst", Label: "Analyst", Description: "Reads incidents"})
	require.NoError(t, err)
	assert.Equal(t, "Analyst", created.GetLabel())
	assert.False(t, created.GetBuiltin())
	assert.Equal(t, "role_exists", errCode(t, func() error {
		_, err := h.client.Roles.Create(ctx, &roles.CreateRoleRequest{Name: "analyst"})
		return err
	}))

	// An empty role is assignable at once: the catalog, not the policies, decides what exists.
	bob, err := h.client.Admin.CreateUser(ctx, &CreateUserRequest{Email: "bob@acme.io", Password: "bob-password", Roles: []string{"analyst"}})
	require.NoError(t, err)

	list, err := h.client.Roles.List(ctx, &roles.ListRolesRequest{})
	require.NoError(t, err)
	byName := map[string]*roles.Role{}
	for _, r := range list.GetRoles() {
		byName[r.GetName()] = r
	}
	require.Contains(t, byName, "admin", "the built-in role row exists without anyone creating it")
	assert.True(t, byName["admin"].GetBuiltin())
	assert.EqualValues(t, 1, byName["analyst"].GetUserCount())
	assert.EqualValues(t, 0, byName["analyst"].GetPolicyCount())
	found, err := h.client.Roles.List(ctx, &roles.ListRolesRequest{Search: "incident"})
	require.NoError(t, err)
	require.Len(t, found.GetRoles(), 1, "search matches the description")

	updated, err := h.client.Roles.Update(ctx, &roles.UpdateRoleRequest{Name: "analyst", Label: "SOC analyst"})
	require.NoError(t, err)
	assert.Equal(t, "SOC analyst", updated.GetLabel())
	assert.Empty(t, updated.GetDescription(), "update sets both fields")

	// Delete is refused while a member holds the role, then while a policy names it.
	_, err = h.client.Roles.Delete(ctx, &roles.RoleNameRequest{Name: "analyst"})
	require.Error(t, err)
	se := asServiceError(err)
	assert.Equal(t, "role_in_use", se.Code)
	assert.Equal(t, []string{"bob@acme.io"}, se.FieldErrors["members"])

	_, err = h.client.Admin.RevokeRole(ctx, &RoleRequest{Id: bob.GetUser().GetId(), Role: "analyst"})
	require.NoError(t, err)
	h.policyRole(tenant, "analyst")
	_, err = h.client.Roles.Delete(ctx, &roles.RoleNameRequest{Name: "analyst"})
	require.Error(t, err)
	se = asServiceError(err)
	assert.Equal(t, "role_in_use", se.Code)
	assert.Equal(t, []string{"test.things read"}, se.FieldErrors["policies"])
	assert.Empty(t, se.FieldErrors["members"])

	_, err = h.pool.Exec(bg, `DELETE FROM metadata.policies WHERE tenant_id = $1`, tenant)
	require.NoError(t, err)
	_, err = h.client.Roles.Delete(ctx, &roles.RoleNameRequest{Name: "analyst"})
	require.NoError(t, err)
	assert.Equal(t, "not_found", errCode(t, func() error {
		_, err := h.client.Roles.Get(ctx, &roles.RoleNameRequest{Name: "analyst"})
		return err
	}))
	assert.Equal(t, "role_in_use", errCode(t, func() error {
		_, err := h.client.Roles.Delete(ctx, &roles.RoleNameRequest{Name: "admin"})
		return err
	}), "a built-in role is never deleted")

	// Recreated under the same name, the role starts a fresh audit trail (a new id).
	_, err = h.client.Roles.Create(ctx, &roles.CreateRoleRequest{Name: "analyst"})
	require.NoError(t, err)
	h.drainAudit()
	trail, err := h.client.Roles.ListAudit(ctx, &roles.ListRoleAuditRequest{Name: "analyst"})
	require.NoError(t, err)
	require.Len(t, trail.GetEvents(), 1)
	assert.Equal(t, "create", trail.GetEvents()[0].GetAction())

	// A role named only by a write outside the admin API (a policy, an assignment) joins the catalog.
	_, err = h.client.Admin.AssignRole(ctx, &RoleRequest{Id: bob.GetUser().GetId(), Role: "ghost"})
	assert.Error(t, err, "the admin API still refuses an unknown role")
	require.NoError(t, rolestore.AssignRole(bg, h.pool, tenant, uuid.MustParse(bob.GetUser().GetId()), "auditor", nil))
	_, err = h.client.Roles.Get(ctx, &roles.RoleNameRequest{Name: "auditor"})
	require.NoError(t, err)

	outsider := h.member(tenant, "viewer@acme.io")
	assert.Equal(t, http.StatusForbidden, statusOf(func() error {
		_, err := h.client.Roles.List(h.as(tenant, outsider.ID), &roles.ListRolesRequest{})
		return err
	}()))
}

func TestRolePermissionsReplaceBasicGrantsOnly(t *testing.T) {
	h := newIdentityHarness(t)
	tenant := uuid.New()
	admin := h.member(tenant, "admin@acme.io", "admin")
	ctx := h.as(tenant, admin.ID)
	bg := context.Background()

	_, err := h.client.Roles.Create(ctx, &roles.CreateRoleRequest{Name: "analyst"})
	require.NoError(t, err)
	// Not this call's to touch: an advanced row naming the role, and a basic row it shares.
	_, err = h.pool.Exec(bg, `INSERT INTO metadata.policies (tenant_id, entity, action, roles, condition)
		VALUES ($1, 'waf.zones', 'delete', '["analyst"]', '{"field":"status","op":"eq","value":"paused"}')`, tenant)
	require.NoError(t, err)
	_, err = h.pool.Exec(bg, `INSERT INTO metadata.policies (tenant_id, entity, action, roles)
		VALUES ($1, 'waf.rules', 'read', '["analyst","viewer"]')`, tenant)
	require.NoError(t, err)

	got, err := h.client.Roles.GetPermissions(ctx, &roles.RoleNameRequest{Name: "analyst"})
	require.NoError(t, err)
	require.Len(t, got.GetGrants(), 1, "a shared basic row counts as the role's grant")
	require.Len(t, got.GetAdvanced(), 1)
	assert.Equal(t, "waf.zones", got.GetAdvanced()[0].GetEntity())
	assert.NotEmpty(t, string(got.GetAdvanced()[0].GetCondition()))

	set, err := h.client.Roles.SetPermissions(ctx, &roles.SetRolePermissionsRequest{Role: "analyst", Grants: []*roles.RoleGrant{
		{Entity: "waf.zones", Action: "read"},
		{Entity: "waf.zones", Action: "update"},
		{Entity: "waf.zones", Action: "transition:pause"},
		{Entity: "waf.findings", Action: "transition:markFalsePositive"}, // 28 chars: needs migration 0042
		{Entity: "waf.zones", Action: "read"},                            // duplicate, written once
	}})
	require.NoError(t, err)
	keys := []string{}
	for _, g := range set.GetGrants() {
		keys = append(keys, g.GetEntity()+" "+g.GetAction())
	}
	assert.ElementsMatch(t, []string{"waf.findings transition:markFalsePositive", "waf.zones read",
		"waf.zones transition:pause", "waf.zones update"}, keys)
	require.Len(t, set.GetAdvanced(), 1, "the advanced row survives")

	var shared []byte
	require.NoError(t, h.pool.QueryRow(bg, `SELECT roles FROM metadata.policies WHERE tenant_id = $1 AND entity = 'waf.rules'`, tenant).Scan(&shared))
	assert.JSONEq(t, `["viewer"]`, string(shared), "the shared row loses only this role")

	// The saved grants are what the permission engine enforces, right away.
	perms := permission.NewPermissionService(policypg.NewPostgresPolicyStore(testRouterFor(h.pool)))
	rc := &permission.RequestContext{TenantID: tenant.String(), UserID: uuid.NewString(), Roles: []string{"analyst"}, HasRoles: true}
	allowed := func(d permission.PermissionDecision, err error) bool {
		require.NoError(t, err)
		return d.Allowed
	}
	assert.True(t, allowed(perms.CanUpdateEntity(bg, rc, "waf.zones")))
	assert.False(t, allowed(perms.CanCreateEntity(bg, rc, "waf.zones")))
	assert.True(t, allowed(perms.CanTransitionAction(bg, rc, "waf.zones", "pause")))
	assert.False(t, allowed(perms.CanTransitionAction(bg, rc, "waf.zones", "resume")), "only the granted workflow action")
	assert.False(t, allowed(perms.CanReadEntity(bg, rc, "waf.rules")))

	assert.Equal(t, "validation_failed", errCode(t, func() error {
		_, err := h.client.Roles.SetPermissions(ctx, &roles.SetRolePermissionsRequest{Role: "analyst",
			Grants: []*roles.RoleGrant{{Entity: "waf.zones", Action: "approve"}}})
		return err
	}), "an action outside the policy grammar")
	assert.Equal(t, "validation_failed", errCode(t, func() error {
		_, err := h.client.Roles.SetPermissions(ctx, &roles.SetRolePermissionsRequest{Role: "admin"})
		return err
	}), "admin bypasses policies")
	assert.Equal(t, "not_found", errCode(t, func() error {
		_, err := h.client.Roles.SetPermissions(ctx, &roles.SetRolePermissionsRequest{Role: "ghost"})
		return err
	}))

	// copyFrom starts a role with the source's basic grants (not its advanced rows).
	copied, err := h.client.Roles.Create(ctx, &roles.CreateRoleRequest{Name: "analyst_two", CopyFrom: "analyst"})
	require.NoError(t, err)
	assert.EqualValues(t, 4, copied.GetPolicyCount())
	two, err := h.client.Roles.GetPermissions(ctx, &roles.RoleNameRequest{Name: "analyst_two"})
	require.NoError(t, err)
	assert.Len(t, two.GetGrants(), 4)
	assert.Empty(t, two.GetAdvanced())

	h.drainAudit()

	trail, err := h.client.Roles.ListAudit(ctx, &roles.ListRoleAuditRequest{Name: "analyst"})
	require.NoError(t, err)
	require.NotEmpty(t, trail.GetEvents())
	assert.Contains(t, string(trail.GetEvents()[0].GetDiff()), "granted")
	assert.Contains(t, string(trail.GetEvents()[0].GetDiff()), "revoked")
}

var inviteToken = regexp.MustCompile(`accept-invite\?token=([A-Za-z0-9_%\-]+)`)

func tokenFrom(t *testing.T, text string) string {
	t.Helper()
	m := inviteToken.FindStringSubmatch(text)
	require.Len(t, m, 2, "an accept-invite link")
	tok, err := url.QueryUnescape(m[1])
	require.NoError(t, err)
	return tok
}

func TestInviteUserEmailsALinkAndResendReplacesIt(t *testing.T) {
	h := newIdentityHarness(t)
	tenant := uuid.New()
	admin := h.member(tenant, "admin@acme.io", "admin")
	_, err := h.pool.Exec(context.Background(), `UPDATE metadata.users SET display_name = 'Alice' WHERE id = $1`, admin.ID)
	require.NoError(t, err)
	h.policyRole(tenant, "analyst")
	ctx := h.as(tenant, admin.ID)
	bg := context.Background()
	email := uuid.NewString() + "@example.com"

	res, err := h.client.Admin.InviteUser(ctx, &InviteUserRequest{Email: email, DisplayName: "Bob", Roles: []string{"analyst"}})
	require.NoError(t, err)
	assert.True(t, res.GetEmailSent())
	assert.Empty(t, res.GetAcceptUrl(), "the link only goes to the invitee once the email went out")
	u := res.GetUser()
	assert.Equal(t, "invited", u.GetStatus())
	assert.Equal(t, []string{"analyst"}, u.GetRoles())
	assert.NotEmpty(t, u.GetInvitationExpiresAt())
	msg := h.mail.last(t, email)
	assert.Contains(t, msg, "Alice", "signed by the inviter")
	first := tokenFrom(t, msg)
	inv, err := identity.FindInvitation(bg, h.pool, first, time.Now())
	require.NoError(t, err)
	assert.True(t, inv.NewIdentity)

	assert.Equal(t, "already_member", errCode(t, func() error {
		_, err := h.client.Admin.InviteUser(ctx, &InviteUserRequest{Email: email})
		return err
	}))
	assert.Equal(t, "already_member", errCode(t, func() error {
		_, err := h.client.Admin.ResendInvitation(ctx, &UserIdRequest{Id: admin.ID.String()})
		return err
	}), "only an invited member gets a link")

	_, err = h.client.Admin.ResendInvitation(ctx, &UserIdRequest{Id: u.GetId()})
	require.NoError(t, err)
	second := tokenFrom(t, h.mail.last(t, email))
	assert.NotEqual(t, first, second)
	_, err = identity.FindInvitation(bg, h.pool, first, time.Now())
	assert.ErrorIs(t, err, identity.ErrInvitationInvalid, "a resend retires the previous link")

	_, err = h.client.Admin.RemoveUser(ctx, &UserIdRequest{Id: u.GetId()})
	require.NoError(t, err)
	_, err = identity.FindInvitation(bg, h.pool, second, time.Now())
	assert.ErrorIs(t, err, identity.ErrInvitationInvalid, "removing the member withdraws the invitation")

	h.drainAudit()

	trail, err := h.client.Admin.ListUserAudit(ctx, &ListUserAuditRequest{Id: u.GetId()})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(trail.GetEvents()), 3, "invite, resend, remove")
}

func TestInviteWithoutEmailReturnsTheLinkAndPolicyAppliesToAdminPasswords(t *testing.T) {
	h := newIdentityHarness(t, false)
	tenant := uuid.New()
	admin := h.member(tenant, "admin@acme.io", "admin")
	ctx := h.as(tenant, admin.ID)
	_, err := h.pool.Exec(context.Background(), `INSERT INTO metadata.tenant_configs (tenant_id, key, value)
		VALUES ($1, 'auth.passwordMinLength', '12')`, tenant)
	require.NoError(t, err)

	res, err := h.client.Admin.InviteUser(ctx, &InviteUserRequest{Email: uuid.NewString() + "@example.com"})
	require.NoError(t, err)
	assert.False(t, res.GetEmailSent())
	require.Contains(t, res.GetAcceptUrl(), "/accept-invite?token=", "no SMTP: the admin passes the link on")

	assert.Equal(t, "weak_password", errCode(t, func() error {
		_, err := h.client.Admin.CreateUser(ctx, &CreateUserRequest{Email: uuid.NewString() + "@example.com", Password: "ten-chars!"})
		return err
	}), "the tenant's 12-character policy")
	_, err = h.client.Admin.CreateUser(ctx, &CreateUserRequest{Email: uuid.NewString() + "@example.com", Password: "twelve-chars"})
	require.NoError(t, err)
}

// Roadmap 115 M6: a permission that belongs to no entity is declared once, granted to a role like
// any other, and enforced by CheckResource.
func TestVirtualResourceGrantsAreValidatedAndEnforced(t *testing.T) {
	permission.DeclareResource(permission.Resource{Name: "test.requestdata", Description: "request data", Actions: []permission.ResourceAction{
		{Name: "view", Description: "see request data"}, {Name: "approve", Description: "approve a change request"}}})
	h := newIdentityHarness(t)
	tenant := uuid.New()
	admin := h.member(tenant, "admin@acme.io", "admin")
	bob := h.member(tenant, "bob@acme.io", "reviewer")
	h.policyRole(tenant, "reviewer")
	ctx := h.as(tenant, admin.ID)
	key := permission.ResourceKey("test.requestdata")

	list, err := h.client.Roles.ListResources(ctx, &Empty{})
	require.NoError(t, err)
	var found *roles.PermissionResource
	for _, r := range list.GetResources() {
		if r.GetName() == "test.requestdata" {
			found = r
		}
	}
	require.NotNil(t, found)
	assert.Equal(t, key, found.GetKey())
	assert.Len(t, found.GetActions(), 2)
	_, err = h.client.Roles.ListResources(h.as(tenant, bob.ID), &Empty{})
	assert.Equal(t, http.StatusForbidden, statusOf(err), "admins only")

	// Only declared actions of declared resources are grantable.
	for _, bad := range []*roles.RoleGrant{
		{Entity: key, Action: "read"}, // an entity action is not one of this resource's
		{Entity: key, Action: "nope"},
		{Entity: permission.ResourceKey("test.ghost"), Action: "view"},
	} {
		bad := bad
		assert.Equal(t, "validation_failed", errCode(t, func() error {
			_, err := h.client.Roles.SetPermissions(ctx, &roles.SetRolePermissionsRequest{Role: "reviewer", Grants: []*roles.RoleGrant{bad}})
			return err
		}), "%+v", bad)
	}
	set, err := h.client.Roles.SetPermissions(ctx, &roles.SetRolePermissionsRequest{Role: "reviewer", Grants: []*roles.RoleGrant{
		{Entity: key, Action: "view"}, {Entity: "waf.zones", Action: "read"}}})
	require.NoError(t, err)
	assert.Len(t, set.GetGrants(), 2)

	// The service honours it, for exactly the granted action.
	perms := permission.NewPermissionService(policypg.NewPostgresPolicyStore(testRouterFor(h.pool)))
	rc := &permission.RequestContext{TenantID: tenant.String(), UserID: bob.ID.String(), Roles: []string{"reviewer"}, HasRoles: true}
	d, err := perms.CheckResource(context.Background(), rc, "test.requestdata", "view")
	require.NoError(t, err)
	assert.True(t, d.Allowed)
	d, err = perms.CheckResource(context.Background(), rc, "test.requestdata", "approve")
	require.NoError(t, err)
	assert.False(t, d.Allowed)
}

// Roadmap 115 M9: the tenant's audit trail is searchable by person/object/time, admin changes made
// through these services land in a verifiable chain, and a forged row is reported.
func TestAuditAdminSearchAndVerify(t *testing.T) {
	h := newIdentityHarness(t)
	tenant := uuid.New()
	admin := h.member(tenant, "admin@acme.io", "admin")
	bob := h.member(tenant, "bob@acme.io", "reviewer")
	ctx := h.as(tenant, admin.ID)

	// Two audited admin changes by the admin.
	_, err := h.client.Admin.DisableUser(ctx, &UserIdRequest{Id: bob.ID.String()})
	require.NoError(t, err)
	_, err = h.client.Admin.EnableUser(ctx, &UserIdRequest{Id: bob.ID.String()})
	require.NoError(t, err)

	h.drainAudit()

	res, err := h.client.Trail.Search(ctx, &trail.SearchAuditRequest{ActorUserId: admin.ID.String()})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(res.GetEntries()), 2)
	for _, e := range res.GetEntries() {
		assert.Equal(t, admin.ID.String(), e.GetActorUserId())
		assert.NotZero(t, e.GetSeq())
		assert.NotEmpty(t, e.GetHash())
	}
	h.drainAudit()
	byObject, err := h.client.Trail.Search(ctx, &trail.SearchAuditRequest{Entity: AuditEntityUsers, RecordId: bob.ID.String()})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(byObject.GetEntries()), 2)
	h.drainAudit()
	none, err := h.client.Trail.Search(ctx, &trail.SearchAuditRequest{ActorUserId: uuid.NewString()})
	require.NoError(t, err)
	assert.Empty(t, none.GetEntries())
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	h.drainAudit()
	late, err := h.client.Trail.Search(ctx, &trail.SearchAuditRequest{From: future})
	require.NoError(t, err)
	assert.Empty(t, late.GetEntries())
	h.drainAudit()
	_, err = h.client.Trail.Search(ctx, &trail.SearchAuditRequest{From: "yesterday"})
	assert.Equal(t, http.StatusBadRequest, statusOf(err))

	h.drainAudit()

	_, err = h.client.Trail.Search(h.as(tenant, bob.ID), &trail.SearchAuditRequest{})
	assert.Equal(t, http.StatusForbidden, statusOf(err), "admins only")
	h.drainAudit()
	_, err = h.client.Trail.Verify(h.as(tenant, bob.ID))
	assert.Equal(t, http.StatusForbidden, statusOf(err))

	h.drainAudit()

	rep, err := h.client.Trail.Verify(ctx)
	require.NoError(t, err)
	assert.True(t, rep.GetOk(), "%+v", rep)
	assert.GreaterOrEqual(t, rep.GetChecked(), int64(2))

	_, err = h.pool.Exec(context.Background(),
		`UPDATE metadata.audit_trail_entries SET diff = '{"x":1}' WHERE tenant_id = $1 AND seq = 1`, tenant)
	require.NoError(t, err)
	h.drainAudit()
	rep, err = h.client.Trail.Verify(ctx)
	require.NoError(t, err)
	assert.False(t, rep.GetOk())
	assert.EqualValues(t, 1, rep.GetBrokenSeq())
}
