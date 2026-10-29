// Package runtime holds cross-cutting plumbing shared across metap-go binaries — ported from
// metap's Rust metap-runtime crate. Not a grab-bag: every helper here exists because the Rust
// original found the same boilerplate duplicated at >= 2 call sites.
package runtime

import (
	"fmt"
	"os"
	"strconv"
)

// EnvOr parses the named env var as an int, falling back to def if unset or unparseable.
func EnvOr(name string, def int) int {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return def
	}
	return parseOr(raw, def)
}

func parseOr(raw string, def int) int {
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}

// RequireEnv reads a required env var, erroring if it is unset.
func RequireEnv(name string) (string, error) {
	v, ok := os.LookupEnv(name)
	if !ok {
		return "", fmt.Errorf("%s is required", name)
	}
	return v, nil
}

// Optional reads an env var, treating a set-but-empty value as unset.
func Optional(name string) (string, bool) {
	return nonEmpty(os.Getenv(name))
}

func nonEmpty(raw string) (string, bool) {
	if raw == "" {
		return "", false
	}
	return raw, true
}

// FlagEnabled reports whether the named env var is exactly "true" or "1" — any other value
// (including "false", "TRUE", "yes") is off.
func FlagEnabled(name string) bool {
	return isTruthy(os.Getenv(name))
}

func isTruthy(raw string) bool {
	return raw == "true" || raw == "1"
}
