package runtime

import "testing"

func TestEnvOrFallsBackToDefaultWhenUnset(t *testing.T) {
	if got := EnvOr("METAP_GO_TEST_UNSET_VAR", 4000); got != 4000 {
		t.Fatalf("got %d, want 4000", got)
	}
}

func TestEnvOrParsesSetValue(t *testing.T) {
	if got := parseOr("8080", 4000); got != 8080 {
		t.Fatalf("got %d, want 8080", got)
	}
}

func TestEnvOrFallsBackOnUnparseableValue(t *testing.T) {
	if got := parseOr("not-a-number", 4000); got != 4000 {
		t.Fatalf("got %d, want 4000", got)
	}
}

func TestRequireEnvErrorsWhenMissing(t *testing.T) {
	if _, err := RequireEnv("METAP_GO_TEST_REQUIRED_UNSET"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestOptionalNoneWhenUnset(t *testing.T) {
	if _, ok := Optional("METAP_GO_TEST_OPTIONAL_UNSET"); ok {
		t.Fatal("expected not ok")
	}
}

func TestOptionalNoneWhenEmpty(t *testing.T) {
	if _, ok := nonEmpty(""); ok {
		t.Fatal("expected not ok for empty string")
	}
}

func TestOptionalSomeWhenSet(t *testing.T) {
	v, ok := nonEmpty("value")
	if !ok || v != "value" {
		t.Fatalf("got (%q, %v), want (\"value\", true)", v, ok)
	}
}

func TestFlagEnabledFalseWhenUnset(t *testing.T) {
	if FlagEnabled("METAP_GO_TEST_FLAG_UNSET") {
		t.Fatal("expected false")
	}
}

func TestFlagEnabledTrueForTrueOr1(t *testing.T) {
	if !isTruthy("true") || !isTruthy("1") {
		t.Fatal("expected true for \"true\" and \"1\"")
	}
}

func TestFlagEnabledFalseForOtherValues(t *testing.T) {
	for _, v := range []string{"false", "", "TRUE", "yes"} {
		if isTruthy(v) {
			t.Fatalf("expected false for %q", v)
		}
	}
}
