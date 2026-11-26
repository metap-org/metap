package admin

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/permission"
)

// capturedAudit keeps what is recorded; everything else on the Store is not used here.
type capturedAudit struct {
	audit.Store
	entries []audit.Entry
}

func (c *capturedAudit) Record(_ context.Context, _ uuid.UUID, e audit.Entry) error {
	c.entries = append(c.entries, e)
	return nil
}

func TestANamedIDIsStableAndDependsOnItsKind(t *testing.T) {
	require.Equal(t, namedID("config", "auth.passwordMinLength"), namedID("config", "auth.passwordMinLength"))
	require.NotEqual(t, namedID("config", "x"), namedID("policies", "x"))
	require.NotEqual(t, namedID("config", "x"), namedID("config", "y"))
}

func TestANilAuditStoreRecordsNothingAndDoesNotFail(t *testing.T) {
	writeAdminAudit(context.Background(), nil, uuid.New(), uuid.New(), "metadata.x", uuid.New(), audit.ActionUpdate,
		map[string][2]any{"a": {1, 2}}, testNow)
}

func TestAnAdminChangeIsRecordedWithItsActorAndBeforeAfter(t *testing.T) {
	store := &capturedAudit{}
	tenant, actor, id := uuid.New(), uuid.New(), uuid.New()
	writeAdminAudit(context.Background(), store, tenant, actor, "metadata.policies", id, audit.ActionCreate,
		map[string][2]any{"entity": {nil, "waf.zones"}}, testNow)

	require.Len(t, store.entries, 1)
	e := store.entries[0]
	require.Equal(t, tenant, e.TenantID)
	require.Equal(t, id, e.RecordID)
	require.Equal(t, "metadata.policies", e.Entity)
	require.Equal(t, audit.ActionCreate, e.Action)
	require.NotNil(t, e.ActorUserID)
	require.Equal(t, actor, *e.ActorUserID)
	require.JSONEq(t, `{"before":null,"after":"waf.zones"}`, string(e.Diff["entity"]))
}

func TestBasicGrantsListsOnlyWhatTheMatrixOwns(t *testing.T) {
	rows := []permission.PolicyRow{
		{Action: "read", Roles: []string{"viewer"}, HasRoles: true, Subject: "context", Effect: permission.EffectAllow},
		{Action: "update", HasRoles: false, Subject: "context", Effect: permission.EffectAllow},
		{Action: "delete", Roles: []string{"viewer"}, HasRoles: true, Subject: "context", Effect: permission.EffectDeny},                  // deny: advanced
		{Action: "read", Roles: []string{"analyst"}, HasRoles: true, Subject: "record", Effect: permission.EffectAllow},                   // record subject: advanced
		{Action: "read", Roles: []string{"analyst"}, HasRoles: true, Subject: "context", Field: "secret", Effect: permission.EffectAllow}, // field: advanced
	}
	require.Equal(t, []string{"*:update", "viewer:read"}, basicGrants(rows))
}

var testNow = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
