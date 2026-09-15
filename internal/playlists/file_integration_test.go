package playlists

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/rs/zerolog"
)

// newFileImportService is a service with nothing configured at all. A file
// import must not need Spotify credentials, an authorized account, or any
// other connection: the file is the whole source.
func newFileImportService(t *testing.T, pool *pgxpool.Pool) *Service {
	t.Helper()
	return NewService(db.New(pool), zerolog.Nop())
}

const exportedCSV = "Track Name,Artist Name(s),Album Name,Duration (ms),ISRC\n" +
	"Motorola,$NOT,Motorola,145319,QM42K1858315\n" +
	"HDMI,BONES,Rotten,139000,\n"

func TestImportingAFileNeedsNoSpotifyAccount(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)

	var settings int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM spotify_settings`).Scan(&settings); err != nil {
		t.Fatal(err)
	}
	if settings != 0 {
		t.Fatalf("the test starts with %d Spotify settings rows, want none", settings)
	}

	playlist, err := newFileImportService(t, pool).
		ImportFile(ctx, "Evening.csv", "", []byte(exportedCSV))
	if err != nil {
		t.Fatalf("ImportFile() error = %v", err)
	}
	if playlist.Source != "file" || playlist.SourceID.String != "Evening.csv" {
		t.Fatalf("playlist = %#v", playlist)
	}
	if playlist.Name != "Evening" || playlist.TrackCount != 2 || !playlist.ImportedAt.Valid {
		t.Fatalf("playlist = %#v", playlist)
	}
	if playlist.SourceRevision.Valid {
		t.Fatalf("a file has no revision, got %q", playlist.SourceRevision.String)
	}

	entries, err := queries.PlaylistEntries(ctx, playlist.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
}

// A row carrying an ISRC the library already answers is owned, and asks for
// nothing: the same reading the Spotify import makes of the same evidence.
func TestImportFileOwnsARowWhoseISRCTheLibraryAnswers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)

	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, '/music/motorola.flac', 1024, now())
	`, fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, isrc, method, summary
		)
		VALUES ($1, 'external', $2, 'QM42K1858315', 'isrc', 'proven by identifier')
	`, fileID, uuid.New()); err != nil {
		t.Fatal(err)
	}

	playlist, err := newFileImportService(t, pool).
		ImportFile(ctx, "Evening.csv", "", []byte(exportedCSV))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := queries.PlaylistEntries(ctx, playlist.ID)
	if err != nil {
		t.Fatal(err)
	}

	owned, wanted := entries[0], entries[1]
	if !owned.OwnedFileID.Valid || owned.OwnedFileID.UUID != fileID {
		t.Fatalf("the row with the answered ISRC is not owned: %#v", owned)
	}
	if owned.TargetID.Valid {
		t.Fatal("an owned entry must create no want")
	}
	// The row with words alone is a question nobody has answered: it becomes a
	// want carrying exactly what the file said, for the sweep to resolve or
	// for a person to decide.
	if !wanted.TargetID.Valid || wanted.TargetStatus.String != "unresolved" {
		t.Fatalf("text-only row = %#v", wanted)
	}
	if wanted.EntryTitle != "HDMI" || wanted.EntryArtist != "BONES" || wanted.EntryAlbum != "Rotten" {
		t.Fatalf("the want lost the file's words: %#v", wanted)
	}
	if wanted.EntryISRC.Valid {
		t.Fatalf("a row with no ISRC must carry none, got %q", wanted.EntryISRC.String)
	}
}

// An M3U path line that names a file the library holds answers its entry
// outright. Nothing is inferred from a path that names no present file — that
// row goes to the resolver on its words like any other.
func TestImportFileLinksAPathTheLibraryHolds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)

	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, '/music/motorola.flac', 1024, now())
	`, fileID); err != nil {
		t.Fatal(err)
	}

	content := "#EXTM3U\n" +
		"#EXTINF:145,$NOT - Motorola\n/music/motorola.flac\n" +
		"#EXTINF:139,BONES - HDMI\n/music/elsewhere/hdmi.flac\n"
	playlist, err := newFileImportService(t, pool).ImportFile(ctx, "Evening.m3u", "", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := queries.PlaylistEntries(ctx, playlist.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	if !entries[0].OwnedFileID.Valid || entries[0].OwnedFileID.UUID != fileID {
		t.Fatalf("the named file did not answer its entry: %#v", entries[0])
	}
	if entries[0].TargetID.Valid {
		t.Fatal("an entry answered by a file must create no want")
	}
	if entries[1].OwnedFileID.Valid {
		t.Fatalf("a path the library does not hold answered an entry: %#v", entries[1])
	}
	if !entries[1].TargetID.Valid || entries[1].EntryArtist != "BONES" {
		t.Fatalf("unheld path row = %#v", entries[1])
	}
}

// Importing the same file twice leaves one list, the same entries in the same
// order, and no second want. A row that has gone from the file loses its
// entry; the want it made is left standing, because taking a song off a list
// is not the statement "stop trying to obtain this recording".
func TestReimportingTheSameFileCreatesNothingTwice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)
	service := newFileImportService(t, pool)

	first, err := service.ImportFile(ctx, "Evening.csv", "", []byte(exportedCSV))
	if err != nil {
		t.Fatal(err)
	}
	before, err := queries.PlaylistEntries(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}

	second, err := service.ImportFile(ctx, "Evening.csv", "", []byte(exportedCSV))
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("a second import of the same file made a second list: %s and %s", first.ID, second.ID)
	}
	lists, err := queries.Playlists(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(lists) != 1 {
		t.Fatalf("lists = %d, want 1", len(lists))
	}

	after, err := queries.PlaylistEntries(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("entries = %d, want %d", len(after), len(before))
	}
	for index := range after {
		if after[index].ID != before[index].ID || after[index].Position != before[index].Position {
			t.Fatalf("entry %d changed: %#v then %#v", index, before[index], after[index])
		}
		if after[index].TargetID != before[index].TargetID {
			t.Fatalf("entry %d has a second want: %#v", index, after[index])
		}
	}

	var wants int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM acquisition_targets`).Scan(&wants); err != nil {
		t.Fatal(err)
	}
	if wants != 2 {
		t.Fatalf("wants = %d, want 2", wants)
	}

	// The shorter file: one row gone, its entry gone, its want still standing.
	shorter := "Track Name,Artist Name(s),Album Name,Duration (ms),ISRC\n" +
		"Motorola,$NOT,Motorola,145319,QM42K1858315\n"
	if _, err := service.ImportFile(ctx, "Evening.csv", "", []byte(shorter)); err != nil {
		t.Fatal(err)
	}
	remaining, err := queries.PlaylistEntries(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].EntryTitle != "Motorola" {
		t.Fatalf("entries after the shorter file = %#v", remaining)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM acquisition_targets`).Scan(&wants); err != nil {
		t.Fatal(err)
	}
	if wants != 2 {
		t.Fatalf("wants after the shorter file = %d, want 2", wants)
	}
}

// The name is the person's. The file's name identifies the list, so renaming
// it and importing the file again still lands on the same list.
func TestImportFileKeepsTheNameTheImportWasGiven(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	service := newFileImportService(t, pool)

	first, err := service.ImportFile(ctx, "Evening.csv", "Songs for the evening", []byte(exportedCSV))
	if err != nil {
		t.Fatal(err)
	}
	if first.Name != "Songs for the evening" {
		t.Fatalf("name = %q", first.Name)
	}
	second, err := service.ImportFile(ctx, "Evening.csv", "Evening again", []byte(exportedCSV))
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.Name != "Evening again" {
		t.Fatalf("second import = %#v", second)
	}
}

// A new want is unresolved until a sweep looks at it, so an import that made
// one has to ask for a sweep.
func TestImportFileAsksForASweepWhenItMadeWants(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	if _, err := newFileImportService(t, pool).
		ImportFile(ctx, "Evening.csv", "", []byte(exportedCSV)); err != nil {
		t.Fatal(err)
	}
	var sweeps int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM jobs WHERE kind = 'sweep_acquisition_targets' AND status = 'queued'`,
	).Scan(&sweeps); err != nil {
		t.Fatal(err)
	}
	if sweeps != 1 {
		t.Fatalf("queued sweeps = %d, want 1", sweeps)
	}
}

func TestImportFileRefusesAFileWithNoSongs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	_, err := newFileImportService(t, pool).
		ImportFile(ctx, "Evening.csv", "", []byte("Track Name,Artist Name(s)\n"))
	if !errors.Is(err, ErrEmptyFile) {
		t.Fatalf("ImportFile() error = %v, want ErrEmptyFile", err)
	}
	var lists int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM playlists`).Scan(&lists); err != nil {
		t.Fatal(err)
	}
	if lists != 0 {
		t.Fatalf("a refused import left %d lists behind", lists)
	}
}
