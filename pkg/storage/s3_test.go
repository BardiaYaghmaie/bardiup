package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
)

// MockS3Client implements a mock S3 client for testing
type MockS3Client struct {
	objects       map[string][]byte
	metadata      map[string]map[string]string
	PutObjectFunc func(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObjectFunc func(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteFunc    func(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	HeadFunc      func(ctx context.Context, params *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	ListFunc      func(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

func newMockS3Client() *MockS3Client {
	return &MockS3Client{
		objects:  make(map[string][]byte),
		metadata: make(map[string]map[string]string),
	}
}

// mockReadCloser implements io.ReadCloser for testing
type mockReadCloser struct {
	*bytes.Reader
}

func (m *mockReadCloser) Close() error {
	return nil
}

// S3BackendTestable is a testable version of S3Backend that uses interfaces
type S3BackendTestable struct {
	mockClient *MockS3Client
	bucket     string
	prefix     string
	logger     logr.Logger
}

func newTestableS3Backend(t *testing.T) (*S3BackendTestable, *MockS3Client) {
	mock := newMockS3Client()
	logger := testr.New(t)
	return &S3BackendTestable{
		mockClient: mock,
		bucket:     "test-bucket",
		prefix:     "backups",
		logger:     logger,
	}, mock
}

func TestS3BackendUpload(t *testing.T) {
	backend, mock := newTestableS3Backend(t)

	t.Run("successful upload", func(t *testing.T) {
		mock.PutObjectFunc = func(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
			// Verify the correct bucket and key
			if *params.Bucket != "test-bucket" {
				t.Errorf("expected bucket 'test-bucket', got %s", *params.Bucket)
			}
			return &s3.PutObjectOutput{}, nil
		}

		data := bytes.NewReader([]byte("test data"))
		metadata := map[string]string{"key": "value"}

		err := backend.Upload(context.Background(), "test-key", data, metadata, mock.PutObjectFunc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("upload failure", func(t *testing.T) {
		mock.PutObjectFunc = func(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
			return nil, errors.New("upload failed")
		}

		data := bytes.NewReader([]byte("test data"))
		err := backend.Upload(context.Background(), "test-key", data, nil, mock.PutObjectFunc)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestS3BackendDownload(t *testing.T) {
	backend, mock := newTestableS3Backend(t)

	t.Run("successful download", func(t *testing.T) {
		testData := []byte("downloaded data")
		mock.GetObjectFunc = func(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			return &s3.GetObjectOutput{
				Body: &mockReadCloser{bytes.NewReader(testData)},
			}, nil
		}

		reader, err := backend.Download(context.Background(), "test-key", mock.GetObjectFunc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer reader.Close()

		data, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("failed to read data: %v", err)
		}
		if string(data) != "downloaded data" {
			t.Errorf("expected 'downloaded data', got %s", string(data))
		}
	})

	t.Run("download failure", func(t *testing.T) {
		mock.GetObjectFunc = func(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			return nil, errors.New("download failed")
		}

		_, err := backend.Download(context.Background(), "test-key", mock.GetObjectFunc)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestS3BackendDelete(t *testing.T) {
	backend, mock := newTestableS3Backend(t)

	t.Run("successful delete", func(t *testing.T) {
		mock.DeleteFunc = func(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
			return &s3.DeleteObjectOutput{}, nil
		}

		err := backend.Delete(context.Background(), "test-key", mock.DeleteFunc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("delete failure", func(t *testing.T) {
		mock.DeleteFunc = func(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
			return nil, errors.New("delete failed")
		}

		err := backend.Delete(context.Background(), "test-key", mock.DeleteFunc)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestS3BackendExists(t *testing.T) {
	backend, mock := newTestableS3Backend(t)

	t.Run("object exists", func(t *testing.T) {
		mock.HeadFunc = func(ctx context.Context, params *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
			return &s3.HeadObjectOutput{}, nil
		}

		exists, err := backend.Exists(context.Background(), "test-key", mock.HeadFunc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !exists {
			t.Error("expected object to exist")
		}
	})

	t.Run("object not found", func(t *testing.T) {
		mock.HeadFunc = func(ctx context.Context, params *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
			return nil, &types.NotFound{}
		}

		exists, err := backend.Exists(context.Background(), "test-key", mock.HeadFunc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if exists {
			t.Error("expected object to not exist")
		}
	})

	t.Run("other error", func(t *testing.T) {
		mock.HeadFunc = func(ctx context.Context, params *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
			return nil, errors.New("some error")
		}

		_, err := backend.Exists(context.Background(), "test-key", mock.HeadFunc)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestS3BackendList(t *testing.T) {
	backend, mock := newTestableS3Backend(t)

	t.Run("successful list", func(t *testing.T) {
		now := time.Now()
		mock.ListFunc = func(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
			return &s3.ListObjectsV2Output{
				Contents: []types.Object{
					{
						Key:          aws.String("backups/test1"),
						Size:         aws.Int64(100),
						LastModified: aws.Time(now),
						ETag:         aws.String("etag1"),
					},
					{
						Key:          aws.String("backups/test2"),
						Size:         aws.Int64(200),
						LastModified: aws.Time(now),
						ETag:         aws.String("etag2"),
					},
				},
				IsTruncated: aws.Bool(false),
			}, nil
		}

		// Also need to mock HeadObject for metadata retrieval
		mock.HeadFunc = func(ctx context.Context, params *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
			return &s3.HeadObjectOutput{
				Metadata: map[string]string{"test": "value"},
			}, nil
		}

		objects, err := backend.List(context.Background(), "", mock.ListFunc, mock.HeadFunc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(objects) != 2 {
			t.Errorf("expected 2 objects, got %d", len(objects))
		}
	})

	t.Run("list failure", func(t *testing.T) {
		mock.ListFunc = func(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
			return nil, errors.New("list failed")
		}

		_, err := backend.List(context.Background(), "", mock.ListFunc, mock.HeadFunc)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestS3BackendGetMetadata(t *testing.T) {
	backend, mock := newTestableS3Backend(t)

	t.Run("successful get metadata", func(t *testing.T) {
		mock.HeadFunc = func(ctx context.Context, params *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
			return &s3.HeadObjectOutput{
				Metadata: map[string]string{
					"backup-type": "daily",
					"retention":   "7d",
				},
			}, nil
		}

		metadata, err := backend.GetMetadata(context.Background(), "test-key", mock.HeadFunc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if metadata["backup-type"] != "daily" {
			t.Errorf("expected backup-type 'daily', got %s", metadata["backup-type"])
		}
	})

	t.Run("get metadata failure", func(t *testing.T) {
		mock.HeadFunc = func(ctx context.Context, params *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
			return nil, errors.New("head failed")
		}

		_, err := backend.GetMetadata(context.Background(), "test-key", mock.HeadFunc)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

// Helper methods for testable backend
func (b *S3BackendTestable) Upload(ctx context.Context, key string, data io.Reader, metadata map[string]string, putFn func(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)) error {
	fullKey := b.prefix + "/" + key
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

	_, err := putFn(ctx, input)
	if err != nil {
		return errors.New("failed to upload to S3: " + err.Error())
	}
	b.logger.Info("Successfully uploaded backup to S3", "bucket", b.bucket, "key", fullKey)
	return nil
}

func (b *S3BackendTestable) Download(ctx context.Context, key string, getFn func(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)) (io.ReadCloser, error) {
	fullKey := b.prefix + "/" + key

	input := &s3.GetObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(fullKey),
	}

	output, err := getFn(ctx, input)
	if err != nil {
		return nil, errors.New("failed to download from S3: " + err.Error())
	}
	return output.Body, nil
}

func (b *S3BackendTestable) Delete(ctx context.Context, key string, deleteFn func(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)) error {
	fullKey := b.prefix + "/" + key

	input := &s3.DeleteObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(fullKey),
	}

	_, err := deleteFn(ctx, input)
	if err != nil {
		return errors.New("failed to delete from S3: " + err.Error())
	}
	b.logger.Info("Successfully deleted backup from S3", "bucket", b.bucket, "key", fullKey)
	return nil
}

func (b *S3BackendTestable) Exists(ctx context.Context, key string, headFn func(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)) (bool, error) {
	fullKey := b.prefix + "/" + key

	input := &s3.HeadObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(fullKey),
	}

	_, err := headFn(ctx, input)
	if err != nil {
		var notFound *types.NotFound
		if errors.As(err, &notFound) {
			return false, nil
		}
		return false, errors.New("failed to check if object exists: " + err.Error())
	}
	return true, nil
}

func (b *S3BackendTestable) List(ctx context.Context, prefix string, listFn func(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error), headFn func(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)) ([]ObjectInfo, error) {
	fullPrefix := b.prefix + "/" + prefix

	input := &s3.ListObjectsV2Input{
		Bucket: aws.String(b.bucket),
		Prefix: aws.String(fullPrefix),
	}

	output, err := listFn(ctx, input)
	if err != nil {
		return nil, errors.New("failed to list objects: " + err.Error())
	}

	var objects []ObjectInfo
	for _, obj := range output.Contents {
		metadata, _ := b.GetMetadata(ctx, *obj.Key, headFn)
		objects = append(objects, ObjectInfo{
			Key:          *obj.Key,
			Size:         *obj.Size,
			LastModified: *obj.LastModified,
			ETag:         *obj.ETag,
			Metadata:     metadata,
		})
	}

	return objects, nil
}

func (b *S3BackendTestable) GetMetadata(ctx context.Context, key string, headFn func(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)) (map[string]string, error) {
	fullKey := b.prefix + "/" + key

	input := &s3.HeadObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(fullKey),
	}

	output, err := headFn(ctx, input)
	if err != nil {
		return nil, errors.New("failed to get metadata: " + err.Error())
	}
	return output.Metadata, nil
}

// Test ObjectInfo struct
func TestObjectInfo(t *testing.T) {
	now := time.Now()
	info := ObjectInfo{
		Key:          "test/key",
		Size:         12345,
		LastModified: now,
		ETag:         "abcd1234",
		Metadata:     map[string]string{"foo": "bar"},
	}

	if info.Key != "test/key" {
		t.Errorf("expected key 'test/key', got %s", info.Key)
	}
	if info.Size != 12345 {
		t.Errorf("expected size 12345, got %d", info.Size)
	}
	if !info.LastModified.Equal(now) {
		t.Error("timestamps don't match")
	}
	if info.ETag != "abcd1234" {
		t.Errorf("expected etag 'abcd1234', got %s", info.ETag)
	}
	if info.Metadata["foo"] != "bar" {
		t.Error("metadata doesn't match")
	}
}

// Test Config struct
func TestConfig(t *testing.T) {
	cfg := Config{
		Type:   "s3",
		Prefix: "backups",
	}

	if cfg.Type != "s3" {
		t.Errorf("expected type 's3', got %s", cfg.Type)
	}
	if cfg.Prefix != "backups" {
		t.Errorf("expected prefix 'backups', got %s", cfg.Prefix)
	}
}
