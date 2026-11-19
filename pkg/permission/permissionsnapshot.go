package permission

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
)

// JsonObject is a flat record data blob.
type JsonObject map[string]json.RawMessage

// PermissionSnapshot is a per-call batch of a tenant/entity's policies, loaded once and reused
// across a single CrudService call.
type PermissionSnapshot struct {
	fieldPolicies          []PolicyRow
	recordPoliciesByAction map[string][]PolicyRow
	// contextPoliciesByAction holds the entity-level (context-subject, no field) policies, so a
	// read path that already loaded the snapshot can answer "may this caller take action X on
	// this entity at all" for capabilities without another round trip per action.
	contextPoliciesByAction map[string][]PolicyRow
}

func LoadPermissionSnapshot(ctx context.Context, store PolicyStore, tenantID uuid.UUID, entity string) (*PermissionSnapshot, error) {
	rows, err := store.LoadAllPolicies(ctx, tenantID, entity)
	if err != nil {
		return nil, err
	}
	return PermissionSnapshotFromRows(rows), nil
}

func PermissionSnapshotFromRows(rows []PolicyRow) *PermissionSnapshot {
	var fieldPolicies []PolicyRow
	recordPoliciesByAction := map[string][]PolicyRow{}
	contextPoliciesByAction := map[string][]PolicyRow{}
	for _, row := range rows {
		if row.Field != "" {
			fieldPolicies = append(fieldPolicies, row)
		}
		if row.Field == "" && row.Subject == "record" {
			recordPoliciesByAction[row.Action] = append(recordPoliciesByAction[row.Action], row)
		}
		if row.Field == "" && row.Subject == "context" {
			contextPoliciesByAction[row.Action] = append(contextPoliciesByAction[row.Action], row)
		}
	}
	return &PermissionSnapshot{fieldPolicies: fieldPolicies, recordPoliciesByAction: recordPoliciesByAction,
		contextPoliciesByAction: contextPoliciesByAction}
}

// CanPerformEntityAction answers the entity-level check for action from the snapshot, with the
// same rules as PermissionService.checkAction (OAuth2 scope first, then admin bypass, then
// policies, default deny). Used for capabilities, so a caller is never shown an action the
// write path would then reject at the entity level.
func (s *PermissionSnapshot) CanPerformEntityAction(ctx *RequestContext, entityName string, action EntityAction) PermissionDecision {
	if !ctx.ScopePermits(entityName, action) {
		return Forbidden()
	}
	if ctx.IsAdmin() {
		return Allowed()
	}
	return contextVerdictGroups(s.contextPoliciesByAction[action.String()], ctx)
}

// CanTransitionAction is the snapshot counterpart of PermissionService.CanTransitionAction.
func (s *PermissionSnapshot) CanTransitionAction(ctx *RequestContext, entityName, action string) PermissionDecision {
	if !ctx.ScopePermits(entityName, ActionTransition) {
		return Forbidden()
	}
	if ctx.IsAdmin() {
		return Allowed()
	}
	rows := append(append([]PolicyRow{}, s.contextPoliciesByAction[ActionTransition.String()]...),
		s.contextPoliciesByAction[TransitionActionPolicy(action)]...)
	return contextVerdictGroups(rows, ctx)
}

func (s *PermissionSnapshot) GetRecordPolicies(action EntityAction) []PolicyRow {
	return s.recordPoliciesByAction[action.String()]
}

func (s *PermissionSnapshot) RequiredRelationFields(action EntityAction) []string {
	return RequiredRelationFields(s.GetRecordPolicies(action))
}

type matchStability int

const (
	stabilityAlways matchStability = iota
	stabilityNever
	stabilityRecordDependent
)

func matchStabilityOf(policy PolicyRow, ctx *RequestContext) matchStability {
	if !RoleGatePassed(policy.Roles, policy.HasRoles, ctx.Roles) {
		return stabilityNever
	}
	if policy.Condition == nil {
		return stabilityAlways
	}
	if policy.Subject == "record" {
		return stabilityRecordDependent
	}
	if EvaluateCondition(*policy.Condition, ctx.ToValue(), ctx).IsPassed() {
		return stabilityAlways
	}
	return stabilityNever
}

func verdictIsRecordDependent(policies []PolicyRow, ctx *RequestContext) bool {
	alwaysAllow, alwaysDeny := false, false
	steerableAllow, steerableDeny := false, false
	for _, policy := range policies {
		deny := policy.Effect == EffectDeny
		switch matchStabilityOf(policy, ctx) {
		case stabilityNever:
		case stabilityAlways:
			if deny {
				alwaysDeny = true
			} else {
				alwaysAllow = true
			}
		case stabilityRecordDependent:
			if deny {
				steerableDeny = true
			} else {
				steerableAllow = true
			}
		}
	}
	if alwaysDeny {
		return false
	}
	if steerableDeny {
		return true
	}
	return !alwaysAllow && steerableAllow
}

func (s *PermissionSnapshot) RecordAccessIsStateDependent(ctx *RequestContext, action EntityAction) bool {
	if ctx.IsAdmin() {
		return false
	}
	return verdictIsRecordDependent(s.GetRecordPolicies(action), ctx)
}

func (s *PermissionSnapshot) RecordStateDependentReadFields(ctx *RequestContext) map[string]bool {
	out := map[string]bool{}
	if ctx.IsAdmin() {
		return out
	}
	byField := map[string][]PolicyRow{}
	for _, policy := range s.fieldPolicies {
		if policy.Action != PolicyActionRead || policy.Field == "" {
			continue
		}
		byField[policy.Field] = append(byField[policy.Field], policy)
	}
	for field, policies := range byField {
		if verdictIsRecordDependent(policies, ctx) {
			out[field] = true
		}
	}
	return out
}

// FilterReadableFields: allow-by-default, unlike entity-level permission. A field with no read
// policy at all is included for anyone who can already read the entity.
func (s *PermissionSnapshot) FilterReadableFields(ctx *RequestContext, record JsonObject) JsonObject {
	if ctx.IsAdmin() {
		out := JsonObject{}
		for k, v := range record {
			out[k] = v
		}
		return out
	}
	readPoliciesByField := map[string][]PolicyRow{}
	for _, policy := range s.fieldPolicies {
		if policy.Action != PolicyActionRead || policy.Field == "" {
			continue
		}
		readPoliciesByField[policy.Field] = append(readPoliciesByField[policy.Field], policy)
	}
	recordValue, _ := json.Marshal(record)
	result := JsonObject{}
	for key, value := range record {
		policies, hasPolicies := readPoliciesByField[key]
		if !hasPolicies {
			result[key] = value
			continue
		}
		if EvaluatePolicies(policies, ctx, recordValue, true).IsAllowed() {
			result[key] = value
		}
	}
	return result
}

// WritableFields: allow-by-default, same asymmetry as FilterReadableFields.
func (s *PermissionSnapshot) WritableFields(ctx *RequestContext, allFieldNames []string, existingRecord JsonObject, hasExisting bool) []string {
	if ctx.IsAdmin() {
		out := make([]string, len(allFieldNames))
		copy(out, allFieldNames)
		return out
	}
	writePoliciesByField := map[string][]PolicyRow{}
	for _, policy := range s.fieldPolicies {
		if policy.Action != "write" || policy.Field == "" {
			continue
		}
		writePoliciesByField[policy.Field] = append(writePoliciesByField[policy.Field], policy)
	}
	var existingValue json.RawMessage
	if hasExisting {
		existingValue, _ = json.Marshal(existingRecord)
	}
	var out []string
	for _, field := range allFieldNames {
		policies, hasPolicies := writePoliciesByField[field]
		if !hasPolicies || EvaluatePolicies(policies, ctx, existingValue, hasExisting).IsAllowed() {
			out = append(out, field)
		}
	}
	return out
}

func (s *PermissionSnapshot) AssertWritableFields(ctx *RequestContext, payloadFields []string, existingRecord JsonObject, hasExisting bool) PermissionDecision {
	if ctx.IsAdmin() {
		return Allowed()
	}
	writable := s.WritableFields(ctx, payloadFields, existingRecord, hasExisting)
	writableSet := map[string]bool{}
	for _, f := range writable {
		writableSet[f] = true
	}
	for _, f := range payloadFields {
		if !writableSet[f] {
			return ForbiddenField(f)
		}
	}
	return Allowed()
}

// CanPerformRecordCondition: allow-by-default when NO record-level policy exists for action.
func (s *PermissionSnapshot) CanPerformRecordCondition(ctx *RequestContext, record JsonObject, action EntityAction) PermissionDecision {
	recordPolicies := s.GetRecordPolicies(action)
	if ctx.IsAdmin() || len(recordPolicies) == 0 {
		return Allowed()
	}
	recordValue, _ := json.Marshal(record)
	switch EvaluatePolicies(recordPolicies, ctx, recordValue, true) {
	case VerdictAllow:
		return Allowed()
	default:
		return Forbidden()
	}
}

// entityLevelAllowedFor is the entity-level verdict for ONE role set (no group fan-out): the
// OAuth2 scope, the admin bypass, then the context policies. workflowAction is set for a
// transition, where the per-action policies apply too.
func (s *PermissionSnapshot) entityLevelAllowedFor(ctx *RequestContext, entityName string, action EntityAction, workflowAction string) bool {
	if !ctx.ScopePermits(entityName, action) {
		return false
	}
	if ctx.IsAdmin() {
		return true
	}
	rows := s.contextPoliciesByAction[action.String()]
	if action == ActionTransition && workflowAction != "" {
		rows = append(append([]PolicyRow{}, rows...), s.contextPoliciesByAction[TransitionActionPolicy(workflowAction)]...)
	}
	return contextVerdict(rows, ctx).Allowed
}

// CanAccessRecord is CanPerformRecordCondition for a caller who may hold roles inside scopes: the
// record is reachable when some role group allows the action on the entity, covers the record,
// and passes the record-level policies under that group's roles alone. Without scoped roles it
// IS CanPerformRecordCondition, so nothing changes for a caller who has none.
//
// Unlike CanPerformRecordCondition it also re-checks the entity level per group, because the
// record-level check on its own allows everything when an entity has no record policies — which
// would hand a scoped caller every record.
func (s *PermissionSnapshot) CanAccessRecord(ctx *RequestContext, entityName string, bindings []ScopeBinding,
	recordID string, record JsonObject, action EntityAction, workflowAction string) PermissionDecision {
	if len(ctx.ScopedRoles) == 0 {
		return s.CanPerformRecordCondition(ctx, record, action)
	}
	for i, g := range ctx.RoleGroups() {
		gctx := ctx
		if i > 0 {
			gctx = ctx.WithRoles(g.Roles)
		}
		if !s.entityLevelAllowedFor(gctx, entityName, action, workflowAction) {
			continue
		}
		if !g.Covers(bindings, recordID, record) {
			continue
		}
		if s.CanPerformRecordCondition(gctx, record, action).Allowed {
			return Allowed()
		}
	}
	return Forbidden()
}

// CanCreateInScope is the create counterpart: the tenant-wide group may create anywhere; a
// scoped group only a record whose scope field lies inside its scopes (the record has no id yet,
// so a binding on field "id" can never cover a new record).
func (s *PermissionSnapshot) CanCreateInScope(ctx *RequestContext, entityName string, bindings []ScopeBinding, record JsonObject) PermissionDecision {
	if len(ctx.ScopedRoles) == 0 {
		return Allowed()
	}
	for i, g := range ctx.RoleGroups() {
		gctx := ctx
		if i > 0 {
			gctx = ctx.WithRoles(g.Roles)
		}
		if s.entityLevelAllowedFor(gctx, entityName, ActionCreate, "") && g.Covers(bindings, "", record) {
			return Allowed()
		}
	}
	return Forbidden()
}

// AccessGroup is a role group whose entity-level permission for an action is open, with its
// own record-level policies to apply under the group's roles.
type AccessGroup struct {
	Group RoleGroup
	// Ctx evaluates the group's policies (the caller's context holding the group's roles).
	Ctx *RequestContext
}

// AccessPlan is how a list or aggregate query must be restricted for a caller with scoped roles:
// the tenant-wide group (Wide, when its entity level allows the action) and the scoped groups.
// With neither, nothing is readable.
type AccessPlan struct {
	Wide   bool
	Groups []AccessGroup
}

// PlanAccess works out the plan for action. ok is false when the caller has no scoped roles and
// the ordinary path (record policies under ctx) applies unchanged.
func (s *PermissionSnapshot) PlanAccess(ctx *RequestContext, entityName string, action EntityAction) (plan AccessPlan, ok bool) {
	if len(ctx.ScopedRoles) == 0 {
		return AccessPlan{}, false
	}
	groups := ctx.RoleGroups()
	plan.Wide = s.entityLevelAllowedFor(ctx, entityName, action, "")
	for _, g := range groups[1:] {
		gctx := ctx.WithRoles(g.Roles)
		if s.entityLevelAllowedFor(gctx, entityName, action, "") {
			plan.Groups = append(plan.Groups, AccessGroup{Group: g, Ctx: gctx})
		}
	}
	return plan, true
}
