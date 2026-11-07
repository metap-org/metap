package eventbus

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExactMatch(t *testing.T) {
	require.True(t, topicMatches("crm.customers.record.created", "crm.customers.record.created"), "expected match")
	require.False(t, topicMatches("crm.customers.record.created", "crm.customers.record.updated"), "expected no match")
}

func TestStarMatchesExactlyOneWord(t *testing.T) {
	require.True(t, topicMatches("crm.customers.workflow.*", "crm.customers.workflow.transitioned"), "expected match")
	require.False(t, topicMatches("crm.customers.workflow.*", "crm.customers.workflow.transitioned.extra"), "expected no match")
	require.False(t, topicMatches("crm.customers.workflow.*", "crm.customers.workflow"), "expected no match")
}

func TestHashMatchesZeroOrMoreWords(t *testing.T) {
	require.True(t, topicMatches("#", "crm.customers.record.created"), "expected match")
	require.True(t, topicMatches("#.workflow.transitioned", "crm.customers.workflow.transitioned"), "expected match")
	require.True(t, topicMatches("#.workflow.transitioned", "workflow.transitioned"), "expected match (# matches zero words too)")
	require.False(t, topicMatches("#.workflow.transitioned", "crm.customers.workflow.updated"), "expected no match")
	require.True(t, topicMatches("crm.customers.#", "crm.customers.record.created"), "expected match")
	require.True(t, topicMatches("crm.customers.#", "crm.customers"), "expected match")
}

func TestNoMatchOnPrefixOrSuffixOnly(t *testing.T) {
	require.False(t, topicMatches("crm.customers", "crm.customers.record.created"), "expected no match")
	require.False(t, topicMatches("crm.customers.record.created", "crm.customers"), "expected no match")
}
