package metering

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	kitmigrate "github.com/trick77/ship/kit/migrate"
	"github.com/trick77/ship/kit/sqlitex"

	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/store"
)

const (
	demoSession   = "0193a8f2-9000-7000-8000-0000000000d1"
	eventsSession = "0193a8f2-5d1e-7c3a-9b2e-6f1d2a3b4c5d"
)

func fixture(t *testing.T, name string) []event.Event {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "contract", "fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	var out []event.Event
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, event.MaxLine), event.MaxLine)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		e, err := event.ParseLine(append(sc.Bytes(), '\n'))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

type rig struct {
	db  *sql.DB
	se  Session
	rec func() *Recorder // a new recorder: what a Flow restart gives
}

// newRig opens a migrated database with a project, its repo, the session id under test and its worktree, written
// as the sessions and projects areas write them (metering imports neither).
func newRig(t *testing.T, sessionID, provider, claudeAuth string) *rig {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "flow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, q := range []string{
		`INSERT INTO projects (id, name) VALUES ('p1', 'demo')`,
		`INSERT INTO project_repos (id, project_id, full_name, default_branch) VALUES ('r1', 'p1', 'dev/scener', 'master')`,
		`INSERT INTO sessions (id, project_id, name, provider, claude_auth, model, mode, branch)
			VALUES ('` + sessionID + `', 'p1', 'demo', '` + provider + `', '` + claudeAuth + `', 'session-row-model', 'ask', 'flow/demo')`,
		`INSERT INTO session_worktrees (id, session_id, repo_id, branch, path, base_ref)
			VALUES ('w1', '` + sessionID + `', 'r1', 'flow/demo', '/w/scener', 'origin/master')`,
	} {
		if _, err := db.ExecContext(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	se := Session{ID: sessionID, ProjectID: "p1", Provider: provider, ClaudeAuth: claudeAuth, Model: "session-row-model"}
	return &rig{db: db, se: se, rec: func() *Recorder { return &Recorder{Store: SQL{DB: db}} }}
}

func (r *rig) feed(t *testing.T, rec *Recorder, events []event.Event) {
	t.Helper()
	for _, e := range events {
		if err := rec.Observe(t.Context(), r.se, e); err != nil {
			t.Fatalf("seq %d %s: %v", e.Seq, e.Kind, err)
		}
	}
}

func (r *rig) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	if err := r.db.QueryRowContext(t.Context(), `SELECT count(*) FROM `+table).Scan(&n); err != nil { //nolint:gosec // test tables
		t.Fatal(err)
	}
	return n
}

var ledgerTables = []string{"metering_turn_starts", "metering_turns", "metering_status_changes", "metering_waits",
	"metering_check_runs", "metering_helper_calls", "metering_permission_requests", "metering_permission_decisions",
	"metering_pull_requests", "metering_file_changes", "metering_sandbox_samples", "metering_counters"}

func (r *rig) counts(t *testing.T) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, tb := range ledgerTables {
		out[tb] = r.count(t, tb)
	}
	return out
}

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t
}

// The demo is a real recorded stream: one turn with a permission request in its middle.
func TestObserve_demoStream(t *testing.T) {
	r := newRig(t, demoSession, "claude", "subscription_token")
	r.feed(t, r.rec(), fixture(t, "demo.jsonl"))
	want := map[string]int{"metering_turn_starts": 1, "metering_turns": 1, "metering_status_changes": 4, "metering_waits": 1,
		"metering_permission_requests": 1, "metering_permission_decisions": 1, "metering_file_changes": 1}
	got := r.counts(t)
	for _, tb := range ledgerTables {
		if got[tb] != want[tb] {
			t.Errorf("%s: %d rows, want %d", tb, got[tb], want[tb])
		}
	}

	var turn, repos, provider, model, mode, started, ended, outcome string
	var seq, dur, in, out, cr, cw int64
	var cost, apiEq sql.NullFloat64
	var parent sql.NullString
	if err := r.db.QueryRowContext(t.Context(), `SELECT seq, turn, repos, provider, model, credential_mode, started_at,
		ended_at, duration_ms, outcome, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_usd,
		api_equivalent_usd, parent_session_id FROM metering_turns`).Scan(&seq, &turn, &repos, &provider, &model, &mode,
		&started, &ended, &dur, &outcome, &in, &out, &cr, &cw, &cost, &apiEq, &parent); err != nil {
		t.Fatal(err)
	}
	if seq != 27 || turn != "t-1" || repos != `["dev/scener"]` || provider != "claude" || model != "claude-opus-5-5" ||
		mode != CredentialSubscriptionToken || outcome != "completed" || dur != 221000 || parent.Valid {
		t.Errorf("turn %d %s %s %s %s %s %s %d %v", seq, turn, repos, provider, model, mode, outcome, dur, parent)
	}
	if !store.Parse(ended).Equal(ts("2026-11-26T12:52:31.1Z")) ||
		!store.Parse(started).Equal(ts("2026-11-26T12:52:31.1Z").Add(-221*time.Second)) {
		t.Errorf("turn from %s to %s", started, ended)
	}
	if in != 3810 || out != 2460 || cr != 141200 || cw != 18100 || cost.Float64 != 0.3121 || apiEq.Float64 != 0.3121 {
		t.Errorf("usage %d %d %d %d cost %v api %v", in, out, cr, cw, cost, apiEq)
	}

	var reason, from, to string
	var wait int64
	if err := r.db.QueryRowContext(t.Context(), `SELECT reason, started_at, ended_at, duration_ms FROM metering_waits`).
		Scan(&reason, &from, &to, &wait); err != nil {
		t.Fatal(err)
	}
	if reason != WaitOwnerApproval || wait != 15000 || !store.Parse(from).Equal(ts("2026-11-26T12:50:45.01Z")) {
		t.Errorf("wait %s %s..%s %d", reason, from, to, wait)
	}
	var tool, kind, decision string
	var decWait int64
	if err := r.db.QueryRowContext(t.Context(), `SELECT tool, kind, decision, wait_ms FROM metering_permission_decisions`).
		Scan(&tool, &kind, &decision, &decWait); err != nil {
		t.Fatal(err)
	}
	if tool != "Bash" || kind != "command" || decision != "allow_once" || decWait != 15000 {
		t.Errorf("decision %s %s %s %d", tool, kind, decision, decWait)
	}
	var files, added, removed int64
	if err := r.db.QueryRowContext(t.Context(), `SELECT files, lines_added, lines_removed FROM metering_file_changes`).
		Scan(&files, &added, &removed); err != nil {
		t.Fatal(err)
	}
	if files != 3 || added != 31 || removed != 7 {
		t.Errorf("file changes %d +%d -%d", files, added, removed)
	}
	rows, err := r.db.QueryContext(t.Context(), `SELECT from_status, to_status FROM metering_status_changes ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var path []string
	for rows.Next() {
		var f, to string
		if err := rows.Scan(&f, &to); err != nil {
			t.Fatal(err)
		}
		path = append(path, f+">"+to)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(path, " ") != ">working working>approval approval>working working>done" {
		t.Errorf("transitions %v", path)
	}
}

// A Flow restart resumes the follower from its last recorded seq with a recorder that remembers nothing. Here
// the restart falls between the permission request and its decision, and the event in flight when Flow died is
// handed on again: the ledger ends up exactly as without the restart.
func TestObserve_idempotentAcrossARestart(t *testing.T) {
	demo := fixture(t, "demo.jsonl")
	whole := newRig(t, demoSession, "claude", "subscription_token")
	whole.feed(t, whole.rec(), demo)
	want := whole.counts(t)

	r := newRig(t, demoSession, "claude", "subscription_token")
	r.feed(t, r.rec(), demo[:18]) // through seq 18, status approval
	r.feed(t, r.rec(), demo[17:]) // seq 18 again, then the rest
	r.feed(t, r.rec(), demo)      // and a full re-follow on top
	got := r.counts(t)
	for _, tb := range ledgerTables {
		if got[tb] != want[tb] {
			t.Errorf("%s: %d rows after the restart, %d without", tb, got[tb], want[tb])
		}
	}
	var wait sql.NullInt64
	var tool string
	if err := r.db.QueryRowContext(t.Context(), `SELECT tool, wait_ms FROM metering_permission_decisions`).
		Scan(&tool, &wait); err != nil {
		t.Fatal(err)
	}
	if tool != "Bash" || wait.Int64 != 15000 {
		t.Errorf("decision after a restart: %s %v", tool, wait)
	}
	var reason string
	var dur int64
	if err := r.db.QueryRowContext(t.Context(), `SELECT reason, duration_ms FROM metering_waits`).Scan(&reason, &dur); err != nil {
		t.Fatal(err)
	}
	if reason != WaitOwnerApproval || dur != 15000 {
		t.Errorf("owner wait after a restart: %s %d", reason, dur)
	}
}

// strip removes the model fields from the turn events, as today's host and replay write them.
func strip(t *testing.T, events []event.Event) []event.Event {
	t.Helper()
	out := slices.Clone(events)
	for i, e := range out {
		switch e.Kind {
		case event.KindTurnStarted, event.KindTurnEnded:
			var m map[string]any
			if err := json.Unmarshal(e.Data, &m); err != nil {
				t.Fatal(err)
			}
			delete(m, "model")
			delete(m, "provider")
			out[i].Data, _ = json.Marshal(m)
		}
	}
	return out
}

func modelOf(t *testing.T, r *rig) string {
	t.Helper()
	var m string
	if err := r.db.QueryRowContext(t.Context(), `SELECT model FROM metering_turns`).Scan(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

// Without a model on turn.ended the turn takes turn.started's, then the last settings', then the session row's.
func TestObserve_modelFallbacks(t *testing.T) {
	demo := fixture(t, "demo.jsonl")

	r := newRig(t, demoSession, "claude", "subscription_token")
	started := slices.Clone(demo)
	for i, e := range started {
		switch e.Kind {
		case event.KindTurnStarted:
			started[i].Data = json.RawMessage(`{"messageId":"m","provider":"claude","model":"claude-haiku-4-5"}`)
		case event.KindTurnEnded:
			started[i].Data = json.RawMessage(`{"outcome":"completed","durationMs":1000}`)
		}
	}
	r.feed(t, r.rec(), started)
	if m := modelOf(t, r); m != "claude-haiku-4-5" {
		t.Errorf("from turn.started: %s", m)
	}

	r = newRig(t, demoSession, "claude", "subscription_token")
	bare := strip(t, demo)
	settings, _ := event.New(demoSession, event.KindSessionSettings, ts("2026-11-26T12:49:00.5Z"),
		event.SessionSettings{Model: "claude-sonnet-4-5"})
	settings.Seq = 2
	r.feed(t, r.rec(), append([]event.Event{bare[0], settings}, bare[3:]...))
	if m := modelOf(t, r); m != "claude-sonnet-4-5" {
		t.Errorf("from session.settings: %s", m)
	}

	// A restart after the turn started: the turn start is in the ledger, memory is not needed.
	r = newRig(t, demoSession, "claude", "subscription_token")
	r.feed(t, r.rec(), bare[:10])
	r.feed(t, r.rec(), bare[10:])
	if m := modelOf(t, r); m != "claude-opus-5-5" {
		t.Errorf("from the turn start before a restart: %s", m)
	}

	// Metering joined mid-turn: it never saw the start, the session row is all there is.
	r = newRig(t, demoSession, "claude", "subscription_token")
	r.feed(t, r.rec(), bare[10:])
	if m := modelOf(t, r); m != "session-row-model" {
		t.Errorf("from the session row: %s", m)
	}
}

// events.jsonl has every kind: pass-through, an unknown tool, a child's report; plus an unknown kind and a failed
// turn of an API-key session.
func TestObserve_countersAndCredentialModes(t *testing.T) {
	r := newRig(t, eventsSession, "claude", "api_key")
	all := fixture(t, "events.jsonl")
	var mine []event.Event
	for _, e := range all {
		if e.Session == eventsSession {
			mine = append(mine, e)
		}
	}
	future := event.Event{V: 1, Seq: 90, TS: ts("2026-11-26T15:00:00Z"), Session: eventsSession,
		Kind: "workspace.indexed", Data: json.RawMessage(`{}`)}
	failed, _ := event.New(eventsSession, event.KindTurnEnded, ts("2026-11-26T15:01:00Z"),
		event.TurnEnded{Outcome: event.TurnOutcomeError, DurationMs: 500, ErrorCode: "provider_error"})
	failed.Seq, failed.Turn = 91, "t-0009"
	undecodable := event.Event{V: 1, Seq: 92, TS: ts("2026-11-26T15:02:00Z"), Session: eventsSession,
		Kind: event.KindTurnEnded, Data: json.RawMessage(`"nope"`)}
	newer := event.Event{V: 2, Seq: 93, TS: ts("2026-11-26T15:03:00Z"), Session: eventsSession,
		Kind: event.KindTurnEnded, Data: json.RawMessage(`{}`)}
	ephemeral := event.Event{V: 1, TS: ts("2026-11-26T15:04:00Z"), Session: eventsSession,
		Kind: event.KindToolProgress, Data: json.RawMessage(`{"toolId":"x","elapsedMs":1}`)}
	r.feed(t, r.rec(), append(mine, future, failed, undecodable, newer, ephemeral))

	got := map[string]int{}
	rows, err := r.db.QueryContext(t.Context(), `SELECT counter, class FROM metering_counters ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var c, cl string
		if err := rows.Scan(&c, &cl); err != nil {
			t.Fatal(err)
		}
		got[c+":"+cl]++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"passthrough:system/workspace_indexed": 1, "passthrough:system/large_report": 1,
		"unknown_kind:workspace.indexed": 1, "provider_error:provider_error": 1, "turn_error:provider_error": 1}
	if len(got) != len(want) {
		t.Errorf("counters %v, want %v", got, want)
	}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("counter %s: %d, want %d (all: %v)", k, got[k], n, got)
		}
	}
	var apiEq sql.NullFloat64
	var cost sql.NullFloat64
	var mode string
	if err := r.db.QueryRowContext(t.Context(), `SELECT credential_mode, cost_usd, api_equivalent_usd FROM metering_turns
		WHERE seq = 29`).Scan(&mode, &cost, &apiEq); err != nil {
		t.Fatal(err)
	}
	if mode != CredentialAPIKey || cost.Float64 != 0.4212 || apiEq.Valid {
		t.Errorf("api-key turn: %s cost %v api equivalent %v", mode, cost, apiEq)
	}
	var failedCost sql.NullFloat64
	if err := r.db.QueryRowContext(t.Context(), `SELECT cost_usd FROM metering_turns WHERE seq = 91`).Scan(&failedCost); err != nil {
		t.Fatal(err)
	}
	if failedCost.Valid {
		t.Errorf("a turn that reported no cost has cost %v", failedCost)
	}

	oc := newRig(t, eventsSession, "opencode", "")
	oc.feed(t, oc.rec(), mine)
	if err := oc.db.QueryRowContext(t.Context(), `SELECT credential_mode FROM metering_turns`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != CredentialProviderKey {
		t.Errorf("OpenCode credential mode %s", mode)
	}
}

// contentOf collects the strings of a fixture that are prompt, message, command, path or code content.
func contentOf(t *testing.T, events []event.Event) []string {
	t.Helper()
	var out []string
	add := func(ss ...string) {
		for _, s := range ss {
			if len(s) >= 6 {
				out = append(out, s)
			}
		}
	}
	var leaves func(v any)
	leaves = func(v any) {
		switch v := v.(type) {
		case string:
			add(v)
		case map[string]any:
			for _, x := range v {
				leaves(x)
			}
		case []any:
			for _, x := range v {
				leaves(x)
			}
		}
	}
	raw := func(m json.RawMessage) {
		var v any
		if json.Unmarshal(m, &v) == nil {
			leaves(v)
		}
	}
	for _, e := range events {
		p, err := e.Payload()
		if err != nil {
			continue
		}
		switch p := p.(type) {
		case *event.UserMessage:
			add(p.Text)
		case *event.MessageQueued:
			add(p.Text)
		case *event.AssistantText:
			add(p.Text)
		case *event.AssistantThinking:
			add(p.Text)
		case *event.ToolStarted:
			add(p.Path, p.Command, p.Pattern, p.Description, p.URL)
			raw(p.Input)
		case *event.ToolFinished:
			add(p.Output)
			raw(p.RawResult)
		case *event.PermissionRequested:
			add(p.Command, p.Path, p.Cwd)
			raw(p.Input)
		case *event.PlanProposed:
			add(p.Plan)
		case *event.QuestionAsked:
			for _, q := range p.Questions {
				add(q.Text, q.Header)
				for _, o := range q.Options {
					add(o.Label, o.Description)
				}
			}
		case *event.ChildReported:
			add(p.Summary)
		case *event.FilesChanged:
			for _, f := range p.Files {
				add(f.Path, f.OldPath)
			}
		}
	}
	return out
}

// Nothing in the ledger contains prompt, message, command, path or code content: every text cell of every
// metering table is checked against every such string of the fixtures fed in.
func TestLedgerHoldsNoContent(t *testing.T) {
	r := newRig(t, demoSession, "claude", "subscription_token")
	demo := fixture(t, "demo.jsonl")
	r.feed(t, r.rec(), demo)
	all := fixture(t, "events.jsonl")
	rec := r.rec()
	for _, e := range all {
		se := Session{ID: e.Session, ProjectID: "p1", Provider: "claude", ClaudeAuth: "subscription_token", Model: "m"}
		if err := rec.Observe(t.Context(), se, e); err != nil {
			t.Fatal(err)
		}
	}
	content := contentOf(t, append(demo, all...))
	if len(content) < 20 {
		t.Fatalf("only %d content strings: the fixture walk is broken", len(content))
	}
	tables, err := r.db.QueryContext(t.Context(), `SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 'metering_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var n string
		if err := tables.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	if err := tables.Err(); err != nil {
		t.Fatal(err)
	}
	_ = tables.Close()
	if len(names) != len(ledgerTables) {
		t.Errorf("metering tables %v, want %v", names, ledgerTables)
	}
	cells := 0
	for _, n := range names {
		rows, err := r.db.QueryContext(t.Context(), `SELECT * FROM `+n) //nolint:gosec // names from sqlite_master
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for i, v := range vals {
				s, ok := v.(string)
				if !ok {
					continue
				}
				cells++
				for _, c := range content {
					if strings.Contains(s, c) {
						t.Errorf("%s.%s holds content %q", n, cols[i], c)
					}
				}
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		_ = rows.Close()
	}
	if cells < 50 {
		t.Errorf("only %d text cells checked", cells)
	}
}

// The ledger migration applies on top of a database that has only 0001, with rows in it.
func TestMigrationAppliesOnTopOf0001(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flow.db")
	db, err := sql.Open("sqlite3", sqlitex.DSN(path, "_txlock=immediate"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := fs.ReadFile(store.Migrations(), "0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	applied, err := kitmigrate.Up(t.Context(), db, fstest.MapFS{"0001_init.sql": {Data: first}},
		kitmigrate.Options{Dialect: kitmigrate.SQLite, Log: slog.New(slog.DiscardHandler)})
	if err != nil || len(applied) != 1 {
		t.Fatalf("0001 alone: %v %v", applied, err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO projects (id, name) VALUES ('p1', 'demo')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO sessions (id, project_id, name, provider, model, mode, branch)
		VALUES ('s1', 'p1', 'n', 'claude', 'm', 'ask', 'b')`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	db, err = store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var n int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM sessions`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("sessions after 0002: %d %v", n, err)
	}
	r := &rig{db: db}
	for _, tb := range ledgerTables {
		if r.count(t, tb) != 0 {
			t.Errorf("%s is not empty", tb)
		}
	}
	// No foreign keys: the ledger outlives its session and project.
	rec := &Recorder{Store: SQL{DB: db}}
	if err := rec.Observe(t.Context(), Session{ID: "s1", ProjectID: "p1", Provider: "claude"},
		mustEvent(t, "s1", 1, event.KindTurnEnded, event.TurnEnded{Outcome: event.TurnOutcomeCompleted, DurationMs: 5})); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `DELETE FROM projects WHERE id = 'p1'`); err != nil {
		t.Fatal(err)
	}
	if r.count(t, "metering_turns") != 1 {
		t.Error("deleting the project took the ledger with it")
	}
}

func mustEvent(t *testing.T, session string, seq int64, kind event.EventKind, payload any) event.Event {
	t.Helper()
	e, err := event.New(session, kind, ts("2026-11-26T12:00:00Z").Add(time.Duration(seq)*time.Second), payload)
	if err != nil {
		t.Fatal(err)
	}
	e.Seq = seq
	return e
}

func TestRecordAPI(t *testing.T) {
	r := newRig(t, demoSession, "claude", "subscription_token")
	now := ts("2026-11-26T13:00:00Z")
	rec := r.rec()
	rec.Now = func() time.Time { return now }
	ctx := t.Context()
	sid := demoSession
	pos := int64(2)
	exit := int64(1)
	cost := 0.0004

	ok := []func() error{
		func() error {
			return rec.RecordWait(ctx, Wait{ID: "w1", SessionID: &sid, ProjectID: "p1", Reason: WaitCheckQueue,
				StartedAt: now, EndedAt: now.Add(90 * time.Second), Resource: "dev/scener", Position: &pos,
				HolderRepo: "dev/toolkit"})
		},
		func() error {
			return rec.RecordWait(ctx, Wait{ProjectID: "p1", Reason: WaitSandboxStart, StartedAt: now,
				EndedAt: now.Add(time.Second)})
		},
		func() error {
			return rec.RecordCheckRun(ctx, CheckRun{ID: "c1", SessionID: sid, ProjectID: "p1", Repo: "dev/scener",
				StartedAt: now, DurationMs: 61000, QueueWaitMs: 90000, Result: CheckFailed, ExitCode: &exit,
				FailingStep: "go test"})
		},
		func() error {
			return rec.RecordHelperCall(ctx, HelperCall{Task: "title", ProjectID: "p1", Provider: "zai",
				Model: "glm-5.3-flash", StartedAt: now, DurationMs: 800, InputTokens: 120, OutputTokens: 1100,
				CostUSD: &cost, Outcome: HelperOK})
		},
		func() error {
			return rec.RecordPullRequest(ctx, PullRequest{ID: "pr1", SessionID: sid, ProjectID: "p1", Repo: "dev/scener",
				Number: 212, Action: PullRequestOpened})
		},
		func() error {
			return rec.RecordPullRequest(ctx, PullRequest{ID: "pr1", SessionID: sid, ProjectID: "p1", Repo: "dev/scener",
				Number: 212, Action: PullRequestOpened}) // again by id: a no-op
		},
		func() error {
			return rec.RecordSandboxSample(ctx, SandboxSample{SandboxID: "sb1", ProjectID: "p1", State: "warm",
				MemoryBytes: 1 << 30, CPUUsec: 5e6, PSIMemSomeUs: 1200, OOMKills: 1})
		},
		func() error {
			return rec.RecordCounter(ctx, Counter{Counter: CounterFlowdRestart, Class: strings.Repeat("é", 40),
				ProjectID: "p1"})
		},
		func() error { return rec.RecordCounter(ctx, Counter{ID: "k1", Counter: CounterReconnect}) },
		func() error { return rec.RecordCounter(ctx, Counter{ID: "k1", Counter: CounterReconnect}) }, // again
	}
	for i, f := range ok {
		if err := f(); err != nil {
			t.Errorf("call %d: %v", i, err)
		}
	}
	bad := []func() error{
		func() error { return rec.RecordWait(ctx, Wait{Reason: "lunch", StartedAt: now, EndedAt: now}) },
		func() error {
			return rec.RecordWait(ctx, Wait{Reason: WaitRateLimit, StartedAt: now, EndedAt: now.Add(-1)})
		},
		func() error { return rec.RecordCheckRun(ctx, CheckRun{Result: "green"}) },
		func() error { return rec.RecordCheckRun(ctx, CheckRun{Result: CheckPassed}) },
		func() error { return rec.RecordHelperCall(ctx, HelperCall{Outcome: "maybe"}) },
		func() error { return rec.RecordHelperCall(ctx, HelperCall{Outcome: HelperOK}) },
		func() error { return rec.RecordPullRequest(ctx, PullRequest{Action: "reopened"}) },
		func() error { return rec.RecordPullRequest(ctx, PullRequest{Action: PullRequestMerged}) },
		func() error { return rec.RecordSandboxSample(ctx, SandboxSample{}) },
		func() error { return rec.RecordCounter(ctx, Counter{Counter: "vibes"}) },
	}
	for i, f := range bad {
		if err := f(); !errors.Is(err, ErrInvalid) {
			t.Errorf("bad call %d: %v, want ErrInvalid", i, err)
		}
	}
	want := map[string]int{"metering_waits": 2, "metering_check_runs": 1, "metering_helper_calls": 1,
		"metering_pull_requests": 1, "metering_sandbox_samples": 1, "metering_counters": 2}
	for tb, n := range want {
		if got := r.count(t, tb); got != n {
			t.Errorf("%s: %d rows, want %d", tb, got, n)
		}
	}
	var dur int64
	if err := r.db.QueryRowContext(ctx, `SELECT duration_ms FROM metering_waits WHERE id = 'w1'`).Scan(&dur); err != nil || dur != 90000 {
		t.Errorf("wait duration %d %v", dur, err)
	}
	var at, class string
	var n int64
	if err := r.db.QueryRowContext(ctx, `SELECT at, class, count FROM metering_counters WHERE counter = 'flowd_restart'`).
		Scan(&at, &class, &n); err != nil {
		t.Fatal(err)
	}
	if !store.Parse(at).Equal(now) || len(class) > maxClass || !strings.HasPrefix(class, "é") || n != 1 {
		t.Errorf("counter at %s class %q (%d bytes) count %d", at, class, len(class), n)
	}
}
