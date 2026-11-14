// Package env reads configuration from environment variables. A variable that is set but empty
// counts as unset everywhere here, Require included, matching how a blank line in a .env file or a
// compose `FOO:` entry is meant to read. A value that is set but cannot be parsed falls back to the
// default and logs a warning naming the variable, so a typo is visible instead of silently ignored.
package env

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
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
		n, err := strconv.Atoi(v)
		if err == nil {
			return n
		}
		warnUnparseable(name, v, "an integer")
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
		d, err := time.ParseDuration(v)
		if err == nil {
			return d
		}
		warnUnparseable(name, v, "a duration such as 30s")
	}
	return def
}

// List splits a comma-separated variable into trimmed, non-empty items; nil when unset or empty.
func List(name string) []string {
	v, ok := Lookup(name)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// Require returns the variable's value, or an error when it is unset or empty.
func Require(name string) (string, error) {
	v, ok := Lookup(name)
	if !ok {
		return "", fmt.Errorf("%s is required", name)
	}
	return v, nil
}

func units(name string, def, unit time.Duration) time.Duration {
	if v, ok := Lookup(name); ok {
		n, err := strconv.ParseUint(v, 10, 63)
		if err == nil {
			return time.Duration(n) * unit
		}
		warnUnparseable(name, v, "a non-negative whole number")
	}
	return def
}

func warnUnparseable(name, value, want string) {
	slog.Warn("ignoring environment variable: value is not "+want+", using the default",
		"variable", name, "value", value)
}
