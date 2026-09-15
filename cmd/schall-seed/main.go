// Command schall-seed migrates a scratch database and fills it with the fixed
// collection in internal/seed. It is what gives the browser tests something to
// look at, and what a developer runs to click through a populated Schall
// without owning any music.
//
// It empties the database before it writes, so nothing is seeded that was not
// confirmed first. Either --expect names the database this run is meant to
// empty and the target has to be that same database, or
// --i-know-this-is-a-scratch-database asserts it without naming it. With
// neither, the seeder refuses: a destructive command that runs on its own
// because a variable happened to be unset is the failure this guard exists to
// not have.
//
// Databases are first compared as endpoints — host, port and database name,
// with the spellings of one host folded together. When those names do not
// settle the question, the servers' cluster and database identities do.
// SCHALL_DATABASE_URL is the second refusal: the target being the database the
// application uses is not something --i-know-this-is-a-scratch-database can
// talk its way past.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/migrations"
	"github.com/pxldi/schall/internal/seed"
)

const (
	// expectFlag names the database the caller means to empty. It is what a
	// script should use: the URL it seeds is overridable from the environment,
	// the one it writes here is not, so the two being the same database is a
	// thing the seeder checks rather than a thing the script promises.
	expectFlag = "expect"
	// scratchFlag is the same confirmation without naming anything, for a
	// person seeding a database they have in front of them. It is long on
	// purpose: the only one who should type it is one who has read what
	// seeding does.
	scratchFlag = "i-know-this-is-a-scratch-database"
)

// lookupWindow bounds all name lookups and database identity probes the guard
// makes. The only thing waiting on it is a seed run that has not started, so it
// is generous.
const lookupWindow = 5 * time.Second

// probeWindow keeps either connection attempt short even when check is called
// without its usual outer deadline.
const probeWindow = 2 * time.Second

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "schall-seed: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("schall-seed", flag.ContinueOnError)
	expect := flags.String(expectFlag, "",
		"the database this run is meant to empty; the target must be that same database")
	scratch := flags.Bool(scratchFlag, false,
		"confirm the target is a scratch database without naming it")
	if err := flags.Parse(args); err != nil {
		return err
	}

	databaseURL := strings.TrimSpace(os.Getenv("SCHALL_SEED_DATABASE_URL"))
	if databaseURL == "" {
		return fmt.Errorf("SCHALL_SEED_DATABASE_URL is required, and must name a scratch database: seeding empties every table in it")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The refusal that matters. Everything else here is recoverable; running
	// this against a database nobody meant to empty is not.
	guarded, cancel := context.WithTimeout(ctx, lookupWindow)
	defer cancel()
	if err := (guard{
		seedURL: databaseURL,
		appURL:  os.Getenv("SCHALL_DATABASE_URL"),
		expect:  *expect,
		scratch: *scratch,
	}).check(guarded); err != nil {
		return err
	}

	// The same retry window startup uses, for the same reason: the seeder is
	// normally run immediately after `docker compose up -d postgres-test`, and a
	// database still starting is not a failure.
	if err := migrations.UpWithRetry(ctx, databaseURL, migrations.RetryOptions{
		Window:   60 * time.Second,
		Interval: 2 * time.Second,
	}); err != nil {
		return err
	}

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect to seed database: %w", err)
	}
	defer pool.Close()

	if err := seed.Apply(ctx, pool); err != nil {
		return err
	}

	fmt.Println("seeded")
	return nil
}

// guard is the decision taken before anything is truncated.
type guard struct {
	seedURL string
	appURL  string
	expect  string
	scratch bool
	// resolver looks host names up. Nil is net.DefaultResolver; tests supply
	// their own rather than depend on what DNS happens to answer.
	resolver hostLookup
	// probe is nil in the command. Unit tests replace the real connection while
	// integration tests exercise it against PostgreSQL.
	probe identityProbe
}

type hostLookup interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// check returns nil only when the target was confirmed and is not a database
// the configured application URL may reach. The scratch flag is an explicit
// confirmation when there is no application URL; configured comparisons fail
// closed whenever neither endpoint comparison nor the server can settle them.
func (g guard) check(ctx context.Context) error {
	target, err := pgconn.ParseConfig(g.seedURL)
	if err != nil {
		return fmt.Errorf("parse SCHALL_SEED_DATABASE_URL: %w", err)
	}

	named, err := g.confirmed(ctx, target)
	if err != nil {
		return err
	}
	if named {
		// The caller wrote this database out and the target is that database.
		// Nothing SCHALL_DATABASE_URL says can be more specific than that, and
		// this is how `make seed` refills a database somebody has the
		// application pointed at on purpose.
		return nil
	}
	return g.notTheApplicationDatabase(ctx, target)
}

// confirmed reports whether the run was confirmed by naming the database, as
// opposed to by bare assertion. It returns an error when it was not confirmed
// at all.
func (g guard) confirmed(ctx context.Context, target *pgconn.Config) (bool, error) {
	if strings.TrimSpace(g.expect) == "" {
		if g.scratch {
			return false, nil
		}
		return false, fmt.Errorf("seeding empties every table in %s and nothing here says that is what you meant: name the database with --%s <url>, or pass --%s for one you have in front of you",
			describe(target), expectFlag, scratchFlag)
	}

	expected, err := pgconn.ParseConfig(strings.TrimSpace(g.expect))
	if err != nil {
		return false, fmt.Errorf("parse --%s: %w", expectFlag, err)
	}
	only, err := g.reachesOnly(ctx, target, expected)
	if err != nil {
		return false, fmt.Errorf("--%s names %s and SCHALL_SEED_DATABASE_URL names %s, and they could not be shown to be one database (%v): seeding empties every table in it",
			expectFlag, describe(expected), describe(target), err)
	}
	if !only {
		return false, fmt.Errorf("--%s names %s, but SCHALL_SEED_DATABASE_URL names %s, and seeding empties every table in it",
			expectFlag, describe(expected), describe(target))
	}
	return true, nil
}

// reachesOnly reports whether every database the target may connect to is the
// one that was named. Confirmation needs a stronger answer than the refusal
// does: refusing wants "may reach the application's database", so one endpoint
// in common is enough, while confirming wants "reaches only the database you
// named". A target listing a second host would otherwise be confirmed by the
// first one matching, and pgx would empty whichever answered.
func (g guard) reachesOnly(ctx context.Context, target, expected *pgconn.Config) (bool, error) {
	if strings.TrimSpace(target.Database) != strings.TrimSpace(expected.Database) {
		return false, nil
	}

	named := endpoints(expected)
	for _, reachable := range endpoints(target) {
		covered, err := g.anySameEndpoint(ctx, reachable, named)
		if err != nil {
			return false, err
		}
		if !covered {
			return false, nil
		}
	}
	return true, nil
}

// notTheApplicationDatabase is the second refusal when an application URL is
// configured, and the one a bare assertion cannot get past: whatever the
// operator believes about the target, it is not to be that database.
func (g guard) notTheApplicationDatabase(ctx context.Context, target *pgconn.Config) error {
	appURL := strings.TrimSpace(g.appURL)
	if appURL == "" {
		// confirmed has already required the explicit scratch flag. With no
		// application database configured there is nothing further to compare.
		return nil
	}

	app, err := pgconn.ParseConfig(appURL)
	if err != nil {
		return fmt.Errorf("SCHALL_DATABASE_URL is set but cannot be parsed (%v), so there is no way to tell whether %s is the database the application uses: fix it, or name the target with --%s <url>",
			err, describe(target), expectFlag)
	}
	same, comparisonErr := g.sameDatabase(ctx, target, app)
	if same {
		return fmt.Errorf("SCHALL_SEED_DATABASE_URL names %s and SCHALL_DATABASE_URL names %s, which are the same database, and seeding would empty it: point the seed URL at a scratch database, or name this one with --%s <url> if emptying it is what you meant",
			describeSpelling(target), describeSpelling(app), expectFlag)
	}
	if comparisonErr == nil {
		// Different database names or resolved endpoints are a definite answer;
		// do not turn an unrelated, stopped application database into a seeding
		// dependency.
		return nil
	}

	seedIdentity, err := g.databaseIdentity(ctx, strings.TrimSpace(g.seedURL))
	if err != nil {
		return fmt.Errorf("endpoint comparison between %s and %s was inconclusive (%v), and connecting to the seed database to read its cluster identity failed: %w; seeding is refused",
			describeSpelling(target), describeSpelling(app), comparisonErr, err)
	}
	appIdentity, err := g.databaseIdentity(ctx, appURL)
	if err != nil {
		return fmt.Errorf("endpoint comparison between %s and %s was inconclusive (%v), and connecting to the application database to read its cluster identity failed: %w; seeding is refused",
			describeSpelling(target), describeSpelling(app), comparisonErr, err)
	}
	if seedIdentity == appIdentity {
		return fmt.Errorf("SCHALL_SEED_DATABASE_URL names %s and SCHALL_DATABASE_URL names %s; PostgreSQL identifies both as the same database, and seeding would empty it: point the seed URL at a scratch database, or name this one with --%s <url> if emptying it is what you meant",
			describeSpelling(target), describeSpelling(app), expectFlag)
	}
	return nil
}

type databaseIdentity struct {
	systemIdentifier string
	databaseOID      string
}

type identityProbe func(context.Context, string) (databaseIdentity, error)

func (g guard) databaseIdentity(ctx context.Context, databaseURL string) (databaseIdentity, error) {
	if g.probe != nil {
		return g.probe(ctx, databaseURL)
	}
	return readDatabaseIdentity(ctx, databaseURL)
}

// readDatabaseIdentity asks PostgreSQL rather than guessing from a connection
// string. system_identifier names the cluster and the database OID names one
// database within it; together they survive DNS aliases, poolers and floating
// addresses without confusing two databases in one cluster.
func readDatabaseIdentity(ctx context.Context, databaseURL string) (databaseIdentity, error) {
	probeCtx, cancel := context.WithTimeout(ctx, probeWindow)
	defer cancel()

	conn, err := pgx.Connect(probeCtx, databaseURL)
	if err != nil {
		return databaseIdentity{}, err
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
		defer closeCancel()
		_ = conn.Close(closeCtx)
	}()

	var identity databaseIdentity
	err = conn.QueryRow(probeCtx, `
		SELECT system_identifier::text, oid::text
		FROM pg_control_system(), pg_database
		WHERE datname = current_database()
	`).Scan(&identity.systemIdentifier, &identity.databaseOID)
	if err != nil {
		return databaseIdentity{}, err
	}
	return identity, nil
}

// sameDatabase reports whether two connection configurations reach the same
// database. Spellings that differ and mean the same thing — localhost against
// 127.0.0.1, an omitted port, an sslmode on one side only — are one database,
// which string comparison had no way to see, and so are two names for one
// server, which comparing the names had no way to see either. The error is a
// third answer, "could not tell", and every caller reads it as the dangerous
// one.
func (g guard) sameDatabase(ctx context.Context, a, b *pgconn.Config) (bool, error) {
	if strings.TrimSpace(a.Database) != strings.TrimSpace(b.Database) {
		return false, nil
	}

	reachable := endpoints(b)
	var deferred error
	for _, x := range endpoints(a) {
		same, err := g.anySameEndpoint(ctx, x, reachable)
		if err != nil {
			if deferred == nil {
				deferred = err
			}
			continue
		}
		if same {
			return true, nil
		}
	}
	if deferred != nil {
		return false, deferred
	}
	return false, nil
}

// anySameEndpoint reports whether one endpoint is any of a set. It answers from
// the text where it can, so the common case waits on no lookup at all, and it
// holds a failed lookup back rather than returning it at once: another endpoint
// in the set may still match, and only a "no" that rests on a name nobody could
// resolve is worth reporting as "could not tell".
func (g guard) anySameEndpoint(ctx context.Context, one endpoint, set []endpoint) (bool, error) {
	for _, other := range set {
		if one == other {
			return true, nil
		}
	}

	// No endpoint in common as written. Only now is a name lookup worth the
	// wait, and only for the pairs a lookup could still join.
	var deferred error
	for _, other := range set {
		if one.port != other.port {
			continue
		}
		same, err := g.sameHost(ctx, one.host, other.host)
		if err != nil {
			if deferred == nil {
				deferred = err
			}
			continue
		}
		if same {
			return true, nil
		}
	}
	if deferred != nil {
		return false, deferred
	}
	return false, nil
}

// sameHost reports whether two names reach one machine. A name that will not
// resolve is an error rather than a difference: not being able to look a host
// up is not evidence that it is somewhere else.
func (g guard) sameHost(ctx context.Context, a, b string) (bool, error) {
	addrsA, err := g.lookup(ctx, a)
	if err != nil {
		return false, err
	}
	addrsB, err := g.lookup(ctx, b)
	if err != nil {
		return false, err
	}
	for _, x := range addrsA {
		for _, y := range addrsB {
			if x == y {
				return true, nil
			}
		}
	}
	return false, nil
}

func (g guard) lookup(ctx context.Context, host string) ([]string, error) {
	resolver := g.resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addrs, err := resolver.LookupHost(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("look up %s: %w", host, err)
	}
	// One address has several spellings too, so they are compared parsed.
	canonical := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		if ip := net.ParseIP(addr); ip != nil {
			canonical = append(canonical, ip.String())
		}
	}
	if len(canonical) == 0 {
		return nil, fmt.Errorf("look up %s: no address", host)
	}
	return canonical, nil
}

// endpoint is a host and port as this program compares them: normalised, and
// never the raw text of the URL.
type endpoint struct {
	host string
	port uint16
}

func (e endpoint) String() string {
	return fmt.Sprintf("%s:%d", e.host, e.port)
}

// endpoints lists everywhere a configuration may connect, its fallbacks
// included: a URL naming several hosts, or one whose sslmode leaves pgx a
// second address to try, reaches any of them, and reaching the application's
// database on the second attempt is the same accident as on the first.
func endpoints(config *pgconn.Config) []endpoint {
	list := []endpoint{newEndpoint(config.Host, config.Port)}
	for _, fallback := range config.Fallbacks {
		list = append(list, newEndpoint(fallback.Host, fallback.Port))
	}
	return list
}

func newEndpoint(host string, port uint16) endpoint {
	if port == 0 {
		port = 5432
	}
	return endpoint{host: normalizeHost(host), port: port}
}

// normalizeHost folds the ways of writing "this machine" together. It errs
// towards calling two hosts the same, because the cost of that is a seed run
// that has to name its database, and the cost of the opposite is an emptied
// library.
func normalizeHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimSuffix(host, ".")
	// A Unix socket directory reaches a server on this machine, and so does
	// localhost; neither says which, so they are folded together.
	if host == "" || strings.HasPrefix(host, "/") {
		return "localhost"
	}
	if host == "localhost" || host == "localhost.localdomain" {
		return "localhost"
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil && (ip.IsLoopback() || ip.IsUnspecified()) {
		return "localhost"
	}
	return host
}

// describe names a database without repeating the credentials that were in the
// URL, so the refusal can be pasted into a bug report. It lists every endpoint
// the URL may reach, because a refusal that showed only the first would name
// two sides identically in exactly the case where the second host is the
// reason.
func describe(config *pgconn.Config) string {
	seen := make(map[endpoint]bool)
	names := make([]string, 0, 1)
	for _, e := range endpoints(config) {
		if seen[e] {
			continue
		}
		seen[e] = true
		names = append(names, e.String())
	}
	return fmt.Sprintf("%s/%s", strings.Join(names, ","), config.Database)
}

// describeSpelling preserves the host spelling that led to a connection while
// omitting its credentials. That makes an identity-based refusal name both
// inputs even when normalisation would print them identically.
func describeSpelling(config *pgconn.Config) string {
	names := make([]string, 0, 1+len(config.Fallbacks))
	names = append(names, fmt.Sprintf("%s:%d", config.Host, newEndpoint(config.Host, config.Port).port))
	for _, fallback := range config.Fallbacks {
		names = append(names, fmt.Sprintf("%s:%d", fallback.Host, newEndpoint(fallback.Host, fallback.Port).port))
	}
	return fmt.Sprintf("%s/%s", strings.Join(names, ","), config.Database)
}
