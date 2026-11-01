package permission

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	mcache "github.com/metap-org/metap-go/cache"
)

// PermissionService mirrors the Rust PermissionService.
type PermissionService struct {
	store PolicyStore
	cache mcache.Cache // nil by default — every call site queries PolicyStore fresh
}

func NewPermissionService(store PolicyStore) *PermissionService {
	return &PermissionService{store: store}
}

func NewPermissionServiceWithCache(store PolicyStore, cache mcache.Cache) *PermissionService {
	return &PermissionService{store: store, cache: cache}
}

func policiesCacheKey(entity string) string { return "policies:" + entity }

// ScopedTenant: RequestContext.TenantID is required and always derived from a verified JWT by
// the auth hook before this is ever called.
func (s *PermissionService) ScopedTenant(ctx *RequestContext) (uuid.UUID, error) {
	if ctx.TenantID == "" {
		return uuid.UUID{}, fmt.Errorf("RequestContext.TenantID is required but was missing or empty")
	}
	return uuid.Parse(ctx.TenantID)
}

func (s *PermissionService) checkAction(goCtx context.Context, ctx *RequestContext, entityName string, action EntityAction) (PermissionDecision, error) {
	userID := ctx.UserID
	if userID == "" {
		userID = "unknown"
	}

	// Before the admin bypass on purpose: an OAuth2 scope narrows even an admin user's own
	// access.
	if !ctx.ScopePermits(entityName, action) {
		slog.Warn("denied: OAuth2 scope does not cover this entity/action", "entity", entityName, "action", action.String(), "user_id", userID)
		return Forbidden(), nil
	}

	if ctx.IsAdmin() {
		slog.Debug("allowed: admin role", "entity", entityName, "action", action.String(), "user_id", userID)
		return Allowed(), nil
	}

	tenantID, err := s.ScopedTenant(ctx)
	if err != nil {
		return PermissionDecision{}, err
	}
	rows, err := s.store.FindContextPolicies(goCtx, tenantID, entityName, action.String())
	if err != nil {
		return PermissionDecision{}, err
	}

	switch EvaluatePolicies(rows, ctx, nil, false) {
	case VerdictAllow:
		slog.Debug("allowed: policy matched", "entity", entityName, "action", action.String(), "user_id", userID)
		return Allowed(), nil
	case VerdictDeny:
		slog.Warn("denied: an explicit deny policy matched", "entity", entityName, "action", action.String(), "user_id", userID)
		return Forbidden(), nil
	default:
		slog.Warn("denied: no policy matched (default-deny for non-admin)", "entity", entityName, "action", action.String(), "user_id", userID, "policy_count", len(rows))
		return Forbidden(), nil
	}
}

func (s *PermissionService) CanReadEntity(ctx context.Context, rc *RequestContext, entity string) (PermissionDecision, error) {
	return s.checkAction(ctx, rc, entity, ActionRead)
}
func (s *PermissionService) CanCreateEntity(ctx context.Context, rc *RequestContext, entity string) (PermissionDecision, error) {
	return s.checkAction(ctx, rc, entity, ActionCreate)
}
func (s *PermissionService) CanUpdateEntity(ctx context.Context, rc *RequestContext, entity string) (PermissionDecision, error) {
	return s.checkAction(ctx, rc, entity, ActionUpdate)
}
func (s *PermissionService) CanDeleteEntity(ctx context.Context, rc *RequestContext, entity string) (PermissionDecision, error) {
	return s.checkAction(ctx, rc, entity, ActionDelete)
}
func (s *PermissionService) CanTransitionEntity(ctx context.Context, rc *RequestContext, entity string) (PermissionDecision, error) {
	return s.checkAction(ctx, rc, entity, ActionTransition)
}

func (s *PermissionService) LoadSnapshot(ctx context.Context, tenantID uuid.UUID, entity string) (*PermissionSnapshot, error) {
	if s.cache == nil {
		return LoadPermissionSnapshot(ctx, s.store, tenantID, entity)
	}

	key := policiesCacheKey(entity)
	if bytes, ok, err := s.cache.Get(ctx, tenantID, key); err == nil && ok {
		var rows []PolicyRow
		if jerr := json.Unmarshal(bytes, &rows); jerr == nil {
			return PermissionSnapshotFromRows(rows), nil
		}
		slog.Warn("policy cache entry failed to deserialize, falling back to store", "tenant_id", tenantID, "entity", entity)
	} else if err != nil {
		slog.Warn("policy cache read failed, falling back to store", "tenant_id", tenantID, "entity", entity, "error", err)
	}

	rows, err := s.store.LoadAllPolicies(ctx, tenantID, entity)
	if err != nil {
		return nil, err
	}
	if bytes, err := json.Marshal(rows); err == nil {
		if err := s.cache.Set(ctx, tenantID, key, bytes); err != nil {
			slog.Warn("policy cache write failed (non-fatal)", "tenant_id", tenantID, "entity", entity, "error", err)
		}
	}
	return PermissionSnapshotFromRows(rows), nil
}

func (s *PermissionService) ListPolicies(ctx context.Context, tenantID uuid.UUID, entity string, hasEntity bool) ([]PolicyRow, error) {
	return s.store.ListPolicies(ctx, tenantID, entity, hasEntity)
}

func (s *PermissionService) CreatePolicy(ctx context.Context, tenantID uuid.UUID, entity, action string, roles []string, hasRoles bool,
	condition *PolicyCondition, createdBy *uuid.UUID, field string, subject *PolicySubject, effect PolicyEffect) (PolicyRow, error) {
	row, err := s.store.CreatePolicy(ctx, tenantID, entity, action, roles, hasRoles, condition, createdBy, field, subject, effect)
	if err != nil {
		return PolicyRow{}, err
	}
	s.invalidatePoliciesCache(ctx, tenantID, entity)
	return row, nil
}

func (s *PermissionService) SyncBasicPolicies(ctx context.Context, tenantID uuid.UUID, entity string, grants []RoleActionGrant, createdBy *uuid.UUID) ([]PolicyRow, error) {
	rows, err := s.store.SyncBasicPolicies(ctx, tenantID, entity, grants, createdBy)
	if err != nil {
		return nil, err
	}
	s.invalidatePoliciesCache(ctx, tenantID, entity)
	return rows, nil
}

func (s *PermissionService) DeletePolicy(ctx context.Context, tenantID, id uuid.UUID) error {
	if s.cache != nil {
		if rows, err := s.store.ListPolicies(ctx, tenantID, "", false); err == nil {
			for _, row := range rows {
				if row.ID == id {
					s.invalidatePoliciesCache(ctx, tenantID, row.Entity)
					break
				}
			}
		}
	}
	return s.store.DeletePolicy(ctx, tenantID, id)
}

func (s *PermissionService) invalidatePoliciesCache(ctx context.Context, tenantID uuid.UUID, entity string) {
	if s.cache == nil {
		return
	}
	if err := s.cache.Invalidate(ctx, tenantID, policiesCacheKey(entity)); err != nil {
		slog.Warn("policy cache invalidate failed (non-fatal, entry will expire via TTL)", "tenant_id", tenantID, "entity", entity, "error", err)
	}
}

func (s *PermissionService) Explain(ctx context.Context, rc *RequestContext, entity, action string, field string, hasField bool,
	record JsonObject, hasRecord bool) (PolicyExplanation, error) {
	tenantID, err := s.ScopedTenant(rc)
	if err != nil {
		return PolicyExplanation{}, err
	}

	var opts ExplainOptions
	switch {
	case hasField:
		opts = ExplainOptions{Field: field}
	case hasRecord:
		subject := SubjectRecord
		opts = ExplainOptions{Subject: &subject}
	}

	rows, err := s.store.FindExplainPolicies(ctx, tenantID, entity, action, opts)
	if err != nil {
		return PolicyExplanation{}, err
	}
	return ExplainPolicies(rows, rc, record, hasRecord), nil
}
