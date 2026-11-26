//go:build integration

package admin

import (
	"context"
	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/internal/admin/settings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/permission/policypg"
	"github.com/metap-org/metap/pkg/tenancy"
)

// Who may do what, and how the tenant is configured, are the changes an admin makes that matter
// most; each leaves an entry in the tenant's audit trail.
func TestPolicyAndConfigChangesAreAudited(t *testing.T) {
	h := newIdentityHarness(t)
	tenant := uuid.New()
	boss := h.member(tenant, "boss@example.com", "admin")
	viewer := h.member(tenant, "viewer@example.com", "viewer")
	asAdmin := h.as(tenant, boss.ID)
	ctx := context.Background()

	perms := permission.NewPermissionService(policypg.NewPostgresPolicyStore(h.cfg.Router))
	policies := NewPolicyAdminService(perms, nil, h.cfg.Audit).WithSink(h.cfg.Sink)

	trail := func(entity string) []audit.Row {
		h.drainAudit()
		rows, err := h.cfg.Audit.Search(ctx, tenant, audit.SearchQuery{Entity: entity, Limit: audit.MaxPageSize})
		require.NoError(t, err)
		return rows
	}
	byEntity := func(entity, action string) []audit.Row {
		var out []audit.Row
		for _, r := range trail(entity) {
			if r.Action == action {
				out = append(out, r)
			}
		}
		return out
	}

	// A non-admin changes nothing, and nothing is recorded for the attempt.
	_, err := policies.CreatePolicy(h.as(tenant, viewer.ID), &CreatePolicyRequest{Entity: "test.things", Action: "read", Roles: []string{"viewer"}})
	require.Error(t, err)
	assert.Empty(t, trail(AuditEntityPolicies))

	// Create.
	created, err := policies.CreatePolicy(asAdmin, &CreatePolicyRequest{Entity: "test.things", Action: "read", Roles: []string{"viewer"}})
	require.NoError(t, err)
	rows := byEntity(AuditEntityPolicies, "create")
	require.Len(t, rows, 1)
	assert.Equal(t, created.Data.ID, rows[0].RecordID.String())
	assert.Equal(t, boss.ID, *rows[0].ActorUserID, "attributed to the admin who made it")
	assert.Contains(t, string(rows[0].Diff), "test.things")
	assert.Contains(t, string(rows[0].Diff), "viewer")

	// Seed defaults: one entry per policy created.
	seeded, err := policies.SeedDefaultPolicies(asAdmin, &SeedDefaultPoliciesRequest{Entity: "test.other", Roles: []string{"analyst"}, Actions: []string{"read", "update"}})
	require.NoError(t, err)
	assert.Len(t, seeded.Data, 2)
	assert.Len(t, byEntity(AuditEntityPolicies, "create"), 3, "the one above and the two seeded")

	// The matrix save: one entry saying what the entity's grants were and are.
	analyst := "analyst"
	_, err = policies.SyncMatrixPolicies(asAdmin, &SyncMatrixRequest{Entity: "test.other", Grants: []MatrixGrant{{Role: &analyst, Action: "delete"}}})
	require.NoError(t, err)
	synced := byEntity(AuditEntityPolicies, "update")
	require.Len(t, synced, 1)
	assert.Contains(t, string(synced[0].Diff), "analyst:read", "before")
	assert.Contains(t, string(synced[0].Diff), "analyst:delete", "after")

	// Delete: the entry says what was removed.
	_, err = policies.DeletePolicy(asAdmin, &PolicyIDRequest{ID: created.Data.ID})
	require.NoError(t, err)
	gone := byEntity(AuditEntityPolicies, "delete")
	require.Len(t, gone, 1)
	assert.Equal(t, created.Data.ID, gone[0].RecordID.String())
	assert.Contains(t, string(gone[0].Diff), "test.things")

	// Tenant configuration: a set and a reset each leave an entry with the old and new value.
	store := config.Unloaded(h.pool)
	require.NoError(t, store.Reload(ctx))
	effective := func(ctx context.Context, tenant uuid.UUID) *config.EffectiveConfig {
		tx, err := h.cfg.Router.Begin(ctx, tenant)
		require.NoError(t, err)
		defer tx.Rollback(ctx) //nolint:errcheck
		snap, err := store.LoadTenant(ctx, tx, tenant)
		require.NoError(t, err)
		return store.Effective(snap)
	}
	cfgSvc := settings.New(settings.NewPgStore(store, h.cfg.Router, settings.EffectiveConfigFunc(effective)), perms, kernel.Deps{Cfg: kernel.Config{Audit: h.cfg.Audit}})
	const key = "auth.passwordMinLength"
	_, err = cfgSvc.SetTenant(asAdmin, &settings.SetConfigRequest{Key: key, Value: []byte(`12`)})
	require.NoError(t, err)
	_, err = cfgSvc.ResetTenant(asAdmin, &settings.ConfigKeyRequest{Key: key})
	require.NoError(t, err)
	changes := trail(settings.AuditEntityTenant)
	require.Len(t, changes, 2, "the set and the reset")
	for _, c := range changes {
		assert.Equal(t, namedID("config", key), c.RecordID)
		assert.Contains(t, string(c.Diff), "before")
	}
}

// With a Sink a configuration change and its audit entry are one transaction, and the cached value
// moves only when the change commits: a tenant's override on the tenant's database, a platform key on
// the shared one.
func TestConfigChangesAreAtomicWithTheirAuditEntry(t *testing.T) {
	h := newIdentityHarness(t)
	tenant := uuid.New()
	boss := h.member(tenant, "boss@example.com", "admin")
	platform := h.member(tenancy.PlatformTenantID, "root@example.com", RolePlatformAdmin)
	ctx := context.Background()
	perms := permission.NewPermissionService(policypg.NewPostgresPolicyStore(h.cfg.Router))

	store := config.Unloaded(h.pool)
	require.NoError(t, store.Reload(ctx))
	effective := func(ctx context.Context, tenant uuid.UUID) *config.EffectiveConfig {
		tx, err := h.cfg.Router.Begin(ctx, tenant)
		require.NoError(t, err)
		defer tx.Rollback(ctx) //nolint:errcheck
		snap, err := store.LoadTenant(ctx, tx, tenant)
		require.NoError(t, err)
		return store.Effective(snap)
	}
	svc := settings.New(settings.NewPgStore(store, h.cfg.Router, settings.EffectiveConfigFunc(effective)), perms, kernel.Deps{Cfg: kernel.Config{Audit: h.cfg.Audit, Sink: h.cfg.Sink}})
	trail := func(entity string) []audit.Row {
		h.drainAudit()
		rows, err := h.cfg.Audit.Search(ctx, tenant, audit.SearchQuery{Entity: entity, Limit: audit.MaxPageSize})
		require.NoError(t, err)
		return rows
	}
	const key = "auth.passwordMinLength"

	_, err := svc.SetTenant(h.as(tenant, boss.ID), &settings.SetConfigRequest{Key: key, Value: []byte(`12`)})
	require.NoError(t, err)
	require.Len(t, trail(settings.AuditEntityTenant), 1)
	require.JSONEq(t, `12`, string(effective(ctx, tenant).Get(key)))

	// A refused change writes no entry and changes nothing.
	_, err = svc.SetTenant(h.as(tenant, boss.ID), &settings.SetConfigRequest{Key: key, Value: []byte(`"not a number"`)})
	require.Error(t, err)
	require.Len(t, trail(settings.AuditEntityTenant), 1)
	require.JSONEq(t, `12`, string(effective(ctx, tenant).Get(key)))

	_, err = svc.ResetTenant(h.as(tenant, boss.ID), &settings.ConfigKeyRequest{Key: key})
	require.NoError(t, err)
	require.Len(t, trail(settings.AuditEntityTenant), 2)
	require.NotEqual(t, `12`, string(effective(ctx, tenant).Get(key)), "the override is gone")

	// A platform key: the snapshot moves when the change commits, with its entry.
	asRoot := h.as(tenancy.PlatformTenantID, platform.ID)
	_, err = svc.SetPlatform(asRoot, &settings.SetConfigRequest{Key: config.HTTPRateLimitBurst, Value: []byte(`321`)})
	require.NoError(t, err)
	require.JSONEq(t, `321`, string(store.Current().Get(config.HTTPRateLimitBurst)))
	h.drainAudit()
	rows, err := h.cfg.Audit.Search(ctx, tenancy.PlatformTenantID, audit.SearchQuery{Entity: settings.AuditEntityPlatform, Limit: audit.MaxPageSize})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	_, err = svc.ResetPlatform(asRoot, &settings.ConfigKeyRequest{Key: config.HTTPRateLimitBurst})
	require.NoError(t, err)
	require.NotEqual(t, `321`, string(store.Current().Get(config.HTTPRateLimitBurst)))
}
