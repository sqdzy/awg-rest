// awg-api is the AmneziaWG REST control-plane server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/awg-rest/awg-rest/internal/auth"
	"github.com/awg-rest/awg-rest/internal/awg"
	"github.com/awg-rest/awg-rest/internal/config"
	"github.com/awg-rest/awg-rest/internal/nodeagent"
	"github.com/awg-rest/awg-rest/internal/outbox"
	"github.com/awg-rest/awg-rest/internal/repo"
	"github.com/awg-rest/awg-rest/internal/server"
	"github.com/google/uuid"
)

func main() {
	devToken := flag.Bool("dev-token", false, "print an HS256 platform_admin JWT for local testing and exit")
	devTokenTTL := flag.Duration("dev-token-ttl", time.Hour, "TTL for -dev-token")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}
	if *devToken {
		tok, err := auth.IssueDevToken(cfg.JWTSecret, cfg.JWTIssuer, cfg.JWTAudience, auth.Principal{
			SubjectID: uuid.New(),
			Roles:     []auth.Role{auth.RolePlatformAdmin},
		}, *devTokenTTL)
		if err != nil {
			panic(err)
		}
		fmt.Println(tok)
		return
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	built, err := server.BuildAPI(ctx, cfg)
	if err != nil {
		panic(err)
	}
	defer built.DB.Close()
	logger := built.Logger

	// Optionally start the outbox worker inside the API process.
	if cfg.EnableEmbeddedWorker {
		ex := buildExecutor(cfg, logger)
		w := &outbox.Worker{
			DB:                 built.DB,
			Outbox:             &repo.Outbox{DB: built.DB},
			Operations:         &repo.Operations{DB: built.DB},
			Peers:              &repo.Peers{DB: built.DB},
			Profiles:           &repo.Profiles{DB: built.DB},
			Nodes:              &repo.Nodes{DB: built.DB},
			Executor:           ex,
			Logger:             logger.With("component", "embedded-worker"),
			BootstrapConfigDir: cfg.BootstrapConfigDir,
		}
		go func() {
			logger.InfoContext(ctx, "starting embedded worker", "executor", cfg.EmbeddedWorkerExec)
			if cfg.ReconcileOnStart {
				if err := w.ReconcileAll(ctx); err != nil {
					logger.WarnContext(ctx, "startup reconcile completed with errors", "err", err)
				}
			}
			if err := w.Run(ctx); err != nil && err != context.Canceled {
				logger.ErrorContext(ctx, "embedded worker stopped", "err", err)
			}
		}()
	}

	logger.InfoContext(ctx, "starting api", "addr", cfg.HTTPAddr)
	go func() {
		if err := built.Server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.ErrorContext(ctx, "http server crashed", "err", err)
			cancel()
		}
	}()

	<-ctx.Done()
	logger.InfoContext(context.Background(), "shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = built.Server.Shutdown(shutdownCtx)
}

func buildExecutor(cfg *config.Config, logger *slog.Logger) awg.Executor {
	switch cfg.EmbeddedWorkerExec {
	case "remote":
		if cfg.NodeAgentURL != "" {
			var ex *nodeagent.RemoteExecutor
			var err error
			if os.Getenv("NODE_AGENT_INSECURE_HTTP") == "true" {
				ex, err = nodeagent.NewInsecureRemoteExecutor(cfg.NodeAgentURL)
			} else {
				ex, err = nodeagent.NewRemoteExecutor(cfg.NodeAgentURL,
					os.Getenv("WORKER_TLS_CERT"),
					os.Getenv("WORKER_TLS_KEY"),
					os.Getenv("WORKER_CA_BUNDLE"))
			}
			if err != nil {
				logger.Error("remote executor init failed", "err", err)
				panic(err)
			}
			return ex
		}
		logger.Warn("EMBEDDED_WORKER_EXEC=remote but NODE_AGENT_URL empty; falling back to fake")
		return awg.NewFakeExecutor(time.Time{})
	case "cli":
		if runtime.GOOS == "linux" {
			return awg.NewCLIExecutor()
		}
		logger.Warn("cli executor requested on non-linux OS; using fake")
		return awg.NewFakeExecutor(time.Time{})
	default:
		return awg.NewFakeExecutor(time.Time{})
	}
}
