package metering_test

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/flowdproto/flowdfake"
	"github.com/trick77/flow/internal/metering"
	"github.com/trick77/flow/internal/projects"
	"github.com/trick77/flow/internal/sessions"
	"github.com/trick77/flow/internal/store"
)

// The follower's tests drive the real sessions.Service, which imports metering: they live outside the package.

type sandboxes struct{ api *flowdfake.Fake }

func (f sandboxes) Ensure(context.Context, string) (flowdproto.API, error) { return f.api, nil }
func (f sandboxes) Warm(context.Context, string) (flowdproto.API, error)   { return f.api, nil }
func (sandboxes) Forget(string)                                            {}

type world struct {
	db   *sql.DB
	ss   sessions.SQL
	fake *flowdfake.Fake
}

func newWorld(t *testing.T) *world {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "flow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := (projects.SQL{DB: db}).Create(t.Context(), projects.Project{ID: "p1", Name: "demo"}, []projects.Repo{
		{ID: "r1", FullName: "dev/scener", DefaultBranch: "master"}}); err != nil {
		t.Fatal(err)
	}
	return &world{db: db, ss: sessions.SQL{DB: db}, fake: flowdfake.New("sb")}
}

// start runs a sessions service with meter and creates a session with a first message.
func (w *world) start(ctx context.Context, t *testing.T, meter sessions.Meter, log *slog.Logger) string {
	t.Helper()
	svc := &sessions.Service{Store: w.ss, Projects: projects.SQL{DB: w.db}, Sandboxes: sandboxes{api: w.fake},
		Log: log, Retry: 10 * time.Millisecond, Meter: meter}
	if err := svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	created, err := svc.Create(ctx, apitypes.SessionCreate{ProjectID: "p1", RepoIds: []string{"r1"},
		Provider: apitypes.ProviderClaude, Model: "claude-opus-5-5", Mode: apitypes.PermissionModeAsk,
		Message: &apitypes.MessageCreate{ClientID: "m1", Text: "Fix the flaky test"}})
	if err != nil {
		t.Fatal(err)
	}
	return created.Summary.ID
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The follower hands its events to the recorder: a session created through the sessions area ends up in the
// ledger, its turn on the model the fake stamped on turn.started.
func TestFollowerFeedsTheLedger(t *testing.T) {
	w := newWorld(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	id := w.start(ctx, t, &metering.Recorder{Store: metering.SQL{DB: w.db}}, slog.New(slog.DiscardHandler))
	waitFor(t, func() bool {
		var n int
		_ = w.db.QueryRowContext(t.Context(), `SELECT count(*) FROM metering_turns WHERE session_id = ?`, id).Scan(&n)
		return n == 1
	})
	var model, mode, repos string
	if err := w.db.QueryRowContext(t.Context(), `SELECT model, credential_mode, repos FROM metering_turns
		WHERE session_id = ?`, id).Scan(&model, &mode, &repos); err != nil {
		t.Fatal(err)
	}
	if model != "claude-opus-5-5" || mode != metering.CredentialSubscriptionToken || repos != `["dev/scener"]` {
		t.Errorf("followed turn: %s %s %s", model, mode, repos)
	}
}

// broken fails every status write, as a full disk would.
type broken struct{ metering.SQL }

func (broken) RecordStatusChange(context.Context, metering.StatusChange) error {
	return errors.New("disk full")
}

// A ledger that cannot be written holds the follower at the event it refused, so nothing is skipped: the session
// row's progress stops just before the first status change.
func TestFollowerStopsAtALedgerFailure(t *testing.T) {
	w := newWorld(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	id := w.start(ctx, t, &metering.Recorder{Store: broken{metering.SQL{DB: w.db}}}, slog.New(slog.DiscardHandler))
	var log []event.Event
	for e, err := range w.fake.Events(ctx, id, 0) {
		if err != nil {
			t.Fatal(err)
		}
		log = append(log, e)
		if e.Kind == event.KindTurnEnded {
			break
		}
	}
	first := slices.IndexFunc(log, func(e event.Event) bool { return e.Kind == event.KindSessionStatus })
	if first < 0 {
		t.Fatal("no status change in the log")
	}
	waitFor(t, func() bool {
		se, err := w.ss.Get(t.Context(), id)
		return err == nil && se.LastSeq == log[first].Seq-1
	})
	time.Sleep(50 * time.Millisecond) // several retries
	if se, _ := w.ss.Get(t.Context(), id); se.LastSeq != log[first].Seq-1 {
		t.Errorf("the follower moved to seq %d past a refused status change at %d", se.LastSeq, log[first].Seq)
	}
}

// errorCount counts records at error level.
type errorCount struct{ n *atomic.Int32 }

func (h errorCount) Enabled(context.Context, slog.Level) bool { return true }
func (h errorCount) Handle(_ context.Context, r slog.Record) error {
	if r.Level >= slog.LevelError {
		h.n.Add(1)
	}
	return nil
}
func (h errorCount) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h errorCount) WithGroup(string) slog.Handler      { return h }

// blocking is a meter whose write waits for the follower's context, as one Flow's shutdown cuts off.
type blocking struct{ calls, forgets *atomic.Int32 }

func (b blocking) Observe(ctx context.Context, _ metering.Session, _ event.Event) error {
	b.calls.Add(1)
	<-ctx.Done()
	return ctx.Err()
}

func (b blocking) Forget(string) { b.forgets.Add(1) }

// Shutting down while the meter writes is not an error worth logging for every followed session; the follower
// tells the meter it stopped following.
func TestShutdownLogsNoMeterError(t *testing.T) {
	w := newWorld(t)
	var errs, calls, forgets atomic.Int32
	ctx, cancel := context.WithCancel(t.Context())
	w.start(ctx, t, blocking{calls: &calls, forgets: &forgets}, slog.New(errorCount{n: &errs}))
	waitFor(t, func() bool { return calls.Load() > 0 })
	cancel()
	waitFor(t, func() bool { return forgets.Load() > 0 })
	if errs.Load() != 0 {
		t.Errorf("%d errors logged at shutdown", errs.Load())
	}
}
