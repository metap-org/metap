package policies

import (
	"context"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/permission"
)

// PgStore is the policy store behind permission.PermissionService.
type PgStore struct{ perms *permission.PermissionService }

func NewPgStore(perms *permission.PermissionService) *PgStore { return &PgStore{perms: perms} }

func (s *PgStore) List(ctx context.Context, tenant uuid.UUID, entity string, hasEntity bool) ([]permission.PolicyRow, error) {
	return s.perms.ListPolicies(ctx, tenant, entity, hasEntity)
}

func (s *PgStore) Create(ctx context.Context, tenant uuid.UUID, in NewPolicy) (permission.PolicyRow, error) {
	return s.perms.CreatePolicy(ctx, tenant, in.Entity, in.Action, in.Roles, in.HasRoles, in.Condition, in.By, in.Field, &in.Subject, in.Effect)
}

func (s *PgStore) Sync(ctx context.Context, tenant uuid.UUID, entity string, grants []permission.RoleActionGrant, by *uuid.UUID) ([]permission.PolicyRow, error) {
	return s.perms.SyncBasicPolicies(ctx, tenant, entity, grants, by)
}

func (s *PgStore) Delete(ctx context.Context, tenant, id uuid.UUID) error {
	return s.perms.DeletePolicy(ctx, tenant, id)
}

func (s *PgStore) Explain(ctx context.Context, rc *permission.RequestContext, entity, action, field string, hasField bool,
	record permission.JsonObject, hasRecord bool) (permission.PolicyExplanation, error) {
	return s.perms.Explain(ctx, rc, entity, action, field, hasField, record, hasRecord)
}
