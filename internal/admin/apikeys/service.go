// Package apikeys is the issuing, listing and revoking of API keys (roadmap 115 M5) as an
// administrative slice: types.go, rules.go (what a key's name and lifetime may be, no I/O), store.go
// (the port) with its Postgres adapter, and this file, which only orchestrates. A key authenticates as
// its member, so creating one is access that outlives the moment: kernel.Durable refuses a vendor's
// support session. Service accounts (the members keys usually act as) stay with the users service.
package apikeys

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/access"
	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/action"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/kit/errcode"
)

// AuditEntityUsers is the audit entity key changes are recorded under (the member the key acts as).
const AuditEntityUsers = "metadata.users"

type Service struct {
	store Store
	audit kernel.Auditor
	// sink, when set, makes each change and its audit entry one transaction on the shared database
	// (the identity data and the audit store live there for every tenant); nil keeps the older
	// "change, then record best-effort".
	sink action.Sink
	// maxDays is the tenant's cap on a key's lifetime in days (auth.apiKeyMaxLifetimeDays); nil or 0
	// means none.
	maxDays func(ctx context.Context, tenant uuid.UUID) int
	now     func() time.Time
}

// New is the service over store. auditor records each change (nil records nothing); maxDays is the
// tenant's lifetime cap (nil: no cap); now is the clock (nil: time.Now).
func New(store Store, auditor kernel.Auditor, maxDays func(ctx context.Context, tenant uuid.UUID) int, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, audit: auditor, maxDays: maxDays, now: now}
}

// WithSink makes every change atomic with its audit entry (see Service.sink) and returns s.
func (s *Service) WithSink(sink action.Sink) *Service {
	s.sink = sink
	return s
}

func (s *Service) change(ctx context.Context, c kernel.Caller, member uuid.UUID, fn func(ctx context.Context) (map[string][2]any, error)) error {
	return kernel.Change(ctx, s.sink, s.audit, s.now, c, AuditEntityUsers, member, fn)
}

func (s *Service) record(ctx context.Context, c kernel.Caller, member uuid.UUID, diff map[string][2]any) {
	if s.audit != nil {
		s.audit.Record(ctx, c, AuditEntityUsers, member, audit.ActionUpdate, diff)
	}
}

// Create issues a key for a member of the caller's tenant. The secret is in the answer once.
func (s *Service) Create(ctx context.Context, req *CreateApiKeyRequest) (*CreateApiKeyResponse, error) {
	c, err := kernel.Durable(ctx, s.store.IsSupportSession)
	if err != nil {
		return nil, err
	}
	member, err := kernel.ParseID(req.UserId)
	if err != nil {
		return nil, err
	}
	var tenantMax int
	if s.maxDays != nil {
		tenantMax = s.maxDays(ctx, c.Tenant)
	}
	now := s.now()
	expires, why := expiry(int(req.ExpiresInDays), tenantMax, now)
	if why != "" {
		return nil, kernel.Err(http.StatusBadRequest, errcode.ValidationFailed, why)
	}
	if why := nameProblem(req.Name); why != "" {
		return nil, kernel.Err(http.StatusBadRequest, errcode.ValidationFailed, why)
	}
	var createdBy *uuid.UUID
	if c.Self != uuid.Nil {
		createdBy = &c.Self
	}
	var token string
	var key *identity.APIKey
	err = s.change(ctx, c, member, func(ctx context.Context) (map[string][2]any, error) {
		var err error
		token, key, err = s.store.Create(ctx, c.Tenant, member, req.Name, expires, createdBy, now)
		if err != nil {
			if errors.Is(err, identity.ErrNotFound) {
				return nil, kernel.IdentityErr(err)
			}
			return nil, kernel.Err(http.StatusBadRequest, errcode.ValidationFailed, err.Error())
		}
		return map[string][2]any{"apiKeyCreated": {nil, key.Name + " (" + key.Prefix + ")"}}, nil
	})
	if err != nil {
		return nil, err
	}
	return &CreateApiKeyResponse{ApiKey: view(*key), Token: token}, nil
}

// List is a member's keys, newest first, revoked ones included.
func (s *Service) List(ctx context.Context, userID string) (*ListApiKeysResponse, error) {
	c, err := kernel.At(ctx, access.Admin)
	if err != nil {
		return nil, err
	}
	member, err := kernel.ParseID(userID)
	if err != nil {
		return nil, err
	}
	keys, err := s.store.List(ctx, c.Tenant, member)
	if err != nil {
		return nil, kernel.IdentityErr(err)
	}
	resp := &ListApiKeysResponse{ApiKeys: []*ApiKey{}}
	for _, k := range keys {
		resp.ApiKeys = append(resp.ApiKeys, view(k))
	}
	return resp, nil
}

// Revoke ends a key of the caller's tenant (a key of another tenant is a 404).
func (s *Service) Revoke(ctx context.Context, keyID string) error {
	c, err := kernel.At(ctx, access.Admin)
	if err != nil {
		return err
	}
	id, err := kernel.ParseID(keyID)
	if err != nil {
		return err
	}
	member, err := s.store.KeyMember(ctx, c.Tenant, id)
	if err != nil {
		return kernel.IdentityErr(err)
	}
	return s.change(ctx, c, member, func(ctx context.Context) (map[string][2]any, error) {
		if err := s.store.Revoke(ctx, c.Tenant, id, s.now()); err != nil {
			return nil, kernel.IdentityErr(err)
		}
		return map[string][2]any{"apiKeyRevoked": {nil, id.String()}}, nil
	})
}
