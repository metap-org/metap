// Package attachments holds file attachment metadata — ported from metap's Rust
// metap-attachments crate, metap-storage's ObjectStore's first real consumer. Deliberately not a
// metadata-driven entity: RecordID can point at a row in any entity's table, which rules out a
// typed foreign key. No HTTP, no business-entity knowledge.
//
// Two storage mechanisms, not one: every function takes a tableName rather than hardcoding
// "attachments" — a caller passes the shared default table for most entities, or a dedicated
// table (EnsureDedicatedTable) for one expecting heavy attachment volume.
//
// Known tradeoff, not a bug: unlike a real Reference field, nothing here blocks deleting the
// record an attachment points at — RecordID can't carry a DB-level FK across arbitrary target
// tables. Deleting a referenced record leaves its attachment rows pointing at nothing (the
// object-storage blob itself is untouched, no data loss), just a dangling metadata reference.
package attachments

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Record struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	EntityName  string
	RecordID    uuid.UUID
	Filename    string
	Key         string
	Size        int64
	ContentType *string
	CreatedBy   *uuid.UUID
	CreatedAt   time.Time
}

type executor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var tableNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// validateTableName: SQL can't parameterize an identifier — every function below interpolates
// tableName directly into its query, so this is checked once, centrally, on every call.
func validateTableName(tableName string) error {
	if !tableNamePattern.MatchString(tableName) {
		return fmt.Errorf("invalid attachments table name %q — must match ^[a-z][a-z0-9_]*$", tableName)
	}
	return nil
}

func rowToRecord(row pgx.Row) (*Record, error) {
	var r Record
	if err := row.Scan(&r.ID, &r.TenantID, &r.EntityName, &r.RecordID, &r.Filename, &r.Key, &r.Size,
		&r.ContentType, &r.CreatedBy, &r.CreatedAt); err != nil {
		return nil, err
	}
	return &r, nil
}

// EnsureDedicatedTable creates a dedicated attachments table for one entity expecting heavy
// volume, if it doesn't already exist. Idempotent (IF NOT EXISTS) — safe to call on every boot.
func EnsureDedicatedTable(ctx context.Context, pool interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}, tableName string) error {
	if err := validateTableName(tableName); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
		tenant_id    uuid NOT NULL,
		entity_name  text NOT NULL,
		record_id    uuid NOT NULL,
		filename     text NOT NULL,
		key          text NOT NULL,
		size         bigint NOT NULL,
		content_type text,
		created_by   uuid,
		created_at   timestamptz NOT NULL DEFAULT now()
	)`, tableName)); err != nil {
		return err
	}
	_, err := pool.Exec(ctx, fmt.Sprintf(
		"CREATE INDEX IF NOT EXISTS %s_tenant_entity_record_idx ON %s (tenant_id, entity_name, record_id)",
		tableName, tableName))
	return err
}

// CreateAttachment inserts one attachment row.
func CreateAttachment(ctx context.Context, e executor, tableName string, tenantID uuid.UUID, entityName string,
	recordID uuid.UUID, filename, key string, size int64, contentType *string, createdBy *uuid.UUID) (*Record, error) {
	if err := validateTableName(tableName); err != nil {
		return nil, err
	}
	row := e.QueryRow(ctx, fmt.Sprintf(
		`INSERT INTO %s (tenant_id, entity_name, record_id, filename, key, size, content_type, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING id, tenant_id, entity_name, record_id, filename, key, size, content_type, created_by, created_at`,
		tableName),
		tenantID, entityName, recordID, filename, key, size, contentType, createdBy)
	return rowToRecord(row)
}

// ListAttachments returns every attachment for one record, newest first.
func ListAttachments(ctx context.Context, e executor, tableName string, tenantID uuid.UUID, entityName string, recordID uuid.UUID) ([]*Record, error) {
	if err := validateTableName(tableName); err != nil {
		return nil, err
	}
	rows, err := e.Query(ctx, fmt.Sprintf(
		`SELECT id, tenant_id, entity_name, record_id, filename, key, size, content_type, created_by, created_at
		 FROM %s WHERE tenant_id = $1 AND entity_name = $2 AND record_id = $3
		 ORDER BY created_at DESC`, tableName),
		tenantID, entityName, recordID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Record
	for rows.Next() {
		r, err := rowToRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetAttachment returns one attachment by id, or nil if not found.
func GetAttachment(ctx context.Context, e executor, tableName string, tenantID, id uuid.UUID) (*Record, error) {
	if err := validateTableName(tableName); err != nil {
		return nil, err
	}
	row := e.QueryRow(ctx, fmt.Sprintf(
		`SELECT id, tenant_id, entity_name, record_id, filename, key, size, content_type, created_by, created_at
		 FROM %s WHERE tenant_id = $1 AND id = $2`, tableName),
		tenantID, id)
	rec, err := rowToRecord(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return rec, err
}

// DeleteAttachment deletes one attachment by id. Deleting a non-existent one is not an error.
func DeleteAttachment(ctx context.Context, e executor, tableName string, tenantID, id uuid.UUID) error {
	if err := validateTableName(tableName); err != nil {
		return err
	}
	_, err := e.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE tenant_id = $1 AND id = $2", tableName), tenantID, id)
	return err
}
