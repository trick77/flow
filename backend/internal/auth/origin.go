package auth

import (
	"net/http"
	"net/url"
	"strings"
)

// sameOrigin is the one origin rule of both modes (package comment); only match and allowNone differ, so no mode
// can end up with a laxer copy of it. match judges an origin a browser named, "scheme://host".
//   - A present Origin must match, on every method: a WebSocket handshake is a GET and has no CORS.
//   - A state change without Origin is judged by its Referer's origin.
//   - A state change with neither passes only when allowNone (dev mode: curl on the machine).
func sameOrigin(r *http.Request, match func(origin string) bool, allowNone bool) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		if !changes(r.Method) {
			return true
		}
		referer := r.Header.Get("Referer")
		if referer == "" {
			return allowNone
		}
		origin = refererOrigin(referer)
	}
	return origin != "" && match(origin)
}

// changes reports whether a method changes state.
func changes(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

// refererOrigin is a Referer's scheme://host, "" when there is none to read.
func refererOrigin(referer string) string {
	u, err := url.Parse(referer)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// isOrigin matches exactly one origin, case-insensitively (oidc mode: the public URL).
func isOrigin(want string) func(string) bool {
	return func(origin string) bool { return strings.EqualFold(origin, want) }
}

// hostOrigin matches an http or https origin naming host (dev mode: the Host the request was sent to).
func hostOrigin(host string) func(string) bool {
	return func(origin string) bool {
		u, err := url.Parse(origin)
		return err == nil && (u.Scheme == "http" || u.Scheme == "https") && strings.EqualFold(u.Host, host)
	}
}
