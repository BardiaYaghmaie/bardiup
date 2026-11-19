package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/BardiaYaghmaie/bardiup/api/v1alpha1"
	"github.com/BardiaYaghmaie/bardiup/internal/storage"
)

// Credential keys expected in the credentials Secret.
const (
	KeyAccessKeyID     = "AWS_ACCESS_KEY_ID"
	KeySecretAccessKey = "AWS_SECRET_ACCESS_KEY"
	KeySessionToken    = "AWS_SESSION_TOKEN"
)

// ObjectStore is the subset of storage operations the controllers need.
type ObjectStore interface {
	Check(ctx context.Context) error
	Delete(ctx context.Context, keys ...string) error
}

// StoreFactory builds an ObjectStore. Overridable in tests.
type StoreFactory func(ctx context.Context, spec v1alpha1.S3Storage, creds storage.Credentials) (ObjectStore, error)

// DefaultStoreFactory creates real S3 clients.
func DefaultStoreFactory(ctx context.Context, spec v1alpha1.S3Storage, creds storage.Credentials) (ObjectStore, error) {
	return storage.New(ctx, spec, creds)
}

// loadCredentials reads and validates the credentials Secret. A nil ref means the default AWS chain.
func loadCredentials(ctx context.Context, r client.Reader, namespace string, ref *corev1.LocalObjectReference) (storage.Credentials, error) {
	if ref == nil || ref.Name == "" {
		return storage.Credentials{}, nil
	}
	sec := &corev1.Secret{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ref.Name}, sec); err != nil {
		if apierrors.IsNotFound(err) {
			return storage.Credentials{}, fmt.Errorf("credentials secret %q not found in namespace %q", ref.Name, namespace)
		}
		return storage.Credentials{}, err
	}
	c := storage.Credentials{
		AccessKeyID:     string(sec.Data[KeyAccessKeyID]),
		SecretAccessKey: string(sec.Data[KeySecretAccessKey]),
		SessionToken:    string(sec.Data[KeySessionToken]),
	}
	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return c, fmt.Errorf("credentials secret %q must contain %s and %s", ref.Name, KeyAccessKeyID, KeySecretAccessKey)
	}
	return c, nil
}

func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:8]
}

// boundedName returns name if it fits max, otherwise a truncated name with a hash suffix.
func boundedName(name string, maxLen int) string {
	if len(name) <= maxLen {
		return name
	}
	keep := maxLen - 9
	return strings.TrimRight(name[:keep], "-.") + "-" + shortHash(name)
}

// labelValue makes s a valid label value (max 63 chars).
func labelValue(s string) string { return boundedName(s, 63) }

// HumanBytes formats n using binary units.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
