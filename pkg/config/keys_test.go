package config

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEveryKeyIsDeclaredOnce(t *testing.T) {
	seen := map[string]bool{}
	for _, def := range Registry {
		require.False(t, seen[def.Key], "duplicate config key: %s", def.Key)
		seen[def.Key] = true
	}
}

// A default that its own validator rejects would mean the platform boots into a state it
// refuses to let anyone set. Operator keys are excluded (their validator rejects everything by
// design); secret keys are excluded because Default is the stored marker, not a credential — see
// TestASecretKeysDefaultIsTheEmptyMarker for the half that matters for those.
func TestEveryWritableDefaultPassesItsOwnValidator(t *testing.T) {
	for _, def := range Registry {
		if def.Level == LevelOperator || def.Secret {
			continue
		}
		require.NoError(t, def.Validate(def.Default()), "default for %s fails its own validator", def.Key)
	}
}

func TestTheSecurityCriticalKeysAreOperatorLevel(t *testing.T) {
	for _, key := range []string{CronWebhookAllowPrivateTargets, CronWebhookAllowedHosts, CORSOrigins} {
		def := Lookup(key)
		require.NotNil(t, def, "%s must stay operator-only", key)
		require.Equal(t, LevelOperator, def.Level, "%s must stay operator-only", key)
	}
}

func TestOnlyTenantTierKeysAreEverPublic(t *testing.T) {
	for _, def := range Registry {
		require.False(t, def.Public && def.Level != LevelTenant, "%s is served unauthenticated but is not a tenant-tier key", def.Key)
	}
}

func TestNoKeyIsBothSecretAndPublic(t *testing.T) {
	for _, def := range Registry {
		require.False(t, def.Secret && def.Public, "%s is declared both secret and public", def.Key)
	}
}

func TestASecretKeysDefaultIsTheEmptyMarker(t *testing.T) {
	for _, def := range Registry {
		if !def.Secret {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(def.Default(), &m); err != nil || len(m) != 0 {
			require.FailNow(t, fmt.Sprintf("%s's default must be the empty marker, got %s", def.Key, def.Default()))
		}
	}
}

func TestSecretKeysLiveOnlyAtTheTenantTier(t *testing.T) {
	for _, def := range Registry {
		require.False(t, def.Secret && def.Level != LevelTenant, "%s is a secret at the wrong tier", def.Key)
	}
}

func TestACredentialWithANewlineIsRefusedAsHeaderInjection(t *testing.T) {
	def := Lookup(CronWebhookAuthorization)
	require.NoError(t, def.Validate(str("Bearer sk_live_abc123")), "unexpected error")
	for _, rejected := range []string{"Bearer x\r\nX-Injected: 1", "Bearer x\nX-Injected: 1", "Bearer x\x00y", ""} {
		err := def.Validate(str(rejected))
		require.Error(t, err, "%q must not be storable as a header credential", rejected)
	}
	err := def.Validate(num(42))
	require.Error(t, err, "must be a string")
}

func TestThePublicThemeValidatorsRejectInjectionShapedValues(t *testing.T) {
	color := Lookup(ThemePrimaryColor)
	for _, ok := range []string{"#0af", "#123456", ""} {
		require.NoError(t, color.Validate(str(ok)), "%q should be valid", ok)
	}
	for _, rejected := range []string{"red", "rgb(1,2,3)", "#0af; background: url(https://evil.example/x)", "#12345", "#zzzzzz", "var(--anything)"} {
		err := color.Validate(str(rejected))
		require.Error(t, err, "%q must not be storable as a theme colour", rejected)
	}

	logo := Lookup(ThemeLogoURL)
	for _, ok := range []string{"https://cdn.example.com/logo.svg", "/assets/logo.svg", ""} {
		require.NoError(t, logo.Validate(str(ok)), "%q should be valid", ok)
	}
	for _, rejected := range []string{
		"javascript:alert(1)",
		"data:image/svg+xml;base64,PHN2Zz48c2NyaXB0Pg==",
		"//evil.example/logo.svg",
		"http://cdn.example.com/logo.svg",
		"HTTPS://cdn.example.com/logo.svg",
	} {
		err := logo.Validate(str(rejected))
		require.Error(t, err, "%q must not be storable as a logo URL", rejected)
	}

	name := Lookup(ThemeDisplayName)
	require.NoError(t, name.Validate(str("Acme Corp")), "unexpected error")
	err := name.Validate(str("line\nbreak"))
	require.Error(t, err, "expected rejection")
	long := make([]byte, 65)
	for i := range long {
		long[i] = 'x'
	}
	err = name.Validate(str(string(long)))
	require.Error(t, err, "expected rejection for overlong name")
	err = name.Validate(num(42))
	require.Error(t, err, "must be a string")
}

func TestValidatorsRejectZeroAndNonIntegers(t *testing.T) {
	depth := Lookup(GraphQLMaxDepth)
	err := depth.Validate(num(0))
	require.Error(t, err, "expected rejection")
	err = depth.Validate(str("ten"))
	require.Error(t, err, "expected rejection")
	require.NoError(t, depth.Validate(num(10)), "unexpected error")
	ttl := Lookup(AuthSessionTTLSeconds)
	err = ttl.Validate(num(30))
	require.Error(t, err, "expected rejection below the 60s floor")
	require.NoError(t, ttl.Validate(num(3600)), "unexpected error")
}
