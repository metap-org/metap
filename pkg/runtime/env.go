// Package runtime holds cross-cutting plumbing for metap's management-plane services (auth
// cookies, service tokens, CORS, metrics, request/trace context, the JSON error shape). The
// general-purpose, standard-library-only parts live in pkg/kit (env, shutdown, httpx) so code
// outside the management plane can use them without importing this package's dependencies
// (chi, prometheus); the functions below are kept as thin aliases for existing callers.
package runtime

import "github.com/metap-org/metap/pkg/kit/env"

// EnvOr parses the named env var as an int, falling back to def if unset or unparseable.
// Equivalent to env.Int.
func EnvOr(name string, def int) int { return env.Int(name, def) }

// RequireEnv returns the named env var, or an error if it is not set. Equivalent to env.Require.
func RequireEnv(name string) (string, error) { return env.Require(name) }

// Optional returns the named env var and true if it is set and non-empty. Equivalent to
// env.Lookup.
func Optional(name string) (string, bool) { return env.Lookup(name) }

// FlagEnabled reports whether the named env var is "true" or "1". Equivalent to env.Bool.
func FlagEnabled(name string) bool { return env.Bool(name) }
