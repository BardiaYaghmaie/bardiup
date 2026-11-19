package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/BardiaYaghmaie/bardiup/api/v1alpha1"
	"github.com/BardiaYaghmaie/bardiup/internal/storage"
)

func fakeS3(t *testing.T) v1alpha1.S3Storage {
	t.Helper()
	backend := s3mem.New()
	if err := backend.CreateBucket("backups"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(gofakes3.New(backend).Server())
	t.Cleanup(srv.Close)
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	return v1alpha1.S3Storage{Bucket: "backups", Region: "us-east-1", Endpoint: srv.URL, ForcePathStyle: true}
}

func write(t *testing.T, p string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, mode); err != nil {
		t.Fatal(err)
	}
}

func TestBackupRestoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	s3cfg := fakeS3(t)

	src := t.TempDir()
	big := make([]byte, 40<<20) // forces a multipart upload with several parts
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(src, "big.bin"), big, 0o600)
	write(t, filepath.Join(src, "dir/nested/hello.txt"), []byte("hello"), 0o640)
	write(t, filepath.Join(src, "script.sh"), []byte("#!/bin/sh\n"), 0o755)
	write(t, filepath.Join(src, "cache/skip.bin"), []byte("x"), 0o644)
	write(t, filepath.Join(src, "dir/skip.tmp"), []byte("x"), 0o644)
	if err := os.Symlink("dir/nested/hello.txt", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(src, "script.sh"), filepath.Join(src, "hardlink.sh")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{
		Storage:   s3cfg,
		ObjectKey: storage.ObjectKey("prefix", "ns", "data", "bk-1"),
		Excludes:  []string{"cache", "*.tmp"},
		Manifest:  &Manifest{Backup: "bk-1", Namespace: "ns", PVC: "data", Method: v1alpha1.BackupMethodCopy},
	}
	res, err := Backup(ctx, cfg, src)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if res.SizeBytes == 0 || len(res.SHA256) != 64 {
		t.Fatalf("bad result %+v", res)
	}
	if res.Skipped != 2 {
		t.Errorf("skipped = %d, want 2", res.Skipped)
	}

	cli, err := storage.New(ctx, s3cfg, storage.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	rc, err := cli.Open(ctx, storage.ManifestKey(cfg.ObjectKey))
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.NewDecoder(rc).Decode(&m); err != nil {
		t.Fatal(err)
	}
	rc.Close()
	if m.SHA256 != res.SHA256 || m.PVC != "data" || m.Files != res.Files || m.Format != "tar+gzip" {
		t.Fatalf("manifest mismatch: %+v vs %+v", m, res)
	}

	// The stored object hashes to the recorded checksum.
	rc, err = cli.Open(ctx, cfg.ObjectKey)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	if _, err := io.Copy(h, rc); err != nil {
		t.Fatal(err)
	}
	rc.Close()
	if got := hex.EncodeToString(h.Sum(nil)); got != res.SHA256 {
		t.Fatalf("object sha %s != %s", got, res.SHA256)
	}

	dst := t.TempDir()
	write(t, filepath.Join(dst, "stale.txt"), []byte("old"), 0o644)
	rcfg := &Config{Storage: s3cfg, ObjectKey: cfg.ObjectKey, ExpectedSHA256: res.SHA256, Clean: true}
	rres, err := Restore(ctx, rcfg, dst)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if rres.Files != res.Files {
		t.Errorf("restored %d files, backed up %d", rres.Files, res.Files)
	}

	got, err := os.ReadFile(filepath.Join(dst, "big.bin"))
	if err != nil || !bytes.Equal(got, big) {
		t.Fatalf("big.bin differs (err=%v)", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "dir/nested/hello.txt")); string(b) != "hello" {
		t.Errorf("hello.txt = %q", b)
	}
	if fi, _ := os.Stat(filepath.Join(dst, "script.sh")); fi == nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("script.sh mode not preserved: %v", fi)
	}
	if fi, _ := os.Stat(filepath.Join(dst, "dir/nested/hello.txt")); fi == nil || fi.Mode().Perm() != 0o640 {
		t.Errorf("hello.txt mode not preserved: %v", fi)
	}
	if target, err := os.Readlink(filepath.Join(dst, "link")); err != nil || target != "dir/nested/hello.txt" {
		t.Errorf("symlink = %q, %v", target, err)
	}
	a, _ := os.Stat(filepath.Join(dst, "script.sh"))
	b, _ := os.Stat(filepath.Join(dst, "hardlink.sh"))
	if a == nil || b == nil || !os.SameFile(a, b) {
		t.Error("hardlink not restored as a hardlink")
	}
	if fi, err := os.Stat(filepath.Join(dst, "empty")); err != nil || !fi.IsDir() {
		t.Error("empty dir missing")
	}
	for _, p := range []string{"stale.txt", "cache", "dir/skip.tmp"} {
		if _, err := os.Lstat(filepath.Join(dst, p)); !os.IsNotExist(err) {
			t.Errorf("%s should not exist after clean restore", p)
		}
	}

	// Archives are standard: GNU/BSD tar can list them.
	if tarBin, err := exec.LookPath("tar"); err == nil {
		rc, _ = cli.Open(ctx, cfg.ObjectKey)
		defer rc.Close()
		cmd := exec.Command(tarBin, "-tzf", "-")
		cmd.Stdin = rc
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("tar -tzf: %v", err)
		}
		if !strings.Contains(string(out), "dir/nested/hello.txt") {
			t.Errorf("tar listing missing file:\n%s", out)
		}
	}
}

func TestRestoreChecksumMismatch(t *testing.T) {
	ctx := context.Background()
	s3cfg := fakeS3(t)
	src := t.TempDir()
	write(t, filepath.Join(src, "a"), []byte("a"), 0o644)
	cfg := &Config{Storage: s3cfg, ObjectKey: "k.tar.gz"}
	if _, err := Backup(ctx, cfg, src); err != nil {
		t.Fatal(err)
	}
	_, err := Restore(ctx, &Config{Storage: s3cfg, ObjectKey: "k.tar.gz", ExpectedSHA256: strings.Repeat("0", 64)}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
}

func TestRestoreVerifiesAgainstManifest(t *testing.T) {
	ctx := context.Background()
	s3cfg := fakeS3(t)
	src := t.TempDir()
	write(t, filepath.Join(src, "a"), []byte("a"), 0o644)
	if _, err := Backup(ctx, &Config{Storage: s3cfg, ObjectKey: "k.tar.gz"}, src); err != nil {
		t.Fatal(err)
	}
	cli, _ := storage.New(ctx, s3cfg, storage.Credentials{})
	if err := cli.Put(ctx, "k.tar.gz.json", []byte(`{"sha256":"`+strings.Repeat("1", 64)+`"}`), "application/json"); err != nil {
		t.Fatal(err)
	}
	_, err := Restore(ctx, &Config{Storage: s3cfg, ObjectKey: "k.tar.gz"}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected manifest checksum to be enforced, got %v", err)
	}
}

func TestRestoreMissingObject(t *testing.T) {
	s3cfg := fakeS3(t)
	_, err := Restore(context.Background(), &Config{Storage: s3cfg, ObjectKey: "missing.tar.gz"}, t.TempDir())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBackupEmptyVolume(t *testing.T) {
	s3cfg := fakeS3(t)
	res, err := Backup(context.Background(), &Config{Storage: s3cfg, ObjectKey: "e.tar.gz"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 0 || res.SizeBytes == 0 {
		t.Fatalf("unexpected %+v", res)
	}
	if _, err := Restore(context.Background(), &Config{Storage: s3cfg, ObjectKey: "e.tar.gz", ExpectedSHA256: res.SHA256}, t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestTerminationMessageLimit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "msg")
	res := &Result{Error: strings.Repeat("e", 10000)}
	for range 100 {
		res.Warnings = append(res.Warnings, strings.Repeat("w", 100))
	}
	if err := WriteTerminationMessage(p, res); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	if len(data) > 4096 {
		t.Fatalf("message is %d bytes", len(data))
	}
	if _, err := ParseResult(string(data)); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfig(t *testing.T) {
	t.Setenv(ConfigEnv, "")
	if _, err := LoadConfig(); err == nil {
		t.Error("expected error for empty env")
	}
	t.Setenv(ConfigEnv, "{")
	if _, err := LoadConfig(); err == nil {
		t.Error("expected parse error")
	}
	t.Setenv(ConfigEnv, `{"storage":{"bucket":"b"}}`)
	if _, err := LoadConfig(); err == nil {
		t.Error("expected missing key error")
	}
	t.Setenv(ConfigEnv, `{"storage":{"bucket":"b"},"objectKey":"k"}`)
	if c, err := LoadConfig(); err != nil || c.ObjectKey != "k" {
		t.Errorf("got %+v, %v", c, err)
	}
}
