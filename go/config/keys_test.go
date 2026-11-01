package config

import (
	"encoding/json"
	"testing"
)

func TestEveryKeyIsDeclaredOnce(t *testing.T) {
	seen := map[string]bool{}
	for _, def := range Registry {
		if seen[def.Key] {
			t.Fatalf("duplicate config key: %s", def.Key)
		}
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
		if err := def.Validate(def.Default()); err != nil {
			t.Fatalf("default for %s fails its own validator: %v", def.Key, err)
		}
	}
}

func TestTheSecurityCriticalKeysAreOperatorLevel(t *testing.T) {
	for _, key := range []string{CronWebhookAllowPrivateTargets, CronWebhookAllowedHosts, CORSOrigins} {
		def := Lookup(key)
		if def == nil || def.Level != LevelOperator {
			t.Fatalf("%s must stay operator-only", key)
		}
	}
}

func TestOnlyTenantTierKeysAreEverPublic(t *testing.T) {
	for _, def := range Registry {
		if def.Public && def.Level != LevelTenant {
			t.Fatalf("%s is served unauthenticated but is not a tenant-tier key", def.Key)
		}
	}
}

func TestNoKeyIsBothSecretAndPublic(t *testing.T) {
	for _, def := range Registry {
		if def.Secret && def.Public {
			t.Fatalf("%s is declared both secret and public", def.Key)
		}
	}
}

func TestASecretKeysDefaultIsTheEmptyMarker(t *testing.T) {
	for _, def := range Registry {
		if !def.Secret {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(def.Default(), &m); err != nil || len(m) != 0 {
			t.Fatalf("%s's default must be the empty marker, got %s", def.Key, def.Default())
		}
	}
}

func TestSecretKeysLiveOnlyAtTheTenantTier(t *testing.T) {
	for _, def := range Registry {
		if def.Secret && def.Level != LevelTenant {
			t.Fatalf("%s is a secret at the wrong tier", def.Key)
		}
	}
}

func TestACredentialWithANewlineIsRefusedAsHeaderInjection(t *testing.T) {
	def := Lookup(CronWebhookAuthorization)
	if err := def.Validate(str("Bearer sk_live_abc123")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, rejected := range []string{"Bearer x\r\nX-Injected: 1", "Bearer x\nX-Injected: 1", "Bearer x\x00y", ""} {
		if err := def.Validate(str(rejected)); err == nil {
			t.Fatalf("%q must not be storable as a header credential", rejected)
		}
	}
	if err := def.Validate(num(42)); err == nil {
		t.Fatal("must be a string")
	}
}

func TestThePublicThemeValidatorsRejectInjectionShapedValues(t *testing.T) {
	color := Lookup(ThemePrimaryColor)
	for _, ok := range []string{"#0af", "#123456", ""} {
		if err := color.Validate(str(ok)); err != nil {
			t.Fatalf("%q should be valid: %v", ok, err)
		}
	}
	for _, rejected := range []string{"red", "rgb(1,2,3)", "#0af; background: url(https://evil.example/x)", "#12345", "#zzzzzz", "var(--anything)"} {
		if err := color.Validate(str(rejected)); err == nil {
			t.Fatalf("%q must not be storable as a theme colour", rejected)
		}
	}

	logo := Lookup(ThemeLogoURL)
	for _, ok := range []string{"https://cdn.example.com/logo.svg", "/assets/logo.svg", ""} {
		if err := logo.Validate(str(ok)); err != nil {
			t.Fatalf("%q should be valid: %v", ok, err)
		}
	}
	for _, rejected := range []string{
		"javascript:alert(1)",
		"data:image/svg+xml;base64,PHN2Zz48c2NyaXB0Pg==",
		"//evil.example/logo.svg",
		"http://cdn.example.com/logo.svg",
		"HTTPS://cdn.example.com/logo.svg",
	} {
		if err := logo.Validate(str(rejected)); err == nil {
			t.Fatalf("%q must not be storable as a logo URL", rejected)
		}
	}

	name := Lookup(ThemeDisplayName)
	if err := name.Validate(str("Acme Corp")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := name.Validate(str("line\nbreak")); err == nil {
		t.Fatal("expected rejection")
	}
	long := make([]byte, 65)
	for i := range long {
		long[i] = 'x'
	}
	if err := name.Validate(str(string(long))); err == nil {
		t.Fatal("expected rejection for overlong name")
	}
	if err := name.Validate(num(42)); err == nil {
		t.Fatal("must be a string")
	}
}

func TestValidatorsRejectZeroAndNonIntegers(t *testing.T) {
	depth := Lookup(GraphQLMaxDepth)
	if err := depth.Validate(num(0)); err == nil {
		t.Fatal("expected rejection")
	}
	if err := depth.Validate(str("ten")); err == nil {
		t.Fatal("expected rejection")
	}
	if err := depth.Validate(num(10)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ttl := Lookup(AuthSessionTTLSeconds)
	if err := ttl.Validate(num(30)); err == nil {
		t.Fatal("expected rejection below the 60s floor")
	}
	if err := ttl.Validate(num(3600)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
