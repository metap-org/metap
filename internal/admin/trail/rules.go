package trail

import (
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/audit"
)

// invalidCursor is the sentence for a cursor that does not decode; the service answers it with its
// own error code, so the two stay together here.
const invalidCursor = "Invalid cursor."

// defaultLimit is the page size when none (or a silly one) is asked for.
const defaultLimit = 50

// SearchFromRequest turns the request's string filters into a store query; the second value is the
// error text of the first malformed filter ("" when all are fine). Exported for the REST export,
// which takes the same filters from the query string. Pure: no I/O.
func SearchFromRequest(req *SearchAuditRequest) (audit.SearchQuery, string) {
	q := audit.SearchQuery{Entity: req.GetEntity(), Actions: kernel.Dedupe(req.GetActions()), Limit: int(req.GetLimit())}
	if raw := req.GetActorUserId(); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return q, "`actorUserId` must be a user id."
		}
		q.ActorUserID = &id
	}
	if raw := req.GetRecordId(); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return q, "`recordId` must be a record id."
		}
		q.RecordID = &id
	}
	for _, b := range []struct {
		raw  string
		dst  **time.Time
		name string
	}{{req.GetFrom(), &q.From, "from"}, {req.GetTo(), &q.To, "to"}} {
		if b.raw == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, b.raw)
		if err != nil {
			return q, "`" + b.name + "` must be an RFC 3339 time."
		}
		*b.dst = &t
	}
	if raw := req.GetCursor(); raw != "" {
		cur, ok := audit.DecodeCursor(raw)
		if !ok {
			return q, invalidCursor
		}
		q.After = &cur
	}
	return q, ""
}

// pageSize is the limit to use for a query: the asked one when it is in range, else the default.
func pageSize(asked int) int {
	if asked <= 0 || asked > audit.MaxPageSize {
		return defaultLimit
	}
	return asked
}

// page cuts rows (fetched with one more than limit, so there is a way to tell) to limit and says
// where the next page starts ("" when this is the last).
func page(rows []audit.Row, limit int) ([]audit.Row, string) {
	if len(rows) <= limit {
		return rows, ""
	}
	rows = rows[:limit]
	last := rows[len(rows)-1]
	return rows, audit.EncodeCursor(audit.Cursor{OccurredAt: last.OccurredAt, ID: last.ID})
}

// View is the wire form of a stored row.
func View(r audit.Row) *AuditEntry {
	e := &AuditEntry{Id: r.ID.String(), Entity: r.Entity, RecordId: r.RecordID.String(), Action: r.Action,
		OccurredAt: kernel.FormatTime(&r.OccurredAt), DiffJson: string(r.Diff), Hash: r.Hash}
	if r.TransitionAction != nil {
		e.TransitionAction = *r.TransitionAction
	}
	if r.ActorUserID != nil {
		e.ActorUserId = r.ActorUserID.String()
	}
	if r.Reason != nil {
		e.Reason = *r.Reason
	}
	if r.Seq != nil {
		e.Seq = *r.Seq
	}
	return e
}
