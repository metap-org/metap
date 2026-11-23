// PreferencesService: the caller's own preferences row (today the locale, the backend half of i18n).
package admin

import (
	"context"
	"github.com/metap-org/metap/pkg/identity"
	"net/http"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
)

// SupportedLocales is a conservative allowlist rather than any string: a typo'd locale would
// otherwise fall back to the frontend's default with no server-side signal anything was wrong.
var SupportedLocales = []string{"en", "vi"}

type PreferencesService struct {
	pool  *pgxpool.Pool
	perms *permission.PermissionService
}

func NewPreferencesService(pool *pgxpool.Pool, perms *permission.PermissionService) *PreferencesService {
	return &PreferencesService{pool: pool, perms: perms}
}

func (s *PreferencesService) who(ctx context.Context) (uuid.UUID, uuid.UUID, error) {
	rc, err := authenticatedCaller(ctx)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	tenant, err := s.perms.ScopedTenant(rc)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	user, err := uuid.Parse(rc.UserID)
	return tenant, user, err
}

func (s *PreferencesService) GetPreferences(ctx context.Context, _ *Empty) (*PreferencesResult, error) {
	tenant, user, err := s.who(ctx)
	if err != nil {
		return nil, err
	}
	locale, err := identity.GetLocale(ctx, s.pool, tenant, user)
	if err != nil {
		return nil, err
	}
	return &PreferencesResult{Data: &Preferences{Locale: locale}}, nil
}

func (s *PreferencesService) UpdatePreferences(ctx context.Context, req *Preferences) (*PreferencesResult, error) {
	tenant, user, err := s.who(ctx)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(SupportedLocales, req.Locale) {
		return nil, svcErr(ctx, http.StatusBadRequest, errcode.ValidationFailed, "`locale` must be one of: en, vi.")
	}
	if err := identity.SetLocale(ctx, s.pool, tenant, user, req.Locale); err != nil {
		return nil, err
	}
	return &PreferencesResult{Data: &Preferences{Locale: req.Locale}}, nil
}
