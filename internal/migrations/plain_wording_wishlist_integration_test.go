package migrations_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/migrations"
)

const plainWordingSchema = "schall_plain_wording_wishlist_check"

// plainWordingBefore applies every migration before 00114 in a schema of its
// own and returns that schema's name.
func plainWordingBefore(ctx context.Context, t *testing.T, pool *pgxpool.Pool) (string, string) {
	t.Helper()
	databaseURL := os.Getenv("SCHALL_TEST_DATABASE_URL")
	schema := plainWordingSchema
	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	})
	separator := "?"
	if strings.Contains(databaseURL, "?") {
		separator = "&"
	}
	scopedURL := databaseURL + separator + "search_path=" + schema + ",public"
	if err := migrations.UpTo(ctx, scopedURL, 113); err != nil {
		t.Fatalf("apply the migrations before the one under test: %v", err)
	}
	return schema, scopedURL
}

func plainWordingWant(ctx context.Context, t *testing.T, pool *pgxpool.Pool, schema, summary string) uuid.UUID {
	t.Helper()
	targetID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO `+schema+`.acquisition_targets (id, origin, entry_title, status, summary)
		VALUES ($1, 'playlist', 'Song', 'unresolved', $2)
	`, targetID, summary); err != nil {
		t.Fatalf("insert want %q: %v", summary, err)
	}
	return targetID
}

func plainWordingSummary(ctx context.Context, t *testing.T, pool *pgxpool.Pool, schema, table string, id uuid.UUID) string {
	t.Helper()
	var summary string
	if err := pool.QueryRow(ctx,
		"SELECT summary FROM "+schema+"."+table+" WHERE id = $1", id).Scan(&summary); err != nil {
		t.Fatal(err)
	}
	return summary
}

// The acquisition loop compares a stranded want's summary and a copy's
// deferred or never-sent summary exactly. Rows written before 00114 carry the
// old sentence, and would stop matching unless the migration moves them too.
func TestTheMigrationRewordsTheSummariesTheLoopComparesExactly(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	schema, scopedURL := plainWordingBefore(ctx, t, pool)

	stranded := plainWordingWant(ctx, t, pool, schema,
		"A copy was imported, but it never appeared in your library. "+
			"Nothing is being fetched for this wishlist entry while that copy sits there.")
	unfound := plainWordingWant(ctx, t, pool, schema,
		"No recording MusicBrainz knows of matches this entry yet. It will be asked about again.")
	copies := map[string]string{
		"The peer is already sending as much as it will take at once, so this copy was not sent. " +
			"Nothing about the music was learned.": "Peer busy. Copy not sent.",
		"The peer accepted this download and never sent it.": "Peer accepted but never sent it",
	}
	copyIDs := map[uuid.UUID]string{}
	for old, want := range copies {
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO `+schema+`.acquisition_target_files
			    (acquisition_target_id, provider, source_username, remote_path,
			     file_name, size_bytes, verdict, decided_by, summary)
			VALUES ($1, 'slskd', 'peer', 'Music/x/'||$3||'.flac', $3||'.flac', 9000000,
			        'undelivered', 'schall', $2)
			RETURNING id
		`, stranded, old, uuid.NewString()).Scan(&id); err != nil {
			t.Fatalf("insert copy %q: %v", old, err)
		}
		copyIDs[id] = want
	}

	if err := migrations.UpTo(ctx, scopedURL, 114); err != nil {
		t.Fatalf("apply the migration under test: %v", err)
	}

	if got := plainWordingSummary(ctx, t, pool, schema, "acquisition_targets", stranded); got !=
		"Imported copy missing from your library. Downloads paused." {
		t.Errorf("stranded want says %q", got)
	}
	if got := plainWordingSummary(ctx, t, pool, schema, "acquisition_targets", unfound); got !=
		"Not on MusicBrainz yet. Retrying later." {
		t.Errorf("unfound want says %q", got)
	}
	for id, want := range copyIDs {
		if got := plainWordingSummary(ctx, t, pool, schema, "acquisition_target_files", id); got != want {
			t.Errorf("copy says %q, want %q", got, want)
		}
	}
}

// A sentence built around a song's own names keeps those names when it is
// reworded.
func TestTheMigrationRewordsABuiltSummaryAroundItsNames(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	schema, scopedURL := plainWordingBefore(ctx, t, pool)

	filed := plainWordingWant(ctx, t, pool, schema,
		`Your library files that copy as "Intro" by Ben. This entry wants "Intro (Live)" by Ben, `+
			`and the audio cannot tell the two apart. Match the file to this track, or stop looking.`)
	floor := plainWordingWant(ctx, t, pool, schema,
		"Only copies below your 320 kbps minimum were found. Looked at again weekly.")

	if err := migrations.UpTo(ctx, scopedURL, 114); err != nil {
		t.Fatalf("apply the migration under test: %v", err)
	}

	if got, want := plainWordingSummary(ctx, t, pool, schema, "acquisition_targets", filed),
		`Library files this copy as "Intro" by Ben, not "Intro (Live)" by Ben. `+
			`Same audio. Match the file, or remove the song.`; got != want {
		t.Errorf("filed elsewhere says %q, want %q", got, want)
	}
	if got, want := plainWordingSummary(ctx, t, pool, schema, "acquisition_targets", floor),
		"Only copies under 320 kbps. Retrying weekly."; got != want {
		t.Errorf("below floor says %q, want %q", got, want)
	}
}
