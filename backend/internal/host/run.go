package host

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"time"

	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/eventlog"
	"github.com/trick77/flow/internal/layout"
)

// Run is flowd host: one session's host until ctx ends or a host.shutdown. flowd starts it as the unit
// layout.HostUnit with --session; the other flags exist for tests and manual runs.
func Run(ctx context.Context, args []string, version string, log *slog.Logger) error {
	fs := flag.NewFlagSet("host", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	session := fs.String("session", "", "session id")
	specPath := fs.String("spec", "", "spec file (default layout.SessionSpecFile)")
	logPath := fs.String("log", "", "event log (default layout.EventLog)")
	socket := fs.String("socket", "", "command socket (default layout.HostSocket)")
	pace := fs.Float64("pace", DefaultPace.Factor, "replay pace factor; 0 plays without waiting")
	resume := fs.String("resume", string(event.ResumeReasonMessage), "session.resumed reason after the first start")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !layout.ValidName(*session) {
		return fmt.Errorf("host: bad --session %q", *session)
	}
	def := func(p *string, v string) {
		if *p == "" {
			*p = v
		}
	}
	def(specPath, layout.SessionSpecFile(*session))
	def(logPath, layout.EventLog(*session))
	def(socket, layout.HostSocket(*session))

	b, err := os.ReadFile(*specPath)
	if err != nil {
		return err
	}
	var spec Spec
	if err := json.Unmarshal(b, &spec); err != nil {
		return fmt.Errorf("host: spec %s: %w", *specPath, err)
	}
	if spec.Session.ID != *session {
		return fmt.Errorf("host: spec is for session %q", spec.Session.ID)
	}
	p := DefaultPace
	if *pace == 0 {
		p = Pace{}
	} else {
		p.Factor = *pace
	}
	adapter, err := NewDemoReplay(p)
	if err != nil {
		return err
	}
	h, err := New(Config{Spec: spec, LogPath: *logPath, Adapter: adapter, Version: version,
		ResumeReason: event.ResumeReason(*resume), Log: log})
	if err != nil {
		return err
	}
	defer func() { _ = h.Close() }()

	_ = os.Remove(*socket) // a stale socket of a host that died
	l, err := net.Listen("unix", *socket)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-h.Stopped():
			cancel()
		case <-ctx.Done():
		}
	}()
	go func() {
		t := time.NewTicker(eventlog.SyncInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				h.Sync()
			}
		}
	}()
	errc := make(chan error, 2)
	go func() { errc <- h.Serve(ctx, l) }()
	go func() { errc <- h.Pump(ctx) }()
	log.Info("host started", "session", *session, "version", version, "lastSeq", h.Hello(0).LastSeq)
	err = <-errc
	cancel()
	err2 := <-errc
	_ = os.Remove(*socket)
	log.Info("host stopping", "session", *session)
	return errors.Join(err, err2)
}
