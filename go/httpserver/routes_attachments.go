// Generic /api/{entity}/{id}/attachments* — file attachments for any entity, any app,
// parameterized by :entity the same way /api/{entity}/{id}/transitions/{action} already is.
// Always mounted regardless of whether AppState.ObjectStore is configured — an unconfigured app
// just gets a 503 here, same shape any other optional feature degrades to.
//
// Deliberately not routed through CrudService: an attachment record isn't a metadata-driven
// entity (RecordID can't carry a typed FK to "whichever entity"), so permission is checked
// directly against the *target* entity's policies via CrudService.CheckRecordPermission — "can I
// edit this issue" is exactly "can I attach a file to it". Ported from
// crates/metap-http/src/routes/attachments.rs.
package httpserver

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/metap-org/metap-go/attachments"
	"github.com/metap-org/metap-go/permission"
)

func attachmentsTableFor(state *AppState, entity string) string {
	if t, ok := state.AttachmentTables[entity]; ok {
		return t
	}
	return "attachments"
}

// ensureRecordPermission checks entity-level and record-level (ABAC) permission for action
// against recordID, writing the error response itself on failure.
func ensureRecordPermission(w http.ResponseWriter, r *http.Request, state *AppState, entity string, recordID uuid.UUID,
	action permission.EntityAction, rc *permission.RequestContext) bool {
	result, err := state.Crud.CheckRecordPermission(r.Context(), entity, recordID, action, rc)
	if err != nil {
		writeInternalError(w, err)
		return false
	}
	if !result.IsOk {
		writeCrudServiceError(w, result.Status, result.ErrorCode, result.Message, result.FieldErrors)
		return false
	}
	return true
}

func attachmentToJSON(record *attachments.Record) map[string]any {
	var contentType any
	if record.ContentType != nil {
		contentType = *record.ContentType
	}
	return map[string]any{
		"id": record.ID, "entityName": record.EntityName, "recordId": record.RecordID,
		"filename": record.Filename, "key": record.Key, "size": record.Size,
		"contentType": contentType, "createdAt": record.CreatedAt,
	}
}

func uploadAttachment(w http.ResponseWriter, r *http.Request, state *AppState) {
	if state.ObjectStore == nil {
		writeServiceError(w, http.StatusServiceUnavailable, "storage_not_configured", "Object storage is not configured.", nil)
		return
	}
	rc, ok := requireAuth(w, r, state)
	if !ok {
		return
	}
	entity := chi.URLParam(r, "entity")
	recordID, err := uuid.Parse(chi.URLParam(r, "recordId"))
	if err != nil {
		writeServiceError(w, http.StatusNotFound, "record_not_found", "", nil)
		return
	}
	if !ensureRecordPermission(w, r, state, entity, recordID, permission.ActionUpdate, rc) {
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}

	mr, err := r.MultipartReader()
	if err != nil {
		writeServiceError(w, http.StatusBadRequest, "invalid_multipart", err.Error(), nil)
		return
	}
	part, err := mr.NextPart()
	if err == io.EOF {
		writeServiceError(w, http.StatusBadRequest, "missing_file", "No file field found in the upload.", nil)
		return
	}
	if err != nil {
		writeServiceError(w, http.StatusBadRequest, "invalid_multipart", err.Error(), nil)
		return
	}
	filename := part.FileName()
	if filename == "" {
		filename = "upload"
	}
	contentType := part.Header.Get("Content-Type")
	data, err := io.ReadAll(part)
	if err != nil {
		writeServiceError(w, http.StatusBadRequest, "invalid_multipart", err.Error(), nil)
		return
	}

	key := fmt.Sprintf("%s/%s/%s/%s", entity, recordID, uuid.New(), filename)
	if err := state.ObjectStore.Put(r.Context(), tenantID, key, data, contentType); err != nil {
		writeInternalError(w, err)
		return
	}

	tx, err := state.Router.Begin(r.Context(), tenantID)
	if err != nil {
		state.ObjectStore.Delete(r.Context(), tenantID, key) //nolint:errcheck
		writeRouterUnavailable(w, err)
		return
	}
	table := attachmentsTableFor(state, entity)
	createdBy := parseOptionalUUID(rc.UserID)
	var contentTypePtr *string
	if contentType != "" {
		contentTypePtr = &contentType
	}
	record, err := attachments.CreateAttachment(r.Context(), tx, table, tenantID, entity, recordID, filename, key,
		int64(len(data)), contentTypePtr, createdBy)
	if err != nil {
		tx.Rollback(r.Context())                             //nolint:errcheck
		state.ObjectStore.Delete(r.Context(), tenantID, key) //nolint:errcheck
		writeInternalError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		state.ObjectStore.Delete(r.Context(), tenantID, key) //nolint:errcheck
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"data": attachmentToJSON(record)})
}

func listAttachments(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAuth(w, r, state)
	if !ok {
		return
	}
	entity := chi.URLParam(r, "entity")
	recordID, err := uuid.Parse(chi.URLParam(r, "recordId"))
	if err != nil {
		writeServiceError(w, http.StatusNotFound, "record_not_found", "", nil)
		return
	}
	if !ensureRecordPermission(w, r, state, entity, recordID, permission.ActionRead, rc) {
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	tx, err := state.Router.Begin(r.Context(), tenantID)
	if err != nil {
		writeRouterUnavailable(w, err)
		return
	}
	table := attachmentsTableFor(state, entity)
	records, err := attachments.ListAttachments(r.Context(), tx, table, tenantID, entity, recordID)
	if err != nil {
		tx.Rollback(r.Context()) //nolint:errcheck
		writeInternalError(w, err)
		return
	}
	tx.Commit(r.Context()) //nolint:errcheck

	data := make([]map[string]any, len(records))
	for i, rec := range records {
		data[i] = attachmentToJSON(rec)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

// loadOwnedAttachment looks up the attachment and checks it actually belongs to
// (entity, recordID) — the attachmentId path segment alone is already tenant-scoped, but
// cross-checking the parent keeps a mismatched URL a clean 404 rather than silently serving it.
// Writes the error response itself on failure.
func loadOwnedAttachment(w http.ResponseWriter, r *http.Request, state *AppState, entity string, recordID,
	attachmentID, tenantID uuid.UUID, table string) (*attachments.Record, bool) {
	tx, err := state.Router.Begin(r.Context(), tenantID)
	if err != nil {
		writeRouterUnavailable(w, err)
		return nil, false
	}
	record, err := attachments.GetAttachment(r.Context(), tx, table, tenantID, attachmentID)
	if err != nil {
		tx.Rollback(r.Context()) //nolint:errcheck
		writeInternalError(w, err)
		return nil, false
	}
	tx.Commit(r.Context()) //nolint:errcheck
	if record == nil || record.EntityName != entity || record.RecordID != recordID {
		writeServiceError(w, http.StatusNotFound, "record_not_found", "", nil)
		return nil, false
	}
	return record, true
}

func downloadAttachment(w http.ResponseWriter, r *http.Request, state *AppState) {
	if state.ObjectStore == nil {
		writeServiceError(w, http.StatusServiceUnavailable, "storage_not_configured", "Object storage is not configured.", nil)
		return
	}
	rc, ok := requireAuth(w, r, state)
	if !ok {
		return
	}
	entity := chi.URLParam(r, "entity")
	recordID, err := uuid.Parse(chi.URLParam(r, "recordId"))
	if err != nil {
		writeServiceError(w, http.StatusNotFound, "record_not_found", "", nil)
		return
	}
	attachmentID, err := uuid.Parse(chi.URLParam(r, "attachmentId"))
	if err != nil {
		writeServiceError(w, http.StatusNotFound, "record_not_found", "", nil)
		return
	}
	if !ensureRecordPermission(w, r, state, entity, recordID, permission.ActionRead, rc) {
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	table := attachmentsTableFor(state, entity)
	record, ok := loadOwnedAttachment(w, r, state, entity, recordID, attachmentID, tenantID, table)
	if !ok {
		return
	}

	data, found, err := state.ObjectStore.Get(r.Context(), tenantID, record.Key)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if !found {
		writeServiceError(w, http.StatusNotFound, "record_not_found", "The file is missing from object storage.", nil)
		return
	}

	contentType := "application/octet-stream"
	if record.ContentType != nil {
		contentType = *record.ContentType
	}
	// Content-Disposition: attachment — never inline — so a browser downloads rather than
	// renders whatever contentType the uploader claimed (a caller-supplied, unverified value).
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", record.Filename))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func deleteAttachment(w http.ResponseWriter, r *http.Request, state *AppState) {
	if state.ObjectStore == nil {
		writeServiceError(w, http.StatusServiceUnavailable, "storage_not_configured", "Object storage is not configured.", nil)
		return
	}
	rc, ok := requireAuth(w, r, state)
	if !ok {
		return
	}
	entity := chi.URLParam(r, "entity")
	recordID, err := uuid.Parse(chi.URLParam(r, "recordId"))
	if err != nil {
		writeServiceError(w, http.StatusNotFound, "record_not_found", "", nil)
		return
	}
	attachmentID, err := uuid.Parse(chi.URLParam(r, "attachmentId"))
	if err != nil {
		writeServiceError(w, http.StatusNotFound, "record_not_found", "", nil)
		return
	}
	if !ensureRecordPermission(w, r, state, entity, recordID, permission.ActionDelete, rc) {
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	table := attachmentsTableFor(state, entity)
	record, ok := loadOwnedAttachment(w, r, state, entity, recordID, attachmentID, tenantID, table)
	if !ok {
		return
	}

	tx, err := state.Router.Begin(r.Context(), tenantID)
	if err != nil {
		writeRouterUnavailable(w, err)
		return
	}
	if err := attachments.DeleteAttachment(r.Context(), tx, table, tenantID, attachmentID); err != nil {
		tx.Rollback(r.Context()) //nolint:errcheck
		writeInternalError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeInternalError(w, err)
		return
	}

	// Best-effort — the metadata row (the thing permission/listing/download actually rely on)
	// is already gone at this point regardless of whether the blob cleanup itself succeeds; a
	// failed delete here leaves an orphaned object in storage, not a dangling metadata
	// reference, so it's logged rather than turned into a failed request.
	if err := state.ObjectStore.Delete(r.Context(), tenantID, record.Key); err != nil {
		slog.Warn("failed to delete attachment blob from object storage", "key", record.Key, "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func mountAttachments(r chi.Router, state *AppState) {
	wrap := func(h func(http.ResponseWriter, *http.Request, *AppState)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { h(w, r, state) }
	}
	r.Post("/api/{entity}/{recordId}/attachments", wrap(uploadAttachment))
	r.Get("/api/{entity}/{recordId}/attachments", wrap(listAttachments))
	r.Get("/api/{entity}/{recordId}/attachments/{attachmentId}/download", wrap(downloadAttachment))
	r.Delete("/api/{entity}/{recordId}/attachments/{attachmentId}", wrap(deleteAttachment))
}
