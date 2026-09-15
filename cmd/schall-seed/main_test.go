package main

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

const appDatabaseURL = "postgres://schall:schall@localhost:5432/schall?sslmode=disable"

// The cases below are all one question: would this run have emptied a database
// nobody named? Each asserts only whether the seeder refused, never how it
// worded it.
func TestGuard(t *testing.T) {
	tests := []struct {
		name   string
		guard  guard
		refuse bool
	}{
		// Confirmation. Seeding says what it is emptying before it empties it.
		{
			name:   "no confirmation at all",
			guard:  guard{seedURL: "postgres://schall:schall@localhost:5433/schall_e2e?sslmode=disable"},
			refuse: true,
		},
		{
			name: "the scratch flag, with no application database configured",
			guard: guard{
				seedURL: "postgres://schall:schall@localhost:5433/schall_e2e?sslmode=disable",
				scratch: true,
			},
		},
		{
			name: "--expect naming the target exactly",
			guard: guard{
				seedURL: "postgres://schall:schall@localhost:5433/schall_e2e?sslmode=disable",
				expect:  "postgres://schall:schall@localhost:5433/schall_e2e?sslmode=disable",
			},
		},
		{
			name: "--expect naming the target in another spelling",
			guard: guard{
				seedURL: "postgres://schall:schall@127.0.0.1:5433/schall_e2e",
				expect:  "postgres://schall:schall@localhost:5433/schall_e2e?sslmode=disable",
			},
		},
		{
			name: "--expect naming the target by another name for one server",
			guard: guard{
				seedURL: "postgres://schall:schall@10.0.0.5:5432/schall_e2e",
				expect:  "postgres://schall:schall@db.internal:5432/schall_e2e",
			},
		},
		{
			name: "--expect naming one of two hosts the target may reach",
			guard: guard{
				seedURL: "postgres://schall:schall@localhost,db.example.test:5433/schall_e2e?sslmode=disable",
				expect:  "postgres://schall:schall@localhost:5433/schall_e2e?sslmode=disable",
			},
			refuse: true,
		},
		{
			name: "--expect naming both hosts the target may reach",
			guard: guard{
				seedURL: "postgres://schall:schall@localhost,db.example.test:5433/schall_e2e?sslmode=disable",
				expect:  "postgres://schall:schall@localhost,db.example.test:5433/schall_e2e?sslmode=disable",
			},
		},
		{
			name: "--expect naming a different database",
			guard: guard{
				seedURL: "postgres://schall:schall@localhost:5433/somebodys_library",
				expect:  "postgres://schall:schall@localhost:5433/schall_e2e?sslmode=disable",
			},
			refuse: true,
		},
		{
			name: "--expect naming a database on a host that will not resolve",
			guard: guard{
				seedURL: "postgres://schall:schall@gone.example.test:5432/schall_e2e",
				expect:  "postgres://schall:schall@db.internal:5432/schall_e2e",
			},
			refuse: true,
		},
		{
			name: "--expect that is not a connection string",
			guard: guard{
				seedURL: "postgres://schall:schall@localhost:5433/schall_e2e",
				expect:  "postgres://schall:schall@localhost:not-a-port/schall_e2e",
			},
			refuse: true,
		},
		{
			name:   "a seed URL that is not a connection string at all",
			guard:  guard{seedURL: "postgres://schall:schall@localhost:not-a-port/schall_e2e", scratch: true},
			refuse: true,
		},

		// The application's database, which confirmation does not buy past.
		{
			name:   "the same URL twice",
			guard:  guard{seedURL: appDatabaseURL, appURL: appDatabaseURL, scratch: true},
			refuse: true,
		},
		{
			name: "localhost and 127.0.0.1 are one host",
			guard: guard{
				seedURL: "postgres://schall:schall@127.0.0.1:5432/schall?sslmode=disable",
				appURL:  appDatabaseURL,
				scratch: true,
			},
			refuse: true,
		},
		{
			name: "an sslmode on one side only",
			guard: guard{
				seedURL: "postgres://schall:schall@localhost:5432/schall",
				appURL:  appDatabaseURL,
				scratch: true,
			},
			refuse: true,
		},
		{
			name: "an omitted port is 5432",
			guard: guard{
				seedURL: "postgres://schall:schall@localhost/schall?sslmode=disable",
				appURL:  appDatabaseURL,
				scratch: true,
			},
			refuse: true,
		},
		{
			name: "a different user reaching the same database",
			guard: guard{
				seedURL: "postgres://someone:else@localhost:5432/schall?sslmode=disable",
				appURL:  appDatabaseURL,
				scratch: true,
			},
			refuse: true,
		},
		{
			name: "the host in a different case",
			guard: guard{
				seedURL: "postgres://schall:schall@LOCALHOST:5432/schall?sslmode=disable",
				appURL:  appDatabaseURL,
				scratch: true,
			},
			refuse: true,
		},
		{
			name: "a keyword DSN naming the URL's database",
			guard: guard{
				seedURL: "host=localhost port=5432 dbname=schall user=schall password=schall sslmode=disable",
				appURL:  appDatabaseURL,
				scratch: true,
			},
			refuse: true,
		},
		{
			name: "the application's URL surrounded by whitespace",
			guard: guard{
				seedURL: appDatabaseURL,
				appURL:  "  " + appDatabaseURL + "\n",
				scratch: true,
			},
			refuse: true,
		},
		{
			name: "a name and an address for one server",
			guard: guard{
				seedURL: "postgres://schall:schall@10.0.0.5:5432/schall",
				appURL:  "postgres://schall:schall@db.internal:5432/schall",
				scratch: true,
			},
			refuse: true,
		},
		{
			name: "two names for one server",
			guard: guard{
				seedURL: "postgres://schall:schall@db.internal:5432/schall",
				appURL:  "postgres://schall:schall@db-primary.internal:5432/schall",
				scratch: true,
			},
			refuse: true,
		},
		{
			name: "a host that will not resolve, against the same database name",
			guard: guard{
				seedURL: "postgres://schall:schall@gone.example.test:5432/schall",
				appURL:  appDatabaseURL,
				scratch: true,
			},
			refuse: true,
		},
		{
			name: "SCHALL_DATABASE_URL set to something unparseable",
			guard: guard{
				seedURL: "postgres://schall:schall@localhost:5433/schall_e2e?sslmode=disable",
				appURL:  "postgres://schall:schall@localhost:not-a-port/schall",
				scratch: true,
			},
			refuse: true,
		},
		{
			name: "the scratch flag does not answer a target that was recognised",
			guard: guard{
				seedURL: "postgres://schall:schall@127.0.0.1/schall",
				appURL:  appDatabaseURL,
				scratch: true,
			},
			refuse: true,
		},
		{
			name: "--expect does, because it named that database",
			guard: guard{
				seedURL: "postgres://schall:schall@127.0.0.1/schall",
				appURL:  appDatabaseURL,
				expect:  "postgres://schall:schall@localhost:5432/schall",
			},
		},

		// Targets that are somewhere else, and stay allowed.
		{
			name: "another database on the same server",
			guard: guard{
				seedURL: "postgres://schall:schall@localhost:5432/schall_e2e?sslmode=disable",
				appURL:  appDatabaseURL,
				scratch: true,
			},
		},
		{
			name: "the same database name on another port",
			guard: guard{
				seedURL: "postgres://schall:schall@localhost:5433/schall?sslmode=disable",
				appURL:  appDatabaseURL,
				scratch: true,
			},
		},
		{
			name: "the same database name on another server",
			guard: guard{
				seedURL: "postgres://schall:schall@db.example.test:5432/schall?sslmode=disable",
				appURL:  appDatabaseURL,
				scratch: true,
			},
		},
		{
			name: "a definitely different database does not require the application host to resolve",
			guard: guard{
				seedURL: "postgres://schall:schall@localhost:5433/schall_e2e",
				appURL:  "postgres://schall:schall@gone.example.test:5432/schall",
				scratch: true,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearPostgresEnvironment(t)

			subject := test.guard
			subject.resolver = testResolver{}
			subject.probe = testIdentityProbe
			err := subject.check(context.Background())

			if test.refuse && err == nil {
				t.Fatalf("seeding %q was allowed, and it empties every table", subject.seedURL)
			}
			if !test.refuse && err != nil {
				t.Fatalf("seeding %q was refused: %v", subject.seedURL, err)
			}
		})
	}
}

// The scratch flag confirms intent, not identity. Even when endpoint text says
// the URLs differ, a positive answer from PostgreSQL still wins.
func TestScratchFlagCannotBypassIdentityMatch(t *testing.T) {
	clearPostgresEnvironment(t)

	const seedURL = "postgres://schall:schall@gone.example.test:5432/schall"
	const appURL = "postgres://schall:schall@localhost:5432/schall"
	identity := databaseIdentity{systemIdentifier: "cluster-1", databaseOID: "16384"}
	err := guard{
		seedURL:  seedURL,
		appURL:   appURL,
		scratch:  true,
		resolver: testResolver{},
		probe: func(context.Context, string) (databaseIdentity, error) {
			return identity, nil
		},
	}.check(context.Background())
	if err == nil {
		t.Fatal("the scratch flag bypassed a positive database identity match")
	}
	for _, spelling := range []string{"gone.example.test:5432/schall", "localhost:5432/schall"} {
		if !strings.Contains(err.Error(), spelling) {
			t.Errorf("refusal does not name %s: %v", spelling, err)
		}
	}
}

func TestGuardDatabaseIdentity(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("SCHALL_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("SCHALL_TEST_DATABASE_URL is not set; database identity tests need a real PostgreSQL cluster")
	}
	clearPostgresEnvironment(t)

	t.Run("same cluster and database through localhost and 127.0.0.1 is refused", func(t *testing.T) {
		seedURL := rewriteDatabaseURL(t, databaseURL, "localhost", "")
		appURL := rewriteDatabaseURL(t, databaseURL, "127.0.0.1", "")
		err := guard{seedURL: seedURL, appURL: appURL, scratch: true}.check(testContext(t))
		if err == nil {
			t.Fatal("one database reached through two host spellings was allowed")
		}
	})

	t.Run("different databases in one cluster are allowed", func(t *testing.T) {
		config, err := pgconn.ParseConfig(databaseURL)
		if err != nil {
			t.Fatalf("parse SCHALL_TEST_DATABASE_URL: %v", err)
		}
		otherDatabase := "postgres"
		if config.Database == otherDatabase {
			otherDatabase = "template1"
		}
		seedURL := rewriteDatabaseURL(t, databaseURL, "localhost", config.Database)
		appURL := rewriteDatabaseURL(t, databaseURL, "127.0.0.1", otherDatabase)
		if err := (guard{seedURL: seedURL, appURL: appURL, scratch: true}).check(testContext(t)); err != nil {
			t.Fatalf("different databases in one cluster were refused: %v", err)
		}
	})

	t.Run("same database name in different clusters is allowed", func(t *testing.T) {
		secondURL := strings.TrimSpace(os.Getenv("SCHALL_SECOND_TEST_DATABASE_URL"))
		if secondURL == "" {
			t.Skip("SCHALL_SECOND_TEST_DATABASE_URL is not set; this case needs a second real PostgreSQL cluster")
		}
		seedURL := rewriteDatabaseURL(t, databaseURL, "", "postgres")
		appURL := rewriteDatabaseURL(t, secondURL, "", "postgres")
		if err := (guard{seedURL: seedURL, appURL: appURL, scratch: true}).check(testContext(t)); err != nil {
			t.Fatalf("the same database name in different clusters was refused: %v", err)
		}
	})

	t.Run("unreachable undecided application database is refused with its cause", func(t *testing.T) {
		seedURL := rewriteDatabaseURL(t, databaseURL, "127.0.0.1", "")
		appURL := rewriteDatabaseURL(t, databaseURL, "gone.invalid", "")

		err := (guard{seedURL: seedURL, appURL: appURL, scratch: true}).check(testContext(t))
		if err == nil {
			t.Fatal("an unreachable application database was allowed")
		}
		if !strings.Contains(err.Error(), "connecting to the application database") || !strings.Contains(err.Error(), "gone.invalid") {
			t.Errorf("refusal does not identify the unreachable application database: %v", err)
		}
	})

	t.Run("definitely different target does not require the application database", func(t *testing.T) {
		config, err := pgconn.ParseConfig(databaseURL)
		if err != nil {
			t.Fatalf("parse SCHALL_TEST_DATABASE_URL: %v", err)
		}
		otherDatabase := "postgres"
		if config.Database == otherDatabase {
			otherDatabase = "template1"
		}
		seedURL := rewriteDatabaseURL(t, databaseURL, "127.0.0.1", config.Database)
		appURL := rewriteDatabaseURL(t, databaseURL, "127.0.0.1", otherDatabase)
		parsed, err := url.Parse(appURL)
		if err != nil {
			t.Fatalf("parse application database URL: %v", err)
		}
		parsed.Host = net.JoinHostPort(parsed.Hostname(), "1")

		if err := (guard{seedURL: seedURL, appURL: parsed.String(), scratch: true}).check(testContext(t)); err != nil {
			t.Fatalf("definitely different target required the unreachable application database: %v", err)
		}
	})
}

// A refusal is read by whoever hit it, and pasted into a bug report by some of
// them, so it names the database without the password that reaches it.
func TestRefusalNamesTheDatabaseAndNotThePassword(t *testing.T) {
	clearPostgresEnvironment(t)

	const url = "postgres://schall:hunter2@localhost:5432/schall?sslmode=disable"
	err := guard{seedURL: url, appURL: url, scratch: true, resolver: testResolver{}}.check(context.Background())
	if err == nil {
		t.Fatal("seeding the application's own database was allowed")
	}
	if !strings.Contains(err.Error(), "localhost:5432/schall") {
		t.Errorf("refusal does not say which database it refused: %v", err)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("refusal repeats the password: %v", err)
	}
}

// A seed run that names nothing and asserts nothing is refused before the
// target is even reachable, which is what makes the flags a contract rather
// than a formality.
func TestRunRefusesWithoutConfirmation(t *testing.T) {
	clearPostgresEnvironment(t)
	t.Setenv("SCHALL_SEED_DATABASE_URL", "postgres://schall:schall@127.0.0.1:1/schall_e2e")

	if err := run(nil); err == nil {
		t.Fatal("an unconfirmed seed run was allowed to start")
	}
}

func testIdentityProbe(ctx context.Context, databaseURL string) (databaseIdentity, error) {
	config, err := pgconn.ParseConfig(databaseURL)
	if err != nil {
		return databaseIdentity{}, err
	}
	e := newEndpoint(config.Host, config.Port)
	addresses, err := (testResolver{}).LookupHost(ctx, e.host)
	if err != nil {
		return databaseIdentity{}, err
	}
	return databaseIdentity{
		systemIdentifier: fmt.Sprintf("%s:%d", addresses[0], e.port),
		databaseOID:      config.Database,
	}, nil
}

func rewriteDatabaseURL(t *testing.T, databaseURL, host, database string) string {
	t.Helper()
	parsed, err := url.Parse(databaseURL)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Skip("database identity tests require SCHALL_TEST_DATABASE_URL values in postgres:// URL form")
	}
	if host == "" {
		host = parsed.Hostname()
	}
	port := parsed.Port()
	if port == "" {
		port = strconv.Itoa(5432)
	}
	parsed.Host = net.JoinHostPort(host, port)
	if database != "" {
		parsed.Path = "/" + database
	}
	return parsed.String()
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), lookupWindow)
	t.Cleanup(cancel)
	return ctx
}

// testResolver answers the names these tests use. Real DNS would make the
// hostname cases depend on where the suite runs.
type testResolver struct{}

func (testResolver) LookupHost(_ context.Context, host string) ([]string, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []string{ip.String()}, nil
	}
	known := map[string][]string{
		"localhost":           {"127.0.0.1", "::1"},
		"db.internal":         {"10.0.0.5"},
		"db-primary.internal": {"10.0.0.5"},
		"db.example.test":     {"203.0.113.10"},
	}
	if addrs, ok := known[host]; ok {
		return addrs, nil
	}
	return nil, fmt.Errorf("no such host")
}

// pgconn fills defaults from the libpq environment variables, which would
// otherwise decide what a URL in these tests means.
func clearPostgresEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{"PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD", "PGSSLMODE", "PGSERVICE", "PGSERVICEFILE"} {
		t.Setenv(name, "")
	}
}
