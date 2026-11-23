// The signed-in person's own login sessions (roadmap 115 M4), as typed REST operations — the
// first group moved off gRPC/GraphQL (roadmap 116 P0). The logic is pkg/identity's; these
// handlers resolve the caller, call it and shape the result.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
)

// auditEntityUsers is the audit entity a member's own account changes are recorded under.
const auditEntityUsers = "metadata.users"

type Session struct {
	ID         string    `json:"id" format:"uuid"`
	Current    bool      `json:"current" doc:"The session this request is using"`
	TenantID   string    `json:"tenantId" format:"uuid"`
	TenantName string    `json:"tenantName"`
	CreatedAt  time.Time `json:"createdAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
	IP         string    `json:"ip"`
	UserAgent  string    `json:"userAgent"`
}

type listSessionsOutput struct {
	Body struct {
		Sessions []Session `json:"sessions"`
	}
}

type sessionIDInput struct {
	ID string `path:"id" format:"uuid" doc:"Session id"`
}

type revokeOthersOutput struct {
	Body struct {
		Count int `json:"count" doc:"How many sessions were signed out"`
	}
}

// accountCaller is who an account operation acts on: the caller's tenant, member and identity.
type accountCaller struct {
	rc       *permission.RequestContext
	tenant   uuid.UUID
	member   uuid.UUID
	identity uuid.UUID
}

func resolveAccountCaller(ctx context.Context, state *AppState) (accountCaller, error) {
	rc := callerOf(ctx)
	if rc == nil {
		return accountCaller{}, newAPIError(http.StatusUnauthorized, errcode.Unauthorized, "Missing or invalid authorization header.", nil)
	}
	tenant, err := uuid.Parse(rc.TenantID)
	if err != nil {
		return accountCaller{}, newAPIError(http.StatusUnauthorized, errcode.Unauthorized, "Token is missing required claims.", nil)
	}
	member, _ := uuid.Parse(rc.UserID)
	id, err := identity.IdentityOf(ctx, state.Pool, tenant, member)
	if err != nil {
		if errors.Is(err, identity.ErrNotFound) {
			return accountCaller{}, newAPIError(http.StatusNotFound, errcode.NotFound, "No account for this caller.", nil)
		}
		return accountCaller{}, err
	}
	return accountCaller{rc: rc, tenant: tenant, member: member, identity: id}, nil
}

// recordAccountChange writes an audit entry under the caller's own member record.
func recordAccountChange(ctx context.Context, state *AppState, c accountCaller, diff map[string][2]any) {
	if state.Audit == nil {
		return
	}
	obj := audit.JsonObject{}
	for field, ba := range diff {
		if raw, err := json.Marshal(map[string]any{"before": ba[0], "after": ba[1]}); err == nil {
			obj[field] = raw
		}
	}
	entry := audit.Entry{TenantID: c.tenant, Entity: auditEntityUsers, RecordID: c.member,
		Action: audit.ActionUpdate, Diff: obj, OccurredAt: time.Now()}
	if c.member != uuid.Nil {
		self := c.member
		entry.ActorUserID = &self
	}
	_ = state.Audit.Record(ctx, c.tenant, entry)
}

func registerSessionOperations(api huma.API, state *AppState) {
	mw := huma.Middlewares{authed(state)}
	tags := []string{"Account"}

	huma.Register(api, huma.Operation{
		OperationID: "listMySessions", Method: http.MethodGet, Path: "/me/sessions",
		Summary: "The caller's login sessions, across tenants and devices", Tags: tags, Middlewares: mw,
	}, func(ctx context.Context, _ *struct{}) (*listSessionsOutput, error) {
		c, err := resolveAccountCaller(ctx, state)
		if err != nil {
			return nil, apiErr(err)
		}
		list, err := identity.ListSessions(ctx, state.Pool, c.identity, time.Now())
		if err != nil {
			return nil, apiErr(err)
		}
		out := &listSessionsOutput{}
		out.Body.Sessions = make([]Session, 0, len(list))
		for _, s := range list {
			out.Body.Sessions = append(out.Body.Sessions, Session{
				ID: s.ID.String(), Current: s.ID.String() == c.rc.SessionID, TenantID: s.TenantID.String(),
				TenantName: s.TenantName, CreatedAt: s.CreatedAt.UTC(), LastSeenAt: s.LastSeenAt.UTC(),
				ExpiresAt: s.ExpiresAt.UTC(), IP: s.IP, UserAgent: s.UserAgent,
			})
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "revokeMySession", Method: http.MethodDelete, Path: "/me/sessions/{id}",
		Summary: "Sign out one of the caller's sessions", Tags: tags, Middlewares: mw,
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *sessionIDInput) (*struct{}, error) {
		c, err := resolveAccountCaller(ctx, state)
		if err != nil {
			return nil, apiErr(err)
		}
		sid, err := uuid.Parse(in.ID)
		if err != nil {
			return nil, newAPIError(http.StatusNotFound, errcode.NotFound, "Session not found.", nil)
		}
		if err := identity.RevokeSession(ctx, state.Pool, c.identity, sid, time.Now()); err != nil {
			if errors.Is(err, identity.ErrNotFound) {
				return nil, newAPIError(http.StatusNotFound, errcode.NotFound, "Session not found.", nil)
			}
			return nil, apiErr(err)
		}
		recordAccountChange(ctx, state, c, map[string][2]any{"session": {sid.String(), "revoked"}})
		return nil, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "revokeMyOtherSessions", Method: http.MethodPost, Path: "/me/sessions/revoke-others",
		Summary: "Sign out every session of the caller but this one", Tags: tags, Middlewares: mw,
	}, func(ctx context.Context, _ *struct{}) (*revokeOthersOutput, error) {
		c, err := resolveAccountCaller(ctx, state)
		if err != nil {
			return nil, apiErr(err)
		}
		keep, _ := uuid.Parse(c.rc.SessionID) // uuid.Nil keeps nothing: a non-session caller ends them all
		n, err := identity.RevokeOtherSessions(ctx, state.Pool, c.identity, keep, time.Now())
		if err != nil {
			return nil, apiErr(err)
		}
		recordAccountChange(ctx, state, c, map[string][2]any{"sessions": {n, "revoked"}})
		out := &revokeOthersOutput{}
		out.Body.Count = n
		return out, nil
	})
}
