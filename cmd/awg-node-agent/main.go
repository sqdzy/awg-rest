// awg-node-agent is the host-local AmneziaWG apply plane. It exposes a small
// HTTPS+mTLS API over which the central control plane drives `awg`/`awg-quick`.
//
// Environment:
//
//	AGENT_ADDR              listen address (default :8081)
//	AGENT_TLS_CERT          PEM cert for HTTPS (optional; mTLS recommended)
//	AGENT_TLS_KEY           PEM key for HTTPS
//	AGENT_CLIENT_CA_BUNDLE  trust bundle for control-plane client certs (mTLS)
//	AGENT_INSECURE_HTTP     allow plain HTTP for local dev/test only
//	LOG_LEVEL, LOG_JSON     observability tunables
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/awg-rest/awg-rest/internal/awg"
	"github.com/awg-rest/awg-rest/internal/nodeagent"
	"github.com/awg-rest/awg-rest/internal/obs"
)

func main() {
	addr := envDefault("AGENT_ADDR", ":8081")
	tlsCert := os.Getenv("AGENT_TLS_CERT")
	tlsKey := os.Getenv("AGENT_TLS_KEY")
	clientCA := os.Getenv("AGENT_CLIENT_CA_BUNDLE")
	level := envDefault("LOG_LEVEL", "info")
	logger := obs.NewLogger(level, os.Getenv("LOG_JSON") != "false")

	var executor awg.Executor
	if runtime.GOOS == "linux" {
		ex := awg.NewCLIExecutor()
		if dir := os.Getenv("BOOTSTRAP_CONF_DIR"); dir != "" {
			ex.BootstrapConfigDir = dir
		}
		executor = ex
	} else {
		logger.Warn("non-linux build; using fake AWG executor", "goos", runtime.GOOS)
		executor = awg.NewFakeExecutor(time.Time{})
	}

	srv, err := nodeagent.NewServer(nodeagent.ServerConfig{
		Addr:              addr,
		Executor:          executor,
		Logger:            logger,
		TLSCertFile:       tlsCert,
		TLSKeyFile:        tlsKey,
		ClientCAs:         clientCA,
		AllowInsecureHTTP: envDefault("AGENT_INSECURE_HTTP", "false") == "true",
	})
	if err != nil {
		panic(err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	go func() {
		logger.InfoContext(ctx, "starting node-agent", "addr", addr, "tls", tlsCert != "")
		var err error
		if srv.TLSConfig != nil {
			err = srv.ListenAndServeTLS(tlsCert, tlsKey)
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server crashed", "err", err)
			cancel()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

func envDefault(k, d string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return d
}
