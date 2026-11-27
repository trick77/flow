package supervisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/layout"
)

// The diff covers commits on the branch, uncommitted edits and untracked files against the base the worktree was
// cut from, summarizes a binary file, leaves ignored files out and never touches the agent's own index.
func TestDiff(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.api.EnsureSession(ctx, sessionSpec("s1", "demo", "trick77/toolkit")); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(r.root, layout.Worktree("demo", "toolkit"))
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(wt, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("committed.txt", "one\ntwo\n")
	gitRun(t, wt, "add", "committed.txt")
	gitRun(t, wt, "commit", "--quiet", "-m", "work")
	write("README", "changed\n")
	write("new.txt", "fresh\n")
	write("blob.bin", "\x00\x01\x02binary")
	write(".gitignore", "ignored.log\n")
	write("ignored.log", "noise\n")
	write("staged.txt", "staged\n")
	gitRun(t, wt, "add", "staged.txt")
	before := gitOut(t, wt, "status", "--porcelain")

	d, err := r.api.Diff(ctx, "s1", "trick77/toolkit")
	if err != nil {
		t.Fatal(err)
	}
	if d.Repo != "trick77/toolkit" || d.BaseRef != "origin/master" || len(d.BaseSHA) != 40 || d.Truncated {
		t.Errorf("diff header %+v", d)
	}
	byPath := map[string]apitypes.DiffFile{}
	for _, f := range d.Files {
		byPath[f.Path] = f
	}
	if f := byPath["README"]; f.Change != apitypes.FileChangeKindModified || f.Added != 1 || f.Removed != 1 ||
		len(f.Hunks) != 1 || !strings.Contains(strings.Join(f.Hunks[0].Lines, "\n"), "+changed") {
		t.Errorf("README %+v", f)
	}
	if f := byPath["committed.txt"]; f.Change != apitypes.FileChangeKindAdded || f.Added != 2 || len(f.Hunks) != 1 ||
		f.Hunks[0].NewStart != 1 || f.Hunks[0].NewLines != 2 {
		t.Errorf("committed.txt %+v", f)
	}
	for _, p := range []string{"new.txt", "staged.txt", ".gitignore"} {
		if f := byPath[p]; f.Change != apitypes.FileChangeKindAdded {
			t.Errorf("%s %+v", p, f)
		}
	}
	if f := byPath["blob.bin"]; !f.Binary || len(f.Hunks) != 0 {
		t.Errorf("blob.bin %+v", f)
	}
	if _, ok := byPath["ignored.log"]; ok {
		t.Error("an ignored file is in the diff")
	}
	if after := gitOut(t, wt, "status", "--porcelain"); after != before {
		t.Errorf("the worktree's index changed:\n%s\nwas\n%s", after, before)
	}
	if left, _ := filepath.Glob(filepath.Join(r.root, "home", "flow", "work", "toolkit", ".git", "worktrees", "*", "flow-diff-*")); len(left) != 0 {
		t.Errorf("temporary index left: %v", left)
	}
	if _, err := r.api.Diff(ctx, "s1", "trick77/ship"); !errors.Is(err, flowdproto.ErrNotFound) {
		t.Errorf("diff of a repository the session lacks: %v", err)
	}

	// A base file turned symlink: git patches the type change as two sections under one name-status entry, which
	// must not shift the hunks of the files after it. The base is moved to HEAD for this (the spec's baseSha).
	gitRun(t, wt, "add", "-A")
	gitRun(t, wt, "commit", "--quiet", "-m", "all")
	s := New(Options{Root: r.root})
	sp, err := s.readSpec("s1")
	if err != nil {
		t.Fatal(err)
	}
	sp.Worktrees[0].BaseSHA = gitOut(t, wt, "rev-parse", "HEAD")
	if err := s.writeSpec(sp); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(wt, "new.txt"))
	if err := os.Symlink("README", filepath.Join(wt, "new.txt")); err != nil {
		t.Fatal(err)
	}
	write("zz-last.txt", "last\n")
	d, err = r.api.Diff(ctx, "s1", "trick77/toolkit")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Files) != 2 || d.Files[0].Path != "new.txt" || d.Files[1].Path != "zz-last.txt" ||
		len(d.Files[1].Hunks) != 1 || strings.Join(d.Files[1].Hunks[0].Lines, ",") != "+last" {
		t.Errorf("after a type change: %+v", d.Files)
	}
}

// Large diffs are bounded: a file past the line cap keeps its counts and is marked truncated.
func TestApplyPatch_bounds(t *testing.T) {
	files := []apitypes.DiffFile{{Path: "a", Hunks: []apitypes.DiffHunk{}}, {Path: "b", Hunks: []apitypes.DiffHunk{}}}
	var p strings.Builder
	p.WriteString("diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1,0 +1,6000 @@\n")
	for range 6000 {
		p.WriteString("+x\n")
	}
	p.WriteString("diff --git a/b b/b\n--- a/b\n+++ b/b\n@@ -1 +1 @@ ctx\n-old\n+new\n")
	applyPatch(files, []int{1, 1}, []byte(p.String()), false)
	if !files[0].Truncated || len(files[0].Hunks[0].Lines) != diffMaxFileLines {
		t.Errorf("a: truncated %v, %d lines", files[0].Truncated, len(files[0].Hunks[0].Lines))
	}
	if files[1].Truncated || len(files[1].Hunks) != 1 || files[1].Hunks[0].OldLines != 1 ||
		strings.Join(files[1].Hunks[0].Lines, ",") != "-old,+new" {
		t.Errorf("b %+v", files[1])
	}
	files = []apitypes.DiffFile{{Path: "a", Hunks: []apitypes.DiffHunk{}}, {Path: "b", Hunks: []apitypes.DiffHunk{}}}
	applyPatch(files, []int{1, 1}, []byte("diff --git a/a b/a\n@@ -1 +1 @@\n-x\n+y\n"), true)
	if files[0].Truncated == false || files[1].Truncated == false {
		t.Errorf("a cut patch left files unmarked: %+v", files)
	}
}
