// Package seed fills a scratch database with one fixed collection, so that
// browser tests and anybody clicking through Schall by hand are looking at the
// same library every time.
//
// It is a fixture, not a demo: nothing in it was fetched from a provider and
// none of its identifiers resolve anywhere. A seeded database that could be
// rewritten by a background refresh would answer a browser test with whatever
// MusicBrainz said this morning, so the fixture is arranged to leave the job
// queue with nothing to do — no file waiting to be resolved, no want due to be
// swept, no release without a cover.
//
// None of that is written as an age. Work Schall does on a clock is due when
// something is older than a window measured from now(), and a row seeded at a
// fixed distance behind now() crosses every such window eventually — so where
// the fixture would otherwise have to be recent, it is arranged not to be asked
// about at all. That is the difference between a database that stays still and
// one that stays still until tomorrow.
//
// Applying it empties the database first. That is what makes it repeatable, and
// it is also why nothing here may ever be pointed at a database somebody cares
// about.
package seed

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed fixture.sql
var fixture string

// Apply empties every application table and writes the fixture into it. It runs
// in one transaction, so a fixture that has fallen behind the schema leaves the
// database as it found it rather than half seeded.
func Apply(ctx context.Context, pool *pgxpool.Pool) error {
	transaction, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin seed: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	if err := empty(ctx, transaction); err != nil {
		return err
	}
	if _, err := transaction.Exec(ctx, fixture); err != nil {
		return fmt.Errorf("apply seed fixture: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit seed: %w", err)
	}
	return nil
}

// empty clears every application table rather than the ones the fixture writes.
// A table the fixture does not name still holds whatever the last run of the
// application left in it, and rows nobody seeded are exactly the ones a browser
// test cannot account for.
func empty(ctx context.Context, transaction pgx.Tx) error {
	rows, err := transaction.Query(ctx, `
		SELECT quote_ident(tablename)
		FROM pg_tables
		WHERE schemaname = 'public' AND tablename <> 'goose_db_version'
	`)
	if err != nil {
		return fmt.Errorf("list seed database tables: %w", err)
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("list seed database tables: %w", err)
	}
	if len(tables) == 0 {
		return nil
	}
	if _, err := transaction.Exec(ctx, "TRUNCATE "+strings.Join(tables, ", ")+" CASCADE"); err != nil {
		return fmt.Errorf("empty seed database: %w", err)
	}
	return nil
}
