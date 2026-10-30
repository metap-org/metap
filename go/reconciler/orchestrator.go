// Fan-out multi-tenant. metadata.reconciler_entity_deployments tracks version skew:
// desired_version > applied_version is both the work queue and the global resume source — an
// orchestrator process dying mid-rollout loses nothing, a restart just re-queries this table.
// Pull-based claim with FOR UPDATE SKIP LOCKED so many workers never grab the same
// (tenant, entity). Concurrency bounded by a caller-sized limit. Failure classified from
// SQLSTATE so only genuinely transient errors retry automatically. Wave rollout so a bad pack is
// caught on 1-2 canary tenants, not the whole fleet.
//
// What this file does **not** do: run forever as a service. Turning this into a long-running
// process is a separate deliverable this package doesn't include.
package reconciler

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/metap-org/metap-go/metadata"
)

type ClaimedEntity struct {
	TenantID       uuid.UUID
	EntityName     string
	DesiredVersion int64
}

// ClaimedPair is one claimed entity plus its (already-resolved) EntityDefinition — what
// TopoSortWaves needs to find Reference-field dependency edges, since ClaimedEntity alone (just
// tenant/name/version) carries no field information.
type ClaimedPair struct {
	Claimed ClaimedEntity
	Def     metadata.EntityDefinition
}

// TopoSortWaves orders a claimed batch into dependency waves so a Reference field's target
// entity is reconciled before the entity that references it, when both are claimed together.
// Only intra-tenant edges matter (FK targets live in the claiming tenant's own database) — a
// dependency cycle can't be linearized: the remaining cyclic entities are placed together in
// one final wave, best-effort.
func TopoSortWaves(entities []ClaimedPair) [][]ClaimedEntity {
	byTenant := map[uuid.UUID][]ClaimedPair{}
	var tenantOrder []uuid.UUID
	for _, e := range entities {
		if _, ok := byTenant[e.Claimed.TenantID]; !ok {
			tenantOrder = append(tenantOrder, e.Claimed.TenantID)
		}
		byTenant[e.Claimed.TenantID] = append(byTenant[e.Claimed.TenantID], e)
	}

	var perTenantWaves [][][]ClaimedEntity
	for _, tenantID := range tenantOrder {
		perTenantWaves = append(perTenantWaves, tenantWaves(byTenant[tenantID]))
	}

	waveCount := 0
	for _, waves := range perTenantWaves {
		if len(waves) > waveCount {
			waveCount = len(waves)
		}
	}
	merged := make([][]ClaimedEntity, waveCount)
	for _, waves := range perTenantWaves {
		for i, wave := range waves {
			merged[i] = append(merged[i], wave...)
		}
	}
	return merged
}

// tenantWaves runs Kahn's algorithm over one tenant's claimed entities, edges from Reference
// fields pointing at another entity claimed in the same batch.
func tenantWaves(items []ClaimedPair) [][]ClaimedEntity {
	names := map[string]bool{}
	for _, it := range items {
		names[it.Claimed.EntityName] = true
	}
	claimedByName := map[string]ClaimedEntity{}
	deps := map[string]map[string]bool{}
	for _, it := range items {
		entityDeps := map[string]bool{}
		for _, f := range it.Def.Fields {
			if f.Kind != metadata.KindReference || f.RefEntity == nil {
				continue
			}
			r := *f.RefEntity
			if r != it.Claimed.EntityName && names[r] {
				entityDeps[r] = true
			}
		}
		deps[it.Claimed.EntityName] = entityDeps
		claimedByName[it.Claimed.EntityName] = it.Claimed
	}

	remaining := map[string]bool{}
	for name := range claimedByName {
		remaining[name] = true
	}
	var waves [][]ClaimedEntity
	for len(remaining) > 0 {
		var ready []string
		for name := range remaining {
			allClear := true
			for dep := range deps[name] {
				if remaining[dep] {
					allClear = false
					break
				}
			}
			if allClear {
				ready = append(ready, name)
			}
		}
		sort.Strings(ready) // deterministic order within a wave
		if len(ready) == 0 {
			// Cycle among everything left — no valid order, run them together as a last wave.
			var leftover []string
			for name := range remaining {
				leftover = append(leftover, name)
			}
			sort.Strings(leftover)
			wave := make([]ClaimedEntity, len(leftover))
			for i, n := range leftover {
				wave[i] = claimedByName[n]
			}
			waves = append(waves, wave)
			break
		}
		wave := make([]ClaimedEntity, len(ready))
		for i, n := range ready {
			wave[i] = claimedByName[n]
		}
		waves = append(waves, wave)
		for _, n := range ready {
			delete(remaining, n)
		}
	}
	return waves
}

// leaseStaleAfter: a 'running' row whose lease_heartbeat is older than this is reclaimed by
// ClaimDue as abandoned.
const leaseStaleAfter = 60 * time.Minute

// ClaimDue: FOR UPDATE SKIP LOCKED — many workers can call this concurrently against the same
// table and never claim the same row twice, without a separate distributed lock. Fresh
// (pending) work is tried before retries (status = 'failed' sorts last in the ORDER BY), and
// nothing past maxAttempts is claimable at all. entityNameFilter == "" claims across every
// entity (one global work queue); non-empty scopes the claim to just that entity.
func ClaimDue(ctx context.Context, pool *pgxpool.Pool, workerID, entityNameFilter string, maxAttempts int32, limit int64) ([]ClaimedEntity, error) {
	staleBefore := time.Now().Add(-leaseStaleAfter)
	var filter *string
	if entityNameFilter != "" {
		filter = &entityNameFilter
	}
	rows, err := pool.Query(ctx,
		`UPDATE metadata.reconciler_entity_deployments SET status = 'running', lease_worker = $1,
		 lease_heartbeat = now(), attempts = attempts + 1, updated_at = now()
		 WHERE (tenant_id, entity_name) IN (
		   SELECT tenant_id, entity_name FROM metadata.reconciler_entity_deployments
		   WHERE desired_version > COALESCE(applied_version, 0)
		         AND (status IN ('pending', 'failed')
		              OR (status = 'running' AND lease_heartbeat < $5))
		         AND attempts < $2 AND ($4::text IS NULL OR entity_name = $4)
		   ORDER BY (status = 'failed') ASC, priority_tier ASC, updated_at ASC
		   LIMIT $3 FOR UPDATE SKIP LOCKED
		 ) RETURNING tenant_id, entity_name, desired_version`,
		workerID, maxAttempts, limit, filter, staleBefore)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ClaimedEntity
	for rows.Next() {
		var c ClaimedEntity
		if err := rows.Scan(&c.TenantID, &c.EntityName, &c.DesiredVersion); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// EnqueueDeployment seeds or bumps one (tenant, entity)'s desired version — the single-tenant
// shape a CLI trigger or an admin action needs, without wave/cohort bookkeeping. No-op if
// desiredVersion isn't actually newer. Resets attempts/failure_class/last_error back to a clean
// pending state so a previously blocked row can be retried by bumping the version.
func EnqueueDeployment(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entityName string, desiredVersion int64) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO metadata.reconciler_entity_deployments (tenant_id, entity_name, desired_version)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (tenant_id, entity_name) DO UPDATE
		 SET desired_version = EXCLUDED.desired_version, status = 'pending', attempts = 0,
		     failure_class = NULL, last_error = NULL, updated_at = now()
		 WHERE metadata.reconciler_entity_deployments.desired_version < EXCLUDED.desired_version`,
		tenantID, entityName, desiredVersion)
	return err
}

// FailureClass is the three SQLSTATE buckets: retry automatically, or don't (data needs a human
// to fix, or the metadata/code itself is wrong).
type FailureClass int

const (
	FailureTransient FailureClass = iota
	FailureDataError
	FailureFatal
)

func (c FailureClass) String() string {
	switch c {
	case FailureDataError:
		return "data_error"
	case FailureFatal:
		return "fatal"
	default:
		return "transient"
	}
}

// ClassifyError walks the error chain for a *pgconn.PgError carrying a Postgres SQLSTATE —
// Reconcile wraps DB errors at several layers, so a plain errors.As on the top-level error
// alone might miss it if a caller wraps further; errors.As already walks the whole chain.
func ClassifyError(err error) FailureClass {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return FailureFatal
	}
	switch pgErr.Code {
	// serialization_failure, deadlock_detected, lock_not_available, too_many_connections
	case "40001", "40P01", "55P03", "53300":
		return FailureTransient
	// foreign_key_violation, unique_violation, check_violation, invalid_text_representation,
	// numeric_value_out_of_range
	case "23503", "23505", "23514", "22P02", "22003":
		return FailureDataError
	default:
		return FailureFatal
	}
}

func RecordSuccess(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entityName string, appliedVersion int64) error {
	_, err := pool.Exec(ctx,
		`UPDATE metadata.reconciler_entity_deployments SET status = 'done', applied_version = $3,
		 failure_class = NULL, last_error = NULL, lease_worker = NULL, updated_at = now()
		 WHERE tenant_id = $1 AND entity_name = $2`,
		tenantID, entityName, appliedVersion)
	return err
}

// RecordFailure: Transient goes back to 'failed' (still claimable, up to maxAttempts).
// DataError/Fatal go to 'blocked' — deliberately outside ClaimDue's status IN ('pending',
// 'failed') filter, so a human has to move it forward, not an automatic retry loop hammering
// the same broken data or metadata.
func RecordFailure(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entityName string, recErr error) (FailureClass, error) {
	class := ClassifyError(recErr)
	status := "blocked"
	if class == FailureTransient {
		status = "failed"
	}
	_, err := pool.Exec(ctx,
		`UPDATE metadata.reconciler_entity_deployments SET status = $3, failure_class = $4, last_error = $5,
		 lease_worker = NULL, updated_at = now() WHERE tenant_id = $1 AND entity_name = $2`,
		tenantID, entityName, status, class.String(), fmt.Sprintf("%v", recErr))
	return class, err
}

// DeploymentStatus is one metadata.reconciler_entity_deployments row, read-only — the
// counterpart to ClaimDue/EnqueueDeployment/RecordSuccess/RecordFailure for a caller that only
// needs to know where a (tenant, entity) deployment currently stands.
type DeploymentStatus struct {
	// Status: "pending" / "running" / "done" / "failed" / "blocked" — the column's own values,
	// not remapped into a Go enum here.
	Status         string
	DesiredVersion int64
	AppliedVersion *int64
	FailureClass   *string
	LastError      *string
}

// Ready reports true once AppliedVersion has actually caught up to DesiredVersion —
// deliberately not just Status == "done": a second publish can bump DesiredVersion again while
// the row still reads "done" from the *previous* one.
func (s DeploymentStatus) Ready() bool {
	return s.Status == "done" && s.AppliedVersion != nil && *s.AppliedVersion >= s.DesiredVersion
}

// GetDeploymentStatus returns (nil, nil) when no row exists at all — either the entity has
// never been enqueued, or migrated onto a dedicated table synchronously. Both cases mean
// "nothing to wait on," which the caller should treat the same as Ready() returning true.
func GetDeploymentStatus(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entityName string) (*DeploymentStatus, error) {
	var s DeploymentStatus
	err := pool.QueryRow(ctx,
		`SELECT status, desired_version, applied_version, failure_class, last_error
		 FROM metadata.reconciler_entity_deployments WHERE tenant_id = $1 AND entity_name = $2`,
		tenantID, entityName).Scan(&s.Status, &s.DesiredVersion, &s.AppliedVersion, &s.FailureClass, &s.LastError)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

type BatchOutcome struct {
	Entity ClaimedEntity
	Err    error
}

// RunClaimedBatch runs reconcileOne for every claimed entity, at most concurrencyLimit at a
// time, and in isolation: one entity's error is recorded (RecordFailure) and does not stop or
// cancel any other entity's work. Success records RecordSuccess with the entity's own
// DesiredVersion as the newly-applied one. Result order matches completion order, not input
// order (the same as the Rust original's buffer_unordered).
func RunClaimedBatch(ctx context.Context, pool *pgxpool.Pool, claimed []ClaimedEntity, concurrencyLimit int, reconcileOne func(context.Context, ClaimedEntity) error) []BatchOutcome {
	if concurrencyLimit < 1 {
		concurrencyLimit = 1
	}
	work := make(chan ClaimedEntity)
	results := make(chan BatchOutcome)

	var wg sync.WaitGroup
	for i := 0; i < concurrencyLimit; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for entity := range work {
				err := reconcileOne(ctx, entity)
				if err == nil {
					_ = RecordSuccess(ctx, pool, entity.TenantID, entity.EntityName, entity.DesiredVersion)
				} else {
					_, _ = RecordFailure(ctx, pool, entity.TenantID, entity.EntityName, err)
				}
				results <- BatchOutcome{Entity: entity, Err: err}
			}
		}()
	}

	go func() {
		for _, e := range claimed {
			work <- e
		}
		close(work)
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	outcomes := make([]BatchOutcome, 0, len(claimed))
	for o := range results {
		outcomes = append(outcomes, o)
	}
	return outcomes
}

// WaveSize: canary(total, 0) is 1-2 tenants, then 5%, 25%, 100%. Each wave's slice is
// *cumulative* (includes every tenant from prior waves).
func WaveSize(totalTenants int, wave uint32) int {
	if totalTenants == 0 {
		return 0
	}
	canary := totalTenants
	if canary > 2 {
		canary = 2
	}
	var size int
	switch wave {
	case 0:
		size = canary
	case 1:
		size = maxInt(canary, int(math.Ceil(float64(totalTenants)*0.05)))
	case 2:
		size = maxInt(canary, int(math.Ceil(float64(totalTenants)*0.25)))
	default:
		size = totalTenants
	}
	if size > totalTenants {
		size = totalTenants
	}
	return size
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

type WaveDecisionKind int

const (
	WaveAdvanced WaveDecisionKind = iota
	WaveHalted
)

type WaveDecision struct {
	Kind             WaveDecisionKind
	TenantsInWave    int    // WaveAdvanced
	ErrorRatePercent uint32 // WaveHalted
}

// AdvanceWave checks the *previous* wave's outcome (skipped for wave 0, which has nothing prior
// to judge) before advancing desired_version to packVersion for tenantIDs[:WaveSize(wave)].
// tenantIDs order is the caller's canary selection — put the tenants you trust least to reveal a
// bad pack first.
func AdvanceWave(ctx context.Context, pool *pgxpool.Pool, entityName string, packVersion int64, tenantIDs []uuid.UUID, wave uint32, maxErrorRatePercent uint32) (WaveDecision, error) {
	if wave > 0 {
		priorSize := WaveSize(len(tenantIDs), wave-1)
		priorCohort := tenantIDs[:priorSize]
		errorRate, err := cohortErrorRatePercent(ctx, pool, entityName, packVersion, priorCohort)
		if err != nil {
			return WaveDecision{}, err
		}
		if errorRate > maxErrorRatePercent {
			return WaveDecision{Kind: WaveHalted, ErrorRatePercent: errorRate}, nil
		}
	}

	size := WaveSize(len(tenantIDs), wave)
	cohort := tenantIDs[:size]
	_, err := pool.Exec(ctx,
		`INSERT INTO metadata.reconciler_entity_deployments (tenant_id, entity_name, desired_version)
		 SELECT unnest($1::uuid[]), $2, $3
		 ON CONFLICT (tenant_id, entity_name) DO UPDATE
		 SET desired_version = EXCLUDED.desired_version, status = 'pending', attempts = 0,
		     failure_class = NULL, last_error = NULL, updated_at = now()
		 WHERE metadata.reconciler_entity_deployments.desired_version < EXCLUDED.desired_version`,
		cohort, entityName, packVersion)
	if err != nil {
		return WaveDecision{}, err
	}

	return WaveDecision{Kind: WaveAdvanced, TenantsInWave: size}, nil
}

func cohortErrorRatePercent(ctx context.Context, pool *pgxpool.Pool, entityName string, packVersion int64, cohort []uuid.UUID) (uint32, error) {
	if len(cohort) == 0 {
		return 0, nil
	}
	var total, blocked int64
	err := pool.QueryRow(ctx,
		`SELECT count(*), count(*) FILTER (WHERE status = 'blocked')
		 FROM metadata.reconciler_entity_deployments
		 WHERE entity_name = $1 AND desired_version = $2 AND tenant_id = ANY($3)`,
		entityName, packVersion, cohort).Scan(&total, &blocked)
	if err != nil {
		return 0, err
	}
	if total == 0 {
		return 0, nil
	}
	return uint32(math.Round(float64(blocked) / float64(total) * 100.0)), nil
}
