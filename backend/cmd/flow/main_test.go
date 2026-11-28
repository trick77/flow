package main

import (
	"context"
	"database/sql"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/trick77/ship/kit/sse"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/config"
	"github.com/trick77/flow/internal/httpapi"
	"github.com/trick77/flow/internal/metering"
	"github.com/trick77/flow/internal/projects"
	"github.com/trick77/flow/internal/sandbox"
	"github.com/trick77/flow/internal/sandboxes"
	"github.com/trick77/flow/internal/store"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "flow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// The running server's session service feeds the metering ledger on Flow's database.
func TestSessionServiceFeedsTheLedger(t *testing.T) {
	db := testDB(t)
	svc := sessionService(db, projects.SQL{DB: db}, nil, nil)
	rec, ok := svc.Meter.(*metering.Recorder)
	if !ok || rec == nil {
		t.Fatalf("Meter = %T, want *metering.Recorder", svc.Meter)
	}
	if st, ok := rec.Store.(metering.SQL); !ok || st.DB != db {
		t.Errorf("recorder store %T, not the server's database", rec.Store)
	}
}

func testConfig() config.Config {
	return config.Config{SandboxMemory: 4 << 30, SandboxMemoryTotal: 14 << 30, SandboxMemoryHeadroom: 3 << 30,
		SandboxNetwork: sandbox.Network{Bridge: config.DefaultSandboxBridge,
			Subnet: netip.MustParsePrefix(config.DefaultSandboxSubnet),
			Range:  netip.MustParsePrefix(config.DefaultSandboxRange)}}
}

// The sandbox manager keeps addresses in the sandbox rows on the server's database and holds the configured
// memory ceiling for all running sandboxes.
func TestSandboxManagerWiring(t *testing.T) {
	db := testDB(t)
	cfg := testConfig()
	m := sandboxManager(cfg, db, projects.SQL{DB: db}, nil, nil, nil)
	addrs, ok := m.Addresses.(sandboxes.SQLAddresses)
	if !ok || addrs.DB != db || addrs.Network.Range != cfg.SandboxNetwork.Range {
		t.Errorf("Addresses = %#v, want SQLAddresses on the server's database and network", m.Addresses)
	}
	if m.MemoryTotal != cfg.SandboxMemoryTotal || m.Memory != cfg.SandboxMemory ||
		m.MemoryHeadroom != cfg.SandboxMemoryHeadroom {
		t.Errorf("memory %d, total %d, headroom %d, want %d, %d, %d", m.Memory, m.MemoryTotal, m.MemoryHeadroom,
			cfg.SandboxMemory, cfg.SandboxMemoryTotal, cfg.SandboxMemoryHeadroom)
	}
	if st, ok := m.Store.(sandboxes.SQL); !ok || st.DB != db {
		t.Errorf("Store = %T, not the server's database", m.Store)
	}
}

// A sandbox change goes out with the row's state; a row that cannot be read publishes nothing rather than none.
func TestSandboxChanged(t *testing.T) {
	db := testDB(t)
	ps := projects.SQL{DB: db}
	if err := ps.Create(t.Context(), projects.Project{ID: "p1", Name: "demo"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO sandboxes (id, project_id, instance, state)
		VALUES ('sb1', 'p1', 'flow-1', 'warm')`); err != nil {
		t.Fatal(err)
	}
	hub := sse.NewHub[apitypes.AppEvent](sse.HubOptions{})
	defer hub.Close()
	ch, cancel, _ := hub.Subscribe("t")
	defer cancel()
	m := sandboxManager(testConfig(), db, ps, nil, nil, hub)
	m.Changed("p1")
	select {
	case e := <-ch:
		if e.Kind != apitypes.AppEventKindSandboxUpdated || e.Sandbox == nil || e.Sandbox.State != apitypes.SandboxStateWarm {
			t.Errorf("event %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no event")
	}
	_ = db.Close()
	m.Changed("p1")
	select {
	case e := <-ch:
		t.Errorf("published on a read error: %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
}

// Nothing of the browser API or the UI is served to a request that arrives over the sandbox network, by its
// remote or its local address; everything else is.
func TestHandlerRefusesTheSandboxNetwork(t *testing.T) {
	h := handler(testConfig(), httpapi.Options{Version: "test", Static: http.NotFoundHandler()})
	do := func(remote string, local net.Addr) int {
		r := httptest.NewRequest("GET", "/api/version", nil)
		r.RemoteAddr = remote
		if local != nil {
			r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, local))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if c := do("192.0.2.10:40000", nil); c != http.StatusOK {
		t.Errorf("from outside: %d", c)
	}
	if c := do("172.30.128.5:40000", nil); c != http.StatusForbidden {
		t.Errorf("from a sandbox: %d", c)
	}
	onSubnet := &net.TCPAddr{IP: net.ParseIP("172.30.0.2"), Port: 8080}
	if c := do("192.0.2.10:40000", onSubnet); c != http.StatusForbidden {
		t.Errorf("on Flow's sandbox-network address: %d", c)
	}
}

// TestSerialChanges forces the interleaving that left a stale state shown: a change that read the old state is
// held between its read and its publish while a newer change comes. Serialized, the newer one cannot read before
// the older one published, so the last event published is the newest state.
func TestSerialChanges(t *testing.T) {
	var mu sync.Mutex
	busy := false
	var published []bool
	read := make(chan struct{}, 2)
	hold := make(chan struct{})
	first := true
	lookup := func(context.Context, string) (apitypes.Sandbox, error) {
		mu.Lock()
		sb := apitypes.Sandbox{State: apitypes.SandboxStateSetup, Busy: busy}
		wait := first
		first = false
		mu.Unlock()
		read <- struct{}{}
		if wait {
			<-hold // the older change, between its read and its publish
		}
		return sb, nil
	}
	publish := func(e apitypes.AppEvent) {
		mu.Lock()
		defer mu.Unlock()
		published = append(published, e.Sandbox.Busy)
	}
	changed := serialChanges(lookup, publish)

	done := make(chan struct{})
	go func() { changed("p1"); done <- struct{}{} }()
	<-read // the older change read busy false and is held
	mu.Lock()
	busy = true
	mu.Unlock()
	go func() { changed("p1"); done <- struct{}{} }()
	select {
	case <-read:
		t.Fatal("the newer change read while the older one had not published")
	case <-time.After(50 * time.Millisecond):
	}
	close(hold)
	<-done
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(published) != 2 || published[0] || !published[1] {
		t.Errorf("published busy %v, want [false true]", published)
	}
}
