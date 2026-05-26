// Package config loads runtime configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config carries all runtime tuning for the API and worker.
type Config struct {
	HTTPAddr    string
	MetricsAddr string
	DatabaseURL string

	JWTSecret       []byte
	JWTPublicKeyPEM []byte
	JWTIssuer       string
	JWTAudience     string
	JWTAllowedAlgs  []string

	IdempotencyTTL time.Duration

	RateCapacity   int
	RateRefillPerS float64

	NodeAgentURL     string
	ClientDNS        []string
	ClientAllowedIPs []string

	// Embedded worker (API + worker in one process).
	EnableEmbeddedWorker bool
	EmbeddedWorkerExec   string // "fake" | "cli" | "remote"
	BootstrapConfigDir   string
	ReconcileOnStart     bool

	LogLevel string
	LogJSON  bool
}

// Load reads env, returning a Config with sane defaults.
func Load() (*Config, error) {
	databaseURL, err := envOrFile("DATABASE_URL", "DATABASE_URL_FILE", "postgres://postgres:postgres@localhost:5432/awg?sslmode=disable")
	if err != nil {
		return nil, err
	}
	jwtSecret, err := envOrFile("JWT_SECRET", "JWT_SECRET_FILE", "")
	if err != nil {
		return nil, err
	}
	jwtSecret = strings.TrimSpace(jwtSecret)
	publicKey, err := envOrFile("JWT_PUBLIC_KEY_PEM", "JWT_PUBLIC_KEY_FILE", "")
	if err != nil {
		return nil, err
	}
	publicKeyPEM := []byte(publicKey)
	allowedDefault := "HS256"
	if len(publicKeyPEM) > 0 {
		allowedDefault = "RS256,ES256,EdDSA"
	}
	c := &Config{
		HTTPAddr:             env("HTTP_ADDR", ":18080"),
		MetricsAddr:          env("METRICS_ADDR", ":9090"),
		DatabaseURL:          databaseURL,
		JWTSecret:            []byte(jwtSecret),
		JWTPublicKeyPEM:      publicKeyPEM,
		JWTIssuer:            env("JWT_ISSUER", "awg-rest"),
		JWTAudience:          env("JWT_AUDIENCE", "awg-control-plane"),
		JWTAllowedAlgs:       splitCSV(env("JWT_ALLOWED_ALGS", allowedDefault)),
		IdempotencyTTL:       envDuration("IDEMPOTENCY_TTL", 24*time.Hour),
		RateCapacity:         envInt("RATE_CAPACITY", 60),
		RateRefillPerS:       envFloat("RATE_REFILL_PER_S", 1.0),
		NodeAgentURL:         env("NODE_AGENT_URL", "http://127.0.0.1:8081"),
		ClientDNS:            splitCSV(env("CLIENT_DNS", "1.1.1.1,1.0.0.1")),
		ClientAllowedIPs:     splitCSV(env("CLIENT_ALLOWED_IPS", "")),
		EnableEmbeddedWorker: envBool("ENABLE_EMBEDDED_WORKER", false),
		EmbeddedWorkerExec:   env("EMBEDDED_WORKER_EXEC", "fake"),
		BootstrapConfigDir:   env("BOOTSTRAP_CONF_DIR", ""),
		ReconcileOnStart:     envBool("RECONCILE_ON_START", true),
		LogLevel:             env("LOG_LEVEL", "info"),
		LogJSON:              envBool("LOG_JSON", true),
	}
	return c, nil
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return def
}

func envOrFile(k, fileK, def string) (string, error) {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return strings.ReplaceAll(v, `\n`, "\n"), nil
	}
	if path, ok := os.LookupEnv(fileK); ok && path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read %s=%s: %w", fileK, path, err)
		}
		return string(b), nil
	}
	return def, nil
}

func splitCSV(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envInt(k string, def int) int {
	if v, ok := os.LookupEnv(k); ok {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}

func envFloat(k string, def float64) float64 {
	if v, ok := os.LookupEnv(k); ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func envBool(k string, def bool) bool {
	if v, ok := os.LookupEnv(k); ok {
		switch strings.ToLower(v) {
		case "1", "true", "yes":
			return true
		case "0", "false", "no":
			return false
		}
	}
	return def
}

func envDuration(k string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(k); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
