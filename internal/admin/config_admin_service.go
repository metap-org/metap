// ConfigAdminService: the tiered platform configuration (package config) as an administrator sees
// it. A platform admin reads and sets the platform-writable keys (fleet-wide, and the default a
// tenant may override); a tenant admin sets its own Tenant-tier overrides; any member reads the
// effective values. The tenant is always the caller's own, never a request field.
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/tenancy"
)

// EffectiveConfigFunc is the cache-first read of a tenant's effective configuration.
type EffectiveConfigFunc func(ctx context.Context, tenant uuid.UUID) *config.EffectiveConfig

// AuditEntityConfig and AuditEntityPlatformConfig are the audit-trail entities configuration
// changes record under: a tenant's own overrides, and the platform's fleet-wide values.
const (
	AuditEntityConfig         = "metadata.tenant_config"
	AuditEntityPlatformConfig = "metadata.platform_config"
)

type ConfigAdminService struct {
	store     *config.Store
	router    *tenancy.Router
	perms     *permission.PermissionService
	effective EffectiveConfigFunc
	audit     audit.Store
}

// NewConfigAdminService serves configuration from store; auditStore (may be nil) is where changes
// are audited.
func NewConfigAdminService(store *config.Store, router *tenancy.Router, perms *permission.PermissionService, effective EffectiveConfigFunc, auditStore audit.Store) *ConfigAdminService {
	return &ConfigAdminService{store: store, router: router, perms: perms, effective: effective, audit: auditStore}
}

// platformValue and tenantValue read a key's current value for an audit entry. A key the registry
// does not know has none (Get panics on it): the change is refused for that reason a moment later,
// and a refused change is not audited.
func (s *ConfigAdminService) platformValue(key string) json.RawMessage {
	if config.Lookup(key) == nil {
		return nil
	}
	return s.store.Current().Get(key)
}

func (s *ConfigAdminService) tenantValue(ctx context.Context, tenant uuid.UUID, key string) json.RawMessage {
	if config.Lookup(key) == nil {
		return nil
	}
	return s.effective(ctx, tenant).Get(key)
}

// recordConfig audits one configuration change. A key whose value is a credential is audited as
// changed, never with its value.
func (s *ConfigAdminService) recordConfig(ctx context.Context, rc *permission.RequestContext, entity, key string, before, after json.RawMessage) {
	tenant, err := uuid.Parse(rc.TenantID)
	if err != nil {
		return
	}
	var b, a any = json.RawMessage(before), json.RawMessage(after)
	if len(before) == 0 {
		b = nil
	}
	if len(after) == 0 {
		a = nil
	}
	if def := config.Lookup(key); def != nil && def.Secret {
		b, a = "(secret)", "(secret)"
	}
	actor, _ := uuid.Parse(rc.UserID)
	writeAdminAudit(ctx, s.audit, tenant, actor, entity, namedID("config", key), audit.ActionUpdate,
		map[string][2]any{key: {b, a}}, time.Now())
}

func configLevelName(level config.Level) string {
	switch level {
	case config.LevelOperator:
		return "operator"
	case config.LevelPlatformGlobal:
		return "platformGlobal"
	default:
		return "tenant"
	}
}

// configErr maps a *config.Error onto the platform error envelope.
func configErr(ctx context.Context, err error) error {
	var ce *config.Error
	if !errors.As(err, &ce) {
		return err
	}
	switch ce.Kind {
	case config.ErrUnknownKey:
		// 404, not 400: an unknown key is an addressing mistake, and the route is keyed by path.
		return svcErr(ctx, http.StatusNotFound, "unknown_config_key", "No config key named \""+ce.Key+"\".")
	case config.ErrNotWritable:
		// 403 rather than 400: the value may well have been fine; the caller may not set this key
		// at all, and saying so plainly is what makes the tier boundary legible.
		return svcErr(ctx, http.StatusForbidden, "config_key_not_writable", "Config key \""+ce.Key+"\" cannot be set here: "+ce.Reason)
	case config.ErrInvalid:
		return svcErr(ctx, http.StatusUnprocessableEntity, errcode.InvalidConfigValue, "Invalid value for \""+ce.Key+"\": "+ce.Reason)
	}
	return ce.Cause
}

// appliesImmediately reports whether a change takes effect without a restart (false for the
// rate-limit keys, which are baked into a middleware layer at router-build time).
func appliesImmediately(key string) bool {
	switch key {
	case config.HTTPRateLimitPerMs, config.HTTPRateLimitBurst, config.HTTPAuthRateLimitPerMs, config.HTTPAuthRateLimitBurst:
		return false
	}
	return true
}

func (s *ConfigAdminService) ListPlatformConfig(ctx context.Context, _ *Empty) (*PlatformConfigList, error) {
	if _, err := platformAdminCaller(ctx); err != nil {
		return nil, err
	}
	view := s.store.Current().PlatformWritableView()
	items := make([]*PlatformConfigItem, len(view))
	for i, e := range view {
		items[i] = &PlatformConfigItem{Key: e.Def.Key, Value: json.RawMessage(e.Value), Level: configLevelName(e.Def.Level),
			// A platform admin setting a Tenant-tier key sets a default, not a decision: the value a
			// tenant sees may be its own.
			TenantOverridable: e.Def.Level == config.LevelTenant}
	}
	return &PlatformConfigList{Data: items}, nil
}

func (s *ConfigAdminService) SetPlatformConfig(ctx context.Context, req *SetConfigRequest) (*PlatformConfigResult, error) {
	rc, err := platformAdminCaller(ctx)
	if err != nil {
		return nil, err
	}
	before := s.platformValue(req.Key)
	if err := s.store.SetPlatformGlobal(ctx, req.Key, req.Value); err != nil {
		return nil, configErr(ctx, err)
	}
	s.recordConfig(ctx, rc, AuditEntityPlatformConfig, req.Key, before, req.Value)
	return &PlatformConfigResult{Data: &PlatformConfigSet{Key: req.Key, Value: req.Value, AppliesImmediately: appliesImmediately(req.Key)}}, nil
}

// ResetPlatformConfig clears an override so the key falls back to its declared default: distinct
// from setting it to the default's current value, which would pin it against a later change.
func (s *ConfigAdminService) ResetPlatformConfig(ctx context.Context, req *ConfigKeyRequest) (*PlatformConfigResult, error) {
	rc, err := platformAdminCaller(ctx)
	if err != nil {
		return nil, err
	}
	before := s.platformValue(req.Key)
	if err := s.store.ResetPlatformGlobal(ctx, req.Key); err != nil {
		return nil, configErr(ctx, err)
	}
	now := s.platformValue(req.Key)
	s.recordConfig(ctx, rc, AuditEntityPlatformConfig, req.Key, before, now)
	return &PlatformConfigResult{Data: &PlatformConfigSet{Key: req.Key, Value: now, AppliesImmediately: appliesImmediately(req.Key)}}, nil
}

// ListTenantConfig is open to any member: every key shapes what the app looks like or how long a
// session lasts for the person reading it. Writing is admin-only.
func (s *ConfigAdminService) ListTenantConfig(ctx context.Context, _ *Empty) (*TenantConfigList, error) {
	rc, err := authenticatedCaller(ctx)
	if err != nil {
		return nil, err
	}
	tenant, err := s.perms.ScopedTenant(rc)
	if err != nil {
		return nil, err
	}
	view := s.effective(ctx, tenant).TenantView()
	items := make([]*TenantConfigItem, len(view))
	for i, e := range view {
		items[i] = &TenantConfigItem{Key: e.Def.Key, Value: json.RawMessage(e.Value), Level: configLevelName(e.Def.Level),
			// Tells "we chose this" from "the platform chose this for us": without it a tenant admin
			// cannot tell whether clearing the key would change anything.
			Overridden: e.Overridden, Public: e.Def.Public,
			// A secret's value is never sent (value is only a marker): shown as write-only.
			Secret: e.Def.Secret}
	}
	return &TenantConfigList{Data: items}, nil
}

func (s *ConfigAdminService) SetTenantConfig(ctx context.Context, req *SetConfigRequest) (*TenantConfigResult, error) {
	rc, err := adminCaller(ctx)
	if err != nil {
		return nil, err
	}
	tenant, err := s.perms.ScopedTenant(rc)
	if err != nil {
		return nil, err
	}
	before := s.tenantValue(ctx, tenant, req.Key)
	tx, err := s.router.Begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	if err := s.store.SetTenant(ctx, tx, tenant, req.Key, req.Value); err != nil {
		_ = tx.Rollback(ctx)
		return nil, configErr(ctx, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	s.recordConfig(ctx, rc, AuditEntityConfig, req.Key, before, req.Value)
	return &TenantConfigResult{Data: &TenantConfigSet{Key: req.Key, Value: req.Value, Overridden: true}}, nil
}

// ResetTenantConfig clears this tenant's override. The key then reads back the fleet default a
// platform admin set, or the value declared in Go: never null.
func (s *ConfigAdminService) ResetTenantConfig(ctx context.Context, req *ConfigKeyRequest) (*TenantConfigResult, error) {
	rc, err := adminCaller(ctx)
	if err != nil {
		return nil, err
	}
	tenant, err := s.perms.ScopedTenant(rc)
	if err != nil {
		return nil, err
	}
	before := s.tenantValue(ctx, tenant, req.Key)
	tx, err := s.router.Begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	if err := s.store.ResetTenant(ctx, tx, tenant, req.Key); err != nil {
		_ = tx.Rollback(ctx)
		return nil, configErr(ctx, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	now := s.effective(ctx, tenant).Get(req.Key)
	s.recordConfig(ctx, rc, AuditEntityConfig, req.Key, before, now)
	return &TenantConfigResult{Data: &TenantConfigSet{Key: req.Key, Value: now, Overridden: false}}, nil
}
