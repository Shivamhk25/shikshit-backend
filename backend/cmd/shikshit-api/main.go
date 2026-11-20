// Command shikshit-api serves the Shikshit HTTP API (the OpenAPI contract in backend/api/openapi).
// It is configured entirely by environment variables; see internal/platform/server.Config.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pie360/backend/internal/platform/server"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "shikshit-api:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := server.LoadConfig(os.Getenv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("database: %w", err)
	}

	e, err := server.New(cfg, pool)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr: cfg.Addr, Handler: e,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	fmt.Fprintf(os.Stderr, "shikshit-api %s listening on %s\n", cfg.Version, cfg.Addr)

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	}
	return nil
}
