package metadata

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuditParentMustBeAReferenceWithRefEntity(t *testing.T) {
	entity := EntityDefinition{
		Name: "t.rules", Label: "Rule", TableName: "t.rules",
		Fields: []EntityField{RefField("zoneId", "Zone", "t.zones", "hostname"), NewField("note", "Note", KindString)},
		Audit:  &EntityAuditConfig{Enabled: true, Parent: "zoneId"},
	}
	require.NoError(t, Validate(entity))
	assert.Equal(t, "t.zones", entity.AuditParentEntity())

	entity.Audit.Parent = "note"
	err := Validate(entity)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "audit.parent")
	assert.Equal(t, "", entity.AuditParentEntity())
}
