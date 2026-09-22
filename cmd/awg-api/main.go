// awg-api is the AmneziaWG REST control-plane server.
package main

import (
	"context"
	"encoding/json"
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
	"github.com/awg-rest/awg-rest/internal/bootstrap"
	"github.com/awg-rest/awg-rest/internal/config"
	"github.com/awg-rest/awg-rest/internal/nodeagent"
	"github.com/awg-rest/awg-rest/internal/outbox"
	"github.com/awg-rest/awg-rest/internal/repo"
	"github.com/awg-rest/awg-rest/internal/server"
	"github.com/google/uuid"
)

func main() {
	bootstrapDefaults := bootstrap.EnvDefaults()
	devToken := flag.Bool("dev-token", false, "print an HS256 platform_admin JWT for local testing and exit")
	devTokenTTL := flag.Duration("dev-token-ttl", time.Hour, "TTL for -dev-token")

	provisionV31 := flag.Bool("provision-v31-node", false, "create a parallel AmneziaWG 3.1 profile/node/pool and bootstrap config, then exit")
	v31Tenant := flag.String("v31-tenant", bootstrapDefaults.TenantSlug, "existing tenant slug for -provision-v31-node")
	v31Profile := flag.String("v31-profile-name", "default-v31", "new V3.1 protocol profile name")
	v31Region := flag.String("v31-node-region", bootstrapDefaults.NodeRegion, "new V3.1 node region")
	v31Hostname := flag.String("v31-node-hostname", "awg-node-31", "new V3.1 node hostname")
	v31Endpoint := flag.String("v31-node-endpoint", bootstrapDefaults.NodeEndpoint, "public IP or DNS name for the V3.1 node")
	v31Port := flag.Int("v31-node-port", 38824, "UDP listen/published port for the V3.1 node")
	v31Iface := flag.String("v31-node-iface", "awg31", "local interface name for the V3.1 node")
	v31Pool := flag.String("v31-pool-cidr", "10.201.0.0/24", "non-overlapping client address pool for the V3.1 node")
	v31NAT := flag.Bool("v31-enable-nat", bootstrapDefaults.EnableNAT, "enable NAT hooks in the V3.1 bootstrap config")
	v31Egress := flag.String("v31-egress-iface", bootstrapDefaults.EgressIface, "egress interface used by V3.1 NAT hooks")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}
	if *provisionV31 {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		db, err := repo.NewDB(ctx, cfg.DatabaseURL)
		if err != nil {
			panic(err)
		}
		defer db.Close()
		if err := repo.Migrate(ctx, db.Pool); err != nil {
			panic(err)
		}

		result, err := bootstrap.ProvisionV31Node(ctx, db, bootstrap.V31NodeOptions{
			TenantSlug:       *v31Tenant,
			ProfileName:      *v31Profile,
			NodeRegion:       *v31Region,
			NodeHostname:     *v31Hostname,
			NodeEndpoint:     *v31Endpoint,
			NodeBasePort:     *v31Port,
			NodeIface:        *v31Iface,
			PoolCIDR:         *v31Pool,
			BootstrapConfDir: cfg.BootstrapConfigDir,
			EnableNAT:        *v31NAT,
			EgressIface:      *v31Egress,
		}, slog.Default())
		if err != nil {
			panic(err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			panic(err)
		}
		return
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
			ex := awg.NewCLIExecutor()
			ex.BootstrapConfigDir = cfg.BootstrapConfigDir
			return ex
		}
		logger.Warn("cli executor requested on non-linux OS; using fake")
		return awg.NewFakeExecutor(time.Time{})
	default:
		return awg.NewFakeExecutor(time.Time{})
	}
}
