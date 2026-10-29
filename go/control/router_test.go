package control

// Ported from the Rust metap-control crate's src/router.rs test module.

import "testing"

func TestValidateSchemaNameAcceptsWhitelistedForms(t *testing.T) {
	if err := ValidateSchemaName("public"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSchemaName("t_ab12"); err != nil {
		t.Fatal(err)
	}
}

func TestValidateSchemaNameRejectsEverythingElse(t *testing.T) {
	cases := []string{"Tenant_AB", "t_ab;DROP TABLE x--", "", `public'; select 1--`, "t_"}
	for _, c := range cases {
		if err := ValidateSchemaName(c); err == nil {
			t.Fatalf("expected %q to be rejected", c)
		}
	}
}
