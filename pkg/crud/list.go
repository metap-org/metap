package crud

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/query"
)

func (s *CrudService) List(ctx context.Context, entityName string, input query.ListInput, rc *permission.RequestContext) (ListResult, error) {
	tenantID, err := s.permissions.ScopedTenant(rc)
	if err != nil {
		return ListResult{}, err
	}
	md, err := s.registryFor(ctx, tenantID)
	if err != nil {
		return ListResult{}, err
	}
	entity, ok := md.GetEntity(entityName)
	if !ok {
		return ListResult{}, NewServiceError(http.StatusNotFound, errcode.EntityNotFound)
	}

	decision, err := s.permissions.CanReadEntity(ctx, rc, entity.Name)
	if err != nil {
		return ListResult{}, err
	}
	if !decision.Allowed {
		return ListResult{}, forbidden(decision)
	}

	snapshot, err := s.permissions.LoadSnapshot(ctx, tenantID, entity.Name)
	if err != nil {
		return ListResult{}, err
	}
	recordPolicies := snapshot.GetRecordPolicies(permission.ActionRead)

	planned, err := query.PlanList(md, s.permissions, entity.Name, input, rc, recordPolicies)
	if err != nil {
		var invalidCursor *query.InvalidCursorError
		var unknownListView *query.UnknownListViewError
		var invalidOffset *query.InvalidOffsetError
		var searchUnsupported *query.SearchNotSupportedError
		var invalidJql *query.InvalidJqlError
		var crossRecord *query.CrossRecordConditionInListError
		var unsupportedContains *query.UnsupportedContainsConditionInListError
		switch {
		case errors.As(err, &invalidCursor):
			return ListResult{}, NewServiceErrorMsg(http.StatusBadRequest, errcode.InvalidCursor, err.Error())
		case errors.As(err, &invalidOffset):
			return ListResult{}, NewServiceErrorMsg(http.StatusBadRequest, errcode.InvalidOffset, err.Error())
		case errors.As(err, &searchUnsupported):
			return ListResult{}, NewServiceErrorMsg(http.StatusBadRequest, errcode.SearchNotSupported, err.Error())
		case errors.As(err, &unknownListView):
			return ListResult{}, NewServiceErrorMsg(http.StatusBadRequest, "unknown_list_view", err.Error())
		case errors.As(err, &invalidJql):
			return ListResult{}, NewServiceErrorMsg(http.StatusBadRequest, errcode.InvalidJQL, err.Error())
		case errors.As(err, &crossRecord), errors.As(err, &unsupportedContains):
			return ListResult{}, NewServiceErrorMsg(http.StatusInternalServerError, errcode.UnsupportedPolicyCondition, err.Error())
		}
		return ListResult{}, err
	}

	sql := fmt.Sprintf("SELECT %s FROM %s t WHERE %s ORDER BY %s LIMIT %d OFFSET %d",
		recordSelectList(entity), entity.TableName, planned.WhereSQL, planned.OrderBySQL, planned.Limit+1, planned.Offset)

	tx, err := s.router.Begin(ctx, tenantID)
	if err != nil {
		if r, ok := routerUnavailable(err); ok {
			return ListResult{}, r
		}
		return ListResult{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	rows, err := tx.Query(ctx, sql, query.ArgsOf(planned.Params)...)
	if err != nil {
		return ListResult{}, err
	}
	var pageDtos []RecordDto
	for rows.Next() {
		dto, err := scanRecordRow(rows, entity.Name)
		if err != nil {
			rows.Close()
			return ListResult{}, err
		}
		pageDtos = append(pageDtos, dto)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return ListResult{}, err
	}

	var total *int64
	if input.WithTotal && input.Cursor == nil {
		var n int64
		countSQL := fmt.Sprintf("SELECT count(*) FROM %s t WHERE %s", entity.TableName, planned.CountWhereSQL)
		if err := tx.QueryRow(ctx, countSQL, query.ArgsOf(planned.Params)...).Scan(&n); err != nil {
			return ListResult{}, err
		}
		total = &n
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
		return ListResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ListResult{}, err
	}

	caps := EntityCapabilities{
		CanCreate: snapshot.CanPerformEntityAction(rc, entity.Name, permission.ActionCreate).Allowed,
		CanUpdate: snapshot.CanPerformEntityAction(rc, entity.Name, permission.ActionUpdate).Allowed,
		CanDelete: snapshot.CanPerformEntityAction(rc, entity.Name, permission.ActionDelete).Allowed,
	}
	return ListResult{Capabilities: caps, Data: data, Page: PageInfo{Limit: planned.Limit, NextCursor: nextCursor, Offset: planned.Offset, Total: total}}, nil
}
