package storage

import (
	"context"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/BardiaYaghmaie/bardiup/api/v1alpha1"
)

func newFake(t *testing.T) *Client {
	t.Helper()
	backend := s3mem.New()
	if err := backend.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(gofakes3.New(backend).Server())
	t.Cleanup(srv.Close)
	c, err := New(context.Background(), v1alpha1.S3Storage{Bucket: "b", Endpoint: srv.URL, ForcePathStyle: true},
		Credentials{AccessKeyID: "a", SecretAccessKey: "s"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPartSize(t *testing.T) {
	if got := PartSize(0); got != minPartSize {
		t.Errorf("PartSize(0) = %d", got)
	}
	tib := int64(1) << 40
	ps := PartSize(tib)
	if ps*maxParts < tib || ps%(1<<20) != 0 {
		t.Errorf("PartSize(1TiB) = %d does not cover the volume or is not MiB aligned", ps)
	}
}

func TestObjectKey(t *testing.T) {
	if got := ObjectKey("/team/a/", "ns", "pvc", "b1"); got != "team/a/ns/pvc/b1.tar.gz" {
		t.Errorf("got %s", got)
	}
	if got := ObjectKey("", "ns", "pvc", "b1"); got != "ns/pvc/b1.tar.gz" {
		t.Errorf("got %s", got)
	}
	if ManifestKey("x.tar.gz") != "x.tar.gz.json" {
		t.Error("manifest key")
	}
}

func TestClientOperations(t *testing.T) {
	ctx := context.Background()
	c := newFake(t)
	if err := c.Check(ctx); err != nil {
		t.Fatalf("check: %v", err)
	}
	if err := c.Put(ctx, "k", []byte("v"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	rc, err := c.Open(ctx, "k")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "v" {
		t.Errorf("got %q", b)
	}
	if err := c.Delete(ctx, "k", "never-existed"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := c.Open(ctx, "k"); err == nil || !IsNotFound(err) {
		t.Errorf("expected not found, got %v", err)
	}

	missing := *c
	missing.bucket = "nope"
	if err := missing.Check(ctx); err == nil {
		t.Error("expected missing bucket error")
	}
}
