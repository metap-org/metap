package settings

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/kit/errcode"
)

// Audit entities configuration changes record under: a tenant's own overrides, and the platform's
// fleet-wide values.
const (
	AuditEntityTenant   = kernel.AuditEntityConfig
	AuditEntityPlatform = kernel.AuditEntityPlatformConfig
)

// diff is what is audited for one key: before and after, a credential's value never. Pure.
func diff(key string, before, after json.RawMessage) map[string][2]any {
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
	return map[string][2]any{key: {b, a}}
}

func levelName(level config.Level) string {
	switch level {
	case config.LevelOperator:
		return "operator"
	case config.LevelPlatformGlobal:
		return "platformGlobal"
	default:
		return "tenant"
	}
}

// mapErr maps a *config.Error onto the platform error envelope; anything else is returned as it is.
func mapErr(err error) error {
	var ce *config.Error
	if !errors.As(err, &ce) {
		return err
	}
	switch ce.Kind {
	case config.ErrUnknownKey:
		// 404, not 400: an unknown key is an addressing mistake, and the route is keyed by path.
		return kernel.Err(http.StatusNotFound, "unknown_config_key", "No config key named \""+ce.Key+"\".")
	case config.ErrNotWritable:
		// 403 rather than 400: the value may well have been fine; the caller may not set this key
		// at all, and saying so plainly is what makes the tier boundary legible.
		return kernel.Err(http.StatusForbidden, "config_key_not_writable", "Config key \""+ce.Key+"\" cannot be set here: "+ce.Reason)
	case config.ErrInvalid:
		return kernel.Err(http.StatusUnprocessableEntity, errcode.InvalidConfigValue, "Invalid value for \""+ce.Key+"\": "+ce.Reason)
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

func platformItems(view []PlatformEntry) []*PlatformConfigItem {
	items := make([]*PlatformConfigItem, len(view))
	for i, e := range view {
		items[i] = &PlatformConfigItem{Key: e.Def.Key, Value: json.RawMessage(e.Value), Level: levelName(e.Def.Level),
			// A platform admin setting a Tenant-tier key sets a default, not a decision: the value a
			// tenant sees may be its own.
			TenantOverridable: e.Def.Level == config.LevelTenant}
	}
	return items
}

func tenantItems(view []TenantEntry) []*TenantConfigItem {
	items := make([]*TenantConfigItem, len(view))
	for i, e := range view {
		items[i] = &TenantConfigItem{Key: e.Def.Key, Value: json.RawMessage(e.Value), Level: levelName(e.Def.Level),
			// Tells "we chose this" from "the platform chose this for us": without it a tenant admin
			// cannot tell whether clearing the key would change anything.
			Overridden: e.Overridden, Public: e.Def.Public,
			// A secret's value is never sent (value is only a marker): shown as write-only.
			Secret: e.Def.Secret}
	}
	return items
}
