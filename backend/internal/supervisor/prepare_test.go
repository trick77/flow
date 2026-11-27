package supervisor

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/layout"
)

// The token file ends root-only whatever mode it arrived with; the settings layer is exactly the documented one and
// readable by the agent, also inside a directory a minimal image ships 0700; the home volume's directories exist.
func TestPrepare_settingsAndToken(t *testing.T) {
	root := t.TempDir()
	etc := filepath.Join(root, "etc", "flow")
	if err := os.MkdirAll(etc, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(etc, "flowd.json")
	if err := os.WriteFile(cfg, []byte(`{"token":"tok"}`), 0o644); err != nil { //nolint:gosec // the test's point
		t.Fatal(err)
	}
	logged := &lockedBuffer{}
	s := New(Options{Root: root, Log: slog.New(slog.NewTextHandler(logged, nil))})
	for range 2 { // idempotent
		if err := s.prepare(cfg); err != nil {
			t.Fatal(err)
		}
	}
	mode := func(p string) os.FileMode {
		t.Helper()
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		return fi.Mode().Perm()
	}
	if m := mode(cfg); m != 0o600 {
		t.Errorf("token file %v, want 0600", m)
	}
	settings := root + layout.Settings
	if m := mode(settings); m != 0o644 {
		t.Errorf("settings %v, want 0644", m)
	}
	if m := mode(etc); m != 0o755 {
		t.Errorf("/etc/flow %v, want 0755", m)
	}
	if b, _ := os.ReadFile(settings); string(b) != `{"sandbox": {"enabled": false}}`+"\n" { //nolint:gosec // test path
		t.Errorf("settings %q", b)
	}
	for _, d := range layout.HomeDirs {
		if m := mode(root + d.Path); m != os.FileMode(d.Mode) {
			t.Errorf("%s: %v, want %v", d.Path, m, os.FileMode(d.Mode))
		}
	}
	if logged.String() != "" {
		t.Errorf("logged %s", logged)
	}
	if err := s.prepare(filepath.Join(root, "missing.json")); err == nil {
		t.Error("prepare without a config succeeded")
	}
}

// Something that is not a plain directory where a home directory belongs is left alone, never followed.
func TestHomeDirs_leaveForeignEntries(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "elsewhere")
	if err := os.MkdirAll(filepath.Dir(root+layout.XDGData), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, root+layout.XDGData); err != nil {
		t.Fatal(err)
	}
	logged := &lockedBuffer{}
	s := New(Options{Root: root, Log: slog.New(slog.NewTextHandler(logged, nil))})
	if err := s.PutEnv(context.Background(), flowdproto.Env{Revision: 1}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(root + layout.XDGData); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the symlink was replaced: %v", err)
	}
	if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o700 {
		t.Errorf("the symlink's target was changed: %v", fi.Mode())
	}
	if _, err := os.Stat(root + layout.NPMCache); err != nil {
		t.Errorf("the other directories were not made: %v", err)
	}
	if !bytes.Contains([]byte(logged.String()), []byte("not a directory")) {
		t.Errorf("not logged: %s", logged)
	}
}

type entry struct {
	name, body, link string
	typ              byte
	mode             int64
}

func archive(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: e.mode, Linkname: e.link, Size: int64(len(e.body))}
		if e.typ != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.typ == tar.TypeReg {
			_, _ = tw.Write([]byte(e.body))
		}
	}
	_ = tw.Close()
	_ = zw.Close()
	return buf.Bytes()
}

// release makes a release directory with a flowd and the archive, and a supervisor running from it.
func release(t *testing.T, bundle []byte) (*Supervisor, string) {
	t.Helper()
	root := t.TempDir()
	dir := root + layout.Release("1.0-abc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "flowd"), []byte("bin"), 0o755) //nolint:gosec // an executable
	if bundle != nil {
		_ = os.WriteFile(filepath.Join(dir, layout.SidecarArchive), bundle, 0o600)
	}
	return New(Options{Root: root, Exe: filepath.Join(dir, "flowd"), Log: slog.New(slog.DiscardHandler)}), dir
}

// The bundle Flow pushes is unpacked once into the release, beside flowd, and its archive removed; a bundle
// pushed into a release that has one never replaces it.
func TestUnpackSidecar(t *testing.T) {
	s, dir := release(t, archive(t,
		entry{name: "package.json", body: `{"type":"module"}`, typ: tar.TypeReg, mode: 0o644},
		entry{name: "dist/", typ: tar.TypeDir, mode: 0o755},
		entry{name: "dist/main.js", body: "main", typ: tar.TypeReg, mode: 0o644},
		entry{name: "node_modules/pkg/cli.js", body: "cli", typ: tar.TypeReg, mode: 0o755},
		entry{name: "node_modules/.bin/cli", link: "../pkg/cli.js", typ: tar.TypeSymlink}))
	if err := s.unpackSidecar(); err != nil {
		t.Fatal(err)
	}
	side := filepath.Join(dir, layout.SidecarDir)
	if b, _ := os.ReadFile(filepath.Join(side, "dist", "main.js")); string(b) != "main" { //nolint:gosec // test path
		t.Errorf("main.js %q", b)
	}
	if fi, err := os.Stat(filepath.Join(side, "node_modules", "pkg", "cli.js")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("executable: %v %v", fi, err)
	}
	if b, err := os.ReadFile(filepath.Join(side, "node_modules", ".bin", "cli")); err != nil || string(b) != "cli" { //nolint:gosec // test path
		t.Errorf("symlink: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dir, layout.SidecarArchive)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("archive left: %v", err)
	}
	if s.release() != "1.0-abc" {
		t.Errorf("release = %q", s.release())
	}
	// Pushed again into a release that has its bundle: dropped, the bundle unchanged.
	_ = os.WriteFile(filepath.Join(dir, layout.SidecarArchive), archive(t,
		entry{name: "dist/main.js", body: "other", typ: tar.TypeReg, mode: 0o644}), 0o600)
	if err := s.unpackSidecar(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(side, "dist", "main.js")); string(b) != "main" { //nolint:gosec // test path
		t.Errorf("an existing bundle was overwritten: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, layout.SidecarArchive)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("archive left: %v", err)
	}
}

// An archive that would write outside its bundle, or holds what a bundle never has, leaves no bundle behind.
func TestUnpackSidecar_refusals(t *testing.T) {
	for name, entries := range map[string][]entry{
		"parent path":      {{name: "../evil", body: "x", typ: tar.TypeReg, mode: 0o644}},
		"absolute symlink": {{name: "l", link: "/etc/passwd", typ: tar.TypeSymlink}},
		"escaping symlink": {{name: "a/l", link: "../../x", typ: tar.TypeSymlink}},
		// d -> . makes d/l's ../x point beside the bundle, which a lexical check of d/l alone misses.
		"through a symlink": {{name: "d", link: ".", typ: tar.TypeSymlink}, {name: "d/l", link: "../x", typ: tar.TypeSymlink}},
		"hard link":         {{name: "f", body: "x", typ: tar.TypeReg, mode: 0o644}, {name: "h", link: "f", typ: tar.TypeLink}},
		"device":            {{name: "dev", typ: tar.TypeChar}},
	} {
		s, dir := release(t, archive(t, entries...))
		if err := s.unpackSidecar(); err == nil {
			t.Errorf("%s: unpacked", name)
		}
		for _, p := range []string{layout.SidecarDir, "." + layout.SidecarDir + ".tmp"} {
			if _, err := os.Lstat(filepath.Join(dir, p)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%s: %s left (%v)", name, p, err)
			}
		}
		if _, err := os.Lstat(filepath.Join(filepath.Dir(dir), "x")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: wrote beside the release", name)
		}
	}
	// No archive: nothing to do.
	s, _ := release(t, nil)
	if err := s.unpackSidecar(); err != nil {
		t.Error(err)
	}
}
