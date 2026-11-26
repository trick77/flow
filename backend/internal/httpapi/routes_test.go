package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/trick77/flow/internal/apitypes"
)

type specOp struct {
	OperationID string            `json:"operationId"`
	Area        string            `json:"x-flow-area"`
	Security    []json.RawMessage `json:"security"`
	HasSecurity bool              `json:"-"`
}

// specRoutes reads every operation of api/openapi.json as "METHOD path operationId area public".
func specRoutes(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	var out []string
	for path, item := range doc.Paths {
		for method, raw := range item {
			if method == "parameters" {
				continue
			}
			var op specOp
			if err := json.Unmarshal(raw, &op); err != nil {
				t.Fatal(err)
			}
			var keys map[string]json.RawMessage
			_ = json.Unmarshal(raw, &keys)
			_, hasSecurity := keys["security"]
			public := hasSecurity && len(op.Security) == 0
			out = append(out, fmt.Sprintf("%s %s %s %s %t", strings.ToUpper(method), path, op.OperationID, op.Area, public))
		}
	}
	slices.Sort(out)
	return out
}

// The registration table and the spec name the same operations, areas and public routes. getVersion is the one
// route httpapi answers itself.
func TestRoutesMatchTheSpec(t *testing.T) {
	var table []string
	for _, r := range Routes {
		table = append(table, fmt.Sprintf("%s %s %s %s %t", r.Method, r.Path, r.OperationID, r.Area, r.Public))
	}
	table = append(table, "GET /api/version getVersion meta true")
	slices.Sort(table)
	spec := specRoutes(t)
	for _, s := range spec {
		if !slices.Contains(table, s) {
			t.Errorf("in the spec, not registered: %s", s)
		}
	}
	for _, r := range table {
		if !slices.Contains(spec, r) {
			t.Errorf("registered, not in the spec: %s", r)
		}
	}
}

// passGuard admits everyone, so the tests reach the handlers behind it.
type passGuard struct{}

func (passGuard) Require(next http.Handler) http.Handler { return next }

func call(t *testing.T, h http.Handler, method, path string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) apitypes.ErrorCode {
	t.Helper()
	var e apitypes.Error
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return e.Code
}

// concrete fills a pattern's wildcards.
func concrete(path string) string {
	r := strings.NewReplacer("{projectId}", "p1", "{repoId}", "r1", "{sessionId}", "s1", "{messageId}", "m1",
		"{requestId}", "q1", "{terminalId}", "t1", "{keyId}", "k1", "{port}", "5173")
	return r.Replace(path)
}

func TestEveryRouteAnswersNotImplemented(t *testing.T) {
	for _, guard := range []Guard{nil, passGuard{}} {
		h := New(Options{Static: http.NotFoundHandler(), Guard: guard})
		for _, r := range Routes {
			rec := call(t, h, r.Method, concrete(r.Path), nil)
			if rec.Code != http.StatusNotImplemented || errorCode(t, rec) != apitypes.ErrorCodeNotImplemented {
				t.Errorf("%s %s (guard %T) = %d %s", r.Method, r.Path, guard, rec.Code, rec.Body)
			}
		}
	}
}

// An area's implementation is reached through the table, behind the Guard.
type projectsImpl struct{ NotImplemented }

func (projectsImpl) GetProject(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, apitypes.ProjectRepo{ID: r.PathValue("projectId")})
}

func TestAreasAreWired(t *testing.T) {
	h := New(Options{Static: http.NotFoundHandler(), Guard: passGuard{}, Areas: Areas{Projects: projectsImpl{}}})
	rec := call(t, h, "GET", "/api/projects/p7", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"p7"`) {
		t.Errorf("GET /api/projects/p7 = %d %s", rec.Code, rec.Body)
	}
	rec = call(t, h, "GET", "/api/nothing/here", nil)
	if rec.Code != http.StatusNotFound || errorCode(t, rec) != apitypes.ErrorCodeNotFound {
		t.Errorf("unknown API path = %d %s", rec.Code, rec.Body)
	}
}

func TestPreviewOriginIsRefused(t *testing.T) {
	isPreview := func(host string) bool { return strings.HasSuffix(host, ".flow.example.com") }
	h := New(Options{Static: http.NotFoundHandler(), Guard: passGuard{}, IsPreviewHost: isPreview,
		Preview: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("proxied")) })})
	for _, hdr := range []http.Header{
		{"Origin": {"https://flow-k3f9-5173.flow.example.com"}},
		{"Referer": {"https://FLOW-K3F9-5173.flow.example.com/app"}},
		{"Origin": {"::not a url"}},
	} {
		rec := call(t, h, "GET", "/api/version", hdr)
		if rec.Code != http.StatusForbidden || errorCode(t, rec) != apitypes.ErrorCodePreviewOrigin {
			t.Errorf("%v: %d %s", hdr, rec.Code, rec.Body)
		}
	}
	for _, hdr := range []http.Header{nil, {"Origin": {"https://flow.example.com"}}, {"Origin": {"null"}}} {
		if rec := call(t, h, "GET", "/api/version", hdr); rec.Code != http.StatusOK {
			t.Errorf("%v: %d", hdr, rec.Code)
		}
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "flow-k3f9-5173.flow.example.com:443"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Body.String() != "proxied" {
		t.Errorf("preview host = %q, want the preview handler", rec.Body)
	}
	req.Host = "x.flow.example.com"
	rec = httptest.NewRecorder()
	New(Options{Static: http.NotFoundHandler(), IsPreviewHost: isPreview}).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("preview host without a proxy = %d", rec.Code)
	}
}

func TestDecode(t *testing.T) {
	var v apitypes.ProjectCreate
	for body, ok := range map[string]bool{
		`{"name":"a","repos":[]}`:             true,
		`{"name":"a","extra":1}`:              false,
		`{"name":"a"} {}`:                     false,
		`[`:                                   false,
		strings.Repeat(" ", MaxBody+2) + `{}`: false,
	} {
		err := Decode(httptest.NewRequest("POST", "/", strings.NewReader(body)), &v)
		var e *apitypes.Error
		if ok != (err == nil) || (err != nil && (!errors.As(err, &e) || e.Code != apitypes.ErrorCodeInvalidRequest)) {
			t.Errorf("Decode(%.20q) = %v", body, err)
		}
	}
}

func TestWriteError(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, fmt.Errorf("wrapped: %w", apitypes.Fail(apitypes.ErrorCodeModeAboveCap, "ask > plan")))
	if rec.Code != http.StatusBadRequest || errorCode(t, rec) != apitypes.ErrorCodeModeAboveCap {
		t.Errorf("wrapped API error = %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	WriteError(rec, errors.New("disk on fire"))
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "fire") {
		t.Errorf("internal error = %d %s, want 500 without the detail", rec.Code, rec.Body)
	}
}
