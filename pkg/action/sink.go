package action

import (
	"bytes"
	"context"
	"io"
	"log/slog"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/kit/standard"
	"github.com/metap-org/metap/pkg/messaging/events"
)

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

// dbSink writes events through the tenant's own transaction and audit entries through the audit
// store, the same two paths the CRUD service uses.
type dbSink struct {
	tx    crud.TxBeginner
	audit audit.Store
}

// NewSink is the production Sink: events are enqueued in one transaction on the tenant's database
// (the outbox publisher delivers them), audit entries go to store (nil drops them, like a CRUD
// service built without an audit store).
func NewSink(tx crud.TxBeginner, store audit.Store) Sink { return dbSink{tx: tx, audit: store} }

func (s dbSink) Emit(ctx context.Context, tenantID uuid.UUID, evs []events.Event) error {
	tx, err := s.tx.Begin(ctx, tenantID)
	if err != nil {
		return err
	}
	for _, e := range evs {
		if err := events.Emit(ctx, tx, e); err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s dbSink) Audit(ctx context.Context, tenantID uuid.UUID, entries []audit.Entry) {
	if s.audit == nil {
		return
	}
	for _, e := range entries {
		if err := s.audit.Record(ctx, tenantID, e); err != nil {
			slog.Error("failed to record audit trail entry", "entity", e.Entity, standard.LogError, err)
		}
	}
}
