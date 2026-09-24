package asana

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// APIError is a non-2xx response from Asana. It never contains request
// headers, so the access token cannot leak through it.
type APIError struct {
	Op      string // e.g. "list users page 2"
	Status  int
	Message string // first errors[].message from the body, if any
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	return fmt.Sprintf("%s: asana API returned %d: %s%s", e.Op, e.Status, msg, hint(e.Status))
}

func hint(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return " (check ASANA_ACCESS_TOKEN)"
	case http.StatusForbidden, http.StatusNotFound:
		return " (check ASANA_WORKSPACE_GID and that the token's user can access it)"
	case http.StatusPaymentRequired:
		return " (feature not available on this Asana plan)"
	}
	return ""
}

func newAPIError(op string, status int, body []byte) *APIError {
	var parsed struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	e := &APIError{Op: op, Status: status}
	if json.Unmarshal(body, &parsed) == nil && len(parsed.Errors) > 0 {
		e.Message = truncate(strings.TrimSpace(parsed.Errors[0].Message), 300)
	}
	return e
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func retryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	return false
}
