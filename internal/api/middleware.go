package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/awg-rest/awg-rest/internal/auth"
	"github.com/awg-rest/awg-rest/internal/domain"
	"github.com/awg-rest/awg-rest/internal/ratelimit"
	"github.com/google/uuid"
)

// reqIDKey is the context key for the request id.
type reqIDKey struct{}

// RequestID generates a UUID per request and exposes it via the X-Request-Id
// response header and the request context.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			id = uuid.NewString()
		}
		ctx := context.WithValue(r.Context(), reqIDKey{}, id)
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestIDFromContext returns the request id stored by RequestID middleware.
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(reqIDKey{}).(string); ok {
		return v
	}
	return ""
}

// AccessLog logs each request with method, path, status, duration and request id.
func AccessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: 200}
			next.ServeHTTP(rec, r)
			logger.LogAttrs(r.Context(), slog.LevelInfo, "http_access",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Duration("duration", time.Since(start)),
				slog.String("request_id", RequestIDFromContext(r.Context())),
			)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(c int) {
	s.status = c
	s.ResponseWriter.WriteHeader(c)
}

// Recoverer turns panics into 500 problem+json.
func Recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.ErrorContext(r.Context(), "panic", "value", rec)
					Problem{Type: "about:blank", Title: "internal_error", Status: 500, Code: "internal_error"}.Write(w)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// Authenticator extracts a Bearer token and validates it via the configured
// auth.Validator. Routes that are exempt should be registered before the
// middleware (e.g. health endpoints).
func Authenticator(v auth.Validator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			if !strings.HasPrefix(h, "Bearer ") {
				WriteError(w, domain.ErrUnauthorized)
				return
			}
			tok := strings.TrimPrefix(h, "Bearer ")
			p, err := v.Validate(r.Context(), tok)
			if err != nil {
				WriteError(w, errors.Join(domain.ErrUnauthorized, err))
				return
			}
			ctx := auth.WithPrincipal(r.Context(), p)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireRoles ensures the authenticated principal carries at least one of
// the given roles.
func RequireRoles(roles ...auth.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, err := auth.FromContext(r.Context())
			if err != nil {
				WriteError(w, domain.ErrUnauthorized)
				return
			}
			if !p.HasAnyRole(roles...) {
				WriteError(w, domain.ErrForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RateLimit applies a per-key token bucket to write paths.
type RateLimit struct {
	Limiter ratelimit.Limiter
	KeyFn   func(*http.Request) string
}

// Middleware returns the http middleware.
func (rl RateLimit) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rl.Limiter == nil {
			next.ServeHTTP(w, r)
			return
		}
		key := r.RemoteAddr
		if rl.KeyFn != nil {
			key = rl.KeyFn(r)
		}
		ok, retryAfter := rl.Limiter.Allow(key)
		if !ok {
			if retryAfter > 0 {
				w.Header().Set("Retry-After", retryAfterSeconds(retryAfter))
			}
			WriteError(w, domain.ErrRateLimited)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func retryAfterSeconds(d time.Duration) string {
	secs := int64(d.Seconds())
	if secs < 1 {
		secs = 1
	}
	return itoa(secs)
}

func itoa(i int64) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	bp := len(b)
	for i > 0 {
		bp--
		b[bp] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		bp--
		b[bp] = '-'
	}
	return string(b[bp:])
}
