package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
)

func seedSearchAlbum(t *testing.T, pool *pgxpool.Pool, artist, title string, tracks int) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	artistID, albumID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name)
		VALUES ($1, $2, $3, $3)
	`, artistID, uuid.New(), artist); err != nil {
		t.Fatalf("seed artist: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, title, album_type)
		VALUES ($1, $2, $3, 'album')
	`, albumID, artistID, title); err != nil {
		t.Fatalf("seed album: %v", err)
	}
	for position := 1; position <= tracks; position++ {
		if _, err := pool.Exec(ctx, `
			INSERT INTO tracks (album_id, title, track_number, disc_number)
			VALUES ($1, $2, $3, 1)
		`, albumID, "Track", position); err != nil {
			t.Fatalf("seed track: %v", err)
		}
	}
	return albumID
}

func TestSourceSearchRunLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	dummy := seedSearchAlbum(t, pool, "Portishead", "Dummy", 11)
	third := seedSearchAlbum(t, pool, "Portishead", "Third", 10)

	queries := New(pool)
	runID, queued, err := queries.CreateSourceSearchRun(ctx, []uuid.UUID{dummy, third}, false)
	if err != nil {
		t.Fatalf("CreateSourceSearchRun() error = %v", err)
	}
	if queued != 2 {
		t.Fatalf("queued = %d, want 2", queued)
	}

	// One job per release, so one unreachable peer costs one result.
	var jobCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)::int FROM jobs
		WHERE kind = 'search_album_sources' AND payload->>'runId' = $1::text
	`, runID).Scan(&jobCount); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if jobCount != 2 {
		t.Fatalf("queued jobs = %d, want 2", jobCount)
	}

	rows, err := queries.SourceSearchRun(ctx, runID)
	if err != nil {
		t.Fatalf("SourceSearchRun() error = %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].Status != "queued" || rows[0].ArtistName != "Portishead" {
		t.Fatalf("first row = %#v", rows[0])
	}
	if rows[0].AlbumTitle != "Dummy" || rows[0].TrackCount != 11 {
		t.Fatalf("first row release = %#v", rows[0])
	}
	if rows[0].RequestedDownloadID.Valid {
		t.Fatalf("a fresh run already reports a download: %#v", rows[0].RequestedDownloadID)
	}

	album, err := queries.ClaimSourceSearch(ctx, runID, dummy)
	if err != nil {
		t.Fatalf("ClaimSourceSearch() error = %v", err)
	}
	if album.ArtistName != "Portishead" || album.AlbumTitle != "Dummy" || album.TrackCount != 11 {
		t.Fatalf("claimed = %#v", album)
	}

	if err := queries.RecordSourceSearch(ctx, RecordSourceSearchParams{
		RunID: runID, AlbumID: dummy, Status: "found", Query: "portishead dummy",
		Candidates: []SourceSearchCandidate{{
			Provider: "slskd", Username: "peer", Directory: "Music/Portishead/Dummy",
			TotalSizeBytes: 55_000_000, Format: "flac", Score: 0.93,
			Reasons: []string{"Lossless FLAC"},
			Files: []SourceSearchFile{
				{Path: `Music\Dummy\01.flac`, Name: "01.flac", Extension: "flac", SizeBytes: 30_000_000},
			},
		}},
		SearchedAt: time.Now(),
	}); err != nil {
		t.Fatalf("RecordSourceSearch() error = %v", err)
	}

	rows, err = queries.SourceSearchRun(ctx, runID)
	if err != nil {
		t.Fatalf("SourceSearchRun() after recording error = %v", err)
	}
	found := rows[0]
	if found.AlbumID != dummy || found.Status != "found" || found.Query != "portishead dummy" {
		t.Fatalf("recorded row = %#v", found)
	}
	if !found.SearchedAt.Valid || found.ErrorMessage.Valid {
		t.Fatalf("recorded timestamps = %#v", found)
	}
	// The candidate is stored whole: a run read back tomorrow has to show what
	// was actually offered, not what a fresh search would find.
	if len(found.Candidates) != 1 || found.Candidates[0].Username != "peer" {
		t.Fatalf("stored candidates = %#v", found.Candidates)
	}
	if len(found.Candidates[0].Files) != 1 || found.Candidates[0].Files[0].Path != `Music\Dummy\01.flac` {
		t.Fatalf("stored files = %#v", found.Candidates[0].Files)
	}
}

// Finding nothing and failing to look are different answers, and a run must
// never render them the same.
func TestSourceSearchSeparatesEmptyFromFailed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	quiet := seedSearchAlbum(t, pool, "Bark Psychosis", "Hex", 7)
	broken := seedSearchAlbum(t, pool, "Talk Talk", "Laughing Stock", 6)

	queries := New(pool)
	runID, _, err := queries.CreateSourceSearchRun(ctx, []uuid.UUID{quiet, broken}, false)
	if err != nil {
		t.Fatalf("CreateSourceSearchRun() error = %v", err)
	}

	if err := queries.RecordSourceSearch(ctx, RecordSourceSearchParams{
		RunID: runID, AlbumID: quiet, Status: "none", Query: "bark psychosis hex",
		SearchedAt: time.Now(),
	}); err != nil {
		t.Fatalf("record empty result: %v", err)
	}
	if err := queries.RecordSourceSearch(ctx, RecordSourceSearchParams{
		RunID: runID, AlbumID: broken, Status: "failed",
		Error: "slskd is unreachable", SearchedAt: time.Now(),
	}); err != nil {
		t.Fatalf("record failed result: %v", err)
	}

	rows, err := queries.SourceSearchRun(ctx, runID)
	if err != nil {
		t.Fatalf("SourceSearchRun() error = %v", err)
	}
	byAlbum := map[uuid.UUID]SourceSearchRow{}
	for _, row := range rows {
		byAlbum[row.AlbumID] = row
	}
	if row := byAlbum[quiet]; row.Status != "none" || row.ErrorMessage.Valid {
		t.Fatalf("empty result = %#v", row)
	}
	if row := byAlbum[broken]; row.Status != "failed" ||
		row.ErrorMessage.String != "slskd is unreachable" {
		t.Fatalf("failed result = %#v", row)
	}
	// Both were looked at, and neither offers anything to choose from.
	if len(byAlbum[quiet].Candidates) != 0 || len(byAlbum[broken].Candidates) != 0 {
		t.Fatalf("candidates leaked into an answerless result")
	}
}

// A run reports the download already recorded against a release, so reopening
// it after acting cannot invite the same folder to be requested twice.
func TestSourceSearchRunReportsExistingDownload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	albumID := seedSearchAlbum(t, pool, "Slowdive", "Souvlaki", 10)
	queries := New(pool)

	runID, _, err := queries.CreateSourceSearchRun(ctx, []uuid.UUID{albumID}, false)
	if err != nil {
		t.Fatalf("CreateSourceSearchRun() error = %v", err)
	}

	requestID, _, err := queries.CreateDownloadRequest(ctx, CreateDownloadRequestParams{
		AlbumID: albumID, Provider: "slskd", SourceUsername: "peer",
		SourceDirectory: "Music/Slowdive/Souvlaki", FileCount: 1,
		ExpectedTrackCount: 10, Format: "flac", Score: 0.8,
		Files: []DownloadRequestFile{
			{Path: "Music/Souvlaki/01.flac", Name: "01.flac", Extension: "flac", SizeBytes: 1},
		},
	})
	if err != nil {
		t.Fatalf("CreateDownloadRequest() error = %v", err)
	}

	rows, err := queries.SourceSearchRun(ctx, runID)
	if err != nil {
		t.Fatalf("SourceSearchRun() error = %v", err)
	}
	if !rows[0].RequestedDownloadID.Valid || rows[0].RequestedDownloadID.UUID != requestID {
		t.Fatalf("requested download = %#v, want %s", rows[0].RequestedDownloadID, requestID)
	}
}

// A candidate is corroborated against the catalogue's own tracks, so claiming a
// search has to hand them over. Without them a search can only report how good
// a copy is, never whether it holds the release that was asked for.
func TestClaimSourceSearchCarriesTheCatalogueTracks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	albumID := seedSearchAlbum(t, pool, "Portishead", "Dummy", 2)
	if _, err := pool.Exec(ctx, `
		UPDATE tracks SET title = 'Mysterons', duration_ms = 205000
		WHERE album_id = $1 AND track_number = 1
	`, albumID); err != nil {
		t.Fatalf("set track: %v", err)
	}
	queries := New(pool)

	runID, _, err := queries.CreateSourceSearchRun(ctx, []uuid.UUID{albumID}, false)
	if err != nil {
		t.Fatalf("CreateSourceSearchRun() error = %v", err)
	}
	album, err := queries.ClaimSourceSearch(ctx, runID, albumID)
	if err != nil {
		t.Fatalf("ClaimSourceSearch() error = %v", err)
	}

	if len(album.Tracks) != 2 {
		t.Fatalf("claimed %d tracks, want both catalogue tracks", len(album.Tracks))
	}
	if album.Tracks[0].Title != "Mysterons" || album.Tracks[0].DurationSeconds != 205 {
		t.Fatalf("first track = %#v, want the title and length in seconds", album.Tracks[0])
	}
}

// Permission to record a download without asking is given when the run starts,
// and the worker has to read it back with the release it is about to search.
func TestClaimSourceSearchCarriesTheRunsPermission(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	albumID := seedSearchAlbum(t, pool, "Burial", "Untrue", 13)
	queries := New(pool)

	runID, _, err := queries.CreateSourceSearchRun(ctx, []uuid.UUID{albumID}, true)
	if err != nil {
		t.Fatalf("CreateSourceSearchRun() error = %v", err)
	}
	album, err := queries.ClaimSourceSearch(ctx, runID, albumID)
	if err != nil {
		t.Fatalf("ClaimSourceSearch() error = %v", err)
	}
	if !album.AutoRequest {
		t.Fatal("the permission the run was started with did not reach the worker")
	}
}

// A download nobody remembers asking for has to be able to say where it came
// from, so a run that acts on its own records that it did.
func TestMarkSourceSearchAutoRequestedRecordsWhatTheRunDid(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	albumID := seedSearchAlbum(t, pool, "Boards of Canada", "Geogaddi", 23)
	queries := New(pool)

	runID, _, err := queries.CreateSourceSearchRun(ctx, []uuid.UUID{albumID}, true)
	if err != nil {
		t.Fatalf("CreateSourceSearchRun() error = %v", err)
	}
	if err := queries.MarkSourceSearchAutoRequested(ctx, runID, albumID, time.Now()); err != nil {
		t.Fatalf("MarkSourceSearchAutoRequested() error = %v", err)
	}

	rows, err := queries.SourceSearchRun(ctx, runID)
	if err != nil {
		t.Fatalf("SourceSearchRun() error = %v", err)
	}
	if !rows[0].AutoRequestedAt.Valid {
		t.Fatal("the run acted on its own without recording that it had")
	}
}

// A run without permission cannot come to have acted. The statement refuses it
// and the table's own constraint refuses it too, because "why is this in my
// downloads" must never have an answer nobody consented to.
func TestMarkSourceSearchAutoRequestedRefusesWithoutPermission(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	albumID := seedSearchAlbum(t, pool, "Aphex Twin", "Drukqs", 30)
	queries := New(pool)

	runID, _, err := queries.CreateSourceSearchRun(ctx, []uuid.UUID{albumID}, false)
	if err != nil {
		t.Fatalf("CreateSourceSearchRun() error = %v", err)
	}
	if err := queries.MarkSourceSearchAutoRequested(ctx, runID, albumID, time.Now()); err != nil {
		t.Fatalf("MarkSourceSearchAutoRequested() error = %v", err)
	}

	rows, err := queries.SourceSearchRun(ctx, runID)
	if err != nil {
		t.Fatalf("SourceSearchRun() error = %v", err)
	}
	if rows[0].AutoRequestedAt.Valid {
		t.Fatal("a run without permission was recorded as having acted on its own")
	}

	// The constraint is the backstop: even a direct write cannot claim an
	// action the run was never permitted.
	if _, err := pool.Exec(ctx, `
		UPDATE source_searches SET auto_requested_at = now() WHERE run_id = $1
	`, runID); err == nil {
		t.Fatal("the table allowed a row to claim an action it was not permitted")
	}
}

func TestSourceSearchRunRefusesUnknownReleases(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	queries := New(pool)
	if _, _, err := queries.CreateSourceSearchRun(ctx, []uuid.UUID{uuid.New()}, false); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("CreateSourceSearchRun() error = %v, want %v", err, pgx.ErrNoRows)
	}
}

// jobStatusFor reads what became of one release's search job, which is where a
// cancelled release is recorded: the search row keeps saying 'queued', because
// nothing ever looked for it.
func jobStatusFor(t *testing.T, pool *pgxpool.Pool, runID, albumID uuid.UUID) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(context.Background(), `
		SELECT status FROM jobs
		WHERE kind = 'search_album_sources'
		  AND payload->>'runId' = $1::text
		  AND payload->>'albumId' = $2::text
	`, runID, albumID).Scan(&status); err != nil {
		t.Fatalf("read job status: %v", err)
	}
	return status
}

func TestCancelSourceSearchRunStopsTheReleasesNobodyHasSearched(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	dummy := seedSearchAlbum(t, pool, "Portishead", "Dummy", 11)
	third := seedSearchAlbum(t, pool, "Portishead", "Third", 10)

	queries := New(pool)
	runID, _, err := queries.CreateSourceSearchRun(ctx, []uuid.UUID{dummy, third}, false)
	if err != nil {
		t.Fatalf("CreateSourceSearchRun() error = %v", err)
	}

	rows, stopped, err := queries.CancelSourceSearchRun(ctx, runID)
	if err != nil {
		t.Fatalf("CancelSourceSearchRun() error = %v", err)
	}
	if stopped != 2 {
		t.Fatalf("stopped = %d, want 2", stopped)
	}
	for _, row := range rows {
		if row.Status != "cancelled" {
			t.Fatalf("row %s status = %q, want cancelled", row.AlbumTitle, row.Status)
		}
	}
	// A cancelled job can no longer be claimed, which is what makes the release
	// permanently unsearched rather than merely still waiting.
	if status := jobStatusFor(t, pool, runID, dummy); status != "cancelled" {
		t.Fatalf("job status = %q, want cancelled", status)
	}
}

// A result is something somebody may act on. Stopping the rest of the run is
// not a reason to withdraw it.
func TestCancelSourceSearchRunLeavesAnAnsweredReleaseAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	dummy := seedSearchAlbum(t, pool, "Portishead", "Dummy", 11)
	third := seedSearchAlbum(t, pool, "Portishead", "Third", 10)

	queries := New(pool)
	runID, _, err := queries.CreateSourceSearchRun(ctx, []uuid.UUID{dummy, third}, false)
	if err != nil {
		t.Fatalf("CreateSourceSearchRun() error = %v", err)
	}
	if err := queries.RecordSourceSearch(ctx, RecordSourceSearchParams{
		RunID: runID, AlbumID: dummy, Status: "found", Query: "portishead dummy",
		Candidates: []SourceSearchCandidate{{Provider: "slskd", Username: "peer", Format: "flac"}},
		SearchedAt: time.Now(),
	}); err != nil {
		t.Fatalf("RecordSourceSearch() error = %v", err)
	}

	rows, stopped, err := queries.CancelSourceSearchRun(ctx, runID)
	if err != nil {
		t.Fatalf("CancelSourceSearchRun() error = %v", err)
	}
	if stopped != 1 {
		t.Fatalf("stopped = %d, want 1", stopped)
	}
	byAlbum := map[uuid.UUID]SourceSearchRow{}
	for _, row := range rows {
		byAlbum[row.AlbumID] = row
	}
	answered := byAlbum[dummy]
	if answered.Status != "found" || len(answered.Candidates) != 1 {
		t.Fatalf("the answered release was changed by cancelling: %#v", answered)
	}
	if byAlbum[third].Status != "cancelled" {
		t.Fatalf("unsearched release status = %q, want cancelled", byAlbum[third].Status)
	}
}

// A job already claimed is the worker's, and taking it away mid-flight would
// risk a result recorded halfway. One release more than was asked for is the
// better outcome.
func TestCancelSourceSearchRunLeavesAClaimedReleaseToFinish(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	dummy := seedSearchAlbum(t, pool, "Portishead", "Dummy", 11)
	third := seedSearchAlbum(t, pool, "Portishead", "Third", 10)

	queries := New(pool)
	runID, _, err := queries.CreateSourceSearchRun(ctx, []uuid.UUID{dummy, third}, false)
	if err != nil {
		t.Fatalf("CreateSourceSearchRun() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE jobs SET status = 'running', started_at = now()
		WHERE kind = 'search_album_sources'
		  AND payload->>'runId' = $1::text
		  AND payload->>'albumId' = $2::text
	`, runID, dummy); err != nil {
		t.Fatalf("claim job: %v", err)
	}
	if _, err := queries.ClaimSourceSearch(ctx, runID, dummy); err != nil {
		t.Fatalf("ClaimSourceSearch() error = %v", err)
	}

	if _, stopped, err := queries.CancelSourceSearchRun(ctx, runID); err != nil {
		t.Fatalf("CancelSourceSearchRun() error = %v", err)
	} else if stopped != 1 {
		t.Fatalf("stopped = %d, want 1", stopped)
	}
	if status := jobStatusFor(t, pool, runID, dummy); status != "running" {
		t.Fatalf("in-flight job status = %q, want running", status)
	}
}

// A search that failed once is put back in the queue with its release still
// reading 'searching', because nothing has answered it yet. It is waiting like
// any other, and cancelling has to reach it — otherwise it waits forever and
// the run never finishes.
func TestCancelSourceSearchRunStopsAReleaseWaitingToBeRetried(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	dummy := seedSearchAlbum(t, pool, "Portishead", "Dummy", 11)

	queries := New(pool)
	runID, _, err := queries.CreateSourceSearchRun(ctx, []uuid.UUID{dummy}, false)
	if err != nil {
		t.Fatalf("CreateSourceSearchRun() error = %v", err)
	}
	if _, err := queries.ClaimSourceSearch(ctx, runID, dummy); err != nil {
		t.Fatalf("ClaimSourceSearch() error = %v", err)
	}
	// What the worker does with a retryable failure: the job goes back to the
	// queue to be run again, and the release keeps saying 'searching'.
	if _, err := pool.Exec(ctx, `
		UPDATE jobs SET status = 'queued', run_after = now() + interval '1 minute'
		WHERE kind = 'search_album_sources' AND payload->>'runId' = $1::text
	`, runID); err != nil {
		t.Fatalf("requeue job: %v", err)
	}

	rows, stopped, err := queries.CancelSourceSearchRun(ctx, runID)
	if err != nil {
		t.Fatalf("CancelSourceSearchRun() error = %v", err)
	}
	if stopped != 1 {
		t.Fatalf("stopped = %d, want 1", stopped)
	}
	if rows[0].Status != "cancelled" {
		t.Fatalf("status = %q, want cancelled", rows[0].Status)
	}
}

// Runs are separate things queued one behind another, so stopping one must not
// reach into the one it was waiting on.
func TestCancelSourceSearchRunLeavesOtherRunsQueued(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	dummy := seedSearchAlbum(t, pool, "Portishead", "Dummy", 11)

	queries := New(pool)
	first, _, err := queries.CreateSourceSearchRun(ctx, []uuid.UUID{dummy}, false)
	if err != nil {
		t.Fatalf("CreateSourceSearchRun() error = %v", err)
	}
	second, _, err := queries.CreateSourceSearchRun(ctx, []uuid.UUID{dummy}, true)
	if err != nil {
		t.Fatalf("CreateSourceSearchRun() error = %v", err)
	}

	if _, _, err := queries.CancelSourceSearchRun(ctx, first); err != nil {
		t.Fatalf("CancelSourceSearchRun() error = %v", err)
	}
	if status := jobStatusFor(t, pool, second, dummy); status != "queued" {
		t.Fatalf("the other run's job status = %q, want queued", status)
	}
	rows, err := queries.SourceSearchRun(ctx, second)
	if err != nil {
		t.Fatalf("SourceSearchRun() error = %v", err)
	}
	if rows[0].Status != "queued" {
		t.Fatalf("the other run's release status = %q, want queued", rows[0].Status)
	}
}

func TestCancelSourceSearchRunRefusesARunThatIsAlreadySearched(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	dummy := seedSearchAlbum(t, pool, "Portishead", "Dummy", 11)

	queries := New(pool)
	runID, _, err := queries.CreateSourceSearchRun(ctx, []uuid.UUID{dummy}, false)
	if err != nil {
		t.Fatalf("CreateSourceSearchRun() error = %v", err)
	}
	if err := queries.RecordSourceSearch(ctx, RecordSourceSearchParams{
		RunID: runID, AlbumID: dummy, Status: "none", Query: "portishead dummy",
		SearchedAt: time.Now(),
	}); err != nil {
		t.Fatalf("RecordSourceSearch() error = %v", err)
	}

	if _, _, err := queries.CancelSourceSearchRun(ctx, runID); !errors.Is(err, ErrNothingToCancel) {
		t.Fatalf("CancelSourceSearchRun() error = %v, want %v", err, ErrNothingToCancel)
	}
}

func TestCancelSourceSearchRunRefusesARunAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	dummy := seedSearchAlbum(t, pool, "Portishead", "Dummy", 11)

	queries := New(pool)
	runID, _, err := queries.CreateSourceSearchRun(ctx, []uuid.UUID{dummy}, false)
	if err != nil {
		t.Fatalf("CreateSourceSearchRun() error = %v", err)
	}
	if _, _, err := queries.CancelSourceSearchRun(ctx, runID); err != nil {
		t.Fatalf("CancelSourceSearchRun() error = %v", err)
	}

	if _, _, err := queries.CancelSourceSearchRun(ctx, runID); !errors.Is(err, ErrNothingToCancel) {
		t.Fatalf("second CancelSourceSearchRun() error = %v, want %v", err, ErrNothingToCancel)
	}
}

func TestCancelSourceSearchRunReportsAnUnknownRunAsMissing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	queries := New(pool)
	if _, _, err := queries.CancelSourceSearchRun(ctx, uuid.New()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("CancelSourceSearchRun() error = %v, want %v", err, pgx.ErrNoRows)
	}
}

// The release that was in flight when a run was cancelled holds a job nothing
// touched, so the worker has to ask. This is how it finds out, and it has to be
// true from the release whose own job is still running.
func TestSourceSearchRunCancelledIsVisibleFromTheReleaseStillRunning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	dummy := seedSearchAlbum(t, pool, "Portishead", "Dummy", 11)
	third := seedSearchAlbum(t, pool, "Portishead", "Third", 10)

	queries := New(pool)
	runID, _, err := queries.CreateSourceSearchRun(ctx, []uuid.UUID{dummy, third}, false)
	if err != nil {
		t.Fatalf("CreateSourceSearchRun() error = %v", err)
	}

	if cancelled, err := queries.SourceSearchRunCancelled(ctx, runID); err != nil {
		t.Fatalf("SourceSearchRunCancelled() error = %v", err)
	} else if cancelled {
		t.Fatal("a run nobody stopped reads as cancelled")
	}

	// Dummy is the worker's: its job is running, so cancelling leaves it alone.
	if _, err := pool.Exec(ctx, `
		UPDATE jobs SET status = 'running', started_at = now()
		WHERE kind = 'search_album_sources'
		  AND payload->>'runId' = $1::text
		  AND payload->>'albumId' = $2::text
	`, runID, dummy); err != nil {
		t.Fatalf("claim job: %v", err)
	}
	if _, err := queries.ClaimSourceSearch(ctx, runID, dummy); err != nil {
		t.Fatalf("ClaimSourceSearch() error = %v", err)
	}
	if _, _, err := queries.CancelSourceSearchRun(ctx, runID); err != nil {
		t.Fatalf("CancelSourceSearchRun() error = %v", err)
	}

	if cancelled, err := queries.SourceSearchRunCancelled(ctx, runID); err != nil {
		t.Fatalf("SourceSearchRunCancelled() error = %v", err)
	} else if !cancelled {
		t.Fatal("the run that was cancelled does not read as cancelled")
	}
	if status := jobStatusFor(t, pool, runID, dummy); status != "running" {
		t.Fatalf("in-flight job status = %q, want running", status)
	}
}

// searchSpentUnderACancelledRun plays out what the worker does when the release
// it was searching runs out of attempts after the run was stopped under it: the
// release is settled with the provider's refusal, and its job is stopped rather
// than queued again. The run around it stays cancelled — the release it never
// reached is still waiting on a job nothing can claim.
func searchSpentUnderACancelledRun(
	t *testing.T, pool *pgxpool.Pool, refusal string,
) map[uuid.UUID]SourceSearchRow {
	t.Helper()
	ctx := context.Background()

	dummy := seedSearchAlbum(t, pool, "Portishead", "Dummy", 11)
	third := seedSearchAlbum(t, pool, "Portishead", "Third", 10)

	queries := New(pool)
	runID, _, err := queries.CreateSourceSearchRun(ctx, []uuid.UUID{dummy, third}, false)
	if err != nil {
		t.Fatalf("CreateSourceSearchRun() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE jobs SET status = 'running', started_at = now(), attempts = max_attempts
		WHERE kind = 'search_album_sources'
		  AND payload->>'runId' = $1::text
		  AND payload->>'albumId' = $2::text
	`, runID, dummy); err != nil {
		t.Fatalf("claim job: %v", err)
	}
	if _, err := queries.ClaimSourceSearch(ctx, runID, dummy); err != nil {
		t.Fatalf("ClaimSourceSearch() error = %v", err)
	}
	if _, _, err := queries.CancelSourceSearchRun(ctx, runID); err != nil {
		t.Fatalf("CancelSourceSearchRun() error = %v", err)
	}

	if err := queries.RecordSourceSearch(ctx, RecordSourceSearchParams{
		RunID: runID, AlbumID: dummy, Status: "failed", Error: refusal, SearchedAt: time.Now(),
	}); err != nil {
		t.Fatalf("RecordSourceSearch() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE jobs SET status = 'cancelled', completed_at = now()
		WHERE kind = 'search_album_sources'
		  AND payload->>'runId' = $1::text
		  AND payload->>'albumId' = $2::text
	`, runID, dummy); err != nil {
		t.Fatalf("stop job: %v", err)
	}

	rows, err := queries.SourceSearchRun(ctx, runID)
	if err != nil {
		t.Fatalf("SourceSearchRun() error = %v", err)
	}
	byAlbum := map[uuid.UUID]SourceSearchRow{}
	for _, row := range rows {
		byAlbum[row.AlbumID] = row
	}
	return byAlbum
}

// A release that ran out of attempts against the provider says so in the
// provider's own words, whatever became of the run it belongs to. Reading it
// back as cancelled would answer "why did this release find nothing" with
// something true about the run and silent about the release.
func TestSourceSearchRunKeepsAFailureSettledUnderACancelledRun(t *testing.T) {
	pool := dbtest.Setup(t)

	refusal := "the download source cannot search at the moment: " +
		"slskd is running but is not connected to the Soulseek network"
	byAlbum := searchSpentUnderACancelledRun(t, pool, refusal)

	for _, row := range byAlbum {
		if row.Status != "failed" {
			continue
		}
		if row.ErrorMessage.String != refusal {
			t.Fatalf("error = %q, want %q", row.ErrorMessage.String, refusal)
		}
		return
	}
	t.Fatal("no release of the run reads as failed; the spent search lost its reason")
}

// The failure is about one release. The releases the run never reached are
// still stopped, and a job of theirs that nothing can claim is what says so.
func TestSourceSearchRunStillReadsCancelledAroundASettledFailure(t *testing.T) {
	pool := dbtest.Setup(t)

	byAlbum := searchSpentUnderACancelledRun(t, pool,
		"the download source cannot search at the moment")

	var cancelled int
	for _, row := range byAlbum {
		if row.Status == "cancelled" {
			cancelled++
		}
	}
	if cancelled != 1 {
		t.Fatalf("cancelled releases = %d, want the one nobody searched", cancelled)
	}
}

// Runs are separate things, and one being stopped says nothing about the one it
// was queued behind.
func TestSourceSearchRunCancelledDoesNotReachOtherRuns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	dummy := seedSearchAlbum(t, pool, "Portishead", "Dummy", 11)

	queries := New(pool)
	first, _, err := queries.CreateSourceSearchRun(ctx, []uuid.UUID{dummy}, false)
	if err != nil {
		t.Fatalf("CreateSourceSearchRun() error = %v", err)
	}
	second, _, err := queries.CreateSourceSearchRun(ctx, []uuid.UUID{dummy}, true)
	if err != nil {
		t.Fatalf("CreateSourceSearchRun() error = %v", err)
	}
	if _, _, err := queries.CancelSourceSearchRun(ctx, first); err != nil {
		t.Fatalf("CancelSourceSearchRun() error = %v", err)
	}

	if cancelled, err := queries.SourceSearchRunCancelled(ctx, second); err != nil {
		t.Fatalf("SourceSearchRunCancelled() error = %v", err)
	} else if cancelled {
		t.Fatal("stopping one run made the next read as cancelled")
	}
}

// A run is stopped while one of its releases is being searched, and then the
// application restarts before that search finishes. Recovery is the pass a
// worker makes once at boot: every job a shutdown left running goes back on the
// queue. Left alone it would put this one back too, and a worker would then
// search a release on behalf of a run the user stopped — spending a provider
// search, and possibly adding a result to a page that was told to stop.
//
// The release the run stopped before anybody looked at keeps its cancelled job.
// The one that was in flight now ends the same way, and nothing that was
// already answered is touched.
func TestRecoveryStopsTheSearchOfARunThatWasCancelled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	dummy := seedSearchAlbum(t, pool, "Portishead", "Dummy", 11)
	third := seedSearchAlbum(t, pool, "Portishead", "Third", 10)

	queries := New(pool)
	runID, _, err := queries.CreateSourceSearchRun(ctx, []uuid.UUID{dummy, third}, false)
	if err != nil {
		t.Fatalf("CreateSourceSearchRun() error = %v", err)
	}

	// Dummy is the one in flight: its job is claimed, so cancelling the run
	// leaves it alone to finish.
	if _, err := pool.Exec(ctx, `
		UPDATE jobs SET status = 'running', started_at = now()
		WHERE kind = 'search_album_sources'
		  AND payload->>'runId' = $1::text
		  AND payload->>'albumId' = $2::text
	`, runID, dummy); err != nil {
		t.Fatalf("claim job: %v", err)
	}
	if _, err := queries.ClaimSourceSearch(ctx, runID, dummy); err != nil {
		t.Fatalf("ClaimSourceSearch() error = %v", err)
	}
	if _, _, err := queries.CancelSourceSearchRun(ctx, runID); err != nil {
		t.Fatalf("CancelSourceSearchRun() error = %v", err)
	}
	if status := jobStatusFor(t, pool, runID, dummy); status != "running" {
		t.Fatalf("in-flight job status = %q, want running before recovery", status)
	}

	if err := queries.RequeueInterruptedJobs(ctx); err != nil {
		t.Fatalf("RequeueInterruptedJobs() error = %v", err)
	}

	if status := jobStatusFor(t, pool, runID, dummy); status != "cancelled" {
		t.Fatalf("the in-flight job of a stopped run is %q after recovery, want cancelled", status)
	}
	if status := jobStatusFor(t, pool, runID, third); status != "cancelled" {
		t.Fatalf("the release nobody searched is %q, want cancelled", status)
	}
	// Nothing was searched, so no release of the run recorded an answer.
	var answered int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM source_searches
		WHERE run_id = $1 AND status NOT IN ('queued', 'searching')
	`, runID).Scan(&answered); err != nil {
		t.Fatalf("count answered releases: %v", err)
	}
	if answered != 0 {
		t.Fatalf("recovery left %d answered releases, want none", answered)
	}
}

// Recovery must not stop a job of a run nobody cancelled. That is the ordinary
// restart: the search was interrupted, and it goes back on the queue to be made
// again.
func TestRecoveryStillPutsBackTheSearchOfARunNobodyStopped(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	dummy := seedSearchAlbum(t, pool, "Portishead", "Dummy", 11)

	queries := New(pool)
	runID, _, err := queries.CreateSourceSearchRun(ctx, []uuid.UUID{dummy}, false)
	if err != nil {
		t.Fatalf("CreateSourceSearchRun() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE jobs SET status = 'running', started_at = now()
		WHERE kind = 'search_album_sources' AND payload->>'runId' = $1::text
	`, runID); err != nil {
		t.Fatalf("claim job: %v", err)
	}

	if err := queries.RequeueInterruptedJobs(ctx); err != nil {
		t.Fatalf("RequeueInterruptedJobs() error = %v", err)
	}

	if status := jobStatusFor(t, pool, runID, dummy); status != "queued" {
		t.Fatalf("job status = %q after recovery, want queued", status)
	}
}
