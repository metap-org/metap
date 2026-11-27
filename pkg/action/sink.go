package action

import (
	"bytes"
	"context"
	"io"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/messaging/events"
	"github.com/metap-org/metap/pkg/tenancy"
)

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

// dbSink runs units of work through the tenant router.
type dbSink struct {
	router *tenancy.Router
}

// NewSink is the production Sink: a unit of work is one transaction on the tenant's database
// (record writes and policy reads made with its context join it); the events and audit entries it
// adds are written to the outbox in that same transaction, the outbox publisher delivers the
// events and the audit drain moves the entries into the tenant's chain.
func NewSink(router *tenancy.Router) Sink {
	return dbSink{router: router}
}

type dbWriter struct {
	ctx  context.Context
	unit *tenancy.Unit
}

func (w dbWriter) Events(evs []events.Event) error {
	for _, e := range evs {
		if err := events.Emit(w.ctx, w.unit.Tx(), e); err != nil {
			return err
		}
	}
	return nil
}

func (w dbWriter) Audit(entries []audit.Entry) error {
	for _, e := range entries {
		if err := audit.Enqueue(w.ctx, w.unit.Tx(), e); err != nil {
			return err
		}
	}
	return nil
}

func (s dbSink) Atomic(ctx context.Context, tenantID uuid.UUID, work func(ctx context.Context, w Writer) error) error {
	uctx, unit, err := s.router.BeginUnit(ctx, tenantID)
	if err != nil {
		return err
	}
	// Deferred, not only on error: a work that panics (the HTTP layer recovers it and carries on) would
	// otherwise leave the transaction open, its connection out of the pool and its locks held. Rollback
	// is a no-op after Commit; it runs without the request's cancellation so a dropped client still
	// gets its transaction ended.
	defer unit.Rollback(context.WithoutCancel(ctx))
	if err := work(uctx, dbWriter{ctx: uctx, unit: unit}); err != nil {
		return err
	}
	return unit.Commit(ctx)
}

// AtomicShared is Atomic on the shared database, for work on identity data (see SharedSink).
func (s dbSink) AtomicShared(ctx context.Context, work func(ctx context.Context, w Writer) error) error {
	uctx, unit, err := s.router.BeginSharedUnit(ctx)
	if err != nil {
		return err
	}
	// Deferred, not only on error: a work that panics (the HTTP layer recovers it and carries on) would
	// otherwise leave the transaction open, its connection out of the pool and its locks held. Rollback
	// is a no-op after Commit; it runs without the request's cancellation so a dropped client still
	// gets its transaction ended.
	defer unit.Rollback(context.WithoutCancel(ctx))
	if err := work(uctx, dbWriter{ctx: uctx, unit: unit}); err != nil {
		return err
	}
	return unit.Commit(ctx)
}
