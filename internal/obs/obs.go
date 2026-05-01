// Package obs collects logging and metrics setup. OpenTelemetry tracing is
// intentionally hidden behind a function pointer to keep the dependency tree
// small for default builds.
package obs

import (
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// NewLogger returns a slog.Logger configured for the given level.
func NewLogger(level string, jsonFmt bool) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	if jsonFmt {
		h = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(h)
}

// Metrics bundles the standard Prometheus metrics exposed by the control plane.
type Metrics struct {
	Registry *prometheus.Registry

	APIRequests       *prometheus.CounterVec
	APIErrors         *prometheus.CounterVec
	APIDuration       *prometheus.HistogramVec
	IdempotencyReplay prometheus.Counter
	RateRejections    prometheus.Counter
	OutboxPending     prometheus.Gauge
	OutboxLag         prometheus.Gauge
	ReconcileRuns     *prometheus.CounterVec
	ReconcileFailures *prometheus.CounterVec
	RuntimeDriftPeers prometheus.Gauge
	NodeApplyFailures *prometheus.CounterVec
}

// NewMetrics constructs and registers all metrics.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{Registry: reg}

	m.APIRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "api_requests_total", Help: "Number of HTTP requests by route and status."}, []string{"route", "status"})
	m.APIErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "api_errors_total", Help: "Number of API errors by code."}, []string{"code"})
	m.APIDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "api_request_duration_seconds", Help: "Request duration in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"route"})
	m.IdempotencyReplay = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "idempotency_replay_total", Help: "Number of idempotency replays."})
	m.RateRejections = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "rate_limit_rejections_total", Help: "Number of rate-limit rejections."})
	m.OutboxPending = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "outbox_pending_total", Help: "Pending outbox jobs."})
	m.OutboxLag = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "outbox_lag_seconds", Help: "Age of the oldest pending outbox job in seconds."})
	m.ReconcileRuns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "reconcile_runs_total", Help: "Reconcile runs by node."}, []string{"node_id"})
	m.ReconcileFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "reconcile_failures_total", Help: "Reconcile failures by node."}, []string{"node_id"})
	m.RuntimeDriftPeers = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "runtime_drift_peers_total", Help: "Peers whose runtime state drifted from desired."})
	m.NodeApplyFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "node_apply_failures_total", Help: "Per-node apply failures."}, []string{"node_id"})

	reg.MustRegister(
		m.APIRequests, m.APIErrors, m.APIDuration,
		m.IdempotencyReplay, m.RateRejections,
		m.OutboxPending, m.OutboxLag,
		m.ReconcileRuns, m.ReconcileFailures,
		m.RuntimeDriftPeers, m.NodeApplyFailures,
	)
	return m
}

// HTTPHandler returns the Prometheus scrape handler.
func (m *Metrics) HTTPHandler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{Registry: m.Registry})
}
