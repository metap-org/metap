// Validates directly from EntityField (kind/required/enumValues) rather than a per-entity,
// hand-authored schema. Known simplification vs. a Zod-style schema: this validates JSON *type*
// per FieldKind (string/number/boolean/enum-membership) and numeric range/string-length bounds,
// but not free-form string *formats* (email, UUID shape beyond Reference's own check).
package crud

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/metap-org/metap-go/metadata"
)

// FieldErrors mirrors the Rust FieldErrors.
type FieldErrors = map[string][]string

func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

func kindMatches(kind metadata.FieldKind, raw json.RawMessage) bool {
	switch kind {
	case metadata.KindID, metadata.KindString:
		var s string
		return json.Unmarshal(raw, &s) == nil
	// A Reference field's value must be a syntactically valid UUID — not just any string (a
	// non-UUID, or a non-canonically-rendered UUID, e.g. uppercase hex, would otherwise pass
	// unvalidated; delete()'s reference-integrity guard compares this value against id.String()
	// with plain SQL string equality, so a non-canonical stored value would silently fail to
	// match a real reference).
	case metadata.KindReference:
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return false
		}
		_, err := uuid.Parse(s)
		return err == nil
	case metadata.KindNumber, metadata.KindMoney:
		var n float64
		return json.Unmarshal(raw, &n) == nil
	case metadata.KindBoolean:
		var b bool
		return json.Unmarshal(raw, &b) == nil
	case metadata.KindDate, metadata.KindDatetime:
		var s string
		return json.Unmarshal(raw, &s) == nil
	case metadata.KindJSON:
		return true
	default: // KindEnum handled separately — needs EnumValues, not just kind
		return false
	}
}

func boolValField(p *bool) bool { return p != nil && *p }

// ValidatePayload validates data against entity.Fields. Returns it unchanged on success for
// every kind except Reference, where the stored value is canonicalized to its lowercase-
// hyphenated uuid.String() form regardless of how the caller rendered it, so every later
// exact-string comparison against a Reference field can rely on a single canonical form having
// been stored. On failure, returns per-field error messages.
func ValidatePayload(entity metadata.EntityDefinition, data JsonObject) (JsonObject, FieldErrors) {
	errs := FieldErrors{}
	result := make(JsonObject, len(data))
	for k, v := range data {
		result[k] = v
	}

	for _, field := range entity.Fields {
		raw, present := data[field.Name]
		hasValue := present && !isJSONNull(raw)

		if boolValField(field.Required) && !hasValue {
			errs[field.Name] = append(errs[field.Name], "required")
			continue
		}
		if !hasValue {
			continue
		}

		var valid bool
		if field.Kind == metadata.KindEnum {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				for _, v := range field.EnumValues {
					if v == s {
						valid = true
						break
					}
				}
			}
		} else {
			valid = kindMatches(field.Kind, raw)
		}

		if !valid {
			errs[field.Name] = append(errs[field.Name], "invalid_type")
			continue
		}

		if field.Kind == metadata.KindReference {
			var s string
			_ = json.Unmarshal(raw, &s) // valid above already confirmed this parses
			u, _ := uuid.Parse(s)
			canonical, _ := json.Marshal(u.String())
			result[field.Name] = canonical
		}

		// metadata.Validate already rejects Min/Max/MinLength/MaxLength set on a kind they
		// don't apply to, so it's safe to check both pairs unconditionally here — only the pair
		// matching this field's actual kind will ever be non-nil.
		if field.Kind == metadata.KindNumber || field.Kind == metadata.KindMoney {
			var n float64
			if json.Unmarshal(raw, &n) == nil {
				if field.Min != nil && n < *field.Min {
					errs[field.Name] = append(errs[field.Name], "min")
				}
				if field.Max != nil && n > *field.Max {
					errs[field.Name] = append(errs[field.Name], "max")
				}
			}
		}
		if field.Kind == metadata.KindString {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				length := uint32(utf8.RuneCountInString(s))
				if field.MinLength != nil && length < *field.MinLength {
					errs[field.Name] = append(errs[field.Name], "min_length")
				}
				if field.MaxLength != nil && length > *field.MaxLength {
					errs[field.Name] = append(errs[field.Name], "max_length")
				}
			}
		}
	}

	if len(errs) == 0 {
		return result, nil
	}
	return nil, errs
}
