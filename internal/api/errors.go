package api

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// ErrorCode values exposed to clients (SPEC §6: error envelope).
const (
	CodeNotFound    = "not_found"
	CodeBadRequest  = "bad_request"
	CodeInternal    = "internal_error"
	CodeUnavailable = "service_unavailable"
	CodeValidation  = "validation_error"
	CodeRateLimited = "rate_limited"
	CodeUnsupported = "unsupported"
	CodeForbidden   = "forbidden"
	CodeConflict    = "conflict"
)

// errorBody is the HTTP error envelope mandated by the plan: the Jsend-like
// schema {"error":{"code","message"}} for all non-2xx responses.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeError emits a structured JSON error response.
func writeError(w http.ResponseWriter, status int, code, format string, args ...any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorBody{
		Error: errorDetail{Code: code, Message: fmt.Sprintf(format, args...)},
	})
}

// writeJSON emits a 200 JSON payload.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
