package admin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/kit/errcode"
)

func (s *AccountService) ListMySessions(ctx context.Context, _ *Empty) (*ListSessionsResponse, error) {
	c, err := s.d.caller(ctx)
	if err != nil {
		return nil, err
	}
	id, err := s.ownIdentity(ctx, c)
	if err != nil {
		return nil, err
	}
	list, err := identity.ListSessions(ctx, s.d.cfg.Shared, id, s.d.now())
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	out := &ListSessionsResponse{Sessions: []*Session{}}
	for _, x := range list {
		out.Sessions = append(out.Sessions, &Session{
			Id: x.ID.String(), Current: x.ID.String() == c.rc.SessionID, TenantId: x.TenantID.String(), TenantName: x.TenantName,
			CreatedAt: x.CreatedAt.UTC().Format(time.RFC3339), LastSeenAt: x.LastSeenAt.UTC().Format(time.RFC3339),
			ExpiresAt: x.ExpiresAt.UTC().Format(time.RFC3339), Ip: x.IP, UserAgent: x.UserAgent,
		})
	}
	return out, nil
}

func (s *AccountService) RevokeMySession(ctx context.Context, req *SessionIdRequest) (*Empty, error) {
	c, err := s.d.caller(ctx)
	if err != nil {
		return nil, err
	}
	id, err := s.ownIdentity(ctx, c)
	if err != nil {
		return nil, err
	}
	sid, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, svcErr(ctx, http.StatusNotFound, errcode.NotFound, "Session not found.")
	}
	if err := identity.RevokeSession(ctx, s.d.cfg.Shared, id, sid, s.d.now()); err != nil {
		if errors.Is(err, identity.ErrNotFound) {
			return nil, svcErr(ctx, http.StatusNotFound, errcode.NotFound, "Session not found.")
		}
		return nil, rpcError(ctx, err)
	}
	s.d.record(ctx, c, c.self, audit.ActionUpdate, map[string][2]any{"session": {sid.String(), "revoked"}})
	return &Empty{}, nil
}

func (s *AccountService) RevokeOtherSessions(ctx context.Context, _ *Empty) (*RevokedSessions, error) {
	c, err := s.d.caller(ctx)
	if err != nil {
		return nil, err
	}
	id, err := s.ownIdentity(ctx, c)
	if err != nil {
		return nil, err
	}
	keep, _ := uuid.Parse(c.rc.SessionID) // uuid.Nil keeps nothing: a non-session caller ends them all
	n, err := identity.RevokeOtherSessions(ctx, s.d.cfg.Shared, id, keep, s.d.now())
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	s.d.record(ctx, c, c.self, audit.ActionUpdate, map[string][2]any{"sessions": {n, "revoked"}})
	return &RevokedSessions{Count: int32(n)}, nil
}
