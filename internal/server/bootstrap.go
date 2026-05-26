// Package server provides shared bootstrap helpers for the cmd binaries.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/awg-rest/awg-rest/internal/api"
	"github.com/awg-rest/awg-rest/internal/auth"
	"github.com/awg-rest/awg-rest/internal/bootstrap"
	"github.com/awg-rest/awg-rest/internal/config"
	"github.com/awg-rest/awg-rest/internal/obs"
	"github.com/awg-rest/awg-rest/internal/ratelimit"
	"github.com/awg-rest/awg-rest/internal/repo"
)

// BuildAPI assembles the HTTP API server from configuration.
type Built struct {
	Logger    *slog.Logger
	Metrics   *obs.Metrics
	DB        *repo.DB
	Service   *api.Service
	Validator auth.Validator
	Server    *http.Server
}

// BuildAPI assembles the API binary's HTTP server.
func BuildAPI(ctx context.Context, cfg *config.Config) (*Built, error) {
	logger := obs.NewLogger(cfg.LogLevel, cfg.LogJSON)
	metrics := obs.NewMetrics()

	db, err := repo.NewDB(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("db: %w", err)
	}
	if err := repo.Migrate(ctx, db.Pool); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := bootstrap.RunIfEmpty(ctx, db, bootstrap.EnvDefaults(), logger); err != nil {
		db.Close()
		return nil, fmt.Errorf("bootstrap: %w", err)
	}

	svc := &api.Service{
		DB:               db,
		Tenants:          &repo.Tenants{DB: db},
		Nodes:            &repo.Nodes{DB: db},
		Profiles:         &repo.Profiles{DB: db},
		Peers:            &repo.Peers{DB: db},
		Operations:       &repo.Operations{DB: db},
		Outbox:           &repo.Outbox{DB: db},
		Idem:             &repo.Idempotency{DB: db},
		Audit:            &repo.Audit{DB: db},
		IdempotencyTTL:   cfg.IdempotencyTTL,
		ClientDNS:        cfg.ClientDNS,
		ClientAllowedIPs: cfg.ClientAllowedIPs,
	}
	handlers := &api.Handlers{Service: svc}
	validator, err := buildJWTValidator(cfg)
	if err != nil {
		db.Close()
		return nil, err
	}
	limiter := ratelimit.NewTokenBucket(cfg.RateCapacity, cfg.RateRefillPerS)

	router := api.NewRouter(api.RouterConfig{
		Handlers:       handlers,
		Validator:      validator,
		Logger:         logger,
		RateLimiter:    limiter,
		MetricsHandler: metrics.HTTPHandler(),
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return &Built{Logger: logger, Metrics: metrics, DB: db, Service: svc, Validator: validator, Server: srv}, nil
}

func buildJWTValidator(cfg *config.Config) (auth.Validator, error) {
	if len(cfg.JWTPublicKeyPEM) > 0 {
		return auth.NewStaticKeyValidatorFromPEM(cfg.JWTPublicKeyPEM, cfg.JWTIssuer, cfg.JWTAudience, cfg.JWTAllowedAlgs)
	}
	if len(cfg.JWTSecret) == 0 {
		return nil, fmt.Errorf("JWT_SECRET or JWT_PUBLIC_KEY_FILE must be set")
	}
	return &auth.HMACValidator{
		Secret:      cfg.JWTSecret,
		Issuer:      cfg.JWTIssuer,
		Audience:    cfg.JWTAudience,
		AllowedAlgs: cfg.JWTAllowedAlgs,
		Leeway:      30 * time.Second,
	}, nil
}
