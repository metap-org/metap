package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/kit/env"
)

const sample = `
database:
  url: postgres://file/db
smtp:
  host: mail.example.com
  port: 587
cors_origins: [https://a.example.com, https://b.example.com]
allow_private_targets: true
empty:
`

func TestParseNamesEachKeyAfterTheVariableItReplaces(t *testing.T) {
	got, err := config.ParseFile([]byte(sample))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"DATABASE_URL":          "postgres://file/db",
		"SMTP_HOST":             "mail.example.com",
		"SMTP_PORT":             "587",
		"CORS_ORIGINS":          "https://a.example.com,https://b.example.com",
		"ALLOW_PRIVATE_TARGETS": "true",
	}, got)
}

func TestParseRefusesWhatIsNotAPlainValue(t *testing.T) {
	_, err := config.ParseFile([]byte("a:\n  - {b: 1}\n"))
	assert.Error(t, err)
}

func TestLoadFeedsEveryEnvReaderAndTheEnvironmentStillWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.yaml")
	require.NoError(t, os.WriteFile(path, []byte(sample), 0o600))
	t.Cleanup(func() { env.SetFallback(nil) })
	require.NoError(t, config.LoadFile(path))

	assert.Equal(t, "mail.example.com", env.String("SMTP_HOST", "default"))
	assert.Equal(t, 587, env.Int("SMTP_PORT", 25))
	assert.Equal(t, []string{"https://a.example.com", "https://b.example.com"}, env.List("CORS_ORIGINS"))
	assert.True(t, env.Bool("ALLOW_PRIVATE_TARGETS"))

	t.Setenv("SMTP_HOST", "from-env")
	assert.Equal(t, "from-env", env.String("SMTP_HOST", "default"))
}

func TestLoadFailsOnAMissingOrBrokenFile(t *testing.T) {
	assert.Error(t, config.LoadFile(filepath.Join(t.TempDir(), "absent.yaml")))
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	require.NoError(t, os.WriteFile(bad, []byte("a: [unclosed"), 0o600))
	assert.Error(t, config.LoadFile(bad))
}
