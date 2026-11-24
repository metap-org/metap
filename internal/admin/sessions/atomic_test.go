package sessions

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/action/actiontest"
	"github.com/metap-org/metap/pkg/audit"
)

func TestWithASinkARevocationAndItsEntryAreWrittenTogether(t *testing.T) {
	sink := &actiontest.Sink{}
	w := newWorld()
	w.svc.WithSink(sink)
	bob := w.store.person(w.tenant)
	laptop := w.store.signIn(w.tenant, bob, time.Hour, w.now)
	w.store.signIn(w.tenant, bob, time.Hour, w.now)

	n, err := w.svc.RevokeOthers(w.as(bob, laptop.String()))
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Empty(t, w.audit.entries, "recorded through the sink, not after the fact")
	require.Len(t, sink.Entries, 1)
	require.Equal(t, audit.ActionUpdate, sink.Entries[0].Action)
	require.Contains(t, string(sink.Entries[0].Diff["sessions"]), "revoked")
}

func TestARevocationThatFindsNothingWritesNoEntry(t *testing.T) {
	sink := &actiontest.Sink{}
	w := newWorld()
	w.svc.WithSink(sink)
	bob := w.store.person(w.tenant)

	require.Error(t, w.svc.Revoke(w.as(bob, ""), "not-an-id"))
	require.Empty(t, sink.Entries)
}
