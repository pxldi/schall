package library

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/rs/zerolog"
)

// upgradeableFile is libraryFile with a bit rate on it, which is what makes a
// file eligible to be one side of ReplaceIfBetter's comparison.
func upgradeableFile(
	t *testing.T, pool *pgxpool.Pool, rootID uuid.UUID, path string, bitRateKbps int,
) uuid.UUID {
	t.Helper()
	fileID := libraryFile(t, pool, rootID, path, "An Artist", "An Album")
	if _, err := pool.Exec(context.Background(), `
		UPDATE library_files SET bit_rate_kbps = $2, resolution_status = 'resolved'
		WHERE id = $1
	`, fileID, bitRateKbps); err != nil {
		t.Fatal(err)
	}
	return fileID
}

func switchOnUpgrades(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO upgrade_settings (singleton, enabled) VALUES (true, true)
		ON CONFLICT (singleton) DO UPDATE SET enabled = true
	`); err != nil {
		t.Fatal(err)
	}
}

func switchOffUpgrades(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO upgrade_settings (singleton, enabled) VALUES (true, false)
		ON CONFLICT (singleton) DO UPDATE SET enabled = false
	`); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceIfBetterDeletesTheOldFileWhenTheNewCopyIsBetter(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	switchOnUpgrades(t, pool)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()

	oldPath := filepath.Join(rootPath, "old.mp3")
	newPath := filepath.Join(rootPath, "new.mp3")
	oldID := upgradeableFile(t, pool, rootID, oldPath, 128)
	newID := upgradeableFile(t, pool, rootID, newPath, 320)
	identifiedAsRecording(t, pool, oldID, recordingID)
	identifiedAsRecording(t, pool, newID, recordingID)

	replaced, err := NewRemover(pool, zerolog.Nop()).ReplaceIfBetter(ctx, oldID, newID)
	if err != nil {
		t.Fatal(err)
	}
	if !replaced {
		t.Fatal("a genuinely better copy did not replace the old file")
	}
	if filePresent(t, pool, oldID) {
		t.Fatal("the old file still has a library row")
	}
	if !filePresent(t, pool, newID) {
		t.Fatal("the new file was deleted")
	}
	if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the old audio is still on disc: %v", err)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("the new audio is gone: %v", err)
	}

	written := removals(t, pool)
	if len(written) != 1 {
		t.Fatalf("removal rows = %d, want 1", len(written))
	}
	if written[0].path != oldPath {
		t.Fatalf("removal path = %s, want %s", written[0].path, oldPath)
	}
	if !written[0].keptFileID.Valid || written[0].keptFileID.UUID != newID {
		t.Fatalf("removal kept file = %v, want %v", written[0].keptFileID, newID)
	}
	if !written[0].unlinkedAt.Valid {
		t.Fatal("the removal does not say the old audio left the disc")
	}
}

// The floor already refuses a worse or equal copy for an ordinary want, so
// this is a belt-and-braces check on the one place that deletes anything:
// even handed a copy that should never have arrived, ReplaceIfBetter refuses
// to call it an upgrade — and the copy that was not an upgrade is discarded
// rather than left as a second file, since the want wanted an upgrade and not
// a duplicate.
func TestReplaceIfBetterDiscardsTheNewCopyWhenItIsNotBetter(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	switchOnUpgrades(t, pool)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()

	oldPath := filepath.Join(rootPath, "old.mp3")
	newPath := filepath.Join(rootPath, "new.mp3")
	oldID := upgradeableFile(t, pool, rootID, oldPath, 320)
	newID := upgradeableFile(t, pool, rootID, newPath, 128)
	identifiedAsRecording(t, pool, oldID, recordingID)
	identifiedAsRecording(t, pool, newID, recordingID)

	replaced, err := NewRemover(pool, zerolog.Nop()).ReplaceIfBetter(ctx, oldID, newID)
	if err != nil {
		t.Fatal(err)
	}
	if replaced {
		t.Fatal("a worse copy was reported as having replaced the old file")
	}
	if !filePresent(t, pool, oldID) {
		t.Fatal("the old file was deleted even though the new copy was not better")
	}
	if filePresent(t, pool, newID) {
		t.Fatal("the copy that was not an upgrade was kept as a second file")
	}
	if _, err := os.Stat(newPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the discarded audio is still on disc: %v", err)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("the old audio is gone: %v", err)
	}

	written := removals(t, pool)
	if len(written) != 1 {
		t.Fatalf("removal rows = %d, want 1", len(written))
	}
	if written[0].path != newPath {
		t.Fatalf("removal path = %s, want %s", written[0].path, newPath)
	}
	if !written[0].keptFileID.Valid || written[0].keptFileID.UUID != oldID {
		t.Fatalf("removal kept file = %v, want %v", written[0].keptFileID, oldID)
	}

	var refusedAt sql.NullTime
	if err := pool.QueryRow(ctx,
		`SELECT upgrade_refused_at FROM library_files WHERE id = $1`, oldID,
	).Scan(&refusedAt); err != nil {
		t.Fatal(err)
	}
	if !refusedAt.Valid {
		t.Fatal("the old file was not marked as having a copy refused for it")
	}
}

// The HIGH-severity gap this closes: a library row can say a file exists
// while its audio is actually gone from disc. Licensing the old file's
// deletion, or the new copy's discard, on the strength of a copy that turns
// out to be missing would destroy music for nothing.
func TestReplaceIfBetterDeletesNothingWhenTheBetterCopyIsNotReadable(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	switchOnUpgrades(t, pool)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()

	oldPath := filepath.Join(rootPath, "old.mp3")
	newPath := filepath.Join(rootPath, "new.mp3")
	oldID := upgradeableFile(t, pool, rootID, oldPath, 128)
	newID := upgradeableFile(t, pool, rootID, newPath, 320)
	identifiedAsRecording(t, pool, oldID, recordingID)
	identifiedAsRecording(t, pool, newID, recordingID)

	// The row exists and says the copy is better; the bytes behind it do not.
	if err := os.Remove(newPath); err != nil {
		t.Fatal(err)
	}

	replaced, err := NewRemover(pool, zerolog.Nop()).ReplaceIfBetter(ctx, oldID, newID)
	if err != nil {
		t.Fatal(err)
	}
	if replaced {
		t.Fatal("a copy that is not readable on disk was reported as a replacement")
	}
	if !filePresent(t, pool, oldID) {
		t.Fatal("the old file was deleted on the strength of an unreadable copy")
	}
	if !filePresent(t, pool, newID) {
		t.Fatal("the missing copy's own row was deleted")
	}
	if len(removals(t, pool)) != 0 {
		t.Fatal("a licence row was written for a copy that was never readable")
	}
}

// The same guarantee, on the other side of the comparison: a not-better copy
// must not be discarded on the strength of an old file the row says is there
// but whose audio has actually gone missing underneath it.
func TestReplaceIfBetterDiscardsNothingWhenTheOldFileIsNotReadable(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	switchOnUpgrades(t, pool)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()

	oldPath := filepath.Join(rootPath, "old.mp3")
	newPath := filepath.Join(rootPath, "new.mp3")
	oldID := upgradeableFile(t, pool, rootID, oldPath, 320)
	newID := upgradeableFile(t, pool, rootID, newPath, 128)
	identifiedAsRecording(t, pool, oldID, recordingID)
	identifiedAsRecording(t, pool, newID, recordingID)

	if err := os.Remove(oldPath); err != nil {
		t.Fatal(err)
	}

	replaced, err := NewRemover(pool, zerolog.Nop()).ReplaceIfBetter(ctx, oldID, newID)
	if err != nil {
		t.Fatal(err)
	}
	if replaced {
		t.Fatal("a discard was reported as a replacement")
	}
	if !filePresent(t, pool, newID) {
		t.Fatal("the new file was discarded on the strength of an unreadable old file")
	}
	if len(removals(t, pool)) != 0 {
		t.Fatal("a licence row was written even though the surviving file was not readable")
	}
}

func TestReplaceIfBetterDoesNothingWhenSwitchedOff(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()

	oldID := upgradeableFile(t, pool, rootID, filepath.Join(rootPath, "old.mp3"), 128)
	newID := upgradeableFile(t, pool, rootID, filepath.Join(rootPath, "new.mp3"), 320)
	identifiedAsRecording(t, pool, oldID, recordingID)
	identifiedAsRecording(t, pool, newID, recordingID)

	replaced, err := NewRemover(pool, zerolog.Nop()).ReplaceIfBetter(ctx, oldID, newID)
	if err != nil {
		t.Fatal(err)
	}
	if replaced {
		t.Fatal("a replacement happened while the switch was off")
	}
	if !filePresent(t, pool, oldID) {
		t.Fatal("a file was deleted while the switch was off")
	}
}

// The licence row is what a deletion needs before the unlink even when the
// unlink then fails — the same guarantee keepone.go's
// TestACopyThatCannotBeDeletedLeavesTheOthersDeletedAndOnRecord pins for the
// duplicates screen.
func TestReplaceIfBetterRecordsTheLicenceEvenWhenTheUnlinkFails(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	switchOnUpgrades(t, pool)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()

	stuckDir := filepath.Join(rootPath, "locked")
	oldPath := filepath.Join(stuckDir, "old.mp3")
	newPath := filepath.Join(rootPath, "new.mp3")
	oldID := upgradeableFile(t, pool, rootID, oldPath, 128)
	newID := upgradeableFile(t, pool, rootID, newPath, 320)
	identifiedAsRecording(t, pool, oldID, recordingID)
	identifiedAsRecording(t, pool, newID, recordingID)

	if err := os.Chmod(stuckDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stuckDir, 0o755) })

	replaced, err := NewRemover(pool, zerolog.Nop()).ReplaceIfBetter(ctx, oldID, newID)
	if err != nil {
		t.Fatal(err)
	}
	if !replaced {
		t.Fatal("the replacement did not happen")
	}
	if filePresent(t, pool, oldID) {
		t.Fatal("the old file still has a library row")
	}
	written := removals(t, pool)
	if len(written) != 1 {
		t.Fatalf("removal rows = %d, want 1", len(written))
	}
	if written[0].unlinkedAt.Valid {
		t.Fatal("the removal says the audio left a disc it could not be removed from")
	}
	if written[0].unlinkError == "" {
		t.Fatal("the removal does not say why the audio is still on disc")
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("the stranded audio is gone from disc: %v", err)
	}
}

// A playlist entry that already resolved to the old file follows it to the
// new one, so the reference the playlist carries survives the replacement
// instead of quietly reading "not yet obtained".
func TestReplaceIfBetterCarriesAPlaylistReferenceToTheNewFile(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	switchOnUpgrades(t, pool)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()

	oldID := upgradeableFile(t, pool, rootID, filepath.Join(rootPath, "old.mp3"), 128)
	newID := upgradeableFile(t, pool, rootID, filepath.Join(rootPath, "new.mp3"), 320)
	identifiedAsRecording(t, pool, oldID, recordingID)
	identifiedAsRecording(t, pool, newID, recordingID)

	var playlistID, entryID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO playlists (source, name) VALUES ('manual', 'A playlist') RETURNING id
	`).Scan(&playlistID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO playlist_entries (
			playlist_id, position, entry_title, owned_library_file_id
		) VALUES ($1, 1, 'A song', $2)
		RETURNING id
	`, playlistID, oldID).Scan(&entryID); err != nil {
		t.Fatal(err)
	}

	replaced, err := NewRemover(pool, zerolog.Nop()).ReplaceIfBetter(ctx, oldID, newID)
	if err != nil {
		t.Fatal(err)
	}
	if !replaced {
		t.Fatal("the replacement did not happen")
	}

	var owned uuid.NullUUID
	if err := pool.QueryRow(ctx,
		`SELECT owned_library_file_id FROM playlist_entries WHERE id = $1`, entryID,
	).Scan(&owned); err != nil {
		t.Fatal(err)
	}
	if !owned.Valid || owned.UUID != newID {
		t.Fatalf("playlist entry owned file = %v, want %v", owned, newID)
	}
}

func TestReplaceOwnedWithUploadDeletesAWorseLossyCopy(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()

	oldPath := filepath.Join(rootPath, "held.mp3")
	uploadedPath := filepath.Join(rootPath, "uploaded.flac")
	oldID := upgradeableFile(t, pool, rootID, oldPath, 320)
	uploadedID := upgradeableFile(t, pool, rootID, uploadedPath, 0)
	identifiedAsRecording(t, pool, oldID, recordingID)
	identifiedAsRecording(t, pool, uploadedID, recordingID)

	replaced, err := NewRemover(pool, zerolog.Nop()).ReplaceOwnedWithUpload(ctx, uploadedID)
	if err != nil {
		t.Fatal(err)
	}
	if !replaced {
		t.Fatal("a lossless upload did not replace the held MP3")
	}
	if filePresent(t, pool, oldID) || !filePresent(t, pool, uploadedID) {
		t.Fatal("the replacement left the wrong library rows")
	}
	if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the old audio is still on disc: %v", err)
	}
	if _, err := os.Stat(uploadedPath); err != nil {
		t.Fatalf("the uploaded audio is gone: %v", err)
	}

	written := removals(t, pool)
	if len(written) != 1 {
		t.Fatalf("removal rows = %d, want 1", len(written))
	}
	if written[0].removedBy != uploadRemovedBy || written[0].reason != uploadReplacedReason {
		t.Fatalf("removal licence = %q, %q", written[0].removedBy, written[0].reason)
	}
}

func TestReplaceOwnedWithUploadKeepsAWorseCopy(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()

	oldID := upgradeableFile(t, pool, rootID, filepath.Join(rootPath, "held.mp3"), 320)
	uploadedID := upgradeableFile(t, pool, rootID, filepath.Join(rootPath, "uploaded.mp3"), 128)
	identifiedAsRecording(t, pool, oldID, recordingID)
	identifiedAsRecording(t, pool, uploadedID, recordingID)

	replaced, err := NewRemover(pool, zerolog.Nop()).ReplaceOwnedWithUpload(ctx, uploadedID)
	if err != nil {
		t.Fatal(err)
	}
	if replaced || !filePresent(t, pool, oldID) || !filePresent(t, pool, uploadedID) {
		t.Fatal("a worse upload was deleted or reported as a replacement")
	}
	if len(removals(t, pool)) != 0 {
		t.Fatal("a worse upload wrote a removal licence")
	}

	var refusedAt sql.NullTime
	if err := pool.QueryRow(ctx,
		`SELECT upgrade_refused_at FROM library_files WHERE id = $1`, oldID,
	).Scan(&refusedAt); err != nil {
		t.Fatal(err)
	}
	if refusedAt.Valid {
		t.Fatal("an upload set upgrade_refused_at on the held copy")
	}
}

func TestReplaceOwnedWithUploadLeavesAnAlreadyAmbiguousGroupAlone(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()

	firstID := upgradeableFile(t, pool, rootID, filepath.Join(rootPath, "held-one.mp3"), 128)
	secondID := upgradeableFile(t, pool, rootID, filepath.Join(rootPath, "held-two.mp3"), 320)
	uploadedID := upgradeableFile(t, pool, rootID, filepath.Join(rootPath, "uploaded.flac"), 0)
	for _, fileID := range []uuid.UUID{firstID, secondID, uploadedID} {
		identifiedAsRecording(t, pool, fileID, recordingID)
	}

	replaced, err := NewRemover(pool, zerolog.Nop()).ReplaceOwnedWithUpload(ctx, uploadedID)
	if err != nil {
		t.Fatal(err)
	}
	if replaced || !filePresent(t, pool, firstID) || !filePresent(t, pool, secondID) ||
		!filePresent(t, pool, uploadedID) {
		t.Fatal("an upload answered a recording held by more than one other copy")
	}
	if len(removals(t, pool)) != 0 {
		t.Fatal("an ambiguous upload wrote a removal licence")
	}
}

func TestReplaceOwnedWithUploadIgnoresUpgradeSetting(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	switchOffUpgrades(t, pool)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()

	oldID := upgradeableFile(t, pool, rootID, filepath.Join(rootPath, "held.mp3"), 128)
	uploadedID := upgradeableFile(t, pool, rootID, filepath.Join(rootPath, "uploaded.flac"), 0)
	identifiedAsRecording(t, pool, oldID, recordingID)
	identifiedAsRecording(t, pool, uploadedID, recordingID)

	replaced, err := NewRemover(pool, zerolog.Nop()).ReplaceOwnedWithUpload(ctx, uploadedID)
	if err != nil {
		t.Fatal(err)
	}
	if !replaced || filePresent(t, pool, oldID) || !filePresent(t, pool, uploadedID) {
		t.Fatal("the upgrade setting stopped a better upload from replacing the old copy")
	}
}
