package crud

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/metap-org/metap/pkg/metadata"
)

func rawJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestStringArrayAllowed(t *testing.T) {
	multi := metadata.NewField("roles", "Roles", metadata.KindJSON).WithWidget(metadata.WidgetMultiSelect)
	multi.EnumValues = []string{"a", "b"}
	assert.True(t, stringArrayAllowed(rawJSON([]string{"a", "b"}), multi))
	assert.False(t, stringArrayAllowed(rawJSON([]string{"a", "z"}), multi), "an item outside enumValues is refused")
	assert.False(t, stringArrayAllowed(rawJSON("a"), multi), "not an array")

	tags := metadata.NewField("tags", "Tags", metadata.KindJSON).WithWidget(metadata.WidgetTags)
	assert.True(t, stringArrayAllowed(rawJSON([]string{"anything", "goes"}), tags))
}
