// cron-scheduler is the dispatch loop for the cron package's job definitions — ported from
// metap's Rust metap-cron-scheduler binary. Runs 3 loops concurrently (ticker, executor, trigger
// listener) plus the OAuth2 expired-token cleanup sweep.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/metap-org/metap-go/control"
	"github.com/metap-org/metap-go/cronscheduler"
	"github.com/metap-org/metap-go/crud"
	"github.com/metap-org/metap-go/grpcapi"
	"github.com/metap-org/metap-go/infra"
	"github.com/metap-org/metap-go/infra/eventbus"
	"github.com/metap-org/metap-go/runtime"
)

func main() {
	infra.InitTracing()
	config, err := infra.LoadConfig()
	if err != nil {
		slog.Error("loading config", "error", err)
		os.Exit(1)
	}

	ctx, cancel := signalContext()
	defer cancel()

	tickMs := time.Duration(runtime.EnvOr("CRON_TICK_MS", 5000)) * time.Millisecond
	batchSize := int64(runtime.EnvOr("CRON_BATCH_SIZE", 50))
	targetBaseURL, ok := runtime.Optional("CRON_TARGET_BASE_URL")
	if !ok {
		targetBaseURL = fmt.Sprintf("http://localhost:%d", config.Port)
	}
	httpClient := runtime.DefaultHTTPClient()

	// Logs into the target's own POST /auth/login and keeps the token fresh in the background —
	// replaces a static, hand-minted-once JWT. Unlike graphql-gateway, this process runs other
	// job types (webhook, email) that don't need this credential at all, so a missing/failing
	// login degrades to an empty token (workflow_transition/bulk_query_action jobs then fail,
	// logged per-job) rather than refusing to start.
	loginURL, ok := runtime.Optional("CRON_LOGIN_URL")
	if !ok {
		loginURL = strings.TrimSuffix(targetBaseURL, "/") + "/auth/login"
	}
	serviceEmail, hasEmail := runtime.Optional("CRON_SERVICE_EMAIL")
	servicePassword, hasPassword := runtime.Optional("CRON_SERVICE_PASSWORD")

	var serviceToken *runtime.ServiceTokenSource
	switch {
	case hasEmail && hasPassword:
		st, err := runtime.StartServiceTokenSource(ctx, httpClient, loginURL, serviceEmail, servicePassword)
		if err != nil {
			slog.Warn("failed to log in with CRON_SERVICE_EMAIL/CRON_SERVICE_PASSWORD — workflow_transition/bulk_query_action jobs will fail until this is fixed; webhook jobs are unaffected", "error", err)
			serviceToken = runtime.NewStaticServiceTokenSource("")
		} else {
			serviceToken = st
		}
	default:
		slog.Warn("CRON_SERVICE_EMAIL/CRON_SERVICE_PASSWORD are unset — workflow_transition/bulk_query_action jobs will fail; webhook jobs are unaffected. Provision one with `dev-tools create-user`/`dev-tools seed-admin` and grant it whatever role the jobs it runs need.")
		serviceToken = runtime.NewStaticServiceTokenSource("")
	}

	// workflow_transition/bulk_query_action jobs' entity CRUD backend, over gRPC now that
	// metap-http no longer serves a REST /api/:entity* surface for these to call. Same
	// degrade-gracefully shape as service_token above — a missing/unreachable gRPC endpoint only
	// fails these two job types, everything else is unaffected.
	grpcAddr, hasGRPC := runtime.Optional("CRON_TARGET_GRPC_ADDR")
	var targetGRPCBackend crud.RecordBackend
	if hasGRPC {
		backend, err := grpcapi.ConnectGrpcBackend(grpcAddr, serviceToken)
		if err != nil {
			slog.Warn("failed to connect to CRON_TARGET_GRPC_ADDR — workflow_transition/bulk_query_action jobs will fail until this is fixed; webhook/email jobs are unaffected",
				"error", err, "addr", grpcAddr)
		} else {
			targetGRPCBackend = backend
		}
	} else {
		slog.Warn("CRON_TARGET_GRPC_ADDR is unset — workflow_transition/bulk_query_action jobs will fail; webhook/email jobs are unaffected. Point it at the owning app's gRPC port (GRPC_PORT).")
	}

	slog.Info("connecting to postgres...")
	pool, err := pgxpool.New(ctx, config.OutboxDatabaseURLOrDefault())
	if err != nil {
		slog.Error("connecting to postgres", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Same backend picker every other binary uses, so a deployment configures Vault/AWS/GCP once
	// and every process agrees on where a tenant's credentials live. A backend that fails to
	// construct degrades to nil rather than refusing to boot — most job types need no credential
	// at all, and only the jobs that asked for one should fail.
	secretStore, err := control.BuildSecretStore(ctx, control.SecretStoreConfigFromAppConfig(config))
	if err != nil {
		slog.Warn("no secret backend available — webhook jobs requesting their tenant's stored credential will fail; every other job type is unaffected", "error", err)
		secretStore = nil
	}

	executorConfig := &cronscheduler.ExecutorConfig{
		TargetBaseURL:     targetBaseURL,
		TargetGRPCBackend: targetGRPCBackend,
		ServiceToken:      serviceToken,
		SMTP: cronscheduler.SmtpConfig{
			Host: config.SMTPHost, Port: config.SMTPPort, User: config.SMTPUser,
			Password: config.SMTPPassword, From: config.SMTPFrom,
		},
		Webhook: cronscheduler.WebhookPolicyFromEnv(),
		Secrets: secretStore,
	}
	tickerConfig := cronscheduler.TickerConfig{Interval: tickMs, BatchSize: batchSize}

	slog.Info("ready, ticking and listening", "tick_ms", tickMs, "batch_size", batchSize, "target_base_url", targetBaseURL)

	executorConnect := func() (eventbus.EventBus, error) { return eventbus.ConnectRabbitEventBus(config.RabbitMQURL) }
	triggerConnect := func() (eventbus.EventBus, error) { return eventbus.ConnectRabbitEventBus(config.RabbitMQURL) }

	// OAUTH_CLEANUP_INTERVAL_SECONDS=0 turns the OAuth2 expired-token sweep off. Its pool is the
	// main DATABASE_URL one (where /oauth/token writes), which only differs from pool above when
	// OUTBOX_DATABASE_URL points the outbox somewhere else.
	oauthCleanupSecs := uint64(runtime.EnvOr("OAUTH_CLEANUP_INTERVAL_SECONDS", 3600))
	var oauthPool *pgxpool.Pool
	if oauthCleanupSecs != 0 {
		if config.OutboxDatabaseURLOrDefault() == config.DatabaseURL {
			oauthPool = pool
		} else {
			oauthPool, err = pgxpool.New(ctx, config.DatabaseURL)
			if err != nil {
				slog.Error("connecting to postgres for oauth cleanup", "error", err)
				os.Exit(1)
			}
		}
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return cronscheduler.RunTicker(gctx, pool, executorConfig, tickerConfig) })
	g.Go(func() error { return cronscheduler.RunExecutor(gctx, executorConnect, pool, executorConfig) })
	g.Go(func() error { return cronscheduler.RunTriggerListener(gctx, triggerConnect, pool, executorConfig) })
	if oauthPool != nil {
		g.Go(func() error {
			cronscheduler.RunOAuthCleanup(gctx, oauthPool, time.Duration(oauthCleanupSecs)*time.Second)
			return nil
		})
	}

	runErr := g.Wait()

	if oauthPool != nil && oauthPool != pool {
		oauthPool.Close()
	}

	if runErr != nil {
		slog.Error("cron-scheduler exited with an error", "error", runErr)
		os.Exit(1)
	}
}

func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		runtime.WaitForShutdownSignal()
		cancel()
	}()
	return ctx, cancel
}
