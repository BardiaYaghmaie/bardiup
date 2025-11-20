package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type mockS3Client struct {
	mock.Mock
}

func (m *mockS3Client) HeadObject(ctx context.Context, params *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	args := m.Called(ctx, params, optFns)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*s3.HeadObjectOutput), args.Error(1)
}

func (m *mockS3Client) PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	args := m.Called(ctx, params, optFns)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*s3.PutObjectOutput), args.Error(1)
}

func (m *mockS3Client) GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	args := m.Called(ctx, params, optFns)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*s3.GetObjectOutput), args.Error(1)
}

func (m *mockS3Client) DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	args := m.Called(ctx, params, optFns)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*s3.DeleteObjectOutput), args.Error(1)
}

func (m *mockS3Client) ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	args := m.Called(ctx, params, optFns)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*s3.ListObjectsV2Output), args.Error(1)
}

func TestS3Backend_Exists(t *testing.T) {
	tests := []struct {
		name           string
		mockOutput     *s3.HeadObjectOutput
		mockErr        error
		expectedExists bool
		expectedErr    bool
	}{
		{
			name:           "object exists",
			mockOutput:     &s3.HeadObjectOutput{},
			mockErr:        nil,
			expectedExists: true,
			expectedErr:    false,
		},
		{
			name:           "object does not exist",
			mockOutput:     nil,
			mockErr:        &types.NotFound{},
			expectedExists: false,
			expectedErr:    false,
		},
		{
			name:           "other error",
			mockOutput:     nil,
			mockErr:        errors.New("some other error"),
			expectedExists: false,
			expectedErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := new(mockS3Client)
			backend := &S3Backend{
				client: mockClient,
				bucket: "test-bucket",
				log:    logr.Discard(),
			}

			mockClient.On("HeadObject", mock.Anything, mock.Anything, mock.Anything).Return(tt.mockOutput, tt.mockErr)

			exists, err := backend.Exists(context.Background(), "test-key")

			assert.Equal(t, tt.expectedExists, exists)
			if tt.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
