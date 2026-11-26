// Package trail is the tenant admin's view of the audit trail (roadmap 115 M9) as an administrative
// slice: searching it and verifying its hash chain. The trail itself is written elsewhere (pkg/audit,
// and every administrative change); this slice only reads. types.go, rules.go (turning filters into
// a query, paging, the wire form), store.go (the read port, audit.Store satisfies it) and this file.
package trail

import (
	"context"
	"net/http"

	"github.com/metap-org/metap/internal/access"
	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/kit/errcode"
)

type Service struct{ store Store }

// New is the service over store (nil: no trail is kept).
func New(store Store) *Service { return &Service{store: store} }

// Search is one page of the caller's tenant's trail, newest first.
func (s *Service) Search(ctx context.Context, req *SearchAuditRequest) (*SearchAuditResponse, error) {
	c, err := kernel.At(ctx, access.Admin)
	if err != nil {
		return nil, err
	}
	if s.store == nil {
		return &SearchAuditResponse{}, nil
	}
	q, bad := SearchFromRequest(req)
	if bad != "" {
		code := errcode.ValidationFailed
		if bad == invalidCursor {
			code = errcode.InvalidCursor
		}
		return nil, kernel.Err(http.StatusBadRequest, code, bad)
	}
	q.Limit = pageSize(q.Limit)
	rows, err := s.store.Search(ctx, c.Tenant, q)
	if err != nil {
		return nil, err
	}
	rows, next := page(rows, q.Limit)
	resp := &SearchAuditResponse{NextCursor: next}
	for _, r := range rows {
		resp.Entries = append(resp.Entries, View(r))
	}
	return resp, nil
}

// Verify walks the tenant's hash chain and reports the first link that does not hold.
func (s *Service) Verify(ctx context.Context) (*AuditChainReport, error) {
	c, err := kernel.At(ctx, access.Admin)
	if err != nil {
		return nil, err
	}
	if s.store == nil {
		return &AuditChainReport{Ok: true}, nil
	}
	rep, err := s.store.VerifyChain(ctx, c.Tenant)
	if err != nil {
		return nil, err
	}
	return &AuditChainReport{Ok: rep.OK, Checked: rep.Checked, BrokenSeq: rep.BrokenSeq, Reason: rep.Reason,
		HeadSeq: rep.HeadSeq, HeadHash: rep.HeadHash}, nil
}
