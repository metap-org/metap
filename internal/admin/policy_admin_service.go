// PolicyAdminService: a tenant's entity policies (who may read, create, update, delete or
// transition which entity), the permission matrix's save call, explaining a decision, and dropping
// a member's cached context attributes. It sits on permission.PermissionService and needs no
// identity database, so a service that does not issue tokens serves it too.
package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/action"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
)

// ContextInvalidator drops what is cached about a member's context attributes.
type ContextInvalidator interface {
	Invalidate(tenantID, userID uuid.UUID)
}

// AuditEntityPolicies is the audit-trail entity policy administration records under.
const AuditEntityPolicies = "metadata.policies"

// PolicyAdminService manages the caller's tenant's policies. Every call needs the admin role. Every
// change is audited: who may do what is the most security-relevant thing an admin edits.
type PolicyAdminService struct {
	perms *permission.PermissionService
	cache ContextInvalidator
	audit audit.Store
	// sink, when set, makes a policy change and its audit entry one transaction on the tenant's
	// database; nil keeps "change, then record best-effort".
	sink action.Sink
}

// WithSink makes every change atomic with its audit entry and returns s.
func (s *PolicyAdminService) WithSink(sink action.Sink) *PolicyAdminService {
	s.sink = sink
	return s
}

// change runs one policy change and records it (atomically with a sink). fn returns the id of the
// administered record and what to audit; a nil diff writes no entry.
func (s *PolicyAdminService) change(ctx context.Context, rc *permission.RequestContext, act audit.AuditAction,
	fn func(ctx context.Context) (uuid.UUID, map[string][2]any, error)) error {
	c, err := kernel.CallerOf(rc)
	if err != nil {
		return err
	}
	return kernel.ChangeInTenantNew(ctx, s.sink, kernel.StoreAuditor{Store: s.audit}, nil, c, AuditEntityPolicies, act, fn)
}

// NewPolicyAdminService serves policies from perms; store (may be nil) is where changes are audited.
func NewPolicyAdminService(perms *permission.PermissionService, cache ContextInvalidator, store audit.Store) *PolicyAdminService {
	return &PolicyAdminService{perms: perms, cache: cache, audit: store}
}

// policyFields is the before/after view of a policy for an audit entry; before is true for a
// policy that is going away.
func policyFields(row permission.PolicyRow, gone bool) map[string][2]any {
	pair := func(v any) [2]any {
		if gone {
			return [2]any{v, nil}
		}
		return [2]any{nil, v}
	}
	diff := map[string][2]any{
		"entity": pair(row.Entity), "action": pair(row.Action), "effect": pair(row.Effect.String()), "subject": pair(row.Subject),
	}
	if row.HasRoles {
		diff["roles"] = pair(row.Roles)
	}
	if row.Field != "" {
		diff["field"] = pair(row.Field)
	}
	if row.Condition != nil {
		diff["condition"] = pair(row.Condition)
	}
	return diff
}

// basicGrants lists a set of policy rows as sorted "role:action" strings, the part of an entity's
// policies the permission matrix owns (no condition, field, record subject or deny): what a sync
// replaces, and so what an audit entry for it shows.
func basicGrants(rows []permission.PolicyRow) []string {
	out := []string{}
	for _, r := range rows {
		if r.Condition != nil || r.Field != "" || r.Subject != "context" || r.Effect != permission.EffectAllow {
			continue
		}
		role := "*"
		if r.HasRoles {
			role = strings.Join(r.Roles, ",")
		}
		out = append(out, role+":"+r.Action)
	}
	sort.Strings(out)
	return out
}

func policyView(row permission.PolicyRow) *Policy {
	p := &Policy{ID: row.ID.String(), TenantID: row.TenantID.String(), Entity: row.Entity, Action: row.Action,
		Subject: row.Subject, Effect: row.Effect.String(), Condition: jsonOr(nil, "null")}
	if row.Field != "" {
		p.Field = &row.Field
	}
	if row.HasRoles {
		p.Roles = row.Roles
		if p.Roles == nil {
			p.Roles = []string{}
		}
	}
	if row.Condition != nil {
		if raw, err := json.Marshal(row.Condition); err == nil {
			p.Condition = raw
		}
	}
	if row.CreatedBy != nil {
		s := row.CreatedBy.String()
		p.CreatedBy = &s
	}
	return p
}

func policyViews(rows []permission.PolicyRow) []*Policy {
	out := make([]*Policy, len(rows))
	for i, row := range rows {
		out[i] = policyView(row)
	}
	return out
}

func parseUUID(ctx context.Context, name, v string) (uuid.UUID, error) {
	id, err := uuid.Parse(v)
	if err != nil {
		return uuid.Nil, svcErr(ctx, http.StatusBadRequest, errcode.ValidationFailed, "`"+name+"` must be a UUID.")
	}
	return id, nil
}

func optionalUUID(s string) *uuid.UUID {
	id, err := uuid.Parse(s)
	if s == "" || err != nil {
		return nil
	}
	return &id
}

func unknownAction(ctx context.Context, action string) error {
	return svcErr(ctx, http.StatusBadRequest, errcode.ValidationFailed,
		"Unknown action \""+action+"\" — must be one of [\""+strings.Join(permission.EntityActionNames(), "\", \"")+"\"].")
}

func (s *PolicyAdminService) ListPolicies(ctx context.Context, req *ListPoliciesRequest) (*PolicyList, error) {
	rc, err := adminCaller(ctx)
	if err != nil {
		return nil, err
	}
	tenant, err := s.perms.ScopedTenant(rc)
	if err != nil {
		return nil, err
	}
	rows, err := s.perms.ListPolicies(ctx, tenant, req.Entity, req.Entity != "")
	if err != nil {
		return nil, err
	}
	return &PolicyList{Data: policyViews(rows)}, nil
}

func (s *PolicyAdminService) CreatePolicy(ctx context.Context, req *CreatePolicyRequest) (*PolicyOne, error) {
	rc, err := adminCaller(ctx)
	if err != nil {
		return nil, err
	}
	tenant, err := s.perms.ScopedTenant(rc)
	if err != nil {
		return nil, err
	}
	var cond *permission.PolicyCondition
	if len(req.Condition) > 0 && string(req.Condition) != "null" {
		cond = &permission.PolicyCondition{}
		if err := json.Unmarshal(req.Condition, cond); err != nil {
			return nil, svcErr(ctx, http.StatusBadRequest, errcode.ValidationFailed, "`condition` is not a valid condition: "+err.Error())
		}
	}
	subject := permission.SubjectContext
	if req.Subject != nil && *req.Subject == "record" {
		subject = permission.SubjectRecord
	}
	effect := permission.EffectAllow
	if req.Effect != nil {
		effect = permission.ParsePolicyEffect(*req.Effect)
	}
	field := ""
	if req.Field != nil {
		field = *req.Field
	}
	var row permission.PolicyRow
	err = s.change(ctx, rc, audit.ActionCreate, func(ctx context.Context) (uuid.UUID, map[string][2]any, error) {
		var err error
		if row, err = s.perms.CreatePolicy(ctx, tenant, req.Entity, req.Action, req.Roles, req.Roles != nil, cond,
			optionalUUID(rc.UserID), field, &subject, effect); err != nil {
			return uuid.Nil, nil, err
		}
		return row.ID, policyFields(row, false), nil
	})
	if err != nil {
		return nil, err
	}
	return &PolicyOne{Data: policyView(row)}, nil
}

// SeedDefaultPolicies creates one context-subject, no-condition (pure RBAC) policy per action for
// the roles on the entity: the ergonomic counterpart to CreatePolicy now that PermissionService
// denies by default when an entity/action has no policy at all.
func (s *PolicyAdminService) SeedDefaultPolicies(ctx context.Context, req *SeedDefaultPoliciesRequest) (*PolicyList, error) {
	rc, err := adminCaller(ctx)
	if err != nil {
		return nil, err
	}
	tenant, err := s.perms.ScopedTenant(rc)
	if err != nil {
		return nil, err
	}
	if len(req.Roles) == 0 {
		return nil, svcErr(ctx, http.StatusBadRequest, errcode.ValidationFailed, "`roles` must not be empty.")
	}
	actions := req.Actions
	if len(actions) == 0 {
		actions = permission.EntityActionNames()
	}
	for _, a := range actions {
		if !permission.IsEntityAction(a) {
			return nil, unknownAction(ctx, a)
		}
	}
	createdBy := optionalUUID(rc.UserID)
	subject := permission.SubjectContext
	created := make([]*Policy, 0, len(actions))
	for _, action := range actions {
		var row permission.PolicyRow
		err := s.change(ctx, rc, audit.ActionCreate, func(ctx context.Context) (uuid.UUID, map[string][2]any, error) {
			var err error
			if row, err = s.perms.CreatePolicy(ctx, tenant, req.Entity, action, req.Roles, true, nil, createdBy, "", &subject, permission.EffectAllow); err != nil {
				return uuid.Nil, nil, err
			}
			return row.ID, policyFields(row, false), nil
		})
		if err != nil {
			return nil, err
		}
		created = append(created, policyView(row))
	}
	return &PolicyList{Data: created}, nil
}

// SyncMatrixPolicies is the permission matrix's single save call: it replaces every basic-shaped
// policy of the entity with exactly the grants, atomically. It never touches an advanced policy
// (condition, field, record subject, deny).
func (s *PolicyAdminService) SyncMatrixPolicies(ctx context.Context, req *SyncMatrixRequest) (*PolicyList, error) {
	rc, err := adminCaller(ctx)
	if err != nil {
		return nil, err
	}
	tenant, err := s.perms.ScopedTenant(rc)
	if err != nil {
		return nil, err
	}
	grants := make([]permission.RoleActionGrant, len(req.Grants))
	for i, g := range req.Grants {
		if !permission.IsEntityAction(g.Action) {
			return nil, unknownAction(ctx, g.Action)
		}
		role := ""
		if g.Role != nil {
			role = *g.Role
		}
		grants[i] = permission.RoleActionGrant{Role: role, HasRole: g.Role != nil, Action: g.Action}
	}
	// What the matrix held before, for the audit entry: read ahead of the replace.
	var before []string
	if old, err := s.perms.ListPolicies(ctx, tenant, req.Entity, true); err == nil {
		before = basicGrants(old)
	}
	var rows []permission.PolicyRow
	err = s.change(ctx, rc, audit.ActionUpdate, func(ctx context.Context) (uuid.UUID, map[string][2]any, error) {
		var err error
		if rows, err = s.perms.SyncBasicPolicies(ctx, tenant, req.Entity, grants, optionalUUID(rc.UserID)); err != nil {
			return uuid.Nil, nil, err
		}
		return namedID("policies:"+tenant.String(), req.Entity),
			map[string][2]any{"entity": {nil, req.Entity}, "grants": {before, basicGrants(rows)}}, nil
	})
	if err != nil {
		return nil, err
	}
	return &PolicyList{Data: policyViews(rows)}, nil
}

func (s *PolicyAdminService) DeletePolicy(ctx context.Context, req *PolicyIDRequest) (*Empty, error) {
	rc, err := adminCaller(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseUUID(ctx, "id", req.ID)
	if err != nil {
		return nil, err
	}
	tenant, err := s.perms.ScopedTenant(rc)
	if err != nil {
		return nil, err
	}
	// The policy as it was, for the audit entry: it is gone once deleted.
	var gone *permission.PolicyRow
	if all, err := s.perms.ListPolicies(ctx, tenant, "", false); err == nil {
		for i := range all {
			if all[i].ID == id {
				gone = &all[i]
				break
			}
		}
	}
	err = s.change(ctx, rc, audit.ActionDelete, func(ctx context.Context) (uuid.UUID, map[string][2]any, error) {
		if err := s.perms.DeletePolicy(ctx, tenant, id); err != nil {
			return uuid.Nil, nil, err
		}
		if gone == nil {
			return id, nil, nil
		}
		return id, policyFields(*gone, true), nil
	})
	if err != nil {
		return nil, err
	}
	return &Empty{}, nil
}

// ExplainPolicy says which policies decide an action on an entity for the caller.
func (s *PolicyAdminService) ExplainPolicy(ctx context.Context, req *ExplainPolicyRequest) (*ExplainResponse, error) {
	rc, err := adminCaller(ctx)
	if err != nil {
		return nil, err
	}
	field := ""
	if req.Field != nil {
		field = *req.Field
	}
	explanation, err := s.perms.Explain(ctx, rc, req.Entity, req.Action, field, req.Field != nil,
		permission.JsonObject(req.Record), req.Record != nil)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(explanation)
	if err != nil {
		return nil, err
	}
	return &ExplainResponse{Data: raw}, nil
}

// InvalidateContext drops a member's cached context attributes.
func (s *PolicyAdminService) InvalidateContext(ctx context.Context, req *UserIDRequest) (*Empty, error) {
	rc, err := adminCaller(ctx)
	if err != nil {
		return nil, err
	}
	userID, err := parseUUID(ctx, "userId", req.UserID)
	if err != nil {
		return nil, err
	}
	tenant, err := s.perms.ScopedTenant(rc)
	if err != nil {
		return nil, err
	}
	s.cache.Invalidate(tenant, userID)
	return &Empty{}, nil
}
