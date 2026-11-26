// Package credentials keeps a tenant's named credentials as an administrative slice: the row (name,
// kind, where) in metadata.tenant_credentials, the value in the chosen home (kernel.CredentialManager:
// the metap database or the deployment's secret manager). Other features link to a credential by id (a
// cron webhook's `credentialId`); the value is write-only and the audit trail never holds one.
// types.go, rules.go (what a name, a value and a home may be, the wire form), store.go with its
// Postgres adapter, and this file.
package credentials

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/secrets"
)

type Service struct {
	store   Store
	manager kernel.CredentialManager
	deps    kernel.Deps
}

// New is the service over store. manager keeps the values (nil: every call answers 501, the
// deployment has nowhere to put one).
func New(store Store, manager kernel.CredentialManager, deps kernel.Deps) *Service {
	return &Service{store: store, manager: manager, deps: deps}
}

func (s *Service) mgr() (kernel.CredentialManager, error) {
	if s.manager == nil {
		return nil, kernel.Err(http.StatusNotImplemented, errcode.NotFound, "Credential storage is not available on this service.")
	}
	return s.manager, nil
}

func (s *Service) get(ctx context.Context, tenant, id uuid.UUID) (Row, error) {
	r, err := s.store.Get(ctx, tenant, id)
	if errors.Is(err, ErrNotFound) {
		return r, kernel.Err(http.StatusNotFound, errcode.NotFound, "Credential not found.")
	}
	return r, err
}

func (s *Service) view(ctx context.Context, tenant uuid.UUID, m kernel.CredentialManager) (*CredentialsView, error) {
	caps := m.Capabilities()
	out := &CredentialsView{DatabaseAvailable: caps.Database, ExternalAvailable: caps.External, ExternalKind: caps.ExternalKind,
		Credentials: []*Credential{}}
	list, err := s.store.List(ctx, tenant)
	if err != nil {
		return nil, err
	}
	used, err := s.store.Usage(ctx, tenant)
	if err != nil {
		return nil, err
	}
	for _, c := range list {
		out.Credentials = append(out.Credentials, view(c, used[c.ID]))
	}
	return out, nil
}

func (s *Service) List(ctx context.Context, _ *kernel.Empty) (*CredentialsView, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	m, err := s.mgr()
	if err != nil {
		return nil, err
	}
	return s.view(ctx, c.Tenant, m)
}

func (s *Service) Create(ctx context.Context, req *CreateCredentialRequest) (*CredentialsView, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	m, err := s.mgr()
	if err != nil {
		return nil, err
	}
	name, err := checkName(req.Name)
	if err != nil {
		return nil, err
	}
	if !validKind(req.Kind) {
		return nil, fieldInvalid("kind", "`kind` must be authorization or secret.")
	}
	if err := checkHome(m.Capabilities(), req.Source); err != nil {
		return nil, err
	}
	username, stored := "", req.Value
	if req.Kind == KindUserPass {
		if username, err = checkUsername(req.Username); err != nil {
			return nil, err
		}
		if err := checkValue(req.Kind, req.Value); err != nil {
			return nil, err
		}
		stored = secrets.EncodeUserPass(username, req.Value)
	} else {
		if req.Username != "" {
			return nil, fieldInvalid("username", "Only a username and password credential has a username.")
		}
		if err := checkValue(req.Kind, req.Value); err != nil {
			return nil, err
		}
	}
	row := Row{ID: uuid.New(), Name: name, Description: strings.TrimSpace(req.Description), Kind: req.Kind, Source: req.Source, Username: username}
	ref := secrets.TenantCredentialRef(c.Tenant, row.ID)
	// The value first, then the row: a failure leaves an unreferenced secret, never a credential that
	// points at nothing. The put is a network call, so it stays outside the change.
	if err := m.Put(ctx, ref, req.Source, stored); err != nil {
		return nil, kernel.Err(http.StatusConflict, errcode.Forbidden, err.Error())
	}
	err = s.deps.Change(ctx, c, AuditEntity, row.ID, audit.ActionCreate, func(ctx context.Context) (map[string][2]any, error) {
		err := s.store.Insert(ctx, c.Tenant, row, c.SelfPtr())
		return map[string][2]any{"name": {nil, name}, "kind": {nil, req.Kind}, "source": {nil, req.Source}}, err
	})
	if err != nil {
		_ = m.Remove(ctx, ref)
		if errors.Is(err, ErrNameTaken) {
			return nil, fieldInvalid("name", "A credential with this name already exists.")
		}
		return nil, err
	}
	return s.view(ctx, c.Tenant, m)
}

func (s *Service) Update(ctx context.Context, req *UpdateCredentialRequest) (*CredentialsView, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	m, err := s.mgr()
	if err != nil {
		return nil, err
	}
	id, err := kernel.ParseID(req.Id)
	if err != nil {
		return nil, err
	}
	cur, err := s.get(ctx, c.Tenant, id)
	if err != nil {
		return nil, err
	}
	next := cur
	diff := map[string][2]any{}
	if req.Name != nil {
		name, err := checkName(*req.Name)
		if err != nil {
			return nil, err
		}
		if name != cur.Name {
			diff["name"] = [2]any{cur.Name, name}
		}
		next.Name = name
	}
	if req.Description != nil {
		next.Description = strings.TrimSpace(*req.Description)
	}
	if moving := req.Source != nil && *req.Source != cur.Source; moving {
		if err := checkHome(m.Capabilities(), *req.Source); err != nil {
			return nil, err
		}
		if req.Value == nil || *req.Value == "" {
			return nil, fieldInvalid("value", "A value is required to move a credential: it is never read back.")
		}
		diff["source"] = [2]any{cur.Source, *req.Source}
		next.Source = *req.Source
	}
	newValue := ""
	if req.Value != nil {
		newValue = *req.Value
	}
	if req.Username != nil && cur.Kind != KindUserPass {
		return nil, fieldInvalid("username", "Only a username and password credential has a username.")
	}
	usernameChanged := false
	if cur.Kind == KindUserPass && req.Username != nil {
		username, err := checkUsername(*req.Username)
		if err != nil {
			return nil, err
		}
		if username != cur.Username {
			usernameChanged = true
			diff["username"] = [2]any{cur.Username, username}
			next.Username = username
		}
	}
	ref := secrets.TenantCredentialRef(c.Tenant, id)
	if newValue != "" || usernameChanged {
		stored := newValue
		if cur.Kind == KindUserPass {
			password := newValue
			if password == "" {
				// A new username keeps the stored password: read it for this alone.
				existing, gerr := m.Get(ctx, ref)
				_, kept, ok := secrets.DecodeUserPass(existing)
				if gerr != nil || !ok {
					return nil, fieldInvalid("value", "The stored password could not be read: enter the password again.")
				}
				password = kept
			}
			if err := checkValue(cur.Kind, password); err != nil {
				return nil, err
			}
			stored = secrets.EncodeUserPass(next.Username, password)
		} else if err := checkValue(cur.Kind, newValue); err != nil {
			return nil, err
		}
		if err := checkHome(m.Capabilities(), next.Source); err != nil {
			return nil, err
		}
		if err := m.Put(ctx, ref, next.Source, stored); err != nil {
			return nil, kernel.Err(http.StatusConflict, errcode.Forbidden, err.Error())
		}
		if _, ok := diff["source"]; !ok && newValue != "" {
			diff["value"] = [2]any{"set", "replaced"}
		}
	}
	err = s.deps.Change(ctx, c, AuditEntity, id, audit.ActionUpdate, func(ctx context.Context) (map[string][2]any, error) {
		if err := s.store.Update(ctx, c.Tenant, next); err != nil {
			return nil, err
		}
		if len(diff) == 0 {
			return nil, nil // nothing a person would call a change (a description edit): no entry
		}
		return diff, nil
	})
	if err != nil {
		if errors.Is(err, ErrNameTaken) {
			return nil, fieldInvalid("name", "A credential with this name already exists.")
		}
		return nil, err
	}
	return s.view(ctx, c.Tenant, m)
}

func (s *Service) Delete(ctx context.Context, req *CredentialIdRequest) (*CredentialsView, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	m, err := s.mgr()
	if err != nil {
		return nil, err
	}
	id, err := kernel.ParseID(req.Id)
	if err != nil {
		return nil, err
	}
	cur, err := s.get(ctx, c.Tenant, id)
	if err != nil {
		return nil, err
	}
	used, err := s.store.Usage(ctx, c.Tenant)
	if err != nil {
		return nil, err
	}
	if uses := used[id]; len(uses) > 0 {
		return nil, inUseError(uses)
	}
	if err := m.Remove(ctx, secrets.TenantCredentialRef(c.Tenant, id)); err != nil {
		return nil, kernel.Err(http.StatusConflict, errcode.Forbidden, err.Error())
	}
	err = s.deps.Change(ctx, c, AuditEntity, id, audit.ActionDelete, func(ctx context.Context) (map[string][2]any, error) {
		err := s.store.Delete(ctx, c.Tenant, id)
		return map[string][2]any{"name": {cur.Name, nil}}, err
	})
	if err != nil {
		return nil, err
	}
	return s.view(ctx, c.Tenant, m)
}
