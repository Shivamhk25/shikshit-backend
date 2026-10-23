package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pie360/backend/internal/shared/config"
	"github.com/pie360/backend/internal/shared/logger"
)

// Pool wraps pgxpool.Pool with additional functionality
type Pool struct {
	*pgxpool.Pool
	config *config.DatabaseConfig
	logger *logger.Logger
}

// NewPool creates a new database connection pool
func NewPool(ctx context.Context, cfg *config.DatabaseConfig, log *logger.Logger) (*Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("failed to parse database config: %w", err)
	}

	poolConfig.MaxConns = cfg.MaxConns
	poolConfig.MinConns = cfg.MinConns
	poolConfig.MaxConnLifetime = cfg.MaxConnLifetime
	poolConfig.MaxConnIdleTime = cfg.MaxConnIdleTime
	poolConfig.HealthCheckPeriod = cfg.HealthCheckPeriod

	// Set up after connect hook for tenant context
	poolConfig.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		// The tenant context will be set per-request via middleware
		return nil
	}

	// Set up before acquire hook for health checks
	poolConfig.BeforeAcquire = func(ctx context.Context, conn *pgx.Conn) bool {
		return true
	}

	// Set up after release hook
	poolConfig.AfterRelease = func(conn *pgx.Conn) bool {
		return true
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	// Test connection
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	log.Info().
		Int("max_conns", int(cfg.MaxConns)).
		Int("min_conns", int(cfg.MinConns)).
		Msg("Database connection pool created")

	return &Pool{
		Pool:   pool,
		config: cfg,
		logger: log,
	}, nil
}

// Close closes the connection pool gracefully
func (p *Pool) Close() {
	p.Pool.Close()
	p.logger.Info().Msg("Database connection pool closed")
}

// Stats returns pool statistics
func (p *Pool) Stats() *pgxpool.Stat {
	return p.Pool.Stat()
}

// Acquire acquires a connection from the pool with timeout
func (p *Pool) Acquire(ctx context.Context) (*pgxpool.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return p.Pool.Acquire(ctx)
}

// BeginTx starts a new transaction
func (p *Pool) BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
	return p.Pool.BeginTx(ctx, opts)
}

// WithTenant sets the tenant context for the current connection
func (p *Pool) WithTenant(ctx context.Context, tenantID string) (context.Context, error) {
	conn, err := p.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	_, err = conn.Exec(ctx, "SET LOCAL app.current_institute_id = $1", tenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to set tenant context: %w", err)
	}
	return ctx, nil
}

// HealthCheck performs a health check on the database
func (p *Pool) HealthCheck(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return p.Ping(ctx)
}

// QueryExecutor is an interface for query execution (pool or transaction)
type QueryExecutor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// Ensure Pool implements QueryExecutor
var _ QueryExecutor = (*Pool)(nil)
var _ QueryExecutor = (*pgxpool.Conn)(nil)
var _ QueryExecutor = (pgx.Tx)(nil)