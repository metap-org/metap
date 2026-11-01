package crud

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/metap-org/metap-go/permission"
	"github.com/metap-org/metap-go/query"
)

// Aggregate mirrors the Rust CrudService::aggregate — the GROUP BY/COUNT/SUM sibling of List.
// Permission is the same gate List uses: entity-level Read must be allowed, and the caller's
// record-level (ABAC) read policies are applied to the rows *before* they are aggregated.
func (s *CrudService) AggregateWithInput(ctx context.Context, entityName string, input query.AggregateInput, rc *permission.RequestContext) (ServiceResult[[]json.RawMessage], error) {
	tenantID, err := s.permissions.ScopedTenant(rc)
	if err != nil {
		return ServiceResult[[]json.RawMessage]{}, err
	}
	md, err := s.registryFor(ctx, tenantID)
	if err != nil {
		return ServiceResult[[]json.RawMessage]{}, err
	}
	entity, ok := md.GetEntity(entityName)
	if !ok {
		return ResultErr[[]json.RawMessage](404, "entity_not_found"), nil
	}

	decision, err := s.permissions.CanReadEntity(ctx, rc, entity.Name)
	if err != nil {
		return ServiceResult[[]json.RawMessage]{}, err
	}
	if !decision.Allowed {
		return forbidden[[]json.RawMessage](decision), nil
	}

	snapshot, err := s.permissions.LoadSnapshot(ctx, tenantID, entity.Name)
	if err != nil {
		return ServiceResult[[]json.RawMessage]{}, err
	}
	recordPolicies := snapshot.GetRecordPolicies(permission.ActionRead)

	planned, err := query.PlanAggregate(md, s.permissions, entity.Name, input, rc, recordPolicies)
	if err != nil {
		var invalidAggregate *query.InvalidAggregateError
		var crossRecord *query.CrossRecordConditionInListError
		var unsupportedContains *query.UnsupportedContainsConditionInListError
		switch {
		case errors.As(err, &invalidAggregate):
			return ResultErrWithMessage[[]json.RawMessage](400, "invalid_aggregate", err.Error()), nil
		case errors.As(err, &crossRecord), errors.As(err, &unsupportedContains):
			return ResultErrWithMessage[[]json.RawMessage](500, "unsupported_policy_condition", err.Error()), nil
		}
		return ServiceResult[[]json.RawMessage]{}, err
	}

	tx, err := s.router.Begin(ctx, tenantID)
	if err != nil {
		if r, ok := routerUnavailable[[]json.RawMessage](err); ok {
			return r, nil
		}
		return ServiceResult[[]json.RawMessage]{}, err
	}
	rows, err := tx.Query(ctx, planned.SQL, query.ArgsOf(planned.Params)...)
	if err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		return ServiceResult[[]json.RawMessage]{}, err
	}
	// One jsonb column per row (to_jsonb(agg)), so the projection's shape — caller-defined,
	// unknown at compile time — never has to be described as typed columns.
	var data []json.RawMessage
	for rows.Next() {
		var raw json.RawMessage
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			tx.Rollback(ctx) //nolint:errcheck
			return ServiceResult[[]json.RawMessage]{}, err
		}
		data = append(data, raw)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		return ServiceResult[[]json.RawMessage]{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ServiceResult[[]json.RawMessage]{}, err
	}

	return ResultOk(data), nil
}
