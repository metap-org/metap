package peripherals

import "testing"

func TestIndexNameReplacesDotsAndUsesTheRightPrefix(t *testing.T) {
	if got := buildIndexName("crm.customers", "email", "idx"); got != "idx_records_crm_customers_email" {
		t.Fatalf("got %q", got)
	}
	if got := buildIndexName("crm.customers", "code", "uniq"); got != "uniq_records_crm_customers_code" {
		t.Fatalf("got %q", got)
	}
	if got := buildIndexName("crm.customers", "name", "gin"); got != "gin_records_crm_customers_name" {
		t.Fatalf("got %q", got)
	}
	if got := buildIndexName("crm.customers", "name", "trgm"); got != "trgm_records_crm_customers_name" {
		t.Fatalf("got %q", got)
	}
}

func TestQuoteLiteralEscapesSingleQuotes(t *testing.T) {
	if got := quoteLiteral("o'brien"); got != "'o''brien'" {
		t.Fatalf("got %q", got)
	}
}

func TestQuoteIdentifierEscapesDoubleQuotes(t *testing.T) {
	if got := quoteIdentifier(`weird"name`); got != `"weird""name"` {
		t.Fatalf("got %q", got)
	}
}
