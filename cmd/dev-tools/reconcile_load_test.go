package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "e.json")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

func TestLoadEntityFromFileAcceptsWrappedAndBare(t *testing.T) {
	wrapped, err := loadEntityFromFile(writeTemp(t, `{"data":{"name":"a.b","tableName":"s.t","fields":[]}}`))
	require.NoError(t, err)
	require.Equal(t, "a.b", wrapped.Name)
	bare, err := loadEntityFromFile(writeTemp(t, `{"name":"a.b","tableName":"s.t","fields":[]}`))
	require.NoError(t, err)
	require.Equal(t, "a.b", bare.Name)
}

func TestLoadEntityFromFileRejectsAnEntityWithNoName(t *testing.T) {
	// Used to decode to an empty entity, "migrate" nothing and report a clean verification.
	_, err := loadEntityFromFile(writeTemp(t, `{"foo":1}`))
	require.Error(t, err)
}
