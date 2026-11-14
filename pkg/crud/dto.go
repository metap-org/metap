// Package crud is CrudService — list/get/create/update/transition/delete, the field-metadata-
// driven payload validator, record masking/capabilities.
package crud

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type JsonObject = map[string]json.RawMessage

type RecordDto struct {
	ID        uuid.UUID  `json:"id"`
	Entity    string     `json:"entity"`
	Code      *string    `json:"code"`
	Status    *string    `json:"status"`
	Data      JsonObject `json:"data"`
	Version   int32      `json:"version"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
	// RelatedDisplay: list-only batch display hydration (CrudService.list's
	// hydrateRelatedDisplay) — keyed by the name of each Reference field on this entity that
	// declares RefDisplayField, valued by that related record's display value. nil for every
	// other response (get/create/update/transition/delete) and for a list response on an entity
	// with no such field.
	RelatedDisplay map[string]string `json:"relatedDisplay,omitempty"`
}

type TransitionAvailability struct {
	Action    string  `json:"action"`
	Available bool    `json:"available"`
	Reason    *string `json:"reason,omitempty"`
}

type RecordCapabilities struct {
	WritableFields []string `json:"writableFields"`
	CanUpdate      bool     `json:"canUpdate"`
	// CanDelete: separate from CanUpdate for the same reason Transitions is — Delete is its own
	// EntityAction, so a policy can grant editing without granting removal.
	CanDelete   bool                     `json:"canDelete"`
	Transitions []TransitionAvailability `json:"transitions"`
}
