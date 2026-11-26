// Package supervisor is flowd's core. flowd runs inside each sandbox's system container and will own the agent
// processes there; for now it announces itself and waits for its stop.
package supervisor

import (
	"context"
	"log/slog"
)

// Run logs the version and blocks until ctx ends (kit's run.Context: SIGINT or SIGTERM). A stop is not a failure.
func Run(ctx context.Context, log *slog.Logger, version string) error {
	log.InfoContext(ctx, "flowd started", "version", version)
	<-ctx.Done()
	// The cause, not an err attr: a stop is the normal end.
	log.InfoContext(context.WithoutCancel(ctx), "flowd stopping", "cause", context.Cause(ctx).Error())
	return nil
}
