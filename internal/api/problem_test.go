package api

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/awg-rest/awg-rest/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestFromError_Mapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"not_found", domain.ErrNotFound, 404, "not_found"},
		{"unauthorized", domain.ErrUnauthorized, 401, "unauthorized"},
		{"forbidden", domain.ErrForbidden, 403, "forbidden"},
		{"conflict", domain.ErrConflict, 409, "conflict"},
		{"peer_exists", domain.ErrPeerExists, 409, "conflict"},
		{"idempotency", domain.ErrIdempotencyConflict, 409, "idempotency_conflict"},
		{"pool_exhausted", domain.ErrIPPoolExhausted, 409, "ip_pool_exhausted"},
		{"rate_limited", domain.ErrRateLimited, 429, "rate_limited"},
		{"invalid_key", domain.ErrInvalidKey, 400, "invalid_request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := FromError(tc.err)
			require.Equal(t, tc.status, p.Status)
			require.Equal(t, tc.code, p.Code)
		})
	}
}

func TestFromError_ValidationErrors(t *testing.T) {
	t.Parallel()
	ve := domain.ValidationErrors{
		{Field: "name", Code: "required", Message: "name is required"},
	}
	p := FromError(ve)
	require.Equal(t, 422, p.Status)
	require.Equal(t, "validation_failed", p.Code)
	require.Len(t, p.Errors, 1)
}

func TestFromError_UnknownIs500(t *testing.T) {
	t.Parallel()
	p := FromError(errors.New("bizarre"))
	require.Equal(t, 500, p.Status)
}

func TestProblem_WriteSetsContentType(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	FromError(domain.ErrNotFound).Write(rec)
	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	var body map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	require.Equal(t, float64(404), body["status"])
}
