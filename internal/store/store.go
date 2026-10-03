// Package store is the PostgreSQL repository: connection pool, embedded
// migrations, and the queries behind sites, experiments and the
// configuration snapshot. Nothing on the page-render path calls it.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a row does not exist or belongs to another site.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned on unique-key violations (duplicate site or
// experiment key).
var ErrConflict = errors.New("already exists")

// Store wraps a pgx pool.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects with conservative pool settings. The pool is sized for the
// control plane and the event writers, not for per-visitor traffic.
func Open(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	if cfg.MaxConns == 0 || cfg.MaxConns > 20 {
		cfg.MaxConns = 10
	}
	cfg.MinConns = 1
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

// New wraps an existing pool (tests).
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Close releases the pool.
func (s *Store) Close() { s.pool.Close() }

// Ping reports database reachability for /readyz.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Pool exposes the underlying pool for packages that run their own queries.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// tx runs fn in a transaction.
func (s *Store) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// mapErr translates driver errors into the package sentinels.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrConflict
	}
	return err
}
