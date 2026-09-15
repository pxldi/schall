package library

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/filecopy"
	"github.com/pxldi/schall/internal/transcode"
)

// enabledPolicy is a transcoding policy that always answers "shrink lossless
// files to MP3", the way the settings page stores it once somebody turns the
// button on.
func enabledPolicy(context.Context) (transcode.Policy, error) {
	return transcode.Policy{
		Enabled: true, Target: transcode.TargetMP3, Bitrate: "320", When: transcode.WhenLossless,
	}, nil
}

func newTranscodeSweep(pool *pgxpool.Pool, binary string) *TranscodeSweep {
	return NewTranscodeSweep(pool, transcode.NewService(enabledPolicy, binary, zerolog.Nop()), zerolog.Nop())
}

// resolvedFile records a library file the way an import that verified a
// MusicBrainz recording would: present, resolved, with a recording id. It is
// what makes a file eligible for the sweep at all.
func resolvedFile(
	t *testing.T, pool *pgxpool.Pool, rootID uuid.UUID, path string, resolved bool,
) uuid.UUID {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	status := "pending"
	var recordingID *uuid.UUID
	if resolved {
		status = "resolved"
		id := uuid.New()
		recordingID = &id
	}
	var fileID uuid.UUID
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO library_files (
		    library_root_id, path, size_bytes, modified_at,
		    resolution_status, musicbrainz_recording_id
		)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id
	`, rootID, path, info.Size(), databaseTime(info.ModTime()), status, recordingID,
	).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	return fileID
}

func fileRow(t *testing.T, pool *pgxpool.Pool, fileID uuid.UUID) (
	path string, sizeBytes int64, fromFormat *string, checkedAt bool,
) {
	t.Helper()
	if err := pool.QueryRow(context.Background(), `
		SELECT path, size_bytes, transcoded_from_format, transcode_checked_at IS NOT NULL
		FROM library_files WHERE id = $1
	`, fileID).Scan(&path, &sizeBytes, &fromFormat, &checkedAt); err != nil {
		t.Fatal(err)
	}
	return path, sizeBytes, fromFormat, checkedAt
}

// TestTheSweepShrinksAResolvedFile is the pass's ordinary case: a present,
// proven file under a lossless-shrinking policy is re-encoded, filed in its
// row's place, and its original bytes are accounted for by a removal record
// before they are removed.
func TestTheSweepShrinksAResolvedFile(t *testing.T) {
	needsFfmpeg(t)
	pool := dbtest.Setup(t)
	rootID, root := libraryRoot(t, pool)

	resolvedPath := filepath.Join(root, "resolved.wav")
	sineFile(t, resolvedPath, 0.5)
	originalInfo, err := os.Stat(resolvedPath)
	if err != nil {
		t.Fatal(err)
	}
	resolvedID := resolvedFile(t, pool, rootID, resolvedPath, true)

	sweep := newTranscodeSweep(pool, "")
	job := queueTranscodeSweep(t, pool)
	if _, err := sweep.Sweep(context.Background(), job); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	// The resolved file was shrunk: its row now points at an MP3 that exists,
	// the WAV that arrived is gone, and a removal record accounts for it.
	path, size, fromFormat, _ := fileRow(t, pool, resolvedID)
	if filepath.Ext(path) != ".mp3" {
		t.Fatalf("resolved file's path is %q, wanted an MP3", path)
	}
	if fromFormat == nil || *fromFormat != "wav" {
		t.Fatalf("transcoded_from_format = %v, wanted wav", fromFormat)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the smaller copy is not on disc: %v", err)
	}
	if _, err := os.Stat(resolvedPath); !os.IsNotExist(err) {
		t.Fatalf("the original WAV is still on disc: %v", err)
	}
	var removals int
	var unlinked bool
	var reason string
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*), bool_and(unlinked_at IS NOT NULL), min(reason)
		FROM library_file_removals WHERE library_file_id = $1
	`, resolvedID).Scan(&removals, &unlinked, &reason); err != nil {
		t.Fatal(err)
	}
	if removals != 1 {
		t.Fatalf("removal records for the resolved file = %d, wanted 1", removals)
	}
	if !unlinked {
		t.Fatal("the removal record was never marked as unlinked")
	}
	// A removal record is the permanent record of why a piece of music was deleted, so
	// it has to read as a sentence. This one said "re-encoded to wav at mp3",
	// which names the two formats the wrong way round and means nothing.
	if reason != "re-encoded from wav to mp3 under the transcoding setting" {
		t.Fatalf("the removal record says %q", reason)
	}
	if size <= 0 || size >= originalInfo.Size() {
		t.Fatalf("the copy is %d bytes and the WAV was %d", size, originalInfo.Size())
	}
}

// TestTheSweepLeavesTheOriginalWhenTheWrittenCopyIsShort confirms that a
// readable copy can still be refused when Place left it shorter than the
// original. The row has no duration, so the sweep reads the original before
// deciding.
func TestTheSweepLeavesTheOriginalWhenTheWrittenCopyIsShort(t *testing.T) {
	needsFfmpeg(t)
	pool := dbtest.Setup(t)
	rootID, root := libraryRoot(t, pool)

	originalPath := filepath.Join(root, "short-copy.wav")
	longSineFile(t, originalPath)
	originalInfo, err := os.Stat(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	fileID := resolvedFile(t, pool, rootID, originalPath, true)

	sweep := newTranscodeSweep(pool, "")
	sweep.place = shortDestination(t)
	job := queueTranscodeSweep(t, pool)
	if _, err := sweep.Sweep(context.Background(), job); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	path, size, fromFormat, checked := fileRow(t, pool, fileID)
	if path != originalPath {
		t.Fatalf("the row points at %q, wanted the original", path)
	}
	if size != originalInfo.Size() {
		t.Fatalf("the row size is %d, wanted %d", size, originalInfo.Size())
	}
	if fromFormat != nil {
		t.Fatalf("the original was recorded as transcoded from %q", *fromFormat)
	}
	if !checked {
		t.Fatal("the short copy was not marked checked")
	}
	if _, err := os.Stat(originalPath); err != nil {
		t.Fatalf("the original was removed: %v", err)
	}
	if count := removalsFor(t, pool, fileID); count != 0 {
		t.Fatalf("a removal record exists for a refused short copy")
	}
}

func longSineFile(t *testing.T, path string) {
	t.Helper()
	command := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=1000:duration=3", "-y", path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("write the source file: %v: %s", err, output)
	}
}

func shortDestination(t *testing.T) func(string, string, string) error {
	t.Helper()
	return func(source, destination, stagingPrefix string) error {
		if err := filecopy.Place(source, destination, stagingPrefix); err != nil {
			return err
		}
		short := destination + ".short.mp3"
		command := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
			"-i", destination, "-t", "0.1", "-c", "copy", "-y", short)
		if output, err := command.CombinedOutput(); err != nil {
			return fmt.Errorf("make a short destination: %v: %s", err, output)
		}
		return os.Rename(short, destination)
	}
}

// TestTheSweepLeavesAnUnresolvedFileAlone is the safety-critical assertion for
// the pass over the existing library, mirroring the one for imports: a file
// that is still an open question is never handed to the encoder, whatever the
// policy says. Only a file with a MusicBrainz recording already resolved onto
// it is eligible.
func TestTheSweepLeavesAnUnresolvedFileAlone(t *testing.T) {
	needsFfmpeg(t)
	pool := dbtest.Setup(t)
	rootID, root := libraryRoot(t, pool)

	pendingPath := filepath.Join(root, "pending.wav")
	sineFile(t, pendingPath, 0.5)
	pendingID := resolvedFile(t, pool, rootID, pendingPath, false)

	sweep := newTranscodeSweep(pool, "")
	job := queueTranscodeSweep(t, pool)
	if _, err := sweep.Sweep(context.Background(), job); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	// Still its own path, still on disc, and marked checked so the sweep does
	// not keep opening a file it will never be allowed to touch.
	pendingPathNow, _, pendingFormat, pendingChecked := fileRow(t, pool, pendingID)
	if pendingPathNow != pendingPath {
		t.Fatalf("the unresolved file's path changed to %q", pendingPathNow)
	}
	if pendingFormat != nil {
		t.Fatalf("the unresolved file was recorded as transcoded: %v", *pendingFormat)
	}
	if _, err := os.Stat(pendingPath); err != nil {
		t.Fatalf("the unresolved file's original was removed: %v", err)
	}
	if pendingChecked {
		t.Fatal("a file the sweep never opened was marked checked")
	}
	if count := removalsFor(t, pool, pendingID); count != 0 {
		t.Fatalf("a removal record exists for a file that was never shrunk")
	}
}

func removalsFor(t *testing.T, pool *pgxpool.Pool, fileID uuid.UUID) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM library_file_removals WHERE library_file_id = $1
	`, fileID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func queueTranscodeSweep(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	sweep := newTranscodeSweep(pool, "")
	row, err := sweep.QueueSweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return row.ID
}

// TestTheSweepLeavesAnAlreadyMP3FileAlone confirms the no-op case: a file
// already at the target format is marked checked and never touched again.
func TestTheSweepLeavesAnAlreadyMP3FileAlone(t *testing.T) {
	needsFfmpeg(t)
	pool := dbtest.Setup(t)
	rootID, root := libraryRoot(t, pool)

	path := filepath.Join(root, "already.mp3")
	sineFile(t, path+".wav", 0.5)
	if err := os.Rename(path+".wav", path); err != nil {
		t.Fatal(err)
	}
	fileID := resolvedFile(t, pool, rootID, path, true)

	sweep := newTranscodeSweep(pool, "")
	job := queueTranscodeSweep(t, pool)
	if _, err := sweep.Sweep(context.Background(), job); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	pathNow, _, fromFormat, checked := fileRow(t, pool, fileID)
	if pathNow != path {
		t.Fatalf("path changed to %q", pathNow)
	}
	if fromFormat != nil {
		t.Fatalf("an MP3 was recorded as transcoded: %v", *fromFormat)
	}
	if !checked {
		t.Fatal("the file was not marked checked")
	}
}
