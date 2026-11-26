package credentials

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/jobs/cron"
)

// PgStore keeps credential rows in metadata.tenant_credentials.
type PgStore struct{ deps kernel.Deps }

func NewPgStore(deps kernel.Deps) *PgStore { return &PgStore{deps: deps} }

const columns = `id, name, description, kind, source, username, created_at, updated_at`

func scan(row pgx.Row) (Row, error) {
	var c Row
	err := row.Scan(&c.ID, &c.Name, &c.Description, &c.Kind, &c.Source, &c.Username, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

func nameTaken(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrNameTaken
	}
	return err
}

func (s *PgStore) Get(ctx context.Context, tenant, id uuid.UUID) (Row, error) {
	c, err := scan(s.deps.DB().QueryRow(ctx,
		`SELECT `+columns+` FROM metadata.tenant_credentials WHERE tenant_id = $1 AND id = $2`, tenant, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

func (s *PgStore) List(ctx context.Context, tenant uuid.UUID) ([]Row, error) {
	rows, err := s.deps.DB().Query(ctx,
		`SELECT `+columns+` FROM metadata.tenant_credentials WHERE tenant_id = $1 ORDER BY lower(name)`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Row
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, c)
	}
	return list, rows.Err()
}

func (s *PgStore) Insert(ctx context.Context, tenant uuid.UUID, r Row, createdBy *uuid.UUID) error {
	_, err := s.deps.DB().Exec(ctx, `INSERT INTO metadata.tenant_credentials (id, tenant_id, name, description, kind, source, username, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, r.ID, tenant, r.Name, r.Description, r.Kind, r.Source, r.Username, createdBy)
	return nameTaken(err)
}

func (s *PgStore) Update(ctx context.Context, tenant uuid.UUID, r Row) error {
	_, err := s.deps.DB().Exec(ctx, `UPDATE metadata.tenant_credentials
		SET name = $3, description = $4, source = $5, username = $6, updated_at = now() WHERE tenant_id = $1 AND id = $2`,
		tenant, r.ID, r.Name, r.Description, r.Source, r.Username)
	return nameTaken(err)
}

func (s *PgStore) Delete(ctx context.Context, tenant, id uuid.UUID) error {
	_, err := s.deps.DB().Exec(ctx, `DELETE FROM metadata.tenant_credentials WHERE tenant_id = $1 AND id = $2`, tenant, id)
	return err
}

func (s *PgStore) Usage(ctx context.Context, tenant uuid.UUID) (map[uuid.UUID][]*CredentialUse, error) {
	jobs, err := cron.ListJobs(ctx, s.deps.Cfg.Shared, tenant)
	if err != nil {
		return nil, err
	}
	used := map[uuid.UUID][]*CredentialUse{}
	for _, job := range jobs {
		for _, id := range cron.ReferencedCredentials(job.TargetType, job.TargetConfig) {
			used[id] = append(used[id], &CredentialUse{Kind: "cronJob", Id: job.ID.String(), Name: job.Name})
		}
	}
	return used, nil
}
