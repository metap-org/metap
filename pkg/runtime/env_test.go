package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The parsing rules themselves are tested in pkg/kit/env; these pin the aliases' contracts.

func TestEnvOrParsesOrFallsBack(t *testing.T) {
	assert.Equal(t, 4000, EnvOr("METAP_TEST_ENVOR_UNSET", 4000))
	t.Setenv("METAP_TEST_ENVOR", "8080")
	assert.Equal(t, 8080, EnvOr("METAP_TEST_ENVOR", 4000))
	t.Setenv("METAP_TEST_ENVOR", "not-a-number")
	assert.Equal(t, 4000, EnvOr("METAP_TEST_ENVOR", 4000))
}

func TestRequireEnvErrorsWhenMissing(t *testing.T) {
	_, err := RequireEnv("METAP_TEST_REQUIRE_UNSET")
	require.Error(t, err)
}

func TestOptionalIsNoneWhenUnsetOrEmpty(t *testing.T) {
	_, ok := Optional("METAP_TEST_OPTIONAL_UNSET")
	assert.False(t, ok)
	t.Setenv("METAP_TEST_OPTIONAL", "")
	_, ok = Optional("METAP_TEST_OPTIONAL")
	assert.False(t, ok)
	t.Setenv("METAP_TEST_OPTIONAL", "value")
	v, ok := Optional("METAP_TEST_OPTIONAL")
	assert.True(t, ok)
	assert.Equal(t, "value", v)
}

func TestFlagEnabledOnlyForTrueOr1(t *testing.T) {
	assert.False(t, FlagEnabled("METAP_TEST_FLAG_UNSET"))
	for v, want := range map[string]bool{"true": true, "1": true, "yes": false, "0": false} {
		t.Setenv("METAP_TEST_FLAG", v)
		assert.Equal(t, want, FlagEnabled("METAP_TEST_FLAG"), v)
	}
}
