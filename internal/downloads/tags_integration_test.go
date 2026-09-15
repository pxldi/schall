package downloads

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/library"
	"github.com/pxldi/schall/internal/tagging"
)

// A file is described by the catalogue, not by the road it took to get here. A
// release imported from a folder and a file already in the library that a pass
// writes are two different queries over the same catalogue, and if they ever
// disagreed the same album would sit in the music server under two accounts of
// itself — which is the thing tagging exists to stop.
//
// So the whole account is compared, credit and all: the tags the importer would
// write into this file are handed to AlreadySays against the file the pass
// wrote, and every tag Schall claims has to read back the same.
func TestThePassWritesTheSameAccountAFolderImportWould(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	albumID, trackID := catalogueCollaboration(ctx, t, pool)
	queries := db.New(pool)
	requestID := requestWaitingToBeImported(ctx, t, queries, albumID)
	row, err := queries.ClaimDownloadImport(ctx, requestID)
	if err != nil {
		t.Fatalf("claim the import: %v", err)
	}
	if len(row.Tracks) != 1 || len(row.Tracks[0].Credits) != 2 {
		t.Fatalf("the release was claimed as %#v", row.Tracks)
	}
	path := fileInTheLibrary(ctx, t, pool, trackID)

	if _, err := library.NewTagger(pool, zerolog.Nop()).Tag(ctx, taggingPass(ctx, t, pool)); err != nil {
		t.Fatalf("write the library's tags: %v", err)
	}

	if !tagging.AlreadySays(path, releaseTags(row, row.Tracks[0])) {
		written, _ := os.ReadFile(path)
		t.Fatalf("the pass wrote something a folder import would not have (%d bytes)", len(written))
	}
}

// catalogueCollaboration is one album of one track credited to two artists —
// the case a joined string cannot carry.
func catalogueCollaboration(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool,
) (uuid.UUID, uuid.UUID) {
	t.Helper()
	artistID, albumID := uuid.New(), uuid.New()
	porter, nina := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name)
		VALUES ($1, $2, 'Porter Robinson', 'Robinson, Porter')
	`, artistID, porter); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, musicbrainz_release_group_id, title)
		VALUES ($1, $2, $3, 'SMILE! :D')
	`, albumID, artistID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO release_editions (
			album_id, musicbrainz_release_id, title, release_date, selection_reason
		)
		VALUES ($1, $2, 'SMILE! :D', '2024-04-26', 'chosen by the test')
	`, albumID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	var trackID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tracks (album_id, musicbrainz_recording_id, title, disc_number, track_number)
		VALUES ($1, $2, 'Everything to Me', 1, 1)
		RETURNING id
	`, albumID, uuid.New()).Scan(&trackID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO album_artist_credits (album_id, position, credited_name, musicbrainz_artist_id)
		VALUES ($1, 1, 'Porter Robinson', $2)
	`, albumID, porter); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO track_artist_credits (
			track_id, position, credited_name, musicbrainz_artist_id, join_phrase
		)
		VALUES ($1, 1, 'Porter Robinson', $2, ' & '), ($1, 2, 'Ninajirachi', $3, '')
	`, trackID, porter, nina); err != nil {
		t.Fatal(err)
	}
	return albumID, trackID
}

// requestWaitingToBeImported is a folder of that release, arrived and waiting.
func requestWaitingToBeImported(
	ctx context.Context, t *testing.T, queries *db.Queries, albumID uuid.UUID,
) uuid.UUID {
	t.Helper()
	requestID, _, err := queries.CreateDownloadRequest(ctx, db.CreateDownloadRequestParams{
		AlbumID: albumID, Provider: "slskd", SourceUsername: "peer",
		SourceDirectory: "Music/Album", FileCount: 1, ExpectedTrackCount: 1,
		TotalSizeBytes: 3, Format: "flac", Score: 1,
		Files: []db.DownloadRequestFile{
			{Path: `Music\Album\01.flac`, Name: "01.flac", SizeBytes: 3},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.StartDownloadRequest(ctx, requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.RecordTransferProgress(ctx, requestID, []db.TransferProgress{
		{Path: `Music\Album\01.flac`, State: "completed", SizeBytes: 3, TransferredBytes: 3},
	}); err != nil {
		t.Fatal(err)
	}
	return requestID
}

// fileInTheLibrary lays down a stranger's copy of that track and records it the
// way a scan would, mapped to the catalogue track it answers.
func fileInTheLibrary(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, trackID uuid.UUID,
) string {
	t.Helper()
	audio, err := os.ReadFile(filepath.Join("..", "tagging", "testdata", "silence.flac"))
	if err != nil {
		t.Fatal(err)
	}
	rootPath := t.TempDir()
	rootID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO library_roots (id, path) VALUES ($1, $2)`, rootID, rootPath,
	); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(rootPath, "01.flac")
	if err := os.WriteFile(path, audio, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	var fileID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO library_files (
			library_root_id, path, size_bytes, modified_at, artist_tag, album_tag, title_tag,
			track_number, duration_ms
		)
		VALUES ($1, $2, $3, $4, 'peer artist', 'peer album', 'everything to me', 7, 3000)
		RETURNING id
	`, rootID, path, info.Size(), info.ModTime()).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO track_mappings (track_id, library_file_id, method, confidence)
		VALUES ($1, $2, 'musicbrainz_recording_id', 1)
	`, trackID, fileID); err != nil {
		t.Fatal(err)
	}
	return path
}

// taggingPass asks for the pass over the library and hands back the job carrying
// it.
func taggingPass(ctx context.Context, t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	job, err := library.NewTagger(pool, zerolog.Nop()).QueueTagging(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return job.ID
}
