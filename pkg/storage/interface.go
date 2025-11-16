package storage

import (
	"context"
	"io"
	"time"
)

// Backend defines the interface for backup storage backends
type Backend interface {
	// Upload uploads data to the storage backend
	Upload(ctx context.Context, key string, data io.Reader, metadata map[string]string) error

	// Download downloads data from the storage backend
	Download(ctx context.Context, key string) (io.ReadCloser, error)

	// Delete deletes data from the storage backend
	Delete(ctx context.Context, key string) error

	// Exists checks if a key exists in the storage backend
	Exists(ctx context.Context, key string) (bool, error)

	// List lists all keys with the given prefix
	List(ctx context.Context, prefix string) ([]ObjectInfo, error)

	// GetMetadata gets metadata for a specific key
	GetMetadata(ctx context.Context, key string) (map[string]string, error)
}

// ObjectInfo contains information about a stored object
type ObjectInfo struct {
	Key          string
	Size         int64
	LastModified time.Time
	ETag         string
	Metadata     map[string]string
}

// Config contains common storage backend configuration
type Config struct {
	Type   string
	Prefix string
}
