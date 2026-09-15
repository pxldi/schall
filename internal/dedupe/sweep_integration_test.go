package dedupe

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/library"
	"github.com/rs/zerolog"
)

func duplicateLibraryRoot(t *testing.T, pool *pgxpool.Pool) (uuid.UUID, string) {
	t.Helper()
	rootPath := t.TempDir()
	rootID := uuid.New()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO library_roots (id, path) VALUES ($1, $2)`, rootID, rootPath); err != nil {
		t.Fatal(err)
	}
	return rootID, rootPath
}

func duplicateLibraryFile(
	t *testing.T, pool *pgxpool.Pool, rootID uuid.UUID, path string,
) uuid.UUID {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("broken fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	var fileID uuid.UUID
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO library_files (
			library_root_id, path, size_bytes, modified_at,
			artist_tag, album_tag, title_tag,
			duration_ms, bit_rate_kbps, sample_rate_hz, channels,
			measured_duration_ms
		)
		VALUES ($1, $2, 14, now(), 'Portishead', 'Dummy', 'Glory Box',
			180000, 320, 44100, 2, 180000)
		RETURNING id
	`, rootID, path).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	return fileID
}

func identifyDuplicateFile(t *testing.T, pool *pgxpool.Pool, fileID, recordingID uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, method, summary
		) VALUES ($1, 'external', $2, 'acoustid', 'the audio is this recording')
	`, fileID, recordingID); err != nil {
		t.Fatal(err)
	}
}

// An unattended pass records its own actor in each removal licence.
func TestAnUnattendedDuplicateSweepRecordsTheSweepAsRemover(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO duplicate_resolution_settings (singleton, enabled)
		VALUES (true, true)
	`); err != nil {
		t.Fatal(err)
	}
	rootID, rootPath := duplicateLibraryRoot(t, pool)
	recordingID := uuid.New()
	keeperID := duplicateLibraryFile(t, pool, rootID, filepath.Join(rootPath, "keeper.flac"))
	spareID := duplicateLibraryFile(t, pool, rootID, filepath.Join(rootPath, "spare.flac"))
	identifyDuplicateFile(t, pool, keeperID, recordingID)
	identifyDuplicateFile(t, pool, spareID, recordingID)

	result, err := NewDuplicateSweep(
		library.NewRemover(pool, zerolog.Nop()), pool,
	).SweepDuplicates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Removed != 1 {
		t.Fatalf("removed = %d, want 1", result.Removed)
	}

	var removedBy string
	if err := pool.QueryRow(ctx,
		`SELECT removed_by FROM library_file_removals`,
	).Scan(&removedBy); err != nil {
		t.Fatal(err)
	}
	if removedBy != duplicateSweepRemovalActor {
		t.Fatalf("removed_by = %q, want %q", removedBy, duplicateSweepRemovalActor)
	}
	if removedBy == library.DuplicateScreenRemovalActor {
		t.Fatal("the unattended sweep recorded the duplicates screen actor")
	}
}
