// Package crud is CrudService — list/get/create/update/transition/delete, the field-metadata-
// driven payload validator, record masking/capabilities. Ported from metap's Rust metap-crud
// crate.
package crud

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// JsonObject mirrors the Rust serde_json::Map<String, Value> — same convention as
// audit.JsonObject/permission.JsonObject (map[string]json.RawMessage), so a value can cross
// between this package and those without re-encoding.
type JsonObject = map[string]json.RawMessage

// RecordDto mirrors the Rust RecordDto.
type RecordDto struct {
	ID        uuid.UUID
	Entity    string
	Code      *string
	Status    *string
	Data      JsonObject
	Version   int32
	CreatedAt time.Time
	UpdatedAt time.Time
	// RelatedDisplay: list-only batch display hydration (CrudService.list's
	// hydrateRelatedDisplay) — keyed by the name of each Reference field on this entity that
	// declares RefDisplayField, valued by that related record's display value. nil for every
	// other response (get/create/update/transition/delete) and for a list response on an entity
	// with no such field.
	RelatedDisplay map[string]string
}

// TransitionAvailability mirrors the Rust TransitionAvailability.
type TransitionAvailability struct {
	Action    string
	Available bool
	Reason    *string
}

// RecordCapabilities mirrors the Rust RecordCapabilities.
type RecordCapabilities struct {
	WritableFields []string
	CanUpdate      bool
	// CanDelete: separate from CanUpdate for the same reason Transitions is — Delete is its own
	// EntityAction, so a policy can grant editing without granting removal.
	CanDelete   bool
	Transitions []TransitionAvailability
}
