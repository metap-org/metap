// Picks which SecretStore impl a binary should build. Centralized here (rather than each binary
// hand-rolling the same branch) so four independent copies can't drift.
package control

import (
	"context"
	"fmt"

	"github.com/metap-org/metap-go/infra"
	"github.com/metap-org/metap-go/runtime"
)

// SecretStoreConfig: just the env-derived fields BuildSecretStore actually reads — split out
// from infra.AppConfig so a binary with no Postgres/RabbitMQ pool of its own can build a
// SecretStore without also satisfying that config's unrelated requirements.
type SecretStoreConfig struct {
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

func SecretStoreConfigFromAppConfig(config *infra.AppConfig) SecretStoreConfig {
	return SecretStoreConfig{
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
func SecretStoreConfigFromEnv() SecretStoreConfig {
	vaultAddr, _ := runtime.Optional("VAULT_ADDR")
	vaultToken, _ := runtime.Optional("VAULT_TOKEN")
	vaultRoleID, _ := runtime.Optional("VAULT_ROLE_ID")
	vaultSecretID, _ := runtime.Optional("VAULT_SECRET_ID")
	vaultApproleMount, _ := runtime.Optional("VAULT_APPROLE_MOUNT")
	awsRegion, _ := runtime.Optional("AWS_SECRETS_REGION")
	awsAccessKey, _ := runtime.Optional("AWS_SECRETS_ACCESS_KEY")
	awsSecretKey, _ := runtime.Optional("AWS_SECRETS_SECRET_KEY")
	awsEndpointURL, _ := runtime.Optional("AWS_SECRETS_ENDPOINT_URL")
	gcpProjectID, _ := runtime.Optional("GCP_SECRETS_PROJECT_ID")
	return SecretStoreConfig{
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
func BuildSecretStore(ctx context.Context, config SecretStoreConfig) (SecretStore, error) {
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

	return EnvStore{}, nil
}
