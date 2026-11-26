// Package httpapi is the server's HTTP surface: the JSON API under /api, kit's probes, and the embedded UI on every
// other path.
package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/trick77/ship/kit/httpx"
)

// Options wires the handler.
type Options struct {
	// Version is what GET /api/version reports.
	Version string
	// Static serves every path outside /api and the probes: the embedded SPA.
	Static http.Handler
}

// versionResponse is the body of GET /api/version.
type versionResponse struct {
	Version string `json:"version"`
}

// New returns the server's root handler. The access log is outermost, so a panic's line and its 500 carry the
// request id; the probes are not logged.
func New(o Options) http.Handler {
	mux := http.NewServeMux()
	httpx.Health(mux, nil)
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, versionResponse{Version: o.Version})
	})
	mux.Handle("/", o.Static)
	return httpx.Chain(mux,
		httpx.AccessLog(nil, httpx.AccessLogOptions{RoutineLevel: slog.LevelInfo}),
		httpx.Recover(nil),
	)
}
