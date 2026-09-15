// External test package for the same reason the rest of the migration tests
// are: internal/dbtest applies migrations itself, and an in-package test
// importing it would be an import cycle.
package migrations_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/migrations"
)

// The schema 00096 is applied over, so the rows it changes can be put there
// before it runs.
const recheckSchema = "schall_recheck_check"

// 00096 marks the wants that are waiting for evidence rather than for a person,
// and the MusicBrainz kind is due now. A due time is a fact on a row and nothing
// reads it to start work, so the migration has to queue the look as well. A want
// whose 00089-era job was completed or failed away would otherwise sit at a due
// time nothing ever acts on.
func TestTheRecheckMigrationQueuesTheLookItSaysIsDue(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	databaseURL := os.Getenv("SCHALL_TEST_DATABASE_URL")

	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+recheckSchema+" CASCADE"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+recheckSchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+recheckSchema+" CASCADE")
	})

	separator := "?"
	if strings.Contains(databaseURL, "?") {
		separator = "&"
	}
	scopedURL := databaseURL + separator + "search_path=" + recheckSchema + ",public"
	if err := migrations.UpTo(ctx, scopedURL, 95); err != nil {
		t.Fatalf("apply the migrations before the one under test: %v", err)
	}

	// Three wants. One stopped on the unanswerable registration question with no
	// job left for it, one with the job 00089 queued still waiting, and one
	// holding an ordinary question that has nothing to do with the provider.
	rows := []struct {
		title  string
		summry string
		queued bool
		wants  int
	}{
		{
			"stranded", "The audio was identified as another recording, and MusicBrainz " +
				"could not be asked whether that is this track entered twice.", false, 1,
		},
		{
			"already queued", "The audio was identified as another recording, and MusicBrainz " +
				"could not be asked whether that is this track entered twice.", true, 1,
		},
		{"undecided", "Nothing could decide whether this copy is the wanted recording.", false, 0},
	}
	ids := make(map[string]uuid.UUID, len(rows))
	for _, row := range rows {
		var targetID uuid.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO `+recheckSchema+`.acquisition_targets
			    (origin, entry_title, status, musicbrainz_recording_id, next_attempt_at)
			VALUES ('manual', $1, 'pending', $2, now())
			RETURNING id
		`, row.title, uuid.New()).Scan(&targetID); err != nil {
			t.Fatalf("insert the want %q: %v", row.title, err)
		}
		ids[row.title] = targetID
		if _, err := pool.Exec(ctx, `
			INSERT INTO `+recheckSchema+`.acquisition_target_files
			    (acquisition_target_id, provider, source_username, remote_path,
			     file_name, size_bytes, verdict, decided_by, summary)
			VALUES ($1, 'slskd', 'peer', 'Music/x/03.flac', '03.flac', 9000000,
			        'held', 'schall', $2)
		`, targetID, row.summry); err != nil {
			t.Fatalf("insert the held copy of %q: %v", row.title, err)
		}
		if row.queued {
			if _, err := pool.Exec(ctx, `
				INSERT INTO `+recheckSchema+`.jobs (kind, payload, max_attempts, run_after)
				VALUES ('judge_want_copies',
				        jsonb_build_object('acquisition_target_id', $1::text), 3, now())
			`, targetID); err != nil {
				t.Fatalf("queue the surviving job for %q: %v", row.title, err)
			}
		}
	}

	if err := migrations.UpTo(ctx, scopedURL, 96); err != nil {
		t.Fatalf("apply the migration under test: %v", err)
	}

	for _, row := range rows {
		var reason pgtype.Text
		var due pgtype.Timestamptz
		if err := pool.QueryRow(ctx, `
			SELECT recheck_reason, recheck_after
			FROM `+recheckSchema+`.acquisition_targets WHERE entry_title = $1
		`, row.title).Scan(&reason, &due); err != nil {
			t.Fatalf("read %q back: %v", row.title, err)
		}
		var jobs int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM `+recheckSchema+`.jobs
			WHERE kind = 'judge_want_copies'
			  AND status IN ('queued', 'running')
			  AND payload->>'acquisition_target_id' = $1::text
		`, ids[row.title]).Scan(&jobs); err != nil {
			t.Fatalf("count the looks queued for %q: %v", row.title, err)
		}
		if jobs != row.wants {
			t.Errorf("%q: %d looks queued, want %d", row.title, jobs, row.wants)
		}
		if row.wants == 0 {
			if reason.Valid || due.Valid {
				t.Errorf("%q: reason = %q due = %v, want a want nothing marked",
					row.title, reason.String, due)
			}
			continue
		}
		if reason.String != "musicbrainz" || !due.Valid {
			t.Errorf("%q: reason = %q due = %v, want the provider wait recorded and due",
				row.title, reason.String, due)
		}
	}

}
