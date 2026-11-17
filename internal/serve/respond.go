package serve

import (
	"encoding/json"
	"net/http"

	"github.com/blsmth/agentctl/internal/logging"
)

// Error codes shared by every handler. The web app switches on these, so
// they are part of the API contract (see docs/design/mobile-web-app.md).
const (
	codeForbiddenSource = "forbidden_source"
	codeUnauthorized    = "unauthorized"
	codeCSRF            = "csrf_required"
	codeWriteDisabled   = "write_disabled"
	codeNotFound        = "not_found"
	codeInternal        = "internal"
	codeBadRequest      = "bad_request"
	codePaneUnavailable = "pane_unavailable"
)

// apiError is the JSON body of every error response.
type apiError struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// writeJSON writes v as JSON with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logging.Debug("serve: writing response: %v", err)
	}
}

// writeError writes {"error": msg, "code": code} with the given status.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apiError{Error: msg, Code: code})
}

// writeHTML writes an HTML page with the given status.
func writeHTML(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", cspHTML)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
