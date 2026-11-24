package apikeys

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/action/actiontest"
	"github.com/metap-org/metap/pkg/audit"
)

func TestWithASinkTheKeyAndItsAuditEntryAreWrittenTogether(t *testing.T) {
	w := newWorld()
	sink := &actiontest.Sink{}
	w.svc.WithSink(sink)
	admin, asAdmin := w.admin()
	bot := w.store.member(w.tenant)

	got, err := w.svc.Create(asAdmin, &CreateApiKeyRequest{UserId: bot.String(), Name: "pipeline", ExpiresInDays: 30})
	require.NoError(t, err)
	require.Empty(t, w.audit.entries, "recorded through the sink, not after the fact")
	require.Len(t, sink.Entries, 1)
	e := sink.Entries[0]
	require.Equal(t, w.tenant, e.TenantID)
	require.Equal(t, AuditEntityUsers, e.Entity)
	require.Equal(t, bot, e.RecordID)
	require.Equal(t, audit.ActionUpdate, e.Action, "the form the audit trail already shows")
	require.Equal(t, admin, *e.ActorUserID)
	require.Contains(t, string(e.Diff["apiKeyCreated"]), "pipeline ("+got.ApiKey.Prefix+")")
	require.NotContains(t, string(e.Diff["apiKeyCreated"]), got.Token, "the secret is never audited")

	require.NoError(t, w.svc.Revoke(asAdmin, got.ApiKey.Id))
	require.Len(t, sink.Entries, 2)
	require.Contains(t, string(sink.Entries[1].Diff["apiKeyRevoked"]), got.ApiKey.Id)
}

func TestAChangeThatFailsWritesNoEntry(t *testing.T) {
	w := newWorld()
	sink := &actiontest.Sink{}
	w.svc.WithSink(sink)
	_, asAdmin := w.admin()

	_, err := w.svc.Create(asAdmin, &CreateApiKeyRequest{UserId: w.store.member(w.tenant).String(), Name: "  "})
	require.Error(t, err)
	require.Empty(t, sink.Entries)
}
