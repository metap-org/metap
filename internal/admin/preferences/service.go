// Package preferences is the caller's own preferences row, as an administrative slice: types.go
// (what goes over the wire), rules.go (decisions, no I/O), store.go (the port) and pgstore.go (its
// Postgres adapter), and this file, which only orchestrates: who is calling, the rule, the store.
// The route and its access level are declared in internal/httpapi (operationAccess), the one table
// of who may call what.
package preferences

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
)

// TenantScoper says which tenant a request acts in (permission.PermissionService does).
type TenantScoper interface {
	ScopedTenant(rc *permission.RequestContext) (uuid.UUID, error)
}

type Service struct {
	store Store
	scope TenantScoper
}

func New(store Store, scope TenantScoper) *Service { return &Service{store: store, scope: scope} }

// who is the tenant and member the caller's preferences belong to.
func (s *Service) who(ctx context.Context) (tenant, user uuid.UUID, err error) {
	rc, err := kernel.Principal(ctx)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	tenant, err = s.scope.ScopedTenant(rc)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	user, err = uuid.Parse(rc.UserID)
	return tenant, user, err
}

func (s *Service) Get(ctx context.Context) (*PreferencesResult, error) {
	tenant, user, err := s.who(ctx)
	if err != nil {
		return nil, err
	}
	locale, err := s.store.Locale(ctx, tenant, user)
	if err != nil {
		return nil, err
	}
	return &PreferencesResult{Data: &Preferences{Locale: locale}}, nil
}

func (s *Service) Update(ctx context.Context, req *Preferences) (*PreferencesResult, error) {
	tenant, user, err := s.who(ctx)
	if err != nil {
		return nil, err
	}
	if why := localeProblem(req.Locale); why != "" {
		return nil, kernel.Err(http.StatusBadRequest, errcode.ValidationFailed, why)
	}
	if err := s.store.SetLocale(ctx, tenant, user, req.Locale); err != nil {
		return nil, err
	}
	return &PreferencesResult{Data: &Preferences{Locale: req.Locale}}, nil
}
