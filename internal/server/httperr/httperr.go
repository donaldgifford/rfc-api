// Package httperr maps domain errors to RFC 7807 problem+json HTTP
// responses. The single-entry Write function is called from every
// handler and from catch-all middlewares so error shape is uniform
// across the API.
//
// See DESIGN-0001 §Error handling for the mapping table and
// RFC 7807 for the response shape.
package httperr

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/donaldgifford/rfc-api/internal/domain"
	"github.com/donaldgifford/rfc-api/internal/server/reqctx"
)

// contentType is the RFC 7807 media type. Always set on responses
// written by Write, even on 500s.
const contentType = "application/problem+json"

// Problem is the RFC 7807 problem+json body. Includes the rfc-api
// extension field request_id for log correlation.
type Problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail,omitempty"`
	Instance  string `json:"instance,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// classification pairs a domain error with its HTTP status + problem
// type URI + safe client-facing title.
type classification struct {
	status int
	kind   string
	title  string
}

// classify maps a domain error to its HTTP classification. Unknown
// errors default to 500 / internal.
//
// errors.Is(..., domain.ErrFoo) lets services wrap with %w and still
// get the right response code.
func classify(err error) classification {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return classification{http.StatusNotFound, "/problems/not-found", "Resource not found"}
	case errors.Is(err, domain.ErrInvalidInput):
		return classification{http.StatusBadRequest, "/problems/invalid-input", "Invalid input"}
	case errors.Is(err, domain.ErrConflict):
		return classification{http.StatusConflict, "/problems/conflict", "Conflict"}
	case errors.Is(err, domain.ErrUpstream):
		return classification{http.StatusBadGateway, "/problems/upstream", "Upstream failure"}
	case errors.Is(err, domain.ErrUnauthenticated):
		return classification{http.StatusUnauthorized, "/problems/unauthenticated", "Unauthenticated"}
	case errors.Is(err, domain.ErrRateLimited):
		return classification{http.StatusTooManyRequests, "/problems/rate-limited", "Rate limit exceeded"}
	default:
		return classification{http.StatusInternalServerError, "/problems/internal", "Internal error"}
	}
}

// safeDetail returns a client-safe detail string for the given error.
// Classified errors expose their message; unclassified (500) errors
// do NOT -- their detail is a fixed string to avoid leaking paths,
// SQL, or stack traces. Full error is still logged server-side under
// the request id.
func safeDetail(err error, cls classification) string {
	if cls.status == http.StatusInternalServerError {
		return "an internal error occurred"
	}
	return err.Error()
}

// Write writes an RFC 7807 problem+json response for err. Safe to
// call from any handler or middleware. Always sets Content-Type
// and the rfc-api X-Request-ID response header (when a request id
// is set on the context).
//
// Callers must not write to w after calling Write.
func Write(w http.ResponseWriter, r *http.Request, err error) {
	cls := classify(err)

	// Log the full error server-side under the request id so the safe
	// client detail doesn't erase operator-visible context.
	logger := slog.Default()
	logger.ErrorContext(r.Context(), "http error",
		"http.response.status_code", cls.status,
		"problem.type", cls.kind,
		"request_id", reqctx.ID(r.Context()),
		"err", err.Error(),
	)

	problem := Problem{
		Type:      cls.kind,
		Title:     cls.title,
		Status:    cls.status,
		Detail:    safeDetail(err, cls),
		Instance:  r.URL.RequestURI(),
		RequestID: reqctx.ID(r.Context()),
	}

	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(cls.status)
	if encErr := json.NewEncoder(w).Encode(problem); encErr != nil {
		// Response is already in flight; best we can do is log.
		logger.ErrorContext(r.Context(), "encode problem body",
			"err", encErr.Error(),
		)
	}
}
