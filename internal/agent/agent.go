// Package agent is the data mover that runs inside backup and restore Jobs.
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"time"

	"github.com/BardiaYaghmaie/bardiup/api/v1alpha1"
	"github.com/BardiaYaghmaie/bardiup/internal/archive"
	"github.com/BardiaYaghmaie/bardiup/internal/storage"
	"github.com/BardiaYaghmaie/bardiup/internal/version"
)

// ConfigEnv is the environment variable carrying the JSON-encoded Config.
const ConfigEnv = "BARDIUP_AGENT_CONFIG"

// DataDir is where the volume is mounted inside mover pods.
const DataDir = "/data"

// Config is passed from the controller to the agent.
type Config struct {
	Storage   v1alpha1.S3Storage `json:"storage"`
	ObjectKey string             `json:"objectKey"`

	// Backup
	Excludes []string  `json:"excludes,omitempty"`
	SizeHint int64     `json:"sizeHint,omitempty"`
	Manifest *Manifest `json:"manifest,omitempty"`

	// Restore
	ExpectedSHA256 string `json:"expectedSHA256,omitempty"`
	Clean          bool   `json:"clean,omitempty"`
}

// Manifest is stored as <objectKey>.json next to every archive so a bucket is
// self-describing and restorable without the cluster that produced it.
type Manifest struct {
	FormatVersion  int                    `json:"formatVersion"`
	BardiupVersion string                 `json:"bardiupVersion"`
	Backup         string                 `json:"backup"`
	BackupConfig   string                 `json:"backupConfig,omitempty"`
	Namespace      string                 `json:"namespace"`
	PVC            string                 `json:"pvc"`
	Source         *v1alpha1.SourceVolume `json:"source,omitempty"`
	Method         v1alpha1.BackupMethod  `json:"method"`
	StartedAt      time.Time              `json:"startedAt"`
	CompletedAt    time.Time              `json:"completedAt"`
	SHA256         string                 `json:"sha256"`
	SizeBytes      int64                  `json:"sizeBytes"`
	Files          int64                  `json:"files"`
	DataBytes      int64                  `json:"dataBytes"`
	Format         string                 `json:"format"`
}

// Result is written to the container termination message and read back by the controller.
type Result struct {
	SHA256    string   `json:"sha256,omitempty"`
	SizeBytes int64    `json:"sizeBytes,omitempty"`
	Files     int64    `json:"files,omitempty"`
	DataBytes int64    `json:"dataBytes,omitempty"`
	Skipped   int64    `json:"skipped,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// LoadConfig reads the agent configuration from the environment.
func LoadConfig() (*Config, error) {
	raw := os.Getenv(ConfigEnv)
	if raw == "" {
		return nil, fmt.Errorf("%s is not set", ConfigEnv)
	}
	cfg := &Config{}
	if err := json.Unmarshal([]byte(raw), cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", ConfigEnv, err)
	}
	if cfg.ObjectKey == "" {
		return nil, errors.New("objectKey is empty")
	}
	return cfg, nil
}

func newClient(ctx context.Context, cfg *Config) (*storage.Client, error) {
	return storage.New(ctx, cfg.Storage, storage.Credentials{})
}

type counter struct{ n int64 }

func (c *counter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

// Backup archives dir and uploads it plus its manifest.
func Backup(ctx context.Context, cfg *Config, dir string) (*Result, error) {
	cli, err := newClient(ctx, cfg)
	if err != nil {
		return nil, err
	}
	started := time.Now().UTC()

	pr, pw := io.Pipe()
	h := sha256.New()
	cnt := &counter{}
	type writeResult struct {
		st  *archive.Stats
		err error
	}
	done := make(chan writeResult, 1)
	go func() {
		st, err := archive.Write(io.MultiWriter(pw, h, cnt), dir, cfg.Excludes)
		_ = pw.CloseWithError(err)
		done <- writeResult{st, err}
	}()

	upErr := cli.Upload(ctx, cfg.ObjectKey, pr, cfg.SizeHint, "application/gzip")
	_ = pr.CloseWithError(errors.New("upload aborted"))
	wr := <-done
	if wr.err != nil {
		return nil, fmt.Errorf("archive %s: %w", dir, wr.err)
	}
	if upErr != nil {
		return nil, fmt.Errorf("upload: %w", upErr)
	}

	res := &Result{
		SHA256:    hex.EncodeToString(h.Sum(nil)),
		SizeBytes: cnt.n,
		Files:     wr.st.Files,
		DataBytes: wr.st.Bytes,
		Skipped:   wr.st.Skipped,
		Warnings:  wr.st.Warnings,
	}

	m := Manifest{}
	if cfg.Manifest != nil {
		m = *cfg.Manifest
	}
	m.FormatVersion = 1
	m.BardiupVersion = version.Version
	m.StartedAt = started
	m.CompletedAt = time.Now().UTC()
	m.SHA256 = res.SHA256
	m.SizeBytes = res.SizeBytes
	m.Files = res.Files
	m.DataBytes = res.DataBytes
	m.Format = "tar+gzip"
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := cli.Put(ctx, storage.ManifestKey(cfg.ObjectKey), data, "application/json"); err != nil {
		return nil, fmt.Errorf("upload manifest: %w", err)
	}
	return res, nil
}

type hashReader struct {
	r io.Reader
	h hash.Hash
}

func (h *hashReader) Read(p []byte) (int, error) {
	n, err := h.r.Read(p)
	h.h.Write(p[:n])
	return n, err
}

// Restore downloads the archive and extracts it into dir, verifying its checksum.
func Restore(ctx context.Context, cfg *Config, dir string) (*Result, error) {
	cli, err := newClient(ctx, cfg)
	if err != nil {
		return nil, err
	}
	expected := cfg.ExpectedSHA256
	if expected == "" {
		expected = manifestChecksum(ctx, cli, cfg.ObjectKey)
	}
	body, err := cli.Open(ctx, cfg.ObjectKey)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	hr := &hashReader{r: body, h: sha256.New()}
	st, err := archive.Extract(hr, dir, archive.ExtractOptions{Clean: cfg.Clean, Chown: os.Geteuid() == 0})
	if err != nil {
		return nil, err
	}
	sum := hex.EncodeToString(hr.h.Sum(nil))
	if expected != "" && sum != expected {
		return nil, fmt.Errorf("checksum mismatch: archive is %s, backup recorded %s; data in the target may be incomplete", sum, expected)
	}
	return &Result{SHA256: sum, Files: st.Files, DataBytes: st.Bytes, Skipped: st.Skipped, Warnings: st.Warnings}, nil
}

func manifestChecksum(ctx context.Context, cli *storage.Client, key string) string {
	rc, err := cli.Open(ctx, storage.ManifestKey(key))
	if err != nil {
		return ""
	}
	defer rc.Close()
	var m Manifest
	if err := json.NewDecoder(io.LimitReader(rc, 1<<20)).Decode(&m); err != nil {
		return ""
	}
	return m.SHA256
}

// WriteTerminationMessage writes res to path, trimming warnings to stay below the 4KiB kubelet limit.
func WriteTerminationMessage(path string, res *Result) error {
	for {
		data, err := json.Marshal(res)
		if err != nil {
			return err
		}
		if len(data) <= 4000 || (len(res.Warnings) == 0 && len(res.Error) < 200) {
			return os.WriteFile(path, data, 0o644)
		}
		if len(res.Warnings) > 0 {
			res.Warnings = res.Warnings[:len(res.Warnings)-1]
		} else {
			res.Error = res.Error[:len(res.Error)/2]
		}
	}
}

// ParseResult decodes a termination message.
func ParseResult(msg string) (*Result, error) {
	res := &Result{}
	if err := json.Unmarshal([]byte(msg), res); err != nil {
		return nil, err
	}
	return res, nil
}
