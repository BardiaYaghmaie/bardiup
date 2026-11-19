package archive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExcluded(t *testing.T) {
	cases := []struct {
		rel      string
		patterns []string
		want     bool
	}{
		{"lost+found", []string{"lost+found"}, true},
		{"a/b/c.tmp", []string{"*.tmp"}, true},
		{"cache/x", []string{"cache/*"}, true},
		{"other/cache/x", []string{"cache/*"}, false},
		{"data/file", []string{"*.tmp", "cache"}, false},
		{"deep/cache", []string{"cache"}, true},
		{"cache", []string{"/cache/"}, true},
	}
	for _, c := range cases {
		if got := Excluded(c.rel, c.patterns); got != c.want {
			t.Errorf("Excluded(%q, %v) = %v, want %v", c.rel, c.patterns, got, c.want)
		}
	}
}

func malicious(t *testing.T, entries ...tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, h := range entries {
		h := h
		if h.Typeflag == tar.TypeReg {
			h.Size = 1
		}
		if err := tw.WriteHeader(&h); err != nil {
			return nil
		}
		if h.Typeflag == tar.TypeReg {
			_, _ = tw.Write([]byte("x"))
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestExtractRejectsTraversal(t *testing.T) {
	cases := map[string][]tar.Header{
		"dotdot":   {{Name: "../evil", Typeflag: tar.TypeReg, Mode: 0o644}},
		"absolute": {{Name: "/etc/evil", Typeflag: tar.TypeReg, Mode: 0o644}},
		"via-symlink": {
			{Name: "escape", Typeflag: tar.TypeSymlink, Linkname: "/tmp"},
			{Name: "escape/evil", Typeflag: tar.TypeReg, Mode: 0o644},
		},
		"hardlink-out": {{Name: "h", Typeflag: tar.TypeLink, Linkname: "../../etc/passwd"}},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			dst := filepath.Join(parent, "dst")
			if err := os.Mkdir(dst, 0o755); err != nil {
				t.Fatal(err)
			}
			_, err := Extract(bytes.NewReader(malicious(t, entries...)), dst, ExtractOptions{})
			if err == nil {
				t.Fatal("expected extraction to fail")
			}
			if _, err := os.Stat(filepath.Join(parent, "evil")); err == nil {
				t.Fatal("file written outside target")
			}
		})
	}
}

func TestExtractNotGzip(t *testing.T) {
	_, err := Extract(strings.NewReader("not gzip"), t.TempDir(), ExtractOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestWriteSkipsSpecialFiles(t *testing.T) {
	if _, err := os.Stat("/dev/null"); err != nil {
		t.Skip()
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "f"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(src, "fifo")
	if err := mkfifo(sock); err != nil {
		t.Skip("mkfifo unsupported:", err)
	}
	var buf bytes.Buffer
	st, err := Write(&buf, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Files != 1 || st.Skipped != 1 || len(st.Warnings) != 1 {
		t.Fatalf("stats %+v", st)
	}
}

// FuzzExtract feeds arbitrary tar entries to Extract: it must never panic or write outside the target.
func FuzzExtract(f *testing.F) {
	f.Add("a/b.txt", byte(tar.TypeReg), "", int64(0o644))
	f.Add("../x", byte(tar.TypeReg), "", int64(0o644))
	f.Add("l", byte(tar.TypeSymlink), "../../etc", int64(0o777))
	f.Add("h", byte(tar.TypeLink), "/etc/passwd", int64(0o644))
	f.Add("d/", byte(tar.TypeDir), "", int64(0o7777))
	f.Fuzz(func(t *testing.T, name string, typ byte, link string, mode int64) {
		if name == "" {
			return
		}
		parent := t.TempDir()
		dst := filepath.Join(parent, "dst")
		if err := os.Mkdir(dst, 0o755); err != nil {
			t.Fatal(err)
		}
		hdr := tar.Header{Name: name, Typeflag: typ, Linkname: link, Mode: mode & 0o7777}
		data := malicious(t, hdr, tar.Header{Name: name + "/f", Typeflag: tar.TypeReg, Mode: 0o644})
		_, _ = Extract(bytes.NewReader(data), dst, ExtractOptions{})
		entries, _ := os.ReadDir(parent)
		if len(entries) != 1 {
			t.Fatalf("extraction wrote outside the target: %v", entries)
		}
	})
}
