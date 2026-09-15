package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The setting has to reach the connection, not only the struct. A runtime
// parameter spelled wrong is accepted by ParseConfig and refused by PostgreSQL,
// so the only proof is the server saying what it is set to.
func TestTheConnectionAsksPostgresNotToCompileQueries(t *testing.T) {
	databaseURL := os.Getenv("SCHALL_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set SCHALL_TEST_DATABASE_URL to a scratch database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	poolConfig, err := databasePoolConfig(databaseURL)
	if err != nil {
		t.Fatalf("databasePoolConfig() error = %v", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	var jit string
	if err := pool.QueryRow(ctx, "SHOW jit").Scan(&jit); err != nil {
		t.Fatalf("SHOW jit: %v", err)
	}
	if jit != "off" {
		t.Fatalf("jit = %q, want it off: the planner's cost estimate for the "+
			"artists list is 1.8M and LLVM spends a second on it", jit)
	}
}
