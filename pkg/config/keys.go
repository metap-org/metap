package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

// Level is who may write a key — and, for LevelOperator, that nobody may. See Registry's doc
// comment: a key's tier is a property of the key, declared here in Go, never of the caller.
type Level int

const (
	// LevelOperator is deployment-level, env-var only — no API writes it. Keys at this level are
	// still listed in Registry so the write surface can reject them by name.
	LevelOperator Level = iota
	// LevelPlatformGlobal is fleet-wide, written by a platform admin.
	LevelPlatformGlobal
	// LevelTenant is per-tenant, written by that tenant's own admin. Also settable at the
	// platform tier as the fleet default every tenant inherits until it sets its own value.
	LevelTenant
)

// KeyDef is one declared key: its tier, its default, and how to validate a proposed value.
//
// For a Secret key the two value-shaped fields describe different things: Default is the stored
// marker ({}, meaning no credential), while Validate checks the plaintext a caller sends before
// it goes to a secret store.
type KeyDef struct {
	Key      string
	Level    Level
	Default  func() json.RawMessage
	Validate func(json.RawMessage) error
	// Public: whether the unauthenticated public-config surface may return this key.
	Public bool
	// Secret: whether this key's value is a credential, stored in a secret store rather than in
	// tenant_configs. A Secret key can never also be Public.
	Secret bool
}

func jsonNumber(v json.RawMessage) (float64, bool) {
	var n float64
	if err := json.Unmarshal(v, &n); err != nil {
		return 0, false
	}
	return n, true
}

func jsonString(v json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return "", false
	}
	return s, true
}

func boolValue(v json.RawMessage) error {
	var b bool
	if err := json.Unmarshal(v, &b); err != nil {
		return fmt.Errorf("must be true or false")
	}
	return nil
}

// stringList accepts a JSON array of at most maxItems non-empty strings of at most maxLen bytes.
func stringList(maxItems, maxLen int) func(json.RawMessage) error {
	return func(v json.RawMessage) error {
		var list []string
		if err := json.Unmarshal(v, &list); err != nil {
			return fmt.Errorf("must be a list of strings")
		}
		if len(list) > maxItems {
			return fmt.Errorf("at most %d entries", maxItems)
		}
		for _, s := range list {
			if strings.TrimSpace(s) == "" || len(s) > maxLen {
				return fmt.Errorf("every entry must be 1 to %d characters", maxLen)
			}
		}
		return nil
	}
}

func num(n float64) json.RawMessage {
	b, _ := json.Marshal(n)
	return b
}

func str(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

func emptyObjectMarker() json.RawMessage {
	return json.RawMessage(`{}`)
}

// positiveIntIn rejects anything that is not a positive integer within [min, max].
func positiveIntIn(min, max float64) func(json.RawMessage) error {
	return func(v json.RawMessage) error {
		n, ok := jsonNumber(v)
		if !ok {
			return fmt.Errorf("must be a positive integer")
		}
		if n != float64(int64(n)) || n < min || n > max {
			return fmt.Errorf("must be between %v and %v, got %v", min, max, n)
		}
		return nil
	}
}

// hexColor rejects anything that is not a #rgb/#rrggbb hex color — this value is served
// unauthenticated and lands in a CSS custom property in the browser, so accepting arbitrary
// strings would hand anyone who can write a tenant's config a CSS injection.
func hexColor(v json.RawMessage) error {
	s, ok := jsonString(v)
	if !ok {
		return fmt.Errorf("must be a string")
	}
	hex, ok := cutPrefix(s, "#")
	if !ok {
		return fmt.Errorf("must start with '#'")
	}
	if len(hex) != 3 && len(hex) != 6 {
		return fmt.Errorf("must be #rgb or #rrggbb, got %d hex digits", len(hex))
	}
	for _, c := range hex {
		if !isHexDigit(c) {
			return fmt.Errorf("must contain hex digits only")
		}
	}
	return nil
}

func isHexDigit(c rune) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func cutPrefix(s, prefix string) (string, bool) {
	if strings.HasPrefix(s, prefix) {
		return s[len(prefix):], true
	}
	return "", false
}

// logoURL accepts an absolute https:// URL or a site-relative path, and nothing else.
func logoURL(v json.RawMessage) error {
	s, ok := jsonString(v)
	if !ok {
		return fmt.Errorf("must be a string")
	}
	if len(s) > 2048 {
		return fmt.Errorf("must be at most 2048 characters")
	}
	if s == "" {
		return nil
	}
	if strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "//") {
		return nil
	}
	if strings.HasPrefix(s, "https://") && len(s) > len("https://") {
		return nil
	}
	return fmt.Errorf("must be an https:// URL or a site-relative path starting with '/'")
}

// headerCredential validates a credential that will be sent verbatim as an HTTP header value.
// The CR/LF rejection is header-injection prevention, not tidiness.
func headerCredential(v json.RawMessage) error {
	s, ok := jsonString(v)
	if !ok {
		return fmt.Errorf("must be a string")
	}
	if s == "" {
		return fmt.Errorf("must not be empty — clear the key instead to remove the credential")
	}
	if len(s) > 4096 {
		return fmt.Errorf("must be at most 4096 characters")
	}
	for _, c := range s {
		if c == '\t' {
			continue
		}
		if c < ' ' || c > '~' {
			return fmt.Errorf("must contain only printable ASCII (no newlines, no control characters)")
		}
	}
	return nil
}

// displayText is plain single-line text, bounded. Control characters are rejected rather than
// stripped — a silently-altered value is harder to debug than a refused one.
func displayText(maxLen int) func(json.RawMessage) error {
	return func(v json.RawMessage) error {
		s, ok := jsonString(v)
		if !ok {
			return fmt.Errorf("must be a string")
		}
		if len([]rune(s)) > maxLen {
			return fmt.Errorf("must be at most %d characters", maxLen)
		}
		for _, c := range s {
			if unicode.IsControl(c) {
				return fmt.Errorf("must not contain control characters")
			}
		}
		return nil
	}
}

func operatorOnly(json.RawMessage) error {
	return fmt.Errorf("operator-only key")
}

const (
	GraphQLMaxDepth                = "graphql.maxDepth"
	GraphQLMaxComplexity           = "graphql.maxComplexity"
	HTTPRateLimitPerMs             = "http.rateLimitPerMillisecond"
	HTTPRateLimitBurst             = "http.rateLimitBurst"
	HTTPAuthRateLimitPerMs         = "http.authRateLimitPerMillisecond"
	HTTPAuthRateLimitBurst         = "http.authRateLimitBurst"
	AuthSessionTTLSeconds          = "auth.sessionTtlSeconds"
	AuthSessionAbsoluteMaxSeconds  = "auth.sessionAbsoluteMaxSeconds"
	AuthPasswordMinLength          = "auth.passwordMinLength"
	AuthRequireMFA                 = "auth.requireMfa"
	AuthAPIKeyMaxLifetimeDays      = "auth.apiKeyMaxLifetimeDays"
	SignupEnabled                  = "signup.enabled"
	SignupBlockedTenantNames       = "signup.blockedTenantNames"
	CronWebhookAuthorization       = "cron.webhookAuthorization"
	ThemePrimaryColor              = "theme.primaryColor"
	ThemeLogoURL                   = "theme.logoUrl"
	ThemeDisplayName               = "theme.displayName"
	CronWebhookAllowPrivateTargets = "cron.webhookAllowPrivateTargets"
	CronWebhookAllowedHosts        = "cron.webhookAllowedHosts"
	CORSOrigins                    = "http.corsOrigins"
)

// Registry is every key the platform knows. Adding one here is the only way to make it settable.
var Registry = []KeyDef{
	// --- PlatformGlobal: the values that were hard-coded in code until this feature ---
	{Key: GraphQLMaxDepth, Level: LevelPlatformGlobal, Default: func() json.RawMessage { return num(10) },
		Validate: positiveIntIn(3, 50)},
	{Key: GraphQLMaxComplexity, Level: LevelPlatformGlobal, Default: func() json.RawMessage { return num(1000) },
		Validate: positiveIntIn(10, 1_000_000)},
	{Key: HTTPRateLimitPerMs, Level: LevelPlatformGlobal, Default: func() json.RawMessage { return num(200) },
		Validate: positiveIntIn(1, 60_000)},
	{Key: HTTPRateLimitBurst, Level: LevelPlatformGlobal, Default: func() json.RawMessage { return num(300) },
		Validate: positiveIntIn(1, 100_000)},
	// The stricter per-IP bucket for the public routes a guesser would hammer (login, MFA code,
	// signup, invitation, password reset): one token every 3s, burst 20, shared by all of them.
	{Key: HTTPAuthRateLimitPerMs, Level: LevelPlatformGlobal, Default: func() json.RawMessage { return num(3000) },
		Validate: positiveIntIn(1, 3_600_000)},
	{Key: HTTPAuthRateLimitBurst, Level: LevelPlatformGlobal, Default: func() json.RawMessage { return num(20) },
		Validate: positiveIntIn(1, 10_000)},
	// --- Tenant: a fleet default a platform admin sets, that each tenant may override ---
	{Key: AuthSessionTTLSeconds, Level: LevelTenant, Default: func() json.RawMessage { return num(3600) },
		Validate: positiveIntIn(60, 2_592_000)},
	// The shortest password a member of the tenant may choose (roadmap 113 P2). A person in several
	// tenants must satisfy the strictest of them: their password is one, shared by every tenant.
	{Key: AuthPasswordMinLength, Level: LevelTenant, Default: func() json.RawMessage { return num(8) },
		Validate: positiveIntIn(8, 128)},
	// Every member signing in to this tenant must pass a second factor (roadmap 115 M3); one who has
	// none is made to set it up first.
	{Key: AuthRequireMFA, Level: LevelTenant, Default: func() json.RawMessage { return json.RawMessage("false") },
		Validate: boolValue},
	// The longest an API key of this tenant may live (roadmap 115 M11). 0 = no tenant cap (the
	// platform's own 3650-day limit still applies); with a cap set, a key must carry an expiry and
	// it may not exceed the cap.
	{Key: AuthAPIKeyMaxLifetimeDays, Level: LevelTenant, Default: func() json.RawMessage { return num(0) },
		Validate: positiveIntIn(0, 3650)},
	// Anyone may register a tenant of their own through /auth/signup (roadmap 115 M2). Off until an
	// operator decides the deployment is open to the public.
	{Key: SignupEnabled, Level: LevelPlatformGlobal, Default: func() json.RawMessage { return json.RawMessage("false") },
		Validate: boolValue},
	// Tenant names nobody may register through /auth/signup, on top of the reserved list built into
	// the code (brand names, "admin", "support", ...). Compared ignoring case, spaces and punctuation.
	{Key: SignupBlockedTenantNames, Level: LevelPlatformGlobal, Default: func() json.RawMessage { return json.RawMessage("[]") },
		Validate: stringList(200, 100)},
	{Key: AuthSessionAbsoluteMaxSeconds, Level: LevelPlatformGlobal, Default: func() json.RawMessage { return num(86_400) },
		Validate: positiveIntIn(3600, 2_592_000)},
	// --- Tenant + secret ---
	{Key: CronWebhookAuthorization, Level: LevelTenant, Default: emptyObjectMarker, Validate: headerCredential, Secret: true},
	// --- Tenant + public ---
	{Key: ThemePrimaryColor, Level: LevelTenant, Default: func() json.RawMessage { return str("") }, Public: true,
		Validate: func(v json.RawMessage) error {
			if s, ok := jsonString(v); ok && s == "" {
				return nil
			}
			return hexColor(v)
		}},
	{Key: ThemeLogoURL, Level: LevelTenant, Default: func() json.RawMessage { return str("") }, Public: true, Validate: logoURL},
	{Key: ThemeDisplayName, Level: LevelTenant, Default: func() json.RawMessage { return str("") }, Public: true, Validate: displayText(64)},
	// --- Operator: declared only so the write surface can refuse them by name ---
	{Key: CronWebhookAllowPrivateTargets, Level: LevelOperator, Default: func() json.RawMessage { return json.RawMessage("false") }, Validate: operatorOnly},
	{Key: CronWebhookAllowedHosts, Level: LevelOperator, Default: func() json.RawMessage { return json.RawMessage("[]") }, Validate: operatorOnly},
	{Key: CORSOrigins, Level: LevelOperator, Default: func() json.RawMessage { return json.RawMessage("[]") }, Validate: operatorOnly},
}

// Lookup returns the KeyDef for key, or nil if it isn't declared.
func Lookup(key string) *KeyDef {
	for i := range Registry {
		if Registry[i].Key == key {
			return &Registry[i]
		}
	}
	return nil
}
