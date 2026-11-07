package metadata

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOnlyTheStaticMetadataPathsAreGenerated(t *testing.T) {
	doc := GenerateOpenAPIDocument()
	paths := doc["paths"].(map[string]any)
	_, ok := paths["/metadata/entities"]
	require.True(t, ok, "expected /metadata/entities")
	_, ok = paths["/metadata/entities/{entity}"]
	require.True(t, ok, "expected /metadata/entities/{entity}")
	_, ok = paths["/metadata/actions"]
	require.True(t, ok, "expected /metadata/actions")
	require.Len(t, paths, 3, "got %d paths, want 3 (no per-entity /api/{entity}* CRUD path)", len(paths))
}
