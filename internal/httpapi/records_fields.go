// `?fields=a,b,c` on the REST entity routes (roadmap 116): trim each record's `data` to the named
// fields. It only ever narrows what the caller was already allowed to read (permission masking
// has run before this), and the envelope (id, version, status, timestamps, ...) is always kept.
package httpapi

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
)

// maxFieldsPerRequest bounds `fields`; an entity never has anywhere near this many fields.
const maxFieldsPerRequest = 200

// parseFields reads `fields` from the query. A nil set (no `fields` given) means every readable
// field. A name that is not a field of the entity is a 400 naming it; an entity this service does
// not know is left to the backend, which answers 404 itself.
func parseFields(w http.ResponseWriter, r *http.Request, state *AppState, rc *permission.RequestContext, entity string, values url.Values) (map[string]bool, bool) {
	raw := strings.TrimSpace(values.Get("fields"))
	if raw == "" {
		return nil, true
	}
	names := strings.Split(raw, ",")
	if len(names) > maxFieldsPerRequest {
		writeServiceError(w, http.StatusBadRequest, errcode.ValidationFailed, "`fields` lists too many fields.", nil)
		return nil, false
	}
	known := map[string]bool{}
	haveEntity := false
	if tenant, err := uuid.Parse(rc.TenantID); err == nil {
		if reg, err := state.MetadataFor(r.Context(), tenant); err == nil && reg != nil {
			if def, ok := reg.GetEntity(entity); ok {
				haveEntity = true
				for _, f := range def.Fields {
					known[f.Name] = true
				}
			}
		}
	}
	set := make(map[string]bool, len(names))
	var unknown []string
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if haveEntity && !known[n] {
			unknown = append(unknown, "not a field of "+entity+": "+n)
			continue
		}
		set[n] = true
	}
	if len(unknown) > 0 {
		writeServiceError(w, http.StatusBadRequest, errcode.ValidationFailed, "`fields` names a field the entity does not have.",
			map[string][]string{"fields": unknown})
		return nil, false
	}
	return set, true
}

// projectData keeps only the named keys of a record's data (nil set: untouched).
func projectData(data crud.JsonObject, set map[string]bool) crud.JsonObject {
	if set == nil || data == nil {
		return data
	}
	out := make(crud.JsonObject, len(set))
	for k, v := range data {
		if set[k] {
			out[k] = v
		}
	}
	return out
}

// projectRecords trims every record in place; RelatedDisplay entries for dropped reference
// fields go too, so a trimmed record never carries display text for a field it no longer has.
func projectRecords(records []crud.RecordDto, set map[string]bool) {
	if set == nil {
		return
	}
	for i := range records {
		records[i].Data = projectData(records[i].Data, set)
		for k := range records[i].RelatedDisplay {
			if !set[k] {
				delete(records[i].RelatedDisplay, k)
			}
		}
	}
}
