package metadata

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFieldModifiersReturnCopies(t *testing.T) {
	base := NewField("title", "Title", KindString)
	req := base.Require().Index().Sort()
	assert.Nil(t, base.Required, "modifier must not mutate the receiver")
	require.NotNil(t, req.Required)
	assert.True(t, *req.Required && *req.Indexed && *req.Sortable)
	assert.Nil(t, req.Optional().Required)
}

func TestEnumAndRefFieldCarryTheirSpecifics(t *testing.T) {
	e := EnumField("s", "S", "a", "b")
	assert.Equal(t, KindEnum, e.Kind)
	assert.Equal(t, []string{"a", "b"}, e.EnumValues)
	r := RefField("zoneId", "Zone", "waf.zones", "hostname")
	assert.Equal(t, "waf.zones", *r.RefEntity)
	assert.Equal(t, "hostname", *r.RefDisplayField)
	assert.Equal(t, UniqueScopeGlobal, *NewField("h", "H", KindString).UniqGlobal().UniqueScope)
}

func TestDefinitionsPutReferencedEntitiesFirst(t *testing.T) {
	mk := func(name string, refs ...string) EntityDefinition {
		e := regEntity(name, []EntityField{NewField("name", "name", KindString)})
		for _, r := range refs {
			e.Fields = append(e.Fields, RefField(strings.ReplaceAll(r, ".", "_")+"Id", r, r, "name"))
		}
		return e
	}
	reg := NewMetadataRegistry()
	for _, e := range []EntityDefinition{mk("test.c", "test.b"), mk("test.b", "test.a"), mk("test.a")} {
		require.NoError(t, reg.Register(e))
	}
	var names []string
	for _, e := range reg.Definitions() {
		names = append(names, e.Name)
	}
	assert.Equal(t, []string{"test.a", "test.b", "test.c"}, names)
}
