package supervisor

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/trick77/flow/internal/layout"
)

// SettingsLayer is the Flow settings layer for Claude Code (layout.Settings, the SDK's settings option): Claude
// Code's own sandbox off, since the Flow sandbox is the boundary, and nothing that changes per session (a stable
// layer keeps the prompt cache). docs/architecture.md, "Sandbox layout".
const SettingsLayer = `{"sandbox": {"enabled": false}}` + "\n"

// Bounds on an unpacked sidecar bundle: the real one is about 66 MB in a few thousand files.
const (
	maxBundleBytes   = 1 << 30
	maxBundleEntries = 100_000
)

// prepare makes the sandbox ready for sessions before flowd answers: the config holding the token root-only, the
// settings layer readable by the agent, the release's sidecar bundle unpacked, the home volume's directories. Only
// the config's mode is fatal: the rest is logged, and a session that needs it fails visibly at its start.
func (s *Supervisor) prepare(configPath string) error {
	if err := rootOnly(configPath); err != nil {
		return fmt.Errorf("%s: %w", configPath, err)
	}
	if err := s.writeSettings(); err != nil {
		s.o.Log.Error("writing the Claude settings layer", "path", layout.Settings, "err", err)
	}
	if err := s.unpackSidecar(); err != nil {
		s.o.Log.Error("unpacking the sidecar bundle", "release", filepath.Dir(s.o.Exe), "err", err)
	}
	s.homeDirs()
	return nil
}

// rootOnly makes the config 0600 (and root's, when flowd is root): it holds the token Flow authenticates with.
func rootOnly(p string) error {
	if os.Geteuid() == 0 {
		if err := os.Lchown(p, 0, 0); err != nil {
			return err
		}
	}
	return os.Chmod(p, 0o600)
}

// writeSettings writes SettingsLayer root-owned 0644 and keeps its directory 0755: the CLI runs as the agent user
// and must read it (a minimal image ships /etc/flow 0700; the token file in it stays 0600).
func (s *Supervisor) writeSettings() error {
	p := s.p(layout.Settings)
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // the agent must read the settings layer
		return err
	}
	if err := os.Chmod(dir, 0o755); err != nil { //nolint:gosec // see above
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(SettingsLayer), 0o644); err != nil { //nolint:gosec // readable on purpose
		return err
	}
	if err := os.Chmod(tmp, 0o644); err != nil { //nolint:gosec // umask-proof, see above
		return err
	}
	return os.Rename(tmp, p)
}

// homeDirs makes layout.HomeDirs agent-owned, idempotently: a missing one is created, one owned by someone else is
// handed to the agent (never recursively), anything that is not a plain directory is left alone and logged.
func (s *Supervisor) homeDirs() {
	if err := os.MkdirAll(s.p(layout.HomeVolume), 0o755); err != nil { //nolint:gosec // traversed by the agent
		s.o.Log.Error("home volume", "path", layout.HomeVolume, "err", err)
		return
	}
	for _, d := range layout.HomeDirs {
		if err := s.homeDir(s.p(d.Path), fs.FileMode(d.Mode)); err != nil {
			s.o.Log.Error("home volume directory", "path", d.Path, "err", err)
		}
	}
}

func (s *Supervisor) homeDir(p string, mode fs.FileMode) error {
	fi, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(p, mode); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if err := os.Chmod(p, mode); err != nil { // umask-proof
			return err
		}
		fi, err = os.Lstat(p)
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("not a directory (%s), left as it is", fi.Mode().Type())
	}
	if os.Geteuid() != 0 {
		return nil
	}
	if owner(fi) == layout.AgentUID {
		return nil
	}
	return os.Lchown(p, layout.AgentUID, layout.AgentGID)
}

// owner is a file's uid, -1 when the platform does not say.
func owner(fi fs.FileInfo) int {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid)
	}
	return -1
}

// unpackSidecar unpacks the release's sidecar bundle (layout.SidecarArchive, pushed by Flow beside flowd) into
// layout.SidecarDir, once: into a temporary directory renamed into place when complete, so a release never holds a
// half bundle and an existing one is never overwritten. The archive is removed afterwards.
func (s *Supervisor) unpackSidecar() error {
	if s.o.Exe == "" {
		return nil
	}
	release := filepath.Dir(s.o.Exe)
	archive := filepath.Join(release, layout.SidecarArchive)
	dst := filepath.Join(release, layout.SidecarDir)
	if _, err := os.Stat(archive); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := os.Stat(dst); err == nil {
		return os.Remove(archive)
	}
	tmp := filepath.Join(release, "."+layout.SidecarDir+".tmp")
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	if err := untar(archive, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	s.o.Log.Info("sidecar bundle unpacked", "dir", dst)
	return os.Remove(archive)
}

// untar extracts a gzip'd tar of regular files, directories and relative symlinks that stay inside dst
// (node_modules/.bin); anything else, a path leaving dst or a bundle past the bounds is refused. Symlinks are made
// last, so no file is ever written through one.
func untar(archive, dst string) error {
	links, err := extract(archive, dst)
	if err != nil {
		return err
	}
	root, err := filepath.EvalSymlinks(dst)
	if err != nil {
		return err
	}
	for _, l := range links {
		parent, err := filepath.EvalSymlinks(filepath.Join(dst, filepath.FromSlash(path.Dir(l[0]))))
		if err != nil {
			return err
		}
		if !inside(root, parent) || !inside(root, filepath.Join(parent, filepath.FromSlash(l[1]))) {
			return fmt.Errorf("bundle symlink %q -> %q leaves the bundle", l[0], l[1])
		}
		if err := os.Symlink(l[1], filepath.Join(parent, path.Base(l[0]))); err != nil {
			return err
		}
	}
	return nil
}

func inside(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// extract writes the directories and regular files and returns the symlinks (name, target) for untar.
func extract(archive, dst string) ([][2]string, error) {
	f, err := os.Open(archive) //nolint:gosec // the release's own file, named by layout
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	if err := os.Mkdir(dst, 0o755); err != nil { //nolint:gosec // the bundle is read by the agent user
		return nil, err
	}
	tr := tar.NewReader(zr)
	var total int64
	var links [][2]string
	for n := 0; ; n++ {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return links, nil
		}
		if err != nil {
			return nil, err
		}
		if n >= maxBundleEntries {
			return nil, errors.New("bundle has too many entries")
		}
		name := path.Clean(strings.TrimPrefix(h.Name, "./"))
		if name == "." {
			continue
		}
		if !fs.ValidPath(name) {
			return nil, fmt.Errorf("bundle entry %q leaves the bundle", h.Name)
		}
		target := filepath.Join(dst, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil { //nolint:gosec // see Mkdir above
				return nil, err
			}
		case tar.TypeReg:
			if total += h.Size; total > maxBundleBytes {
				return nil, errors.New("bundle too large")
			}
			if err := writeEntry(target, tr, h); err != nil {
				return nil, err
			}
		case tar.TypeSymlink:
			if path.IsAbs(h.Linkname) || h.Linkname == "" {
				return nil, fmt.Errorf("bundle symlink %q -> %q leaves the bundle", h.Name, h.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil { //nolint:gosec // see Mkdir above
				return nil, err
			}
			links = append(links, [2]string{name, h.Linkname})
		default:
			return nil, fmt.Errorf("bundle entry %q: type %c not allowed", h.Name, h.Typeflag)
		}
	}
}

// writeEntry writes one regular file: executable bits kept, everything else 0644 (root's, readable by the agent).
func writeEntry(target string, r io.Reader, h *tar.Header) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil { //nolint:gosec // see untar
		return err
	}
	mode := fs.FileMode(0o644)
	if h.Mode&0o111 != 0 {
		mode = 0o755
	}
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode) //nolint:gosec // inside dst, checked
	if err != nil {
		return err
	}
	if _, err := io.CopyN(out, r, h.Size); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
