package crud

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/tenancy"
	"github.com/metap-org/metap/pkg/workflow"
)

// The four record writes (Create, Update, Delete, Transition) are the same pipeline with a few
// steps that differ (roadmap 120, P2):
//
//	resolve         the tenant, the registry, the entity           (404 for an unknown entity)
//	authorizeEntity may this caller do this to this entity at all  (entity-level policy)
//	loadCurrent     the record as stored                           (update, delete, transition)
//	authorizeRecord may they do it to this record                  (record-level policy, scopes)
//	prepare         the data to write: pure, no I/O                (prepareCreate/Update/Transition)
//	persist         one transaction: write, events; then audit; then mask the answer
//
// Each Create/Update/Delete/Transition reads as that list. Everything that touches the database is
// in resolve's neighbours loadCurrent and persist, so the prepare functions are tested with plain
// values.

// call is one record write after the request is resolved: who is asking, in which tenant, about
// which entity.
type call struct {
	tenantID uuid.UUID
	registry *metadata.MetadataRegistry
	entity   metadata.EntityDefinition
	rc       *permission.RequestContext
}

// resolve finds the tenant, the metadata registry that tenant sees, and the entity.
func (s *CrudService) resolve(ctx context.Context, rc *permission.RequestContext, entityName string) (call, error) {
	tenantID, err := s.permissions.ScopedTenant(rc)
	if err != nil {
		return call{}, err
	}
	registry, err := s.registryFor(ctx, tenantID)
	if err != nil {
		return call{}, err
	}
	entity, ok := registry.GetEntity(entityName)
	if !ok {
		return call{}, NewServiceError(http.StatusNotFound, errcode.EntityNotFound)
	}
	return call{tenantID: tenantID, registry: registry, entity: entity, rc: rc}, nil
}

// authorizeEntity is the entity-level check, the one place a record write asks the role/policy
// question. workflowAction is the transition's action name (only for ActionTransition: a policy on
// `transition:<action>` grants that action alone).
func (s *CrudService) authorizeEntity(ctx context.Context, c call, verb permission.EntityAction, workflowAction string) error {
	var d permission.PermissionDecision
	var err error
	switch verb {
	case permission.ActionCreate:
		d, err = s.access.CanCreateEntity(ctx, c.rc, c.entity.Name)
	case permission.ActionUpdate:
		d, err = s.access.CanUpdateEntity(ctx, c.rc, c.entity.Name)
	case permission.ActionDelete:
		d, err = s.access.CanDeleteEntity(ctx, c.rc, c.entity.Name)
	case permission.ActionTransition:
		d, err = s.access.CanTransitionAction(ctx, c.rc, c.entity.Name, workflowAction)
	default:
		panic("authorizeEntity: unsupported verb " + verb.String())
	}
	if err != nil {
		return err
	}
	if !d.Allowed {
		return forbidden(d)
	}
	return nil
}

// current is the record a write starts from, read in its own short transaction before any
// decision is made about its content.
type current struct {
	record *RecordDto
	// actors is who last took each workflow action on the record; read only for an entity whose
	// workflow guards use it.
	actors map[string]string
}

// loadCurrent reads the stored record (404 when it does not exist). withActors also reads the
// workflow's last actors, which only a transition needs.
func (s *CrudService) loadCurrent(ctx context.Context, c call, id uuid.UUID, withActors bool) (*current, error) {
	tx, err := s.router.Begin(ctx, c.tenantID)
	if err != nil {
		if r, ok := routerUnavailable(err); ok {
			return nil, r
		}
		return nil, err
	}
	existing, err := fetchExisting(ctx, tx, id, c.tenantID, c.entity)
	if err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		return nil, err
	}
	if existing == nil {
		tx.Rollback(ctx) //nolint:errcheck
		return nil, NewServiceError(http.StatusNotFound, errcode.RecordNotFound)
	}
	cur := &current{record: existing}
	if withActors && workflow.UsesWorkflowContext(c.entity) {
		byRecord, err := workflow.LastActors(ctx, tx, c.tenantID, c.entity.Name, []uuid.UUID{id})
		if err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			return nil, err
		}
		cur.actors = byRecord[id]
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return cur, nil
}

// authorizeRecord is the record-level check: the caller's policies and scopes against this record
// as it is stored. It returns the entity's permission snapshot, which the later steps (writable
// fields, masking the answer) reuse.
func (s *CrudService) authorizeRecord(ctx context.Context, c call, cur *current, verb permission.EntityAction, workflowAction string) (*permission.PermissionSnapshot, error) {
	snapshot, err := s.permissions.LoadSnapshot(ctx, c.tenantID, c.entity.Name)
	if err != nil {
		return nil, err
	}
	enriched, err := s.enrichRecordForActions(ctx, c.entity, snapshot, []permission.EntityAction{verb}, c.tenantID, c.registry, cur.record.Data)
	if err != nil {
		return nil, err
	}
	d := snapshot.CanAccessRecord(c.rc, c.entity.Name, c.entity.Scopes, cur.record.ID.String(), permission.JsonObject(enriched), verb, workflowAction)
	if !d.Allowed {
		return nil, forbidden(d)
	}
	return snapshot, nil
}

// write is what persist needs to run one record write: the statement, how its failures read, the
// events to enqueue with it, and the audit entry to record after it.
type write struct {
	userID *uuid.UUID
	reason *string
	sql    string
	args   []any
	// unique: a unique violation is answered as the entity's field error (create, update).
	unique bool
	// versioned: no row back means the record's version moved: 409 version_conflict (update,
	// delete, transition).
	versioned bool
	// before runs inside the transaction ahead of the write; an error rolls everything back (delete
	// uses it for the reference-integrity check).
	before func(ctx context.Context, tx pgx.Tx) error
	// emit enqueues the outbox events in the same transaction.
	emit func(ctx context.Context, tx pgx.Tx, record RecordDto) error
	// after runs once the transaction has committed, ahead of the audit entry.
	after func()

	auditAction      audit.AuditAction
	transitionAction *string
	// diff is the audit entry's change set, computed from the record as written.
	diff func(record RecordDto) audit.JsonObject
}

// persist runs the write in one transaction, records the audit entry once it has committed, and
// returns the written record as the caller may read it.
func (s *CrudService) persist(ctx context.Context, c call, snapshot *permission.PermissionSnapshot, w write) (RecordDto, error) {
	tx, err := s.router.Begin(ctx, c.tenantID)
	if err != nil {
		if r, ok := routerUnavailable(err); ok {
			return RecordDto{}, r
		}
		return RecordDto{}, err
	}
	if w.before != nil {
		if err := w.before(ctx, tx); err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			return RecordDto{}, err
		}
	}
	record, err := scanRecordRow(tx.QueryRow(ctx, w.sql, w.args...), c.entity.Name)
	if err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		if w.unique {
			if r, ok := uniqueViolation(c.entity, err); ok {
				return RecordDto{}, r
			}
		}
		if w.versioned && errors.Is(err, pgx.ErrNoRows) {
			return RecordDto{}, NewServiceError(http.StatusConflict, errcode.VersionConflict)
		}
		return RecordDto{}, err
	}
	if err := w.emit(ctx, tx, record); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		return RecordDto{}, err
	}
	// The audit entry goes into the write's own transaction (the outbox), so a committed write
	// always has its entry and a rolled-back one never does.
	versionAfter := record.Version
	if entry, ok := s.auditEntryFor(c.entity, record.Data, audit.Entry{
		TenantID:         c.tenantID,
		Entity:           c.entity.Name,
		RecordID:         record.ID,
		Action:           w.auditAction,
		TransitionAction: w.transitionAction,
		ActorUserID:      w.userID,
		Reason:           w.reason,
		Diff:             w.diff(record),
		VersionAfter:     &versionAfter,
		OccurredAt:       time.Now().UTC(),
	}); ok {
		if err := audit.Enqueue(ctx, tx, entry); err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			return RecordDto{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return RecordDto{}, err
	}
	// What follows the commit (the context-cache invalidation) must not happen for a write whose
	// enclosing unit of work later rolls back, so it waits for that unit when there is one and runs
	// now when there is not.
	if w.after != nil {
		tenancy.OnCommit(ctx, w.after)
	}
	return maskRecordForRead(c.entity, c.rc, snapshot, record), nil
}
