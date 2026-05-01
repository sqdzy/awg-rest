package nodeagent

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/awg-rest/awg-rest/internal/awg"
	"github.com/go-chi/chi/v5"
)

// ServerConfig configures the agent HTTP server.
type ServerConfig struct {
	Addr              string
	Executor          awg.Executor
	Logger            *slog.Logger
	TLSCertFile       string
	TLSKeyFile        string
	ClientCAs         string // path to CA bundle; required for mTLS
	AllowInsecureHTTP bool
}

// NewServer builds the agent HTTP handler and (when TLS material is present)
// the *http.Server with mTLS enforced.
func NewServer(cfg ServerConfig) (*http.Server, error) {
	if cfg.Executor == nil {
		return nil, errors.New("nodeagent: executor is required")
	}
	r := chi.NewRouter()
	r.Use(loggingMiddleware(cfg.Logger))

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	r.Route("/v1/iface/{iface}", func(r chi.Router) {
		r.Post("/syncconf", syncConfHandler(cfg.Executor))
		r.Post("/peers", setPeerHandler(cfg.Executor))
		r.Delete("/peers/{pub}", removePeerHandler(cfg.Executor))
		r.Get("/dump", dumpHandler(cfg.Executor))
		r.Get("/showconf", showconfHandler(cfg.Executor))
		r.Post("/up", upHandler(cfg.Executor))
		r.Post("/down", downHandler(cfg.Executor))
	})

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	hasTLSConfig := cfg.TLSCertFile != "" || cfg.TLSKeyFile != "" || cfg.ClientCAs != ""
	if hasTLSConfig {
		if cfg.TLSCertFile == "" || cfg.TLSKeyFile == "" || cfg.ClientCAs == "" {
			return nil, errors.New("nodeagent: AGENT_TLS_CERT, AGENT_TLS_KEY, and AGENT_CLIENT_CA_BUNDLE are all required for production mTLS")
		}
		tc, err := buildTLSConfig(cfg)
		if err != nil {
			return nil, err
		}
		srv.TLSConfig = tc
	} else if !cfg.AllowInsecureHTTP {
		return nil, errors.New("nodeagent: plain HTTP is disabled; set AllowInsecureHTTP only for dev/test")
	}
	return srv, nil
}

// ListenAndServe starts the agent. If TLS is configured it uses ListenAndServeTLS,
// otherwise plain HTTP (only acceptable for trusted-LAN dev).
func ListenAndServe(srv *http.Server, certFile, keyFile string) error {
	if srv.TLSConfig != nil {
		return srv.ListenAndServeTLS(certFile, keyFile)
	}
	return srv.ListenAndServe()
}

func buildTLSConfig(cfg ServerConfig) (*tls.Config, error) {
	tc := &tls.Config{
		MinVersion: tls.VersionTLS13,
	}
	if cfg.ClientCAs != "" {
		pool := x509.NewCertPool()
		caBytes, err := os.ReadFile(cfg.ClientCAs)
		if err != nil {
			return nil, fmt.Errorf("read client CA: %w", err)
		}
		if !pool.AppendCertsFromPEM(caBytes) {
			return nil, errors.New("nodeagent: bad CA bundle")
		}
		tc.ClientCAs = pool
		tc.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return tc, nil
}

func loggingMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: 200}
			next.ServeHTTP(rec, r)
			logger.LogAttrs(r.Context(), slog.LevelInfo, "agent_http",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Duration("dur", time.Since(start)),
			)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(c int) { s.status = c; s.ResponseWriter.WriteHeader(c) }

// ---- Handlers ---------------------------------------------------------------

func syncConfHandler(e awg.Executor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		iface, ok := ifaceParam(w, r)
		if !ok {
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeProblem(w, 400, "bad_request", err.Error())
			return
		}
		if err := e.SyncConf(r.Context(), iface, string(body)); err != nil {
			writeProblem(w, 500, "syncconf_failed", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func setPeerHandler(e awg.Executor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		iface, ok := ifaceParam(w, r)
		if !ok {
			return
		}
		var spec PeerSpec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			writeProblem(w, 400, "bad_json", err.Error())
			return
		}
		if err := e.SetPeer(r.Context(), iface, awg.PeerSpec{
			PublicKey:     spec.PublicKey,
			PresharedKey:  spec.PresharedKey,
			AllowedIPs:    spec.AllowedIPs,
			Endpoint:      spec.Endpoint,
			KeepaliveSecs: spec.KeepaliveSecs,
		}); err != nil {
			writeProblem(w, 500, "set_peer_failed", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func removePeerHandler(e awg.Executor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		iface, ok := ifaceParam(w, r)
		if !ok {
			return
		}
		pub := chi.URLParam(r, "pub")
		if err := e.RemovePeer(r.Context(), iface, pub); err != nil {
			writeProblem(w, 500, "remove_peer_failed", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func dumpHandler(e awg.Executor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		iface, ok := ifaceParam(w, r)
		if !ok {
			return
		}
		ifaceRT, peers, err := e.ShowDump(r.Context(), iface)
		if err != nil {
			writeProblem(w, 500, "show_dump_failed", err.Error())
			return
		}
		out := DumpResponse{
			Interface: InterfaceRuntime{
				PublicKey:  ifaceRT.PublicKey,
				ListenPort: ifaceRT.ListenPort,
				FwMark:     ifaceRT.FwMark,
			},
		}
		for _, p := range peers {
			out.Peers = append(out.Peers, PeerRuntime{
				PublicKey:     p.PublicKey,
				PresharedKey:  p.PresharedKey,
				Endpoint:      p.Endpoint,
				AllowedIPs:    p.AllowedIPs,
				LastHandshake: p.LastHandshake,
				RxBytes:       p.RxBytes,
				TxBytes:       p.TxBytes,
				KeepaliveSecs: p.KeepaliveSecs,
			})
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func showconfHandler(e awg.Executor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		iface, ok := ifaceParam(w, r)
		if !ok {
			return
		}
		conf, err := e.ShowConf(r.Context(), iface)
		if err != nil {
			writeProblem(w, 500, "show_conf_failed", err.Error())
			return
		}
		conf = redactConfigSecrets(conf)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(conf))
	}
}

func redactConfigSecrets(conf string) string {
	lines := strings.Split(conf, "\n")
	for i, line := range lines {
		if isSecretConfigLine(line) {
			key, _, _ := strings.Cut(line, "=")
			lines[i] = strings.TrimRight(key, " \t") + " = (redacted)"
		}
	}
	return strings.Join(lines, "\n")
}

func isSecretConfigLine(line string) bool {
	key, _, ok := strings.Cut(line, "=")
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "privatekey", "presharedkey":
		return true
	default:
		return false
	}
}

func upHandler(e awg.Executor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		iface, ok := ifaceParam(w, r)
		if !ok {
			return
		}
		var body struct {
			ConfigPath string `json:"config_path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if err := e.InterfaceUp(r.Context(), iface, body.ConfigPath); err != nil {
			writeProblem(w, 500, "up_failed", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func downHandler(e awg.Executor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		iface, ok := ifaceParam(w, r)
		if !ok {
			return
		}
		if err := e.InterfaceDown(r.Context(), iface); err != nil {
			writeProblem(w, 500, "down_failed", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func ifaceParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	iface := chi.URLParam(r, "iface")
	if !awg.ValidInterfaceName(iface) {
		writeProblem(w, http.StatusBadRequest, "invalid_iface", "interface name must be 1-15 ASCII letters, digits, dot, dash, or underscore")
		return "", false
	}
	return iface, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeProblem(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type": "about:blank", "title": code, "status": status, "code": code, "detail": detail,
	})
}

// ToString helps tests inspect a problem-message body.
func ProblemDetail(body []byte) string {
	var p struct {
		Detail string `json:"detail"`
	}
	_ = json.Unmarshal(body, &p)
	return strings.TrimSpace(p.Detail)
}
