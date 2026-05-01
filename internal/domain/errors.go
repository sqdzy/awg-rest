package domain

import "errors"

// Sentinel errors used across the control plane. They are mapped to problem+json
// types and HTTP status codes by the API layer.
var (
	ErrNotFound            = errors.New("not_found")
	ErrConflict            = errors.New("conflict")
	ErrIdempotencyConflict = errors.New("idempotency_conflict")
	ErrValidation          = errors.New("validation_failed")
	ErrUnauthorized        = errors.New("unauthorized")
	ErrForbidden           = errors.New("forbidden")
	ErrIPPoolExhausted     = errors.New("ip_pool_exhausted")
	ErrPeerExists          = errors.New("peer_exists")
	ErrInvalidProfile      = errors.New("invalid_profile")
	ErrInvalidKey          = errors.New("invalid_key")
	ErrUnsupportedProtocol = errors.New("unsupported_protocol")
	ErrRateLimited         = errors.New("rate_limited")
)

// ValidationError carries a structured field-level error usable in problem+json
// 422 responses.
type ValidationError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (v ValidationError) Error() string { return v.Field + ": " + v.Message }

// ValidationErrors aggregates multiple field-level errors.
type ValidationErrors []ValidationError

func (v ValidationErrors) Error() string {
	if len(v) == 0 {
		return "validation_failed"
	}
	out := v[0].Error()
	for _, e := range v[1:] {
		out += "; " + e.Error()
	}
	return out
}

func (v ValidationErrors) Empty() bool { return len(v) == 0 }
