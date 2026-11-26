// Package apitypes holds the browser API's request and response types, generated from api/openapi.json
// (apitypes.gen.go), and the error value handlers return.
package apitypes

import "net/http"

// Error is a value of the generated Error schema that is also a Go error, so a handler can return it.
func (e *Error) Error() string {
	if e.Detail != "" {
		return string(e.Code) + ": " + e.Detail
	}
	return string(e.Code)
}

// Fail returns an *Error with code and an optional detail for logs.
func Fail(code ErrorCode, detail string) *Error { return &Error{Code: code, Detail: detail} }

// Status is the HTTP status that goes with an error code.
func (c ErrorCode) Status() int {
	switch c {
	case ErrorCodeNotImplemented:
		return http.StatusNotImplemented
	case ErrorCodeUnauthenticated:
		return http.StatusUnauthorized
	case ErrorCodeForbidden, ErrorCodePreviewOrigin:
		return http.StatusForbidden
	case ErrorCodeNotFound:
		return http.StatusNotFound
	case ErrorCodeInvalidRequest, ErrorCodeModeAboveCap:
		return http.StatusBadRequest
	case ErrorCodeUpstream:
		return http.StatusBadGateway
	case ErrorCodeSandboxUnavailable:
		return http.StatusServiceUnavailable
	case ErrorCodeInternal:
		return http.StatusInternalServerError
	case ErrorCodeTooManySessions:
		return http.StatusTooManyRequests
	default:
		// conflict and the state errors: the request was fine, the target's state refuses it.
		return http.StatusConflict
	}
}
