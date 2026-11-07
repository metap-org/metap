package config

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func snapshotWith(pairs map[string]json.RawMessage) *Snapshot {
	return newSnapshot(pairs)
}

func tenantWith(pairs map[string]json.RawMessage) *TenantSnapshot {
	if pairs == nil {
		pairs = map[string]json.RawMessage{}
	}
	return &TenantSnapshot{overrides: pairs}
}

func TestAnUnsetKeyReadsBackTheDeclaredDefaultNotNull(t *testing.T) {
	empty := snapshotWith(nil)
	got := empty.GetU64(GraphQLMaxDepth)
	require.EqualValues(t, 10, got, "got %d, want 10", got)
	got = empty.GetU64(AuthSessionTTLSeconds)
	require.EqualValues(t, 3600, got, "got %d, want 3600", got)
}

func TestAStoredOverrideWinsOverTheDefault(t *testing.T) {
	snap := snapshotWith(map[string]json.RawMessage{GraphQLMaxDepth: num(25)})
	got := snap.GetU64(GraphQLMaxDepth)
	require.EqualValues(t, 25, got, "got %d, want 25", got)
	got = snap.GetU64(AuthSessionTTLSeconds)
	require.EqualValues(t, 3600, got, "got %d, want 3600 (untouched key keeps its default)", got)
}

func TestANonNumericStoredValueFallsBackInsteadOfZeroingALimit(t *testing.T) {
	snap := snapshotWith(map[string]json.RawMessage{GraphQLMaxDepth: str("not a number")})
	got := snap.GetU64(GraphQLMaxDepth)
	require.EqualValues(t, 10, got, "got %d, want 10", got)
}

func TestThePlatformWritableViewNeverIncludesOperatorKeys(t *testing.T) {
	view := snapshotWith(nil).PlatformWritableView()
	shown := map[string]bool{}
	for _, item := range view {
		shown[item.Def.Key] = true
	}
	require.True(t, shown[GraphQLMaxDepth], "expected GraphQLMaxDepth to be shown")
	for _, operatorKey := range []string{CronWebhookAllowPrivateTargets, CronWebhookAllowedHosts, CORSOrigins} {
		require.False(t, shown[operatorKey], "%s must not be exposed by the platform-admin surface", operatorKey)
	}
	require.True(t, shown[ThemePrimaryColor], "a tenant-tier key is listed here: the platform row is that key's fleet default")
}

func TestEachTierOverridesOnlyTheKeysItStores(t *testing.T) {
	platform := snapshotWith(map[string]json.RawMessage{
		AuthSessionTTLSeconds: num(7200),
		ThemePrimaryColor:     str("#0af"),
	})
	tenant := tenantWith(map[string]json.RawMessage{ThemePrimaryColor: str("#123456")})
	effective := NewEffectiveConfig(platform, tenant)

	var primaryColor string
	_ = json.Unmarshal(effective.Get(ThemePrimaryColor), &primaryColor)
	require.Equal(t, "#123456", primaryColor, "got %q, want tenant override to win", primaryColor)
	got := effective.GetU64(AuthSessionTTLSeconds)
	require.EqualValues(t, 7200, got, "got %d, want the fleet default", got)
	got = effective.GetU64(GraphQLMaxDepth)
	require.EqualValues(t, 10, got, "got %d, want the value declared in Go", got)
}

func TestATenantThatSetNothingReadsTheFleetDefaults(t *testing.T) {
	platform := snapshotWith(map[string]json.RawMessage{AuthSessionTTLSeconds: num(900)})
	effective := NewEffectiveConfig(platform, tenantWith(nil))
	got := effective.GetU64(AuthSessionTTLSeconds)
	require.EqualValues(t, 900, got, "got %d, want 900", got)
	for _, item := range effective.TenantView() {
		require.False(t, item.Overridden, "%s should not be overridden", item.Def.Key)
	}
}

// The public-surface boundary: PublicView is what an unauthenticated caller receives, so it must
// be driven by the registry's own flag — not by tier, and not by anything a request can
// influence.
func TestThePublicViewContainsOnlyKeysDeclaredPublic(t *testing.T) {
	tenant := tenantWith(map[string]json.RawMessage{
		ThemeDisplayName:      str("Acme"),
		AuthSessionTTLSeconds: num(600),
	})
	effective := NewEffectiveConfig(snapshotWith(nil), tenant)
	public := map[string]bool{}
	for _, item := range effective.PublicView() {
		public[item.Key] = true
	}
	require.True(t, public[ThemeDisplayName], "expected ThemeDisplayName in the public view")
	require.False(t, public[AuthSessionTTLSeconds], "a tenant-tier key that is not declared public must never reach the unauthenticated surface")
	for _, def := range Registry {
		require.False(t, !def.Public && public[def.Key], "%s reached the public view without being declared public", def.Key)
	}
}

func TestTheTenantViewListsOnlyTenantWritableKeys(t *testing.T) {
	effective := NewEffectiveConfig(snapshotWith(nil), nil)
	for _, item := range effective.TenantView() {
		require.Equal(t, LevelTenant, item.Def.Level, "%s is not tenant-writable", item.Def.Key)
	}
}
