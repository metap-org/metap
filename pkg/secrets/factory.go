// Picks which SecretStore impl a binary should build. Centralized here (rather than each binary
// hand-rolling the same branch) so four independent copies can't drift.
// Package secrets builds the control.SecretStore a deployment is configured for — GCP Secret
// Manager, AWS Secrets Manager, Vault, or (default) control.EnvStore. The cloud SDKs live here,
// not in package control, so importing control (and everything built on it: crud, httpserver,
// grpcapi) no longer links three cloud SDKs into every binary; only a binary that calls Build
// does.
package secrets

import (
	"context"
	"fmt"

	"github.com/metap-org/metap/pkg/control"

	"github.com/metap-org/metap/pkg/infra"
	"github.com/metap-org/metap/pkg/kit/env"
)

// SecretStoreConfig: just the env-derived fields BuildSecretStore actually reads — split out
// from infra.AppConfig so a binary with no Postgres/RabbitMQ pool of its own can build a
// SecretStore without also satisfying that config's unrelated requirements.
type Config struct {
	VaultAddr         string
	VaultToken        string
	VaultRoleID       string
	VaultSecretID     string
	VaultApproleMount string

	AWSSecretsRegion      string
	AWSSecretsAccessKey   string
	AWSSecretsSecretKey   string
	AWSSecretsEndpointURL string

	GCPSecretsProjectID string
}

func ConfigFromAppConfig(config *infra.AppConfig) Config {
	return Config{
		VaultAddr:             config.VaultAddr,
		VaultToken:            config.VaultToken,
		VaultRoleID:           config.VaultRoleID,
		VaultSecretID:         config.VaultSecretID,
		VaultApproleMount:     config.VaultApproleMount,
		AWSSecretsRegion:      config.AWSSecretsRegion,
		AWSSecretsAccessKey:   config.AWSSecretsAccessKey,
		AWSSecretsSecretKey:   config.AWSSecretsSecretKey,
		AWSSecretsEndpointURL: config.AWSSecretsEndpointURL,
		GCPSecretsProjectID:   config.GCPSecretsProjectID,
	}
}

// SecretStoreConfigFromEnv reads the same env vars infra.LoadConfig does, directly — for a
// binary that has no AppConfig/Postgres pool at all. Same names, so an operator's existing
// VAULT_ADDR/AWS_SECRETS_REGION/... deployment config works unchanged regardless of which binary
// reads it.
func ConfigFromEnv() Config {
	vaultAddr, _ := env.Lookup("VAULT_ADDR")
	vaultToken, _ := env.Lookup("VAULT_TOKEN")
	vaultRoleID, _ := env.Lookup("VAULT_ROLE_ID")
	vaultSecretID, _ := env.Lookup("VAULT_SECRET_ID")
	vaultApproleMount, _ := env.Lookup("VAULT_APPROLE_MOUNT")
	awsRegion, _ := env.Lookup("AWS_SECRETS_REGION")
	awsAccessKey, _ := env.Lookup("AWS_SECRETS_ACCESS_KEY")
	awsSecretKey, _ := env.Lookup("AWS_SECRETS_SECRET_KEY")
	awsEndpointURL, _ := env.Lookup("AWS_SECRETS_ENDPOINT_URL")
	gcpProjectID, _ := env.Lookup("GCP_SECRETS_PROJECT_ID")
	return Config{
		VaultAddr: vaultAddr, VaultToken: vaultToken, VaultRoleID: vaultRoleID,
		VaultSecretID: vaultSecretID, VaultApproleMount: vaultApproleMount,
		AWSSecretsRegion: awsRegion, AWSSecretsAccessKey: awsAccessKey,
		AWSSecretsSecretKey: awsSecretKey, AWSSecretsEndpointURL: awsEndpointURL,
		GCPSecretsProjectID: gcpProjectID,
	}
}

// BuildSecretStore's precedence when more than one backend's env vars are somehow set at once:
// GCP, then AWS, then Vault, then the EnvStore fallback — an arbitrary but fixed order,
// deterministic and documented rather than left to if/match arm evaluation order.
func Build(ctx context.Context, config Config) (control.SecretStore, error) {
	if config.GCPSecretsProjectID != "" {
		return NewGcpSecretManagerStore(ctx, config.GCPSecretsProjectID)
	}

	if config.AWSSecretsRegion != "" {
		if config.AWSSecretsAccessKey == "" {
			return nil, fmt.Errorf("AWS_SECRETS_REGION is set but AWS_SECRETS_ACCESS_KEY is not")
		}
		if config.AWSSecretsSecretKey == "" {
			return nil, fmt.Errorf("AWS_SECRETS_REGION is set but AWS_SECRETS_SECRET_KEY is not")
		}
		return NewAwsSecretsManagerStore(ctx, AwsSecretsManagerStoreConfig{
			Region: config.AWSSecretsRegion, AccessKey: config.AWSSecretsAccessKey,
			SecretKey: config.AWSSecretsSecretKey, EndpointURL: config.AWSSecretsEndpointURL,
		})
	}

	if config.VaultAddr != "" {
		switch {
		case config.VaultRoleID != "" && config.VaultSecretID != "":
			mount := config.VaultApproleMount
			if mount == "" {
				mount = defaultApproleMount
			}
			return NewVaultStoreWithAppRole(ctx, config.VaultAddr, mount, config.VaultRoleID, config.VaultSecretID)
		case config.VaultToken != "":
			return NewVaultStore(config.VaultAddr, config.VaultToken)
		default:
			return nil, fmt.Errorf("VAULT_ADDR is set but neither VAULT_TOKEN nor VAULT_ROLE_ID+VAULT_SECRET_ID is")
		}
	}

	return control.EnvStore{}, nil
}
