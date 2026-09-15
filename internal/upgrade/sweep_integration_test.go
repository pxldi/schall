package upgrade

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/rs/zerolog"
)

// libraryRoot lays down the one root every library file this package tests
// needs to belong to.
func libraryRoot(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	rootID := uuid.New()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO library_roots (id, path) VALUES ($1, $2)`, rootID, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	return rootID
}

// seededFile is one library file this package's integration tests can shape:
// its bit rate, its resolution status, and whether it carries a settled
// identity at all.
type seededFile struct {
	path         string
	bitRateKbps  *int
	resolved     bool
	settledProof string
	recordingID  uuid.UUID
}

func insertFile(t *testing.T, pool *pgxpool.Pool, rootID uuid.UUID, file seededFile) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	status := "pending"
	if file.resolved {
		status = "resolved"
	}
	var fileID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO library_files (
			library_root_id, path, size_bytes, modified_at, bit_rate_kbps,
			resolution_status, artist_tag, title_tag
		)
		VALUES ($1, $2, 14, now(), $3, $4, 'An Artist', 'A Title')
		RETURNING id
	`, rootID, file.path, file.bitRateKbps, status).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	if file.settledProof != "" {
		if _, err := pool.Exec(ctx, `
			INSERT INTO library_file_identities (
				library_file_id, kind, musicbrainz_recording_id, method, summary
			) VALUES ($1, 'external', $2, $3, 'the audio is this recording')
		`, fileID, file.recordingID, file.settledProof); err != nil {
			t.Fatal(err)
		}
	}
	return fileID
}

func enableUpgrades(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO upgrade_settings (singleton, enabled) VALUES (true, true)
		ON CONFLICT (singleton) DO UPDATE SET enabled = true
	`); err != nil {
		t.Fatal(err)
	}
}

func setFloor(t *testing.T, pool *pgxpool.Pool, minimumBitRate int32) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO source_preferences (singleton, minimum_bitrate)
		VALUES (true, $1)
		ON CONFLICT (singleton) DO UPDATE SET minimum_bitrate = excluded.minimum_bitrate
	`, minimumBitRate); err != nil {
		t.Fatal(err)
	}
}

func wantExists(t *testing.T, pool *pgxpool.Pool, recordingID uuid.UUID) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(context.Background(), `
		SELECT EXISTS (
			SELECT 1 FROM acquisition_targets
			WHERE musicbrainz_recording_id = $1 AND status <> 'superseded'
		)
	`, recordingID).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func bitRate(kbps int) *int {
	return &kbps
}

// An unresolved file is never targeted: it has no settled identity for the
// sweep to read a recording off, whatever its stored bit rate says.
func TestSweepNeverTargetsAnUnresolvedFile(t *testing.T) {
	pool := dbtest.Setup(t)
	enableUpgrades(t, pool)
	setFloor(t, pool, 320)
	rootID := libraryRoot(t, pool)
	recordingID := uuid.New()

	insertFile(t, pool, rootID, seededFile{
		path: "/music/unresolved.mp3", bitRateKbps: bitRate(128),
		resolved: false, recordingID: recordingID,
	})

	service := NewService(db.New(pool), zerolog.Nop())
	if _, err := service.Sweep(context.Background(), uuid.Nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if wantExists(t, pool, recordingID) {
		t.Fatal("a want was raised for an unresolved file")
	}
}

// A lossless file is never targeted, however low its stored bit rate reads.
func TestSweepNeverTargetsALosslessFileEndToEnd(t *testing.T) {
	pool := dbtest.Setup(t)
	enableUpgrades(t, pool)
	setFloor(t, pool, 1411)
	rootID := libraryRoot(t, pool)
	recordingID := uuid.New()

	insertFile(t, pool, rootID, seededFile{
		path: "/music/quiet.flac", bitRateKbps: bitRate(900),
		resolved: true, settledProof: "manual", recordingID: recordingID,
	})

	service := NewService(db.New(pool), zerolog.Nop())
	if _, err := service.Sweep(context.Background(), uuid.Nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if wantExists(t, pool, recordingID) {
		t.Fatal("a want was raised for a lossless file")
	}
}

// A file resolved only to plausible tags — not a settled proof — is left
// alone too: the sweep reads exactly the proofs a library witness may speak
// on, and nothing weaker.
func TestSweepNeverTargetsAFileWithNoSettledIdentity(t *testing.T) {
	pool := dbtest.Setup(t)
	enableUpgrades(t, pool)
	setFloor(t, pool, 320)
	rootID := libraryRoot(t, pool)
	recordingID := uuid.New()

	insertFile(t, pool, rootID, seededFile{
		path: "/music/tagged-only.mp3", bitRateKbps: bitRate(128),
		resolved: true, settledProof: "title-artist-duration", recordingID: recordingID,
	})

	service := NewService(db.New(pool), zerolog.Nop())
	if _, err := service.Sweep(context.Background(), uuid.Nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if wantExists(t, pool, recordingID) {
		t.Fatal("a want was raised for a file with no settled identity")
	}
}

func TestSweepRaisesAWantForASettledFileBelowTheFloorEndToEnd(t *testing.T) {
	pool := dbtest.Setup(t)
	enableUpgrades(t, pool)
	setFloor(t, pool, 320)
	rootID := libraryRoot(t, pool)
	recordingID := uuid.New()

	fileID := insertFile(t, pool, rootID, seededFile{
		path: "/music/below.mp3", bitRateKbps: bitRate(128),
		resolved: true, settledProof: "manual", recordingID: recordingID,
	})

	service := NewService(db.New(pool), zerolog.Nop())
	result, err := service.Sweep(context.Background(), uuid.Nil)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Raised != 1 {
		t.Fatalf("raised %d wants, want 1", result.Raised)
	}
	var upgradeOf uuid.NullUUID
	if err := pool.QueryRow(context.Background(), `
		SELECT upgrade_of_library_file_id FROM acquisition_targets
		WHERE musicbrainz_recording_id = $1
	`, recordingID).Scan(&upgradeOf); err != nil {
		t.Fatal(err)
	}
	if !upgradeOf.Valid || upgradeOf.UUID != fileID {
		t.Fatalf("upgrade_of_library_file_id = %v, want %v", upgradeOf, fileID)
	}
}
