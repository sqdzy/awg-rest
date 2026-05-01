package api

import (
	"log/slog"
	"net/http"

	"github.com/awg-rest/awg-rest/internal/auth"
	"github.com/awg-rest/awg-rest/internal/ratelimit"
	"github.com/go-chi/chi/v5"
)

// RouterConfig wires the HTTP handlers and middleware.
type RouterConfig struct {
	Handlers       *Handlers
	Validator      auth.Validator
	Logger         *slog.Logger
	RateLimiter    ratelimit.Limiter
	MetricsHandler http.Handler // Prometheus
}

// NewRouter builds the chi.Router with all middleware applied.
func NewRouter(cfg RouterConfig) http.Handler {
	r := chi.NewRouter()
	r.Use(RequestID)
	r.Use(Recoverer(cfg.Logger))
	r.Use(AccessLog(cfg.Logger))

	// Health endpoints are unauthenticated.
	r.Get("/health/live", cfg.Handlers.HealthLive)
	r.Get("/health/ready", cfg.Handlers.HealthReady)
	if cfg.MetricsHandler != nil {
		r.Method(http.MethodGet, "/metrics", cfg.MetricsHandler)
	}

	rl := RateLimit{
		Limiter: cfg.RateLimiter,
		KeyFn: func(r *http.Request) string {
			if p, err := auth.FromContext(r.Context()); err == nil {
				return p.SubjectID.String() + "|" + chi.URLParam(r, "tenant")
			}
			return r.RemoteAddr
		},
	}

	r.Group(func(r chi.Router) {
		r.Use(Authenticator(cfg.Validator))
		r.With(RequireRoles(auth.RolePlatformAdmin, auth.RoleTenantAdmin, auth.RoleSupportReadOnly, auth.RoleAutomationClient)).
			Get("/v1/operations/{id}", cfg.Handlers.GetOperation)

		r.Route("/v1/tenants/{tenant}/peers", func(r chi.Router) {
			r.With(RequireRoles(auth.RolePlatformAdmin, auth.RoleTenantAdmin, auth.RoleSupportReadOnly, auth.RoleAutomationClient)).
				Get("/{peerID}", cfg.Handlers.GetPeer)
			r.With(RequireRoles(auth.RolePlatformAdmin, auth.RoleTenantAdmin, auth.RoleSupportReadOnly, auth.RoleAutomationClient)).
				Get("/{peerID}/configuration", cfg.Handlers.GetPeerConfiguration)

			r.Group(func(r chi.Router) {
				r.Use(rl.Middleware)
				r.Use(RequireRoles(auth.RoleTenantAdmin, auth.RolePlatformAdmin, auth.RoleAutomationClient))
				r.Post("/", cfg.Handlers.CreatePeer)
				r.Post("/{peerID}:revoke", cfg.Handlers.RevokePeer)
			})
		})
	})

	return r
}
