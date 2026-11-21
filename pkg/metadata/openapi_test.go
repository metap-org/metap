package metadata

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMetadataAndGenericEntityPathsAreGenerated(t *testing.T) {
	doc := GenerateOpenAPIDocument()
	paths := doc["paths"].(map[string]any)
	_, ok := paths["/metadata/entities"]
	require.True(t, ok, "expected /metadata/entities")
	_, ok = paths["/metadata/entities/{entity}"]
	require.True(t, ok, "expected /metadata/entities/{entity}")
	_, ok = paths["/metadata/actions"]
	require.True(t, ok, "expected /metadata/actions")
	for _, p := range []string{
		"/api/{entity}", "/api/{entity}/{id}", "/api/{entity}/aggregate", "/api/{entity}/{id}/transitions/{action}",
		"/api/{entity}/{id}/actions/{action}", "/api/{entity}/actions/{action}",
	} {
		_, ok = paths[p]
		require.True(t, ok, "expected %s", p)
	}
	require.Len(t, paths, 9, "3 metadata paths + 6 generic entity routes; never a per-entity path")

	var names []string
	for _, raw := range paths["/api/{entity}"].(map[string]any)["get"].(map[string]any)["parameters"].([]any) {
		names = append(names, raw.(map[string]any)["name"].(string))
	}
	require.Subset(t, names, []string{"limit", "sort", "cursor", "offset", "search", "listView", "jql", "ids", "fields", "expand"})
}
