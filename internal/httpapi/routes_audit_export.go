// GET /admin/audit/export — the tenant's audit trail as a file (roadmap 115 M9). Admin only, the
// tenant is the caller's. Same filters as the GraphQL `auditEntries` search, but the whole
// matching history streams out page by page instead of one page: format=csv (default) or json
// (one JSON array). A file has no GraphQL shape, so this stays REST like attachments.
package httpapi

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/metap-org/metap/pkg/kit/httpx/router"

	"github.com/metap-org/metap/internal/admin"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/kit/errcode"
)

// auditExportCSVHeader is the CSV column order.
var auditExportCSVHeader = []string{"seq", "occurredAt", "entity", "recordId", "action", "transitionAction",
	"actorUserId", "reason", "diff", "hash"}

func exportAudit(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc := RC(r)
	if state.Audit == nil {
		writeServiceError(w, http.StatusServiceUnavailable, "audit_unavailable", "The audit trail is not configured.", nil)
		return
	}
	tenant, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeServiceError(w, http.StatusUnauthorized, errcode.Unauthorized, "", nil)
		return
	}
	values := r.URL.Query()
	req := &admin.SearchAuditRequest{ActorUserId: values.Get("actor"), Entity: values.Get("entity"), RecordId: values.Get("record"),
		From: values.Get("from"), To: values.Get("to")}
	for _, a := range strings.Split(values.Get("actions"), ",") {
		if a = strings.TrimSpace(a); a != "" {
			req.Actions = append(req.Actions, a)
		}
	}
	q, bad := admin.AuditSearchFromRequest(req)
	if bad != "" {
		writeServiceError(w, http.StatusBadRequest, errcode.ValidationFailed, bad, nil)
		return
	}
	format := values.Get("format")
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "json" {
		writeServiceError(w, http.StatusBadRequest, errcode.ValidationFailed, "`format` must be csv or json.", nil)
		return
	}

	name := "audit-" + time.Now().UTC().Format("20060102T150405Z") + "." + format
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	var emit func(audit.Row)
	var finish func()
	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		cw := csv.NewWriter(w)
		_ = cw.Write(auditExportCSVHeader)
		emit = func(row audit.Row) {
			e := admin.ToPBAuditEntry(row)
			seq := ""
			if row.Seq != nil {
				seq = strconv.FormatInt(*row.Seq, 10)
			}
			_ = cw.Write([]string{seq, e.GetOccurredAt(), e.GetEntity(), e.GetRecordId(), e.GetAction(),
				e.GetTransitionAction(), e.GetActorUserId(), e.GetReason(), e.GetDiffJson(), e.GetHash()})
		}
		finish = cw.Flush
	} else {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("["))
		first := true
		emit = func(row audit.Row) {
			b, err := json.Marshal(row)
			if err != nil {
				return
			}
			if !first {
				_, _ = w.Write([]byte(","))
			}
			first = false
			_, _ = w.Write(b)
		}
		finish = func() { _, _ = w.Write([]byte("]")) }
	}

	q.Limit = audit.MaxPageSize
	for {
		rows, err := state.Audit.Search(r.Context(), tenant, q)
		if err != nil {
			// Headers are gone; the truncated file is the signal.
			return
		}
		page := rows
		if len(page) > q.Limit {
			page = page[:q.Limit]
		}
		for _, row := range page {
			emit(row)
		}
		if len(rows) <= q.Limit {
			break
		}
		last := page[len(page)-1]
		q.After = &audit.Cursor{OccurredAt: last.OccurredAt, ID: last.ID}
	}
	finish()
}

func mountAuditExport(r *router.Router, state *AppState) {
	acc := AccessGroups(r, state)
	acc.Admin.Get("/admin/audit/export", func(w http.ResponseWriter, r *http.Request) { exportAudit(w, r, state) })
}
