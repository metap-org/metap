package adminapi

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/kit/errcode"
)

// AuditAdminService searches and verifies the tenant's audit trail (roadmap 115 M9).
type AuditAdminService struct {
	d identityDeps
}

func NewAuditAdminService(cfg IdentityConfig) *AuditAdminService {
	return &AuditAdminService{d: identityDeps{cfg: cfg}}
}

// AuditSearchFromRequest turns the request's string filters into a store query; the second value
// is the error text of the first malformed filter ("" when all are fine). Exported for the REST
// export, which takes the same filters from the query string.
func AuditSearchFromRequest(req *SearchAuditRequest) (audit.SearchQuery, string) {
	q := audit.SearchQuery{Entity: req.GetEntity(), Actions: dedupe(req.GetActions()), Limit: int(req.GetLimit())}
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
			return q, "Invalid cursor."
		}
		q.After = &cur
	}
	return q, ""
}

// ToPBAuditEntry is the wire form of a stored row.
func ToPBAuditEntry(r audit.Row) *AuditEntry {
	e := &AuditEntry{Id: r.ID.String(), Entity: r.Entity, RecordId: r.RecordID.String(), Action: r.Action,
		OccurredAt: formatTime(&r.OccurredAt), DiffJson: string(r.Diff), Hash: r.Hash}
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

func (s *AuditAdminService) SearchAudit(ctx context.Context, req *SearchAuditRequest) (*SearchAuditResponse, error) {
	c, err := s.d.admin(ctx)
	if err != nil {
		return nil, err
	}
	if s.d.cfg.Audit == nil {
		return &SearchAuditResponse{}, nil
	}
	q, bad := AuditSearchFromRequest(req)
	if bad != "" {
		code := errcode.ValidationFailed
		if bad == "Invalid cursor." {
			code = errcode.InvalidCursor
		}
		return nil, svcErr(ctx, http.StatusBadRequest, code, bad)
	}
	if q.Limit <= 0 || q.Limit > audit.MaxPageSize {
		q.Limit = 50
	}
	limit := q.Limit
	rows, err := s.d.cfg.Audit.Search(ctx, c.tenant, q)
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	resp := &SearchAuditResponse{}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		resp.NextCursor = audit.EncodeCursor(audit.Cursor{OccurredAt: last.OccurredAt, ID: last.ID})
	}
	for _, r := range rows {
		resp.Entries = append(resp.Entries, ToPBAuditEntry(r))
	}
	return resp, nil
}

func (s *AuditAdminService) VerifyAuditChain(ctx context.Context, _ *Empty) (*AuditChainReport, error) {
	c, err := s.d.admin(ctx)
	if err != nil {
		return nil, err
	}
	if s.d.cfg.Audit == nil {
		return &AuditChainReport{Ok: true}, nil
	}
	rep, err := s.d.cfg.Audit.VerifyChain(ctx, c.tenant)
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	return &AuditChainReport{Ok: rep.OK, Checked: rep.Checked, BrokenSeq: rep.BrokenSeq, Reason: rep.Reason,
		HeadSeq: rep.HeadSeq, HeadHash: rep.HeadHash}, nil
}
