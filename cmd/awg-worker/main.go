// awg-worker is the durable outbox/reconciliation loop. It is intentionally a
// separate binary from the API so deployment can scale them independently.
package main

import (
	"context"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/awg-rest/awg-rest/internal/awg"
	"github.com/awg-rest/awg-rest/internal/config"
	"github.com/awg-rest/awg-rest/internal/nodeagent"
	"github.com/awg-rest/awg-rest/internal/obs"
	"github.com/awg-rest/awg-rest/internal/outbox"
	"github.com/awg-rest/awg-rest/internal/repo"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	logger := obs.NewLogger(cfg.LogLevel, cfg.LogJSON)

	db, err := repo.NewDB(ctx, cfg.DatabaseURL)
	if err != nil {
		panic(err)
	}
	defer db.Close()
	if err := repo.Migrate(ctx, db.Pool); err != nil {
		panic(err)
	}

	// Pick the executor. Preference order:
	//   1. NODE_AGENT_URL set -> RemoteExecutor over HTTPS+mTLS (production).
	//   2. Linux                -> direct `awg` CLI.
	//   3. Otherwise             -> in-process fake (dev).
	var executor awg.Executor
	switch {
	case cfg.NodeAgentURL != "" && cfg.NodeAgentURL != "http://127.0.0.1:8081":
		var ex *nodeagent.RemoteExecutor
		if os.Getenv("NODE_AGENT_INSECURE_HTTP") == "true" {
			ex, err = nodeagent.NewInsecureRemoteExecutor(cfg.NodeAgentURL)
		} else {
			ex, err = nodeagent.NewRemoteExecutor(cfg.NodeAgentURL,
				os.Getenv("WORKER_TLS_CERT"),
				os.Getenv("WORKER_TLS_KEY"),
				os.Getenv("WORKER_CA_BUNDLE"))
		}
		if err != nil {
			panic(err)
		}
		executor = ex
		logger.Info("using remote node-agent executor", "url", cfg.NodeAgentURL)
	case runtime.GOOS == "linux":
		ex := awg.NewCLIExecutor()
		ex.BootstrapConfigDir = cfg.BootstrapConfigDir
		executor = ex
		logger.Info("using local awg CLI executor")
	default:
		logger.Warn("non-linux build; using fake AWG executor", "goos", runtime.GOOS)
		executor = awg.NewFakeExecutor(time.Time{})
	}

	w := &outbox.Worker{
		DB:                 db,
		Outbox:             &repo.Outbox{DB: db},
		Operations:         &repo.Operations{DB: db},
		Peers:              &repo.Peers{DB: db},
		Profiles:           &repo.Profiles{DB: db},
		Nodes:              &repo.Nodes{DB: db},
		Executor:           executor,
		Logger:             logger,
		BootstrapConfigDir: cfg.BootstrapConfigDir,
	}
	if cfg.ReconcileOnStart {
		if err := w.ReconcileAll(ctx); err != nil {
			logger.WarnContext(ctx, "startup reconcile completed with errors", "err", err)
		}
	}
	logger.InfoContext(ctx, "starting worker")
	if err := w.Run(ctx); err != nil && err != context.Canceled {
		logger.ErrorContext(ctx, "worker stopped", "err", err)
	}
}
