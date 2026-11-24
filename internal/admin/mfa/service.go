// Package mfa is a person's own two-factor sign-in (roadmap 115 M3) as an administrative slice:
// turning it on and off, the recovery codes, its status. The factor and its TOTP arithmetic live in
// pkg/auth/identity; this slice is who may ask, in what order, and what is audited. An admin
// resetting someone else's factor is a user-administration operation and stays with the users
// service (it needs the owner and shared-identity guards).
package mfa

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/audit"
)

// AuditEntityUsers is the audit entity a member's own account changes are recorded under.
const AuditEntityUsers = "metadata.users"

type Service struct {
	store Store
	audit kernel.Auditor
	now   func() time.Time
}

// New is the service over store. auditor records the changes (nil records nothing); now is the clock
// (nil: time.Now).
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
		return kernel.Caller{}, uuid.Nil, kernel.IdentityErr(err)
	}
	return c, id, nil
}

func (s *Service) record(ctx context.Context, c kernel.Caller, diff map[string][2]any) {
	if s.audit != nil {
		s.audit.Record(ctx, c, AuditEntityUsers, c.Self, audit.ActionUpdate, diff)
	}
}

// Status is whether two-step sign-in is on and how many recovery codes are left.
func (s *Service) Status(ctx context.Context) (*MfaStatus, error) {
	_, id, err := s.own(ctx)
	if err != nil {
		return nil, err
	}
	on, left, err := s.store.Status(ctx, id)
	if err != nil {
		return nil, err
	}
	return &MfaStatus{Enabled: on, RecoveryCodesLeft: int32(left)}, nil
}

// Begin starts turning two-step sign-in on: the secret and the URI an authenticator app scans.
func (s *Service) Begin(ctx context.Context) (*MfaEnrollment, error) {
	_, id, err := s.own(ctx)
	if err != nil {
		return nil, err
	}
	email, err := s.store.Email(ctx, id)
	if err != nil {
		return nil, kernel.IdentityErr(err)
	}
	secret, uri, err := s.store.Begin(ctx, id, email, s.now())
	if err != nil {
		return nil, kernel.IdentityErr(err)
	}
	return &MfaEnrollment{Secret: secret, OtpauthUri: uri}, nil
}

// Confirm finishes turning it on with the first code from the app; the recovery codes are in the
// answer once.
func (s *Service) Confirm(ctx context.Context, req *MfaCodeRequest) (*MfaRecoveryCodes, error) {
	c, id, err := s.own(ctx)
	if err != nil {
		return nil, err
	}
	codes, err := s.store.Confirm(ctx, id, req.Code, s.now())
	if err != nil {
		return nil, kernel.IdentityErr(err)
	}
	s.record(ctx, c, map[string][2]any{"mfa": {"off", "on"}})
	return &MfaRecoveryCodes{Codes: codes}, nil
}

// Disable turns it off; it needs a current or recovery code.
func (s *Service) Disable(ctx context.Context, req *MfaCodeRequest) error {
	c, id, err := s.own(ctx)
	if err != nil {
		return err
	}
	if err := s.store.Disable(ctx, id, req.Code, s.now()); err != nil {
		return kernel.IdentityErr(err)
	}
	s.record(ctx, c, map[string][2]any{"mfa": {"on", "off"}})
	return nil
}

// RegenerateRecoveryCodes replaces every recovery code; it needs a current or recovery code. It is
// audited (the old codes stop working, which is a change to how the account can be recovered), and
// the entry says that it happened, never what the codes are.
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, req *MfaCodeRequest) (*MfaRecoveryCodes, error) {
	c, id, err := s.own(ctx)
	if err != nil {
		return nil, err
	}
	codes, err := s.store.Regenerate(ctx, id, req.Code, s.now())
	if err != nil {
		return nil, kernel.IdentityErr(err)
	}
	s.record(ctx, c, map[string][2]any{"recoveryCodes": {"replaced", "regenerated"}})
	return &MfaRecoveryCodes{Codes: codes}, nil
}
