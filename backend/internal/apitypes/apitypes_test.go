package apitypes

import (
	"net/http"
	"testing"
)

func TestErrorCodeStatus(t *testing.T) {
	want := map[ErrorCode]int{
		ErrorCodeNotImplemented:     http.StatusNotImplemented,
		ErrorCodeUnauthenticated:    http.StatusUnauthorized,
		ErrorCodeForbidden:          http.StatusForbidden,
		ErrorCodePreviewOrigin:      http.StatusForbidden,
		ErrorCodeNotFound:           http.StatusNotFound,
		ErrorCodeInvalidRequest:     http.StatusBadRequest,
		ErrorCodeModeAboveCap:       http.StatusBadRequest,
		ErrorCodeUpstream:           http.StatusBadGateway,
		ErrorCodeSandboxUnavailable: http.StatusServiceUnavailable,
		// The host's bridge is gone, or all running sandboxes hold the memory ceiling: the service, not the
		// request.
		ErrorCodeSandboxNetworkMissing: http.StatusServiceUnavailable,
		ErrorCodeSandboxMemoryFull:     http.StatusServiceUnavailable,
		ErrorCodeInternal:              http.StatusInternalServerError,
		ErrorCodeTooManySessions:       http.StatusTooManyRequests,
	}
	for _, c := range ErrorCodeValues {
		status := c.Status()
		if w, ok := want[c]; ok && status != w {
			t.Errorf("%s: %d, want %d", c, status, w)
		}
		if !ok(want, c) && status != http.StatusConflict {
			t.Errorf("%s: %d, want 409 for a state error", c, status)
		}
	}
}

func ok(m map[ErrorCode]int, c ErrorCode) bool { _, found := m[c]; return found }

func TestError(t *testing.T) {
	if got := Fail(ErrorCodeConflict, "").Error(); got != "conflict" {
		t.Errorf("Error() = %q", got)
	}
	if got := Fail(ErrorCodeUpstream, "github 502").Error(); got != "upstream: github 502" {
		t.Errorf("Error() = %q", got)
	}
}
