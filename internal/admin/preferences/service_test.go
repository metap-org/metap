package preferences

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/permission"
)

// fixedScope puts every request in one tenant.
type fixedScope struct{ tenant uuid.UUID }

func (f fixedScope) ScopedTenant(*permission.RequestContext) (uuid.UUID, error) { return f.tenant, nil }

func asMember(tenant, user uuid.UUID) context.Context {
	return kernel.WithCaller(context.Background(), &permission.RequestContext{TenantID: tenant.String(), UserID: user.String()})
}

func newService(tenant uuid.UUID) (*Service, *memStore) {
	store := newMemStore()
	return New(store, fixedScope{tenant}), store
}

func TestLocaleRuleAcceptsOnlyTheSupportedOnes(t *testing.T) {
	for _, ok := range SupportedLocales {
		require.Empty(t, localeProblem(ok), ok)
	}
	for _, bad := range []string{"", "EN", "fr", "en-US", "vi "} {
		require.NotEmpty(t, localeProblem(bad), bad)
	}
}

func TestAMemberReadsBackWhatTheySaved(t *testing.T) {
	tenant, user := uuid.New(), uuid.New()
	svc, _ := newService(tenant)
	ctx := asMember(tenant, user)

	first, err := svc.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, DefaultLocale, first.Data.Locale)

	saved, err := svc.Update(ctx, &Preferences{Locale: "vi"})
	require.NoError(t, err)
	require.Equal(t, "vi", saved.Data.Locale)

	again, err := svc.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, "vi", again.Data.Locale)
}

func TestAnUnsupportedLocaleIsRefusedAndNothingIsSaved(t *testing.T) {
	tenant, user := uuid.New(), uuid.New()
	svc, store := newService(tenant)

	_, err := svc.Update(asMember(tenant, user), &Preferences{Locale: "fr"})
	se, ok := crud.AsServiceError(err)
	require.True(t, ok)
	require.Equal(t, http.StatusBadRequest, int(se.Status))
	require.Equal(t, "validation_failed", se.Code)

	got, _ := store.Locale(context.Background(), tenant, user)
	require.Equal(t, DefaultLocale, got, "a refused update writes nothing")
}

func TestNoCallerIsUnauthorized(t *testing.T) {
	svc, _ := newService(uuid.New())
	_, err := svc.Get(context.Background())
	se, ok := crud.AsServiceError(err)
	require.True(t, ok)
	require.Equal(t, http.StatusUnauthorized, int(se.Status))
	_, err = svc.Update(context.Background(), &Preferences{Locale: "en"})
	se, ok = crud.AsServiceError(err)
	require.True(t, ok)
	require.Equal(t, http.StatusUnauthorized, int(se.Status))
}
