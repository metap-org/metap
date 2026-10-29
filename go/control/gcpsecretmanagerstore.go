// SecretStore over GCP Secret Manager. Unlike the AWS/S3-backed stores in this codebase, GCP has
// no access-key/secret-key credential shape to construct explicitly — its idiomatic identity
// mechanism is Application Default Credentials (a service-account JSON key file named by
// GOOGLE_APPLICATION_CREDENTIALS, or the ambient workload identity on GKE/Cloud Run/GCE), which
// secretmanager.NewClient resolves on its own. That's the one deliberate asymmetry with
// AwsSecretsManagerStoreConfig here — everything else (the {"dsn": "..."} JSON payload shape,
// the PutDSN/DbCredentials split) mirrors it exactly.
package control

import (
	"context"
	"encoding/json"
	"fmt"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
)

type GcpSecretManagerStore struct {
	client    *secretmanager.Client
	projectID string
}

// NewGcpSecretManagerStore builds the client via Application Default Credentials — see this
// file's own doc comment for why there's no explicit-credentials constructor parameter the way
// NewAwsSecretsManagerStore has one.
func NewGcpSecretManagerStore(ctx context.Context, projectID string) (*GcpSecretManagerStore, error) {
	client, err := secretmanager.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to build GCP Secret Manager client: %w", err)
	}
	return &GcpSecretManagerStore{client: client, projectID: projectID}, nil
}

func (s *GcpSecretManagerStore) secretResourceName(ref string) string {
	return fmt.Sprintf("projects/%s/secrets/%s", s.projectID, ref)
}

// ensureSecretExists is the best-effort "create the container" half of a write — the version
// add that follows is what actually matters and surfaces a real error clearly if the container
// genuinely doesn't exist and couldn't be created.
func (s *GcpSecretManagerStore) ensureSecretExists(ctx context.Context, ref string) {
	_, _ = s.client.CreateSecret(ctx, &secretmanagerpb.CreateSecretRequest{
		Parent:   "projects/" + s.projectID,
		SecretId: ref,
		Secret: &secretmanagerpb.Secret{
			Replication: &secretmanagerpb.Replication{
				Replication: &secretmanagerpb.Replication_Automatic_{
					Automatic: &secretmanagerpb.Replication_Automatic{},
				},
			},
		},
	})
}

func (s *GcpSecretManagerStore) addVersion(ctx context.Context, ref string, payload []byte) error {
	s.ensureSecretExists(ctx, ref)
	_, err := s.client.AddSecretVersion(ctx, &secretmanagerpb.AddSecretVersionRequest{
		Parent:  s.secretResourceName(ref),
		Payload: &secretmanagerpb.SecretPayload{Data: payload},
	})
	return err
}

func (s *GcpSecretManagerStore) accessLatest(ctx context.Context, ref string) ([]byte, error) {
	resp, err := s.client.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{
		Name: s.secretResourceName(ref) + "/versions/latest",
	})
	if err != nil {
		return nil, err
	}
	if resp.Payload == nil {
		return nil, fmt.Errorf("GCP Secret Manager secret %s has no payload", ref)
	}
	return resp.Payload.Data, nil
}

// PutDSN writes the DSN a dsnSecretRef will resolve to. Creates the secret container (automatic,
// multi-region replication — the simplest choice, no cross-region compliance requirement drove
// this) on first write if it doesn't already exist, then adds a new version.
func (s *GcpSecretManagerStore) PutDSN(ctx context.Context, dsnSecretRef, dsn string) error {
	payload, err := json.Marshal(map[string]string{"dsn": dsn})
	if err != nil {
		return err
	}
	if err := s.addVersion(ctx, dsnSecretRef, payload); err != nil {
		return fmt.Errorf("GCP Secret Manager add_secret_version failed for %s: %w", dsnSecretRef, err)
	}
	return nil
}

func (s *GcpSecretManagerStore) DbCredentials(ctx context.Context, dsnSecretRef string) (*DbCreds, error) {
	data, err := s.accessLatest(ctx, dsnSecretRef)
	if err != nil {
		return nil, fmt.Errorf("GCP Secret Manager access_secret_version failed for %s: %w", dsnSecretRef, err)
	}
	var secret struct{ Dsn string }
	if err := json.Unmarshal(data, &secret); err != nil {
		return nil, fmt.Errorf(`GCP Secret Manager secret %s is not the expected {"dsn": ...} JSON shape: %w`, dsnSecretRef, err)
	}
	return &DbCreds{DSN: secret.Dsn}, nil
}

func (s *GcpSecretManagerStore) GetSecret(ctx context.Context, secretRef string) (string, error) {
	data, err := s.accessLatest(ctx, secretRef)
	if err != nil {
		return "", fmt.Errorf("GCP Secret Manager access_secret_version failed for %s: %w", secretRef, err)
	}
	var secret struct{ Value string }
	if err := json.Unmarshal(data, &secret); err != nil {
		return "", fmt.Errorf(`GCP Secret Manager secret %s is not the expected {"value": ...} JSON shape: %w`, secretRef, err)
	}
	return secret.Value, nil
}

func (s *GcpSecretManagerStore) PutSecret(ctx context.Context, secretRef, value string) error {
	payload, err := json.Marshal(map[string]string{"value": value})
	if err != nil {
		return err
	}
	// Built from the reference only — the value must never reach an error string.
	if err := s.addVersion(ctx, secretRef, payload); err != nil {
		return fmt.Errorf("GCP Secret Manager add_secret_version failed for %s: %w", secretRef, err)
	}
	return nil
}

func (s *GcpSecretManagerStore) DeleteSecret(ctx context.Context, secretRef string) error {
	if err := s.client.DeleteSecret(ctx, &secretmanagerpb.DeleteSecretRequest{Name: s.secretResourceName(secretRef)}); err != nil {
		return fmt.Errorf("GCP Secret Manager delete_secret failed for %s: %w", secretRef, err)
	}
	return nil
}
