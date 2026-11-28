//go:build integration

package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/auth/jwks"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/permission/policypg"
	"github.com/metap-org/metap/pkg/reconciler"
	"github.com/metap-org/metap/pkg/tenancy"
	"github.com/metap-org/metap/pkg/testkit"
)

// The context entity: one row per member, whose fields policies read as attributes of the caller. It
// declares a field called roles on purpose: a record must not be able to speak for the caller.
var ctxMembers = metadata.EntityDefinition{Name: "ctxt.members", Label: "Member", TableName: "ctxt.members",
	Fields: []metadata.EntityField{
		field("userId", metadata.KindString), field("suspended", metadata.KindBoolean), field("roles", metadata.KindString),
	}}

var ctxDocs = metadata.EntityDefinition{Name: "ctxt.docs", Label: "Doc", TableName: "ctxt.docs",
	Fields: []metadata.EntityField{field("title", metadata.KindString)}}

func init() {
	metadata.SubmitEntity(func() metadata.EntityDefinition { return ctxMembers })
	metadata.SubmitEntity(func() metadata.EntityDefinition { return ctxDocs })
}

type running struct {
	t            *testing.T
	pool         *pgxpool.Pool
	base         string
	tenant       uuid.UUID
	staff, admin uuid.UUID
	staffTok     string
	adminTok     string
	stopped      chan error
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return port
}

// serviceEnv prepares a database and the environment app.Run reads, as a deployment sets it.
func serviceEnv(t *testing.T) (*pgxpool.Pool, string, int) {
	t.Helper()
	pool := testkit.NewPostgres(t)
	for _, e := range []metadata.EntityDefinition{ctxMembers, ctxDocs} {
		_, err := reconciler.Reconcile(context.Background(), pool, uuid.Nil, e, nil)
		require.NoError(t, err)
	}
	u, err := url.Parse(testkit.BaseDatabaseURL())
	require.NoError(t, err)
	u.Path = "/" + pool.Config().ConnConfig.Database

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	private := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	public, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	dir := t.TempDir()
	privatePath, publicPath := filepath.Join(dir, "private.pem"), filepath.Join(dir, "public.pem")
	require.NoError(t, os.WriteFile(privatePath, private, 0o600))
	require.NoError(t, os.WriteFile(publicPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: public}), 0o600))

	port := freePort(t)
	for k, v := range map[string]string{
		"DATABASE_URL": u.String(), "RABBITMQ_URL": "amqp://unused", "HOST": "127.0.0.1", "PORT": strconv.Itoa(port),
		"AUTH_JWT_PUBLIC_KEY_PATH": publicPath, "AUTH_JWT_PRIVATE_KEY_PATH": privatePath, "JWKS_KEYS_DIR": "", "JWKS_URL": "",
		"AUTH_CONTEXT_ENTITY": "ctxt.members",
		// Long, so only invalidation can make a change show during the test.
		"AUTH_CONTEXT_CACHE_TTL_SECONDS": "3600",
		"ROLE_CACHE_TTL_SECONDS":         "0", "POLICY_CACHE_TTL_SECONDS": "0",
	} {
		t.Setenv(k, v)
	}
	return pool, string(private), port
}

// startService boots app.Run with a member who is staff, an admin, and two policies: staff read docs,
// except a member whose context record says suspended.
func startService(t *testing.T) *running {
	t.Helper()
	pool, private, port := serviceEnv(t)
	ctx := context.Background()
	s := &running{t: t, pool: pool, base: "http://127.0.0.1:" + strconv.Itoa(port),
		tenant: uuid.New(), staff: uuid.New(), admin: uuid.New(), stopped: make(chan error, 1)}
	for user, role := range map[uuid.UUID]string{s.staff: "staff", s.admin: "admin"} {
		_, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, $3)", s.tenant, user, role)
		require.NoError(t, err)
	}
	router := tenancy.NewRouter(pool, tenancy.NewRegistryCache(tenancy.NewPostgresTenantRegistry(pool)), nil)
	permissions := permission.NewPermissionService(policypg.NewPostgresPolicyStore(router))
	suspended := permission.AttributeCondition("suspended", permission.OpEq, permission.LiteralValue(json.RawMessage("true")))
	onContext := permission.SubjectContext
	for _, p := range []struct {
		effect permission.PolicyEffect
		when   *permission.PolicyCondition
	}{{permission.EffectAllow, nil}, {permission.EffectDeny, &suspended}} {
		_, err := permissions.CreatePolicy(ctx, s.tenant, "ctxt.docs", "read", []string{"staff"}, true, p.when, nil, "", &onContext, p.effect)
		require.NoError(t, err)
	}
	var err error
	s.staffTok, err = jwks.MintRSAJWT(private, s.tenant, s.staff, 600)
	require.NoError(t, err)
	s.adminTok, err = jwks.MintRSAJWT(private, s.tenant, s.admin, 600)
	require.NoError(t, err)

	runCtx, stop := context.WithCancel(ctx)
	go func() {
		s.stopped <- Run(runCtx, Options{Name: "ctx-service", Entities: []metadata.EntityDefinition{ctxMembers, ctxDocs}})
	}()
	t.Cleanup(func() {
		stop()
		select {
		case <-s.stopped:
		case <-time.After(20 * time.Second):
			t.Log("the service did not stop")
		}
	})
	require.Eventually(t, func() bool {
		select {
		case err := <-s.stopped:
			s.stopped <- err
			t.Fatalf("the service stopped: %v", err)
		default:
		}
		res, err := http.Get(s.base + "/health")
		if err != nil {
			return false
		}
		res.Body.Close()
		return res.StatusCode == 200
	}, 20*time.Second, 50*time.Millisecond)
	return s
}

func (s *running) call(method, path, token string, body any) (int, map[string]any) {
	s.t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		require.NoError(s.t, err)
	}
	req, err := http.NewRequest(method, s.base+path, bytes.NewReader(raw))
	require.NoError(s.t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	require.NoError(s.t, err)
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	data, _ := out["data"].(map[string]any)
	return res.StatusCode, data
}

func (s *running) staffReads() int {
	status, _ := s.call("GET", "/api/ctxt.docs", s.staffTok, nil)
	return status
}

func TestADenyOnAContextAttributeHoldsAndFollowsTheRecord(t *testing.T) {
	s := startService(t)
	require.Equal(t, 200, s.staffReads(), "no context record: nothing says suspended")

	// Creating the record takes effect at once, although "no record" was just cached.
	status, created := s.call("POST", "/api/ctxt.members", s.adminTok,
		map[string]any{"data": map[string]any{"userId": s.staff.String(), "suspended": true}})
	require.Equal(t, 201, status)
	assert.Equal(t, 403, s.staffReads(), "a suspended member is refused")
	id := created["id"].(string)

	status, _ = s.call("PATCH", "/api/ctxt.members/"+id, s.adminTok, map[string]any{"version": 1, "data": map[string]any{"suspended": false}})
	require.Equal(t, 200, status)
	assert.Equal(t, 200, s.staffReads(), "an update takes effect at once")
	status, _ = s.call("PATCH", "/api/ctxt.members/"+id, s.adminTok, map[string]any{"version": 2, "data": map[string]any{"suspended": true}})
	require.Equal(t, 200, status)
	require.Equal(t, 403, s.staffReads())

	// Moving the record to someone else stops it describing this member.
	status, _ = s.call("PATCH", "/api/ctxt.members/"+id, s.adminTok, map[string]any{"version": 3, "data": map[string]any{"userId": uuid.NewString()}})
	require.Equal(t, 200, status)
	assert.Equal(t, 200, s.staffReads(), "the previous owner of the record is forgotten too")
	status, _ = s.call("PATCH", "/api/ctxt.members/"+id, s.adminTok, map[string]any{"version": 4, "data": map[string]any{"userId": s.staff.String()}})
	require.Equal(t, 200, status)
	require.Equal(t, 403, s.staffReads())

	status, _ = s.call("DELETE", "/api/ctxt.members/"+id, s.adminTok, map[string]any{"version": 5})
	require.Equal(t, 200, status)
	assert.Equal(t, 200, s.staffReads(), "a deleted record no longer describes anyone")
}

func TestARequestIsRefusedWhenTheContextAttributesCannotBeRead(t *testing.T) {
	s := startService(t)
	_, err := s.pool.Exec(context.Background(), `ALTER TABLE ctxt.members RENAME COLUMN "userId" TO gone`)
	require.NoError(t, err)
	assert.NotEqual(t, 200, s.staffReads(), "without the attributes a deny cannot be judged, so nothing is allowed")
}

func TestAContextRecordCannotSpeakForTheCaller(t *testing.T) {
	s := startService(t)
	status, _ := s.call("POST", "/api/ctxt.members", s.adminTok,
		map[string]any{"data": map[string]any{"userId": s.staff.String(), "suspended": false, "roles": "admin"}})
	require.Equal(t, 201, status)
	status, me := s.call("GET", "/auth/me", s.staffTok, nil)
	require.Equal(t, 200, status)
	assert.Equal(t, []any{"staff"}, me["roles"])
	policies := permission.AttributeCondition("roles", permission.OpContains, permission.LiteralValue(json.RawMessage(`"staff"`)))
	rc := &permission.RequestContext{TenantID: s.tenant.String(), UserID: s.staff.String(), Roles: []string{"staff"}, HasRoles: true,
		ContextAttributes: map[string]json.RawMessage{"roles": json.RawMessage(`"admin"`)}}
	assert.True(t, permission.EvaluateCondition(policies, rc.ToValue(), rc).IsPassed(), "$ctx.roles is the caller's roles whatever an attribute is called")
}

func TestAServiceDoesNotStartOnAContextEntityItCannotUse(t *testing.T) {
	for name, value := range map[string]string{"unknown entity": "ctxt.nobody", "no userId field": "ctxt.docs"} {
		t.Run(name, func(t *testing.T) {
			serviceEnv(t)
			t.Setenv("AUTH_CONTEXT_ENTITY", value)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			err := Run(ctx, Options{Name: "ctx-service", Entities: []metadata.EntityDefinition{ctxMembers, ctxDocs}})
			require.ErrorContains(t, err, "AUTH_CONTEXT_ENTITY")
		})
	}
}

// With two records for one member there is no telling which one speaks, and taking either could
// drop the one a deny reads.
func TestAMemberWithTwoContextRecordsIsRefused(t *testing.T) {
	s := startService(t)
	for _, suspended := range []bool{false, true} {
		status, _ := s.call("POST", "/api/ctxt.members", s.adminTok,
			map[string]any{"data": map[string]any{"userId": s.staff.String(), "suspended": suspended}})
		require.Equal(t, 201, status)
	}
	assert.NotEqual(t, 200, s.staffReads())
}
