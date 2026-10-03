// Package storetest opens an isolated, migrated schema for integration
// tests. Tests skip when TEST_DATABASE_URL is unset so the unit suite runs
// without a database.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"variantsvc/internal/store"
)

// Open returns a Store bound to a fresh schema that is dropped when the
// test ends. Each call gets its own schema, so tests can run in parallel.
func Open(t *testing.T) *store.Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var b [6]byte
	rand.Read(b[:]) //nolint:errcheck
	schema := "t_" + hex.EncodeToString(b[:])

	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatalf("create schema: %v", err)
	}

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 5
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect to schema: %v", err)
	}
	s := store.New(pool)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE") //nolint:errcheck
		admin.Close()
	})
	return s
}
