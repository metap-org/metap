package eventbus

import "testing"

func TestExactMatch(t *testing.T) {
	if !topicMatches("crm.customers.record.created", "crm.customers.record.created") {
		t.Fatal("expected match")
	}
	if topicMatches("crm.customers.record.created", "crm.customers.record.updated") {
		t.Fatal("expected no match")
	}
}

func TestStarMatchesExactlyOneWord(t *testing.T) {
	if !topicMatches("crm.customers.workflow.*", "crm.customers.workflow.transitioned") {
		t.Fatal("expected match")
	}
	if topicMatches("crm.customers.workflow.*", "crm.customers.workflow.transitioned.extra") {
		t.Fatal("expected no match")
	}
	if topicMatches("crm.customers.workflow.*", "crm.customers.workflow") {
		t.Fatal("expected no match")
	}
}

func TestHashMatchesZeroOrMoreWords(t *testing.T) {
	if !topicMatches("#", "crm.customers.record.created") {
		t.Fatal("expected match")
	}
	if !topicMatches("#.workflow.transitioned", "crm.customers.workflow.transitioned") {
		t.Fatal("expected match")
	}
	if !topicMatches("#.workflow.transitioned", "workflow.transitioned") {
		t.Fatal("expected match (# matches zero words too)")
	}
	if topicMatches("#.workflow.transitioned", "crm.customers.workflow.updated") {
		t.Fatal("expected no match")
	}
	if !topicMatches("crm.customers.#", "crm.customers.record.created") {
		t.Fatal("expected match")
	}
	if !topicMatches("crm.customers.#", "crm.customers") {
		t.Fatal("expected match")
	}
}

func TestNoMatchOnPrefixOrSuffixOnly(t *testing.T) {
	if topicMatches("crm.customers", "crm.customers.record.created") {
		t.Fatal("expected no match")
	}
	if topicMatches("crm.customers.record.created", "crm.customers") {
		t.Fatal("expected no match")
	}
}
