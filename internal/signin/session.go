package signin

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
)

// Renewal is a session slid forward: a fresh token and its lifetime.
type Renewal struct {
	Token      string
	TTLSeconds uint64
}

// Renew slides a session: a caller still inside auth.sessionAbsoluteMaxSeconds of their original
// login (startedAt) gets a fresh auth.sessionTtlSeconds window. It reports false, changing nothing,
// for every reason not to renew (past the absolute cap, a subject that is not a person, a token that
// cannot be minted): the caller keeps what it has.
func (s *Service) Renew(ctx context.Context, rc *permission.RequestContext, startedAt time.Time) (*Renewal, bool) {
	tenantID, err := uuid.Parse(rc.TenantID)
	if err != nil {
		return nil, false
	}
	userID, err := uuid.Parse(rc.UserID)
	if err != nil {
		return nil, false
	}
	cfg := s.d.Effective(ctx, tenantID)
	if s.now().Unix()-startedAt.Unix() >= int64(cfg.GetU64(config.AuthSessionAbsoluteMaxSeconds)) {
		return nil, false
	}
	ttl := cfg.GetU64(config.AuthSessionTTLSeconds)
	token, err := s.d.MintSession(tenantID, userID, ttl, rc.SessionID)
	if err != nil {
		return nil, false
	}
	if sid, err := uuid.Parse(rc.SessionID); err == nil {
		_ = s.store.ExtendSession(ctx, sid, time.Duration(ttl)*time.Second, s.now())
	}
	return &Renewal{Token: token, TTLSeconds: ttl}, true
}

// IssueToken mints a fresh Bearer token for the caller's own identity and session, for a browser
// that must authenticate to a service which only speaks Bearer.
func (s *Service) IssueToken(ctx context.Context, rc *permission.RequestContext) (string, error) {
	tenantID, err := uuid.Parse(rc.TenantID)
	if err != nil {
		return "", err
	}
	userID, err := uuid.Parse(rc.UserID)
	if err != nil {
		return "", err
	}
	token, err := s.d.MintSession(tenantID, userID, s.d.Effective(ctx, tenantID).GetU64(config.AuthSessionTTLSeconds), rc.SessionID)
	if err != nil {
		if isNotIssuer(err) {
			return "", serviceErr(http.StatusNotImplemented, errcode.NotTokenIssuer, err.Error(), nil)
		}
		return "", err
	}
	return token, nil
}

// OwnEmail is the caller's own address, "" when it has none (a service account) or cannot be read.
func (s *Service) OwnEmail(ctx context.Context, rc *permission.RequestContext) string {
	userID, err := uuid.Parse(rc.UserID)
	if err != nil {
		return ""
	}
	tenantID, err := uuid.Parse(rc.TenantID)
	if err != nil {
		return ""
	}
	return s.store.UserEmail(ctx, tenantID, userID)
}
