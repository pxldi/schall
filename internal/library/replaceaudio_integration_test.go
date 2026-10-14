package library

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/chromaprint"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/rs/zerolog"
)

// printsFrom stands in for ffmpeg and fpcalc: each path prints the raw frames
// of one fixture in internal/chromaprint/testdata, or of the file an absolute
// path names. A path with no fixture
// prints nothing, as fpcalc does for audio it cannot read.
func printsFrom(t *testing.T, fixtures map[string]string) *chromaprint.Fingerprinter {
	t.Helper()
	return chromaprint.NewFingerprinter(chromaprint.Options{
		Pipe: func(_ context.Context, _, _, file string, _ int) ([]byte, error) {
			fixture, found := fixtures[file]
			if !found {
				return nil, errors.New("exit status 3")
			}
			if filepath.IsAbs(fixture) {
				return os.ReadFile(fixture)
			}
			return os.ReadFile(filepath.Join("..", "chromaprint", "testdata", fixture+".json"))
		},
	})
}

// damagedTaggedFile is a damaged file of three minutes whose identity rests
// on its tags alone.
func damagedTaggedFile(
	t *testing.T, pool *pgxpool.Pool, rootID uuid.UUID, path string, recordingID uuid.UUID,
) uuid.UUID {
	t.Helper()
	fileID := upgradeableFile(t, pool, rootID, path, 128)
	if _, err := pool.Exec(context.Background(), `
		UPDATE library_files
		SET decode_error = 'Header missing', decode_checked_at = now(), duration_ms = 180000
		WHERE id = $1
	`, fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, method, summary
		) VALUES ($1, 'external', $2, 'title-artist-duration', 'the tags name this recording')
	`, fileID, recordingID); err != nil {
		t.Fatal(err)
	}
	return fileID
}

// provenCopy is a three-minute 320 kbps copy proven by its audio.
func provenCopy(
	t *testing.T, pool *pgxpool.Pool, rootID uuid.UUID, path string, recordingID uuid.UUID,
) uuid.UUID {
	t.Helper()
	fileID := upgradeableFile(t, pool, rootID, path, 320)
	if _, err := pool.Exec(context.Background(),
		`UPDATE library_files SET duration_ms = 180000 WHERE id = $1`, fileID); err != nil {
		t.Fatal(err)
	}
	identifiedAsRecording(t, pool, fileID, recordingID)
	return fileID
}

func refusedAt(t *testing.T, pool *pgxpool.Pool, fileID uuid.UUID) bool {
	t.Helper()
	var refused bool
	if err := pool.QueryRow(context.Background(),
		`SELECT upgrade_refused_at IS NOT NULL FROM library_files WHERE id = $1`, fileID,
	).Scan(&refused); err != nil {
		t.Fatal(err)
	}
	return refused
}

func TestReplaceIfBetterReplacesADamagedTaggedFileWhoseAudioAgrees(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	switchOnUpgrades(t, pool)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()
	oldPath := filepath.Join(rootPath, "old.mp3")
	newPath := filepath.Join(rootPath, "new.mp3")
	oldID := damagedTaggedFile(t, pool, rootID, oldPath, recordingID)
	newID := provenCopy(t, pool, rootID, newPath, recordingID)
	prints := printsFrom(t, map[string]string{oldPath: "reference-full", newPath: "reference-full"})

	replaced, err := NewRemover(pool, zerolog.Nop()).WithFingerprinter(prints).
		ReplaceIfBetter(ctx, oldID, newID)
	if err != nil {
		t.Fatal(err)
	}
	if !replaced || filePresent(t, pool, oldID) {
		t.Fatal("a damaged file whose audio agrees with the new copy was not replaced")
	}
	written := removals(t, pool)
	if len(written) != 1 || written[0].reason != upgradeAgreedReason {
		t.Fatalf("removals = %+v; want one, saying the audio matched", written)
	}
}

func TestReplaceIfBetterKeepsADamagedTaggedFileWhoseAudioDisagrees(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	switchOnUpgrades(t, pool)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()
	oldPath := filepath.Join(rootPath, "old.mp3")
	newPath := filepath.Join(rootPath, "new.mp3")
	oldID := damagedTaggedFile(t, pool, rootID, oldPath, recordingID)
	newID := provenCopy(t, pool, rootID, newPath, recordingID)
	prints := printsFrom(t, map[string]string{oldPath: "reference-full", newPath: "different-tail-full"})

	replaced, err := NewRemover(pool, zerolog.Nop()).WithFingerprinter(prints).
		ReplaceIfBetter(ctx, oldID, newID)
	if err != nil {
		t.Fatal(err)
	}
	assertBothKept(t, pool, replaced, oldID, newID, oldPath, newPath)
}

func TestReplaceIfBetterKeepsADamagedTaggedFileThatCannotBeFingerprinted(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	switchOnUpgrades(t, pool)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()
	oldPath := filepath.Join(rootPath, "old.mp3")
	newPath := filepath.Join(rootPath, "new.mp3")
	oldID := damagedTaggedFile(t, pool, rootID, oldPath, recordingID)
	newID := provenCopy(t, pool, rootID, newPath, recordingID)
	prints := printsFrom(t, map[string]string{newPath: "reference-full"})

	replaced, err := NewRemover(pool, zerolog.Nop()).WithFingerprinter(prints).
		ReplaceIfBetter(ctx, oldID, newID)
	if err != nil {
		t.Fatal(err)
	}
	assertBothKept(t, pool, replaced, oldID, newID, oldPath, newPath)
}

// A fingerprint that covers only the opening of the damaged file proves
// nothing about the rest, so a prefix that agrees is not agreement.
func TestReplaceIfBetterKeepsADamagedTaggedFileFingerprintedOnlyToTheDamage(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	switchOnUpgrades(t, pool)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()
	oldPath := filepath.Join(rootPath, "old.mp3")
	newPath := filepath.Join(rootPath, "new.mp3")
	oldID := damagedTaggedFile(t, pool, rootID, oldPath, recordingID)
	newID := provenCopy(t, pool, rootID, newPath, recordingID)
	prints := printsFrom(t, map[string]string{oldPath: "reference-prefix", newPath: "reference-full"})

	replaced, err := NewRemover(pool, zerolog.Nop()).WithFingerprinter(prints).
		ReplaceIfBetter(ctx, oldID, newID)
	if err != nil {
		t.Fatal(err)
	}
	assertBothKept(t, pool, replaced, oldID, newID, oldPath, newPath)
}

// A damaged file that decodes to twice the new copy's length holds other audio
// past the new copy's end, whatever its tags say about its length.
func TestReplaceIfBetterKeepsADamagedTaggedFileThatDecodesLongerThanTheNewCopy(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	switchOnUpgrades(t, pool)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()
	oldPath := filepath.Join(rootPath, "old.mp3")
	newPath := filepath.Join(rootPath, "new.mp3")
	oldID := damagedTaggedFile(t, pool, rootID, oldPath, recordingID)
	newID := provenCopy(t, pool, rootID, newPath, recordingID)
	prints := printsFrom(t, map[string]string{oldPath: twice(t, "reference-full"), newPath: "reference-full"})

	replaced, err := NewRemover(pool, zerolog.Nop()).WithFingerprinter(prints).
		ReplaceIfBetter(ctx, oldID, newID)
	if err != nil {
		t.Fatal(err)
	}
	assertBothKept(t, pool, replaced, oldID, newID, oldPath, newPath)
}

// twice writes a fixture's frames played two times over and returns its path.
func twice(t *testing.T, fixture string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "chromaprint", "testdata", fixture+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var printed struct {
		Fingerprint []uint32 `json:"fingerprint"`
	}
	if err := json.Unmarshal(raw, &printed); err != nil {
		t.Fatal(err)
	}
	printed.Fingerprint = append(printed.Fingerprint, printed.Fingerprint...)
	doubled, err := json.Marshal(printed)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "twice.json")
	if err := os.WriteFile(path, doubled, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReplaceIfBetterKeepsADamagedTaggedFileWithNoFingerprinter(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	switchOnUpgrades(t, pool)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()
	oldPath := filepath.Join(rootPath, "old.mp3")
	newPath := filepath.Join(rootPath, "new.mp3")
	oldID := damagedTaggedFile(t, pool, rootID, oldPath, recordingID)
	newID := provenCopy(t, pool, rootID, newPath, recordingID)

	replaced, err := NewRemover(pool, zerolog.Nop()).ReplaceIfBetter(ctx, oldID, newID)
	if err != nil {
		t.Fatal(err)
	}
	assertBothKept(t, pool, replaced, oldID, newID, oldPath, newPath)
}

// A damaged file with a settled identity is replaced as before, without its
// audio being read.
func TestReplaceIfBetterReplacesASettledDamagedFileWithoutReadingItsAudio(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	switchOnUpgrades(t, pool)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()
	oldPath := filepath.Join(rootPath, "old.mp3")
	newPath := filepath.Join(rootPath, "new.mp3")
	oldID := provenCopy(t, pool, rootID, oldPath, recordingID)
	if _, err := pool.Exec(ctx, `
		UPDATE library_files
		SET decode_error = 'Header missing', decode_checked_at = now(), bit_rate_kbps = 320
		WHERE id = $1
	`, oldID); err != nil {
		t.Fatal(err)
	}
	newID := provenCopy(t, pool, rootID, newPath, recordingID)

	replaced, err := NewRemover(pool, zerolog.Nop()).ReplaceIfBetter(ctx, oldID, newID)
	if err != nil {
		t.Fatal(err)
	}
	if !replaced || filePresent(t, pool, oldID) {
		t.Fatal("a settled damaged file was not replaced")
	}
	written := removals(t, pool)
	if len(written) != 1 || written[0].reason != upgradeReplacedReason {
		t.Fatalf("removals = %+v; want one with today's reason", written)
	}
}

// After a replacement whose audio did not agree, the pair is on the duplicates
// screen. Keeping one of each reads no audio, so it must leave the pair.
func TestKeepOneOfEachLeavesADamagedTaggedFileBesideItsReplacement(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootID, rootPath := libraryRoot(t, pool)
	recordingID := uuid.New()
	oldPath := filepath.Join(rootPath, "old.mp3")
	newPath := filepath.Join(rootPath, "new.mp3")
	oldID := damagedTaggedFile(t, pool, rootID, oldPath, recordingID)
	newID := provenCopy(t, pool, rootID, newPath, recordingID)
	if _, err := pool.Exec(ctx, `
		UPDATE library_files
		SET sample_rate_hz = 44100, channels = 2, measured_duration_ms = 180000
		WHERE id = ANY($1)
	`, []uuid.UUID{oldID, newID}); err != nil {
		t.Fatal(err)
	}

	swept, err := NewRemover(pool, zerolog.Nop()).KeepOneOfEach(ctx, "a test")
	if err != nil {
		t.Fatal(err)
	}
	if swept.Resolved != 0 || swept.LeftAlone != 1 {
		t.Fatalf("swept = %+v; want the pair left alone", swept)
	}
	if !filePresent(t, pool, oldID) || !filePresent(t, pool, newID) {
		t.Fatal("a copy of the pair was deleted")
	}
	if len(removals(t, pool)) != 0 {
		t.Fatal("a removal was recorded")
	}
}

func assertBothKept(
	t *testing.T, pool *pgxpool.Pool, replaced bool, oldID, newID uuid.UUID, oldPath, newPath string,
) {
	t.Helper()
	if replaced {
		t.Fatal("the damaged file was reported replaced")
	}
	if !filePresent(t, pool, oldID) || !filePresent(t, pool, newID) {
		t.Fatal("a library row was deleted")
	}
	for _, path := range []string{oldPath, newPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s is gone: %v", path, err)
		}
	}
	if len(removals(t, pool)) != 0 {
		t.Fatal("a removal was recorded")
	}
	if !refusedAt(t, pool, oldID) {
		t.Fatal("the damaged file does not stand back from the sweep")
	}
}
