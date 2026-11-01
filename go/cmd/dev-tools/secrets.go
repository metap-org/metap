package main

import (
	"context"
	"fmt"
	"os"

	"github.com/joho/godotenv"

	"github.com/metap-org/metap-go/control"
	"github.com/metap-org/metap-go/infra"
	"github.com/metap-org/metap-go/runtime"
)

// vaultPutDSN populates Vault for a dedicated_db tenant — the Vault-backed counterpart to "set
// env var <dsnSecretRef>" that provisionTenant's dedicated_db path already prints for EnvStore.
// Independent of provisionTenant: run it before or after, in either order — Router only
// resolves the DSN when it actually routes a request to that tenant.
func vaultPutDSN(args []string) error {
	if len(args) < 4 {
		fmt.Fprintln(os.Stderr, "Usage: dev-tools vault-put-dsn <dsnSecretRef> <dsn>")
		os.Exit(1)
	}
	_ = godotenv.Load()
	vaultAddr, err := runtime.RequireEnv("VAULT_ADDR")
	if err != nil {
		return err
	}
	vaultToken, err := runtime.RequireEnv("VAULT_TOKEN")
	if err != nil {
		return err
	}
	dsnSecretRef, dsn := args[2], args[3]

	store, err := control.NewVaultStore(vaultAddr, vaultToken)
	if err != nil {
		return err
	}
	if err := store.PutDSN(context.Background(), dsnSecretRef, dsn); err != nil {
		return err
	}

	fmt.Printf("Wrote dsn_secret_ref %q to Vault at %s.\n", dsnSecretRef, vaultAddr)
	return nil
}

// awsSecretsPutDSN is the AWS counterpart to vaultPutDSN — populates AWS Secrets Manager for a
// dedicated_db tenant instead. Independent of provisionTenant, same as that function.
func awsSecretsPutDSN(args []string) error {
	if len(args) < 4 {
		fmt.Fprintln(os.Stderr, "Usage: dev-tools aws-secrets-put-dsn <dsnSecretRef> <dsn>")
		os.Exit(1)
	}
	_ = godotenv.Load()
	region, err := runtime.RequireEnv("AWS_SECRETS_REGION")
	if err != nil {
		return err
	}
	accessKey, err := runtime.RequireEnv("AWS_SECRETS_ACCESS_KEY")
	if err != nil {
		return err
	}
	secretKey, err := runtime.RequireEnv("AWS_SECRETS_SECRET_KEY")
	if err != nil {
		return err
	}
	endpointURL, _ := runtime.Optional("AWS_SECRETS_ENDPOINT_URL")
	dsnSecretRef, dsn := args[2], args[3]

	ctx := context.Background()
	store, err := control.NewAwsSecretsManagerStore(ctx, control.AwsSecretsManagerStoreConfig{
		Region: region, AccessKey: accessKey, SecretKey: secretKey, EndpointURL: endpointURL,
	})
	if err != nil {
		return err
	}
	if err := store.PutDSN(ctx, dsnSecretRef, dsn); err != nil {
		return err
	}

	fmt.Printf("Wrote dsn_secret_ref %q to AWS Secrets Manager in region %s.\n", dsnSecretRef, region)
	return nil
}

// gcpSecretsPutDSN is the GCP counterpart to vaultPutDSN/awsSecretsPutDSN — populates GCP
// Secret Manager for a dedicated_db tenant instead. Independent of provisionTenant, same as
// those.
func gcpSecretsPutDSN(args []string) error {
	if len(args) < 4 {
		fmt.Fprintln(os.Stderr, "Usage: dev-tools gcp-secrets-put-dsn <dsnSecretRef> <dsn>")
		os.Exit(1)
	}
	_ = godotenv.Load()
	projectID, err := runtime.RequireEnv("GCP_SECRETS_PROJECT_ID")
	if err != nil {
		return err
	}
	dsnSecretRef, dsn := args[2], args[3]

	ctx := context.Background()
	store, err := control.NewGcpSecretManagerStore(ctx, projectID)
	if err != nil {
		return err
	}
	if err := store.PutDSN(ctx, dsnSecretRef, dsn); err != nil {
		return err
	}

	fmt.Printf("Wrote dsn_secret_ref %q to GCP Secret Manager in project %s.\n", dsnSecretRef, projectID)
	return nil
}

// putTenantSecret stores a tenant's credential for a "secret" config key, from the operator
// side.
//
// Exists alongside PUT /admin/config/{key} (the tenant-admin equivalent) for two reasons: the
// default EnvStore backend refuses runtime writes, so on a deployment that hasn't configured
// Vault/AWS/GCP this command is what tells an operator the exact environment variable to set —
// it prints the derived reference on failure; and an operator provisioning a credential on a
// tenant's behalf shouldn't have to hold that tenant's admin session.
//
// The reference is derived here exactly as it is everywhere else (control.TenantSecretRef) —
// this command cannot write to an arbitrary reference any more than an HTTP caller can.
func putTenantSecret(args []string) error {
	if len(args) < 5 {
		fmt.Fprintln(os.Stderr, "Usage: dev-tools put-tenant-secret <tenantId> <configKey> <value>")
		fmt.Fprintln(os.Stderr, "  e.g. dev-tools put-tenant-secret <uuid> cron.webhookAuthorization 'Bearer sk_live_...'")
		os.Exit(1)
	}
	_ = godotenv.Load()
	tenantID, err := parseUUID(args[2])
	if err != nil {
		return err
	}
	key, value := args[3], args[4]
	secretRef := control.TenantSecretRef(tenantID, key)

	ctx := context.Background()
	config, err := infra.LoadConfig()
	if err != nil {
		return err
	}
	store, err := control.BuildSecretStore(ctx, control.SecretStoreConfigFromAppConfig(config))
	if err != nil {
		return err
	}
	if err := store.PutSecret(ctx, secretRef, value); err != nil {
		// err already carries the exact variable name on the read-only EnvStore backend, so
		// repeating the reference here would only duplicate it.
		fmt.Fprintf(os.Stderr, "Could not store the credential: %v\n", err)
		fmt.Fprintln(os.Stderr, "On the default environment-variable backend, export that variable in the "+
			"environment of every process that runs webhooks (cron-scheduler).")
		os.Exit(1)
	}

	// Tenant and key only — the reference is not printed on success because nothing acts on it.
	fmt.Printf("Stored the credential for tenant %s, key %q.\n", tenantID, key)
	return nil
}
