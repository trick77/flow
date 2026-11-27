package metering

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/trick77/flow/internal/event"
)

// ev builds one durable event of the session under test at seq, one second apart.
func ev(t *testing.T, seq int64, turn string, kind event.EventKind, data string) event.Event {
	t.Helper()
	return event.Event{V: 1, Seq: seq, TS: ts("2026-11-26T12:00:00Z").Add(time.Duration(seq) * time.Second),
		Session: demoSession, Kind: kind, Turn: turn, Data: json.RawMessage(data)}
}

// failTurnOnce fails the first RecordTurn, as a locked or full database would.
type failTurnOnce struct {
	SQL
	failed bool
}

func (f *failTurnOnce) RecordTurn(ctx context.Context, t Turn) error {
	if !f.failed {
		f.failed = true
		return errors.New("database is locked")
	}
	return f.SQL.RecordTurn(ctx, t)
}

func turnRow(t *testing.T, r *rig) (provider, model string, cost, apiEq sql.NullFloat64) {
	t.Helper()
	if err := r.db.QueryRowContext(t.Context(), `SELECT provider, model, cost_usd, api_equivalent_usd FROM metering_turns`).
		Scan(&provider, &model, &cost, &apiEq); err != nil {
		t.Fatal(err)
	}
	return provider, model, cost, apiEq
}

// A turn row that failed to write is written by the follower's retry with the turn's own model.
func TestReview_turnRetryKeepsTheTurnsModel(t *testing.T) {
	r := newRig(t, demoSession, "claude", "subscription_token")
	rec := r.rec()
	rec.Store = &failTurnOnce{SQL: SQL{DB: r.db}}
	r.feed(t, rec, []event.Event{
		ev(t, 1, "", event.KindSessionStarted, `{"provider":"claude","model":"model-a","mode":"ask","cwd":"/w"}`),
		ev(t, 2, "t-1", event.KindTurnStarted, `{"messageId":"m","provider":"claude","model":"model-b"}`),
	})
	end := ev(t, 3, "t-1", event.KindTurnEnded, `{"outcome":"completed","durationMs":1000}`)
	if err := rec.Observe(t.Context(), r.se, end); err == nil {
		t.Fatal("the failing write was not reported")
	}
	if err := rec.Observe(t.Context(), r.se, end); err != nil {
		t.Fatal(err)
	}
	if _, m, _, _ := turnRow(t, r); m != "model-b" {
		t.Errorf("model after a retry: %s, want model-b", m)
	}
}

// A turn that reported usage but no cost has an unknown cost, not a zero one; a reported zero stays zero.
func TestReview_absentCostIsNull(t *testing.T) {
	r := newRig(t, demoSession, "claude", "subscription_token")
	r.feed(t, r.rec(), []event.Event{ev(t, 1, "t-1", event.KindTurnEnded,
		`{"outcome":"completed","durationMs":1000,"usage":{"inputTokens":10,"outputTokens":5}}`)})
	if _, _, cost, apiEq := turnRow(t, r); cost.Valid || apiEq.Valid {
		t.Errorf("absent cost stored as %v, api equivalent %v", cost, apiEq)
	}
	z := newRig(t, demoSession, "claude", "subscription_token")
	z.feed(t, z.rec(), []event.Event{ev(t, 1, "t-1", event.KindTurnEnded,
		`{"outcome":"interrupted","durationMs":10,"costUsd":0}`)})
	if _, _, cost, _ := turnRow(t, z); !cost.Valid || cost.Float64 != 0 {
		t.Errorf("reported zero cost stored as %v", cost)
	}
}

// A model change while a turn runs applies to the next turn, not to the running one.
func TestReview_modelIsFixedAtTurnStart(t *testing.T) {
	r := newRig(t, demoSession, "claude", "subscription_token")
	r.feed(t, r.rec(), []event.Event{
		ev(t, 1, "", event.KindSessionStarted, `{"provider":"claude","model":"model-a","mode":"ask","cwd":"/w"}`),
		ev(t, 2, "t-1", event.KindTurnStarted, `{"messageId":"m"}`),
		ev(t, 3, "t-1", event.KindSessionSettings, `{"model":"model-x"}`),
		ev(t, 4, "t-1", event.KindTurnEnded, `{"outcome":"completed","durationMs":1000}`),
		ev(t, 5, "t-2", event.KindTurnStarted, `{"messageId":"m2"}`),
		ev(t, 6, "t-2", event.KindTurnEnded, `{"outcome":"completed","durationMs":1000}`),
	})
	rows, err := r.db.QueryContext(t.Context(), `SELECT model FROM metering_turns ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			t.Fatal(err)
		}
		got = append(got, m)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "model-a" || got[1] != "model-x" {
		t.Errorf("turn models %v, want [model-a model-x]", got)
	}
}

// A pull request closed and reopened keeps every state change.
func TestReview_pullRequestReopened(t *testing.T) {
	r := newRig(t, demoSession, "claude", "subscription_token")
	rec := r.rec()
	for i, a := range []string{"opened", "closed", "reopened", "closed"} {
		if err := rec.RecordPullRequest(t.Context(), PullRequest{SessionID: demoSession, ProjectID: "p1",
			Repo: "dev/scener", Number: 7, Action: a, At: ts("2026-11-26T12:00:00Z").Add(time.Duration(i) * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if n := r.count(t, "metering_pull_requests"); n != 4 {
		t.Errorf("%d pull-request rows, want 4", n)
	}
	// From the stream: one row per pull request and event, so a re-follow adds nothing, while one event may
	// change two pull requests.
	seq := int64(40)
	for range 2 {
		for _, n := range []int64{7, 8} {
			if err := rec.RecordPullRequest(t.Context(), PullRequest{SessionID: demoSession, ProjectID: "p1",
				Repo: "dev/scener", Number: n, Action: PullRequestMerged, Seq: &seq}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if n := r.count(t, "metering_pull_requests"); n != 6 {
		t.Errorf("%d pull-request rows after a repeated event, want 6", n)
	}
}

func counterRows(t *testing.T, r *rig) map[string]int {
	t.Helper()
	rows, err := r.db.QueryContext(t.Context(), `SELECT counter, class FROM metering_counters`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int{}
	for rows.Next() {
		var c, cl string
		if err := rows.Scan(&c, &cl); err != nil {
			t.Fatal(err)
		}
		out[c+":"+cl]++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// provider_error counts only provider-side errors; every error turn is a turn_error by class; a lost turn is a
// turn_lost.
func TestReview_turnErrorCounters(t *testing.T) {
	r := newRig(t, demoSession, "claude", "subscription_token")
	r.feed(t, r.rec(), []event.Event{
		ev(t, 1, "t-1", event.KindTurnEnded, `{"outcome":"error","durationMs":1,"errorCode":"max_turns"}`),
		ev(t, 2, "t-2", event.KindTurnEnded, `{"outcome":"error","durationMs":1,"errorCode":"budget"}`),
		ev(t, 3, "t-3", event.KindTurnEnded, `{"outcome":"error","durationMs":1,"errorCode":"adapter_exited"}`),
		ev(t, 4, "t-4", event.KindTurnEnded, `{"outcome":"error","durationMs":1,"errorCode":"provider_error"}`),
		ev(t, 5, "t-5", event.KindTurnEnded, `{"outcome":"error","durationMs":1}`),
		ev(t, 6, "t-6", event.KindTurnEnded, `{"outcome":"lost","durationMs":1}`),
		ev(t, 7, "t-7", event.KindTurnEnded, `{"outcome":"interrupted","durationMs":1}`),
	})
	want := map[string]int{"turn_error:max_turns": 1, "turn_error:budget": 1, "turn_error:adapter_exited": 1,
		"turn_error:provider_error": 1, "turn_error:unknown": 1, "provider_error:provider_error": 1,
		"provider_error:unknown": 1, "turn_lost:": 1}
	got := counterRows(t, r)
	if len(got) != len(want) {
		t.Errorf("counters %v, want %v", got, want)
	}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("%s: %d, want %d (all %v)", k, got[k], n, got)
		}
	}
}

// The recorder forgets a session once nothing is open: memory stays bounded by the sessions in flight.
func TestReview_memoryIsBounded(t *testing.T) {
	r := newRig(t, demoSession, "claude", "subscription_token")
	rec := r.rec()
	r.feed(t, rec, fixture(t, "demo.jsonl"))
	rec.mu.Lock()
	n := len(rec.live)
	rec.mu.Unlock()
	if n != 0 {
		t.Errorf("%d sessions remembered after a finished turn", n)
	}
}

// A request id the provider reused is paired with the request asked before the decision, not a later one.
func TestReview_decisionPairsWithTheEarlierRequest(t *testing.T) {
	r := newRig(t, demoSession, "claude", "subscription_token")
	req := `{"requestId":"req_1","tool":"Bash","kind":"command","mode":"ask","canAlwaysAllow":false}`
	r.feed(t, r.rec(), []event.Event{
		ev(t, 5, "t-1", event.KindPermissionRequested, req),
		ev(t, 10, "t-1", event.KindPermissionResolved, `{"requestId":"req_1","decision":"deny"}`),
		ev(t, 20, "t-2", event.KindPermissionRequested, req),
	})
	r2 := newRig(t, demoSession, "claude", "subscription_token")
	r2.feed(t, r2.rec(), []event.Event{
		ev(t, 5, "t-1", event.KindPermissionRequested, req),
		ev(t, 20, "t-2", event.KindPermissionRequested, req),
	})
	// The follower re-reading seq 10 after the later request was recorded (a re-follow) pairs it the same way.
	r2.feed(t, r2.rec(), []event.Event{ev(t, 10, "t-1", event.KindPermissionResolved, `{"requestId":"req_1","decision":"deny"}`)})
	for _, x := range []*rig{r, r2} {
		var wait sql.NullInt64
		if err := x.db.QueryRowContext(t.Context(), `SELECT wait_ms FROM metering_permission_decisions`).Scan(&wait); err != nil {
			t.Fatal(err)
		}
		if !wait.Valid || wait.Int64 != 5000 {
			t.Errorf("decision wait %v, want 5000", wait)
		}
	}
}
