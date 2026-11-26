package sandboxfake

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/trick77/flow/internal/sandbox"
)

func TestLifecycle(t *testing.T) {
	ctx := context.Background()
	r := New()
	if err := r.Create(ctx, sandbox.Spec{Name: "base", Image: "flow-base", Limits: sandbox.Limits{CPUs: 2}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Create(ctx, sandbox.Spec{Name: "base"}); !errors.Is(err, sandbox.ErrExists) {
		t.Errorf("second create: %v", err)
	}
	if err := r.Push(ctx, "base", sandbox.File{Path: "/x"}); !errors.Is(err, sandbox.ErrState) {
		t.Errorf("push into a stopped instance: %v", err)
	}
	if err := r.EnsureVolume(ctx, sandbox.Volume{Name: "home"}); err != nil {
		t.Fatal(err)
	}
	if err := r.EnsureVolume(ctx, sandbox.Volume{Name: "home", Shifted: true}); err != nil {
		t.Fatal(err)
	}
	m := sandbox.Mount{Volume: "home", Path: "/srv/flow-home"}
	for range 2 {
		if err := r.Attach(ctx, "base", m); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Attach(ctx, "base", sandbox.Mount{Volume: "nope"}); !errors.Is(err, sandbox.ErrNotFound) {
		t.Errorf("attach a missing volume: %v", err)
	}
	if err := r.Clone(ctx, "base", "p1"); err != nil {
		t.Fatal(err)
	}
	if err := r.Clone(ctx, "base", "p1"); !errors.Is(err, sandbox.ErrExists) {
		t.Errorf("clone onto an existing name: %v", err)
	}
	if err := r.Clone(ctx, "nope", "p2"); !errors.Is(err, sandbox.ErrNotFound) {
		t.Errorf("clone of a missing instance: %v", err)
	}
	for range 2 {
		if err := r.Start(ctx, "p1"); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.SetLimits(ctx, "p1", sandbox.Limits{CPUs: 6, MemoryBytes: 4 << 30}); err != nil {
		t.Fatal(err)
	}
	if err := r.Push(ctx, "p1", sandbox.File{Path: "/opt/flow/bin/flowd", Content: []byte("elf"), Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	in, ok := r.Instance("p1")
	if !ok || len(in.Mounts) != 1 || in.Limits.CPUs != 6 || string(in.Files["/opt/flow/bin/flowd"].Content) != "elf" {
		t.Errorf("instance = %+v", in)
	}
	st, err := r.State(ctx, "p1")
	if err != nil || st.Status != sandbox.StatusRunning || st.IPv4 == "" || st.Arch != "amd64" || st.MemoryBytes == 0 {
		t.Errorf("state = %+v, %v", st, err)
	}
	if err := r.Delete(ctx, "p1"); !errors.Is(err, sandbox.ErrState) {
		t.Errorf("delete running: %v", err)
	}
	if err := r.Stop(ctx, "p1", time.Second); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	list, _ := r.List(ctx)
	if len(list) != 1 || list[0].Name != "base" || list[0].IPv4 != "" {
		t.Errorf("list = %+v", list)
	}
	if _, ok := r.Instance("p1"); ok {
		t.Error("deleted instance still there")
	}
}

func TestExec(t *testing.T) {
	ctx := context.Background()
	r := New()
	_ = r.Create(ctx, sandbox.Spec{Name: "a"})
	if _, err := r.Exec(ctx, "a", sandbox.Command{Args: []string{"true"}}); !errors.Is(err, sandbox.ErrState) {
		t.Errorf("exec in a stopped instance: %v", err)
	}
	_ = r.Start(ctx, "a")
	if res, err := r.Exec(ctx, "a", sandbox.Command{Args: []string{"true"}}); err != nil || res.ExitCode != 0 {
		t.Errorf("default exec = %+v, %v", res, err)
	}
	r.ExecFunc = func(_ string, c sandbox.Command) (sandbox.Result, error) {
		return sandbox.Result{ExitCode: 3, Stdout: []byte(c.Args[0])}, nil
	}
	res, err := r.Exec(ctx, "a", sandbox.Command{Args: []string{"systemctl"}})
	if err != nil || res.ExitCode != 3 || string(res.Stdout) != "systemctl" || len(r.Execs) != 2 {
		t.Errorf("exec = %+v, %v, %d recorded", res, err, len(r.Execs))
	}
}

func TestMissing(t *testing.T) {
	ctx := context.Background()
	r := New()
	for name, err := range map[string]error{
		"start":  r.Start(ctx, "x"),
		"stop":   r.Stop(ctx, "x", 0),
		"delete": r.Delete(ctx, "x"),
		"limits": r.SetLimits(ctx, "x", sandbox.Limits{}),
		"attach": r.Attach(ctx, "x", sandbox.Mount{}),
		"push":   r.Push(ctx, "x"),
	} {
		if !errors.Is(err, sandbox.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := r.State(ctx, "x"); !errors.Is(err, sandbox.ErrNotFound) {
		t.Errorf("state: %v", err)
	}
}
