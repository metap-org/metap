package attachments

import "testing"

func TestValidateTableNameAcceptsNormalIdentifiers(t *testing.T) {
	if err := validateTableName("attachments"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := validateTableName("jira_issue_attachments"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateTableNameRejectsInjectionShapedInput(t *testing.T) {
	for _, bad := range []string{"attachments; DROP TABLE users;--", "Attachments", "", "1attachments"} {
		if validateTableName(bad) == nil {
			t.Fatalf("expected %q to be rejected", bad)
		}
	}
}
