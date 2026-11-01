package cronscheduler

import "testing"

func TestClassifyTopicTransitioned(t *testing.T) {
	got := classifyTopic("crm.customers.workflow.transitioned")
	if got.kind != topicTransitioned || got.entity != "crm.customers" {
		t.Errorf("got %+v", got)
	}
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
		if got.kind != topicRecordEvent || got.entity != tc.entity || got.event != tc.event {
			t.Errorf("classifyTopic(%q) = %+v, want entity=%q event=%q", tc.routingKey, got, tc.entity, tc.event)
		}
	}
}

func TestClassifyTopicNoneForUnrelatedRoutingKeys(t *testing.T) {
	for _, rk := range []string{"cron.job.due", "something.else", ""} {
		if got := classifyTopic(rk); got.kind != topicNone {
			t.Errorf("classifyTopic(%q) = %+v, want topicNone", rk, got)
		}
	}
}
