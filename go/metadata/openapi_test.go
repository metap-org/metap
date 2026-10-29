package metadata

import "testing"

func TestOnlyTheStaticMetadataPathsAreGenerated(t *testing.T) {
	doc := GenerateOpenAPIDocument()
	paths := doc["paths"].(map[string]any)
	if _, ok := paths["/metadata/entities"]; !ok {
		t.Fatal("expected /metadata/entities")
	}
	if _, ok := paths["/metadata/entities/{entity}"]; !ok {
		t.Fatal("expected /metadata/entities/{entity}")
	}
	if _, ok := paths["/metadata/actions"]; !ok {
		t.Fatal("expected /metadata/actions")
	}
	if len(paths) != 3 {
		t.Fatalf("got %d paths, want 3 (no per-entity /api/{entity}* CRUD path)", len(paths))
	}
}
