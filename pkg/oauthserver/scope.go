package oauthserver

import "strings"

// ScopeTokens splits a space-separated scope string (RFC 6749 §3.3) into its tokens — the wire
// format every scope column/claim/query-param in this package uses. Kept as a plain
// string/[]string rather than a newtype for the same reason the Rust original stays a bare
// &str/String: every caller either splits once to check subset/membership or passes the string
// straight through unopened (storage, the JWT claim).
func ScopeTokens(scope string) []string {
	return strings.Fields(scope)
}

// ScopeIsSubset reports whether every token in requested also appears in allowed — the one rule
// the authorize endpoint and the refresh_token grant's optional narrower scope param both apply.
// Order-independent, duplicate-tolerant.
func ScopeIsSubset(requested, allowed string) bool {
	allowedTokens := ScopeTokens(allowed)
	allowedSet := make(map[string]bool, len(allowedTokens))
	for _, t := range allowedTokens {
		allowedSet[t] = true
	}
	for _, t := range ScopeTokens(requested) {
		if !allowedSet[t] {
			return false
		}
	}
	return true
}
