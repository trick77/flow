package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDev(t *testing.T) {
	w := httptest.NewRecorder()
	Dev{}.GetAuthSession(w, httptest.NewRequest("GET", "/api/auth/session", nil))
	if !strings.Contains(w.Body.String(), `"signedIn":true`) || !strings.Contains(w.Body.String(), `"mode":"dev"`) {
		t.Errorf("session %s", w.Body)
	}
	w = httptest.NewRecorder()
	Dev{}.AuthLogin(w, httptest.NewRequest("GET", "/api/auth/login", nil))
	if w.Code != http.StatusFound {
		t.Errorf("login %d", w.Code)
	}
}

// Dev mode admits whoever reaches loopback, so it must keep browsers' other pages out: a non-loopback Host (DNS
// rebinding) and a cross-origin state change (a page posting to 127.0.0.1) are refused.
func TestDevGuard(t *testing.T) {
	for _, c := range []struct {
		name, method, host, origin string
		want                       int
	}{
		{"loopback read", "GET", "127.0.0.1:8080", "", http.StatusOK},
		{"localhost write, same origin", "POST", "localhost:8080", "http://localhost:8080", http.StatusOK},
		{"write without Origin (curl)", "POST", "127.0.0.1:8080", "", http.StatusOK},
		{"ipv6 loopback", "GET", "[::1]:8080", "", http.StatusOK},
		{"rebound host name", "GET", "evil.example:8080", "", http.StatusForbidden},
		{"rebound write", "POST", "evil.example", "http://evil.example", http.StatusForbidden},
		{"cross-origin write", "POST", "127.0.0.1:8080", "http://evil.example", http.StatusForbidden},
		{"cross-port write", "DELETE", "127.0.0.1:8080", "http://127.0.0.1:9999", http.StatusForbidden},
		{"opaque origin write", "POST", "127.0.0.1:8080", "null", http.StatusForbidden},
		{"unreadable origin", "POST", "127.0.0.1:8080", "%zz", http.StatusForbidden},
	} {
		r := httptest.NewRequest(c.method, "/api/sessions", nil)
		r.Host = c.host
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		w := httptest.NewRecorder()
		Dev{}.Require(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s: %d, want %d", c.name, w.Code, c.want)
		}
	}
}
