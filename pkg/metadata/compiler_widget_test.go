package metadata

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func widgetEntity(f EntityField) EntityDefinition {
	return EntityDefinition{Name: "t.w", Label: "W", TableName: "t.w", Fields: []EntityField{f}}
}

func TestWidgetMustMatchKind(t *testing.T) {
	ok := NewField("on", "On", KindBoolean).WithWidget(WidgetSwitch)
	err := Validate(widgetEntity(ok))
	require.NoError(t, err)

	bad := NewField("d", "D", KindDate).WithWidget(WidgetSwitch)
	err = Validate(widgetEntity(bad))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support")

	noValues := NewField("tags", "T", KindJSON).WithWidget(WidgetMultiSelect)
	err = Validate(widgetEntity(noValues))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "enumValues")

	free := NewField("tags", "T", KindJSON).WithWidget(WidgetTags)
	err = Validate(widgetEntity(free))
	require.NoError(t, err)
}
