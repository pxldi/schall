package migrations

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed *.sql
var files embed.FS

// RetryOptions bounds how long UpWithRetry tolerates a database that is not
// accepting connections yet. The window has to cover a database started
// alongside Schall and still expire quickly enough that a genuinely
// unreachable database fails loudly instead of hanging.
type RetryOptions struct {
	Window   time.Duration
	Interval time.Duration
	// OnRetry, when set, is called before each wait so the caller can report
	// that startup is waiting rather than stuck.
	OnRetry func(attempt int, err error)
}

func Up(ctx context.Context, databaseURL string) error {
	database, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open migration database: %w", err)
	}
	defer database.Close()

	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		database,
		files,
	)
	if err != nil {
		return fmt.Errorf("create migration provider: %w", err)
	}
	defer provider.Close()

	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply database migrations: %w", err)
	}
	return nil
}

// UpTo applies migrations as far as one version and stops.
//
// Nothing in the running program calls it. It exists so a test can put a
// database in the state a migration was written against — the rows a data
// migration is meant to change — and then apply that migration over them, which
// is the only way to check that the change it makes is the change it claims.
func UpTo(ctx context.Context, databaseURL string, version int64) error {
	database, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open migration database: %w", err)
	}
	defer database.Close()

	provider, err := goose.NewProvider(goose.DialectPostgres, database, files)
	if err != nil {
		return fmt.Errorf("create migration provider: %w", err)
	}
	defer provider.Close()

	if _, err := provider.UpTo(ctx, version); err != nil {
		return fmt.Errorf("apply database migrations up to %d: %w", version, err)
	}
	return nil
}

// UpWithRetry applies migrations, retrying only while the database refuses
// connections. A migration that fails for any other reason is returned
// immediately: waiting cannot fix broken SQL, and retrying it would hide the
// error behind a delay.
func UpWithRetry(ctx context.Context, databaseURL string, options RetryOptions) error {
	if options.Window <= 0 || options.Interval <= 0 {
		return Up(ctx, databaseURL)
	}

	deadline := time.Now().Add(options.Window)
	for attempt := 1; ; attempt++ {
		err := Up(ctx, databaseURL)
		if err == nil {
			return nil
		}
		if !unreachable(err) || !time.Now().Add(options.Interval).Before(deadline) {
			return err
		}
		if options.OnRetry != nil {
			options.OnRetry(attempt, err)
		}

		timer := time.NewTimer(options.Interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return err
		case <-timer.C:
		}
	}
}

// unreachable reports whether the failure means the database is not accepting
// connections yet, rather than that something is actually wrong with it.
func unreachable(err error) bool {
	var connectError *pgconn.ConnectError
	if errors.As(err, &connectError) {
		return true
	}
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) {
		// cannot_connect_now: the server is running but still starting up.
		return pgError.Code == "57P03"
	}
	var netError net.Error
	if errors.As(err, &netError) {
		return true
	}
	return false
}
