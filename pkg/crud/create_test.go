package crud

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/control"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
)

// Unit tests for every decision Create makes before it touches the database, using mockery
// mocks for the two seams (TxBeginner, PermissionChecker). Anything past router.Begin — the SQL
// itself, unique/reference enforcement, outbox — stays in crud_service_postgres_test.go.
//
// The mocks fail the test on any call that wasn't set up, so "Begin is never called" is asserted
// simply by not expecting it.

type createFixture struct {
	svc      *CrudService
	router   *MockTxBeginner
	perms    *MockPermissionChecker
	tenantID uuid.UUID
	rc       *permission.RequestContext
}

func newCreateFixture(t *testing.T, entities ...metadata.EntityDefinition) createFixture {
	t.Helper()
	registry := metadata.NewMetadataRegistry()
	for _, e := range entities {
		require.NoError(t, registry.Register(e))
	}
	var md atomic.Pointer[metadata.MetadataRegistry]
	md.Store(registry)

	router := NewMockTxBeginner(t)
	perms := NewMockPermissionChecker(t)
	tenantID := uuid.New()
	rc := &permission.RequestContext{TenantID: tenantID.String(), UserID: uuid.NewString(), Roles: []string{"admin"}, HasRoles: true}
	return createFixture{svc: NewCrudService(router, &md, perms), router: router, perms: perms, tenantID: tenantID, rc: rc}
}

// allowUpToBegin sets up the permission calls a Create makes before opening a transaction.
func (f createFixture) allowUpToBegin(entity string) {
	f.perms.EXPECT().ScopedTenant(f.rc).Return(f.tenantID, nil)
	f.perms.EXPECT().CanCreateEntity(mock.Anything, f.rc, entity).Return(permission.Allowed(), nil)
	f.perms.EXPECT().LoadSnapshot(mock.Anything, f.tenantID, entity).Return(&permission.PermissionSnapshot{}, nil)
}

func widgetEntity() metadata.EntityDefinition {
	system := true
	return ventity(
		vfield("name", metadata.KindString, true),
		metadata.EntityField{Name: "lastUsedAt", Label: "lastUsedAt", Kind: metadata.KindString, System: &system},
	)
}

func TestCreateUnknownEntityIs404WithoutCheckingPermissions(t *testing.T) {
	f := newCreateFixture(t, widgetEntity())
	f.perms.EXPECT().ScopedTenant(f.rc).Return(f.tenantID, nil)

	res, err := f.svc.Create(context.Background(), "test.nope", vobj(map[string]any{"name": "x"}), f.rc, nil)
	require.NoError(t, err)
	assert.False(t, res.IsOk)
	assert.EqualValues(t, 404, res.Status)
	assert.Equal(t, "entity_not_found", res.ErrorCode)
}

func TestCreateDeniedByEntityPolicyIs403AndNeverOpensATransaction(t *testing.T) {
	f := newCreateFixture(t, widgetEntity())
	f.perms.EXPECT().ScopedTenant(f.rc).Return(f.tenantID, nil)
	f.perms.EXPECT().CanCreateEntity(mock.Anything, f.rc, "test.widgets").
		Return(permission.PermissionDecision{Allowed: false, Reason: "no_create_policy"}, nil)

	res, err := f.svc.Create(context.Background(), "test.widgets", vobj(map[string]any{"name": "x"}), f.rc, nil)
	require.NoError(t, err)
	assert.EqualValues(t, 403, res.Status)
	assert.Equal(t, "no_create_policy", res.ErrorCode)
}

func TestCreateRejectsAClientSuppliedSystemField(t *testing.T) {
	f := newCreateFixture(t, widgetEntity())
	f.allowUpToBegin("test.widgets")

	res, err := f.svc.Create(context.Background(), "test.widgets",
		vobj(map[string]any{"name": "x", "lastUsedAt": "2026-01-01"}), f.rc, nil)
	require.NoError(t, err)
	assert.EqualValues(t, 400, res.Status)
	assert.Equal(t, "validation_failed", res.ErrorCode)
	assert.Equal(t, []string{"system_field"}, res.FieldErrors["lastUsedAt"])
}

func TestCreateWithAnInvalidPayloadIs400AndNeverOpensATransaction(t *testing.T) {
	f := newCreateFixture(t, widgetEntity())
	f.allowUpToBegin("test.widgets")

	res, err := f.svc.Create(context.Background(), "test.widgets", JsonObject{}, f.rc, nil)
	require.NoError(t, err)
	assert.EqualValues(t, 400, res.Status)
	assert.Equal(t, "validation_failed", res.ErrorCode)
	assert.Equal(t, []string{"required"}, res.FieldErrors["name"])
}

func TestCreateMapsAnUnavailableTenantToAClientFacingStatus(t *testing.T) {
	cases := []struct {
		kind   control.RouterErrorKind
		status uint16
		code   string
	}{
		{control.ErrTenantSuspended, 403, "tenant_unavailable"},
		{control.ErrTenantExpired, 403, "tenant_unavailable"},
		{control.ErrTenantMigrating, 503, "tenant_unavailable"},
		{control.ErrTenantProvisioning, 503, "tenant_unavailable"},
		{control.ErrTenantDeleted, 404, "tenant_not_found"},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			f := newCreateFixture(t, widgetEntity())
			f.allowUpToBegin("test.widgets")
			f.router.EXPECT().Begin(mock.Anything, f.tenantID).Return(nil, &control.RouterError{Kind: tc.kind})

			res, err := f.svc.Create(context.Background(), "test.widgets", vobj(map[string]any{"name": "x"}), f.rc, nil)
			require.NoError(t, err)
			assert.Equal(t, tc.status, res.Status)
			assert.Equal(t, tc.code, res.ErrorCode)
		})
	}
}

func TestCreatePropagatesAnUnexpectedRouterError(t *testing.T) {
	f := newCreateFixture(t, widgetEntity())
	f.allowUpToBegin("test.widgets")
	boom := errors.New("connection refused")
	f.router.EXPECT().Begin(mock.Anything, f.tenantID).Return(nil, boom)

	_, err := f.svc.Create(context.Background(), "test.widgets", vobj(map[string]any{"name": "x"}), f.rc, nil)
	require.ErrorIs(t, err, boom)
}

func TestCreatePropagatesATenantScopingError(t *testing.T) {
	f := newCreateFixture(t, widgetEntity())
	boom := errors.New("tenant mismatch")
	f.perms.EXPECT().ScopedTenant(f.rc).Return(uuid.Nil, boom)

	_, err := f.svc.Create(context.Background(), "test.widgets", vobj(map[string]any{"name": "x"}), f.rc, nil)
	require.ErrorIs(t, err, boom)
}
