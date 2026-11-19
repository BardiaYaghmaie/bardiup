// Package storage talks to S3-compatible object stores.
package storage

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/BardiaYaghmaie/bardiup/api/v1alpha1"
)

const (
	minPartSize  = 16 << 20
	maxParts     = 9000 // S3 allows 10000; keep headroom for archives larger than the size hint.
	bufferBudget = 512 << 20
)

// Credentials are static S3 credentials. A zero value means "use the default AWS credential chain".
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// Client is an S3 client bound to one bucket.
type Client struct {
	s3     *s3.Client
	bucket string
	sse    types.ServerSideEncryption
	kmsKey string
}

// New builds a client for the given storage spec.
func New(ctx context.Context, spec v1alpha1.S3Storage, creds Credentials) (*Client, error) {
	region := spec.Region
	if region == "" {
		region = "us-east-1"
	}
	// Ride out short object store outages and restarts instead of failing a long backup.
	opts := []func(*config.LoadOptions) error{config.WithRegion(region), config.WithRetryMaxAttempts(10)}
	if creds.AccessKeyID != "" {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(creds.AccessKeyID, creds.SecretAccessKey, creds.SessionToken)))
	}
	if spec.InsecureSkipTLSVerify {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit user opt-in
		opts = append(opts, config.WithHTTPClient(&http.Client{Transport: tr}))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	cli := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if spec.Endpoint != "" {
			o.BaseEndpoint = aws.String(spec.Endpoint)
		}
		o.UsePathStyle = spec.ForcePathStyle
		// Many S3-compatible stores reject the newer default integrity checksums.
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	})
	return &Client{
		s3:     cli,
		bucket: spec.Bucket,
		sse:    types.ServerSideEncryption(spec.ServerSideEncryption),
		kmsKey: spec.KMSKeyID,
	}, nil
}

// Check verifies the bucket exists and is reachable with the configured credentials.
func (c *Client) Check(ctx context.Context) error {
	_, err := c.s3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(c.bucket)})
	if err != nil {
		return fmt.Errorf("bucket %q not reachable: %w", c.bucket, err)
	}
	return nil
}

// Put uploads a small object in one request.
func (c *Client) Put(ctx context.Context, key string, data []byte, contentType string) error {
	in := &s3.PutObjectInput{
		Bucket:        aws.String(c.bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
		ContentType:   aws.String(contentType),
	}
	c.applySSE(&in.ServerSideEncryption, &in.SSEKMSKeyId)
	if _, err := c.s3.PutObject(ctx, in); err != nil {
		return fmt.Errorf("put %s: %w", key, err)
	}
	return nil
}

func (c *Client) applySSE(sse *types.ServerSideEncryption, kms **string) {
	if c.sse == "" {
		return
	}
	*sse = c.sse
	if c.kmsKey != "" {
		*kms = aws.String(c.kmsKey)
	}
}

// PartSize returns the multipart part size for an expected object size.
func PartSize(sizeHint int64) int64 {
	ps := int64(minPartSize)
	if sizeHint/maxParts > ps {
		ps = sizeHint / maxParts
		ps = (ps + (1 << 20) - 1) &^ ((1 << 20) - 1)
	}
	return ps
}

// Upload streams r to key using multipart upload. sizeHint (may be 0) sizes the parts so that
// archives up to roughly that size fit S3's part limit.
func (c *Client) Upload(ctx context.Context, key string, r io.Reader, sizeHint int64, contentType string) error {
	partSize := PartSize(sizeHint)
	first := make([]byte, partSize)
	n, err := io.ReadFull(r, first)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read: %w", err)
	}
	if int64(n) < partSize {
		return c.Put(ctx, key, first[:n], contentType)
	}

	create := &s3.CreateMultipartUploadInput{
		Bucket:      aws.String(c.bucket),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
	}
	c.applySSE(&create.ServerSideEncryption, &create.SSEKMSKeyId)
	mpu, err := c.s3.CreateMultipartUpload(ctx, create)
	if err != nil {
		return fmt.Errorf("create multipart upload %s: %w", key, err)
	}
	if err := c.uploadParts(ctx, key, mpu.UploadId, first, r, partSize); err != nil {
		abortCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		_, _ = c.s3.AbortMultipartUpload(abortCtx, &s3.AbortMultipartUploadInput{
			Bucket: aws.String(c.bucket), Key: aws.String(key), UploadId: mpu.UploadId,
		})
		return err
	}
	return nil
}

func (c *Client) uploadParts(ctx context.Context, key string, uploadID *string, first []byte, r io.Reader, partSize int64) error {
	concurrency := int(bufferBudget / partSize)
	concurrency = max(1, min(concurrency, 4))

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type part struct {
		num  int32
		data []byte
	}
	parts := make(chan part)
	pool := sync.Pool{New: func() any { return make([]byte, partSize) }}
	var (
		mu        sync.Mutex
		completed []types.CompletedPart
		firstErr  error
		wg        sync.WaitGroup
	)
	fail := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
		mu.Unlock()
	}
	for range concurrency {
		wg.Go(func() {
			for p := range parts {
				out, err := c.s3.UploadPart(ctx, &s3.UploadPartInput{
					Bucket:        aws.String(c.bucket),
					Key:           aws.String(key),
					UploadId:      uploadID,
					PartNumber:    aws.Int32(p.num),
					Body:          bytes.NewReader(p.data),
					ContentLength: aws.Int64(int64(len(p.data))),
				})
				if cap(p.data) == int(partSize) {
					pool.Put(p.data[:cap(p.data)]) //nolint:staticcheck // slices are fine here
				}
				if err != nil {
					fail(fmt.Errorf("upload part %d: %w", p.num, err))
					continue
				}
				mu.Lock()
				completed = append(completed, types.CompletedPart{ETag: out.ETag, PartNumber: aws.Int32(p.num)})
				mu.Unlock()
			}
		})
	}

	send := func(p part) bool {
		select {
		case parts <- p:
			return true
		case <-ctx.Done():
			return false
		}
	}

	num := int32(1)
	ok := send(part{num: num, data: first})
	for ok {
		buf := pool.Get().([]byte)
		n, err := io.ReadFull(r, buf)
		if n > 0 {
			num++
			if num > 10000 {
				fail(fmt.Errorf("archive exceeds 10000 parts of %d bytes; raise the PVC size hint", partSize))
				break
			}
			ok = send(part{num: num, data: buf[:n]})
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			fail(fmt.Errorf("read: %w", err))
			break
		}
	}
	close(parts)
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	slices.SortFunc(completed, func(a, b types.CompletedPart) int { return int(*a.PartNumber - *b.PartNumber) })
	_, err := c.s3.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:          aws.String(c.bucket),
		Key:             aws.String(key),
		UploadId:        uploadID,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: completed},
	})
	if err != nil {
		return fmt.Errorf("complete multipart upload %s: %w", key, err)
	}
	return nil
}

// Open returns a reader for key that transparently resumes with ranged requests when the
// connection drops mid-stream.
func (c *Client) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	rr := &resumingReader{ctx: ctx, c: c, key: key}
	if err := rr.open(); err != nil {
		return nil, err
	}
	return rr, nil
}

type resumingReader struct {
	ctx     context.Context
	c       *Client
	key     string
	body    io.ReadCloser
	offset  int64
	retries int
}

func (r *resumingReader) open() error {
	in := &s3.GetObjectInput{Bucket: aws.String(r.c.bucket), Key: aws.String(r.key)}
	if r.offset > 0 {
		in.Range = aws.String(fmt.Sprintf("bytes=%d-", r.offset))
	}
	out, err := r.c.s3.GetObject(r.ctx, in)
	if err != nil {
		return fmt.Errorf("get %s: %w", r.key, err)
	}
	r.body = out.Body
	return nil
}

func (r *resumingReader) Read(p []byte) (int, error) {
	for {
		n, err := r.body.Read(p)
		r.offset += int64(n)
		if err == nil || errors.Is(err, io.EOF) || r.ctx.Err() != nil {
			return n, err
		}
		if r.retries >= 5 {
			return n, err
		}
		r.retries++
		_ = r.body.Close()
		time.Sleep(time.Duration(r.retries) * time.Second)
		if oerr := r.open(); oerr != nil {
			return n, fmt.Errorf("resume after %w: %w", err, oerr)
		}
		if n > 0 {
			return n, nil
		}
	}
}

func (r *resumingReader) Close() error { return r.body.Close() }

// Delete removes keys. Missing keys are not an error.
func (c *Client) Delete(ctx context.Context, keys ...string) error {
	for _, k := range keys {
		_, err := c.s3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(c.bucket), Key: aws.String(k)})
		if err != nil && !IsNotFound(err) {
			return fmt.Errorf("delete %s: %w", k, err)
		}
	}
	return nil
}

// IsNotFound reports whether err is an S3 "not found" error.
func IsNotFound(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "NoSuchBucket":
			return true
		}
	}
	return false
}

// ObjectKey builds the archive key for a backup.
func ObjectKey(prefix, namespace, pvc, backup string) string {
	parts := []string{}
	if p := strings.Trim(prefix, "/"); p != "" {
		parts = append(parts, p)
	}
	parts = append(parts, namespace, pvc, backup+".tar.gz")
	return strings.Join(parts, "/")
}

// ManifestKey is the key of the JSON manifest stored next to an archive.
func ManifestKey(objectKey string) string { return objectKey + ".json" }
