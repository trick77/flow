package profile

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/store"
)

func openSQL(t *testing.T) SQL {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "flow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return SQL{DB: db}
}

func ptr(s string) *string { return &s }

// Update changes only the fields it is given; SetConfigSync records a sync, its time only when it synced.
func TestSQL_UpdateAndConfigSync(t *testing.T) {
	s := openSQL(t)
	ctx := t.Context()
	p, err := s.Update(ctx, Patch{GitName: ptr("Jan"), GitEmail: ptr("jan@example.com")})
	if err != nil || p.GitName != "Jan" || p.GitEmail != "jan@example.com" || p.FormatLocale != "en-CH" {
		t.Fatalf("first update %+v, %v", p, err)
	}
	p, err = s.Update(ctx, Patch{SidebarGrouping: ptr("project"), ConfigRepo: ptr("a/config")})
	if err != nil || p.GitName != "Jan" || p.SidebarGrouping != "project" || p.ConfigRepo != "a/config" {
		t.Fatalf("second update %+v, %v", p, err)
	}
	if p, err = s.Update(ctx, Patch{}); err != nil || p.GitName != "Jan" {
		t.Errorf("empty patch %+v, %v", p, err)
	}
	if err := s.SetConfigSync(ctx, ConfigSync{State: "failed", ErrorCode: "upstream"}); err != nil {
		t.Fatal(err)
	}
	if p, _ = s.Get(ctx); p.ConfigSyncState != "failed" || p.ConfigErrorCode != "upstream" || p.ConfigSyncedAt != nil {
		t.Errorf("failed sync %+v", p)
	}
	if err := s.SetConfigSync(ctx, ConfigSync{State: "synced", Commit: "abc", Files: 3}); err != nil {
		t.Fatal(err)
	}
	if p, _ = s.Get(ctx); p.ConfigSyncState != "synced" || p.ConfigCommit != "abc" || p.ConfigFiles != 3 ||
		p.ConfigSyncedAt == nil || p.ConfigErrorCode != "" {
		t.Errorf("synced %+v", p)
	}
	if _, err := s.Update(ctx, Patch{SidebarGrouping: ptr("nonsense")}); err == nil {
		t.Error("the table's check accepted a grouping it does not know")
	}
}

func call(t *testing.T, h http.HandlerFunc, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "/api/profile", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func TestAPI(t *testing.T) {
	s := openSQL(t)
	changed := 0
	var events []apitypes.AppEvent
	a := &API{Store: s, Changed: func() { changed++ },
		Publish: func(e apitypes.AppEvent) { events = append(events, e) }}

	w := call(t, a.GetProfile, "GET", "")
	var p apitypes.Profile
	if err := json.Unmarshal(w.Body.Bytes(), &p); w.Code != 200 || err != nil || p.FormatLocale != "en-CH" ||
		p.ConfigSync.State != apitypes.ConfigSyncStateNever {
		t.Fatalf("get: %d %s", w.Code, w.Body)
	}

	w = call(t, a.UpdateProfile, "PATCH", `{"gitName":"  Jan Muster ","gitEmail":"jan@example.com"}`)
	if err := json.Unmarshal(w.Body.Bytes(), &p); w.Code != 200 || err != nil || p.GitName != "Jan Muster" ||
		p.GitEmail != "jan@example.com" {
		t.Fatalf("update: %d %s", w.Code, w.Body)
	}
	if changed != 1 || len(events) != 1 || events[0].Kind != apitypes.AppEventKindProfileUpdated {
		t.Errorf("after an identity change: changed %d, events %+v", changed, events)
	}
	// No identity change: nothing to push.
	w = call(t, a.UpdateProfile, "PATCH",
		`{"sidebarGrouping":"project","timeZone":"America/Argentina/Buenos_Aires","formatLocale":"de-CH","language":"en","configRepo":"https://github.com/a/config.git"}`)
	if err := json.Unmarshal(w.Body.Bytes(), &p); w.Code != 200 || err != nil || p.ConfigRepo != "a/config" ||
		p.GitName != "Jan Muster" || changed != 1 {
		t.Errorf("other fields: %d %s, changed %d", w.Code, w.Body, changed)
	}
	// An empty value clears.
	w = call(t, a.UpdateProfile, "PATCH", `{"gitEmail":"","configRepo":""}`)
	p = apitypes.Profile{}
	if err := json.Unmarshal(w.Body.Bytes(), &p); w.Code != 200 || err != nil || p.GitEmail != "" || p.ConfigRepo != "" ||
		changed != 2 {
		t.Errorf("clearing: %d %s, changed %d", w.Code, w.Body, changed)
	}

	for body, field := range map[string]string{
		`{"gitName":"a\nGIT_AUTHOR_NAME=x"}`:             "gitName",
		`{"gitName":"` + strings.Repeat("x", 201) + `"}`: "gitName",
		`{"gitEmail":"no-at"}`:                           "gitEmail",
		`{"gitEmail":"a b@example.com"}`:                 "gitEmail",
		`{"gitEmail":"<a@example.com>"}`:                 "gitEmail",
		`{"language":"english please"}`:                  "language",
		`{"formatLocale":""}`:                            "formatLocale",
		`{"timeZone":"../etc/passwd"}`:                   "timeZone",
		`{"sidebarGrouping":"color"}`:                    "sidebarGrouping",
		`{"configRepo":"not a repo"}`:                    "configRepo",
	} {
		w := call(t, a.UpdateProfile, "PATCH", body)
		var e apitypes.Error
		if err := json.Unmarshal(w.Body.Bytes(), &e); w.Code != 400 || err != nil || e.Field != field {
			t.Errorf("%s: %d %s, want field %s", body, w.Code, w.Body, field)
		}
	}
	if w := call(t, a.UpdateProfile, "PATCH", `{"unknown":1}`); w.Code != 400 {
		t.Errorf("unknown field: %d", w.Code)
	}
	if p, _ := s.Get(t.Context()); p.GitName != "Jan Muster" {
		t.Errorf("a refused update changed the profile: %+v", p)
	}

	// Without hooks an update still answers.
	bare := &API{Store: s}
	if w := call(t, bare.UpdateProfile, "PATCH", `{"gitName":"X"}`); w.Code != 200 {
		t.Errorf("without hooks: %d", w.Code)
	}
	_ = s.DB.Close()
	for _, h := range []http.HandlerFunc{a.GetProfile, a.UpdateProfile} {
		if w := call(t, h, "PATCH", `{"gitName":"Y"}`); w.Code != 500 {
			t.Errorf("closed database: %d", w.Code)
		}
	}
}

// An update whose write fails after the read answers internal, and nothing is pushed.
func TestAPI_updateFails(t *testing.T) {
	a := &API{Store: failingUpdate{openSQL(t)}, Changed: func() { t.Error("pushed after a failed update") }}
	if w := call(t, a.UpdateProfile, "PATCH", `{"gitName":"Y"}`); w.Code != 500 {
		t.Errorf("failed write: %d", w.Code)
	}
}

type failingUpdate struct{ SQL }

func (failingUpdate) Update(context.Context, Patch) (Profile, error) {
	return Profile{}, errors.New("disk full")
}
