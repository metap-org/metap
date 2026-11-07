package env_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/kit/env"
)

func TestStringTreatsUnsetAndEmptyAsDefault(t *testing.T) {
	assert.Equal(t, "def", env.String("KIT_TEST_UNSET", "def"))
	t.Setenv("KIT_TEST_EMPTY", "")
	assert.Equal(t, "def", env.String("KIT_TEST_EMPTY", "def"))
	t.Setenv("KIT_TEST_SET", "value")
	assert.Equal(t, "value", env.String("KIT_TEST_SET", "def"))
}

func TestPtrIsNilUnlessNonEmpty(t *testing.T) {
	assert.Nil(t, env.Ptr("KIT_TEST_UNSET"))
	t.Setenv("KIT_TEST_EMPTY", "")
	assert.Nil(t, env.Ptr("KIT_TEST_EMPTY"))
	t.Setenv("KIT_TEST_SET", "x")
	require.NotNil(t, env.Ptr("KIT_TEST_SET"))
	assert.Equal(t, "x", *env.Ptr("KIT_TEST_SET"))
}

func TestIntFallsBackOnUnsetOrUnparseable(t *testing.T) {
	assert.Equal(t, 4000, env.Int("KIT_TEST_UNSET", 4000))
	t.Setenv("KIT_TEST_INT", "8080")
	assert.Equal(t, 8080, env.Int("KIT_TEST_INT", 4000))
	t.Setenv("KIT_TEST_INT", "not-a-number")
	assert.Equal(t, 4000, env.Int("KIT_TEST_INT", 4000))
}

func TestBoolAcceptsOnlyTrueAnd1(t *testing.T) {
	assert.False(t, env.Bool("KIT_TEST_UNSET"))
	for _, v := range []string{"true", "1"} {
		t.Setenv("KIT_TEST_BOOL", v)
		assert.True(t, env.Bool("KIT_TEST_BOOL"), v)
	}
	for _, v := range []string{"TRUE", "yes", "0", "false", ""} {
		t.Setenv("KIT_TEST_BOOL", v)
		assert.False(t, env.Bool("KIT_TEST_BOOL"), v)
	}
}

func TestSecondsAndMillisParseWholeUnits(t *testing.T) {
	assert.Equal(t, 10*time.Second, env.Seconds("KIT_TEST_UNSET", 10*time.Second))
	t.Setenv("KIT_TEST_DUR", "30")
	assert.Equal(t, 30*time.Second, env.Seconds("KIT_TEST_DUR", time.Second))
	assert.Equal(t, 30*time.Millisecond, env.Millis("KIT_TEST_DUR", time.Second))
	t.Setenv("KIT_TEST_DUR", "-5")
	assert.Equal(t, time.Second, env.Seconds("KIT_TEST_DUR", time.Second))
}

func TestDurationParsesGoDurationStrings(t *testing.T) {
	t.Setenv("KIT_TEST_DUR", "1m30s")
	assert.Equal(t, 90*time.Second, env.Duration("KIT_TEST_DUR", time.Second))
	t.Setenv("KIT_TEST_DUR", "90")
	assert.Equal(t, time.Second, env.Duration("KIT_TEST_DUR", time.Second))
}

func TestRequireErrorsOnlyWhenUnset(t *testing.T) {
	_, err := env.Require("KIT_TEST_UNSET")
	require.ErrorContains(t, err, "KIT_TEST_UNSET is required")
	t.Setenv("KIT_TEST_EMPTY", "")
	v, err := env.Require("KIT_TEST_EMPTY")
	require.NoError(t, err)
	assert.Empty(t, v)
}
