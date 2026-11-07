package attachments

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateTableNameAcceptsNormalIdentifiers(t *testing.T) {
	require.NoError(t, validateTableName("attachments"), "unexpected error")
	require.NoError(t, validateTableName("jira_issue_attachments"), "unexpected error")
}

func TestValidateTableNameRejectsInjectionShapedInput(t *testing.T) {
	for _, bad := range []string{"attachments; DROP TABLE users;--", "Attachments", "", "1attachments"} {
		require.Error(t, validateTableName(bad), "expected %q to be rejected", bad)
	}
}
