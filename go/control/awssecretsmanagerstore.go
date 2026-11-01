// SecretStore over AWS Secrets Manager. Same construction style storage.S3ObjectStore already
// established for this codebase's other AWS-SDK-backed store — explicit static credentials, not
// the SDK's default credential-provider chain, so a misconfigured environment fails loudly with
// a clear credential error instead of silently picking up whatever ambient IAM role happens to
// be present.
//
// The secret's SecretString value is expected to be JSON, {"dsn": "<connection-string>"} — the
// same shape VaultStore uses, so an operator has one mental model regardless of which
// SecretStore backend a tenant's dsn_secret_ref actually resolves against.
package control

import (
	"context"
	"encoding/json"
	"fmt"

	awsv2config "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

type AwsSecretsManagerStoreConfig struct {
	Region    string
	AccessKey string
	SecretKey string
	// EndpointURL: LocalStack or another AWS-API-compatible test double — real AWS Secrets
	// Manager leaves this "" and lets the SDK resolve the standard regional endpoint.
	EndpointURL string
}

type AwsSecretsManagerStore struct {
	client *secretsmanager.Client
}

func NewAwsSecretsManagerStore(ctx context.Context, cfg AwsSecretsManagerStoreConfig) (*AwsSecretsManagerStore, error) {
	awsCfg, err := awsv2config.LoadDefaultConfig(ctx,
		awsv2config.WithRegion(cfg.Region),
		awsv2config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")),
	)
	if err != nil {
		return nil, err
	}
	client := secretsmanager.NewFromConfig(awsCfg, func(o *secretsmanager.Options) {
		if cfg.EndpointURL != "" {
			o.BaseEndpoint = &cfg.EndpointURL
		}
	})
	return &AwsSecretsManagerStore{client: client}, nil
}

// PutDSN writes the DSN a dsnSecretRef will resolve to. Creates the secret container on first
// write (idempotent: a second call for the same dsnSecretRef just adds a new version, same
// "latest always wins" semantics DbCredentials reads back).
func (s *AwsSecretsManagerStore) PutDSN(ctx context.Context, dsnSecretRef, dsn string) error {
	payload, err := json.Marshal(map[string]string{"dsn": dsn})
	if err != nil {
		return err
	}

	// Best-effort: the container may already exist — PutSecretValue below is the write that
	// actually matters and surfaces a real error clearly if the container genuinely doesn't
	// exist and couldn't be created.
	_, _ = s.client.CreateSecret(ctx, &secretsmanager.CreateSecretInput{Name: &dsnSecretRef})

	payloadStr := string(payload)
	if _, err := s.client.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{
		SecretId: &dsnSecretRef, SecretString: &payloadStr,
	}); err != nil {
		return fmt.Errorf("AWS Secrets Manager put_secret_value failed for %s: %w", dsnSecretRef, err)
	}
	return nil
}

func (s *AwsSecretsManagerStore) DbCredentials(ctx context.Context, dsnSecretRef string) (*DbCreds, error) {
	out, err := s.client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: &dsnSecretRef})
	if err != nil {
		return nil, fmt.Errorf("AWS Secrets Manager get_secret_value failed for %s: %w", dsnSecretRef, err)
	}
	if out.SecretString == nil {
		return nil, fmt.Errorf("AWS Secrets Manager secret %s has no SecretString value", dsnSecretRef)
	}
	var secret struct{ Dsn string }
	if err := json.Unmarshal([]byte(*out.SecretString), &secret); err != nil {
		return nil, fmt.Errorf(`AWS Secrets Manager secret %s is not the expected {"dsn": ...} JSON shape: %w`, dsnSecretRef, err)
	}
	return &DbCreds{DSN: secret.Dsn}, nil
}

func (s *AwsSecretsManagerStore) GetSecret(ctx context.Context, secretRef string) (string, error) {
	out, err := s.client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: &secretRef})
	if err != nil {
		return "", fmt.Errorf("AWS Secrets Manager get_secret_value failed for %s: %w", secretRef, err)
	}
	if out.SecretString == nil {
		return "", fmt.Errorf("AWS Secrets Manager secret %s has no SecretString value", secretRef)
	}
	var secret struct{ Value string }
	if err := json.Unmarshal([]byte(*out.SecretString), &secret); err != nil {
		return "", fmt.Errorf(`AWS Secrets Manager secret %s is not the expected {"value": ...} JSON shape: %w`, secretRef, err)
	}
	return secret.Value, nil
}

func (s *AwsSecretsManagerStore) PutSecret(ctx context.Context, secretRef, value string) error {
	payload, err := json.Marshal(map[string]string{"value": value})
	if err != nil {
		return err
	}
	// Same best-effort create as PutDSN.
	_, _ = s.client.CreateSecret(ctx, &secretsmanager.CreateSecretInput{Name: &secretRef})
	payloadStr := string(payload)
	if _, err := s.client.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{
		SecretId: &secretRef, SecretString: &payloadStr,
	}); err != nil {
		// Built from the reference only — the value must never reach an error string.
		return fmt.Errorf("AWS Secrets Manager put_secret_value failed for %s: %w", secretRef, err)
	}
	return nil
}

func (s *AwsSecretsManagerStore) DeleteSecret(ctx context.Context, secretRef string) error {
	forceDelete := true
	// Without ForceDeleteWithoutRecovery, AWS keeps the secret recoverable for a 30-day window,
	// during which the same reference still resolves — which would make "the tenant revoked
	// this credential" untrue for a month.
	if _, err := s.client.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
		SecretId: &secretRef, ForceDeleteWithoutRecovery: &forceDelete,
	}); err != nil {
		return fmt.Errorf("AWS Secrets Manager delete_secret failed for %s: %w", secretRef, err)
	}
	return nil
}
