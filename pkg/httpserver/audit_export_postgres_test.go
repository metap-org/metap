//go:build integration

package httpserver_test

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"github.com/metap-org/metap/pkg/auth/jwks"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/audit"
)

// Roadmap 115 M9: the tenant's audit trail exports as CSV or JSON, filtered, admins only, never
// another tenant's rows.
func TestAuditExportCSVAndJSON(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenant, other := uuid.New(), uuid.New()
	adminID, analystID := uuid.New(), uuid.New()
	for _, r := range []struct {
		user uuid.UUID
		role string
	}{{adminID, "admin"}, {analystID, "analyst"}} {
		_, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, $3)", tenant, r.user, r.role)
		require.NoError(t, err)
	}
	state, privPEM := newTestAppState(t, pool, httpTestEntity())
	store := audit.NewPostgresStore(pool)
	state.Audit = store
	base := startTestServer(t, state, nil)
	adminToken, err := jwks.MintRSAJWT(privPEM, tenant, adminID, 3600)
	require.NoError(t, err)
	analystToken, err := jwks.MintRSAJWT(privPEM, tenant, analystID, 3600)
	require.NoError(t, err)

	now := time.Now()
	for i, who := range []uuid.UUID{adminID, adminID, analystID} {
		who := who
		require.NoError(t, store.Record(ctx, tenant, audit.Entry{TenantID: tenant, Entity: "waf.zones", RecordID: uuid.New(),
			Action: audit.ActionUpdate, ActorUserID: &who, OccurredAt: now.Add(time.Duration(i) * time.Second),
			Diff: audit.JsonObject{"name": json.RawMessage(`{"before":"a, \"b\"","after":"c"}`)}}))
	}
	require.NoError(t, store.Record(ctx, other, audit.Entry{TenantID: other, Entity: "waf.zones", RecordID: uuid.New(),
		Action: audit.ActionCreate, OccurredAt: now, Diff: audit.JsonObject{}}))

	denied := doRequest(t, "GET", base+"/admin/audit/export", analystToken, nil)
	assert.Equal(t, 403, denied.StatusCode)
	denied.Body.Close()

	res := doRequest(t, "GET", base+"/admin/audit/export", adminToken, nil)
	require.Equal(t, 200, res.StatusCode)
	assert.Contains(t, res.Header.Get("Content-Type"), "text/csv")
	assert.Contains(t, res.Header.Get("Content-Disposition"), "attachment")
	rows, err := csv.NewReader(res.Body).ReadAll()
	res.Body.Close()
	require.NoError(t, err, "quotes and commas inside a diff survive CSV")
	require.Len(t, rows, 4, "header + this tenant's 3 entries, not the other tenant's")
	assert.Equal(t, "seq", rows[0][0])
	assert.Contains(t, rows[1][8], `a, \"b\"`)

	res = doRequest(t, "GET", base+"/admin/audit/export?format=json&actor="+analystID.String(), adminToken, nil)
	require.Equal(t, 200, res.StatusCode)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	var entries []audit.Row
	require.NoError(t, json.Unmarshal(body, &entries), string(body))
	require.Len(t, entries, 1)
	assert.Equal(t, analystID, *entries[0].ActorUserID)

	for _, bad := range []string{"?format=xml", "?from=yesterday", "?actor=nobody"} {
		res = doRequest(t, "GET", base+"/admin/audit/export"+bad, adminToken, nil)
		assert.Equal(t, 400, res.StatusCode, bad)
		res.Body.Close()
	}
}
