package supervisor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/flowdproto"
)

// Diff bounds: the files listed, the bytes of the file lists and of the whole patch read from git, the lines kept per
// file. A file past the patch budget or its line cap is listed with its counts and truncated set, without (all) hunks;
// files past diffMaxFiles are left out and the diff is truncated.
const (
	diffMaxFiles     = 2000
	diffMaxListBytes = 4 << 20
	diffMaxPatch     = 4 << 20
	diffMaxFileLines = 5000
	diffWait         = 2 * time.Minute
)

// Diff implements flowdproto.API: the worktree of repo (owner/name) against the base commit it was cut from
// (the spec's baseSha), committed and uncommitted changes and untracked files (.gitignore respected), as git sees
// them now. Untracked files are taken in as intent-to-add entries of a temporary index beside the worktree's own,
// never the real one: the agent's staging stays as it is. Output from git is read bounded (see the limits above).
func (s *Supervisor) Diff(ctx context.Context, id, repo string) (apitypes.Diff, error) {
	sp, err := s.readSpec(id)
	if err != nil {
		return apitypes.Diff{}, err
	}
	var w *flowdproto.WorktreeState
	for i := range sp.Worktrees {
		if sp.Worktrees[i].Repo == repo {
			w = &sp.Worktrees[i]
		}
	}
	if w == nil {
		return apitypes.Diff{}, fmt.Errorf("%w: worktree of %s in session %s", flowdproto.ErrNotFound, repo, id)
	}
	dir := s.p(w.Path)
	if _, err := os.Stat(dir); err != nil {
		return apitypes.Diff{}, fmt.Errorf("%w: worktree %s: %w", flowdproto.ErrNotFound, w.Path, err)
	}
	ctx, cancel := context.WithTimeout(ctx, diffWait)
	defer cancel()
	base := w.BaseSHA
	if base == "" {
		if base, err = s.git(ctx, dir, "rev-parse", "--verify", w.BaseRef+"^{commit}"); err != nil {
			return apitypes.Diff{}, err
		}
	}
	index, err := s.git(ctx, dir, "rev-parse", "--git-path", "index")
	if err != nil {
		return apitypes.Diff{}, err
	}
	if !filepath.IsAbs(index) {
		index = filepath.Join(dir, index)
	}
	tmp := filepath.Join(filepath.Dir(index), "flow-diff-"+randomHex()+".index")
	defer func() { _ = os.Remove(tmp); _ = os.Remove(tmp + ".lock") }()
	env := []string{"GIT_INDEX_FILE=" + tmp, "GIT_OPTIONAL_LOCKS=0"}
	run := func(limit int, args ...string) ([]byte, error) {
		return s.o.Run.Run(ctx, Cmd{Name: "git", Args: append([]string{"-c", "core.quotePath=false"}, args...), Dir: dir,
			AsAgent: true, Env: append([]string{"GIT_TERMINAL_PROMPT=0"}, env...), Limit: limit})
	}
	// A copy of the real index keeps its stat cache (only changed files are hashed again); without one, HEAD's tree.
	if _, err := os.Stat(index); err == nil {
		if _, err := s.o.Run.Run(ctx, Cmd{Name: "cp", Args: []string{index, tmp}, AsAgent: true}); err != nil {
			return apitypes.Diff{}, err
		}
	} else if _, err := run(1<<10, "read-tree", "HEAD"); err != nil {
		return apitypes.Diff{}, err
	}
	// Intent-to-add only: untracked files become known paths without a blob written for each (the working tree is
	// diffed, not the index), so a request adds nothing to the object store.
	if _, err := run(64<<10, "add", "--all", "--intent-to-add", "--", "."); err != nil {
		return apitypes.Diff{}, err
	}
	diffArgs := []string{"diff", "-M", "--no-color", "--no-ext-diff", "--no-textconv"}
	names, err := run(diffMaxListBytes, append(diffArgs, "--name-status", "-z", base, "--")...)
	if err != nil {
		return apitypes.Diff{}, err
	}
	counts, err := run(diffMaxListBytes, append(diffArgs, "--numstat", "-z", base, "--")...)
	if err != nil {
		return apitypes.Diff{}, err
	}
	patch, err := run(diffMaxPatch, append(diffArgs, "--unified=3", base, "--")...)
	if err != nil {
		return apitypes.Diff{}, err
	}
	out := apitypes.Diff{Repo: w.Repo, BaseRef: w.BaseRef, BaseSHA: base, Files: []apitypes.DiffFile{}}
	files, sections, cut := parseNameStatus(names)
	out.Truncated = cut || len(files) > diffMaxFiles
	if len(files) > diffMaxFiles {
		files = files[:diffMaxFiles]
	}
	applyNumstat(files, counts)
	applyPatch(files, sections, patch, len(patch) > diffMaxPatch)
	out.Files = files
	return out, nil
}

func randomHex() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// parseNameStatus reads `git diff --name-status -z`: a status, then one path (two for a rename or copy). sections
// is how many "diff --git" sections each file has in the patch: two for a type change (a file turned symlink, or
// back), which git patches as a deletion and an addition (measured), one otherwise. cut: the output was longer than
// its bound, so the last entries are missing.
func parseNameStatus(b []byte) ([]apitypes.DiffFile, []int, bool) {
	cut := len(b) > diffMaxListBytes
	f := bytes.Split(bytes.TrimSuffix(b[:min(len(b), diffMaxListBytes)], []byte{0}), []byte{0})
	var out []apitypes.DiffFile
	var sections []int
	for i := 0; i < len(f) && len(f[i]) > 0; {
		status := string(f[i])
		df := apitypes.DiffFile{Hunks: []apitypes.DiffHunk{}}
		n := 1
		if status[0] == 'T' {
			n = 2
		}
		switch status[0] {
		case 'R', 'C':
			if i+2 >= len(f) {
				return out, sections, true
			}
			df.OldPath, df.Path = string(f[i+1]), string(f[i+2])
			df.Change = apitypes.FileChangeKindRenamed
			if status[0] == 'C' {
				df.Change, df.OldPath = apitypes.FileChangeKindAdded, ""
			}
			i += 3
		default:
			if i+1 >= len(f) {
				return out, sections, true
			}
			df.Path = string(f[i+1])
			df.Change = map[byte]apitypes.FileChangeKind{'A': apitypes.FileChangeKindAdded,
				'D': apitypes.FileChangeKindDeleted}[status[0]]
			if df.Change == "" {
				df.Change = apitypes.FileChangeKindModified
			}
			i += 2
		}
		out = append(out, df)
		sections = append(sections, n)
	}
	return out, sections, cut
}

// applyNumstat sets added/removed and binary from `git diff --numstat -z`, in the same order as the name list:
// "added\tremoved\tpath\0", or "added\tremoved\t\0old\0new\0" for a rename; "-" counts are a binary file.
func applyNumstat(files []apitypes.DiffFile, b []byte) {
	f := bytes.Split(b[:min(len(b), diffMaxListBytes)], []byte{0})
	n := 0
	for i := 0; i < len(f) && n < len(files); i++ {
		parts := strings.SplitN(string(f[i]), "\t", 3)
		if len(parts) != 3 {
			continue
		}
		if parts[0] == "-" && parts[1] == "-" {
			files[n].Binary = true
		} else {
			files[n].Added, _ = strconv.Atoi(parts[0])
			files[n].Removed, _ = strconv.Atoi(parts[1])
		}
		if parts[2] == "" {
			i += 2 // a rename: old and new path follow
		}
		n++
	}
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// applyPatch fills the hunks from the unified patch, its "diff --git" sections taken in the list's order, sections[i]
// of them for file i. cut: the patch was longer than its bound; the file it ended in and every later one are
// truncated (counts kept, hunks not).
func applyPatch(files []apitypes.DiffFile, sections []int, patch []byte, cut bool) {
	if cut {
		patch = patch[:min(len(patch), diffMaxPatch)]
		if i := bytes.LastIndexByte(patch, '\n'); i >= 0 {
			patch = patch[:i+1] // a line cut midway is no line
		}
	}
	n, left := -1, 0
	var lines int
	var hunk *apitypes.DiffHunk
	for line := range strings.Lines(string(patch)) {
		line = strings.TrimSuffix(line, "\n")
		switch {
		case strings.HasPrefix(line, "diff --git "):
			hunk = nil
			if left--; left <= 0 {
				n++
				lines, left = 0, 1
				if n < len(sections) {
					left = sections[n]
				}
			}
			continue
		case n < 0 || n >= len(files):
			continue
		}
		f := &files[n]
		if m := hunkHeader.FindStringSubmatch(line); m != nil {
			if f.Truncated {
				continue
			}
			files[n].Hunks = append(f.Hunks, apitypes.DiffHunk{Header: line, OldStart: atoi(m[1]), OldLines: count(m[2]),
				NewStart: atoi(m[3]), NewLines: count(m[4]), Lines: []string{}})
			hunk = &files[n].Hunks[len(files[n].Hunks)-1]
			continue
		}
		if hunk == nil || f.Truncated || line == "" || !strings.ContainsRune(" +-\\", rune(line[0])) {
			continue
		}
		if lines >= diffMaxFileLines {
			f.Truncated = true
			continue
		}
		hunk.Lines = append(hunk.Lines, line)
		lines++
	}
	if cut {
		for i := max(n, 0); i < len(files); i++ {
			if !files[i].Binary {
				files[i].Truncated = true
			}
		}
	}
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// count is a hunk header's line count: absent means 1.
func count(s string) int {
	if s == "" {
		return 1
	}
	return atoi(s)
}
