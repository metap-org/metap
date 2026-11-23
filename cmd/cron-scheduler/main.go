// cron-scheduler is the dispatch loop for the cron package's job definitions. Runs 3 loops
// concurrently (ticker, executor, trigger listener) plus the OAuth2 expired-token cleanup sweep and
// audit partition maintenance.
package main

import (
	"context"
	"fmt"
	"github.com/metap-org/metap/pkg/kit/httpx"
	"github.com/metap-org/metap/pkg/kit/shutdown"
	"github.com/metap-org/metap/pkg/recordclient"
	"github.com/metap-org/metap/pkg/secrets"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/jobs/scheduler"
	"github.com/metap-org/metap/pkg/kit/env"
	"github.com/metap-org/metap/pkg/kit/standard"
	"github.com/metap-org/metap/pkg/messaging/eventbus"
	"github.com/metap-org/metap/pkg/secrets/backend"
)

func main() {
	config.InitLogging()
	config, err := config.LoadApp()
	if err != nil {
		slog.Error("loading config", standard.LogError, err)
		os.Exit(1)
	}

	ctx, cancel := signalContext()
	defer cancel()

	tickMs := time.Duration(env.Int("CRON_TICK_MS", 5000)) * time.Millisecond
	batchSize := int64(env.Int("CRON_BATCH_SIZE", 50))
	targetBaseURL, ok := env.Lookup("CRON_TARGET_BASE_URL")
	if !ok {
		targetBaseURL = fmt.Sprintf("http://localhost:%d", config.Port)
	}
	httpClient := httpx.DefaultClient()

	// Logs into the target's own POST /auth/login and keeps the token fresh in the background —
	// replaces a static, hand-minted-once JWT. Unlike graphql-gateway, this process runs other
	// job types (webhook, email) that don't need this credential at all, so a missing/failing
	// login degrades to an empty token (workflow_transition/bulk_query_action jobs then fail,
	// logged per-job) rather than refusing to start.
	loginURL, ok := env.Lookup("CRON_LOGIN_URL")
	if !ok {
		loginURL = strings.TrimSuffix(targetBaseURL, "/") + "/auth/login"
	}
	serviceEmail, hasEmail := env.Lookup("CRON_SERVICE_EMAIL")
	servicePassword, hasPassword := env.Lookup("CRON_SERVICE_PASSWORD")

	var serviceToken *httpx.ServiceTokenSource
	switch {
	case hasEmail && hasPassword:
		st, err := httpx.StartServiceTokenSource(ctx, httpClient, loginURL, serviceEmail, servicePassword)
		if err != nil {
			slog.Warn("failed to log in with CRON_SERVICE_EMAIL/CRON_SERVICE_PASSWORD — workflow_transition/bulk_query_action jobs will fail until this is fixed; webhook jobs are unaffected", standard.LogError, err)
			serviceToken = httpx.NewStaticServiceTokenSource("")
		} else {
			serviceToken = st
		}
	default:
		slog.Warn("CRON_SERVICE_EMAIL/CRON_SERVICE_PASSWORD are unset — workflow_transition/bulk_query_action jobs will fail; webhook jobs are unaffected. Provision one with `dev-tools create-user`/`dev-tools seed-admin` and grant it whatever role the jobs it runs need.")
		serviceToken = httpx.NewStaticServiceTokenSource("")
	}

	// workflow_transition/bulk_query_action jobs' entity CRUD backend: the owning app's REST API
	// (roadmap 116). A missing CRON_TARGET_URL only fails these two job types; everything else runs.
	var targetBackend crud.RecordBackend
	if targetURL, ok := env.Lookup("CRON_TARGET_URL"); ok && targetURL != "" {
		targetBackend = recordclient.New(targetURL, serviceToken)
	} else {
		slog.Warn("CRON_TARGET_URL is unset — workflow_transition/bulk_query_action jobs will fail; webhook/email jobs are unaffected. Point it at the owning app's HTTP base URL.")
	}

	slog.Info("connecting to postgres...")
	pool, err := pgxpool.New(ctx, config.OutboxDatabaseURLOrDefault())
	if err != nil {
		slog.Error("connecting to postgres", standard.LogError, err)
		os.Exit(1)
	}
	defer pool.Close()

	// Same backend picker every other binary uses, so a deployment configures Vault/AWS/GCP once
	// and every process agrees on where a tenant's credentials live. A backend that fails to
	// construct degrades to nil rather than refusing to boot — most job types need no credential
	// at all, and only the jobs that asked for one should fail.
	var secretStore secrets.Store
	secretManager, err := backend.NewManager(ctx, backend.ConfigFromAppConfig(config), pool)
	if err != nil {
		slog.Warn("no secret backend available — webhook jobs requesting their tenant's stored credential will fail; every other job type is unaffected", standard.LogError, err)
	} else {
		secretStore = secretManager
	}

	executorConfig := &scheduler.ExecutorConfig{
		TargetBaseURL: targetBaseURL,
		TargetBackend: targetBackend,
		ServiceToken:  serviceToken,
		SMTP: scheduler.SmtpConfig{
			Host: config.SMTPHost, Port: config.SMTPPort, User: config.SMTPUser,
			Password: config.SMTPPassword, From: config.SMTPFrom,
		},
		Webhook: scheduler.WebhookPolicyFromEnv(),
		Secrets: secretStore,
	}
	tickerConfig := scheduler.TickerConfig{Interval: tickMs, BatchSize: batchSize}

	slog.Info("ready, ticking and listening", "tick_ms", tickMs, "batch_size", batchSize, "target_base_url", targetBaseURL)

	executorConnect := func() (eventbus.EventBus, error) { return eventbus.ConnectRabbitEventBus(config.RabbitMQURL) }
	triggerConnect := func() (eventbus.EventBus, error) { return eventbus.ConnectRabbitEventBus(config.RabbitMQURL) }

	// OAUTH_CLEANUP_INTERVAL_SECONDS=0 turns the OAuth2 expired-token sweep off. Its pool is the
	// main DATABASE_URL one (where /oauth/token writes), which only differs from pool above when
	// OUTBOX_DATABASE_URL points the outbox somewhere else.
	oauthCleanupSecs := uint64(env.Int("OAUTH_CLEANUP_INTERVAL_SECONDS", 3600))
	var oauthPool *pgxpool.Pool
	if oauthCleanupSecs != 0 {
		if config.OutboxDatabaseURLOrDefault() == config.DatabaseURL {
			oauthPool = pool
		} else {
			oauthPool, err = pgxpool.New(ctx, config.DatabaseURL)
			if err != nil {
				slog.Error("connecting to postgres for oauth cleanup", standard.LogError, err)
				os.Exit(1)
			}
		}
	}

	// AUDIT_PARTITION_INTERVAL_SECONDS=0 turns audit partition maintenance off. It runs against
	// the main DATABASE_URL, where metadata.audit_trail_entries lives (audit.PostgresStore's pool).
	auditPartitionSecs := uint64(env.Int("AUDIT_PARTITION_INTERVAL_SECONDS", 86400))
	auditMonthsAhead := int(env.Int("AUDIT_PARTITION_MONTHS_AHEAD", 3))
	var auditPool *pgxpool.Pool
	if auditPartitionSecs != 0 {
		switch {
		case oauthPool != nil:
			auditPool = oauthPool
		case config.OutboxDatabaseURLOrDefault() == config.DatabaseURL:
			auditPool = pool
		default:
			auditPool, err = pgxpool.New(ctx, config.DatabaseURL)
			if err != nil {
				slog.Error("connecting to postgres for audit partition maintenance", standard.LogError, err)
				os.Exit(1)
			}
		}
	}

	// TENANT_PURGE_INTERVAL_SECONDS=0 turns the tenant deletion sweep off. The registry
	// (control.tenants) lives in the main database, like the audit trail.
	tenantPurgeSecs := uint64(env.Int("TENANT_PURGE_INTERVAL_SECONDS", 3600))

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return scheduler.RunTicker(gctx, pool, executorConfig, tickerConfig) })
	g.Go(func() error { return scheduler.RunExecutor(gctx, executorConnect, pool, executorConfig) })
	g.Go(func() error { return scheduler.RunTriggerListener(gctx, triggerConnect, pool, executorConfig) })
	if oauthPool != nil {
		g.Go(func() error {
			scheduler.RunOAuthCleanup(gctx, oauthPool, time.Duration(oauthCleanupSecs)*time.Second)
			return nil
		})
	}

	if auditPool != nil {
		g.Go(func() error {
			scheduler.RunAuditPartitionMaintenance(gctx, auditPool, time.Duration(auditPartitionSecs)*time.Second, auditMonthsAhead)
			return nil
		})
	}

	if tenantPurgeSecs != 0 {
		// Own short-lived pool: it must point at the main database whatever the outbox pool does.
		purgePool, err := pgxpool.New(ctx, config.DatabaseURL)
		if err != nil {
			slog.Error("connecting to postgres for the tenant deletion sweep", standard.LogError, err)
			os.Exit(1)
		}
		defer purgePool.Close()
		g.Go(func() error {
			scheduler.RunTenantPurge(gctx, purgePool, time.Duration(tenantPurgeSecs)*time.Second)
			return nil
		})
	}

	runErr := g.Wait()

	if auditPool != nil && auditPool != pool && auditPool != oauthPool {
		auditPool.Close()
	}

	if oauthPool != nil && oauthPool != pool {
		oauthPool.Close()
	}

	if runErr != nil {
		slog.Error("cron-scheduler exited with an error", standard.LogError, runErr)
		os.Exit(1)
	}
}

func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		shutdown.Wait()
		cancel()
	}()
	return ctx, cancel
}
