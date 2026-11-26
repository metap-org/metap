// Package settings is the tiered platform configuration (package config) as an administrator sees it,
// as an administrative slice. A platform admin reads and sets the platform-writable keys (fleet-wide,
// and the default a tenant may override); a tenant admin sets its own Tenant-tier overrides; any
// member reads the effective values. The tenant is always the caller's own, never a request field.
// types.go, rules.go (the audit diff, level names, error mapping, the wire items), store.go (the
// port over config.Store) with its adapter, and this file.
package settings

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/access"
	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
)

// TenantScoper says which tenant a request acts in (permission.PermissionService does).
type TenantScoper interface {
	ScopedTenant(rc *permission.RequestContext) (uuid.UUID, error)
}

type Service struct {
	store Store
	scope TenantScoper
	deps  kernel.Deps
}

// New is the service over store; deps carry the audit sink and store, and the clock.
func New(store Store, scope TenantScoper, deps kernel.Deps) *Service {
	return &Service{store: store, scope: scope, deps: deps}
}

// change runs fn (the write) and records it: atomically with a sink, on the shared database for the
// platform's keys and on the tenant's own for a tenant's overrides, else write-then-record.
func (s *Service) change(ctx context.Context, c kernel.Caller, entity, key string, shared bool,
	fn func(ctx context.Context) (before, after json.RawMessage, err error)) error {
	body := func(ctx context.Context) (map[string][2]any, error) {
		before, after, err := fn(ctx)
		if err != nil {
			return nil, err
		}
		return diff(key, before, after), nil
	}
	id := kernel.NamedID("config", key)
	if shared {
		return s.deps.Change(ctx, c, entity, id, audit.ActionUpdate, body)
	}
	return s.deps.ChangeInTenant(ctx, c, entity, id, audit.ActionUpdate, body)
}

// ListPlatform is every key a platform admin may write, with its fleet-wide value.
func (s *Service) ListPlatform(ctx context.Context) (*PlatformConfigList, error) {
	if _, err := kernel.At(ctx, access.PlatformAdmin); err != nil {
		return nil, err
	}
	return &PlatformConfigList{Data: platformItems(s.store.PlatformView())}, nil
}

func (s *Service) SetPlatform(ctx context.Context, req *SetConfigRequest) (*PlatformConfigResult, error) {
	c, err := s.platformCaller(ctx)
	if err != nil {
		return nil, err
	}
	err = s.change(ctx, c, AuditEntityPlatform, req.Key, true, func(ctx context.Context) (json.RawMessage, json.RawMessage, error) {
		before := s.store.PlatformValue(req.Key)
		if err := s.store.SetPlatform(ctx, req.Key, req.Value); err != nil {
			return nil, nil, mapErr(err)
		}
		return before, req.Value, nil
	})
	if err != nil {
		return nil, err
	}
	return &PlatformConfigResult{Data: &PlatformConfigSet{Key: req.Key, Value: req.Value, AppliesImmediately: appliesImmediately(req.Key)}}, nil
}

// ResetPlatform clears an override so the key falls back to its declared default: distinct from
// setting it to the default's current value, which would pin it against a later change.
func (s *Service) ResetPlatform(ctx context.Context, req *ConfigKeyRequest) (*PlatformConfigResult, error) {
	c, err := s.platformCaller(ctx)
	if err != nil {
		return nil, err
	}
	var now json.RawMessage
	err = s.change(ctx, c, AuditEntityPlatform, req.Key, true, func(ctx context.Context) (json.RawMessage, json.RawMessage, error) {
		before := s.store.PlatformValue(req.Key)
		if err := s.store.ResetPlatform(ctx, req.Key); err != nil {
			return nil, nil, mapErr(err)
		}
		// The snapshot is swapped when the change commits, so what the key reads back after the reset is
		// its declared default (a Reset has no other outcome for a platform key).
		if def := config.Lookup(req.Key); def != nil && def.Default != nil {
			now = def.Default()
		}
		return before, now, nil
	})
	if err != nil {
		return nil, err
	}
	return &PlatformConfigResult{Data: &PlatformConfigSet{Key: req.Key, Value: now, AppliesImmediately: appliesImmediately(req.Key)}}, nil
}

// ListTenant is open to any member: every key shapes what the app looks like or how long a session
// lasts for the person reading it. Writing is admin-only.
func (s *Service) ListTenant(ctx context.Context) (*TenantConfigList, error) {
	rc, err := kernel.Principal(ctx)
	if err != nil {
		return nil, err
	}
	tenant, err := s.scope.ScopedTenant(rc)
	if err != nil {
		return nil, err
	}
	return &TenantConfigList{Data: tenantItems(s.store.TenantView(ctx, tenant))}, nil
}

func (s *Service) SetTenant(ctx context.Context, req *SetConfigRequest) (*TenantConfigResult, error) {
	c, tenant, err := s.tenantAdmin(ctx)
	if err != nil {
		return nil, err
	}
	err = s.change(ctx, c, AuditEntityTenant, req.Key, false, func(ctx context.Context) (json.RawMessage, json.RawMessage, error) {
		before := s.store.TenantValue(ctx, tenant, req.Key)
		if err := s.store.SetTenant(ctx, tenant, req.Key, req.Value); err != nil {
			return nil, nil, mapErr(err)
		}
		return before, req.Value, nil
	})
	if err != nil {
		return nil, err
	}
	return &TenantConfigResult{Data: &TenantConfigSet{Key: req.Key, Value: req.Value, Overridden: true}}, nil
}

// ResetTenant clears this tenant's override. The key then reads back the fleet default a platform
// admin set, or the value declared in Go: never null.
func (s *Service) ResetTenant(ctx context.Context, req *ConfigKeyRequest) (*TenantConfigResult, error) {
	c, tenant, err := s.tenantAdmin(ctx)
	if err != nil {
		return nil, err
	}
	var now json.RawMessage
	err = s.change(ctx, c, AuditEntityTenant, req.Key, false, func(ctx context.Context) (json.RawMessage, json.RawMessage, error) {
		before := s.store.TenantValue(ctx, tenant, req.Key)
		if err := s.store.ResetTenant(ctx, tenant, req.Key); err != nil {
			return nil, nil, mapErr(err)
		}
		now = s.store.DefaultFor(req.Key)
		return before, now, nil
	})
	if err != nil {
		return nil, err
	}
	return &TenantConfigResult{Data: &TenantConfigSet{Key: req.Key, Value: now, Overridden: false}}, nil
}

// platformCaller is a platform admin, as the audit caller.
func (s *Service) platformCaller(ctx context.Context) (kernel.Caller, error) {
	rc, err := s.checked(ctx, access.PlatformAdmin)
	if err != nil {
		return kernel.Caller{}, err
	}
	return kernel.CallerOf(rc)
}

// tenantAdmin is a tenant admin, as the audit caller, with the tenant the permission service scopes
// the request to.
func (s *Service) tenantAdmin(ctx context.Context) (kernel.Caller, uuid.UUID, error) {
	rc, err := s.checked(ctx, access.Admin)
	if err != nil {
		return kernel.Caller{}, uuid.Nil, err
	}
	tenant, err := s.scope.ScopedTenant(rc)
	if err != nil {
		return kernel.Caller{}, uuid.Nil, err
	}
	c, err := kernel.CallerOf(rc)
	return c, tenant, err
}

func (s *Service) checked(ctx context.Context, level access.Level) (*permission.RequestContext, error) {
	rc, err := kernel.Principal(ctx)
	if err != nil {
		return nil, err
	}
	if why := access.Refusal(level, rc); why != "" {
		return nil, kernel.Err(http.StatusForbidden, errcode.Forbidden, why)
	}
	return rc, nil
}
