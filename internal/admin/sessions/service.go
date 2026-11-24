// Package sessions is the signed-in person's own login sessions (roadmap 115 M4) as an
// administrative slice. Before it, two copies of this logic existed: a service in package admin that
// no route served, and handlers in internal/httpapi that called pkg/auth/identity directly. This is
// the one: httpapi only decodes, calls it and shapes the answer.
package sessions

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/action"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/kit/errcode"
)

// AuditEntityUsers is the audit entity a member's own account changes are recorded under.
const AuditEntityUsers = "metadata.users"

type Service struct {
	store Store
	audit kernel.Auditor
	// sink, when set, makes a revocation and its audit entry one transaction on the shared database;
	// nil keeps "change, then record best-effort".
	sink action.Sink
	now  func() time.Time
}

// WithSink makes every change atomic with its audit entry and returns s.
func (s *Service) WithSink(sink action.Sink) *Service {
	s.sink = sink
	return s
}

func (s *Service) change(ctx context.Context, c kernel.Caller, fn func(ctx context.Context) (map[string][2]any, error)) error {
	return kernel.Change(ctx, s.sink, s.audit, s.now, c, AuditEntityUsers, c.Self, fn)
}

// New is the service over store. auditor records the revocations (nil records nothing); now is the
// clock (nil: time.Now).
func New(store Store, auditor kernel.Auditor, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, audit: auditor, now: now}
}

// own is the caller and the person behind them.
func (s *Service) own(ctx context.Context) (kernel.Caller, uuid.UUID, error) {
	c, err := kernel.Authenticated(ctx)
	if err != nil {
		return kernel.Caller{}, uuid.Nil, err
	}
	id, err := s.store.IdentityOf(ctx, c.Tenant, c.Self)
	if err != nil {
		if errors.Is(err, identity.ErrNotFound) {
			return kernel.Caller{}, uuid.Nil, kernel.Err(http.StatusNotFound, errcode.NotFound, "No account for this caller.")
		}
		return kernel.Caller{}, uuid.Nil, err
	}
	return c, id, nil
}

// List is the caller's live sessions across tenants and devices; the one this request uses is marked.
func (s *Service) List(ctx context.Context) ([]Session, error) {
	c, id, err := s.own(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.List(ctx, id, s.now())
	if err != nil {
		return nil, err
	}
	return view(rows, c.RC.SessionID), nil
}

// Revoke signs out one of the caller's own sessions. An id that is not theirs, is already over, or
// is not an id at all is the same 404.
func (s *Service) Revoke(ctx context.Context, rawID string) error {
	c, id, err := s.own(ctx)
	if err != nil {
		return err
	}
	notFound := kernel.Err(http.StatusNotFound, errcode.NotFound, "Session not found.")
	sid, ok := parseSessionID(rawID)
	if !ok {
		return notFound
	}
	return s.change(ctx, c, func(ctx context.Context) (map[string][2]any, error) {
		if err := s.store.Revoke(ctx, id, sid, s.now()); err != nil {
			if errors.Is(err, identity.ErrNotFound) {
				return nil, notFound
			}
			return nil, err
		}
		return map[string][2]any{"session": {sid.String(), "revoked"}}, nil
	})
}

// RevokeOthers signs out every session of the caller but the one this request uses, and says how
// many it ended.
func (s *Service) RevokeOthers(ctx context.Context) (int, error) {
	c, id, err := s.own(ctx)
	if err != nil {
		return 0, err
	}
	var n int
	err = s.change(ctx, c, func(ctx context.Context) (map[string][2]any, error) {
		var err error
		if n, err = s.store.RevokeOthers(ctx, id, keepSession(c.RC.SessionID), s.now()); err != nil {
			return nil, err
		}
		return map[string][2]any{"sessions": {n, "revoked"}}, nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}
