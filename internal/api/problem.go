package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/awg-rest/awg-rest/internal/domain"
)

// Problem implements RFC 9457 problem details for HTTP APIs.
type Problem struct {
	Type     string                    `json:"type"`
	Title    string                    `json:"title"`
	Status   int                       `json:"status"`
	Detail   string                    `json:"detail,omitempty"`
	Instance string                    `json:"instance,omitempty"`
	Code     string                    `json:"code,omitempty"`
	Errors   []domain.ValidationError  `json:"errors,omitempty"`
	Extra    map[string]any            `json:"-"`
}

// Write encodes the problem with content-type application/problem+json.
func (p Problem) Write(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

// FromError maps an error to a Problem; if the error carries a known sentinel
// from the domain package, the mapping is well-defined.
func FromError(err error) Problem {
	if err == nil {
		return Problem{Status: http.StatusInternalServerError, Title: "internal_error"}
	}
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return Problem{Type: "about:blank", Title: "not_found", Status: 404, Code: "not_found", Detail: err.Error()}
	case errors.Is(err, domain.ErrUnauthorized):
		return Problem{Type: "about:blank", Title: "unauthorized", Status: 401, Code: "unauthorized", Detail: err.Error()}
	case errors.Is(err, domain.ErrForbidden):
		return Problem{Type: "about:blank", Title: "forbidden", Status: 403, Code: "forbidden", Detail: err.Error()}
	case errors.Is(err, domain.ErrIdempotencyConflict):
		return Problem{Type: "about:blank", Title: "idempotency_conflict", Status: 409, Code: "idempotency_conflict", Detail: err.Error()}
	case errors.Is(err, domain.ErrConflict),
		errors.Is(err, domain.ErrPeerExists):
		return Problem{Type: "about:blank", Title: "conflict", Status: 409, Code: "conflict", Detail: err.Error()}
	case errors.Is(err, domain.ErrIPPoolExhausted):
		return Problem{Type: "about:blank", Title: "ip_pool_exhausted", Status: 409, Code: "ip_pool_exhausted", Detail: err.Error()}
	case errors.Is(err, domain.ErrRateLimited):
		return Problem{Type: "about:blank", Title: "rate_limited", Status: 429, Code: "rate_limited", Detail: err.Error()}
	case errors.Is(err, domain.ErrInvalidKey),
		errors.Is(err, domain.ErrInvalidProfile),
		errors.Is(err, domain.ErrUnsupportedProtocol):
		return Problem{Type: "about:blank", Title: "invalid_request", Status: 400, Code: "invalid_request", Detail: err.Error()}
	}
	var ve domain.ValidationErrors
	if errors.As(err, &ve) {
		return Problem{
			Type: "about:blank", Title: "validation_failed", Status: 422,
			Code: "validation_failed", Errors: ve, Detail: ve.Error(),
		}
	}
	return Problem{Type: "about:blank", Title: "internal_error", Status: 500, Code: "internal_error", Detail: err.Error()}
}

// WriteError is a convenience wrapper.
func WriteError(w http.ResponseWriter, err error) {
	FromError(err).Write(w)
}
