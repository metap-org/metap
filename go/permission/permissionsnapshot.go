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
	for _, row := range rows {
		if row.Field != "" {
			fieldPolicies = append(fieldPolicies, row)
		}
		if row.Field == "" && row.Subject == "record" {
			recordPoliciesByAction[row.Action] = append(recordPoliciesByAction[row.Action], row)
		}
	}
	return &PermissionSnapshot{fieldPolicies: fieldPolicies, recordPoliciesByAction: recordPoliciesByAction}
}

func (s *PermissionSnapshot) GetRecordPolicies(action EntityAction) []PolicyRow {
	return s.recordPoliciesByAction[action.String()]
}

func (s *PermissionSnapshot) RequiredRelationFields(action EntityAction) []string {
	return RequiredRelationFields(s.GetRecordPolicies(action))
}

// matchStability mirrors the Rust MatchStability enum.
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
		if policy.Action != "read" || policy.Field == "" {
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
		if policy.Action != "read" || policy.Field == "" {
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
