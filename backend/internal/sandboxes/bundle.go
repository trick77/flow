package sandboxes

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Bundle packs the sidecar directory as the server image carries it (package.json, dist/, the production
// node_modules) into the archive Flow delivers beside flowd (layout.SidecarArchive). The same files always give the
// same bytes (lexical order, no times, owners or modes beyond the executable bit), so the release's hash stays the
// same across Flow restarts and only a changed bundle is delivered again. A missing directory is no bundle (nil):
// replay sessions still run, a Claude session's start fails visibly in the sandbox. dir itself may be a symlink
// (WalkDir does not follow a root link: it would pack nothing).
func Bundle(dir string) ([]byte, error) {
	if _, err := os.Stat(filepath.Join(dir, "package.json")); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	files := 0
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.DefaultCompression)
	if err != nil {
		return nil, err
	}
	tw := tar.NewWriter(zw)
	epoch := time.Unix(0, 0).UTC()
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		name := filepath.ToSlash(rel)
		h := &tar.Header{Name: name, ModTime: epoch, Format: tar.FormatPAX}
		switch t := d.Type(); {
		case t.IsDir():
			h.Typeflag, h.Name, h.Mode = tar.TypeDir, name+"/", 0o755
			return tw.WriteHeader(h)
		case t&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			h.Typeflag, h.Linkname, h.Mode = tar.TypeSymlink, target, 0o777
			return tw.WriteHeader(h)
		case t.IsRegular():
			fi, err := d.Info()
			if err != nil {
				return err
			}
			h.Typeflag, h.Size, h.Mode = tar.TypeReg, fi.Size(), 0o644
			if fi.Mode()&0o111 != 0 {
				h.Mode = 0o755
			}
			if err := tw.WriteHeader(h); err != nil {
				return err
			}
			files++
			return copyFile(tw, p)
		default:
			return fmt.Errorf("sidecar bundle: %s is neither file, directory nor symlink", name)
		}
	})
	if err != nil {
		return nil, err
	}
	if files == 0 { // package.json was seen above: a bundle without it would be delivered and fail in the sandbox
		return nil, fmt.Errorf("sidecar bundle: no files packed from %s", dir)
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func copyFile(w io.Writer, p string) error {
	f, err := os.Open(p) //nolint:gosec // a file of the configured sidecar directory
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = io.Copy(w, f)
	return err
}
