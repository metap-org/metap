package crud

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/workflow"
)

// Delete soft-deletes a record: resolve, authorize the entity, load the record, authorize it,
// persist (after checking nothing else still points at it).
func (s *CrudService) Delete(ctx context.Context, entityName string, id uuid.UUID, expectedVersion int32, rc *permission.RequestContext, reason *string) (RecordDto, error) {
	c, err := s.resolve(ctx, rc, entityName)
	if err != nil {
		return RecordDto{}, err
	}
	if err := s.authorizeEntity(ctx, c, permission.ActionDelete, ""); err != nil {
		return RecordDto{}, err
	}
	cur, err := s.loadCurrent(ctx, c, id, false)
	if err != nil {
		return RecordDto{}, err
	}
	snapshot, err := s.authorizeRecord(ctx, c, cur, permission.ActionDelete, "")
	if err != nil {
		return RecordDto{}, err
	}
	userID, err := parseUserID(rc)
	if err != nil {
		return RecordDto{}, err
	}

	deleteSQL := fmt.Sprintf(
		`UPDATE %s AS t SET deleted = true, version = t.version + 1, updated_at = now(), updated_by = $1
		 WHERE t.id = $2 AND t.tenant_id = $3 AND t.version = $4 AND t.deleted = false RETURNING %s`,
		c.entity.TableName, recordSelectList(c.entity),
	)
	return s.persist(ctx, c, snapshot, write{
		userID: userID, reason: reason,
		sql:       deleteSQL,
		args:      []any{userID, id, c.tenantID, expectedVersion},
		versioned: true,
		previous:  cur.record.Data,
		before: func(ctx context.Context, tx pgx.Tx) error {
			return s.assertNotReferenced(ctx, tx, c, id)
		},
		emit: func(ctx context.Context, tx pgx.Tx, record RecordDto) error {
			return workflow.EmitDeleted(ctx, tx, c.entity, c.tenantID, record.ID)
		},
		auditAction: audit.ActionDelete,
		// Action-based, not diff-based: RecordDto never exposes the deleted column.
		diff: func(RecordDto) audit.JsonObject { return audit.JsonObject{} },
	})
}

// assertNotReferenced is the reference-integrity guard: deleting a record that another record still
// points to through a Reference field would leave a silent orphan reference. It runs in the same
// transaction as the delete itself, right before it.
func (s *CrudService) assertNotReferenced(ctx context.Context, tx pgx.Tx, c call, id uuid.UUID) error {
	refs := referencingFields(c.registry, c.entity.Name)
	blockers, err := findReferencingRecords(ctx, tx, c.tenantID, id, refs)
	if err != nil {
		return err
	}
	if len(blockers) == 0 {
		return nil
	}
	first := blockers[0]
	// Keyed "<entity>.<field>" (not just the field name) so the same field name on two different
	// referencing entities doesn't collide; the value is each blocker's own record id.
	fieldErrors := map[string][]string{}
	for _, hit := range blockers {
		key := hit.entity + "." + hit.field
		fieldErrors[key] = append(fieldErrors[key], hit.id.String())
	}
	return NewServiceErrorMsgFields(http.StatusConflict, errcode.RecordReferenced,
		fmt.Sprintf("This record is referenced by %q on %q and cannot be deleted.", first.field, first.entity),
		fieldErrors)
}
