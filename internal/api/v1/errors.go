// Package v1 implements Reelix's native HTTP API under /api/v1.
//
// This is the project's own API, consumed by the administration interface and
// by automation. It is independent of the Jellyfin compatibility surface: it
// has its own authentication scheme, its own JSON conventions (camelCase,
// RFC3339 timestamps), and its own DTOs. Nothing here imports
// internal/compat/jellyfin, and nothing there imports this.
package v1

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/maverickman79/reelix/internal/logging"
	"github.com/maverickman79/reelix/internal/repository"
	"github.com/maverickman79/reelix/internal/service"
)

// errorBody is the response shape for every failure.
//
//	{"error": {"code": "invalid_credentials", "message": "..."}}
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error codes. Stable strings, because callers branch on them; the messages
// are for humans and may change.
const (
	codeInvalidRequest     = "invalid_request"
	codeInvalidCredentials = "invalid_credentials"
	codeUnauthenticated    = "unauthenticated"
	codeForbidden          = "forbidden"
	codeNotFound           = "not_found"
	codeConflict           = "conflict"
	codeAlreadySetUp       = "already_set_up"
	codeInternal           = "internal"
)

// loggerFrom returns the request-scoped logger installed by the HTTP
// middleware, so API failures carry the same request_id as the access log.
func loggerFrom(ctx context.Context) *slog.Logger { return logging.FromContext(ctx) }

// writeError maps a service-layer error onto an HTTP response.
//
// Unrecognised errors become a generic 500: the constitution forbids leaking
// stack traces, credentials, filesystem detail, or database internals through
// the API, so the real error is logged and the caller is told nothing beyond
// that something failed.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	log := loggerFrom(r.Context())

	switch {
	case errors.Is(err, service.ErrInvalidArgument):
		writeJSON(w, r, http.StatusBadRequest, errorBody{errorDetail{codeInvalidRequest, err.Error()}})

	case errors.Is(err, service.ErrInvalidCredentials):
		// Deliberately identical for an unknown user and a wrong password.
		writeJSON(w, r, http.StatusUnauthorized,
			errorBody{errorDetail{codeInvalidCredentials, "invalid username or password"}})

	case errors.Is(err, service.ErrUnauthenticated):
		w.Header().Set("WWW-Authenticate", `Bearer realm="reelix"`)
		writeJSON(w, r, http.StatusUnauthorized,
			errorBody{errorDetail{codeUnauthenticated, "authentication required"}})

	case errors.Is(err, service.ErrForbidden):
		writeJSON(w, r, http.StatusForbidden,
			errorBody{errorDetail{codeForbidden, "not permitted"}})

	case errors.Is(err, service.ErrAlreadySetUp):
		writeJSON(w, r, http.StatusConflict,
			errorBody{errorDetail{codeAlreadySetUp, "the server already has an administrator"}})

	case errors.Is(err, repository.ErrNotFound):
		writeJSON(w, r, http.StatusNotFound,
			errorBody{errorDetail{codeNotFound, "not found"}})

	case errors.Is(err, repository.ErrConflict):
		writeJSON(w, r, http.StatusConflict,
			errorBody{errorDetail{codeConflict, "already exists"}})

	default:
		log.Error("request failed",
			slog.String(logging.KeyOperation, "handle"),
			slog.String(logging.KeyError, err.Error()))
		writeJSON(w, r, http.StatusInternalServerError,
			errorBody{errorDetail{codeInternal, "internal server error"}})
	}
}

// writeJSON encodes v as the response body.
//
// An encoding failure after the header is written cannot be corrected on the
// wire, so it is logged rather than returned.
func writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		loggerFrom(r.Context()).Error("encoding response body failed",
			slog.String(logging.KeyOperation, "write_json"),
			slog.String(logging.KeyError, err.Error()))
	}
}

// maxRequestBody bounds how much JSON a handler will read.
//
// Every endpoint here takes a small object; without a limit, an unauthenticated
// endpoint like login is an invitation to stream gigabytes into the process.
const maxRequestBody = 64 << 10

// decodeJSON reads a JSON request body into dst.
//
// Unknown fields are rejected: silently ignoring a misspelled field means a
// caller who typed "paths" as "path" gets a library with no paths and no
// explanation.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody))
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return service.InvalidArgumentf("request body is not valid JSON: %s", err.Error())
	}
	return nil
}

// remarshal re-decodes an already-decoded body into a typed struct.
//
// The metadata patch has to know which keys were PRESENT, because a null and
// an omission mean different things there, and only a map preserves that.
// Decoding twice is the cost of keeping that distinction.
func remarshal(from any, into any) error {
	raw, err := json.Marshal(from)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, into)
}
