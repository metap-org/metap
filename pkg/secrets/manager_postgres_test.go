//go:build integration

package secrets

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/control"
	"github.com/metap-org/metap/pkg/testkit"
)

// memStore is an external secret manager that takes writes (stands in for Vault/AWS/GCP).
type memStore struct {
	mu   sync.Mutex
	data map[string]string
}

func newMemStore() *memStore { return &memStore{data: map[string]string{}} }

func (m *memStore) DbCredentials(context.Context, string) (*control.DbCreds, error) {
	return nil, errors.New("not used")
}
func (m *memStore) GetSecret(_ context.Context, ref string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.data[ref]; ok {
		return v, nil
	}
	return "", errors.New("not found")
}
func (m *memStore) PutSecret(_ context.Context, ref, v string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[ref] = v
	return nil
}
func (m *memStore) DeleteSecret(_ context.Context, ref string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, ref)
	return nil
}

func dbStore(t *testing.T) *DBStore {
	pool := testkit.NewPostgres(t)
	key, err := GenerateKey()
	require.NoError(t, err)
	ring, err := ParseKeyring("k1:" + key)
	require.NoError(t, err)
	return NewDBStore(pool, ring)
}

func TestDBStoreEncryptsAtRest(t *testing.T) {
	ctx := context.Background()
	db := dbStore(t)
	require.NoError(t, db.Put(ctx, "REF_A", "Bearer s3cret"))

	var raw []byte
	require.NoError(t, db.pool.QueryRow(ctx, `SELECT ciphertext FROM metadata.tenant_secrets WHERE ref = 'REF_A'`).Scan(&raw))
	assert.NotContains(t, string(raw), "s3cret")

	got, err := db.Get(ctx, "REF_A")
	require.NoError(t, err)
	assert.Equal(t, "Bearer s3cret", got)
	require.NoError(t, db.Put(ctx, "REF_A", "Bearer other"), "saving again replaces")
	got, _ = db.Get(ctx, "REF_A")
	assert.Equal(t, "Bearer other", got)

	_, err = db.Get(ctx, "REF_MISSING")
	assert.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, db.Delete(ctx, "REF_A"))
	_, err = db.Get(ctx, "REF_A")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestManagerKeepsOneCopyAndReadsDatabaseFirst(t *testing.T) {
	ctx := context.Background()
	ext := newMemStore()
	m := NewManagerWith(dbStore(t), ext, KindVault)
	assert.Equal(t, Capabilities{Database: true, External: true, Kind: KindVault}, m.Capabilities())

	st, err := m.Status(ctx, "REF")
	require.NoError(t, err)
	assert.Equal(t, "", st.Source, "nothing stored yet")

	require.NoError(t, m.Put(ctx, "REF", SourceExternal, "from-vault"))
	st, _ = m.Status(ctx, "REF")
	assert.Equal(t, SourceExternal, st.Source)
	got, err := m.GetSecret(ctx, "REF")
	require.NoError(t, err)
	assert.Equal(t, "from-vault", got)

	// Moving to the database removes the external copy: one source of truth.
	require.NoError(t, m.Put(ctx, "REF", SourceDatabase, "from-db"))
	st, _ = m.Status(ctx, "REF")
	assert.Equal(t, SourceDatabase, st.Source)
	assert.False(t, st.UpdatedAt.IsZero())
	_, err = ext.GetSecret(ctx, "REF")
	assert.Error(t, err, "the external copy is gone")
	got, _ = m.GetSecret(ctx, "REF")
	assert.Equal(t, "from-db", got)

	// ...and back.
	require.NoError(t, m.Put(ctx, "REF", SourceExternal, "again"))
	_, err = m.db.Get(ctx, "REF")
	assert.ErrorIs(t, err, ErrNotFound, "the database copy is gone")

	require.NoError(t, m.Remove(ctx, "REF"))
	st, _ = m.Status(ctx, "REF")
	assert.Equal(t, "", st.Source)
}

func TestManagerWithoutExternalOrDatabase(t *testing.T) {
	ctx := context.Background()
	t.Setenv("REF_ENV", "from-environment")

	// Environment only (the default deployment): reads work, the external mode refuses, and the
	// SecretStore write path (SSO) falls back to the database when it is enabled.
	m := NewManagerWith(dbStore(t), control.EnvStore{}, KindEnv)
	assert.Equal(t, Capabilities{Database: true, External: false, Kind: KindEnv}, m.Capabilities())
	st, _ := m.Status(ctx, "REF_ENV")
	assert.Equal(t, SourceExternal, st.Source, "an operator-provisioned environment value reads as external")
	assert.ErrorContains(t, m.Put(ctx, "REF", SourceExternal, "x"), "no external secret manager")
	require.NoError(t, m.PutSecret(ctx, "REF_SSO", "sso-secret"))
	got, err := m.GetSecret(ctx, "REF_SSO")
	require.NoError(t, err)
	assert.Equal(t, "sso-secret", got)
	require.NoError(t, m.DeleteSecret(ctx, "REF_SSO"), "deleting needs no write to the environment")

	// Database disabled: the database mode refuses.
	off := NewManagerWith(nil, control.EnvStore{}, KindEnv)
	assert.Equal(t, Capabilities{Kind: KindEnv}, off.Capabilities())
	assert.ErrorContains(t, off.Put(ctx, "REF", SourceDatabase, "x"), "not enabled")
	assert.Error(t, off.PutSecret(ctx, "REF", "x"), "the environment's own refusal surfaces")
}
