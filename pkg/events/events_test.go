package events

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTopic(t *testing.T) {
	cases := []struct {
		rk   string
		want Topic
	}{
		{"crm.customers.workflow.transitioned", Topic{Kind: KindTransitioned, Entity: "crm.customers"}},
		{"crm.customers.record.created", Topic{Kind: KindRecord, Entity: "crm.customers", Action: RecordCreated}},
		{"waf.zones.record.updated", Topic{Kind: KindRecord, Entity: "waf.zones", Action: RecordUpdated}},
		{"waf.zones.record.deleted", Topic{Kind: KindRecord, Entity: "waf.zones", Action: RecordDeleted}},
		{"waf.zones.record.exploded", Topic{Kind: KindOther}},
		{".workflow.transitioned", Topic{Kind: KindOther}},
		{"cron.job.due", Topic{Kind: KindOther}},
		{"", Topic{Kind: KindOther}},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, ParseTopic(tc.rk), tc.rk)
	}
}

func TestBuildersRoundTripThroughParse(t *testing.T) {
	assert.Equal(t, Topic{Kind: KindTransitioned, Entity: "a.b"}, ParseTopic(WorkflowTransitioned("a.b")))
	assert.Equal(t, Topic{Kind: KindRecord, Entity: "a.b", Action: RecordDeleted}, ParseTopic(Record("a.b", RecordDeleted)))
	assert.Equal(t, "waf.*.record.*", RecordPattern("waf.*", ""))
}

func TestValidateTopic(t *testing.T) {
	require.NoError(t, ValidateTopic("billing.invoice.paid"))
	for _, bad := range []string{"", "a..b", "a.#", "a.*", "a b", "waf.zones.record.created", "x.workflow.transitioned"} {
		assert.Error(t, ValidateTopic(bad), bad)
	}
}

type recordingTx struct{ args []any }

func (r *recordingTx) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	r.args = args
	return pgconn.CommandTag{}, nil
}

func TestEmitRejectsInvalidAndWritesValidEvents(t *testing.T) {
	tx := &recordingTx{}
	id := uuid.New()
	require.Error(t, Emit(context.Background(), tx, Event{Topic: "a.*", AggregateType: "x", AggregateID: id}))
	require.Error(t, Emit(context.Background(), tx, Event{Topic: "a.b", AggregateType: "", AggregateID: id}))
	assert.Nil(t, tx.args, "nothing is written for a rejected event")

	type paid struct {
		Amount int `json:"amount"`
	}
	require.NoError(t, Emit(context.Background(), tx, Event{Topic: "billing.invoice.paid", AggregateType: "invoice", AggregateID: id, Payload: paid{42}}))
	assert.Equal(t, "billing.invoice.paid", tx.args[0])
	assert.JSONEq(t, `{"amount":42}`, string(tx.args[3].(json.RawMessage)))
}

func TestPlatformPayloadsCarryVersionAndDecode(t *testing.T) {
	tx := &recordingTx{}
	tenant, rec := uuid.New(), uuid.New()
	require.NoError(t, EmitTransitioned(context.Background(), tx, "a.b", Transitioned{TenantID: tenant, RecordID: rec, Action: "go", From: "x", To: "y"}))
	p, err := Decode[Transitioned](tx.args[3].(json.RawMessage))
	require.NoError(t, err)
	assert.Equal(t, PayloadVersion, p.Version)
	assert.Equal(t, "go", p.Action)
	assert.Equal(t, tenant, p.TenantID)

	_, err = Decode[Envelope]([]byte(`[1]`))
	assert.Error(t, err)
}
