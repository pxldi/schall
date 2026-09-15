package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/events"
)

// fakeMatcher stands in for library resolution, which runs on every file a scan
// touches. What it decides is pinned in internal/matching; here it is the hook a
// scan calls, which is also the only point inside a walk a test can act at.
type fakeMatcher struct {
	seen []uuid.UUID
	// onFile, when set, runs before the call returns. It is how a test disturbs
	// the filesystem between one file of a walk and the next.
	onFile func(uuid.UUID) error
}

func (matcher *fakeMatcher) ReconcileFile(_ context.Context, fileID uuid.UUID) error {
	matcher.seen = append(matcher.seen, fileID)
	if matcher.onFile != nil {
		return matcher.onFile(fileID)
	}
	return nil
}

// musicFolder registers one enabled music folder and hands back the directory it
// points at.
func musicFolder(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	rootPath := t.TempDir()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO library_roots (id, path) VALUES ($1, $2)`, uuid.New(), rootPath); err != nil {
		t.Fatal(err)
	}
	return rootPath
}

// writeAudio lays down a file with an extension a scan indexes. What is in it
// does not matter to any test that is about the walk rather than about the tags.
func writeAudio(t *testing.T, directory, name string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte("broken fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestIncrementalScanAndMissingFileRetention(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)

	rootPath := t.TempDir()
	audioPath := filepath.Join(rootPath, "Portishead - Roads.mp3")
	if err := os.WriteFile(audioPath, []byte("broken fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	rootID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO library_roots (id, path) VALUES ($1, $2)`, rootID, rootPath); err != nil {
		t.Fatal(err)
	}

	scanner := NewScanner(pool)
	first, err := scanner.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.Discovered != 1 || first.Updated != 1 || first.Errors != 1 {
		t.Fatalf("first scan = %#v", first)
	}
	second, err := scanner.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second.Unchanged != 1 || second.Updated != 0 {
		t.Fatalf("second scan = %#v", second)
	}

	var fileID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM library_files WHERE path = $1`, audioPath).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	var trackID uuid.UUID
	if err := pool.QueryRow(ctx, `
		WITH artist AS (
			INSERT INTO artists (name, sort_name) VALUES ('Portishead', 'Portishead') RETURNING id
		), album AS (
			INSERT INTO albums (artist_id, title) SELECT id, 'Dummy' FROM artist RETURNING id
		)
		INSERT INTO tracks (album_id, title, track_number)
		SELECT id, 'Roads', 1 FROM album
		RETURNING id
	`).Scan(&trackID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO track_mappings (track_id, library_file_id, method, is_manual)
		VALUES ($1, $2, 'manual', true)
	`, trackID, fileID); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(audioPath); err != nil {
		t.Fatal(err)
	}
	third, err := scanner.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if third.Missing != 1 {
		t.Fatalf("third scan = %#v", third)
	}
	var missing bool
	var mappingCount int
	if err := pool.QueryRow(ctx, `SELECT missing_at IS NOT NULL FROM library_files WHERE id = $1`, fileID).Scan(&missing); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM track_mappings WHERE library_file_id = $1`, fileID).Scan(&mappingCount); err != nil {
		t.Fatal(err)
	}
	if !missing || mappingCount != 1 {
		t.Fatalf("missing = %v, mapping count = %d", missing, mappingCount)
	}
}

// A scan of a large library is where a view asking on a timer is most obviously
// behind, so a scan says as it goes that the library changed rather than only
// once it has finished.
func TestScanAnnouncesADiscoveredFile(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)

	rootPath := t.TempDir()
	audioPath := filepath.Join(rootPath, "Portishead - Roads.mp3")
	if err := os.WriteFile(audioPath, []byte("broken fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO library_roots (id, path) VALUES ($1, $2)`, uuid.New(), rootPath); err != nil {
		t.Fatal(err)
	}

	hub := events.NewHub()
	notices, unsubscribe := hub.Subscribe()
	defer unsubscribe()

	if _, err := NewScanner(pool).WithEvents(hub).Scan(ctx); err != nil {
		t.Fatal(err)
	}

	select {
	case topic := <-notices:
		if topic != events.TopicLibrary {
			t.Fatalf("notice = %q, want the library topic", topic)
		}
	default:
		t.Fatal("a scan that discovered a file published nothing")
	}
}

// A rescan that finds everything where it left it changes nothing a view shows.
// Announcing anyway would cost every open interface a refetch of the whole
// summary for each file that had not moved.
func TestRescanSaysNothingWhenNothingChanged(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)

	rootPath := t.TempDir()
	audioPath := filepath.Join(rootPath, "Portishead - Roads.mp3")
	if err := os.WriteFile(audioPath, []byte("broken fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO library_roots (id, path) VALUES ($1, $2)`, uuid.New(), rootPath); err != nil {
		t.Fatal(err)
	}

	hub := events.NewHub()
	scanner := NewScanner(pool).WithEvents(hub)
	if _, err := scanner.Scan(ctx); err != nil {
		t.Fatal(err)
	}

	// Subscribed only for the second scan, so what is counted is what a rescan
	// of a settled library has to say.
	notices, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	second, err := scanner.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second.Unchanged != 1 {
		t.Fatalf("second scan = %#v", second)
	}

	select {
	case topic := <-notices:
		t.Fatalf("a rescan that changed nothing published %q", topic)
	default:
	}
}

// The extension list is the whole answer to "is this music", so everything else
// in a music folder is walked past rather than indexed and then ignored forever.
func TestAScanIndexesOnlyTheFilesTheLibraryCanHold(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootPath := musicFolder(t, pool)
	writeAudio(t, rootPath, "01 Roads.mp3")
	writeAudio(t, rootPath, "cover.jpg")
	writeAudio(t, rootPath, "notes.txt")
	writeAudio(t, rootPath, "Roads.ape")

	result, err := NewScanner(pool).Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Discovered != 1 {
		t.Fatalf("scan = %#v, want only the audio file discovered", result)
	}
	var indexed int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM library_files`).Scan(&indexed); err != nil {
		t.Fatal(err)
	}
	if indexed != 1 {
		t.Fatalf("indexed %d files, want only the one a scan can hold", indexed)
	}
}

// A symlink is somebody else's file under a name in this folder. Indexing it
// would give the same audio two library rows the moment the real folder is also
// a music folder.
func TestAScanDoesNotIndexASymlinkedFile(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootPath := musicFolder(t, pool)
	elsewhere := writeAudio(t, t.TempDir(), "Roads.mp3")
	if err := os.Symlink(elsewhere, filepath.Join(rootPath, "Roads.mp3")); err != nil {
		t.Fatal(err)
	}

	result, err := NewScanner(pool).Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Discovered != 0 {
		t.Fatalf("scan = %#v, want a symlinked file left alone", result)
	}
}

// The same rule one level up: a symlinked folder is not walked, so a link back
// into the library cannot make a scan index everything twice or loop forever.
func TestAScanDoesNotWalkASymlinkedFolder(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootPath := musicFolder(t, pool)
	elsewhere := t.TempDir()
	writeAudio(t, elsewhere, "Roads.mp3")
	if err := os.Symlink(elsewhere, filepath.Join(rootPath, "Portishead")); err != nil {
		t.Fatal(err)
	}

	result, err := NewScanner(pool).Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Discovered != 0 {
		t.Fatalf("scan = %#v, want a symlinked folder left unwalked", result)
	}
}

// A folder that is not there is an unmounted disk far more often than it is a
// library that emptied itself. The scan fails and says which folder, rather than
// reporting every file in it as missing.
func TestAScanFailsOnAMusicFolderThatIsNotThere(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	missing := filepath.Join(t.TempDir(), "unmounted")
	if _, err := pool.Exec(ctx,
		`INSERT INTO library_roots (id, path) VALUES ($1, $2)`, uuid.New(), missing); err != nil {
		t.Fatal(err)
	}

	_, err := NewScanner(pool).Scan(ctx)
	if err == nil {
		t.Fatal("a scan of a folder that is not there reported success")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("error = %v, want it to name the folder that could not be walked", err)
	}
}

// A file removed while the scan is still walking cannot be read, and the scan
// says so rather than recording a row for bytes that are gone. The job retries,
// and the next walk simply does not see it.
func TestAFileThatDisappearsMidWalkFailsTheScan(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootPath := musicFolder(t, pool)
	writeAudio(t, rootPath, "01.mp3")
	second := writeAudio(t, rootPath, "02.mp3")

	matcher := &fakeMatcher{onFile: func(uuid.UUID) error { return os.Remove(second) }}

	_, err := NewScanner(pool, matcher).Scan(ctx)
	if err == nil {
		t.Fatal("a file that vanished mid-walk was scanned as though it were there")
	}
	if !strings.Contains(err.Error(), "02.mp3") {
		t.Fatalf("error = %v, want it to name the file that went", err)
	}
}

// The whole point of an incremental scan: a file whose bytes changed is read
// again, so its tags and its identity can change with them.
func TestAFileWhoseSizeChangedIsReadAgain(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootPath := musicFolder(t, pool)
	path := writeAudio(t, rootPath, "01.mp3")

	scanner := NewScanner(pool)
	if _, err := scanner.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("a fixture of another length"), 0o644); err != nil {
		t.Fatal(err)
	}

	second, err := scanner.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second.Updated != 1 || second.Unchanged != 0 {
		t.Fatalf("second scan = %#v, want the changed file read again", second)
	}
}

// keptFingerprint reads what was written down about a file's audio.
func keptFingerprint(t *testing.T, ctx context.Context, pool *pgxpool.Pool, path string) string {
	t.Helper()
	var kept *string
	if err := pool.QueryRow(ctx, `
		SELECT fingerprint FROM library_files WHERE path = $1
	`, path).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept == nil {
		return ""
	}
	return *kept
}

// A fingerprint describes audio, and bytes that changed may not be that audio any
// more. Keeping it would risk answering a question about this file with what the
// last one sounded like.
func TestAFileWhoseBytesChangedLosesItsFingerprint(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootPath := musicFolder(t, pool)
	path := writeAudio(t, rootPath, "01.mp3")

	scanner := NewScanner(pool)
	if _, err := scanner.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE library_files SET fingerprint = '302:AQADtEmSJEmSJEmS' WHERE path = $1
	`, path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("a fixture of another length"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := scanner.Scan(ctx); err != nil {
		t.Fatal(err)
	}

	if kept := keptFingerprint(t, ctx, pool, path); kept != "" {
		t.Fatalf("fingerprint = %q, want it thrown away with the audio it described", kept)
	}
}

// What the audio is, read off the stream on the way in. The fixture is a second
// of silence encoded by ffmpeg, so the numbers are the encoder's rather than
// this test's; what is asserted is that a scan asked at all.
func TestAScanRecordsWhatTheAudioIs(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootPath := musicFolder(t, pool)
	path := copyAudio(t, rootPath, "01.flac")

	if _, err := NewScanner(pool).Scan(ctx); err != nil {
		t.Fatal(err)
	}

	properties := storedProperties(t, ctx, pool, path)
	if properties.sampleRate == nil || *properties.sampleRate == 0 {
		t.Fatalf("sample rate = %v, want the stream's own", properties.sampleRate)
	}
	if properties.channels == nil || *properties.channels == 0 {
		t.Fatalf("channels = %v, want the stream's own", properties.channels)
	}
	if properties.measured == nil || *properties.measured < 500 {
		t.Fatalf("measured duration = %v, want about a second", properties.measured)
	}
	if properties.readAt == nil {
		t.Fatal("properties_read_at is unset on a file that was read")
	}
}

// Every file in the library was indexed before there was anywhere to put what
// its audio is, so the answer has to reach the files that never changed. A scan
// reads them once and still counts them unchanged: they are unchanged, and
// calling them updated would tag and rematch the whole library for nothing.
func TestAFileTheLibraryAlreadyHeldIsListenedToOnce(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootPath := musicFolder(t, pool)
	path := copyAudio(t, rootPath, "01.flac")

	scanner := NewScanner(pool)
	if _, err := scanner.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	// The row as it stood before migration 00045.
	if _, err := pool.Exec(ctx, `
		UPDATE library_files
		SET properties_read_at = NULL, sample_rate_hz = NULL, channels = NULL,
		    bit_rate_kbps = NULL, measured_duration_ms = NULL
		WHERE path = $1
	`, path); err != nil {
		t.Fatal(err)
	}

	result, err := scanner.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Unchanged != 1 || result.Updated != 0 {
		t.Fatalf("scan = %+v, want the file counted unchanged", result)
	}
	if properties := storedProperties(t, ctx, pool, path); properties.sampleRate == nil {
		t.Fatal("a file the library already held was never listened to")
	}

	// And once is once: a third scan has nothing left to ask.
	before := storedProperties(t, ctx, pool, path).readAt
	if _, err := scanner.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if after := storedProperties(t, ctx, pool, path).readAt; !after.Equal(*before) {
		t.Fatalf("read at %v then %v, want the audio asked about once", before, after)
	}
}

// The same argument for the length. Until every container was taught to count
// its own, an MP3 was stored with none unless a tagger had written a TLEN frame,
// and a file with none never reaches an auto-match through the tag arm. So the
// scan fills the gap in for a file it finds exactly where it left it, and does
// it once.
func TestAFileTheLibraryAlreadyHeldIsGivenTheLengthItNeverHad(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootPath := musicFolder(t, pool)
	path := copyAudio(t, rootPath, "01.flac")

	scanner := NewScanner(pool)
	if _, err := scanner.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	// The row as it stood for everything that was not a FLAC.
	if _, err := pool.Exec(ctx, `
		UPDATE library_files SET duration_ms = NULL WHERE path = $1
	`, path); err != nil {
		t.Fatal(err)
	}

	result, err := scanner.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Unchanged != 1 || result.Updated != 0 {
		t.Fatalf("scan = %+v, want the file counted unchanged", result)
	}
	if stored := storedDuration(t, ctx, pool, path); stored == nil || *stored <= 0 {
		t.Fatal("a file the library already held was never counted")
	}
}

// And a number the library already holds is left alone. This pass gives the
// library the lengths it never had; it does not relitigate the ones it has,
// which is what makes it unable to take a match away from anybody.
func TestAScanDoesNotRecountALengthTheLibraryAlreadyHolds(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootPath := musicFolder(t, pool)
	path := copyAudio(t, rootPath, "01.flac")

	scanner := NewScanner(pool)
	if _, err := scanner.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	// A length nothing measured — the shape a TLEN frame left behind.
	if _, err := pool.Exec(ctx, `
		UPDATE library_files SET duration_ms = 180000 WHERE path = $1
	`, path); err != nil {
		t.Fatal(err)
	}

	if _, err := scanner.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if stored := storedDuration(t, ctx, pool, path); stored == nil || *stored != 180000 {
		t.Fatalf("duration = %v, want the number the row already held", stored)
	}
}

func storedDuration(t *testing.T, ctx context.Context, pool *pgxpool.Pool, path string) *int32 {
	t.Helper()
	var duration *int32
	if err := pool.QueryRow(ctx,
		`SELECT duration_ms FROM library_files WHERE path = $1`, path,
	).Scan(&duration); err != nil {
		t.Fatal(err)
	}
	return duration
}

// A file whose stream will not parse is asked once and left saying nothing. It
// is not a scan error: plenty of audio the library is glad to hold says nothing
// this can read, and the comparison that reads these numbers refuses for want of
// the numbers rather than for want of somebody having tried.
func TestAFileWhoseAudioSaysNothingIsAskedOnce(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootPath := musicFolder(t, pool)
	path := writeAudio(t, rootPath, "01.mp3")

	result, err := NewScanner(pool).Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}

	properties := storedProperties(t, ctx, pool, path)
	if properties.readAt == nil {
		t.Fatal("properties_read_at is unset, so every scan would ask again")
	}
	if properties.sampleRate != nil {
		t.Fatalf("sample rate = %v, want nothing said", *properties.sampleRate)
	}
	if result.Errors != 1 {
		t.Fatalf("errors = %d, want the one the tags already reported", result.Errors)
	}
}

// The bytes changed, so what the audio is describes audio that is gone. It is
// cleared with the fingerprint and for the same reason.
func TestAFileWhoseBytesChangedIsListenedToAgain(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootPath := musicFolder(t, pool)
	path := copyAudio(t, rootPath, "01.flac")

	scanner := NewScanner(pool)
	if _, err := scanner.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("no longer a stream at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := scanner.Scan(ctx); err != nil {
		t.Fatal(err)
	}

	properties := storedProperties(t, ctx, pool, path)
	if properties.sampleRate != nil {
		t.Fatalf("sample rate = %v, want it thrown away with the audio it described",
			*properties.sampleRate)
	}
	if properties.readAt == nil {
		t.Fatal("properties_read_at is unset, so every scan would ask again")
	}
}

type audioProperties struct {
	sampleRate *int32
	channels   *int32
	measured   *int32
	readAt     *time.Time
}

func storedProperties(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, path string,
) audioProperties {
	t.Helper()
	var stored audioProperties
	if err := pool.QueryRow(ctx, `
		SELECT sample_rate_hz, channels, measured_duration_ms, properties_read_at
		FROM library_files WHERE path = $1
	`, path).Scan(&stored.sampleRate, &stored.channels, &stored.measured, &stored.readAt); err != nil {
		t.Fatal(err)
	}
	return stored
}

func copyAudio(t *testing.T, directory, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", "silence.flac"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The other half of the same rule, and the half that matters after Schall writes
// a tag: the tagger records the size and time of what it wrote, so the next scan
// finds the file unchanged. Rewriting a title does not make the music something
// else, and a fingerprint thrown away here would buy a second decode for no new
// fact.
func TestAFileTheScanFindsUnchangedKeepsItsFingerprint(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootPath := musicFolder(t, pool)
	path := writeAudio(t, rootPath, "01.mp3")

	scanner := NewScanner(pool)
	if _, err := scanner.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE library_files SET fingerprint = '302:AQADtEmSJEmSJEmS' WHERE path = $1
	`, path); err != nil {
		t.Fatal(err)
	}
	if _, err := scanner.Scan(ctx); err != nil {
		t.Fatal(err)
	}

	if kept := keptFingerprint(t, ctx, pool, path); kept != "302:AQADtEmSJEmSJEmS" {
		t.Fatalf("fingerprint = %q, want it kept", kept)
	}
}

// Time does not only go forwards. A file restored from a backup, or copied with
// its timestamps, comes back older than the row that describes it, and a scan
// that only looked for a newer stamp would never read it again.
func TestAFileWhoseModificationTimeWentBackwardsIsReadAgain(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootPath := musicFolder(t, pool)
	path := writeAudio(t, rootPath, "01.mp3")

	scanner := NewScanner(pool)
	if _, err := scanner.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	older := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(path, older, older); err != nil {
		t.Fatal(err)
	}

	second, err := scanner.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second.Updated != 1 || second.Unchanged != 0 {
		t.Fatalf("second scan = %#v, want an older file read again", second)
	}
}

// A file that came back is a change, so it is unmarked and put back through
// resolution. Leaving it missing would keep it out of every view while it sat on
// the disk.
func TestAFileThatCameBackIsResolvedAgain(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootPath := musicFolder(t, pool)
	path := writeAudio(t, rootPath, "01.mp3")
	away := filepath.Join(t.TempDir(), "01.mp3")

	matcher := &fakeMatcher{}
	scanner := NewScanner(pool, matcher)
	if _, err := scanner.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, away); err != nil {
		t.Fatal(err)
	}
	if _, err := scanner.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	// Renaming back keeps the size and the modification time, which is what makes
	// this the unchanged-but-restored case rather than an ordinary update.
	if err := os.Rename(away, path); err != nil {
		t.Fatal(err)
	}
	matcher.seen = nil

	third, err := scanner.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if third.Unchanged != 1 || third.Updated != 0 {
		t.Fatalf("third scan = %#v, want the file back and unchanged", third)
	}
	if len(matcher.seen) != 1 {
		t.Fatalf("resolved %d files, want the restored one asked about again", len(matcher.seen))
	}
	var missing bool
	if err := pool.QueryRow(ctx,
		`SELECT missing_at IS NOT NULL FROM library_files WHERE path = $1`, path).Scan(&missing); err != nil {
		t.Fatal(err)
	}
	if missing {
		t.Fatal("a file that came back is still recorded as missing")
	}
}

// A rescan of a settled library opens nothing. Counting Unchanged proves only
// that the row was not rewritten, so the file's tags are changed underneath a
// size and a modification time that stay put: what is stored afterwards is what
// the last scan read, and a scan that opened the file again would have found the
// new title.
func TestARescanDoesNotReadAFileItHasAlreadyRead(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootPath := musicFolder(t, pool)
	path := filepath.Join(rootPath, "01.flac")
	writeFLAC(t, path, 48000, 144000, "TITLE=Roads")

	scanner := NewScanner(pool)
	if _, err := scanner.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// The same length under a different title, put back with the timestamp it
	// had, so nothing a scan looks at has changed.
	writeFLAC(t, path, 48000, 144000, "TITLE=Glory")
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() {
		t.Fatalf("the fixture changed size from %d to %d", before.Size(), after.Size())
	}

	second, err := scanner.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second.Unchanged != 1 || second.Updated != 0 {
		t.Fatalf("second scan = %#v, want the file left alone", second)
	}
	var title string
	if err := pool.QueryRow(ctx,
		`SELECT title_tag FROM library_files WHERE path = $1`, path).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "Roads" {
		t.Fatalf("title = %q, want the file not to have been read again", title)
	}
}

// Music folders can be switched off without being forgotten, and a switched-off
// folder is one no scan walks.
func TestADisabledMusicFolderIsNotWalked(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootPath := musicFolder(t, pool)
	writeAudio(t, rootPath, "01.mp3")

	disabled := t.TempDir()
	writeAudio(t, disabled, "02.mp3")
	if _, err := pool.Exec(ctx,
		`INSERT INTO library_roots (id, path, enabled) VALUES ($1, $2, false)`,
		uuid.New(), disabled); err != nil {
		t.Fatal(err)
	}

	result, err := NewScanner(pool).Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Discovered != 1 {
		t.Fatalf("scan = %#v, want only the enabled folder walked", result)
	}
}

// Nothing configured is a question for the user, not an empty library. Reporting
// success would mark every file already indexed as missing.
func TestAScanRefusesWhenNoMusicFolderIsEnabled(t *testing.T) {
	pool := dbtest.Setup(t)

	_, err := NewScanner(pool).Scan(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no enabled music folders") {
		t.Fatalf("error = %v, want a scan with nothing to walk refused", err)
	}
}

// A scan that is being shut down stops where it is. The files it had already
// indexed keep their rows; the rest are simply not walked.
func TestAScanStopsWhenItIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := dbtest.Setup(t)
	rootPath := musicFolder(t, pool)
	writeAudio(t, rootPath, "01.mp3")
	writeAudio(t, rootPath, "02.mp3")

	matcher := &fakeMatcher{onFile: func(uuid.UUID) error {
		cancel()
		return nil
	}}

	_, err := NewScanner(pool, matcher).Scan(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want the cancellation reported", err)
	}
	var indexed int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM library_files`).Scan(&indexed); err != nil {
		t.Fatal(err)
	}
	if indexed != 1 {
		t.Fatalf("indexed %d files, want the walk stopped after the first", indexed)
	}
}

// Every file a scan indexes goes through resolution, which is how a new file
// ever becomes music Schall knows about.
func TestEveryIndexedFileIsResolved(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootPath := musicFolder(t, pool)
	path := writeAudio(t, rootPath, "01.mp3")

	matcher := &fakeMatcher{}
	if _, err := NewScanner(pool, matcher).Scan(ctx); err != nil {
		t.Fatal(err)
	}

	var fileID uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT id FROM library_files WHERE path = $1`, path).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	if len(matcher.seen) != 1 || matcher.seen[0] != fileID {
		t.Fatalf("resolved %v, want the file that was indexed", matcher.seen)
	}
}

// A file that has gone is a change to what the library holds, so what was
// matched to it has to be asked again.
func TestAFileThatWentMissingIsResolvedAgain(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootPath := musicFolder(t, pool)
	path := writeAudio(t, rootPath, "01.mp3")

	matcher := &fakeMatcher{}
	scanner := NewScanner(pool, matcher)
	if _, err := scanner.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	matcher.seen = nil

	second, err := scanner.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second.Missing != 1 {
		t.Fatalf("second scan = %#v", second)
	}
	if len(matcher.seen) != 1 {
		t.Fatalf("resolved %v, want the file that went missing asked about again", matcher.seen)
	}
}

// Resolution failing is a database or a provider failing, and the scan reports
// it rather than finishing and claiming the library is up to date.
func TestAScanFailsWhenANewFileCannotBeResolved(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootPath := musicFolder(t, pool)
	writeAudio(t, rootPath, "01.mp3")

	matcher := &fakeMatcher{onFile: func(uuid.UUID) error {
		return errors.New("the catalogue is unreachable")
	}}

	_, err := NewScanner(pool, matcher).Scan(ctx)
	if err == nil || !strings.Contains(err.Error(), "catalogue is unreachable") {
		t.Fatalf("error = %v, want the resolution failure reported", err)
	}
}

// The same for a file that came back: it is a change, and a change nobody could
// resolve must not read as a scan that finished cleanly.
func TestAScanFailsWhenARestoredFileCannotBeResolved(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootPath := musicFolder(t, pool)
	path := writeAudio(t, rootPath, "01.mp3")
	away := filepath.Join(t.TempDir(), "01.mp3")

	matcher := &fakeMatcher{}
	scanner := NewScanner(pool, matcher)
	if _, err := scanner.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, away); err != nil {
		t.Fatal(err)
	}
	if _, err := scanner.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(away, path); err != nil {
		t.Fatal(err)
	}
	matcher.onFile = func(uuid.UUID) error { return errors.New("the catalogue is unreachable") }

	_, err := scanner.Scan(ctx)
	if err == nil || !strings.Contains(err.Error(), "catalogue is unreachable") {
		t.Fatalf("error = %v, want the resolution failure reported", err)
	}
}

// And for a file that went: whatever was matched to it is still matched to it
// until something says otherwise, so a failure there stops the scan too.
func TestAScanFailsWhenAMissingFileCannotBeResolved(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootPath := musicFolder(t, pool)
	path := writeAudio(t, rootPath, "01.mp3")

	matcher := &fakeMatcher{}
	scanner := NewScanner(pool, matcher)
	if _, err := scanner.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	matcher.onFile = func(uuid.UUID) error { return errors.New("the catalogue is unreachable") }

	_, err := scanner.Scan(ctx)
	if err == nil || !strings.Contains(err.Error(), "catalogue is unreachable") {
		t.Fatalf("error = %v, want the resolution failure reported", err)
	}
}

// The length a scan indexes is the length the stream itself holds. A TLEN frame
// somebody typed used to win here, which put an invented number in front of the
// five seconds a match can turn on.
func TestAScanIndexesAFLACAtTheLengthItsStreamHolds(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootPath := musicFolder(t, pool)
	path := filepath.Join(rootPath, "01 Roads.flac")
	writeFLAC(t, path, 48000, 144000, "TLEN=180000")

	if _, err := NewScanner(pool).Scan(ctx); err != nil {
		t.Fatal(err)
	}

	var durationMS int
	if err := pool.QueryRow(ctx,
		`SELECT duration_ms FROM library_files WHERE path = $1`, path).Scan(&durationMS); err != nil {
		t.Fatal(err)
	}
	if durationMS != 3000 {
		t.Fatalf("indexed duration = %d, want the 3000 ms the stream itself holds", durationMS)
	}
}

// A file whose tags will not parse is still a file the library holds. The reason
// is stored against it so it can be looked at, and the scan carries on.
func TestAFileWhoseTagsWillNotParseIsIndexedWithTheReason(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootPath := musicFolder(t, pool)
	path := writeAudio(t, rootPath, "01 Roads.flac")

	result, err := NewScanner(pool).Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Errors != 1 || result.Updated != 1 {
		t.Fatalf("scan = %#v, want the file indexed and the failure counted", result)
	}
	var scanError *string
	if err := pool.QueryRow(ctx,
		`SELECT scan_error FROM library_files WHERE path = $1`, path).Scan(&scanError); err != nil {
		t.Fatal(err)
	}
	if scanError == nil || *scanError == "" {
		t.Fatal("a file whose tags would not parse says nothing about why")
	}
}

// A managed copy becomes a library file the moment a scan reaches it, and the
// download it came from has to point at that row: it is the provenance every
// later decision about the file rests on.
func TestAScanTiesADownloadToTheLibraryFileItBecame(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootPath := musicFolder(t, pool)
	path := writeAudio(t, rootPath, "01.mp3")

	var downloadID uuid.UUID
	if err := pool.QueryRow(ctx, `
		WITH artist AS (
			INSERT INTO artists (name, sort_name) VALUES ('Portishead', 'Portishead') RETURNING id
		), album AS (
			INSERT INTO albums (artist_id, title) SELECT id, 'Dummy' FROM artist RETURNING id
		), request AS (
			INSERT INTO download_requests (
				album_id, provider, source_username, source_directory,
				file_count, expected_track_count, total_size_bytes, format, score
			)
			SELECT id, 'slskd', 'peer', 'Music', 1, 1, 14, 'mp3', 0.9 FROM album
			RETURNING id
		)
		INSERT INTO downloads (download_request_id, remote_path, local_path)
		SELECT id, 'Music\01.mp3', $1 FROM request
		RETURNING id
	`, path).Scan(&downloadID); err != nil {
		t.Fatal(err)
	}

	if _, err := NewScanner(pool).Scan(ctx); err != nil {
		t.Fatal(err)
	}

	var libraryFileID, indexedID uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT library_file_id FROM downloads WHERE id = $1`, downloadID).Scan(&libraryFileID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT id FROM library_files WHERE path = $1`, path).Scan(&indexedID); err != nil {
		t.Fatal(err)
	}
	if libraryFileID != indexedID {
		t.Fatalf("download points at %s, want the library row %s", libraryFileID, indexedID)
	}
}

// The same rule for an MP3, which is the format the rule was written for. An
// MPEG stream has no length field anywhere in it, so until every container was
// taught to count, an MP3 was indexed with no duration at all — and a file with
// no duration reaches matching as silence, which never agrees and never
// contradicts. Walking the frames is what gives it a number.
func TestAScanIndexesAnMP3AtTheLengthItsFramesCount(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootPath := musicFolder(t, pool)
	path := copyFixture(t, "silence.mp3", rootPath, "01 Roads.mp3")

	if _, err := NewScanner(pool).Scan(ctx); err != nil {
		t.Fatal(err)
	}

	duration := storedDuration(t, ctx, pool, path)
	if duration == nil {
		t.Fatal("duration_ms is NULL, so the MP3 still reaches matching as silence")
	}
	// The fixture is a second of silence, and the encoder's own run-up is in
	// the stream as frames, so sixteen frames of 72 ms are counted. The number
	// is the file's own, not a claim written about it.
	if *duration != 1152 {
		t.Fatalf("indexed duration = %d, want the 1152 ms its frames count", *duration)
	}
}

// And the other half: a stream that stops before it says anything is still a
// file the library holds. It is indexed with no duration, which is the honest
// answer, and the scan carries on rather than failing over it.
func TestAScanIndexesATruncatedMP3WithNoLengthAtAll(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootPath := musicFolder(t, pool)
	whole, err := os.ReadFile(filepath.Join("testdata", "silence.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(rootPath, "02 Cut Short.mp3")
	if err := os.WriteFile(path, whole[:12], 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := NewScanner(pool).Scan(ctx); err != nil {
		t.Fatal(err)
	}

	if duration := storedDuration(t, ctx, pool, path); duration != nil {
		t.Fatalf("indexed duration = %d, want none counted off a stream that stopped", *duration)
	}
}

// copyFixture lays down one of this package's testdata files under a name a
// scan will walk, so a test can assert on what is actually in the audio.
func copyFixture(t *testing.T, fixture, directory, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// pendingRemovalRow writes the licence for a file the library let go of and has
// not yet managed to unlink, the state a scan must not walk past as if it were
// simply new. keptPath and the recording are placeholders here: it is the
// unlinked_at column a scan reads, not what the row says about the survivor.
func pendingRemovalRow(t *testing.T, pool *pgxpool.Pool, path string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO library_file_removals (
			library_file_id, library_path, size_bytes,
			musicbrainz_recording_id, kept_path, removed_by, reason
		) VALUES ($1, $2, 0, $3, 'kept.flac', 'test', 'keeping one copy')
	`, uuid.New(), path, uuid.New()); err != nil {
		t.Fatal(err)
	}
}

// The row going and the unlink are two separate commits, and a scan can land
// between them. It must not index the audio still on disc as a new file: doing
// so brings the duplicate somebody just asked to remove back under a new id, and
// a later successful unlink would then delete audio out from under a row nobody
// decided to remove.
func TestAScanDoesNotIndexAFileAwaitingUnlink(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootPath := musicFolder(t, pool)
	path := writeAudio(t, rootPath, "roads.mp3")
	pendingRemovalRow(t, pool, path)

	result, err := NewScanner(pool).Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Updated != 0 {
		t.Fatalf("updated = %d, want 0: the file awaiting unlink was indexed", result.Updated)
	}
	var present bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM library_files WHERE path = $1)`, path,
	).Scan(&present); err != nil {
		t.Fatal(err)
	}
	if present {
		t.Fatal("a file awaiting unlink was given a library row")
	}
}

// Once the audio actually leaves the disc — unlinked_at stamped — the path is
// free again, and a file put there afterwards is indexed like any other.
func TestAScanIndexesAPathOnceItsRemovalIsUnlinked(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	rootPath := musicFolder(t, pool)
	path := writeAudio(t, rootPath, "roads.mp3")
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_removals (
			library_file_id, library_path, size_bytes,
			musicbrainz_recording_id, kept_path, removed_by, reason, unlinked_at
		) VALUES ($1, $2, 0, $3, 'kept.flac', 'test', 'keeping one copy', now())
	`, uuid.New(), path, uuid.New()); err != nil {
		t.Fatal(err)
	}

	result, err := NewScanner(pool).Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Updated != 1 {
		t.Fatalf("updated = %d, want 1: a file whose removal already unlinked was skipped", result.Updated)
	}
}
