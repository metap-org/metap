// The signed-in person's own login sessions (roadmap 115 M4), as typed REST operations. The logic is
// internal/admin/sessions (a slice: rules, store, service); these handlers only decode, call it and
// shape the answer.
package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/internal/admin/sessions"
)

type listSessionsOutput struct {
	Body struct {
		Sessions []sessions.Session `json:"sessions"`
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

func registerSessionOperations(api huma.API, state *AppState) {
	mw := huma.Middlewares{authed(state)}
	tags := []string{"Account"}
	svc := sessions.New(sessions.NewPgStore(kernel.IdentityDB{Pool: state.Pool}), kernel.StoreAuditor{Store: state.Audit}, nil)
	// The service reads its caller from the context the way the administrative services do.
	as := func(ctx context.Context) context.Context { return kernel.WithCaller(ctx, callerOf(ctx)) }

	huma.Register(api, huma.Operation{
		OperationID: "listMySessions", Method: http.MethodGet, Path: "/me/sessions",
		Summary: "The caller's login sessions, across tenants and devices", Tags: tags, Middlewares: mw,
	}, func(ctx context.Context, _ *struct{}) (*listSessionsOutput, error) {
		list, err := svc.List(as(ctx))
		if err != nil {
			return nil, apiErr(err)
		}
		out := &listSessionsOutput{}
		out.Body.Sessions = list
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "revokeMySession", Method: http.MethodDelete, Path: "/me/sessions/{id}",
		Summary: "Sign out one of the caller's sessions", Tags: tags, Middlewares: mw,
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *sessionIDInput) (*struct{}, error) {
		if err := svc.Revoke(as(ctx), in.ID); err != nil {
			return nil, apiErr(err)
		}
		return nil, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "revokeMyOtherSessions", Method: http.MethodPost, Path: "/me/sessions/revoke-others",
		Summary: "Sign out every session of the caller but this one", Tags: tags, Middlewares: mw,
	}, func(ctx context.Context, _ *struct{}) (*revokeOthersOutput, error) {
		n, err := svc.RevokeOthers(as(ctx))
		if err != nil {
			return nil, apiErr(err)
		}
		out := &revokeOthersOutput{}
		out.Body.Count = n
		return out, nil
	})
}
