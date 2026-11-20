package graphqlgateway

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/crud"
)

// The message is the human text only (the code is in extensions.code), and a ServiceError's
// Details reach the client as extensions.details.
func TestGqlErrorMessageAndDetails(t *testing.T) {
	err := gqlError(&crud.ServiceError{Status: 422, Code: "guard_failed", Message: "The domain is not verified yet.",
		Details: map[string]any{"field": "verificationStatus"}})
	assert.Equal(t, "The domain is not verified yet.", err.Error())

	ext := err.(*serviceError).Extensions()
	assert.Equal(t, "guard_failed", ext["code"])
	require.Contains(t, ext, "details")
	assert.Equal(t, "verificationStatus", ext["details"].(map[string]any)["field"])

	plain := gqlError(&crud.ServiceError{Status: 404, Code: "not_found"})
	assert.Equal(t, "not_found", plain.Error(), "with no message the code is the message")
	assert.NotContains(t, plain.(*serviceError).Extensions(), "details")
}
