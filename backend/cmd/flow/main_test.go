package main

import (
	"context"
	"database/sql"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"testing"

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
	return config.Config{SandboxMemory: 4 << 30, SandboxMemoryTotal: 14 << 30,
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
	if m.MemoryTotal != cfg.SandboxMemoryTotal || m.Memory != cfg.SandboxMemory {
		t.Errorf("memory %d of total %d, want %d of %d", m.Memory, m.MemoryTotal, cfg.SandboxMemory,
			cfg.SandboxMemoryTotal)
	}
	if st, ok := m.Store.(sandboxes.SQL); !ok || st.DB != db {
		t.Errorf("Store = %T, not the server's database", m.Store)
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
