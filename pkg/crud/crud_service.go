package crud

import (
	"context"
	"encoding/json"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
)

// CrudService is the only thing a transport layer calls for record operations. Orchestrates:
// permission check -> field-metadata-driven validation -> query planning -> DB write -> workflow
// status assignment -> outbox enqueue.
// Metadata is an *atomic.Pointer[metadata.MetadataRegistry]. Every method here loads a snapshot
// once, synchronously, near the top (via registryFor) and uses that same snapshot for the rest of
// the call.
type CrudService struct {
	router      TxBeginner
	metadata    *atomic.Pointer[metadata.MetadataRegistry]
	permissions PermissionChecker
	// access answers the entity-level question for the entity being read or written. It is the
	// same PermissionChecker unless the service came from Trusted. Reads of OTHER entities (related
	// display) always go to permissions: Trusted skips the question about the entity a job works
	// on, not what a record may show of the entities it points to.
	access EntityAccess

	// audit is the opt-in general-purpose audit trail — nil (via NewCrudService) preserves every
	// existing deployment's behavior exactly; WithAudit is the second, additive constructor a
	// binary opts into.
	auditStore audit.Store

	// contextInvalidation closes the gap where automatic AUTH_CONTEXT_ENTITY cache invalidation
	// used to live only inside a now-removed REST update handler — nil (the default) preserves
	// every existing deployment's behavior exactly.
	contextInvalidation *contextInvalidationConfig

	// metadataResolver is opt-in per-tenant metadata resolution — nil (the default) preserves
	// today's behavior exactly: every tenant shares the one Metadata snapshot above.
	metadataResolver metadata.MetadataResolver
}

type contextInvalidationConfig struct {
	// entityName is the entity AUTH_CONTEXT_ENTITY resolves to — only an Update on *this* entity
	// triggers invalidation.
	entityName string
	cache      ContextInvalidator
}

func NewCrudService(router TxBeginner, md *atomic.Pointer[metadata.MetadataRegistry], permissions PermissionChecker) *CrudService {
	return &CrudService{router: router, metadata: md, permissions: permissions, access: permissions}
}

// WithAudit is the same as NewCrudService, plus an audit trail sink every create/update/
// delete/transition records to — but only for an entity whose own EntityAuditConfig.Enabled is
// true.
func WithAudit(router TxBeginner, md *atomic.Pointer[metadata.MetadataRegistry], permissions PermissionChecker, store audit.Store) *CrudService {
	return &CrudService{router: router, metadata: md, permissions: permissions, access: permissions, auditStore: store}
}

// Trusted returns a copy of the service that does not ask the entity-level question: the caller is
// code (a job, a bootstrap, a sweep) acting on every entity of its own service, not a person whose
// roles bound what they may touch. Record-level policy, field rules, scopes and masking still apply
// to whoever the RequestContext names, so a trusted service run with a user's context still cannot
// show them a record or field their policies hide.
//
// The name is the point: a reader sees at the call site that the role check is off. Everything
// else reaches a CrudService through NewCrudService or WithAudit, which check.
func (s *CrudService) Trusted() *CrudService {
	c := *s
	c.access = trustedAccess{}
	return &c
}

// WithContextInvalidation is the opt-in AUTH_CONTEXT_ENTITY cache invalidation — chainable onto
// the result of NewCrudService or WithAudit. cache should be the *same* ContextAttributesCache
// instance ResolveRequestContext reads from — a separate instance would invalidate a cache
// nothing actually reads.
// ContextInvalidator drops what is cached about a caller's context attributes (httpapi's
// ContextAttributesCache is the one implementation).
type ContextInvalidator interface {
	Invalidate(tenantID, userID uuid.UUID)
}

func (s *CrudService) WithContextInvalidation(entityName string, cache ContextInvalidator) *CrudService {
	s.contextInvalidation = &contextInvalidationConfig{entityName: entityName, cache: cache}
	return s
}

// WithMetadataResolver is the opt-in per-tenant metadata resolution. Every existing caller that
// never calls this keeps resolving against the plain atomic.Pointer snapshot, unchanged.
func (s *CrudService) WithMetadataResolver(resolver metadata.MetadataResolver) *CrudService {
	s.metadataResolver = resolver
	return s
}

// registryFor is the one place every method below resolves "the registry this call should see".
// nil resolver (the default) falls back to the plain atomic.Pointer snapshot — today's
// single-global-registry behavior exactly.
func (s *CrudService) registryFor(ctx context.Context, tenantID uuid.UUID) (*metadata.MetadataRegistry, error) {
	if s.metadataResolver != nil {
		return s.metadataResolver.Resolve(ctx, tenantID)
	}
	return s.metadata.Load(), nil
}

// auditEntryFor completes one audit-trail entry for the write that is about to commit, or reports
// false when there is nothing to record: no audit store is configured on this service, or the
// entity does not opt in (entity.Audit). data is the record as written (read back from the row), the
// source of the entry's parent when the entity declares EntityAuditConfig.Parent.
//
// The entry is written to the outbox in the write's own transaction (audit.Enqueue), so it exists
// exactly when the write does; the audit drain moves it into the tenant's chain afterwards.
func (s *CrudService) auditEntryFor(entity metadata.EntityDefinition, data JsonObject, entry audit.Entry) (audit.Entry, bool) {
	if s.auditStore == nil {
		return entry, false
	}
	config := entity.Audit
	if config == nil || !config.Enabled {
		return entry, false
	}
	// Write-side redaction, applied here rather than at each of the 4 call sites precisely
	// because this is the one choke point all of them pass through.
	audit.RedactDiffFields(entry.Diff, config.RedactedFields)
	if parentEntity := entity.AuditParentEntity(); parentEntity != "" {
		if id, ok := parentIDOf(data, config.Parent); ok {
			entry.ParentEntity, entry.ParentID = &parentEntity, &id
		}
	}
	return entry, true
}

// parentIDOf reads the parent reference field's value (a uuid string) from a record's data; false
// when it is absent, null or not a uuid (a record with no parent, e.g. a tenant-wide rule).
func parentIDOf(data JsonObject, field string) (uuid.UUID, bool) {
	raw, ok := data[field]
	if !ok {
		return uuid.Nil, false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil || s == "" {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(s)
	return id, err == nil
}

// invalidateContextCacheIfConfigured no-ops unless contextInvalidation is configured *and*
// entityName matches its entityName. data must be the full, unmasked write payload — the
// AUTH_CONTEXT_ENTITY record's own userId field is business data, unrelated to which fields the
// *actor performing this update* can read back. Only ever called from Update.
func (s *CrudService) invalidateContextCacheIfConfigured(entityName string, tenantID uuid.UUID, data JsonObject) {
	config := s.contextInvalidation
	if config == nil || config.entityName != entityName {
		return
	}
	raw, ok := data["userId"]
	if !ok {
		return
	}
	var userIDStr string
	if err := json.Unmarshal(raw, &userIDStr); err != nil {
		return
	}
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return
	}
	config.cache.Invalidate(tenantID, userID)
}

// enrichRecordForActions resolves cross-record permission conditions (a record-level policy's
// condition referencing a related record via a dotted attribute path, resolved one hop through a
// Reference field). Builds a *copy* of record with each such relation field's value replaced by
// the related record's own data — never mutates the caller's copy. Only runs when
// snapshot.RequiredRelationFields(action) (for the union of actions) is non-empty.
func (s *CrudService) enrichRecordForActions(ctx context.Context, entity metadata.EntityDefinition, snapshot *permission.PermissionSnapshot,
	actions []permission.EntityAction, tenantID uuid.UUID, registry *metadata.MetadataRegistry, record JsonObject) (JsonObject, error) {
	var relationFields []string
	seen := map[string]bool{}
	for _, action := range actions {
		for _, field := range snapshot.RequiredRelationFields(action) {
			if !seen[field] {
				seen[field] = true
				relationFields = append(relationFields, field)
			}
		}
	}
	if len(relationFields) == 0 {
		return record, nil
	}

	enriched := make(JsonObject, len(record))
	for k, v := range record {
		enriched[k] = v
	}

	tx, err := s.router.Begin(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	for _, fieldName := range relationFields {
		field := findField(entity, fieldName)
		if field == nil || field.Kind != metadata.KindReference || field.RefEntity == nil {
			continue
		}
		refRaw, ok := record[fieldName]
		if !ok {
			continue
		}
		var refIDStr string
		if json.Unmarshal(refRaw, &refIDStr) != nil {
			continue
		}
		refID, err := uuid.Parse(refIDStr)
		if err != nil {
			continue
		}
		refEntityDef, ok := registry.GetEntity(*field.RefEntity)
		if !ok {
			continue
		}
		relatedData, err := fetchRelatedData(ctx, tx, refID, tenantID, refEntityDef)
		if err != nil {
			return nil, err
		}
		if relatedData != nil {
			b, _ := json.Marshal(relatedData)
			enriched[fieldName] = b
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return enriched, nil
}

func findField(entity metadata.EntityDefinition, name string) *metadata.EntityField {
	for i := range entity.Fields {
		if entity.Fields[i].Name == name {
			return &entity.Fields[i]
		}
	}
	return nil
}

// displayReferenceFields are the Reference fields of entity a list view displays (they name a
// RefDisplayField).
func displayReferenceFields(entity metadata.EntityDefinition) []metadata.EntityField {
	var out []metadata.EntityField
	for _, f := range entity.Fields {
		if f.Kind == metadata.KindReference && f.RefDisplayField != nil {
			out = append(out, f)
		}
	}
	return out
}

// relatedAccess holds, for each related entity a list displays, the permission snapshot of the
// ones the caller may read at all (an entity the caller cannot read is absent).
type relatedAccess map[string]*permission.PermissionSnapshot

// loadRelatedAccess reads the related entities' permissions. It runs before the list opens its
// transaction: a snapshot is a database read of its own, and asking for one while a transaction
// holds a connection takes a second connection per request, which with enough concurrent lists
// leaves every pooled connection waiting for another.
func (s *CrudService) loadRelatedAccess(ctx context.Context, entity metadata.EntityDefinition, rc *permission.RequestContext, tenantID uuid.UUID) (relatedAccess, error) {
	out := relatedAccess{}
	for _, f := range displayReferenceFields(entity) {
		ref := *f.RefEntity
		if _, done := out[ref]; done {
			continue
		}
		decision, err := s.permissions.CanReadEntity(ctx, rc, ref)
		if err != nil {
			return nil, err
		}
		if !decision.Allowed {
			continue
		}
		snapshot, err := s.permissions.LoadSnapshot(ctx, tenantID, ref)
		if err != nil {
			return nil, err
		}
		out[ref] = snapshot
	}
	return out, nil
}

// hydrateRelatedDisplay is the "Mode 2" batch display hydration — the list-only counterpart to
// enrichRecordForActions, but for a different purpose: that method resolves relations
// *permission conditions* need, invisibly; this resolves relations a *list view* wants to
// display (any Reference field that declares RefDisplayField), and its result —
// RecordDto.RelatedDisplay — is exactly what gets returned to the caller. One batched
// WHERE id = ANY($1) query per such field for the whole page, not per row.
//
// Runs after field-level read masking so a field the caller isn't allowed to see at all can't get
// a display value either, skips a field entirely if the caller can't read its target entity at
// all, and evaluates each related record's own record-level read policy before including its
// display value: a display convenience must not leak a value the caller would get a 403 for
// reading directly.
func (s *CrudService) hydrateRelatedDisplay(ctx context.Context, entity metadata.EntityDefinition, rc *permission.RequestContext,
	tenantID uuid.UUID, registry *metadata.MetadataRegistry, tx pgx.Tx, records []RecordDto, access relatedAccess) ([]RecordDto, error) {
	displayFields := displayReferenceFields(entity)
	if len(displayFields) == 0 {
		return records, nil
	}

	resolved := map[string]map[uuid.UUID]string{} // field name -> (related record id -> display value)
	for _, field := range displayFields {
		refEntity := *field.RefEntity
		displayField := *field.RefDisplayField

		if _, ok := access[refEntity]; !ok {
			continue // the caller cannot read the related entity at all
		}

		idSet := map[uuid.UUID]bool{}
		for _, r := range records {
			raw, ok := r.Data[field.Name]
			if !ok {
				continue
			}
			var s string
			if json.Unmarshal(raw, &s) != nil {
				continue
			}
			id, err := uuid.Parse(s)
			if err != nil {
				continue
			}
			idSet[id] = true
		}
		if len(idSet) == 0 {
			continue
		}
		ids := make([]uuid.UUID, 0, len(idSet))
		for id := range idSet {
			ids = append(ids, id)
		}

		refEntityDef, ok := registry.GetEntity(refEntity)
		if !ok {
			continue
		}

		related, err := fetchRelatedRecordsBatch(ctx, tx, ids, tenantID, refEntityDef)
		if err != nil {
			return nil, err
		}
		if len(related) == 0 {
			continue
		}

		relatedSnapshot := access[refEntity]
		values := map[uuid.UUID]string{}
		for id, relatedData := range related {
			recordDecision := relatedSnapshot.CanAccessRecord(rc, refEntity, refEntityDef.Scopes, id.String(), permission.JsonObject(relatedData), permission.ActionRead, "")
			if !recordDecision.Allowed {
				continue
			}
			raw, ok := relatedData[displayField]
			if !ok {
				continue
			}
			var v string
			if json.Unmarshal(raw, &v) == nil {
				values[id] = v
			}
		}
		resolved[field.Name] = values
	}

	if len(resolved) == 0 {
		return records, nil
	}

	out := make([]RecordDto, len(records))
	for i, record := range records {
		display := map[string]string{}
		for _, field := range displayFields {
			values, ok := resolved[field.Name]
			if !ok {
				continue
			}
			raw, ok := record.Data[field.Name]
			if !ok {
				continue
			}
			var s string
			if json.Unmarshal(raw, &s) != nil {
				continue
			}
			id, err := uuid.Parse(s)
			if err != nil {
				continue
			}
			if v, ok := values[id]; ok {
				display[field.Name] = v
			}
		}
		if len(display) > 0 {
			record.RelatedDisplay = display
		}
		out[i] = record
	}
	return out, nil
}
