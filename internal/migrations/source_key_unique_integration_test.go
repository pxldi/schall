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

// The schema 00105 is applied over. It is dropped again whatever the test
// concludes.
const sourceKeySchema = "schall_source_key_check"

// 00105 merges the wants already keyed to one address before it adds the index
// that keeps them one. Without the merge the index cannot be built on a
// database that has the duplicates the index exists to prevent.
func TestTheSourceKeyMigrationMergesWantsKeyedToOneAddress(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	databaseURL := os.Getenv("SCHALL_TEST_DATABASE_URL")

	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+sourceKeySchema+" CASCADE"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+sourceKeySchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+sourceKeySchema+" CASCADE")
	})
	separator := "?"
	if strings.Contains(databaseURL, "?") {
		separator = "&"
	}
	scopedURL := databaseURL + separator + "search_path=" + sourceKeySchema + ",public"
	if err := migrations.UpTo(ctx, scopedURL, 104); err != nil {
		t.Fatalf("apply the migrations before the one under test: %v", err)
	}

	// The want somebody removed is the oldest. A want still looking survives it,
	// and the oldest of those survives the rest.
	now := time.Now()
	rows := []struct {
		title   string
		id      string
		status  string
		created time.Time
	}{
		{"removed", "soundcloud:293", "not_wanted", now.Add(-3 * time.Hour)},
		{"first", "soundcloud:293", "unresolved", now.Add(-2 * time.Hour)},
		{"second", "soundcloud:293", "unresolved", now.Add(-time.Hour)},
		{"other", "soundcloud:294", "unresolved", now},
	}
	ids := map[string]uuid.UUID{}
	for _, row := range rows {
		notWantedAt := pgtype.Timestamptz{}
		if row.status == "not_wanted" {
			notWantedAt = pgtype.Timestamptz{Time: now, Valid: true}
		}
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO `+sourceKeySchema+`.acquisition_targets
			    (origin, entry_title, status, not_wanted_at, created_at,
			     source, external_id, external_url, minimum_bitrate)
			VALUES ('manual', $1, $2, $3, $4, 'soundcloud', $5, 'https://soundcloud.com/abo/edit', 320)
			RETURNING id
		`, row.title, row.status, notWantedAt, row.created, row.id).Scan(&id); err != nil {
			t.Fatalf("insert %q: %v", row.title, err)
		}
		ids[row.title] = id
	}

	if err := migrations.UpTo(ctx, scopedURL, 105); err != nil {
		t.Fatalf("apply the migration under test: %v", err)
	}

	for _, row := range rows {
		var status string
		var by pgtype.UUID
		if err := pool.QueryRow(ctx, `
			SELECT status, superseded_by_id FROM `+sourceKeySchema+`.acquisition_targets WHERE id = $1
		`, ids[row.title]).Scan(&status, &by); err != nil {
			t.Fatalf("read %q back: %v", row.title, err)
		}
		switch row.title {
		case "first", "other":
			if status != "unresolved" || by.Valid {
				t.Errorf("%q = %s by %v, want it left as it was", row.title, status, by)
			}
		default:
			if status != "superseded" || !by.Valid || uuid.UUID(by.Bytes) != ids["first"] {
				t.Errorf("%q = %s by %v, want it superseded by the oldest want still looking",
					row.title, status, by)
			}
		}
	}
}
