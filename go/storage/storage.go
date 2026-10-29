// Package storage is object storage — a swappable interface, ported from metap's Rust
// metap-storage crate. Same shape as Cache / a secret store: one interface, callers hold an
// ObjectStore, never the concrete type.
//
// ObjectStore deliberately doesn't expose S3 concepts (buckets, ETags, versioning, multipart) —
// a plain key/bytes blob store, the smallest shape that still fits every backend under
// consideration.
//
// Security-first, not caller discipline: every method takes tenantID as a mandatory first
// parameter. S3ObjectStore turns (tenantID, key) into the real S3 key (tenantID/key) internally —
// a caller can never construct a request that reads/writes another tenant's objects, and
// validateKey rejects a key shaped to escape its own tenant prefix before it ever reaches the
// backend.
package storage

import (
	"context"
	"errors"
	"strings"
	"unicode"

	"github.com/google/uuid"
)

// ObjectStore is the swappable interface — S3ObjectStore is the only impl today.
type ObjectStore interface {
	// Put overwrites whatever was already at key, if anything — "last write wins", same as a
	// plain S3 PUT.
	Put(ctx context.Context, tenantID uuid.UUID, key string, data []byte, contentType string) error
	// Get returns (nil, false, nil) for "no object at this key" — a normal, expected outcome,
	// never mixed into the error return.
	Get(ctx context.Context, tenantID uuid.UUID, key string) ([]byte, bool, error)
	// Delete on a key that doesn't exist is not an error — matches S3's own idempotent DELETE.
	Delete(ctx context.Context, tenantID uuid.UUID, key string) error
	Exists(ctx context.Context, tenantID uuid.UUID, key string) (bool, error)
}

// validateKey rejects a key shaped to misbehave once prefixed with a tenant id — ".."/"."  path
// segments, a leading '/', empty, over S3's own 1024-byte key limit, or a control character.
func validateKey(key string) error {
	if key == "" {
		return errors.New("object key must not be empty")
	}
	if len(key) > 1024 {
		return errors.New("object key must be at most 1024 bytes")
	}
	if strings.HasPrefix(key, "/") {
		return errors.New("object key must not start with '/'")
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == ".." || segment == "." {
			return errors.New("object key must not contain '.' or '..' path segments")
		}
	}
	for _, r := range key {
		if unicode.IsControl(r) {
			return errors.New("object key must not contain control characters")
		}
	}
	return nil
}
