package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trick77/flow/internal/apitypes"
)

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	static := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, "SPA")
	})
	srv := httptest.NewServer(New(Options{Version: "1.2.3", Static: static}))
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, url string) (int, string, http.Header) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return resp.StatusCode, string(b), resp.Header
}

func TestVersion(t *testing.T) {
	srv := newServer(t)
	code, body, h := get(t, srv.URL+"/api/version")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if ct := h.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var got apitypes.Version
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	if got.Version != "1.2.3" {
		t.Errorf("version = %q, want 1.2.3", got.Version)
	}
}

// The test server's client connects from loopback, the only caller kit's probes answer.
func TestHealthz(t *testing.T) {
	srv := newServer(t)
	if code, body, _ := get(t, srv.URL+"/healthz"); code != http.StatusOK || body != "ok\n" {
		t.Errorf("/healthz = %d %q, want 200 ok", code, body)
	}
	if code, _, _ := get(t, srv.URL+"/readyz"); code != http.StatusOK {
		t.Errorf("/readyz = %d, want 200", code)
	}
}

func TestStaticServesTheRest(t *testing.T) {
	srv := newServer(t)
	if code, body, _ := get(t, srv.URL+"/sessions/42"); code != http.StatusOK || body != "SPA" {
		t.Errorf("/sessions/42 = %d %q, want the SPA", code, body)
	}
	if code, _, _ := get(t, srv.URL+"/api/nope"); code != http.StatusNotFound {
		t.Errorf("/api/nope = %d, want 404", code)
	}
}

func TestPanicIsA500(t *testing.T) {
	boom := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })
	srv := httptest.NewServer(New(Options{Version: "1.2.3", Static: boom}))
	t.Cleanup(srv.Close)
	if code, _, _ := get(t, srv.URL+"/"); code != http.StatusInternalServerError {
		t.Errorf("panicking handler = %d, want 500", code)
	}
}

func TestRequestIDHeader(t *testing.T) {
	srv := newServer(t)
	if _, _, h := get(t, srv.URL+"/api/version"); h.Get("X-Request-Id") == "" {
		t.Error("no X-Request-Id on the response: the access log is not wired")
	}
}
