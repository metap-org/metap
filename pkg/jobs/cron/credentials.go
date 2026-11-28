package cron

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ReferencedCredentials lists the tenant credential ids a job's target uses: a webhook's
// `credentialId`, or those of the webhook steps of a chain. It is what the Credentials tab counts
// as "used by" and what stops a job from naming a credential that does not exist.
func ReferencedCredentials(targetType string, targetConfig json.RawMessage) []uuid.UUID {
	var ids []uuid.UUID
	add := func(raw json.RawMessage) {
		var cfg struct {
			CredentialID string `json:"credentialId"`
		}
		if json.Unmarshal(raw, &cfg) != nil || cfg.CredentialID == "" {
			return
		}
		if id, err := uuid.Parse(cfg.CredentialID); err == nil {
			ids = append(ids, id)
		}
	}
	switch tt, _ := ParseTargetType(targetType); tt {
	case TargetWebhook:
		add(targetConfig)
	case TargetSteps:
		var chain struct {
			Steps []struct {
				TargetType   string          `json:"targetType"`
				TargetConfig json.RawMessage `json:"targetConfig"`
			} `json:"steps"`
		}
		if json.Unmarshal(targetConfig, &chain) != nil {
			return nil
		}
		for _, step := range chain.Steps {
			if st, _ := ParseTargetType(step.TargetType); st == TargetWebhook {
				add(step.TargetConfig)
			}
		}
	}
	return ids
}

// MissingCredential is the first id of ids that is not one of tenantID's credentials; found is false
// when they all are.
func MissingCredential(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, ids []uuid.UUID) (missing uuid.UUID, found bool, err error) {
	for _, id := range ids {
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM metadata.tenant_credentials WHERE tenant_id = $1 AND id = $2)`,
			tenantID, id).Scan(&exists); err != nil {
			return uuid.Nil, false, err
		}
		if !exists {
			return id, true, nil
		}
	}
	return uuid.Nil, false, nil
}
