package storage

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/bardiup/bardiup/api/v1alpha1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// S3Backend implements the Backend interface for S3-compatible storage
type S3Backend struct {
	client *s3.Client
	bucket string
	prefix string
	log    logr.Logger
}

// NewS3Backend creates a new S3 storage backend
func NewS3Backend(ctx context.Context, k8sClient client.Client, s3Config *v1alpha1.S3Backend, prefix string, log logr.Logger) (*S3Backend, error) {
	// Get credentials from secret
	secret := &corev1.Secret{}
	if err := k8sClient.Get(ctx, client.ObjectKey{
		Name:      s3Config.CredentialsSecret,
		Namespace: "bardiup-system", // Assuming controller is in bardiup-system namespace
	}, secret); err != nil {
		return nil, fmt.Errorf("failed to get credentials secret: %w", err)
	}

	accessKey := string(secret.Data["accessKeyId"])
	secretKey := string(secret.Data["secretAccessKey"])

	if accessKey == "" || secretKey == "" {
		return nil, fmt.Errorf("accessKeyId and secretAccessKey must be provided in credentials secret")
	}

	// Create AWS config
	awsCfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(s3Config.Region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	// Create S3 client with custom endpoint if provided
	clientOptions := []func(*s3.Options){}
	if s3Config.Endpoint != "" {
		clientOptions = append(clientOptions, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(s3Config.Endpoint)
			if s3Config.ForcePathStyle {
				o.UsePathStyle = true
			}
		})
	}

	s3Client := s3.NewFromConfig(awsCfg, clientOptions...)

	return &S3Backend{
		client: s3Client,
		bucket: s3Config.Bucket,
		prefix: prefix,
		log:    log,
	}, nil
}

// Upload uploads data to S3
func (b *S3Backend) Upload(ctx context.Context, key string, data io.Reader, metadata map[string]string) error {
	fullKey := filepath.Join(b.prefix, key)

	// Convert metadata to S3 format
	s3Metadata := make(map[string]string)
	for k, v := range metadata {
		s3Metadata[k] = v
	}

	input := &s3.PutObjectInput{
		Bucket:   aws.String(b.bucket),
		Key:      aws.String(fullKey),
		Body:     data,
		Metadata: s3Metadata,
	}

	_, err := b.client.PutObject(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to upload to S3: %w", err)
	}

	b.log.Info("Successfully uploaded backup to S3", "bucket", b.bucket, "key", fullKey)
	return nil
}

// Download downloads data from S3
func (b *S3Backend) Download(ctx context.Context, key string) (io.ReadCloser, error) {
	fullKey := filepath.Join(b.prefix, key)

	input := &s3.GetObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(fullKey),
	}

	output, err := b.client.GetObject(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to download from S3: %w", err)
	}

	return output.Body, nil
}

// Delete deletes data from S3
func (b *S3Backend) Delete(ctx context.Context, key string) error {
	fullKey := filepath.Join(b.prefix, key)

	input := &s3.DeleteObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(fullKey),
	}

	_, err := b.client.DeleteObject(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to delete from S3: %w", err)
	}

	b.log.Info("Successfully deleted backup from S3", "bucket", b.bucket, "key", fullKey)
	return nil
}

// Exists checks if a key exists in S3
func (b *S3Backend) Exists(ctx context.Context, key string) (bool, error) {
	fullKey := filepath.Join(b.prefix, key)

	input := &s3.HeadObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(fullKey),
	}

	_, err := b.client.HeadObject(ctx, input)
	if err != nil {
		// Check if it's a not found error
		var notFound *types.NotFound
		if err == notFound {
			return false, nil
		}
		return false, fmt.Errorf("failed to check if object exists: %w", err)
	}

	return true, nil
}

// List lists all keys with the given prefix
func (b *S3Backend) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	fullPrefix := filepath.Join(b.prefix, prefix)

	input := &s3.ListObjectsV2Input{
		Bucket: aws.String(b.bucket),
		Prefix: aws.String(fullPrefix),
	}

	var objects []ObjectInfo
	paginator := s3.NewListObjectsV2Paginator(b.client, input)

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list objects: %w", err)
		}

		for _, obj := range page.Contents {
			// Get metadata for each object
			metadata, _ := b.GetMetadata(ctx, *obj.Key)

			objects = append(objects, ObjectInfo{
				Key:          *obj.Key,
				Size:         *obj.Size,
				LastModified: *obj.LastModified,
				ETag:         *obj.ETag,
				Metadata:     metadata,
			})
		}
	}

	return objects, nil
}

// GetMetadata gets metadata for a specific key
func (b *S3Backend) GetMetadata(ctx context.Context, key string) (map[string]string, error) {
	fullKey := filepath.Join(b.prefix, key)

	input := &s3.HeadObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(fullKey),
	}

	output, err := b.client.HeadObject(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to get metadata: %w", err)
	}

	return output.Metadata, nil
}
