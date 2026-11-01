// Package query is the Postgres-dialect query planner — QueryPlanner.plan_list, keyset cursor
// encode/decode, record-level policy -> SQL, and a small JQL query language. Ported from metap's
// Rust metap-query crate. An opaque, base64-encoded JSON cursor — never trust its contents
// further than shape-checking here; PlanList separately validates it matches the query's actual
// resolved sort.
package query

import (
	"encoding/base64"
	"encoding/json"
)

type SortDir string

const (
	SortAsc  SortDir = "asc"
	SortDesc SortDir = "desc"
)

// Cursor mirrors the Rust Cursor. Value is nil when the page's last row had a SQL NULL sort
// value — distinct from a real empty string. Collapsing both into the same empty-string
// encoding is exactly the keyset-pagination bug this type exists to avoid: a NULL sort value can
// never satisfy sort_expr > ”/sort_expr = ” under SQL's three-valued logic, so every row after
// the first NULL would silently vanish from later pages. PlanList branches on this explicitly.
type Cursor struct {
	Field string  `json:"field"`
	Value *string `json:"value"`
	ID    string  `json:"id"`
	Dir   SortDir `json:"dir"`
}

func isCursorUUID(s string) bool {
	// Same shape check as the Rust is_uuid, not full RFC validation.
	if len(s) != 36 {
		return false
	}
	dashes := []int{8, 13, 18, 23}
	for _, pos := range dashes {
		if s[pos] != '-' {
			return false
		}
	}
	hexRanges := [][2]int{{0, 8}, {9, 13}, {14, 18}, {19, 23}, {24, 36}}
	for _, r := range hexRanges {
		for i := r[0]; i < r[1]; i++ {
			c := s[i]
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
}

func EncodeCursor(c Cursor) string {
	b, err := json.Marshal(c)
	if err != nil {
		panic("Cursor always serializes: " + err.Error())
	}
	return base64.StdEncoding.EncodeToString(b)
}

// DecodeCursor returns (Cursor{}, false) for any malformed input — bad base64, bad JSON, or an
// id that doesn't look like a UUID.
func DecodeCursor(raw string) (Cursor, bool) {
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return Cursor{}, false
	}
	var c Cursor
	if err := json.Unmarshal(decoded, &c); err != nil {
		return Cursor{}, false
	}
	if !isCursorUUID(c.ID) {
		return Cursor{}, false
	}
	return c, true
}
