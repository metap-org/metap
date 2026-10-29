package audit

import "encoding/json"

// JsonObject is the record's data jsonb blob shape — flat, post-validation record fields.
type JsonObject map[string]json.RawMessage

// DiffJSONObjects is a shallow, top-level-key diff between two record data blobs. Only keys
// where before != after are included, as {"before": <old or null>, "after": <new or null>} — a
// key present in one side only reads unambiguously as added/removed via the null. Deliberately
// the smallest useful shape, not a general JSON-patch implementation.
func DiffJSONObjects(before, after JsonObject) JsonObject {
	out := JsonObject{}
	seen := map[string]bool{}
	for k := range before {
		seen[k] = true
	}
	for k := range after {
		seen[k] = true
	}
	for key := range seen {
		b, bOK := before[key]
		a, aOK := after[key]
		if bOK && aOK && jsonRawEqual(b, a) {
			continue
		}
		if !bOK && !aOK {
			continue
		}
		entry := map[string]json.RawMessage{}
		if bOK {
			entry["before"] = b
		} else {
			entry["before"] = json.RawMessage("null")
		}
		if aOK {
			entry["after"] = a
		} else {
			entry["after"] = json.RawMessage("null")
		}
		b2, _ := json.Marshal(entry)
		out[key] = b2
	}
	return out
}

// jsonRawEqual compares two json.RawMessage values by decoded value, not by byte formatting —
// matches serde_json::Value's PartialEq, which the Rust diff_json_objects relies on (two
// differently-whitespaced encodings of the same value must count as unchanged).
func jsonRawEqual(a, b json.RawMessage) bool {
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return string(a) == string(b)
	}
	ab, _ := json.Marshal(av)
	bb, _ := json.Marshal(bv)
	return string(ab) == string(bb)
}
