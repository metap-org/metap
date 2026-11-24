package admin

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/config"
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

func TestAConfigChangeNeverRecordsTheValueOfASecretKey(t *testing.T) {
	store := &capturedAudit{}
	s := &ConfigAdminService{audit: store}
	tenant := uuid.New()
	rc := &permission.RequestContext{TenantID: tenant.String(), UserID: uuid.NewString()}

	require.NotNil(t, config.Lookup(config.CronWebhookAuthorization))
	require.True(t, config.Lookup(config.CronWebhookAuthorization).Secret, "the premise: this key is a credential")
	s.recordConfig(context.Background(), rc, AuditEntityConfig, config.CronWebhookAuthorization,
		json.RawMessage(`"Bearer old-secret"`), json.RawMessage(`"Bearer new-secret"`))
	require.Len(t, store.entries, 1)
	raw := string(store.entries[0].Diff[config.CronWebhookAuthorization])
	require.NotContains(t, raw, "old-secret")
	require.NotContains(t, raw, "new-secret")
	require.Contains(t, raw, "(secret)", "it says the key changed, not to what")
}

func TestAConfigChangeRecordsBeforeAndAfterOfAnOrdinaryKey(t *testing.T) {
	store := &capturedAudit{}
	s := &ConfigAdminService{audit: store}
	rc := &permission.RequestContext{TenantID: uuid.NewString(), UserID: uuid.NewString()}

	s.recordConfig(context.Background(), rc, AuditEntityPlatformConfig, "auth.passwordMinLength", json.RawMessage(`8`), json.RawMessage(`12`))
	require.Len(t, store.entries, 1)
	require.Equal(t, AuditEntityPlatformConfig, store.entries[0].Entity)
	require.Equal(t, namedID("config", "auth.passwordMinLength"), store.entries[0].RecordID)
	require.JSONEq(t, `{"before":8,"after":12}`, string(store.entries[0].Diff["auth.passwordMinLength"]))
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

// Reading a configuration key the registry does not know panics, and the change is refused for
// exactly that reason: the audit read must not get there first.
func TestReadingAnUnknownKeyForTheAuditEntryIsEmptyNotAPanic(t *testing.T) {
	s := &ConfigAdminService{}
	require.Nil(t, s.platformValue("not.a.real.key"))
	require.Nil(t, s.tenantValue(context.Background(), uuid.New(), "not.a.real.key"))
}
