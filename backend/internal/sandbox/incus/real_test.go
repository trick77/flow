//go:build incus

package incus

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/trick77/flow/internal/sandbox"
)

// TestReal runs against a real Incus with the restricted certificate (deploy/host-setup.sh), configured like Flow:
// BACKEND_INCUS_URL, BACKEND_INCUS_PROJECT, BACKEND_INCUS_CERT, BACKEND_INCUS_KEY, BACKEND_INCUS_SERVER_CERT,
// BACKEND_SANDBOX_IMAGE (default flow-base), BACKEND_SANDBOX_NETWORK (default incusbr0). Build with -tags incus.
func TestReal(t *testing.T) {
	c, err := New(Config{URL: os.Getenv("BACKEND_INCUS_URL"), Project: os.Getenv("BACKEND_INCUS_PROJECT"),
		CertFile: os.Getenv("BACKEND_INCUS_CERT"), KeyFile: os.Getenv("BACKEND_INCUS_KEY"),
		ServerCertFile: os.Getenv("BACKEND_INCUS_SERVER_CERT")})
	if err != nil {
		t.Fatal(err)
	}
	image, network := os.Getenv("BACKEND_SANDBOX_IMAGE"), os.Getenv("BACKEND_SANDBOX_NETWORK")
	if image == "" {
		image = "flow-base"
	}
	if network == "" {
		network = "incusbr0"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	name := "flow-itest"
	_ = c.Stop(ctx, name, 5*time.Second)
	_ = c.Delete(ctx, name)
	t0 := time.Now()
	if err := c.Create(ctx, sandbox.Spec{Name: name, Image: image, Network: network, Nesting: true,
		Limits: sandbox.Limits{MemoryBytes: 1 << 30}}); err != nil {
		t.Fatal(err)
	}
	t.Logf("create %s", time.Since(t0))
	defer func() {
		_ = c.Stop(context.Background(), name, 5*time.Second)
		_ = c.Delete(context.Background(), name)
	}()
	t0 = time.Now()
	if err := c.Start(ctx, name); err != nil {
		t.Fatal(err)
	}
	t.Logf("start %s", time.Since(t0))
	var st sandbox.State
	for st.IPv4 == "" && ctx.Err() == nil {
		st, _ = c.State(ctx, name)
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("address %s after %s", st.IPv4, time.Since(t0))
	payload := strings.Repeat("flow", 1<<18) // 1 MiB
	if err := c.Push(ctx, name, sandbox.File{Path: "/opt/flow/releases/itest/blob", Content: []byte(payload), Mode: 0o750}); err != nil {
		t.Fatal(err)
	}
	res, err := c.Exec(ctx, name, sandbox.Command{Args: []string{"sh", "-c",
		"stat -c '%s %a %u' /opt/flow/releases/itest/blob; echo oops >&2; exit 7"}, Timeout: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(res.Stdout)); got != "1048576 750 0" || res.ExitCode != 7 || strings.TrimSpace(string(res.Stderr)) != "oops" {
		t.Errorf("exec = %q, exit %d, stderr %q", got, res.ExitCode, res.Stderr)
	}
	if err := c.Stop(ctx, name, 10*time.Second); err != nil {
		t.Fatal(err)
	}
}
