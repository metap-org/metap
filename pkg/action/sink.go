package action

import (
	"bytes"
	"context"
	"io"
	"log/slog"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/kit/standard"
	"github.com/metap-org/metap/pkg/messaging/events"
	"github.com/metap-org/metap/pkg/tenancy"
)

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

// dbSink runs units of work through the tenant router and records audit entries in the audit
// store, the same two paths the CRUD service uses.
type dbSink struct {
	router *tenancy.Router
	audit  audit.Store
}

// NewSink is the production Sink: a unit of work is one transaction on the tenant's database
// (record writes and policy reads made with its context join it, and the outbox publisher delivers
// the events it enqueued); audit entries go to store (nil drops them, like a CRUD service built
// without an audit store).
func NewSink(router *tenancy.Router, store audit.Store) Sink {
	return dbSink{router: router, audit: store}
}

func (s dbSink) Atomic(ctx context.Context, tenantID uuid.UUID, work func(ctx context.Context, enqueue func([]events.Event) error) error) error {
	uctx, unit, err := s.router.BeginUnit(ctx, tenantID)
	if err != nil {
		return err
	}
	enqueue := func(evs []events.Event) error {
		for _, e := range evs {
			if err := events.Emit(uctx, unit.Tx(), e); err != nil {
				return err
			}
		}
		return nil
	}
	if err := work(uctx, enqueue); err != nil {
		unit.Rollback(ctx)
		return err
	}
	return unit.Commit(ctx)
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
