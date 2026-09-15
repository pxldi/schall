package migrations

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// A port nothing listens on, so connecting fails the way a database that has
// not started yet fails.
const closedPortURL = "postgres://schall:schall@127.0.0.1:1/schall?sslmode=disable"

// embeddedNames is every migration the binary carries, in the order goose reads
// them. It is what ships; the directory listing is only what the working tree
// happens to hold.
func embeddedNames(t *testing.T) []string {
	t.Helper()
	entries, err := files.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

// embeddedVersions is the numeric prefix of every embedded migration, which is
// the version goose records once it has applied one.
func embeddedVersions(t *testing.T) []int64 {
	t.Helper()
	versions := make([]int64, 0)
	for _, name := range embeddedNames(t) {
		version, err := strconv.ParseInt(strings.SplitN(name, "_", 2)[0], 10, 64)
		if err != nil {
			t.Fatalf("migration %q does not begin with a version: %v", name, err)
		}
		versions = append(versions, version)
	}
	return versions
}

// A gap in the numbering is a migration somebody wrote and did not commit, or
// two written at once under one number. Both are found here rather than by a
// deployment applying a set nobody has ever applied together.
func TestMigrationsAreNumberedContiguouslyFromOne(t *testing.T) {
	versions := embeddedVersions(t)
	if len(versions) == 0 {
		t.Fatal("no migrations are embedded at all")
	}
	for index, version := range versions {
		if want := int64(index + 1); version != want {
			t.Fatalf("migration %d is numbered %05d, want %05d", index, version, want)
		}
	}
}

// Every migration numbers itself in five digits, so the lexical order goose
// reads them in is the order they were written in.
func TestEveryMigrationIsNamedWithFiveDigits(t *testing.T) {
	for _, name := range embeddedNames(t) {
		prefix, rest, found := strings.Cut(name, "_")
		if !found || len(prefix) != 5 || rest == "" {
			t.Errorf("migration %q is not NNNNN_name.sql", name)
		}
		if !strings.HasSuffix(name, ".sql") {
			t.Errorf("migration %q is not a .sql file", name)
		}
	}
}

func TestEveryMigrationCarriesAnUpSection(t *testing.T) {
	for _, name := range embeddedNames(t) {
		body, err := files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "-- +goose Up") {
			t.Errorf("migration %q has no Up section", name)
		}
	}
}

// Without a Down section a migration cannot be rolled back, and goose refuses
// the whole set rather than the one file.
func TestEveryMigrationCarriesADownSection(t *testing.T) {
	for _, name := range embeddedNames(t) {
		body, err := files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "-- +goose Down") {
			t.Errorf("migration %q has no Down section", name)
		}
	}
}

// The binary applies what it embedded, not what is in the directory. A file
// added without rebuilding the embed — or left on disk after being removed from
// it — is a schema the running server does not have.
func TestTheEmbeddedMigrationsAreTheFilesOnDisk(t *testing.T) {
	onDisk, err := filepath.Glob("*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(onDisk)
	embedded := embeddedNames(t)
	if len(onDisk) != len(embedded) {
		t.Fatalf("%d files on disk, %d embedded", len(onDisk), len(embedded))
	}
	for index, name := range embedded {
		if onDisk[index] != name {
			t.Fatalf("embedded %q, on disk %q", name, onDisk[index])
		}
		body, err := files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		fromDisk, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != string(fromDisk) {
			t.Errorf("embedded %q differs from the file on disk", name)
		}
	}
}

// A database URL nothing can be made of is a configuration mistake, and it has
// to read as one rather than as a database that has not started yet: waiting
// out the startup window would hide a typo behind a minute of silence.
func TestUpReportsADatabaseURLItCannotParse(t *testing.T) {
	err := Up(context.Background(), "postgres://user:pass@host:not-a-port/db")
	if err == nil {
		t.Fatal("Up() accepted a database URL that cannot be parsed")
	}
	if unreachable(err) {
		t.Fatalf("error = %v was classified as a database that is still starting", err)
	}
}

// The same mistake seen through the retry loop: it is returned on the first
// attempt rather than retried for the whole window.
func TestUpWithRetryDoesNotWaitOutAnUnparseableDatabaseURL(t *testing.T) {
	attempts := 0
	err := UpWithRetry(context.Background(), "postgres://user:pass@host:not-a-port/db",
		RetryOptions{
			Window:   time.Minute,
			Interval: time.Second,
			OnRetry:  func(int, error) { attempts++ },
		})
	if err == nil {
		t.Fatal("UpWithRetry() accepted a database URL that cannot be parsed")
	}
	if attempts != 0 {
		t.Fatalf("retried %d times over a URL no wait could fix", attempts)
	}
}

func TestUnreachableClassifiesFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"connect error", &pgconn.ConnectError{Config: &pgconn.Config{}}, true},
		{"wrapped connect error", fmt.Errorf("apply database migrations: %w",
			&pgconn.ConnectError{Config: &pgconn.Config{}}), true},
		{"still starting up", &pgconn.PgError{Code: "57P03"}, true},
		{"dial error", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, true},
		{"broken migration", &pgconn.PgError{Code: "42601"}, false},
		{"other failure", errors.New("create migration provider: bad dialect"), false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := unreachable(testCase.err); got != testCase.want {
				t.Fatalf("unreachable(%v) = %v, want %v", testCase.err, got, testCase.want)
			}
		})
	}
}

func TestUpWithRetryGivesUpAfterTheWindow(t *testing.T) {
	attempts := 0
	start := time.Now()
	err := UpWithRetry(context.Background(), closedPortURL, RetryOptions{
		Window:   300 * time.Millisecond,
		Interval: 50 * time.Millisecond,
		OnRetry:  func(int, error) { attempts++ },
	})

	if err == nil {
		t.Fatal("UpWithRetry() succeeded against a closed port")
	}
	if attempts == 0 {
		t.Fatal("UpWithRetry() reported no retries")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("UpWithRetry() waited %s, far beyond its window", elapsed)
	}
}

func TestUpWithRetryStopsWhenTheContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := UpWithRetry(ctx, closedPortURL, RetryOptions{
		Window:   time.Minute,
		Interval: time.Second,
	})
	if err == nil {
		t.Fatal("UpWithRetry() succeeded with a cancelled context")
	}
}

// Startup that is being shut down must not sit out the interval it was about to
// wait. The cancellation arrives while the retry is already pending, which is
// where a whole interval could otherwise be spent on a database nobody is
// waiting for any more.
func TestUpWithRetryStopsWhenTheContextIsCancelledDuringTheWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := time.Now()
	err := UpWithRetry(ctx, closedPortURL, RetryOptions{
		Window:   time.Minute,
		Interval: 30 * time.Second,
		OnRetry:  func(int, error) { cancel() },
	})
	if err == nil {
		t.Fatal("UpWithRetry() succeeded against a closed port")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("UpWithRetry() waited %s after being cancelled", elapsed)
	}
}

// Retry options that ask for no window are a caller who wants the answer now.
// Applying once and reporting the failure is what cmd/schall relies on when it
// is told not to wait for the database at all.
func TestUpWithRetryDoesNotRetryWithoutAWindow(t *testing.T) {
	attempts := 0
	err := UpWithRetry(context.Background(), closedPortURL, RetryOptions{
		Window:   0,
		Interval: time.Second,
		OnRetry:  func(int, error) { attempts++ },
	})
	if err == nil {
		t.Fatal("UpWithRetry() succeeded against a closed port")
	}
	if attempts != 0 {
		t.Fatalf("retried %d times with no window to retry in", attempts)
	}
}

// An interval of zero is the same instruction as a window of zero: there is no
// pause to spend, so nothing is retried.
func TestUpWithRetryDoesNotRetryWithoutAnInterval(t *testing.T) {
	attempts := 0
	err := UpWithRetry(context.Background(), closedPortURL, RetryOptions{
		Window:   time.Minute,
		Interval: 0,
		OnRetry:  func(int, error) { attempts++ },
	})
	if err == nil {
		t.Fatal("UpWithRetry() succeeded against a closed port")
	}
	if attempts != 0 {
		t.Fatalf("retried %d times with no interval to wait", attempts)
	}
}
