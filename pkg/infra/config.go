// Package infra holds the lowest-level plumbing every metap-go binary needs.
package infra

import (
	"errors"
	"strings"

	"github.com/joho/godotenv"
	"github.com/metap-org/metap/pkg/kit/env"
)

type NodeEnv int

const (
	NodeEnvDevelopment NodeEnv = iota
	NodeEnvTest
	NodeEnvProduction
)

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
	// JWKSKeysDir (JWKS_KEYS_DIR): this service is the token issuer — it signs with the key dir's
	// signing key and publishes /.well-known/jwks.json. JWKSURL (JWKS_URL): this service verifies
	// against that issuer. With either set, AuthJWT*KeyPath become optional (a public key path
	// then only keeps legacy RS256 tokens verifying during a cutover).
	JWKSKeysDir string
	JWKSURL     string
	StaticDir   string

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

// LoadConfig loads .env (if present) then reads every env var.
func LoadConfig() (*AppConfig, error) {
	_ = godotenv.Load()

	nodeEnv := NodeEnvDevelopment
	switch env.String("NODE_ENV", "") {
	case "test":
		nodeEnv = NodeEnvTest
	case "production":
		nodeEnv = NodeEnvProduction
	}

	hostStr := env.String("HOST", "0.0.0.0")

	port := env.Int("PORT", 3000)
	if port <= 0 {
		port = 3000
	}

	databaseURL, err := env.Require("DATABASE_URL")
	if err != nil {
		return nil, err
	}
	if !isURLLike(databaseURL) {
		return nil, errors.New("DATABASE_URL must be a valid URL")
	}

	outboxDatabaseURL := ""
	if v, ok := env.Lookup("OUTBOX_DATABASE_URL"); ok {
		if !isURLLike(v) {
			return nil, errors.New("OUTBOX_DATABASE_URL must be a valid URL")
		}
		outboxDatabaseURL = v
	}

	rabbitmqURL, err := env.Require("RABBITMQ_URL")
	if err != nil {
		return nil, err
	}
	if !isURLLike(rabbitmqURL) {
		return nil, errors.New("RABBITMQ_URL must be a valid URL")
	}

	corsOrigins := env.List("CORS_ORIGINS")

	jwksKeysDir := env.String("JWKS_KEYS_DIR", "")
	jwksURL := env.String("JWKS_URL", "")
	authJWTPublicKeyPath := env.String("AUTH_JWT_PUBLIC_KEY_PATH", "")
	authJWTPrivateKeyPath := env.String("AUTH_JWT_PRIVATE_KEY_PATH", "")
	if jwksKeysDir == "" && jwksURL == "" {
		// Legacy static-RSA mode: every service signs and verifies with the shared keypair.
		if authJWTPublicKeyPath == "" {
			return nil, errors.New("AUTH_JWT_PUBLIC_KEY_PATH is required (or configure JWKS_KEYS_DIR / JWKS_URL)")
		}
		if authJWTPrivateKeyPath == "" {
			return nil, errors.New("AUTH_JWT_PRIVATE_KEY_PATH is required (or configure JWKS_KEYS_DIR / JWKS_URL)")
		}
	}

	staticDir, _ := env.Lookup("STATIC_DIR")
	vaultAddr, _ := env.Lookup("VAULT_ADDR")
	vaultToken, _ := env.Lookup("VAULT_TOKEN")
	vaultRoleID, _ := env.Lookup("VAULT_ROLE_ID")
	vaultSecretID, _ := env.Lookup("VAULT_SECRET_ID")
	vaultApproleMount, _ := env.Lookup("VAULT_APPROLE_MOUNT")

	awsSecretsRegion, _ := env.Lookup("AWS_SECRETS_REGION")
	awsSecretsAccessKey, _ := env.Lookup("AWS_SECRETS_ACCESS_KEY")
	awsSecretsSecretKey, _ := env.Lookup("AWS_SECRETS_SECRET_KEY")
	awsSecretsEndpointURL, _ := env.Lookup("AWS_SECRETS_ENDPOINT_URL")
	gcpSecretsProjectID, _ := env.Lookup("GCP_SECRETS_PROJECT_ID")

	authContextEntity, _ := env.Lookup("AUTH_CONTEXT_ENTITY")
	authContextCacheTTLSeconds := uint64(env.Int("AUTH_CONTEXT_CACHE_TTL_SECONDS", 30))

	policyCacheRedisURL, _ := env.Lookup("POLICY_CACHE_REDIS_URL")
	policyCacheTTLSeconds := uint64(env.Int("POLICY_CACHE_TTL_SECONDS", 30))

	smtpHost, _ := env.Lookup("SMTP_HOST")
	smtpPort := env.Int("SMTP_PORT", 1025)
	smtpUser, _ := env.Lookup("SMTP_USER")
	smtpPassword, _ := env.Lookup("SMTP_PASSWORD")
	smtpFrom, _ := env.Lookup("SMTP_FROM")

	return &AppConfig{
		NodeEnv: nodeEnv, Host: hostStr, Port: port,
		DatabaseURL: databaseURL, OutboxDatabaseURL: outboxDatabaseURL, RabbitMQURL: rabbitmqURL,
		CORSOrigins:          corsOrigins,
		AuthJWTPublicKeyPath: authJWTPublicKeyPath, AuthJWTPrivateKeyPath: authJWTPrivateKeyPath,
		JWKSKeysDir: jwksKeysDir, JWKSURL: jwksURL,
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
