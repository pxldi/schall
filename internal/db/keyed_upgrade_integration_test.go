package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
)

// keyedAndHeld is a want keyed to soundcloud:293 and acquired with a file of the
// given path and bit rate, the state a fetch from the address leaves behind.
func keyedAndHeld(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, path string, bitRateKbps int,
) (*Queries, uuid.UUID, uuid.UUID) {
	t.Helper()
	queries, targetID, _ := anchoredWithoutARecording(ctx, t, pool, "deezer", "2178654")
	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at, resolution_status, bit_rate_kbps)
		VALUES ($1, $2, 4000000, now(), 'source', $3)
	`, fileID, path, bitRateKbps); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (library_file_id, kind, provider, source, external_id,
		                                     external_url, method, is_manual)
		VALUES ($1, 'source', 'soundcloud', 'soundcloud', 'soundcloud:293',
		        'https://soundcloud.com/abo/illegal-edit', 'source_url', true)
	`, fileID); err != nil {
		t.Fatal(err)
	}
	keyed, err := queries.KeyAcquisitionTargetToSource(ctx, keySoundCloud(targetID))
	if err != nil {
		t.Fatalf("KeyAcquisitionTargetToSource() error = %v", err)
	}
	if keyed.Status != "acquired" {
		t.Fatalf("want = %s, want acquired with the file already held", keyed.Status)
	}
	return queries, targetID, fileID
}

func TestAKeyedWantsLossyFileUnderTheFloorIsReported(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, fileID := keyedAndHeld(ctx, t, pool, "/music/Abo/illegal.m4a", 160)

	under, err := queries.KeyedFileUnderFloor(ctx, targetID)
	if err != nil {
		t.Fatalf("KeyedFileUnderFloor() error = %v", err)
	}
	if !under.Valid || under.UUID != fileID {
		t.Errorf("file under floor = %v, want %s", under, fileID)
	}
}

func TestAKeyedWantsLosslessFileIsNeverUnderTheFloor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	// A FLAC's average bit rate can sit under 320 on quiet music.
	queries, targetID, _ := keyedAndHeld(ctx, t, pool, "/music/Abo/illegal.flac", 280)

	under, err := queries.KeyedFileUnderFloor(ctx, targetID)
	if err != nil {
		t.Fatalf("KeyedFileUnderFloor() error = %v", err)
	}
	if under.Valid {
		t.Errorf("file under floor = %v, want none for a lossless file", under)
	}
}

func TestAKeyedWantsFileAtTheFloorIsNotReported(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, _ := keyedAndHeld(ctx, t, pool, "/music/Abo/illegal.mp3", 320)

	under, err := queries.KeyedFileUnderFloor(ctx, targetID)
	if err != nil {
		t.Fatalf("KeyedFileUnderFloor() error = %v", err)
	}
	if under.Valid {
		t.Errorf("file under floor = %v, want none at the floor", under)
	}
}

func TestRaisingAKeyedUpgradeHandsTheKeyToANewWant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, fileID := keyedAndHeld(ctx, t, pool, "/music/Abo/illegal.m4a", 160)
	searchAt := time.Now().Add(time.Hour).Truncate(time.Second)

	newID, err := queries.RaiseKeyedUpgrade(ctx, targetID, fileID, searchAt, "Looking for a better copy.")
	if err != nil {
		t.Fatalf("RaiseKeyedUpgrade() error = %v", err)
	}

	var oldStatus string
	var supersededBy uuid.NullUUID
	if err := pool.QueryRow(ctx, `
		SELECT status, superseded_by_id FROM acquisition_targets WHERE id = $1
	`, targetID).Scan(&oldStatus, &supersededBy); err != nil {
		t.Fatal(err)
	}
	if oldStatus != "superseded" || supersededBy.UUID != newID {
		t.Errorf("old want = %s by %v, want superseded by %s", oldStatus, supersededBy, newID)
	}

	var origin, status, externalID, anchorSource, summary string
	var minimum int32
	var upgradeOf uuid.NullUUID
	var nextSearch time.Time
	if err := pool.QueryRow(ctx, `
		SELECT origin, status, external_id, anchor_source, minimum_bitrate,
		       upgrade_of_library_file_id, next_search_at, summary
		FROM acquisition_targets WHERE id = $1
	`, newID).Scan(&origin, &status, &externalID, &anchorSource, &minimum,
		&upgradeOf, &nextSearch, &summary); err != nil {
		t.Fatal(err)
	}
	if origin != "upgrade" || status != "unresolved" || externalID != "soundcloud:293" ||
		anchorSource != "source" || minimum != 320 {
		t.Errorf("new want = %s %s %q anchor %q floor %d, want an upgrade keyed to the same track",
			origin, status, externalID, anchorSource, minimum)
	}
	if upgradeOf.UUID != fileID || !nextSearch.Equal(searchAt) || summary != "Looking for a better copy." {
		t.Errorf("new want replaces %v, searches %v, says %q", upgradeOf, nextSearch, summary)
	}
}

func TestAKeyedUpgradeIsRaisedOnlyOncePerFile(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, fileID := keyedAndHeld(ctx, t, pool, "/music/Abo/illegal.m4a", 160)
	if _, err := queries.RaiseKeyedUpgrade(ctx, targetID, fileID, time.Now(), "first"); err != nil {
		t.Fatalf("RaiseKeyedUpgrade() error = %v", err)
	}

	_, err := queries.RaiseKeyedUpgrade(ctx, targetID, fileID, time.Now(), "second")
	if !errors.Is(err, ErrNoKeyedUpgrade) {
		t.Errorf("second RaiseKeyedUpgrade() error = %v, want ErrNoKeyedUpgrade", err)
	}
}

// An upgrade want whose copy was no better is acquired holding the old file
// still. It hands the key on to the next search for that same file.
func TestAnUpgradeWantWhoseCopyWasNoBetterCanBeFollowedAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, fileID := keyedAndHeld(ctx, t, pool, "/music/Abo/illegal.m4a", 160)
	upgradeID, err := queries.RaiseKeyedUpgrade(ctx, targetID, fileID, time.Now(), "first")
	if err != nil {
		t.Fatalf("RaiseKeyedUpgrade() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET status = 'acquired', acquired_at = now(), next_search_at = NULL
		WHERE id = $1
	`, upgradeID); err != nil {
		t.Fatal(err)
	}

	if _, err := queries.RaiseKeyedUpgrade(ctx, upgradeID, fileID, time.Now(), "again"); err != nil {
		t.Errorf("RaiseKeyedUpgrade() after a copy that was no better: %v", err)
	}
}

func TestAWantWithoutAKeyRaisesNoKeyedUpgrade(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, _ := anchoredWithoutARecording(ctx, t, pool, "deezer", "2178654")

	_, err := queries.RaiseKeyedUpgrade(ctx, targetID, uuid.New(), time.Now(), "never")
	if !errors.Is(err, ErrNoKeyedUpgrade) {
		t.Errorf("RaiseKeyedUpgrade() error = %v, want ErrNoKeyedUpgrade", err)
	}
}
