package roles

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/metap-org/metap/pkg/crud"
)

// "grants": [null] decodes to a nil element; it is a bad request like any other grant with nothing in it.
func TestANullGrantIsRefusedNotAPanic(t *testing.T) {
	var err error
	assert.NotPanics(t, func() { _, err = checkGrants("editor", []*RoleGrant{nil}) })
	se, ok := crud.AsServiceError(err)
	if assert.True(t, ok, "got %v", err) {
		assert.EqualValues(t, 400, se.Status)
	}
}
