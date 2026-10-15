package acquisition

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/library"
	"github.com/pxldi/schall/internal/sources"
	"github.com/rs/zerolog"
)

// upgradeFile lays down a library file on disc with an identity naming the
// recording. A damaged file's audio did not decode.
func upgradeFile(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool,
	rootID uuid.UUID, path string, bitRateKbps int, damaged bool, recordingID uuid.UUID,
) uuid.UUID {
	t.Helper()
	if err := os.WriteFile(path, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	decodeError := pgtype.Text{}
	if damaged {
		decodeError = pgtype.Text{String: "Header missing", Valid: true}
	}
	var fileID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO library_files (
			library_root_id, path, size_bytes, modified_at, bit_rate_kbps,
			resolution_status, decode_error
		)
		VALUES ($1, $2, 5, now(), $3, 'resolved', $4)
		RETURNING id
	`, rootID, path, bitRateKbps, decodeError).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, method, summary
		) VALUES ($1, 'external', $2, 'fingerprint', 'the audio is this recording')
	`, fileID, recordingID); err != nil {
		t.Fatal(err)
	}
	return fileID
}

func upgradeRoot(ctx context.Context, t *testing.T, pool *pgxpool.Pool) (uuid.UUID, string) {
	t.Helper()
	rootPath := t.TempDir()
	var rootID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO library_roots (path) VALUES ($1) RETURNING id`, rootPath,
	).Scan(&rootID); err != nil {
		t.Fatal(err)
	}
	return rootID, rootPath
}

func upgradeWant(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, fileID, recordingID uuid.UUID,
) uuid.UUID {
	t.Helper()
	id, _, err := db.New(pool).CreateUpgradeTarget(ctx, db.CreateUpgradeTargetParams{
		LibraryFileID: fileID, RecordingID: recordingID,
		EntryArtist: "Hum", EntryTitle: "Scraper",
		EntryDurationMS: pgtype.Int4{Int32: 301_000, Valid: true},
		Summary:         "Your copy's audio is damaged.",
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// acceptedUpgradeCopy records the copy fetched for an upgrade want as proven,
// imported and scanned into the library file given.
func acceptedUpgradeCopy(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID, fileID uuid.UUID, path string,
) {
	t.Helper()
	requestID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO download_requests (
			id, acquisition_target_id, provider, source_username, source_directory,
			file_count, expected_track_count, total_size_bytes, format, score, files,
			status, import_status, import_path, imported_at
		)
		VALUES ($1, $2, 'slskd', 'peer', 'Music/Hum', 1, 1, 5,
		        'mp3', 0.8, '[]'::jsonb, 'completed', 'imported', '/music', now())
	`, requestID, targetID); err != nil {
		t.Fatalf("seed the request: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO downloads (
			download_request_id, provider, remote_path, source_username,
			status, local_path, library_file_id
		)
		VALUES ($1, 'slskd', 'Music\Hum\Scraper.mp3', 'peer', 'completed', $2, $3)
	`, requestID, path, fileID); err != nil {
		t.Fatalf("seed the transfer: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_target_files (
			acquisition_target_id, provider, source_username, remote_path, file_name,
			verdict, summary, evidence, download_request_id
		)
		VALUES ($1, 'slskd', 'peer', 'Music\Hum\Scraper.mp3', 'Scraper.mp3',
		        'accepted', 'Proven by its audio.', $2::jsonb, $3)
	`, targetID, `{"method":"fingerprint","confidence":1,"agrees":["audio"],
	   "wanted":{"artist":"Hum","title":"Scraper","durationMs":301000}}`,
		requestID); err != nil {
		t.Fatalf("seed the accepted copy: %v", err)
	}
}

func switchOnUpgrades(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO upgrade_settings (singleton, enabled) VALUES (true, true)
		ON CONFLICT (singleton) DO UPDATE SET enabled = true
	`); err != nil {
		t.Fatal(err)
	}
}

func scraperOffers() []sources.Candidate {
	return []sources.Candidate{
		{
			Provider: "slskd", Username: "lossy", Directory: `Music\Hum\MP3`, Score: 0.9,
			Files: []sources.File{{
				Path: `Music\Hum\MP3\Scraper.mp3`, Name: "Scraper.mp3", Extension: "mp3",
				SizeBytes: 7_000_000, DurationSeconds: 301, BitRate: 320,
			}},
		},
		{
			Provider: "slskd", Username: "lossless", Directory: `Music\Hum\FLAC`, Score: 0.5,
			Files: []sources.File{{
				Path: `Music\Hum\FLAC\Scraper.flac`, Name: "Scraper.flac", Extension: "flac",
				SizeBytes: 30_000_000, DurationSeconds: 301,
			}},
		},
	}
}

// An upgrade want for a damaged FLAC fetches the FLAC on offer, even when an
// MP3 ranks above it. ReplaceIfBetter would discard the MP3.
func TestAnUpgradeWantForADamagedFLACFetchesALosslessCopy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{candidates: scraperOffers()}
	fetcher := &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)
	rootID, rootPath := upgradeRoot(ctx, t, pool)
	recordingID := uuid.New()
	flacID := upgradeFile(ctx, t, pool, rootID, filepath.Join(rootPath, "Scraper.flac"), 900, true, recordingID)
	targetID := upgradeWant(ctx, t, pool, flacID, recordingID)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	recorded := offers(ctx, t, pool, targetID)
	if len(recorded) != 1 || recorded[0].FileName != "Scraper.flac" {
		t.Fatalf("offers = %+v, want the FLAC fetched", recorded)
	}
}

// When only lossy copies are on offer, the want fetches nothing and says why.
func TestAnUpgradeWantForADamagedFLACRefusesALossyCopy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{candidates: scraperOffers()[:1]}
	fetcher := &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)
	rootID, rootPath := upgradeRoot(ctx, t, pool)
	recordingID := uuid.New()
	flacID := upgradeFile(ctx, t, pool, rootID, filepath.Join(rootPath, "Scraper.flac"), 900, true, recordingID)
	targetID := upgradeWant(ctx, t, pool, flacID, recordingID)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if len(fetcher.started) != 0 {
		t.Fatalf("started %d transfers, want no lossy copy fetched", len(fetcher.started))
	}
	waiting, err := service.Target(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Summary != onlyLossySummary {
		t.Fatalf("summary = %q, want %q", waiting.Summary, onlyLossySummary)
	}
}

// An upgrade want for an undamaged MP3 below the floor still takes an MP3.
func TestAnUpgradeWantForAnMP3BelowTheFloorTakesALossyCopy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	hunter := &fakeHunter{candidates: scraperOffers()[:1]}
	fetcher := &fakeFetcher{}
	service := hunting(pool, hunter, fetcher, true)
	rootID, rootPath := upgradeRoot(ctx, t, pool)
	recordingID := uuid.New()
	mp3ID := upgradeFile(ctx, t, pool, rootID, filepath.Join(rootPath, "Scraper.mp3"), 128, false, recordingID)
	targetID := upgradeWant(ctx, t, pool, mp3ID, recordingID)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	recorded := offers(ctx, t, pool, targetID)
	if len(recorded) != 1 || recorded[0].FileName != "Scraper.mp3" {
		t.Fatalf("offers = %+v, want the MP3 fetched", recorded)
	}
}

// Live on 2026-10-15: an MP3 reached a damaged FLAC's want, was discarded as
// no better, and the want still ended acquired with "Downloaded, audio
// verified, imported." It now hands over to a fresh want that keeps looking.
func TestALossyCopyDiscardedForADamagedFLACLeavesItsWantLooking(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	switchOnUpgrades(ctx, t, pool)
	service := newTestService(pool).WithUpgrader(library.NewRemover(pool, zerolog.Nop()))
	rootID, rootPath := upgradeRoot(ctx, t, pool)
	recordingID := uuid.New()
	flacID := upgradeFile(ctx, t, pool, rootID, filepath.Join(rootPath, "Scraper.flac"), 900, true, recordingID)
	targetID := upgradeWant(ctx, t, pool, flacID, recordingID)
	mp3Path := filepath.Join(rootPath, "Scraper.mp3")
	mp3ID := upgradeFile(ctx, t, pool, rootID, mp3Path, 320, false, recordingID)
	acceptedUpgradeCopy(ctx, t, pool, targetID, mp3ID, mp3Path)
	target, err := service.Target(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.completeAcquired(ctx, target); err != nil {
		t.Fatalf("completeAcquired() error = %v", err)
	}

	var status, summary string
	var upgradeOf uuid.NullUUID
	if err := pool.QueryRow(ctx, `
		SELECT status, summary, upgrade_of_library_file_id FROM acquisition_targets
		WHERE musicbrainz_recording_id = $1 AND status <> 'superseded'
	`, recordingID).Scan(&status, &summary, &upgradeOf); err != nil {
		t.Fatal(err)
	}
	if status == "acquired" || summary != onlyLossySummary || upgradeOf.UUID != flacID {
		t.Fatalf("live want = %s %q for %v; want one still looking for a lossless copy of %v",
			status, summary, upgradeOf, flacID)
	}
}

// A worse copy discarded for an undamaged MP3 leaves its want acquired, as
// before. The MP3 stands back from the sweep instead.
func TestAWorseCopyDiscardedForAnMP3LeavesItsWantAcquired(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	switchOnUpgrades(ctx, t, pool)
	service := newTestService(pool).WithUpgrader(library.NewRemover(pool, zerolog.Nop()))
	rootID, rootPath := upgradeRoot(ctx, t, pool)
	recordingID := uuid.New()
	oldID := upgradeFile(ctx, t, pool, rootID, filepath.Join(rootPath, "old.mp3"), 192, false, recordingID)
	targetID := upgradeWant(ctx, t, pool, oldID, recordingID)
	newPath := filepath.Join(rootPath, "new.mp3")
	newID := upgradeFile(ctx, t, pool, rootID, newPath, 128, false, recordingID)
	acceptedUpgradeCopy(ctx, t, pool, targetID, newID, newPath)
	target, err := service.Target(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.completeAcquired(ctx, target); err != nil {
		t.Fatalf("completeAcquired() error = %v", err)
	}

	settled, err := service.Target(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.Status != "acquired" {
		t.Fatalf("status = %s, want acquired", settled.Status)
	}
}
