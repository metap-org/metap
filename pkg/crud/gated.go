package crud

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/query"
)

// trustedAccess allows every entity-level question: see CrudService.Trusted.
type trustedAccess struct{}

func allow() (permission.PermissionDecision, error) { return permission.Allowed(), nil }

func (trustedAccess) CanReadEntity(context.Context, *permission.RequestContext, string) (permission.PermissionDecision, error) {
	return allow()
}
func (trustedAccess) CanCreateEntity(context.Context, *permission.RequestContext, string) (permission.PermissionDecision, error) {
	return allow()
}
func (trustedAccess) CanUpdateEntity(context.Context, *permission.RequestContext, string) (permission.PermissionDecision, error) {
	return allow()
}
func (trustedAccess) CanDeleteEntity(context.Context, *permission.RequestContext, string) (permission.PermissionDecision, error) {
	return allow()
}
func (trustedAccess) CanTransitionAction(context.Context, *permission.RequestContext, string, string) (permission.PermissionDecision, error) {
	return allow()
}

// Gated wraps a RecordBackend that does not ask the entity-level question itself (a remote
// backend, a wrapper over a Trusted service) so every operation first asks access: may this
// caller's roles read, create, update, delete or transition this entity at all.
//
// A CrudService from NewCrudService or WithAudit already asks it, so it does not need Gated; the
// wrapper is for what sits around or instead of one. The refusal is the same *ServiceError (403)
// the CrudService gives. Unlike it, Gated does not know whether the entity exists, so a caller
// without a policy for an unknown entity gets 403 where the CrudService would answer 404.
func Gated(inner RecordBackend, access EntityAccess) RecordBackend {
	return gatedBackend{inner: inner, access: access}
}

type gatedBackend struct {
	inner  RecordBackend
	access EntityAccess
}

func refuse(d permission.PermissionDecision, err error) error {
	if err != nil {
		return err
	}
	if !d.Allowed {
		return forbidden(d)
	}
	return nil
}

func (g gatedBackend) List(ctx context.Context, entity string, input query.ListInput, rc *permission.RequestContext) (ListResult, error) {
	if err := refuse(g.access.CanReadEntity(ctx, rc, entity)); err != nil {
		return ListResult{}, err
	}
	return g.inner.List(ctx, entity, input, rc)
}

func (g gatedBackend) Get(ctx context.Context, entity string, id uuid.UUID, rc *permission.RequestContext) (GetResult, error) {
	if err := refuse(g.access.CanReadEntity(ctx, rc, entity)); err != nil {
		return GetResult{}, err
	}
	return g.inner.Get(ctx, entity, id, rc)
}

func (g gatedBackend) GetMany(ctx context.Context, entity string, ids []uuid.UUID, rc *permission.RequestContext) ([]GetManyResult, error) {
	if err := refuse(g.access.CanReadEntity(ctx, rc, entity)); err != nil {
		return nil, err
	}
	return g.inner.GetMany(ctx, entity, ids, rc)
}

func (g gatedBackend) Create(ctx context.Context, entity string, data JsonObject, rc *permission.RequestContext, reason *string) (RecordDto, error) {
	if err := refuse(g.access.CanCreateEntity(ctx, rc, entity)); err != nil {
		return RecordDto{}, err
	}
	return g.inner.Create(ctx, entity, data, rc, reason)
}

func (g gatedBackend) Update(ctx context.Context, entity string, id uuid.UUID, expectedVersion int32, data JsonObject, rc *permission.RequestContext, reason *string) (RecordDto, error) {
	if err := refuse(g.access.CanUpdateEntity(ctx, rc, entity)); err != nil {
		return RecordDto{}, err
	}
	return g.inner.Update(ctx, entity, id, expectedVersion, data, rc, reason)
}

func (g gatedBackend) Transition(ctx context.Context, entity string, id uuid.UUID, action string, expectedVersion int32, data JsonObject, rc *permission.RequestContext, reason *string) (RecordDto, error) {
	if err := refuse(g.access.CanTransitionAction(ctx, rc, entity, action)); err != nil {
		return RecordDto{}, err
	}
	return g.inner.Transition(ctx, entity, id, action, expectedVersion, data, rc, reason)
}

func (g gatedBackend) Delete(ctx context.Context, entity string, id uuid.UUID, expectedVersion int32, rc *permission.RequestContext, reason *string) (RecordDto, error) {
	if err := refuse(g.access.CanDeleteEntity(ctx, rc, entity)); err != nil {
		return RecordDto{}, err
	}
	return g.inner.Delete(ctx, entity, id, expectedVersion, rc, reason)
}

func (g gatedBackend) Aggregate(ctx context.Context, entity string, spec query.AggregateSpec, rc *permission.RequestContext) ([]json.RawMessage, error) {
	if err := refuse(g.access.CanReadEntity(ctx, rc, entity)); err != nil {
		return nil, err
	}
	return g.inner.Aggregate(ctx, entity, spec, rc)
}
