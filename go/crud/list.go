package crud

import (
	"context"
	"errors"
	"fmt"

	"github.com/metap-org/metap-go/permission"
	"github.com/metap-org/metap-go/query"
)

// List mirrors the Rust CrudService::list.
func (s *CrudService) List(ctx context.Context, entityName string, input query.ListInput, rc *permission.RequestContext) (ServiceResult[[]RecordDto], error) {
	tenantID, err := s.permissions.ScopedTenant(rc)
	if err != nil {
		return ServiceResult[[]RecordDto]{}, err
	}
	md, err := s.registryFor(ctx, tenantID)
	if err != nil {
		return ServiceResult[[]RecordDto]{}, err
	}
	entity, ok := md.GetEntity(entityName)
	if !ok {
		return ResultErr[[]RecordDto](404, "entity_not_found"), nil
	}

	decision, err := s.permissions.CanReadEntity(ctx, rc, entity.Name)
	if err != nil {
		return ServiceResult[[]RecordDto]{}, err
	}
	if !decision.Allowed {
		return forbidden[[]RecordDto](decision), nil
	}

	snapshot, err := s.permissions.LoadSnapshot(ctx, tenantID, entity.Name)
	if err != nil {
		return ServiceResult[[]RecordDto]{}, err
	}
	recordPolicies := snapshot.GetRecordPolicies(permission.ActionRead)

	planned, err := query.PlanList(md, s.permissions, entity.Name, input, rc, recordPolicies)
	if err != nil {
		var invalidCursor *query.InvalidCursorError
		var unknownListView *query.UnknownListViewError
		var invalidJql *query.InvalidJqlError
		var crossRecord *query.CrossRecordConditionInListError
		var unsupportedContains *query.UnsupportedContainsConditionInListError
		switch {
		case errors.As(err, &invalidCursor):
			return ResultErrWithMessage[[]RecordDto](400, "invalid_cursor", err.Error()), nil
		case errors.As(err, &unknownListView):
			return ResultErrWithMessage[[]RecordDto](400, "unknown_list_view", err.Error()), nil
		case errors.As(err, &invalidJql):
			return ResultErrWithMessage[[]RecordDto](400, "invalid_jql", err.Error()), nil
		case errors.As(err, &crossRecord), errors.As(err, &unsupportedContains):
			return ResultErrWithMessage[[]RecordDto](500, "unsupported_policy_condition", err.Error()), nil
		}
		return ServiceResult[[]RecordDto]{}, err
	}

	sql := fmt.Sprintf("SELECT %s FROM %s WHERE %s ORDER BY %s LIMIT %d",
		recordColumns, entity.TableName, planned.WhereSQL, planned.OrderBySQL, planned.Limit+1)

	tx, err := s.router.Begin(ctx, tenantID)
	if err != nil {
		if r, ok := routerUnavailable[[]RecordDto](err); ok {
			return r, nil
		}
		return ServiceResult[[]RecordDto]{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	rows, err := tx.Query(ctx, sql, query.ArgsOf(planned.Params)...)
	if err != nil {
		return ServiceResult[[]RecordDto]{}, err
	}
	var pageDtos []RecordDto
	for rows.Next() {
		dto, err := scanRecordRow(rows, entity.Name)
		if err != nil {
			rows.Close()
			return ServiceResult[[]RecordDto]{}, err
		}
		pageDtos = append(pageDtos, dto)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return ServiceResult[[]RecordDto]{}, err
	}

	hasMore := int64(len(pageDtos)) > planned.Limit
	if hasMore {
		pageDtos = pageDtos[:planned.Limit]
	}

	var nextCursor *string
	if hasMore && len(pageDtos) > 0 {
		last := pageDtos[len(pageDtos)-1]
		dir := query.SortAsc
		if planned.ResolvedSort.Descending {
			dir = query.SortDesc
		}
		c := query.EncodeCursor(query.Cursor{
			Field: planned.ResolvedSort.Field,
			Value: sortFieldValue(last, planned.ResolvedSort.Field),
			ID:    last.ID.String(),
			Dir:   dir,
		})
		nextCursor = &c
	}

	data := make([]RecordDto, len(pageDtos))
	for i, dto := range pageDtos {
		data[i] = maskRecordForRead(entity, rc, snapshot, dto)
	}
	data, err = s.hydrateRelatedDisplay(ctx, entity, rc, tenantID, md, tx, data)
	if err != nil {
		return ServiceResult[[]RecordDto]{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ServiceResult[[]RecordDto]{}, err
	}

	return ResultOkWithPage(data, PageInfo{Limit: planned.Limit, NextCursor: nextCursor}), nil
}
