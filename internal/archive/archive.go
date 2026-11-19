// Package archive writes and extracts the tar.gz archives Bardiup stores.
// Archives are plain gzip-compressed POSIX tar files: they can be inspected and
// restored with standard tools (tar -xzf) without Bardiup.
package archive

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/klauspost/pgzip"
)

// Stats summarises an archive operation.
type Stats struct {
	Files    int64 // entries written or extracted
	Bytes    int64 // uncompressed file content bytes
	Skipped  int64 // special files, vanished files, excluded paths
	Warnings []string
}

func (s *Stats) warn(format string, args ...any) {
	if len(s.Warnings) < 20 {
		s.Warnings = append(s.Warnings, fmt.Sprintf(format, args...))
	}
}

// Excluded reports whether rel (slash separated, relative to the root) matches a pattern.
// Patterns without a slash also match the base name at any depth.
func Excluded(rel string, patterns []string) bool {
	base := path.Base(rel)
	for _, p := range patterns {
		p = strings.Trim(p, "/")
		if ok, _ := path.Match(p, rel); ok {
			return true
		}
		if !strings.Contains(p, "/") {
			if ok, _ := path.Match(p, base); ok {
				return true
			}
		}
	}
	return false
}

type inode struct{ dev, ino uint64 }

// Write archives the contents of root (not root itself) as tar.gz into w.
func Write(w io.Writer, root string, excludes []string) (*Stats, error) {
	gz, err := pgzip.NewWriterLevel(w, pgzip.DefaultCompression)
	if err != nil {
		return nil, err
	}
	tw := tar.NewWriter(gz)
	st := &Stats{}
	links := map[inode]string{}

	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				st.Skipped++
				return nil
			}
			return err
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if Excluded(rel, excludes) {
			st.Skipped++
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				st.Skipped++
				return nil
			}
			return err
		}
		return addEntry(tw, p, rel, info, links, st)
	})
	if walkErr != nil {
		return st, walkErr
	}
	if err := tw.Close(); err != nil {
		return st, err
	}
	return st, gz.Close()
}

func addEntry(tw *tar.Writer, p, rel string, info fs.FileInfo, links map[inode]string, st *Stats) error {
	mode := info.Mode()
	var target string
	switch {
	case mode.IsRegular(), mode.IsDir():
	case mode&fs.ModeSymlink != 0:
		t, err := os.Readlink(p)
		if err != nil {
			st.Skipped++
			st.warn("readlink %s: %v", rel, err)
			return nil
		}
		target = t
	default:
		st.Skipped++
		st.warn("skipping special file %s (%s)", rel, mode.Type())
		return nil
	}

	hdr, err := tar.FileInfoHeader(info, target)
	if err != nil {
		return err
	}
	hdr.Name = rel
	hdr.Uname, hdr.Gname = "", ""
	hdr.Format = tar.FormatPAX
	if mode.IsDir() {
		hdr.Name += "/"
	}

	if sys, ok := info.Sys().(*syscall.Stat_t); ok && mode.IsRegular() && sys.Nlink > 1 {
		key := inode{uint64(sys.Dev), uint64(sys.Ino)} //nolint:gosec,unconvert // Dev width differs by platform; values are non-negative
		if first, seen := links[key]; seen {
			hdr.Typeflag = tar.TypeLink
			hdr.Linkname = first
			hdr.Size = 0
			st.Files++
			return tw.WriteHeader(hdr)
		}
		links[key] = rel
	}

	if !mode.IsRegular() {
		st.Files++
		return tw.WriteHeader(hdr)
	}

	f, err := os.Open(p) //nolint:gosec // walking the volume is the point
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			st.Skipped++
			return nil
		}
		return err
	}
	defer f.Close()
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	n, err := io.CopyN(tw, f, hdr.Size)
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read %s: %w", rel, err)
	}
	if n < hdr.Size {
		// File shrank while being read (live volume). Pad to keep the archive valid.
		st.warn("%s changed during backup (%d of %d bytes read)", rel, n, hdr.Size)
		if _, err := io.CopyN(tw, zeroReader{}, hdr.Size-n); err != nil {
			return err
		}
	}
	st.Files++
	st.Bytes += hdr.Size
	return nil
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// ExtractOptions configure Extract.
type ExtractOptions struct {
	// Clean removes everything under the target before extracting.
	Clean bool
	// Chown restores numeric ownership. Requires CAP_CHOWN.
	Chown bool
}

// Extract unpacks a tar.gz stream into root. All paths are resolved inside root;
// entries that would escape it (via "..", absolute paths or symlinks) fail the extraction.
// The whole input stream is consumed so callers can verify a checksum afterwards.
func Extract(r io.Reader, root string, opts ExtractOptions) (*Stats, error) {
	rt, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer rt.Close()

	if opts.Clean {
		entries, err := os.ReadDir(root)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if err := rt.RemoveAll(e.Name()); err != nil {
				return nil, fmt.Errorf("clean %s: %w", e.Name(), err)
			}
		}
	}

	gz, err := pgzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("not a gzip stream: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	st := &Stats{}

	type dirMeta struct {
		name  string
		mtime time.Time
	}
	var dirs []dirMeta

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return st, fmt.Errorf("read archive: %w", err)
		}
		name, err := cleanName(hdr.Name)
		if err != nil {
			return st, err
		}
		if name == "." {
			continue
		}
		if dir := path.Dir(name); dir != "." {
			if err := rt.MkdirAll(dir, 0o755); err != nil {
				return st, fmt.Errorf("mkdir %s: %w", dir, err)
			}
		}
		mode := hdrMode(hdr.Mode)

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := rt.Mkdir(name, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
				return st, fmt.Errorf("mkdir %s: %w", name, err)
			}
			dirs = append(dirs, dirMeta{name, hdr.ModTime})
		case tar.TypeReg:
			if err := removeIfNotDir(rt, name); err != nil {
				return st, err
			}
			f, err := rt.OpenFile(name, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
			if err != nil {
				return st, fmt.Errorf("create %s: %w", name, err)
			}
			n, err := io.Copy(f, tr) //nolint:gosec // output is bounded by the target volume size
			cerr := f.Close()
			if err != nil {
				return st, fmt.Errorf("write %s: %w", name, err)
			}
			if cerr != nil {
				return st, cerr
			}
			st.Bytes += n
		case tar.TypeSymlink:
			if err := rt.RemoveAll(name); err != nil {
				return st, err
			}
			if err := rt.Symlink(hdr.Linkname, name); err != nil {
				return st, fmt.Errorf("symlink %s: %w", name, err)
			}
		case tar.TypeLink:
			target, err := cleanName(hdr.Linkname)
			if err != nil {
				return st, err
			}
			if err := rt.RemoveAll(name); err != nil {
				return st, err
			}
			if err := rt.Link(target, name); err != nil {
				return st, fmt.Errorf("link %s: %w", name, err)
			}
			st.Files++
			continue
		default:
			st.Skipped++
			st.warn("skipping unsupported entry %s (type %c)", name, hdr.Typeflag)
			continue
		}
		st.Files++

		if opts.Chown {
			if err := rt.Lchown(name, hdr.Uid, hdr.Gid); err != nil {
				st.warn("chown %s: %v", name, err)
			}
		}
		if hdr.Typeflag != tar.TypeSymlink {
			if err := rt.Chmod(name, mode); err != nil {
				st.warn("chmod %s: %v", name, err)
			}
			if hdr.Typeflag == tar.TypeReg {
				if err := rt.Chtimes(name, hdr.ModTime, hdr.ModTime); err != nil {
					st.warn("chtimes %s: %v", name, err)
				}
			}
		}
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = rt.Chtimes(dirs[i].name, dirs[i].mtime, dirs[i].mtime)
	}
	// Drain gzip trailer and any padding so the caller's hash covers the full object.
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return st, fmt.Errorf("read archive trailer: %w", err)
	}
	if _, err := io.Copy(io.Discard, r); err != nil {
		return st, err
	}
	return st, nil
}

func hdrMode(m int64) fs.FileMode {
	mode := fs.FileMode(m) & fs.ModePerm //nolint:gosec // only permission bits are kept
	if m&0o4000 != 0 {
		mode |= fs.ModeSetuid
	}
	if m&0o2000 != 0 {
		mode |= fs.ModeSetgid
	}
	if m&0o1000 != 0 {
		mode |= fs.ModeSticky
	}
	return mode
}

func removeIfNotDir(rt *os.Root, name string) error {
	fi, err := rt.Lstat(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if fi.Mode().IsRegular() {
		return nil
	}
	return rt.RemoveAll(name)
}

func cleanName(name string) (string, error) {
	n := path.Clean(strings.TrimPrefix(name, "./"))
	if path.IsAbs(n) || n == ".." || strings.HasPrefix(n, "../") {
		return "", fmt.Errorf("archive entry %q escapes the target directory", name)
	}
	return n, nil
}
