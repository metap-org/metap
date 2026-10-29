package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsv2config "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/google/uuid"
)

// S3ObjectStoreConfig mirrors metap-storage's Rust S3ObjectStoreConfig.
type S3ObjectStoreConfig struct {
	// EndpointURL e.g. "http://localhost:8333" for a local SeaweedFS S3 gateway. Real AWS S3
	// leaves this empty and lets the SDK resolve the standard AWS endpoint for Region.
	EndpointURL string
	// Region: every self-hosted S3-compatible server under consideration ignores the region
	// value itself but the SDK still requires one.
	Region    string
	AccessKey string
	SecretKey string
	Bucket    string
	// ForcePathStyle: SeaweedFS (and most self-hosted S3-compatible servers) need path-style
	// addressing (http://host/bucket/key) rather than virtual-hosted-style DNS. true for
	// SeaweedFS.
	ForcePathStyle bool
}

// S3ObjectStore is ObjectStore over the S3 HTTP API — works unmodified against SeaweedFS and any
// other S3-API-compatible server by changing S3ObjectStoreConfig alone.
type S3ObjectStore struct {
	client *s3.Client
	bucket string
}

// NewS3ObjectStore builds a client from cfg. endpointURL/forcePathStyle are per-request options
// on each S3 API call (aws-sdk-go-v2's idiom, rather than Rust's per-client builder field) so
// this function itself never fails — it only needs to succeed in loading the SDK's own default
// config shape.
func NewS3ObjectStore(ctx context.Context, cfg S3ObjectStoreConfig) (*S3ObjectStore, error) {
	awsCfg, err := awsv2config.LoadDefaultConfig(ctx,
		awsv2config.WithRegion(cfg.Region),
		awsv2config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("loading AWS SDK config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.EndpointURL != "" {
			o.BaseEndpoint = aws.String(cfg.EndpointURL)
		}
		o.UsePathStyle = cfg.ForcePathStyle
	})
	return &S3ObjectStore{client: client, bucket: cfg.Bucket}, nil
}

// scopedKey is the real S3 key for (tenantID, key) — every call site goes through this.
func scopedKey(tenantID uuid.UUID, key string) (string, error) {
	if err := validateKey(key); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%s", tenantID, key), nil
}

func isNotFound(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound":
			return true
		}
	}
	return false
}

func (s *S3ObjectStore) Put(ctx context.Context, tenantID uuid.UUID, key string, data []byte, contentType string) error {
	scoped, err := scopedKey(tenantID, key)
	if err != nil {
		return err
	}
	input := &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(scoped),
		Body:   bytes.NewReader(data),
	}
	if contentType != "" {
		input.ContentType = aws.String(contentType)
	}
	_, err = s.client.PutObject(ctx, input)
	return err
}

func (s *S3ObjectStore) Get(ctx context.Context, tenantID uuid.UUID, key string) ([]byte, bool, error) {
	scoped, err := scopedKey(tenantID, key)
	if err != nil {
		return nil, false, err
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(scoped)})
	if isNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func (s *S3ObjectStore) Delete(ctx context.Context, tenantID uuid.UUID, key string) error {
	scoped, err := scopedKey(tenantID, key)
	if err != nil {
		return err
	}
	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(scoped)})
	return err
}

func (s *S3ObjectStore) Exists(ctx context.Context, tenantID uuid.UUID, key string) (bool, error) {
	scoped, err := scopedKey(tenantID, key)
	if err != nil {
		return false, err
	}
	_, err = s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(scoped)})
	if isNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
