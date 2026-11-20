package grpcapi

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/grpcapi/pb"
	"github.com/metap-org/metap/pkg/identity"
	"github.com/metap-org/metap/pkg/kit/errcode"
)

// ownIdentity is the account behind the caller's membership.
func (s *AccountService) ownIdentity(ctx context.Context, c caller) (uuid.UUID, error) {
	id, err := identity.IdentityOf(ctx, s.d.cfg.Shared, c.tenant, c.self)
	if err != nil {
		return uuid.Nil, identityErr(ctx, err)
	}
	return id, nil
}

func (s *AccountService) GetMfaStatus(ctx context.Context, _ *pb.Empty) (*pb.MfaStatus, error) {
	c, err := s.d.caller(ctx)
	if err != nil {
		return nil, err
	}
	id, err := s.ownIdentity(ctx, c)
	if err != nil {
		return nil, err
	}
	on, err := identity.MFAEnabled(ctx, s.d.cfg.Shared, id)
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	left := 0
	if on {
		if left, err = identity.RecoveryCodesLeft(ctx, s.d.cfg.Shared, id); err != nil {
			return nil, rpcError(ctx, err)
		}
	}
	return &pb.MfaStatus{Enabled: on, RecoveryCodesLeft: int32(left)}, nil
}

func (s *AccountService) BeginMfaEnrollment(ctx context.Context, _ *pb.Empty) (*pb.MfaEnrollment, error) {
	c, err := s.d.caller(ctx)
	if err != nil {
		return nil, err
	}
	id, err := s.ownIdentity(ctx, c)
	if err != nil {
		return nil, err
	}
	ident, err := identity.FindIdentityByID(ctx, s.d.cfg.Shared, id)
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	secret, uri, err := identity.BeginMFAEnrollment(ctx, s.d.cfg.Shared, id, ident.Email, s.d.now())
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	return &pb.MfaEnrollment{Secret: secret, OtpauthUri: uri}, nil
}

func (s *AccountService) ConfirmMfaEnrollment(ctx context.Context, req *pb.MfaCodeRequest) (*pb.MfaRecoveryCodes, error) {
	c, err := s.d.caller(ctx)
	if err != nil {
		return nil, err
	}
	id, err := s.ownIdentity(ctx, c)
	if err != nil {
		return nil, err
	}
	var codes []string
	if err := s.inSharedTx(ctx, func(db identity.DBTX) error {
		var err error
		codes, err = identity.ConfirmMFAEnrollment(ctx, db, id, req.GetCode(), s.d.now())
		return err
	}); err != nil {
		return nil, identityErr(ctx, err)
	}
	s.d.record(ctx, c, c.self, audit.ActionUpdate, map[string][2]any{"mfa": {"off", "on"}})
	return &pb.MfaRecoveryCodes{Codes: codes}, nil
}

func (s *AccountService) DisableMfa(ctx context.Context, req *pb.MfaCodeRequest) (*pb.Empty, error) {
	c, err := s.d.caller(ctx)
	if err != nil {
		return nil, err
	}
	id, err := s.ownIdentity(ctx, c)
	if err != nil {
		return nil, err
	}
	if err := s.inSharedTx(ctx, func(db identity.DBTX) error {
		if err := identity.VerifyMFA(ctx, db, id, req.GetCode(), s.d.now()); err != nil {
			return err
		}
		return identity.DisableMFA(ctx, db, id)
	}); err != nil {
		return nil, identityErr(ctx, err)
	}
	s.d.record(ctx, c, c.self, audit.ActionUpdate, map[string][2]any{"mfa": {"on", "off"}})
	return &pb.Empty{}, nil
}

func (s *AccountService) RegenerateRecoveryCodes(ctx context.Context, req *pb.MfaCodeRequest) (*pb.MfaRecoveryCodes, error) {
	c, err := s.d.caller(ctx)
	if err != nil {
		return nil, err
	}
	id, err := s.ownIdentity(ctx, c)
	if err != nil {
		return nil, err
	}
	var codes []string
	if err := s.inSharedTx(ctx, func(db identity.DBTX) error {
		if err := identity.VerifyMFA(ctx, db, id, req.GetCode(), s.d.now()); err != nil {
			return err
		}
		var err error
		codes, err = identity.RegenerateRecoveryCodes(ctx, db, id)
		return err
	}); err != nil {
		return nil, identityErr(ctx, err)
	}
	return &pb.MfaRecoveryCodes{Codes: codes}, nil
}

// inSharedTx runs fn in one transaction on the shared database (identities live there).
func (s *AccountService) inSharedTx(ctx context.Context, fn func(db identity.DBTX) error) error {
	tx, err := s.d.cfg.Shared.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *UserAdminService) ResetMfa(ctx context.Context, req *pb.UserIdRequest) (*pb.Empty, error) {
	c, err := s.admin(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.GetId())
	if err != nil {
		return nil, err
	}
	if err := s.d.guardOwner(ctx, c, id, "reset the MFA of"); err != nil {
		return nil, err
	}
	m, err := identity.GetMember(ctx, s.d.cfg.Shared, c.tenant, id)
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	shared, err := identity.SharedIdentities(ctx, s.d.cfg.Shared, c.tenant, []uuid.UUID{m.IdentityID})
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	if shared[m.IdentityID] {
		return nil, svcErr(ctx, http.StatusConflict, errcode.Forbidden,
			"This person also belongs to another tenant; only they can change their two-factor settings.")
	}
	if err := identity.DisableMFA(ctx, s.d.cfg.Shared, m.IdentityID); err != nil {
		return nil, identityErr(ctx, err)
	}
	s.d.record(ctx, c, id, audit.ActionUpdate, map[string][2]any{"mfa": {"on", "reset"}})
	return &pb.Empty{}, nil
}
