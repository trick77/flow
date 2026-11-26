package supervisor

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestRun_waitsForTheStop(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, log, "1.2.3") }()

	select {
	case err := <-done:
		t.Fatalf("Run returned before the stop: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v, want nil on a stop", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the stop")
	}
	out := buf.String()
	for _, want := range []string{"flowd started", "version=1.2.3", "flowd stopping"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
}
