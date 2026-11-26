package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/trick77/ship/kit/httpx"

	"github.com/trick77/flow/internal/apitypes"
)

var errNotImplemented = apitypes.Fail(apitypes.ErrorCodeNotImplemented, "")

// MaxBody bounds every JSON request body.
const MaxBody = 1 << 20

// WriteJSON answers v as JSON with status.
func WriteJSON(w http.ResponseWriter, status int, v any) { httpx.WriteJSON(w, status, v) }

// WriteError answers err as an apitypes.Error: its code and status when it is one (wrapped or not), internal
// otherwise. The detail of an internal error is logged, never sent.
func WriteError(w http.ResponseWriter, err error) {
	var e *apitypes.Error
	if !errors.As(err, &e) {
		slog.Error("unhandled", "err", err)
		e = apitypes.Fail(apitypes.ErrorCodeInternal, "")
	}
	httpx.WriteJSON(w, e.Code.Status(), e)
}

// Decode reads a JSON body strictly into v: unknown fields, trailing data and oversize bodies are invalid_request.
func Decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, MaxBody+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return apitypes.Fail(apitypes.ErrorCodeInvalidRequest, err.Error())
	}
	if dec.More() {
		return apitypes.Fail(apitypes.ErrorCodeInvalidRequest, "trailing data")
	}
	return nil
}

// hostname is a Host header without its port, lower case.
func hostname(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(host)
}

// fromPreview reports whether a request comes from a page on a preview host: its Origin, else its Referer, names
// such a host. Flow's API refuses those, so a preview page cannot drive the owner's session.
func fromPreview(r *http.Request, isPreview func(host string) bool) bool {
	for _, h := range []string{r.Header.Get("Origin"), r.Header.Get("Referer")} {
		if h == "" || h == "null" {
			continue
		}
		u, err := url.Parse(h)
		if err != nil || u.Host == "" {
			return true // unreadable: refuse rather than guess
		}
		return isPreview(strings.ToLower(u.Hostname()))
	}
	return false
}
