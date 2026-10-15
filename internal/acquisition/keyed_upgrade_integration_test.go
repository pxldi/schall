package acquisition

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/library"
	"github.com/rs/zerolog"
)

// A keyed want that took the copy from its link under the floor goes on looking
// for a better copy while replacing low-quality files is switched on
// (owner's decision, 2026-10-15).

// keyedHoldingAFile keys a want to addressedTrack after the library already
// holds that track in a file of the given name and bit rate, which acquires it.
func keyedHoldingAFile(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, name string, bitRateKbps int,
) (*Service, db.AcquisitionTargetRow, uuid.UUID) {
	t.Helper()
	service := newTestService(pool).
		WithUpgrader(library.NewRemover(pool, zerolog.Nop())).
		WithTrackSources(&fakeTrackSources{track: addressedTrack}, fakePrints{value: excerptFingerprint})
	rootID, rootPath := upgradeRoot(ctx, t, pool)
	var fileID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO library_files (library_root_id, path, size_bytes, modified_at, bit_rate_kbps,
		                           resolution_status)
		VALUES ($1, $2, 5, now(), $3, 'source')
		RETURNING id
	`, rootID, filepath.Join(rootPath, name), bitRateKbps).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (library_file_id, kind, provider, source, external_id,
		                                     external_url, method, is_manual)
		VALUES ($1, 'source', 'soundcloud', 'soundcloud', $2, $3, 'source_url', true)
	`, fileID, addressedTrack.ExternalID, addressedTrack.URL); err != nil {
		t.Fatal(err)
	}
	target := createEntry(ctx, t, service)
	keyed, err := service.KeyToSource(ctx, target.ID, addressedTrack.URL, addressedTrack.ExternalID)
	if err != nil {
		t.Fatalf("KeyToSource() error = %v", err)
	}
	if keyed.Status != "acquired" {
		t.Fatalf("want = %s, want acquired with the file the library holds", keyed.Status)
	}
	return service, keyed, fileID
}

// liveWantFor reads the want keyed to addressedTrack that is not superseded.
func liveWantFor(ctx context.Context, t *testing.T, pool *pgxpool.Pool) (uuid.UUID, string, uuid.NullUUID, time.Time) {
	t.Helper()
	var id uuid.UUID
	var origin string
	var upgradeOf uuid.NullUUID
	var searchAt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT id, origin, upgrade_of_library_file_id, next_search_at FROM acquisition_targets
		WHERE external_id = $1 AND status <> 'superseded'
	`, addressedTrack.ExternalID).Scan(&id, &origin, &upgradeOf, &searchAt); err != nil {
		t.Fatal(err)
	}
	if searchAt == nil {
		return id, origin, upgradeOf, time.Time{}
	}
	return id, origin, upgradeOf, *searchAt
}

func TestAKeyedWantHoldingAFileUnderTheFloorLooksForABetterCopy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	switchOnUpgrades(ctx, t, pool)
	service, keyed, fileID := keyedHoldingAFile(ctx, t, pool, "illegal.m4a", 160)

	service.keepLookingAboveFloor(ctx, keyed, false)

	id, origin, upgradeOf, searchAt := liveWantFor(ctx, t, pool)
	if id == keyed.ID || origin != "upgrade" || upgradeOf.UUID != fileID {
		t.Fatalf("live want %s is %q for %v, want a new upgrade want for %s", id, origin, upgradeOf, fileID)
	}
	if time.Until(searchAt) > time.Minute {
		t.Errorf("first search at %v, want it due now", searchAt)
	}
}

func TestAKeyedWantLooksForNoBetterCopyWhileUpgradesAreSwitchedOff(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service, keyed, _ := keyedHoldingAFile(ctx, t, pool, "illegal.m4a", 160)

	service.keepLookingAboveFloor(ctx, keyed, false)

	if id, _, _, _ := liveWantFor(ctx, t, pool); id != keyed.ID {
		t.Fatalf("live want = %s, want the acquired want %s left alone", id, keyed.ID)
	}
}

func TestAKeyedWantHoldingAFileAtTheFloorLooksForNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	switchOnUpgrades(ctx, t, pool)
	service, keyed, _ := keyedHoldingAFile(ctx, t, pool, "illegal.mp3", 320)

	service.keepLookingAboveFloor(ctx, keyed, false)

	if id, _, _, _ := liveWantFor(ctx, t, pool); id != keyed.ID {
		t.Fatalf("live want = %s, want the acquired want %s left alone", id, keyed.ID)
	}
}

// A copy the upgrader discarded as no better leaves the want waiting out the
// stand-back before it asks Soulseek again.
func TestAKeyedUpgradeWhoseCopyWasNoBetterLooksAgainAfterTheStandBack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	switchOnUpgrades(ctx, t, pool)
	service, keyed, fileID := keyedHoldingAFile(ctx, t, pool, "illegal.m4a", 160)
	service.keepLookingAboveFloor(ctx, keyed, false)
	upgradeID, _, _, _ := liveWantFor(ctx, t, pool)
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET status = 'acquired', acquired_at = now(), next_search_at = NULL
		WHERE id = $1
	`, upgradeID); err != nil {
		t.Fatal(err)
	}
	upgrade, err := service.Target(ctx, upgradeID)
	if err != nil {
		t.Fatal(err)
	}

	service.keepLookingAboveFloor(ctx, upgrade, true)

	id, origin, upgradeOf, searchAt := liveWantFor(ctx, t, pool)
	if id == upgradeID || origin != "upgrade" || upgradeOf.UUID != fileID {
		t.Fatalf("live want %s is %q for %v, want a fresh upgrade want for %s", id, origin, upgradeOf, fileID)
	}
	wait := time.Until(searchAt)
	if wait < keyedUpgradeStandBack-time.Hour || wait > keyedUpgradeStandBack+time.Hour {
		t.Errorf("next search in %v, want the stand-back of %v", wait, keyedUpgradeStandBack)
	}
}

// The want looking for a better copy never fetches from the link: the link's
// copy is the file it already holds.
func TestAKeyedUpgradeWantIsNeverFetchedFromItsLink(t *testing.T) {
	service := newTestService(nil).WithSourceFetches(&fakeSourceFetcher{}, t.TempDir())
	if service.fetchesFromSource(db.AcquisitionTargetRow{Source: "soundcloud", Origin: "upgrade"}) {
		t.Fatal("an upgrade want would fetch the copy it already holds")
	}
	if !service.fetchesFromSource(db.AcquisitionTargetRow{Source: "soundcloud", Origin: "manual"}) {
		t.Fatal("a keyed want no longer fetches from its link")
	}
}
