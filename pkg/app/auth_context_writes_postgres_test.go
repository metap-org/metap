//go:build integration

package app

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/permission/policypg"
	"github.com/metap-org/metap/pkg/reconciler"
	"github.com/metap-org/metap/pkg/tenancy"
	"github.com/metap-org/metap/pkg/testkit"
	"github.com/metap-org/metap/pkg/web"
)

type forgotten []uuid.UUID

func (f *forgotten) Invalidate(_, user uuid.UUID) { *f = append(*f, user) }

// A field only the server sets is written outside the usual path, and can be what a policy reads.
func TestSettingASystemFieldOfTheContextEntityInvalidates(t *testing.T) {
	pool := testkit.NewPostgres(t)
	ctx := context.Background()
	system := field("level", metadata.KindString)
	system.System = yes()
	entity := metadata.EntityDefinition{Name: "ctxt.levels", Label: "Level", TableName: "ctxt.levels",
		Fields: []metadata.EntityField{field("userId", metadata.KindString), system}}
	_, err := reconciler.Reconcile(ctx, pool, uuid.Nil, entity, nil)
	require.NoError(t, err)
	registry := metadata.NewMetadataRegistry()
	require.NoError(t, registry.Register(entity))
	var md atomic.Pointer[metadata.MetadataRegistry]
	md.Store(registry)
	router := tenancy.NewRouter(pool, tenancy.NewRegistryCache(tenancy.NewPostgresTenantRegistry(pool)), nil)
	var spy forgotten
	service := crud.NewCrudService(router, &md, permission.NewPermissionService(policypg.NewPostgresPolicyStore(router))).
		WithContextInvalidation(entity.Name, &spy)

	tenant, member := uuid.New(), uuid.New()
	rc := &permission.RequestContext{TenantID: tenant.String(), UserID: uuid.NewString(), Roles: []string{"admin"}, HasRoles: true}
	raw, err := json.Marshal(member.String())
	require.NoError(t, err)
	record, err := service.Create(ctx, entity.Name, crud.JsonObject{"userId": raw}, rc, nil)
	require.NoError(t, err)
	assert.Equal(t, forgotten{member}, spy, "create")

	spy = nil
	found, err := service.SetSystemFields(ctx, tenant, entity.Name, record.ID, crud.JsonObject{"level": json.RawMessage(`"gold"`)})
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, forgotten{member}, spy)
}

func TestTheAuthContextCacheTTL(t *testing.T) {
	state := func() *web.State { return web.NewState(nil, nil, nil, nil, "", nil, nil) }
	cached := state()
	require.NoError(t, useAuthContext(cached, &config.App{AuthContextCacheTTLSeconds: 30}, "a.b"))
	assert.NotNil(t, cached.ContextAttributesCache)
	assert.Equal(t, "a.b", cached.AuthContextEntity)

	off := state()
	require.NoError(t, useAuthContext(off, &config.App{AuthContextCacheTTLSeconds: 0}, "a.b"))
	assert.Nil(t, off.ContextAttributesCache, "0 reads on every request; to the cache underneath it would mean for ever")
	negative := int64(-1)
	assert.Error(t, useAuthContext(state(), &config.App{AuthContextCacheTTLSeconds: uint64(negative)}, "a.b"))

	unset := state()
	require.NoError(t, useAuthContext(unset, &config.App{}, ""))
	assert.Empty(t, unset.AuthContextEntity)
}
