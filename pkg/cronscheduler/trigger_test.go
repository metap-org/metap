package cronscheduler

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassifyTopicTransitioned(t *testing.T) {
	got := classifyTopic("crm.customers.workflow.transitioned")
	assert.False(t, got.kind != topicTransitioned || got.entity != "crm.customers", "got %+v", got)
}

func TestClassifyTopicRecordEvent(t *testing.T) {
	for _, tc := range []struct {
		routingKey, entity, event string
	}{
		{"crm.customers.record.created", "crm.customers", "created"},
		{"crm.customers.record.updated", "crm.customers", "updated"},
		{"crm.customers.record.deleted", "crm.customers", "deleted"},
	} {
		got := classifyTopic(tc.routingKey)
		assert.False(t, got.kind != topicRecordEvent || got.entity != tc.entity || got.event != tc.event, "classifyTopic(%q) = %+v, want entity=%q event=%q", tc.routingKey, got, tc.entity, tc.event)
	}
}

func TestClassifyTopicNoneForUnrelatedRoutingKeys(t *testing.T) {
	for _, rk := range []string{"cron.job.due", "something.else", ""} {
		got := classifyTopic(rk)
		assert.Equal(t, topicNone, got.kind, "classifyTopic(%q) = %+v, want topicNone", rk, got)
	}
}
