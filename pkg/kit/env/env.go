// Package env reads configuration from environment variables. A variable that is set but empty
// counts as unset everywhere here except Require, matching how a blank line in a .env file or a
// compose `FOO:` entry is meant to read.
package env

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Lookup returns the variable's value and true when it is set to a non-empty string.
func Lookup(name string) (string, bool) {
	v := os.Getenv(name)
	return v, v != ""
}

// Ptr returns a pointer to the variable's value, or nil when it is unset or empty — for optional
// settings carried as *string.
func Ptr(name string) *string {
	if v, ok := Lookup(name); ok {
		return &v
	}
	return nil
}

// String returns the variable's value, or def when it is unset or empty.
func String(name, def string) string {
	if v, ok := Lookup(name); ok {
		return v
	}
	return def
}

// Int returns the variable parsed as an int, or def when it is unset, empty or unparseable.
func Int(name string, def int) int {
	if v, ok := Lookup(name); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// Bool reports whether the variable is "true" or "1".
func Bool(name string) bool {
	v := os.Getenv(name)
	return v == "true" || v == "1"
}

// Seconds reads a whole number of seconds (e.g. REFRESH_INTERVAL_SECONDS=10), or def when unset,
// empty, negative or unparseable.
func Seconds(name string, def time.Duration) time.Duration {
	return units(name, def, time.Second)
}

// Millis reads a whole number of milliseconds, like Seconds.
func Millis(name string, def time.Duration) time.Duration {
	return units(name, def, time.Millisecond)
}

// Duration reads a Go duration string (e.g. "1m30s"), or def when unset, empty or unparseable.
func Duration(name string, def time.Duration) time.Duration {
	if v, ok := Lookup(name); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// Require returns the variable's value, or an error when it is not set at all. A set-but-empty
// variable is returned as "" without error.
func Require(name string) (string, error) {
	v, ok := os.LookupEnv(name)
	if !ok {
		return "", fmt.Errorf("%s is required", name)
	}
	return v, nil
}

func units(name string, def, unit time.Duration) time.Duration {
	if v, ok := Lookup(name); ok {
		if n, err := strconv.ParseUint(v, 10, 63); err == nil {
			return time.Duration(n) * unit
		}
	}
	return def
}
