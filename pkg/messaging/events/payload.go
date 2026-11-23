package events

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// PayloadVersion is stamped into every platform payload as "v". Consumers ignore unknown fields,
// so adding a field is not a version bump; renaming or removing one is.
const PayloadVersion = 1

// Fields lists the JSON object a record's data fields travel in.
type Fields = map[string]json.RawMessage

// Transitioned is the payload of <entity>.workflow.transitioned.
type Transitioned struct {
	Version  int        `json:"v"`
	TenantID uuid.UUID  `json:"tenantId"`
	RecordID uuid.UUID  `json:"recordId"`
	Action   string     `json:"action"`
	From     string     `json:"from"`
	To       string     `json:"to"`
	Actor    *uuid.UUID `json:"actor"`
}

// RecordEvent is the payload of <entity>.record.created|updated|deleted. Data is absent on a
// delete; Version is set on an update.
type RecordEvent struct {
	Version  int       `json:"v"`
	TenantID uuid.UUID `json:"tenantId"`
	RecordID uuid.UUID `json:"recordId"`
	Data     Fields    `json:"data,omitempty"`
	// RecordVersion is the record's optimistic-lock version after an update.
	RecordVersion *int32 `json:"version,omitempty"`
}

// Decode unmarshals a received payload into T, rejecting one that is not a JSON object.
func Decode[T any](payload []byte) (T, error) {
	var v T
	if err := json.Unmarshal(payload, &v); err != nil {
		return v, fmt.Errorf("decoding event payload: %w", err)
	}
	return v, nil
}

// Envelope is the part every platform payload carries: the tenant it belongs to and the record
// it is about. Enough to route or scope without knowing which lifecycle event it is.
type Envelope struct {
	TenantID uuid.UUID `json:"tenantId"`
	RecordID uuid.UUID `json:"recordId"`
}
