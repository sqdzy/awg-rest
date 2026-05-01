// awg-api is the AmneziaWG REST control-plane server.
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/awg-rest/awg-rest/internal/config"
	"github.com/awg-rest/awg-rest/internal/server"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	built, err := server.BuildAPI(ctx, cfg)
	if err != nil {
		panic(err)
	}
	defer built.DB.Close()
	logger := built.Logger

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
