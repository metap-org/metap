package admin

import (
	"context"
	"fmt"
	"github.com/metap-org/metap/pkg/permission/rolestore"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/identity"
	"github.com/metap-org/metap/pkg/kit/errcode"
)

// MaxAPIKeyLifetimeDays bounds an expiring key's lifetime.
const MaxAPIKeyLifetimeDays = 3650

func toAPIKey(k identity.APIKey) *ApiKey {
	return &ApiKey{Id: k.ID.String(), UserId: k.MemberID.String(), Name: k.Name, Prefix: k.Prefix,
		ExpiresAt: formatTime(k.ExpiresAt), RevokedAt: formatTime(k.RevokedAt), LastUsedAt: formatTime(k.LastUsedAt),
		CreatedAt: k.CreatedAt.UTC().Format(time.RFC3339)}
}

func (s *UserAdminService) CreateServiceAccount(ctx context.Context, req *CreateServiceAccountRequest) (*User, error) {
	c, err := s.admin(ctx)
	if err != nil {
		return nil, err
	}
	roles := dedupe(req.GetRoles())
	if err := s.d.checkRoles(ctx, c.tenant, roles); err != nil {
		return nil, err
	}
	var createdBy *uuid.UUID
	if c.self != uuid.Nil {
		createdBy = &c.self
	}
	m, err := identity.CreateServiceAccount(ctx, s.d.cfg.Shared, c.tenant, req.GetName(), createdBy)
	if err != nil {
		return nil, svcErr(ctx, http.StatusBadRequest, errcode.ValidationFailed, err.Error())
	}
	if len(roles) > 0 {
		if err := s.d.withTenantTx(ctx, c.tenant, func(db rolestore.DBTX) error {
			for _, r := range roles {
				if err := rolestore.AssignRole(ctx, db, c.tenant, m.ID, r, createdBy); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			_ = identity.RemoveMember(ctx, s.d.cfg.Shared, c.tenant, m.ID, s.d.now())
			return nil, rpcError(ctx, err)
		}
	}
	s.d.record(ctx, c, m.ID, audit.ActionCreate, map[string][2]any{
		"serviceAccount": {nil, m.DisplayName}, "roles": {nil, roles},
	})
	u, err := s.d.user(ctx, c.tenant, m.ID)
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	return u, nil
}

func (s *UserAdminService) ListServiceAccounts(ctx context.Context, _ *Empty) (*ListUsersResponse, error) {
	c, err := s.admin(ctx)
	if err != nil {
		return nil, err
	}
	ms, err := identity.ListServiceAccounts(ctx, s.d.cfg.Shared, c.tenant)
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	users, err := s.d.toUsers(ctx, c.tenant, ms)
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	return &ListUsersResponse{Users: users, Total: int32(len(users))}, nil
}

func (s *UserAdminService) CreateApiKey(ctx context.Context, req *CreateApiKeyRequest) (*CreateApiKeyResponse, error) {
	c, err := s.admin(ctx)
	if err != nil {
		return nil, err
	}
	member, err := parseID(req.GetUserId())
	if err != nil {
		return nil, err
	}
	days := int(req.GetExpiresInDays())
	if days < 0 || days > MaxAPIKeyLifetimeDays {
		return nil, svcErr(ctx, http.StatusBadRequest, errcode.ValidationFailed, "expiresInDays must be between 0 and 3650")
	}
	// The tenant's own cap (roadmap 115 M11): a key must expire, and no later than the cap.
	if s.d.cfg.APIKeyMaxDays != nil {
		if max := s.d.cfg.APIKeyMaxDays(ctx, c.tenant); max > 0 && (days == 0 || days > max) {
			return nil, svcErr(ctx, http.StatusBadRequest, errcode.ValidationFailed,
				fmt.Sprintf("This tenant limits API keys to %d days; set expiresInDays between 1 and %d.", max, max))
		}
	}
	now := s.d.now()
	var expires *time.Time
	if days > 0 {
		t := now.Add(time.Duration(days) * 24 * time.Hour)
		expires = &t
	}
	var createdBy *uuid.UUID
	if c.self != uuid.Nil {
		createdBy = &c.self
	}
	token, key, err := identity.CreateAPIKey(ctx, s.d.cfg.Shared, c.tenant, member, req.GetName(), expires, createdBy, now)
	if err != nil {
		if err == identity.ErrNotFound {
			return nil, identityErr(ctx, err)
		}
		return nil, svcErr(ctx, http.StatusBadRequest, errcode.ValidationFailed, err.Error())
	}
	s.d.record(ctx, c, member, audit.ActionUpdate, map[string][2]any{"apiKeyCreated": {nil, key.Name + " (" + key.Prefix + ")"}})
	return &CreateApiKeyResponse{ApiKey: toAPIKey(*key), Token: token}, nil
}

func (s *UserAdminService) ListApiKeys(ctx context.Context, req *UserIdRequest) (*ListApiKeysResponse, error) {
	c, err := s.admin(ctx)
	if err != nil {
		return nil, err
	}
	member, err := parseID(req.GetId())
	if err != nil {
		return nil, err
	}
	keys, err := identity.ListAPIKeys(ctx, s.d.cfg.Shared, c.tenant, member)
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	resp := &ListApiKeysResponse{ApiKeys: []*ApiKey{}}
	for _, k := range keys {
		resp.ApiKeys = append(resp.ApiKeys, toAPIKey(k))
	}
	return resp, nil
}

func (s *UserAdminService) RevokeApiKey(ctx context.Context, req *ApiKeyIdRequest) (*Empty, error) {
	c, err := s.admin(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.GetId())
	if err != nil {
		return nil, err
	}
	member, err := identity.KeyTenant(ctx, s.d.cfg.Shared, c.tenant, id)
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	if err := identity.RevokeAPIKey(ctx, s.d.cfg.Shared, c.tenant, id, s.d.now()); err != nil {
		return nil, identityErr(ctx, err)
	}
	s.d.record(ctx, c, member, audit.ActionUpdate, map[string][2]any{"apiKeyRevoked": {nil, id.String()}})
	return &Empty{}, nil
}
