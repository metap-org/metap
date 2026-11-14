package control

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateSchemaNameAcceptsWhitelistedForms(t *testing.T) {
	require.NoError(t, ValidateSchemaName("public"))
	require.NoError(t, ValidateSchemaName("t_ab12"))
}

func TestValidateSchemaNameRejectsEverythingElse(t *testing.T) {
	cases := []string{"Tenant_AB", "t_ab;DROP TABLE x--", "", `public'; select 1--`, "t_"}
	for _, c := range cases {
		err := ValidateSchemaName(c)
		require.Error(t, err, "expected %q to be rejected", c)
	}
}
