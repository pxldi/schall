package db

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
)

func TestDownloadImportLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID, albumID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx,
		"INSERT INTO artists (id, name, sort_name) VALUES ($1, 'Artist', 'Artist')",
		artistID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		"INSERT INTO albums (id, artist_id, title) VALUES ($1, $2, 'Album')",
		albumID, artistID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO tracks (album_id, title, disc_number, track_number, duration_ms)
		VALUES ($1, 'First', 1, 1, 100000), ($1, 'Second', 1, 2, 200000)
	`, albumID); err != nil {
		t.Fatal(err)
	}
	queries := New(pool)
	requestID, _, err := queries.CreateDownloadRequest(ctx, CreateDownloadRequestParams{
		AlbumID: albumID, Provider: "slskd", SourceUsername: "peer",
		SourceDirectory: "Music/Album", FileCount: 2, ExpectedTrackCount: 2,
		TotalSizeBytes: 7, Format: "flac", Score: 1,
		Files: []DownloadRequestFile{
			{Path: `Music\Album\01.flac`, Name: "01.flac", SizeBytes: 3},
			{Path: `Music\Album\02.flac`, Name: "02.flac", SizeBytes: 4},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.StartDownloadRequest(ctx, requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.RecordTransferProgress(ctx, requestID, []TransferProgress{
		{Path: `Music\Album\01.flac`, State: "completed", SizeBytes: 3, TransferredBytes: 3},
		{Path: `Music\Album\02.flac`, State: "completed", SizeBytes: 4, TransferredBytes: 4},
	}); err != nil {
		t.Fatal(err)
	}
	if err := queries.QueueDownloadImport(ctx, requestID); err != nil {
		t.Fatal(err)
	}
	claimed, err := queries.ClaimDownloadImport(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ArtistName != "Artist" || claimed.AlbumTitle != "Album" ||
		len(claimed.Files) != 2 || len(claimed.Tracks) != 2 {
		t.Fatalf("claimed = %#v", claimed)
	}
	localPaths := map[string]ImportedDownloadFile{
		`Music\Album\01.flac`: {LocalPath: "/music/Artist/Album/01.flac", TrackID: claimed.Tracks[0].ID},
		`Music\Album\02.flac`: {LocalPath: "/music/Artist/Album/02.flac", TrackID: claimed.Tracks[1].ID},
	}
	if err := queries.CompleteDownloadImport(
		ctx, requestID, "/music", "/music/Artist/Album", localPaths,
	); err != nil {
		t.Fatal(err)
	}
	stored, err := queries.DownloadRequest(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ImportStatus != "imported" || !stored.ImportedAt.Valid ||
		stored.ImportPath.String != "/music/Artist/Album" {
		t.Fatalf("stored import = %#v", stored)
	}
	var linked, roots, scans int
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM downloads WHERE download_request_id = $1 AND local_path IS NOT NULL),
		  (SELECT count(*) FROM library_roots WHERE path = '/music' AND enabled),
		  (SELECT count(*) FROM jobs WHERE kind = 'scan_library' AND status = 'queued')
	`, requestID).Scan(&linked, &roots, &scans); err != nil {
		t.Fatal(err)
	}
	if linked != 2 || roots != 1 || scans != 1 {
		t.Fatalf("linked=%d roots=%d scans=%d", linked, roots, scans)
	}
}

// TestDownloadImportReviewHistory covers the retry path: a paused import keeps
// its reason, can be sent back through validation, and the history survives.
func TestDownloadImportReviewHistory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID, albumID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx,
		"INSERT INTO artists (id, name, sort_name) VALUES ($1, 'Artist', 'Artist')", artistID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		"INSERT INTO albums (id, artist_id, title) VALUES ($1, $2, 'Album')", albumID, artistID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO tracks (album_id, title, disc_number, track_number, duration_ms)
		VALUES ($1, 'First', 1, 1, 100000)
	`, albumID); err != nil {
		t.Fatal(err)
	}

	queries := New(pool)
	requestID, _, err := queries.CreateDownloadRequest(ctx, CreateDownloadRequestParams{
		AlbumID: albumID, Provider: "slskd", SourceUsername: "peer",
		SourceDirectory: "Music/Album", FileCount: 1, ExpectedTrackCount: 1,
		TotalSizeBytes: 3, Format: "flac", Score: 1,
		Files: []DownloadRequestFile{{Path: `Music\Album\01.flac`, Name: "01.flac", SizeBytes: 3}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.StartDownloadRequest(ctx, requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.RecordTransferProgress(ctx, requestID, []TransferProgress{
		{Path: `Music\Album\01.flac`, State: "completed", SizeBytes: 3, TransferredBytes: 3},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.ClaimDownloadImport(ctx, requestID); err != nil {
		t.Fatal(err)
	}
	evidence := &ImportEvidence{
		Files: []ImportFileEvidence{{
			Name: "01.flac", Position: "1-1",
			Observed: &ImportTags{Album: "Another release", Title: "First", TrackNumber: 1},
			Expected: &ImportTags{Album: "Album", Title: "First", TrackNumber: 1},
			Problems: []string{"tags name another release"},
		}},
		UnmatchedTracks: []ImportTags{},
		Problems:        []string{},
	}
	if err := queries.MarkDownloadImportNeedsReview(
		ctx, requestID, "tags name another release", evidence,
	); err != nil {
		t.Fatal(err)
	}

	paused, err := queries.DownloadRequest(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if paused.ImportStatus != "needs_review" || paused.ImportError.String != "tags name another release" {
		t.Fatalf("paused = %#v", paused)
	}
	if len(paused.ImportReviews) != 1 || paused.ImportReviews[0].Kind != "paused" ||
		paused.ImportReviews[0].Detail != "tags name another release" {
		t.Fatalf("history = %#v", paused.ImportReviews)
	}
	// The evidence survives the round trip, so review reads what validation
	// observed rather than only the one line stored on the request.
	stored := paused.ImportReviews[0].Evidence
	if stored == nil || len(stored.Files) != 1 || stored.Files[0].Observed == nil ||
		stored.Files[0].Observed.Album != "Another release" ||
		stored.Files[0].Expected == nil || stored.Files[0].Expected.Album != "Album" {
		t.Fatalf("evidence = %#v", stored)
	}

	// A reviewer can answer the disputed identity for one track. The decision
	// is stored against the release it belongs to and nothing else.
	var trackID uuid.UUID
	if err := pool.QueryRow(ctx, "SELECT id FROM tracks WHERE album_id = $1", albumID).Scan(&trackID); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.RecordImportDecision(ctx, RecordImportDecisionParams{
		RequestID: requestID, FileName: "01.flac", TrackID: uuid.New(),
		Fingerprint: "irrelevant",
	}); !errors.Is(err, ErrTrackNotInRelease) {
		t.Fatalf("foreign track error = %v, want ErrTrackNotInRelease", err)
	}
	decided, err := queries.RecordImportDecision(ctx, RecordImportDecisionParams{
		RequestID: requestID, FileName: "01.flac", TrackID: trackID,
		Fingerprint: stored.Files[0].Observed.Fingerprint(), Note: "same recording, other tags",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Deciding changes nothing about the import itself: it is still paused,
	// and validation still has to run before anything is imported.
	if decided.ImportStatus != "needs_review" || len(decided.ImportDecisions) != 1 ||
		decided.ImportDecisions[0].TrackTitle != "First" ||
		decided.ImportDecisions[0].Note != "same recording, other tags" {
		t.Fatalf("decided = %#v", decided)
	}
	// The decision reaches validation with the fingerprint it was made
	// against, which is what keeps it from applying to different audio.
	applied, err := queries.importDecisions(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if reaching, ok := applied["01.flac"]; !ok || reaching.TrackID != trackID ||
		reaching.Fingerprint != stored.Files[0].Observed.Fingerprint() {
		t.Fatalf("decisions reaching validation = %#v", applied)
	}
	withdrawn, err := queries.WithdrawImportDecision(ctx, requestID, decided.ImportDecisions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(withdrawn.ImportDecisions) != 0 {
		t.Fatalf("withdrawn = %#v", withdrawn.ImportDecisions)
	}

	retried, err := queries.RevalidateDownloadImport(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	// The retry only reopens validation: the request is pending, not imported,
	// and the reason it stopped for is still readable.
	if retried.ImportStatus != "pending" || retried.ImportError.Valid {
		t.Fatalf("retried = %#v", retried)
	}
	// Pause, decision, withdrawal, and retry are all in the same history.
	kinds := make([]string, 0, len(retried.ImportReviews))
	for _, review := range retried.ImportReviews {
		kinds = append(kinds, review.Kind)
	}
	if strings.Join(kinds, ",") != "paused,resolved,withdrawn,revalidated" {
		t.Fatalf("history = %v", kinds)
	}
	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'import_download' AND status = 'queued'
		  AND payload->>'requestId' = $1::text
	`, requestID).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("queued %d import jobs, want 1", queued)
	}

	// Nothing to retry once the import is no longer waiting for review.
	if _, err := queries.RevalidateDownloadImport(ctx, requestID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("second revalidate error = %v, want pgx.ErrNoRows", err)
	}

	claimed, err := queries.ClaimDownloadImport(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if err := queries.CompleteDownloadImport(ctx, requestID, "/music", "/music/Artist/Album",
		map[string]ImportedDownloadFile{
			`Music\Album\01.flac`: {LocalPath: "/music/Artist/Album/01.flac", TrackID: claimed.Tracks[0].ID},
		},
	); err != nil {
		t.Fatal(err)
	}
	imported, err := queries.DownloadRequest(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if imported.ImportStatus != "imported" || len(imported.ImportReviews) != 5 ||
		imported.ImportReviews[4].Kind != "imported" {
		t.Fatalf("imported = %#v history = %#v", imported.ImportStatus, imported.ImportReviews)
	}
}

// releaseWaitingToBeImported is one catalogued release of two tracks whose
// folder has arrived: the state a claim is made in, with the album it was
// fetched for handed back so the test can credit it.
func releaseWaitingToBeImported(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool,
) (*Queries, uuid.UUID, uuid.UUID) {
	t.Helper()
	artistID, albumID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx,
		"INSERT INTO artists (id, name, sort_name) VALUES ($1, 'Porter Robinson', 'Robinson, Porter')",
		artistID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		"INSERT INTO albums (id, artist_id, title) VALUES ($1, $2, 'SMILE! :D')", albumID, artistID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO tracks (album_id, title, disc_number, track_number, duration_ms)
		VALUES ($1, 'Everything to Me', 1, 1, 100000), ($1, 'Cheerleader', 1, 2, 200000)
	`, albumID); err != nil {
		t.Fatal(err)
	}
	queries := New(pool)
	requestID, _, err := queries.CreateDownloadRequest(ctx, CreateDownloadRequestParams{
		AlbumID: albumID, Provider: "slskd", SourceUsername: "peer",
		SourceDirectory: "Music/Album", FileCount: 1, ExpectedTrackCount: 2,
		TotalSizeBytes: 3, Format: "flac", Score: 1,
		Files: []DownloadRequestFile{{Path: `Music\Album\01.flac`, Name: "01.flac", SizeBytes: 3}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.StartDownloadRequest(ctx, requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.RecordTransferProgress(ctx, requestID, []TransferProgress{
		{Path: `Music\Album\01.flac`, State: "completed", SizeBytes: 3, TransferredBytes: 3},
	}); err != nil {
		t.Fatal(err)
	}
	return queries, requestID, albumID
}

// A release is claimed with the credit the catalogue holds for each of its
// tracks, and for the album beside them. The album's own artist is one artist
// and the track's credit is another claim entirely: a collaboration written into
// a file as one string becomes a third artist in the user's music server, named
// after the pair, with neither real artist reachable from it.
func TestAClaimedImportCarriesTheCreditHeldForEachOfItsTracks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, requestID, albumID := releaseWaitingToBeImported(ctx, t, pool)
	porter, nina := uuid.New(), uuid.New()
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
		SELECT tracks.id, 1, 'Porter Robinson', $2::uuid, ' & '
		FROM tracks WHERE tracks.album_id = $1 AND tracks.track_number = 1
		UNION ALL
		SELECT tracks.id, 2, 'Ninajirachi', $3::uuid, ''
		FROM tracks WHERE tracks.album_id = $1 AND tracks.track_number = 1
	`, albumID, porter, nina); err != nil {
		t.Fatal(err)
	}

	claimed, err := queries.ClaimDownloadImport(ctx, requestID)
	if err != nil {
		t.Fatalf("ClaimDownloadImport() error = %v", err)
	}

	if len(claimed.AlbumCredits) != 1 || claimed.AlbumCredits[0].Name != "Porter Robinson" ||
		claimed.AlbumCredits[0].ArtistID != porter {
		t.Fatalf("the album was claimed as credited to %#v", claimed.AlbumCredits)
	}
	credited := claimed.Tracks[0].Credits
	if len(credited) != 2 {
		t.Fatalf("the first track was claimed as credited to %#v", credited)
	}
	if credited[0].Name != "Porter Robinson" || credited[0].ArtistID != porter {
		t.Fatalf("the first artist credited reads %#v", credited[0])
	}
	if credited[1].Name != "Ninajirachi" || credited[1].ArtistID != nina {
		t.Fatalf("the second artist credited reads %#v", credited[1])
	}
	if len(claimed.Tracks[1].Credits) != 0 {
		t.Fatalf("a track nothing credits was claimed as credited to %#v", claimed.Tracks[1].Credits)
	}
}

// A credit naming somebody MusicBrainz holds no artist for keeps its place in
// the list with no identifier. Rejoining the names has to reproduce the credit
// the release was published with, so a credit dropped for having no identifier
// would leave the record naming somebody it is not by.
func TestACreditWithNoArtistBehindItKeepsItsPlaceInTheList(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, requestID, albumID := releaseWaitingToBeImported(ctx, t, pool)
	known := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO track_artist_credits (
			track_id, position, credited_name, musicbrainz_artist_id
		)
		SELECT tracks.id, 1, 'Some Nobody', NULL::uuid
		FROM tracks WHERE tracks.album_id = $1 AND tracks.track_number = 1
		UNION ALL
		SELECT tracks.id, 2, 'Porter Robinson', $2::uuid
		FROM tracks WHERE tracks.album_id = $1 AND tracks.track_number = 1
	`, albumID, known); err != nil {
		t.Fatal(err)
	}

	claimed, err := queries.ClaimDownloadImport(ctx, requestID)
	if err != nil {
		t.Fatalf("ClaimDownloadImport() error = %v", err)
	}

	credited := claimed.Tracks[0].Credits
	if len(credited) != 2 {
		t.Fatalf("the track was claimed as credited to %#v", credited)
	}
	if credited[0].Name != "Some Nobody" || credited[0].ArtistID != uuid.Nil {
		t.Fatalf("the credit MusicBrainz has no artist for reads %#v", credited[0])
	}
	if credited[1].ArtistID != known {
		t.Fatalf("the identifier landed against %#v", credited[1])
	}
}

// importedRelease is one release whose folder has arrived and been imported: the
// state the retention decision is made in.
func importedRelease(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool,
) (*Queries, uuid.UUID) {
	t.Helper()

	artistID, albumID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx,
		"INSERT INTO artists (id, name, sort_name) VALUES ($1, 'Artist', 'Artist')", artistID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		"INSERT INTO albums (id, artist_id, title) VALUES ($1, $2, 'Album')", albumID, artistID,
	); err != nil {
		t.Fatal(err)
	}
	queries := New(pool)
	requestID, _, err := queries.CreateDownloadRequest(ctx, CreateDownloadRequestParams{
		AlbumID: albumID, Provider: "slskd", SourceUsername: "peer",
		SourceDirectory: "Music/Album", FileCount: 1, ExpectedTrackCount: 1,
		TotalSizeBytes: 3, Format: "flac", Score: 1,
		Files: []DownloadRequestFile{{Path: `Music\Album\01.flac`, Name: "01.flac", SizeBytes: 3}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.StartDownloadRequest(ctx, requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.RecordTransferProgress(ctx, requestID, []TransferProgress{
		{Path: `Music\Album\01.flac`, State: "completed", SizeBytes: 3, TransferredBytes: 3},
	}); err != nil {
		t.Fatal(err)
	}
	return queries, requestID
}

// Removal happens after the import is committed, so it can only ever be reported
// as a later entry in the history — never as part of the transaction that made
// the managed copy authoritative.
func TestRemovingTheProvidersCopyIsAppendedAfterTheImport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, requestID := importedRelease(ctx, t, pool)
	if _, err := queries.ClaimDownloadImport(ctx, requestID); err != nil {
		t.Fatal(err)
	}
	if err := queries.CompleteDownloadImport(ctx, requestID, "/music", "/music/Artist/Album",
		map[string]ImportedDownloadFile{
			`Music\Album\01.flac`: {LocalPath: "/music/Artist/Album/01.flac"},
		}); err != nil {
		t.Fatal(err)
	}

	if err := queries.RecordSourceRetention(ctx, requestID, true, "1 file removed"); err != nil {
		t.Fatalf("RecordSourceRetention() error = %v", err)
	}

	stored, err := queries.DownloadRequest(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	last := stored.ImportReviews[len(stored.ImportReviews)-1]
	if last.Kind != "source_removed" || last.Detail != "1 file removed" {
		t.Fatalf("last history entry = %+v, want the removal recorded after the import", last)
	}
}

// Keeping the provider's copy is the default, and it is written down too: an
// import that took nothing away has to be able to say so.
func TestKeepingTheProvidersCopyIsRecordedAsWell(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, requestID := importedRelease(ctx, t, pool)

	if err := queries.RecordSourceRetention(ctx, requestID, false, "left where it was"); err != nil {
		t.Fatalf("RecordSourceRetention() error = %v", err)
	}

	stored, err := queries.DownloadRequest(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.ImportReviews) != 1 || stored.ImportReviews[0].Kind != "source_retained" {
		t.Fatalf("history = %+v, want the copy recorded as kept", stored.ImportReviews)
	}
}

// Every completed transfer nobody has imported yet is queued at boot, so an
// import that was interrupted by a restart is picked up rather than waiting for
// somebody to notice it.
func TestEveryTransferWaitingToBeImportedIsQueuedAtBoot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, requestID := importedRelease(ctx, t, pool)

	if err := queries.QueuePendingDownloadImports(ctx); err != nil {
		t.Fatalf("QueuePendingDownloadImports() error = %v", err)
	}

	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'import_download' AND payload->>'requestId' = $1::text
	`, requestID).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("queued imports = %d, want the waiting transfer picked up", queued)
	}
}

// And a transfer somebody is already importing is not queued twice: asking for
// the same import again would have two workers copying the same files.
func TestATransferAlreadyBeingImportedIsNotQueuedAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, requestID := importedRelease(ctx, t, pool)

	for range 2 {
		if err := queries.QueuePendingDownloadImports(ctx); err != nil {
			t.Fatalf("QueuePendingDownloadImports() error = %v", err)
		}
	}

	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'import_download' AND payload->>'requestId' = $1::text
	`, requestID).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("queued imports = %d, want exactly one", queued)
	}
}

// The history is an enumerated set of things that can become of an import, held
// by the database: an entry nothing can render is an account nobody can read.
func TestAnImportHistoryEntryOfAnUnknownKindIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	_, requestID := importedRelease(ctx, t, pool)

	if _, err := pool.Exec(ctx, `
		INSERT INTO download_import_reviews (download_request_id, kind, detail)
		VALUES ($1, 'pondered', 'it was thought about')
	`, requestID); err == nil {
		t.Fatal("an import history entry of a kind nothing renders was recorded")
	}
}

// Provenance is the whole point of a recorded decision, and a request fetched for
// a want was never about a release: there is no track list to resolve a file to.
func TestResolvingAFileOfARequestThatIsNotAboutAReleaseIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	targetID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets (
			id, origin, entry_artist, entry_title, musicbrainz_recording_id,
			status, resolution_method, resolved_at, next_attempt_at
		)
		VALUES ($1, 'playlist', 'Anetha', 'Salsed Punk', $2, 'pending', 'isrc', now(), now())
	`, targetID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	requestID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO download_requests (
			id, acquisition_target_id, provider, source_username, source_directory,
			file_count, expected_track_count, total_size_bytes, format, score, files,
			status, import_status, import_error
		)
		VALUES ($1, $2, 'slskd', 'peer', 'Music/Anetha', 1, 1, 9000000, 'flac', 0.7,
		        '[]'::jsonb, 'completed', 'needs_review', 'nothing could decide')
	`, requestID, targetID); err != nil {
		t.Fatal(err)
	}

	_, err := New(pool).RecordImportDecision(ctx, RecordImportDecisionParams{
		RequestID: requestID, FileName: "03.flac", TrackID: uuid.New(),
		Fingerprint: "irrelevant",
	})
	if !errors.Is(err, ErrTrackNotInRelease) {
		t.Fatalf("RecordImportDecision() error = %v, want ErrTrackNotInRelease", err)
	}
}

// Pausing is something validation does to the import it is holding. Pausing one
// nobody is validating would write a reason against a request that is not
// stopped, and the reason would sit there explaining nothing.
func TestPausingAnImportNobodyIsValidatingIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, requestID := importedRelease(ctx, t, pool)

	err := queries.MarkDownloadImportNeedsReview(ctx, requestID, "tags name another release", nil)
	if err == nil {
		t.Fatal("an import nobody was validating was paused")
	}

	stored, err := queries.DownloadRequest(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ImportStatus != "pending" || len(stored.ImportReviews) != 0 {
		t.Fatalf("request = %q with %d history entries, want it untouched",
			stored.ImportStatus, len(stored.ImportReviews))
	}
}

// A peer serves only the name it advertised, so a failed transfer whose path the
// stored request no longer lists cannot be asked for again.
func TestRetryingAFileTheRequestNoLongerListsIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, requestID := importedRelease(ctx, t, pool)
	if _, err := pool.Exec(ctx, `
		UPDATE downloads SET status = 'failed' WHERE download_request_id = $1
	`, requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE download_requests SET status = 'failed', files = '[]'::jsonb WHERE id = $1
	`, requestID); err != nil {
		t.Fatal(err)
	}

	_, _, err := queries.RetryDownloadRequestFiles(ctx, requestID, nil)
	if !errors.Is(err, ErrNoRemotePath) {
		t.Fatalf("RetryDownloadRequestFiles() error = %v, want ErrNoRemotePath", err)
	}
}

// A decision answers a question somebody was asked. An import nobody paused has
// not asked one, and recording a judgement against it would store an answer to
// nothing.
func TestRecordingADecisionAboutAnImportNobodyPausedIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, requestID := importedRelease(ctx, t, pool)

	_, err := queries.RecordImportDecision(ctx, RecordImportDecisionParams{
		RequestID: requestID, FileName: "01.flac", TrackID: uuid.New(),
		Fingerprint: "irrelevant",
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("RecordImportDecision() error = %v, want %v", err, pgx.ErrNoRows)
	}
}

// Withdrawing a judgement nobody recorded is refused rather than written to the
// history as a withdrawal of nothing.
func TestWithdrawingADecisionThatWasNeverMadeIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, requestID := importedRelease(ctx, t, pool)

	_, err := queries.WithdrawImportDecision(ctx, requestID, uuid.New())
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("WithdrawImportDecision() error = %v, want %v", err, pgx.ErrNoRows)
	}
}

// An import validates what has arrived. A transfer still moving has nothing on
// disc to compare against the catalogue, so it is not claimable.
//
// It is not decided about either, and the difference matters: the transfer is
// watched by a poller that writes what it finds, so this request can be ready a
// second later without anybody touching it. The refusal has to say that, because
// the job that reads it as final stops asking and the music is never imported.
func TestClaimingAnImportOfATransferThatHasNotFinishedIsRefusedAsNotReady(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, requestID := importedRelease(ctx, t, pool)
	if _, err := pool.Exec(ctx, `
		UPDATE download_requests SET status = 'started' WHERE id = $1
	`, requestID); err != nil {
		t.Fatal(err)
	}

	_, err := queries.ClaimDownloadImport(ctx, requestID)
	if !errors.Is(err, ErrImportNotReadyYet) {
		t.Fatalf("ClaimDownloadImport() error = %v, want %v", err, ErrImportNotReadyYet)
	}
	if errors.Is(err, ErrImportAlreadyDecided) || errors.Is(err, ErrDownloadRequestGone) {
		t.Fatalf("ClaimDownloadImport() error = %v, want the job to keep its attempts", err)
	}
	if !strings.Contains(err.Error(), "started") {
		t.Fatalf("ClaimDownloadImport() error = %v, want the state that stopped it named", err)
	}
}

// An import records where each downloaded file ended up, against the transfer row
// that fetched it. A path no transfer of this request covers is provenance
// nothing can account for, and the whole import stops rather than recording it.
func TestImportingAFileNoTransferOfTheRequestCoversIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, requestID := importedRelease(ctx, t, pool)
	if _, err := queries.ClaimDownloadImport(ctx, requestID); err != nil {
		t.Fatal(err)
	}

	err := queries.CompleteDownloadImport(ctx, requestID, "/music", "/music/Artist/Album",
		map[string]ImportedDownloadFile{
			`Music\Album\99 nobody fetched this.flac`: {LocalPath: "/music/Artist/Album/99.flac"},
		})
	if err == nil {
		t.Fatal("a file no transfer of this request covers was recorded as imported")
	}

	stored, err := queries.DownloadRequest(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ImportStatus != "validating" {
		t.Fatalf("import status = %q, want the whole import rolled back", stored.ImportStatus)
	}
}

// Completing an import nobody is validating would mark a request imported that no
// validation ever ran on.
func TestCompletingAnImportNobodyIsValidatingIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, requestID := importedRelease(ctx, t, pool)

	err := queries.CompleteDownloadImport(ctx, requestID, "/music", "/music/Artist/Album", nil)
	if err == nil {
		t.Fatal("an import nobody was validating was completed")
	}

	stored, err := queries.DownloadRequest(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ImportStatus != "pending" {
		t.Fatalf("import status = %q, want the request untouched", stored.ImportStatus)
	}
}

// An import that was paused for review has been decided about: a person is
// looking at it, and the request carries the reason it stopped. The import job
// starts by claiming the request, and a claim that finds nothing to take must
// say which state stopped it — and must leave the recorded reason exactly as it
// was, because that reason is what the person is being asked to read.
func TestClaimingAnImportThatWasAlreadyDecidedAboutIsRefusedAndKeepsItsReason(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, requestID := importedRelease(ctx, t, pool)
	if _, err := queries.ClaimDownloadImport(ctx, requestID); err != nil {
		t.Fatal(err)
	}
	const reason = "01.flac is not a track of this release"
	if err := queries.MarkDownloadImportNeedsReview(ctx, requestID, reason, nil); err != nil {
		t.Fatal(err)
	}

	_, err := queries.ClaimDownloadImport(ctx, requestID)

	if !errors.Is(err, ErrImportAlreadyDecided) {
		t.Fatalf("ClaimDownloadImport() error = %v, want %v", err, ErrImportAlreadyDecided)
	}
	if !strings.Contains(err.Error(), "needs_review") {
		t.Fatalf("ClaimDownloadImport() error = %v, want the import state named", err)
	}
	stored, err := queries.DownloadRequest(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ImportError.String != reason {
		t.Fatalf("import_error = %q, want %q unchanged", stored.ImportError.String, reason)
	}
}

// A job carries only the identifier of the request it is for, and a request can
// be deleted between the job being queued and the job being run. Both the claim
// and the question of which kind of import this is must name that, rather than
// reporting that a query returned no rows.
func TestAnImportOfARequestThatIsGoneSaysSo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	missing := uuid.New()

	if _, err := queries.ClaimDownloadImport(ctx, missing); !errors.Is(err, ErrDownloadRequestGone) {
		t.Fatalf("ClaimDownloadImport() error = %v, want %v", err, ErrDownloadRequestGone)
	}
	if _, err := queries.DownloadRequestServesWant(ctx, missing); !errors.Is(err, ErrDownloadRequestGone) {
		t.Fatalf("DownloadRequestServesWant() error = %v, want %v", err, ErrDownloadRequestGone)
	}
}
