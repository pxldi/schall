package seed_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/seed"
)

// The fixture is raw SQL against the live schema, so a migration that renames a
// column or tightens a constraint breaks it. This is where that breaks: here,
// in CI, on the pull request that made the change — rather than in a browser
// suite whose failure would say only that a page was empty.
func TestApplySeedsTheFixture(t *testing.T) {
	pool := dbtest.Setup(t)
	ctx := context.Background()

	if err := seed.Apply(ctx, pool); err != nil {
		t.Fatalf("apply seed: %v", err)
	}

	for _, expected := range []struct {
		what  string
		query string
		count int
	}{
		{"artists", "SELECT count(*) FROM artists", 5},
		{"followed artists", "SELECT count(*) FROM artists WHERE followed_at IS NOT NULL", 4},
		{"releases", "SELECT count(*) FROM albums", 6},
		{"tracks", "SELECT count(*) FROM tracks", 17},
		{"library files", "SELECT count(*) FROM library_files", 17},
		{"track mappings", "SELECT count(*) FROM track_mappings", 12},
		{"wants awaiting review",
			"SELECT count(*) FROM acquisition_targets WHERE status = 'awaiting_review'", 2},
		{"wants still being looked for",
			"SELECT count(*) FROM acquisition_targets WHERE status = 'pending'", 1},
		{"wants somebody stopped",
			"SELECT count(*) FROM acquisition_targets WHERE status = 'not_wanted'", 1},
		{"held copies",
			"SELECT count(*) FROM acquisition_target_files WHERE verdict = 'held'", 2},
		{"playlist entries", "SELECT count(*) FROM playlist_entries", 4},
		{"download requests", "SELECT count(*) FROM download_requests", 1},
	} {
		var count int
		if err := pool.QueryRow(ctx, expected.query).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", expected.what, err)
		}
		if count != expected.count {
			t.Errorf("%s: got %d, want %d", expected.what, count, expected.count)
		}
	}

	// Applying twice is applying once. The browser suite reseeds on every run
	// and a fixture that only worked against an empty database would fail on the
	// second one.
	if err := seed.Apply(ctx, pool); err != nil {
		t.Fatalf("apply seed a second time: %v", err)
	}
}

// One thing the seeded database has to have none of, and the count that says so.
type quiet struct {
	what  string
	query string
}

// What the fixture is for is a database that stays still while somebody reads
// it. Every one of these is a state that would have the application queue work
// on startup and rewrite what a test was about to assert, so each is checked as
// a property of the fixture rather than left to be noticed as a flaky run.
//
// Every one of them names the clock as plainly as the application does, as
// now(), and none of them is handed one. The clock is moved underneath them
// instead — see shiftTheClock — because the difference between a fixture that
// is still and one that is still today is exactly what happens to these counts
// as the clock moves and the rows do not, and a query that had to remember to
// ask for the moved clock is a query that can forget. Most of them name no time
// at all, which is what makes them durable.
var stillness = []quiet{
	// Startup queues a provider request for every file in these two states.
	{"files waiting to be asked about", `
		SELECT count(*) FROM library_files
		WHERE missing_at IS NULL AND resolution_status IN ('pending', 'failed')`},
	// The loudness pass decodes every file nobody has measured, which for the
	// fixture's invented paths is ffmpeg being started once per file and told
	// there is no such file. The fixture marks them all measured, so the pass
	// finds nothing. This is zero at any clock: the pass asks whether a file has
	// ever been measured, not when.
	{"files nothing has measured for loudness", `
		SELECT count(*) FROM library_files
		WHERE missing_at IS NULL AND loudness_measured_at IS NULL`},
	// And an import for every completed request nothing has taken in.
	{"downloads waiting to be imported", `
		SELECT count(*) FROM download_requests
		WHERE status = 'completed' AND import_status = 'pending'`},
	// The acquisition sweeper picks up anything with a due attempt, and an
	// unresolved want with a due search on its anchor (ADR 0037).
	{"wants due for another attempt", `
		SELECT count(*) FROM acquisition_targets
		WHERE next_attempt_at IS NOT NULL OR next_search_at IS NOT NULL`},
	// The sweeper asks MusicBrainz again about every file an anchor proved,
	// once its turn comes (ADR 0037 §6). Nothing in the fixture was proven that
	// way, so this is zero at any clock.
	{"source files due another look", `
		SELECT count(*) FROM library_file_identities WHERE source_recheck_after IS NOT NULL`},
	// The preview sweep fetches a thirty-second excerpt from Deezer for every
	// want that is due one, which is a request to somebody else's service in the
	// middle of a browser test. No want in the fixture carries an ISRC, and a
	// preview is only ever fetched by ISRC, so this is zero at any clock rather
	// than at this one — but a want given an ISRC later would be given a due
	// time with it, which is what this count is here to catch.
	{"wants due a preview anchor", `
		SELECT count(*) FROM acquisition_targets
		WHERE anchor_fingerprint IS NULL AND anchor_next_attempt_at IS NOT NULL`},
	// The fingerprint sweep measures the audio of every held copy that has none
	// on record, which decodes a file per copy in the middle of a browser test.
	// No held copy in the fixture names a download request, and the pass only
	// ever looks at copies whose request delivered bytes it can still read, so
	// this is zero at any clock — but a copy given a request later would be
	// picked up, which is what this count is here to catch.
	{"held copies due a fingerprint", `
		SELECT count(*) FROM acquisition_target_files copies
		JOIN download_requests requests ON requests.id = copies.download_request_id
		WHERE copies.verdict = 'held'
		  AND copies.audio_fingerprint IS NULL
		  AND requests.status = 'completed'`},
	// The follow feed queues a catalogue refresh for any followed artist
	// MusicBrainz knows and nobody has looked at for a day. The day is this
	// test's own copy of acquisition.feedRefreshIdle, which is unexported —
	// shortening the real one moves what the feed does without moving what
	// this asserts, so changing it means changing this too. Nothing in the
	// fixture is both followed and known to MusicBrainz, which is what makes
	// the count zero at any clock rather than at this one.
	{"followed artists due a refresh", `
		SELECT count(*) FROM artists
		WHERE followed_at IS NOT NULL
		  AND musicbrainz_id IS NOT NULL
		  AND (last_refreshed_at IS NULL
		       OR last_refreshed_at < now() - interval '24 hours')`},
	// And it wants every track of a release published after the follow.
	{"releases newer than the follow that put them in the feed", `
		SELECT count(*) FROM albums
		JOIN artists ON artists.id = albums.artist_id
		WHERE artists.followed_at IS NOT NULL
		  AND albums.release_date > artists.followed_at::date`},
	// The cover sweep asks an archive about every release with no picture
	// row, and the artist sweep about every artist with none.
	{"releases with no cover", `
		SELECT count(*) FROM albums
		LEFT JOIN release_cover_art ON release_cover_art.album_id = albums.id
		WHERE release_cover_art.album_id IS NULL`},
	{"artists with no picture", `
		SELECT count(*) FROM artists
		LEFT JOIN artist_images ON artist_images.artist_id = artists.id
		WHERE artist_images.artist_id IS NULL`},
	// The same sweep asks Wikipedia about every artist with a MusicBrainz
	// identifier nobody has asked about. A time with no words beside it is the
	// answer "asked, and there is no article", and every artist here carries
	// one.
	{"artists nobody has asked an encyclopaedia about", `
		SELECT count(*) FROM artists
		WHERE musicbrainz_id IS NOT NULL AND biography_fetched_at IS NULL`},
	// The genre backfill queues an artist refresh for every artist whose
	// community genres nobody has asked MusicBrainz for — their own, or one of
	// their releases'. An empty list is that question answered.
	{"artists whose genres nobody has asked for", `
		SELECT count(*) FROM artists
		WHERE musicbrainz_id IS NOT NULL
		  AND (genres IS NULL
		       OR EXISTS (SELECT 1 FROM albums
		                  WHERE albums.artist_id = artists.id
		                    AND albums.musicbrainz_release_group_id IS NOT NULL
		                    AND albums.genres IS NULL))`},
	// Startup queues a recommendation sweep for an enabled ListenBrainz
	// account, and a sweep replaces every seeded recommendation row with
	// whatever ListenBrainz answers. The fixture seeds the stored answer and
	// no account, which is the state an installation is in after somebody
	// disconnects one.
	{"enabled recommendation accounts", `
		SELECT count(*) FROM listenbrainz_settings WHERE enabled`},
	// The spectrum sweep decodes a few seconds of every file it has never
	// listened to. None of the fixture's files is on disc, so a pass over them
	// would run a decoder per row in the middle of a browser test and learn
	// nothing. Every seeded file is stamped as measured, at any clock: the stamp
	// is a time the fixture wrote rather than a window measured back from now().
	{"files waiting to be measured", `
		SELECT count(*) FROM library_files
		WHERE missing_at IS NULL AND sample_rate_hz IS NOT NULL
		  AND spectrum_read_at IS NULL`},
	// The upgrade sweep looks for library files below the stored bit-rate
	// floor and ships switched off, in its own settings — the same reasoning
	// as listenbrainz_settings above. A fixture that shipped it enabled would
	// have the sweep raise wants, and possibly delete music, in the middle of
	// a browser test.
	{"upgrade sweeps enabled", `
		SELECT count(*) FROM upgrade_settings WHERE enabled`},
	// A job the fixture left runnable would run, and change something.
	{"runnable jobs", `
		SELECT count(*) FROM jobs WHERE status IN ('queued', 'running')`},
}

// The two clocks the fixture is read at, as PostgreSQL interval literals: the
// moment it was seeded, and far enough past every window Schall measures that
// one written as an age has expired by then.
const (
	asSeeded = "0 seconds"
	aYearOn  = "1 year"
)

// assertStill reads every stillness count with the database's clock moved ahead
// by the given interval, and requires all of them to be zero.
func assertStill(ctx context.Context, t *testing.T, pool *pgxpool.Pool, ahead string) {
	t.Helper()

	transaction, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the stillness read: %v", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	shiftTheClock(ctx, t, transaction, ahead)

	for _, quiet := range stillness {
		if borrowed := unshiftedClock(quiet.query); borrowed != "" {
			t.Errorf("%s reads the clock as %s, which the shift does not reach; ask for now()",
				quiet.what, borrowed)
			continue
		}
		var count int
		if err := transaction.QueryRow(ctx, quiet.query).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", quiet.what, err)
		}
		if count != 0 {
			t.Errorf("read %s ahead the fixture leaves %d %s; the seeded database has to stay still",
				ahead, count, quiet.what)
		}
	}
}

// shiftTheClock gives this transaction a now() of its own, ahead of the real one
// by the given interval, by putting a schema that defines one in front of
// pg_catalog on the search path. Every query the transaction then runs reads the
// moved clock whether or not its author knew there was one, which is the whole
// point: taking the clock through nine queries by hand is a convention the tenth
// can quietly opt out of, and opting out here means asserting nothing while
// still passing.
//
// The schema and the search path are both local to the transaction, and the
// transaction is rolled back, so the read leaves the database as it found it.
func shiftTheClock(ctx context.Context, t *testing.T, transaction pgx.Tx, ahead string) {
	t.Helper()

	for _, statement := range []string{
		"CREATE SCHEMA shifted",
		fmt.Sprintf(`CREATE FUNCTION shifted.now() RETURNS timestamptz LANGUAGE sql STABLE
			AS $$ SELECT pg_catalog.now() + interval '%s' $$`, ahead),
		"SET LOCAL search_path = shifted, pg_catalog, public",
	} {
		if _, err := transaction.Exec(ctx, statement); err != nil {
			t.Fatalf("shift the clock %s ahead: %v", ahead, err)
		}
	}
}

// unshiftedClock names the spelling of the clock in a query that the shift does
// not reach, or returns empty when the query asks for the clock only as now().
//
// The shift works by search path, and the search path only resolves names that
// go looking for one. current_timestamp and its relatives are parsed straight to
// pg_catalog, which nothing may be put in front of, and clock_timestamp reads
// the wall clock by definition. Either would read the present in a test about
// the future — the same silent hole as a query that was never handed the clock —
// so a query spelling it that way is refused here rather than run.
func unshiftedClock(query string) string {
	lowered := strings.ToLower(query)
	for _, spelling := range []string{
		"current_timestamp", "current_date", "current_time",
		"localtimestamp", "localtime",
		"clock_timestamp", "statement_timestamp", "transaction_timestamp",
	} {
		if strings.Contains(lowered, spelling) {
			return spelling
		}
	}
	return ""
}

// The refusal is what stands where the shift stops, so it is worth knowing it
// still recognises the spellings it was written for — including in the upper
// case somebody will eventually write them in.
func TestAClockTheShiftCannotReachIsRefused(t *testing.T) {
	for _, query := range []string{
		"SELECT count(*) FROM jobs WHERE created_at > current_timestamp - interval '1 day'",
		"SELECT count(*) FROM albums WHERE release_date > CURRENT_DATE",
		"SELECT count(*) FROM jobs WHERE created_at > clock_timestamp()",
	} {
		if unshiftedClock(query) == "" {
			t.Errorf("%q borrows a clock the shift cannot reach and was not refused", query)
		}
	}
	for _, query := range []string{
		"SELECT count(*) FROM jobs WHERE status IN ('queued', 'running')",
		"SELECT count(*) FROM artists WHERE last_refreshed_at < now() - interval '24 hours'",
	} {
		if borrowed := unshiftedClock(query); borrowed != "" {
			t.Errorf("%q asks for the clock as now() but was refused as %s", query, borrowed)
		}
	}
}

func TestSeededDatabaseLeavesTheQueueNothingToDo(t *testing.T) {
	pool := dbtest.Setup(t)
	ctx := context.Background()

	if err := seed.Apply(ctx, pool); err != nil {
		t.Fatalf("apply seed: %v", err)
	}

	assertStill(ctx, t, pool, asSeeded)
}

// The test above reads the fixture at the moment it was written, which for a
// fixture whose times are ages is the one moment it is certain to be right. A
// database seeded on Friday is read on Monday, though: every window Schall
// measures back from now() has moved three days closer, and nothing in the
// fixture has moved at all. That is how a seeded database used to start queueing
// MusicBrainz requests for invented identifiers overnight — the properties
// above all held, and only at breakfast.
//
// So the same properties are asked again of a clock a year on. A year is far
// past acquisition.feedRefreshIdle and past any window a later fixture might be
// written against, and it is the whole set rather than the follow feed alone
// because the fault was never one wrong number: stillness had been written as an
// age, and any of these could be written that way next.
func TestTheSeededDatabaseStaysStillAsItAges(t *testing.T) {
	pool := dbtest.Setup(t)
	ctx := context.Background()

	if err := seed.Apply(ctx, pool); err != nil {
		t.Fatalf("apply seed: %v", err)
	}

	assertStill(ctx, t, pool, aYearOn)
}

// And the year has to arrive. The stillness queries name the clock as now() and
// are told nothing about the shift, so a shift that stopped reaching a plain
// now() would leave every one of them reading the present — the aged run would
// assert exactly what the run above it already did, and pass while proving
// nothing. That failure is invisible in the counts, which stay zero either way,
// so it is asserted here instead.
func TestTheShiftedClockReachesAPlainNow(t *testing.T) {
	pool := dbtest.Setup(t)
	ctx := context.Background()

	transaction, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the clock read: %v", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	shiftTheClock(ctx, t, transaction, aYearOn)

	var shifted bool
	if err := transaction.QueryRow(ctx,
		"SELECT now() = pg_catalog.now() + interval '"+aYearOn+"'").Scan(&shifted); err != nil {
		t.Fatalf("read the shifted clock: %v", err)
	}
	if !shifted {
		t.Errorf("a query asking for now() read the real clock, not the one shifted %s ahead", aYearOn)
	}
}
