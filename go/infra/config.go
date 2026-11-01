// Package infra holds the lowest-level plumbing every metap-go binary needs — ported from
// metap's Rust metap-infra crate: config loading, the Postgres pool, the outbox write-side, and
// the EventBus (eventbus subpackage).
package infra

import (
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
	"github.com/metap-org/metap-go/runtime"
)

type NodeEnv int

const (
	NodeEnvDevelopment NodeEnv = iota
	NodeEnvTest
	NodeEnvProduction
)

// AppConfig mirrors metap-infra's Rust AppConfig — same env var names, same defaults, same
// required-vs-optional fields.
type AppConfig struct {
	NodeEnv               NodeEnv
	Host                  string
	Port                  int
	DatabaseURL           string
	OutboxDatabaseURL     string // "" means: use DatabaseURL
	RabbitMQURL           string
	CORSOrigins           []string
	AuthJWTPublicKeyPath  string
	AuthJWTPrivateKeyPath string
	StaticDir             string

	VaultAddr         string
	VaultToken        string
	VaultRoleID       string
	VaultSecretID     string
	VaultApproleMount string

	AWSSecretsRegion      string
	AWSSecretsAccessKey   string
	AWSSecretsSecretKey   string
	AWSSecretsEndpointURL string
	GCPSecretsProjectID   string

	AuthContextEntity          string
	AuthContextCacheTTLSeconds uint64

	PolicyCacheRedisURL   string
	PolicyCacheTTLSeconds uint64

	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPPassword string
	SMTPFrom     string
}

// OutboxDatabaseURLOrDefault is the URL a worker's outbox/event-publishing DB connection should
// use — OutboxDatabaseURL if set, otherwise DatabaseURL.
func (c *AppConfig) OutboxDatabaseURLOrDefault() string {
	if c.OutboxDatabaseURL != "" {
		return c.OutboxDatabaseURL
	}
	return c.DatabaseURL
}

func isURLLike(s string) bool {
	return strings.Contains(s, "://") && strings.TrimSpace(s) != ""
}

// LoadConfig loads .env (if present) then reads every env var, same required-vs-optional set the
// Rust version enforces.
func LoadConfig() (*AppConfig, error) {
	_ = godotenv.Load()

	nodeEnv := NodeEnvDevelopment
	switch os.Getenv("NODE_ENV") {
	case "test":
		nodeEnv = NodeEnvTest
	case "production":
		nodeEnv = NodeEnvProduction
	}

	hostStr := os.Getenv("HOST")
	if hostStr == "" {
		hostStr = "0.0.0.0"
	}

	port := 3000
	if p, err := strconv.Atoi(os.Getenv("PORT")); err == nil && p > 0 {
		port = p
	}

	databaseURL, ok := os.LookupEnv("DATABASE_URL")
	if !ok {
		return nil, errors.New("DATABASE_URL is required")
	}
	if !isURLLike(databaseURL) {
		return nil, errors.New("DATABASE_URL must be a valid URL")
	}

	outboxDatabaseURL := ""
	if v := os.Getenv("OUTBOX_DATABASE_URL"); v != "" {
		if !isURLLike(v) {
			return nil, errors.New("OUTBOX_DATABASE_URL must be a valid URL")
		}
		outboxDatabaseURL = v
	}

	rabbitmqURL, ok := os.LookupEnv("RABBITMQ_URL")
	if !ok {
		return nil, errors.New("RABBITMQ_URL is required")
	}
	if !isURLLike(rabbitmqURL) {
		return nil, errors.New("RABBITMQ_URL must be a valid URL")
	}

	var corsOrigins []string
	if v := os.Getenv("CORS_ORIGINS"); v != "" {
		for _, o := range strings.Split(v, ",") {
			if o != "" {
				corsOrigins = append(corsOrigins, o)
			}
		}
	}

	authJWTPublicKeyPath := os.Getenv("AUTH_JWT_PUBLIC_KEY_PATH")
	if authJWTPublicKeyPath == "" {
		return nil, errors.New("AUTH_JWT_PUBLIC_KEY_PATH is required")
	}
	authJWTPrivateKeyPath := os.Getenv("AUTH_JWT_PRIVATE_KEY_PATH")
	if authJWTPrivateKeyPath == "" {
		return nil, errors.New("AUTH_JWT_PRIVATE_KEY_PATH is required")
	}

	staticDir, _ := runtime.Optional("STATIC_DIR")
	vaultAddr, _ := runtime.Optional("VAULT_ADDR")
	vaultToken, _ := runtime.Optional("VAULT_TOKEN")
	vaultRoleID, _ := runtime.Optional("VAULT_ROLE_ID")
	vaultSecretID, _ := runtime.Optional("VAULT_SECRET_ID")
	vaultApproleMount, _ := runtime.Optional("VAULT_APPROLE_MOUNT")

	awsSecretsRegion, _ := runtime.Optional("AWS_SECRETS_REGION")
	awsSecretsAccessKey, _ := runtime.Optional("AWS_SECRETS_ACCESS_KEY")
	awsSecretsSecretKey, _ := runtime.Optional("AWS_SECRETS_SECRET_KEY")
	awsSecretsEndpointURL, _ := runtime.Optional("AWS_SECRETS_ENDPOINT_URL")
	gcpSecretsProjectID, _ := runtime.Optional("GCP_SECRETS_PROJECT_ID")

	authContextEntity, _ := runtime.Optional("AUTH_CONTEXT_ENTITY")
	authContextCacheTTLSeconds := uint64(runtime.EnvOr("AUTH_CONTEXT_CACHE_TTL_SECONDS", 30))

	policyCacheRedisURL, _ := runtime.Optional("POLICY_CACHE_REDIS_URL")
	policyCacheTTLSeconds := uint64(runtime.EnvOr("POLICY_CACHE_TTL_SECONDS", 30))

	smtpHost, _ := runtime.Optional("SMTP_HOST")
	smtpPort := runtime.EnvOr("SMTP_PORT", 1025)
	smtpUser, _ := runtime.Optional("SMTP_USER")
	smtpPassword, _ := runtime.Optional("SMTP_PASSWORD")
	smtpFrom, _ := runtime.Optional("SMTP_FROM")

	return &AppConfig{
		NodeEnv: nodeEnv, Host: hostStr, Port: port,
		DatabaseURL: databaseURL, OutboxDatabaseURL: outboxDatabaseURL, RabbitMQURL: rabbitmqURL,
		CORSOrigins:          corsOrigins,
		AuthJWTPublicKeyPath: authJWTPublicKeyPath, AuthJWTPrivateKeyPath: authJWTPrivateKeyPath,
		StaticDir: staticDir,
		VaultAddr: vaultAddr, VaultToken: vaultToken, VaultRoleID: vaultRoleID,
		VaultSecretID: vaultSecretID, VaultApproleMount: vaultApproleMount,
		AWSSecretsRegion: awsSecretsRegion, AWSSecretsAccessKey: awsSecretsAccessKey,
		AWSSecretsSecretKey: awsSecretsSecretKey, AWSSecretsEndpointURL: awsSecretsEndpointURL,
		GCPSecretsProjectID: gcpSecretsProjectID,
		AuthContextEntity:   authContextEntity, AuthContextCacheTTLSeconds: authContextCacheTTLSeconds,
		PolicyCacheRedisURL: policyCacheRedisURL, PolicyCacheTTLSeconds: policyCacheTTLSeconds,
		SMTPHost: smtpHost, SMTPPort: smtpPort, SMTPUser: smtpUser, SMTPPassword: smtpPassword, SMTPFrom: smtpFrom,
	}, nil
}
