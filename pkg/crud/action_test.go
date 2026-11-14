package crud

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActionRegistry(t *testing.T) {
	reg := NewActionRegistry()
	id := uuid.New()
	require.NoError(t, reg.Register("shop.orders", "ship", func(_ context.Context, c ActionCall) (JsonObject, error) {
		return ToObject(map[string]any{"shipped": c.ID.String(), "entity": c.Entity}, nil)
	}))
	assert.Error(t, reg.Register("shop.orders", "ship", nil), "duplicate registration")
	assert.True(t, reg.Has("shop.orders", "ship"))
	assert.False(t, reg.Has("shop.orders", "cancel"))

	out, err := reg.Invoke(context.Background(), ActionCall{Entity: "shop.orders", Name: "ship", ID: id})
	require.NoError(t, err)
	assert.JSONEq(t, `"`+id.String()+`"`, string(out["shipped"]))

	_, err = reg.Invoke(context.Background(), ActionCall{Entity: "shop.orders", Name: "cancel"})
	se, ok := AsServiceError(err)
	require.True(t, ok)
	assert.EqualValues(t, 404, se.Status)
	assert.Equal(t, "action_not_found", se.Code)
}
