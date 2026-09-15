// This file is the external test package for the same reason the rest of the
// migration tests are: internal/dbtest applies migrations itself, and an
// in-package test importing it would be an import cycle.
package migrations_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/migrations"
)

// The schema 00085 is applied over, so the rows it changes can be put there
// before it runs. It is dropped again whatever the test concludes.
const youtubeAnchorSchema = "schall_youtube_anchor_check"

// 00085 does two things: it adds the two columns an upload reference needs, and
// it puts the wants that were written off before there was a second place to
// look back on the schedule. The second is the one worth a test — a data
// migration that reopened nothing, or reopened a want somebody had stopped
// asking for, would both look like a migration that applied cleanly.
func TestTheUploadMigrationReopensTheWantsDeezerCouldNotAnswerFor(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	databaseURL := os.Getenv("SCHALL_TEST_DATABASE_URL")

	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+youtubeAnchorSchema+" CASCADE"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+youtubeAnchorSchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			"DROP SCHEMA IF EXISTS "+youtubeAnchorSchema+" CASCADE")
	})

	separator := "?"
	if strings.Contains(databaseURL, "?") {
		separator = "&"
	}
	scopedURL := databaseURL + separator + "search_path=" + youtubeAnchorSchema + ",public"
	if err := migrations.UpTo(ctx, scopedURL, 84); err != nil {
		t.Fatalf("apply the migrations before the one under test: %v", err)
	}

	// One row for each sentence the preview path writes, plus the two that must
	// be left alone: a want somebody stopped asking for, and one whose preview
	// could not be read at all rather than not existing.
	rows := []struct {
		title       string
		unavailable string
		status      string
		reopened    bool
	}{
		{"no preview", "Deezer publishes no preview for this recording's ISRC.", "pending", true},
		{
			"no isrc",
			"This entry carries no ISRC, and a preview is only ever fetched by ISRC.",
			"unresolved", true,
		},
		{
			"broken isrc",
			"This entry's ISRC is not a registration code a preview can be fetched by.",
			"awaiting_review", true,
		},
		{"not wanted", "Deezer publishes no preview for this recording's ISRC.", "not_wanted", false},
		{"unreadable", "The preview Deezer returned could not be read as audio.", "pending", false},
	}
	for _, row := range rows {
		reviewReason := pgtype.Text{}
		if row.status == "awaiting_review" {
			reviewReason = pgtype.Text{String: "resolution", Valid: true}
		}
		notWantedAt := pgtype.Timestamptz{}
		if row.status == "not_wanted" {
			notWantedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
		}
		// A want that is being searched for has to name a recording, and one that
		// is still unresolved must not. Both constraints are the schema's own.
		recording := pgtype.UUID{}
		if row.status == "pending" || row.status == "searching" {
			recording = pgtype.UUID{Bytes: uuid.New(), Valid: true}
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO `+youtubeAnchorSchema+`.acquisition_targets
			    (origin, entry_title, status, musicbrainz_recording_id,
			     review_reason, not_wanted_at, anchor_unavailable)
			VALUES ('manual', $1, $2, $3, $4, $5, $6)
		`, row.title, row.status, recording, reviewReason, notWantedAt,
			row.unavailable); err != nil {
			t.Fatalf("insert %q: %v", row.title, err)
		}
	}

	if err := migrations.UpTo(ctx, scopedURL, 85); err != nil {
		t.Fatalf("apply the migration under test: %v", err)
	}

	for _, row := range rows {
		var unavailable pgtype.Text
		var due pgtype.Timestamptz
		err := pool.QueryRow(ctx, `
			SELECT anchor_unavailable, anchor_next_attempt_at
			FROM `+youtubeAnchorSchema+`.acquisition_targets WHERE entry_title = $1
		`, row.title).Scan(&unavailable, &due)
		if err != nil {
			t.Fatalf("read %q back: %v", row.title, err)
		}
		if row.reopened {
			if unavailable.Valid || !due.Valid {
				t.Errorf("%q: unavailable = %q, due = %v; want the want back on the schedule",
					row.title, unavailable.String, due)
			}
			continue
		}
		if !unavailable.Valid || due.Valid {
			t.Errorf("%q: unavailable = %q, due = %v; want it left exactly as it was",
				row.title, unavailable.String, due)
		}
	}
}
