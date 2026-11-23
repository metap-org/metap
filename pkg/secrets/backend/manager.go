package backend

import (
	"context"
	"errors"
	"fmt"
	"github.com/metap-org/metap/pkg/secrets"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The kinds of external secret manager a deployment can be configured with (KindEnv: none — the
// read-only environment fallback).
const (
	KindVault = "vault"
	KindAWS   = "aws"
	KindGCP   = "gcp"
	KindEnv   = "env"
)

// Where a stored credential lives.
const (
	SourceDatabase = "database"
	SourceExternal = "external"
)

// Manager is the secrets.Store of a deployment, and the one place that knows its two
// homes for a tenant credential:
//
//   - the metap database, sealed with an application key (when SECRETS_ENCRYPTION_KEYS is set);
//   - the external secret manager (Vault, AWS or GCP Secret Manager), or, when none is
//     configured, the environment — read-only.
//
// Reads look in the database first, then outside. A write through the SecretStore interface (SSO)
// goes to the external manager when it accepts writes, else to the database. The Credentials tab
// chooses explicitly (Put) and keeps exactly one copy: saving in one place removes the other.
type Manager struct {
	db       *DBStore
	external secrets.Store
	kind     string
}

var _ secrets.Store = (*Manager)(nil)

// NewManager builds the Manager for the deployment's configuration over pool (the metap database).
func NewManager(ctx context.Context, config Config, pool *pgxpool.Pool) (*Manager, error) {
	external, kind, err := buildExternal(ctx, config)
	if err != nil {
		return nil, err
	}
	m := &Manager{external: external, kind: kind}
	if config.EncryptionKeys != "" {
		ring, err := ParseKeyring(config.EncryptionKeys)
		if err != nil {
			return nil, err
		}
		m.db = NewDBStore(pool, ring)
	}
	return m, nil
}

// NewManagerWith assembles a Manager from parts (tests, custom wiring). db may be nil.
func NewManagerWith(db *DBStore, external secrets.Store, kind string) *Manager {
	return &Manager{db: db, external: external, kind: kind}
}

func (m *Manager) externalWritable() bool { return m.kind != KindEnv }

func (m *Manager) DbCredentials(ctx context.Context, ref string) (*secrets.DbCreds, error) {
	return m.external.DbCredentials(ctx, ref)
}

func (m *Manager) GetSecret(ctx context.Context, ref string) (string, error) {
	if m.db != nil {
		value, err := m.db.Get(ctx, ref)
		if err == nil {
			return value, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return "", err
		}
	}
	return m.external.GetSecret(ctx, ref)
}

func (m *Manager) PutSecret(ctx context.Context, ref, value string) error {
	if m.externalWritable() || m.db == nil {
		return m.external.PutSecret(ctx, ref, value)
	}
	return m.db.Put(ctx, ref, value)
}

func (m *Manager) DeleteSecret(ctx context.Context, ref string) error {
	if m.db != nil {
		if err := m.db.Delete(ctx, ref); err != nil {
			return err
		}
	}
	if !m.externalWritable() {
		return nil
	}
	return m.external.DeleteSecret(ctx, ref)
}

// Capabilities: which homes a credential can be saved to here.
type Capabilities struct {
	// Database: SECRETS_ENCRYPTION_KEYS is set, so credentials can be stored encrypted in metap.
	Database bool
	// External: an external secret manager accepts writes; Kind names it (KindVault, ...).
	External bool
	Kind     string
}

func (m *Manager) Capabilities() Capabilities {
	return Capabilities{Database: m.db != nil, External: m.externalWritable(), Kind: m.kind}
}

// Status of the credential under ref: where it is stored ("" when nowhere) and, for the
// database, when it was last written.
type Status struct {
	Source    string // "", SourceDatabase, SourceExternal
	UpdatedAt time.Time
}

func (m *Manager) Status(ctx context.Context, ref string) (Status, error) {
	if m.db != nil {
		at, err := m.db.UpdatedAt(ctx, ref)
		if err == nil {
			return Status{Source: SourceDatabase, UpdatedAt: at}, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Status{}, err
		}
	}
	if _, err := m.external.GetSecret(ctx, ref); err == nil {
		return Status{Source: SourceExternal}, nil
	}
	return Status{}, nil
}

// Put saves value under ref in the chosen home and removes it from the other, so one copy exists.
func (m *Manager) Put(ctx context.Context, ref, source, value string) error {
	switch source {
	case SourceDatabase:
		if m.db == nil {
			return errors.New("storing credentials in the database is not enabled on this deployment (SECRETS_ENCRYPTION_KEYS is not set)")
		}
		if err := m.db.Put(ctx, ref, value); err != nil {
			return err
		}
		if m.externalWritable() {
			_ = m.external.DeleteSecret(ctx, ref) // best effort: the database copy wins on read anyway
		}
		return nil
	case SourceExternal:
		if !m.externalWritable() {
			return fmt.Errorf("no external secret manager is configured on this deployment (set VAULT_ADDR, AWS_SECRETS_REGION or GCP_SECRETS_PROJECT_ID)")
		}
		if err := m.external.PutSecret(ctx, ref, value); err != nil {
			return err
		}
		if m.db != nil {
			return m.db.Delete(ctx, ref)
		}
		return nil
	}
	return fmt.Errorf("unknown credential source %q", source)
}

// Remove deletes the credential from wherever it is stored.
func (m *Manager) Remove(ctx context.Context, ref string) error { return m.DeleteSecret(ctx, ref) }
