// Package httpapi is the server's HTTP surface: the JSON API under /api (api/openapi.json, every route registered
// in routes.go), kit's probes, preview hosts, and the embedded UI on every other path.
package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/trick77/ship/kit/httpx"

	"github.com/trick77/flow/internal/apitypes"
)

// Options wires the handler.
type Options struct {
	// Version is what GET /api/version reports.
	Version string
	// Static serves every path outside /api and the probes: the embedded SPA.
	Static http.Handler
	// Areas implement the API; nil ones answer not_implemented.
	Areas Areas
	// Guard admits the owner on every non-public route; nil refuses everything with not_implemented.
	Guard Guard
	// IsPreviewHost reports whether a host name (lower case, no port) is a preview host; nil: there are none.
	IsPreviewHost func(host string) bool
	// Preview serves requests to preview hosts (the reverse proxy); nil answers 404 there.
	Preview http.Handler
}

// New returns the server's root handler. The access log is outermost, so a panic's line and its 500 carry the
// request id; the probes are not logged.
func New(o Options) http.Handler {
	isPreview := o.IsPreviewHost
	if isPreview == nil {
		isPreview = func(string) bool { return false }
	}
	guard := o.Guard
	if guard == nil {
		guard = NotImplemented{}
	}
	preview := o.Preview
	if preview == nil {
		preview = http.NotFoundHandler()
	}

	api := http.NewServeMux()
	api.HandleFunc("GET /api/version", func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, http.StatusOK, apitypes.Version{Version: o.Version})
	})
	register(api, withDefaults(o.Areas), guard)
	api.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		WriteError(w, apitypes.Fail(apitypes.ErrorCodeNotFound, ""))
	})
	guarded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fromPreview(r, isPreview) {
			WriteError(w, apitypes.Fail(apitypes.ErrorCodePreviewOrigin, ""))
			return
		}
		api.ServeHTTP(w, r)
	})

	mux := http.NewServeMux()
	httpx.Health(mux, nil)
	mux.Handle("/api/", guarded)
	mux.Handle("/", o.Static)
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPreview(hostname(r.Host)) {
			preview.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
	return httpx.Chain(root,
		httpx.AccessLog(nil, httpx.AccessLogOptions{RoutineLevel: slog.LevelInfo}),
		httpx.Recover(nil),
	)
}
