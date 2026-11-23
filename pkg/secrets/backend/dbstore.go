package backend

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound: nothing is stored under the reference.
var ErrNotFound = errors.New("secret not found")

// DBStore keeps credentials in metap's own database, encrypted with a Keyring.
type DBStore struct {
	pool *pgxpool.Pool
	keys *Keyring
}

func NewDBStore(pool *pgxpool.Pool, keys *Keyring) *DBStore { return &DBStore{pool: pool, keys: keys} }

func (s *DBStore) Get(ctx context.Context, ref string) (string, error) {
	var ciphertext, nonce []byte
	var keyID string
	err := s.pool.QueryRow(ctx,
		`SELECT ciphertext, nonce, key_id FROM metadata.tenant_secrets WHERE ref = $1`, ref).
		Scan(&ciphertext, &nonce, &keyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	plaintext, err := s.keys.open(ref, keyID, nonce, ciphertext)
	return string(plaintext), err
}

func (s *DBStore) Put(ctx context.Context, ref, value string) error {
	ciphertext, nonce, keyID, err := s.keys.seal(ref, []byte(value))
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO metadata.tenant_secrets (ref, ciphertext, nonce, key_id, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (ref) DO UPDATE SET ciphertext = EXCLUDED.ciphertext, nonce = EXCLUDED.nonce,
			key_id = EXCLUDED.key_id, updated_at = now()`, ref, ciphertext, nonce, keyID)
	return err
}

func (s *DBStore) Delete(ctx context.Context, ref string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM metadata.tenant_secrets WHERE ref = $1`, ref)
	return err
}

// UpdatedAt is when the credential was last written; ErrNotFound when it is not stored here.
func (s *DBStore) UpdatedAt(ctx context.Context, ref string) (time.Time, error) {
	var at time.Time
	err := s.pool.QueryRow(ctx, `SELECT updated_at FROM metadata.tenant_secrets WHERE ref = $1`, ref).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, ErrNotFound
	}
	return at, err
}
