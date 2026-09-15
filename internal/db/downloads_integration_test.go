package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
)

func TestDownloadRequestLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID, albumID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name)
		VALUES ($1, $2, 'Portishead', 'Portishead')
	`, artistID, uuid.New()); err != nil {
		t.Fatalf("seed artist: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, title, album_type)
		VALUES ($1, $2, 'Dummy', 'album')
	`, albumID, artistID); err != nil {
		t.Fatalf("seed album: %v", err)
	}

	queries := New(pool)
	params := CreateDownloadRequestParams{
		AlbumID: albumID, Provider: "slskd", SourceUsername: "peer",
		SourceDirectory: "Music/Portishead/Dummy",
		FileCount:       2, ExpectedTrackCount: 11, TotalSizeBytes: 55_000_000,
		Format: "flac", Score: 0.93,
		Reasons: []string{"Lossless FLAC", "All 11 catalogue tracks present"},
		Files: []DownloadRequestFile{
			{Name: "01 Mysterons.flac", Extension: "flac", SizeBytes: 30_000_000, DurationSeconds: 305},
			{Name: "02 Sour Times.flac", Extension: "flac", SizeBytes: 25_000_000, DurationSeconds: 254},
		},
	}

	id, created, err := queries.CreateDownloadRequest(ctx, params)
	if err != nil {
		t.Fatalf("CreateDownloadRequest() error = %v", err)
	}
	if !created {
		t.Fatal("first request was not reported as created")
	}

	stored, err := queries.DownloadRequest(ctx, id)
	if err != nil {
		t.Fatalf("DownloadRequest() error = %v", err)
	}
	if stored.Status != "requested" || stored.CancelledAt.Valid {
		t.Fatalf("stored = %#v", stored)
	}
	if stored.AlbumTitle != "Dummy" || stored.ArtistName != "Portishead" || stored.ArtistID.UUID != artistID {
		t.Fatalf("stored target = %#v", stored)
	}
	// The evidence the user acted on is kept verbatim, so a later search that
	// ranks differently cannot rewrite the decision.
	if len(stored.Files) != 2 || stored.Files[0].Name != "01 Mysterons.flac" || stored.Files[1].SizeBytes != 25_000_000 {
		t.Fatalf("stored files = %#v", stored.Files)
	}
	if len(stored.Reasons) != 2 || stored.Reasons[0] != "Lossless FLAC" {
		t.Fatalf("stored reasons = %#v", stored.Reasons)
	}
	if stored.ExpectedTrackCount != 11 || stored.Score != 0.93 || stored.AverageBitRate.Valid {
		t.Fatalf("stored provenance = %#v", stored)
	}

	// Requesting the same folder again returns the open request instead of
	// creating a second one, so an impatient second click cannot become a
	// duplicate acquisition.
	repeatID, repeatCreated, err := queries.CreateDownloadRequest(ctx, params)
	if err != nil {
		t.Fatalf("repeated CreateDownloadRequest() error = %v", err)
	}
	if repeatCreated || repeatID != id {
		t.Fatalf("repeat = %v/%v, want the open request %v reused", repeatID, repeatCreated, id)
	}

	// A different folder for the same release is a separate decision.
	other := params
	other.SourceUsername = "another-peer"
	otherID, otherCreated, err := queries.CreateDownloadRequest(ctx, other)
	if err != nil {
		t.Fatalf("second source CreateDownloadRequest() error = %v", err)
	}
	if !otherCreated || otherID == id {
		t.Fatalf("second source = %v/%v, want a new request", otherID, otherCreated)
	}

	open, err := queries.ListDownloadRequests(ctx, ListDownloadRequestsParams{
		AlbumID: uuid.NullUUID{UUID: albumID, Valid: true}, Status: "requested", Limit: 50,
	})
	if err != nil {
		t.Fatalf("ListDownloadRequests() error = %v", err)
	}
	if open.Total != 2 || len(open.Items) != 2 {
		t.Fatalf("open requests = %d items / total %d, want 2", len(open.Items), open.Total)
	}

	cancelled, err := queries.CancelDownloadRequest(ctx, id)
	if err != nil {
		t.Fatalf("CancelDownloadRequest() error = %v", err)
	}
	if cancelled.Status != "cancelled" || !cancelled.CancelledAt.Valid {
		t.Fatalf("cancelled = %#v", cancelled)
	}
	if _, err := queries.CancelDownloadRequest(ctx, id); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cancelling twice error = %v, want pgx.ErrNoRows", err)
	}

	// The withdrawn decision is still on record, and the folder can be
	// requested again now that nothing open covers it.
	history, err := queries.ListDownloadRequests(ctx, ListDownloadRequestsParams{Limit: 50})
	if err != nil {
		t.Fatalf("ListDownloadRequests() error = %v", err)
	}
	if history.Total != 2 {
		t.Fatalf("history total = %d, want the cancelled request kept", history.Total)
	}
	retryID, retryCreated, err := queries.CreateDownloadRequest(ctx, params)
	if err != nil {
		t.Fatalf("retry CreateDownloadRequest() error = %v", err)
	}
	if !retryCreated || retryID == id {
		t.Fatalf("retry = %v/%v, want a new request after cancelling", retryID, retryCreated)
	}

	// A lossy folder keeps its bitrate, because there it means something.
	lossy := params
	lossy.SourceUsername = "lossy-peer"
	lossy.Format = "mp3"
	lossy.AverageBitRate = pgtype.Int4{Int32: 320, Valid: true}
	lossyID, _, err := queries.CreateDownloadRequest(ctx, lossy)
	if err != nil {
		t.Fatalf("lossy CreateDownloadRequest() error = %v", err)
	}
	lossyRow, err := queries.DownloadRequest(ctx, lossyID)
	if err != nil {
		t.Fatalf("DownloadRequest() error = %v", err)
	}
	if !lossyRow.AverageBitRate.Valid || lossyRow.AverageBitRate.Int32 != 320 {
		t.Fatalf("lossy bitrate = %#v", lossyRow.AverageBitRate)
	}

	// A request belongs to its release: removing the release removes the
	// request rather than leaving provenance pointing nowhere.
	if _, err := pool.Exec(ctx, `DELETE FROM albums WHERE id = $1`, albumID); err != nil {
		t.Fatalf("delete album: %v", err)
	}
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM download_requests`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("%d requests outlived their release", remaining)
	}
}

func TestDownloadTransferLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID, albumID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name)
		VALUES ($1, $2, 'Portishead', 'Portishead')
	`, artistID, uuid.New()); err != nil {
		t.Fatalf("seed artist: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, title, album_type)
		VALUES ($1, $2, 'Dummy', 'album')
	`, albumID, artistID); err != nil {
		t.Fatalf("seed album: %v", err)
	}

	queries := New(pool)
	firstPath, secondPath := `@@peer\Music\Dummy\01.flac`, `@@peer\Music\Dummy\02.flac`
	params := CreateDownloadRequestParams{
		AlbumID: albumID, Provider: "slskd", SourceUsername: "peer",
		SourceDirectory: `@@peer\Music\Dummy`,
		FileCount:       2, ExpectedTrackCount: 2, TotalSizeBytes: 300,
		Format: "flac", Score: 0.9,
		Files: []DownloadRequestFile{
			{Path: firstPath, Name: "01.flac", Extension: "flac", SizeBytes: 100},
			{Path: secondPath, Name: "02.flac", Extension: "flac", SizeBytes: 200},
		},
	}
	requestID, _, err := queries.CreateDownloadRequest(ctx, params)
	if err != nil {
		t.Fatalf("CreateDownloadRequest() error = %v", err)
	}

	started, err := queries.StartDownloadRequest(ctx, requestID)
	if err != nil {
		t.Fatalf("StartDownloadRequest() error = %v", err)
	}
	if started.Status != "started" || !started.StartedAt.Valid {
		t.Fatalf("started = %#v", started)
	}
	if started.TransferCount != 2 {
		t.Fatalf("transfer rows = %d, want one per requested file", started.TransferCount)
	}

	// A request already started cannot be started again, so a second click
	// cannot enqueue the same folder twice.
	if _, err := queries.StartDownloadRequest(ctx, requestID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("starting twice error = %v, want pgx.ErrNoRows", err)
	}

	// Partial progress leaves the request running.
	running, err := queries.RecordTransferProgress(ctx, requestID, []TransferProgress{
		{ProviderID: "t1", Path: firstPath, State: "completed", Detail: "Completed, Succeeded",
			SizeBytes: 100, TransferredBytes: 100},
		{ProviderID: "t2", Path: secondPath, State: "downloading", Detail: "InProgress",
			SizeBytes: 200, TransferredBytes: 50},
	})
	if err != nil {
		t.Fatalf("RecordTransferProgress() error = %v", err)
	}
	if running.Status != "started" {
		t.Fatalf("status = %q, want a request with a transfer still moving to stay started", running.Status)
	}
	if running.CompletedCount != 1 || running.TransferredBytes != 150 {
		t.Fatalf("progress = %#v", running)
	}

	// Everything completed settles the request.
	completed, err := queries.RecordTransferProgress(ctx, requestID, []TransferProgress{
		{ProviderID: "t2", Path: secondPath, State: "completed", Detail: "Completed, Succeeded",
			SizeBytes: 200, TransferredBytes: 200},
	})
	if err != nil {
		t.Fatalf("RecordTransferProgress() error = %v", err)
	}
	if completed.Status != "completed" || completed.CompletedCount != 2 || completed.ErrorMessage.Valid {
		t.Fatalf("completed = %#v", completed)
	}

	// A settled request is not polled or started again.
	stillStarted, err := queries.StartedDownloadRequests(ctx)
	if err != nil {
		t.Fatalf("StartedDownloadRequests() error = %v", err)
	}
	if len(stillStarted) != 0 {
		t.Fatalf("%d settled requests are still being followed", len(stillStarted))
	}

	// A completed request no longer blocks the folder, and a failing one records
	// why it failed.
	retryID, created, err := queries.CreateDownloadRequest(ctx, params)
	if err != nil || !created {
		t.Fatalf("re-request after completion = %v/%v", created, err)
	}
	if _, err := queries.StartDownloadRequest(ctx, retryID); err != nil {
		t.Fatalf("StartDownloadRequest() error = %v", err)
	}
	failed, err := queries.RecordTransferProgress(ctx, retryID, []TransferProgress{
		{ProviderID: "t3", Path: firstPath, State: "completed", Detail: "Completed, Succeeded",
			SizeBytes: 100, TransferredBytes: 100},
		{ProviderID: "t4", Path: secondPath, State: "failed", Detail: "Completed, Errored: peer went offline"},
	})
	if err != nil {
		t.Fatalf("RecordTransferProgress() error = %v", err)
	}
	if failed.Status != "failed" || failed.FailedCount != 1 {
		t.Fatalf("failed = %#v", failed)
	}
	if !failed.ErrorMessage.Valid || !strings.Contains(failed.ErrorMessage.String, "peer went offline") {
		t.Fatalf("failure detail = %#v", failed.ErrorMessage)
	}

	// Reverting a start puts the request back where it was, with the reason.
	revertID, _, err := queries.CreateDownloadRequest(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.StartDownloadRequest(ctx, revertID); err != nil {
		t.Fatal(err)
	}
	reverted, err := queries.RevertDownloadRequestStart(ctx, revertID, "slskd rejected the API key")
	if err != nil {
		t.Fatalf("RevertDownloadRequestStart() error = %v", err)
	}
	if reverted.Status != "requested" || reverted.StartedAt.Valid || reverted.TransferCount != 0 {
		t.Fatalf("reverted = %#v", reverted)
	}
	if reverted.ErrorMessage.String != "slskd rejected the API key" {
		t.Fatalf("reverted reason = %#v", reverted.ErrorMessage)
	}

	// Cancelling a started request stops its unsettled transfers too, and the
	// identifiers needed to stop them at the provider are readable first.
	cancelID, _, err := queries.CreateDownloadRequest(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.StartDownloadRequest(ctx, cancelID); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.RecordTransferProgress(ctx, cancelID, []TransferProgress{
		{ProviderID: "t5", Path: firstPath, State: "downloading", Detail: "InProgress",
			SizeBytes: 100, TransferredBytes: 10},
	}); err != nil {
		t.Fatal(err)
	}
	transferIDs, err := queries.RequestedTransferIDs(ctx, cancelID)
	if err != nil {
		t.Fatalf("RequestedTransferIDs() error = %v", err)
	}
	if len(transferIDs) != 1 || transferIDs[0] != "t5" {
		t.Fatalf("transfer IDs = %#v", transferIDs)
	}
	cancelled, err := queries.CancelDownloadRequest(ctx, cancelID)
	if err != nil {
		t.Fatalf("CancelDownloadRequest() error = %v", err)
	}
	if cancelled.Status != "cancelled" || !cancelled.CancelledAt.Valid {
		t.Fatalf("cancelled = %#v", cancelled)
	}
	var unsettled int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM downloads
		WHERE download_request_id = $1 AND status NOT IN ('completed', 'failed', 'cancelled')
	`, cancelID).Scan(&unsettled); err != nil {
		t.Fatal(err)
	}
	if unsettled != 0 {
		t.Fatalf("%d transfers survived the cancellation", unsettled)
	}

	// A request stored before remote paths existed cannot be started.
	legacy := params
	legacy.SourceUsername = "legacy-peer"
	legacy.Files = []DownloadRequestFile{{Name: "01.flac", SizeBytes: 100}}
	legacyID, _, err := queries.CreateDownloadRequest(ctx, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.StartDownloadRequest(ctx, legacyID); !errors.Is(err, ErrNoRemotePath) {
		t.Fatalf("legacy start error = %v, want ErrNoRemotePath", err)
	}
	unchanged, err := queries.DownloadRequest(ctx, legacyID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Status != "requested" || unchanged.TransferCount != 0 {
		t.Fatalf("a request that could not start was changed anyway: %#v", unchanged)
	}

	// The poller is queued at most once.
	for attempt := 0; attempt < 3; attempt++ {
		if err := queries.QueueDownloadPoll(ctx, time.Now()); err != nil {
			t.Fatalf("QueueDownloadPoll() error = %v", err)
		}
	}
	var pollJobs int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE kind = 'poll_downloads' AND status IN ('queued', 'running')
	`).Scan(&pollJobs); err != nil {
		t.Fatal(err)
	}
	if pollJobs != 1 {
		t.Fatalf("%d pollers are queued, want exactly one", pollJobs)
	}
}

// TestDownloadRetryLifecycle follows one request from a partial failure through
// a retry of the file that did not arrive. It is written as a lifecycle, like
// the transfer test above, because every step depends on the state the previous
// one left behind.
func TestDownloadRetryLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID, albumID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name)
		VALUES ($1, $2, 'Boards of Canada', 'Boards of Canada')
	`, artistID, uuid.New()); err != nil {
		t.Fatalf("seed artist: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, title, album_type)
		VALUES ($1, $2, 'Geogaddi', 'album')
	`, albumID, artistID); err != nil {
		t.Fatalf("seed album: %v", err)
	}

	queries := New(pool)
	firstPath, secondPath := `@@peer\Music\Geogaddi\01.flac`, `@@peer\Music\Geogaddi\02.flac`
	params := CreateDownloadRequestParams{
		AlbumID: albumID, Provider: "slskd", SourceUsername: "peer",
		SourceDirectory: `@@peer\Music\Geogaddi`,
		FileCount:       2, ExpectedTrackCount: 2, TotalSizeBytes: 300,
		Format: "flac", Score: 0.9,
		Files: []DownloadRequestFile{
			{Path: firstPath, Name: "01.flac", Extension: "flac", SizeBytes: 100},
			{Path: secondPath, Name: "02.flac", Extension: "flac", SizeBytes: 200},
		},
	}
	requestID, _, err := queries.CreateDownloadRequest(ctx, params)
	if err != nil {
		t.Fatalf("CreateDownloadRequest() error = %v", err)
	}
	if _, err := queries.StartDownloadRequest(ctx, requestID); err != nil {
		t.Fatalf("StartDownloadRequest() error = %v", err)
	}

	// A request that is still moving has nothing to retry yet.
	if _, _, err := queries.RetryDownloadRequestFiles(ctx, requestID, nil); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("retrying a running request = %v, want pgx.ErrNoRows", err)
	}

	failed, err := queries.RecordTransferProgress(ctx, requestID, []TransferProgress{
		{ProviderID: "t1", Path: firstPath, State: "completed", Detail: "Completed, Succeeded",
			SizeBytes: 100, TransferredBytes: 100},
		{ProviderID: "t2", Path: secondPath, State: "failed", Detail: "Completed, Errored: peer went offline"},
	})
	if err != nil {
		t.Fatalf("RecordTransferProgress() error = %v", err)
	}
	if failed.Status != "failed" {
		t.Fatalf("status = %q, want a settled failure", failed.Status)
	}
	// The request can say which file failed, not only how many did.
	if len(failed.Transfers) != 2 {
		t.Fatalf("transfers = %#v", failed.Transfers)
	}
	if state := transferByPath(failed.Transfers, secondPath); state.Status != "failed" ||
		!strings.Contains(state.Error, "peer went offline") {
		t.Fatalf("failed transfer = %#v", state)
	}

	// A file that arrived is not a file to retry.
	if _, _, err := queries.RetryDownloadRequestFiles(
		ctx, requestID, []string{firstPath},
	); !errors.Is(err, ErrNotRetryable) {
		t.Fatalf("retrying a completed file = %v, want ErrNotRetryable", err)
	}
	refused, err := queries.DownloadRequest(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if refused.Status != "failed" {
		t.Fatalf("a refused retry changed the request to %q", refused.Status)
	}

	retried, files, err := queries.RetryDownloadRequestFiles(ctx, requestID, []string{secondPath})
	if err != nil {
		t.Fatalf("RetryDownloadRequestFiles() error = %v", err)
	}
	if len(files) != 1 || files[0].Path != secondPath {
		t.Fatalf("files to ask for = %#v, want only the one that failed", files)
	}
	if retried.Status != "started" {
		t.Fatalf("status = %q, want the request following its retry again", retried.Status)
	}
	// The file that arrived keeps its completed transfer: retrying one file must
	// not cost the peer the upload of the other.
	if state := transferByPath(retried.Transfers, firstPath); state.Status != "completed" ||
		state.TransferredBytes != 100 {
		t.Fatalf("the completed transfer was disturbed: %#v", state)
	}
	// The requeued file keeps the reason it failed, because until the provider
	// reports something newer that is still all anyone knows about it.
	if state := transferByPath(retried.Transfers, secondPath); state.Status != "queued" ||
		!strings.Contains(state.Error, "peer went offline") {
		t.Fatalf("requeued transfer = %#v", state)
	}
	// Retrying updates the row it already has; a second one would break the
	// unique index that keeps one transfer per remote path.
	if retried.TransferCount != 2 {
		t.Fatalf("transfer rows = %d, want the two the request started with", retried.TransferCount)
	}
	if retried.ErrorMessage.Valid {
		t.Fatalf("a request being retried still reports %q", retried.ErrorMessage.String)
	}

	// A provider that refuses leaves the request failed with its reason, rather
	// than started with transfers nobody is running.
	reverted, err := queries.RevertDownloadRequestRetry(
		ctx, requestID, []string{secondPath}, "peer is offline")
	if err != nil {
		t.Fatalf("RevertDownloadRequestRetry() error = %v", err)
	}
	if reverted.Status != "failed" || reverted.ErrorMessage.String != "peer is offline" {
		t.Fatalf("reverted = %#v", reverted)
	}
	if state := transferByPath(reverted.Transfers, secondPath); state.Status != "failed" {
		t.Fatalf("a reverted retry left the transfer %q", state.Status)
	}
	if state := transferByPath(reverted.Transfers, firstPath); state.Status != "completed" {
		t.Fatalf("a reverted retry disturbed the completed transfer: %#v", state)
	}

	// A retry that the provider accepts and that then completes settles the whole
	// request, and the import it was always meant to have is queued exactly once.
	if _, _, err := queries.RetryDownloadRequestFiles(ctx, requestID, nil); err != nil {
		t.Fatalf("RetryDownloadRequestFiles() error = %v", err)
	}
	completed, err := queries.RecordTransferProgress(ctx, requestID, []TransferProgress{
		{ProviderID: "t3", Path: secondPath, State: "completed", Detail: "Completed, Succeeded",
			SizeBytes: 200, TransferredBytes: 200},
	})
	if err != nil {
		t.Fatalf("RecordTransferProgress() error = %v", err)
	}
	if completed.Status != "completed" || completed.CompletedCount != 2 {
		t.Fatalf("completed = %#v", completed)
	}
	if completed.ImportStatus != "pending" {
		t.Fatalf("import status = %q, want a retried request still waiting to be imported",
			completed.ImportStatus)
	}
	if err := queries.QueueDownloadImport(ctx, requestID); err != nil {
		t.Fatalf("QueueDownloadImport() error = %v", err)
	}
	var importJobs int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'import_download' AND payload->>'requestId' = $1
	`, requestID.String()).Scan(&importJobs); err != nil {
		t.Fatal(err)
	}
	if importJobs != 1 {
		t.Fatalf("%d imports queued for a retried request, want exactly one", importJobs)
	}

	// Nothing failed any more, so there is nothing left to retry.
	if _, _, err := queries.RetryDownloadRequestFiles(ctx, requestID, nil); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("retrying a completed request = %v, want pgx.ErrNoRows", err)
	}
}

// A want follows one copy at a time. Its failed request is one the loop was
// free to pass over, so by the time somebody presses retry another copy can
// already be on its way, and sending this one back to 'started' would be two
// open requests for one want.
func TestRetryingAWantsFailedRequestIsRefusedWhileItFollowsAnotherCopy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	targetID := seedWant(ctx, t, queries)
	firstPath := `@@peer\Music\01.flac`
	first := seedFetchedCopy(ctx, t, queries, targetID, "peer", firstPath)
	failTransfer(ctx, t, queries, first, "peer", firstPath)
	// The loop passes over a request that has settled and follows the next offer.
	seedFetchedCopy(ctx, t, queries, targetID, "other", `@@other\Music\01.flac`)

	if _, _, err := queries.RetryDownloadRequestFiles(ctx, first, nil); !errors.Is(
		err, ErrWantHasAnotherCopy,
	) {
		t.Fatalf("retrying = %v, want ErrWantHasAnotherCopy", err)
	}
	refused, err := queries.DownloadRequest(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if refused.Status != "failed" {
		t.Fatalf("a refused retry left the request %q, want it still failed", refused.Status)
	}
}

// With nothing else in flight for the want, its failed request is retried
// exactly as a release chosen by hand is.
func TestAWantsFailedRequestIsRetriedWhenItFollowsNothingElse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	path := `@@peer\Music\01.flac`
	requestID := seedFetchedCopy(ctx, t, queries, seedWant(ctx, t, queries), "peer", path)
	failTransfer(ctx, t, queries, requestID, "peer", path)

	retried, files, err := queries.RetryDownloadRequestFiles(ctx, requestID, nil)
	if err != nil {
		t.Fatalf("RetryDownloadRequestFiles() error = %v", err)
	}
	if len(files) != 1 || files[0].Path != path {
		t.Fatalf("files to ask for = %#v, want the copy that did not arrive", files)
	}
	if retried.Status != "started" {
		t.Fatalf("status = %q, want the request following its retry again", retried.Status)
	}
}

// A want stops waiting for a copy the moment somebody settles it, withdraws it
// or supersedes it, and any of those can land while a pass is out at a provider
// choosing one. The request is refused rather than written: nobody is waiting
// for the music any more, and when what settled the want was a copy, this one
// would be the second on the disc.
func TestFetchingACopyIsRefusedForAWantThatHasStoppedWaiting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	targetID := seedWant(ctx, t, queries)
	// The person who asked for it has said they no longer want it.
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets
		SET status = 'not_wanted', not_wanted_at = now(), next_attempt_at = NULL
		WHERE id = $1
	`, targetID); err != nil {
		t.Fatal(err)
	}

	_, err := queries.FetchAcquiredFile(ctx, FetchAcquiredFileParams{
		AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
		SourceDirectory: `@@peer\Music`, RemotePath: `@@peer\Music\01.flac`,
		FileName: "01.flac", Extension: "flac", SizeBytes: 100, Score: 0.9,
		Summary: "fetching",
	})
	if !errors.Is(err, ErrWantHasMovedOn) {
		t.Fatalf("fetching for a withdrawn want = %v, want ErrWantHasMovedOn", err)
	}
	// The request and the offer are written together, so a refusal leaves neither.
	for _, table := range []string{"download_requests", "acquisition_target_files"} {
		var rows int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM `+table+` WHERE acquisition_target_id = $1`,
			targetID).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 0 {
			t.Errorf("%d rows in %s, want a refused fetch to write nothing", rows, table)
		}
	}
}

// Two passes can choose a copy for one want at the same time — its own turn and
// a sibling's harvest reaching it. The index behind one open request per want is
// what catches the second, and what it means is what a read would have said, so
// it is answered in those words rather than as a constraint nobody can read.
func TestFetchingASecondCopyIsRefusedWhileAWantAlreadyFollowsOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	targetID := seedWant(ctx, t, queries)
	first := seedFetchedCopy(ctx, t, queries, targetID, "peer", `@@peer\Music\01.flac`)

	_, err := queries.FetchAcquiredFile(ctx, FetchAcquiredFileParams{
		AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "other",
		SourceDirectory: `@@other\Music`, RemotePath: `@@other\Music\01.flac`,
		FileName: "01.flac", Extension: "flac", SizeBytes: 100, Score: 0.9,
		Summary: "fetching",
	})
	if !errors.Is(err, ErrWantHasAnotherCopy) {
		t.Fatalf("fetching a second copy = %v, want ErrWantHasAnotherCopy", err)
	}
	var requests []uuid.UUID
	rows, err := pool.Query(ctx,
		`SELECT id FROM download_requests WHERE acquisition_target_id = $1`, targetID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, id)
	}
	if len(requests) != 1 || requests[0] != first {
		t.Errorf("requests = %v, want only the copy the want was already following",
			requests)
	}
}

// seedWant records a want with a recording already resolved, which is what a
// copy is fetched against.
func seedWant(ctx context.Context, t *testing.T, queries *Queries) uuid.UUID {
	t.Helper()

	targetID, _, err := queries.CreateAcquisitionTarget(ctx, CreateAcquisitionTargetParams{
		Origin:                 "manual",
		EntryArtist:            "Portishead",
		EntryTitle:             "Glory Box",
		MusicBrainzRecordingID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
		Summary:                "wanted",
	})
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}
	return targetID
}

// seedFetchedCopy records one copy chosen for a want and starts its transfer,
// which is what the loop leaves behind for every candidate it follows.
func seedFetchedCopy(
	ctx context.Context, t *testing.T, queries *Queries, targetID uuid.UUID, peer, path string,
) uuid.UUID {
	t.Helper()

	requestID, err := queries.FetchAcquiredFile(ctx, FetchAcquiredFileParams{
		AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: peer,
		SourceDirectory: `@@` + peer + `\Music`, RemotePath: path,
		FileName: "01.flac", Extension: "flac", SizeBytes: 100, Score: 0.9,
		Summary: "fetching",
	})
	if err != nil {
		t.Fatalf("FetchAcquiredFile() error = %v", err)
	}
	if _, err := queries.StartDownloadRequest(ctx, requestID); err != nil {
		t.Fatalf("StartDownloadRequest() error = %v", err)
	}
	return requestID
}

// failTransfer settles a started transfer with the reason a peer that went away
// gives, which is the state the retry button is offered against.
func failTransfer(
	ctx context.Context, t *testing.T, queries *Queries, requestID uuid.UUID, peer, path string,
) {
	t.Helper()

	settled, err := queries.RecordTransferProgress(ctx, requestID, []TransferProgress{
		{ProviderID: peer + "-t1", Path: path, State: "failed",
			Detail: "Completed, Errored: peer went offline"},
	})
	if err != nil {
		t.Fatalf("RecordTransferProgress() error = %v", err)
	}
	if settled.Status != "failed" {
		t.Fatalf("status = %q, want a settled failure", settled.Status)
	}
}

// TestTransferHistoryKeepsWhatEachAttemptCameTo follows one file that takes
// three tries to arrive. The transfer row only ever holds the current state, so
// without the history a file that eventually arrives is indistinguishable from
// one that arrived first time, and the reasons the earlier attempts gave — the
// evidence for giving up on this peer — are gone.
func TestTransferHistoryKeepsWhatEachAttemptCameTo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	queries := New(pool)
	path := `@@peer\Music\Amnesiac\01.flac`
	requestID := seedSingleFileRequest(ctx, t, pool, queries, path)

	if _, err := queries.RecordTransferProgress(ctx, requestID, []TransferProgress{
		{ProviderID: "t1", Path: path, State: "failed", Detail: "Completed, Errored: peer is offline"},
	}); err != nil {
		t.Fatalf("RecordTransferProgress() error = %v", err)
	}
	if _, _, err := queries.RetryDownloadRequestFiles(ctx, requestID, nil); err != nil {
		t.Fatalf("RetryDownloadRequestFiles() error = %v", err)
	}
	if _, err := queries.RecordTransferProgress(ctx, requestID, []TransferProgress{
		{ProviderID: "t2", Path: path, State: "failed", Detail: "Completed, TimedOut"},
	}); err != nil {
		t.Fatalf("RecordTransferProgress() error = %v", err)
	}
	if _, _, err := queries.RetryDownloadRequestFiles(ctx, requestID, nil); err != nil {
		t.Fatalf("RetryDownloadRequestFiles() error = %v", err)
	}
	settled, err := queries.RecordTransferProgress(ctx, requestID, []TransferProgress{
		{ProviderID: "t3", Path: path, State: "completed", Detail: "Completed, Succeeded",
			SizeBytes: 100, TransferredBytes: 100},
	})
	if err != nil {
		t.Fatalf("RecordTransferProgress() error = %v", err)
	}

	transfer := transferByPath(settled.Transfers, path)
	if transfer.Status != "completed" {
		t.Fatalf("status = %q, want the file that finally arrived", transfer.Status)
	}
	if transfer.Attempt != 3 {
		t.Fatalf("attempt = %d, want the third try", transfer.Attempt)
	}
	if len(transfer.Attempts) != 3 {
		t.Fatalf("attempts = %#v, want one entry per try", transfer.Attempts)
	}
	// Oldest first, each in the provider's own words rather than paraphrased.
	if got := transfer.Attempts[0]; got.Attempt != 1 || got.Outcome != "failed" ||
		!strings.Contains(got.Detail, "peer is offline") {
		t.Fatalf("first attempt = %#v", got)
	}
	if got := transfer.Attempts[1]; got.Attempt != 2 || got.Outcome != "failed" ||
		!strings.Contains(got.Detail, "TimedOut") {
		t.Fatalf("second attempt = %#v", got)
	}
	if got := transfer.Attempts[2]; got.Attempt != 3 || got.Outcome != "completed" ||
		got.TransferredBytes != 100 {
		t.Fatalf("third attempt = %#v", got)
	}
}

// TestTransferHistoryRecordsOneOutcomePerAttempt pins the idempotency the
// poller depends on. slskd keeps reporting a settled transfer until it forgets
// it, so every pass after the first sees the same outcome again; a history that
// grew a row each time would report one failure as a dozen.
func TestTransferHistoryRecordsOneOutcomePerAttempt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	queries := New(pool)
	path := `@@peer\Music\Amnesiac\01.flac`
	requestID := seedSingleFileRequest(ctx, t, pool, queries, path)

	progress := []TransferProgress{
		{ProviderID: "t1", Path: path, State: "failed", Detail: "Completed, Errored: peer is offline"},
	}
	for range 3 {
		if _, err := queries.RecordTransferProgress(ctx, requestID, progress); err != nil {
			t.Fatalf("RecordTransferProgress() error = %v", err)
		}
	}

	row, err := queries.DownloadRequest(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if attempts := transferByPath(row.Transfers, path).Attempts; len(attempts) != 1 {
		t.Fatalf("attempts = %#v, want the one failure that happened", attempts)
	}
}

// Two clicks on the same candidate close enough together overlap in the
// database: the loser blocks on the open-source index until the winner commits.
// It must then be told about the winner's request, because a request it can see
// in the interface a moment later cannot read as an internal error.
func TestTwoCallersAskingForOneFolderAtOnceGetOneRequest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	params := seedRequestParams(ctx, t, pool)

	transaction, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	winner, created, err := New(transaction).CreateDownloadRequest(ctx, params)
	if err != nil || !created {
		t.Fatalf("CreateDownloadRequest() = %v, %v, %v", winner, created, err)
	}

	type answer struct {
		id      uuid.UUID
		created bool
		err     error
	}
	loser := make(chan answer, 1)
	go func() {
		id, created, err := New(pool).CreateDownloadRequest(ctx, params)
		loser <- answer{id, created, err}
	}()

	waitForBlockedInsert(ctx, t, pool, "download_requests")
	if err := transaction.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	second := <-loser
	if second.err != nil {
		t.Fatalf("CreateDownloadRequest() error = %v", second.err)
	}
	if second.created {
		t.Fatal("both callers recorded a request for the same folder")
	}
	if second.id != winner {
		t.Fatalf("second caller was told about %s, want the recorded request %s", second.id, winner)
	}
}

// A started request is still open — the index says so — so requesting its
// folder again is the same repeat click, and gets the same answer.
func TestRequestingAFolderWhoseTransferHasStartedReturnsThatRequest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	params := seedRequestParams(ctx, t, pool)

	id, _, err := queries.CreateDownloadRequest(ctx, params)
	if err != nil {
		t.Fatalf("CreateDownloadRequest() error = %v", err)
	}
	if _, err := queries.StartDownloadRequest(ctx, id); err != nil {
		t.Fatalf("StartDownloadRequest() error = %v", err)
	}

	repeatID, repeatCreated, err := queries.CreateDownloadRequest(ctx, params)
	if err != nil {
		t.Fatalf("repeated CreateDownloadRequest() error = %v", err)
	}
	if repeatCreated || repeatID != id {
		t.Fatalf("repeat = %v/%v, want the started request %v reused", repeatID, repeatCreated, id)
	}
}

// seedRequestParams seeds a release and describes one folder for it, so a test
// about requesting the same folder twice has one thing to request.
func seedRequestParams(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool,
) CreateDownloadRequestParams {
	t.Helper()

	artistID, albumID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name)
		VALUES ($1, $2, 'Portishead', 'Portishead')
	`, artistID, uuid.New()); err != nil {
		t.Fatalf("seed artist: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, title, album_type)
		VALUES ($1, $2, 'Dummy', 'album')
	`, albumID, artistID); err != nil {
		t.Fatalf("seed album: %v", err)
	}
	return CreateDownloadRequestParams{
		AlbumID: albumID, Provider: "slskd", SourceUsername: "peer",
		SourceDirectory: `@@peer\Music\Dummy`,
		FileCount:       1, ExpectedTrackCount: 1, TotalSizeBytes: 100,
		Format: "flac", Score: 0.9,
		Files: []DownloadRequestFile{
			{Path: `@@peer\Music\Dummy\01.flac`, Name: "01.flac", Extension: "flac", SizeBytes: 100},
		},
	}
}

// seedRequestInState records one request against the seeded release and puts it
// in the state the test is about: what became of the transfer, what became of
// the copy afterwards, and when it was asked for. Each one takes its own folder,
// because two open requests for one folder are refused by the database.
func seedRequestInState(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, queries *Queries,
	params CreateDownloadRequestParams, folder, status, importStatus string, requestedAt time.Time,
) uuid.UUID {
	t.Helper()

	params.SourceDirectory = folder
	id, created, err := queries.CreateDownloadRequest(ctx, params)
	if err != nil {
		t.Fatalf("CreateDownloadRequest() error = %v", err)
	}
	if !created {
		t.Fatalf("folder %q reused an earlier request", folder)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE download_requests
		SET status = $2,
		    cancelled_at = CASE WHEN $2 = 'cancelled' THEN now() END,
		    import_status = $3,
		    imported_at = CASE WHEN $3 = 'imported' THEN now() END,
		    import_path = CASE WHEN $3 = 'imported' THEN '/music/imported.flac' END,
		    requested_at = $4
		WHERE id = $1
	`, id, status, importStatus, requestedAt); err != nil {
		t.Fatalf("set request state: %v", err)
	}
	return id
}

// The Downloads screen reads the list one pile at a time. The piles are why a
// transfer under way cannot go missing: the list is ordered by when each request
// was asked for, and the screen used to read the hundred most recent rows and
// keep the open ones out of those — so an open transfer older than a hundred
// settled ones was on no page the screen could reach. Asking for the pile puts
// it on the first page, however old it is.
func TestAnOpenRequestIsOnTheFirstPageHoweverOldItIs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	params := seedRequestParams(ctx, t, pool)

	buried := seedRequestInState(ctx, t, pool, queries, params,
		`@@peer\Music\Buried`, "started", "pending", time.Now().Add(-90*24*time.Hour))
	for index := range 30 {
		seedRequestInState(ctx, t, pool, queries, params,
			fmt.Sprintf(`@@peer\Music\Settled\%d`, index), "completed", "imported",
			time.Now().Add(-time.Duration(index)*time.Hour))
	}

	page, err := queries.ListDownloadRequests(ctx, ListDownloadRequestsParams{View: "open", Limit: 25})
	if err != nil {
		t.Fatalf("ListDownloadRequests() error = %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != buried {
		t.Fatalf("open page = %d items / total %d, want the one open request", len(page.Items), page.Total)
	}
	if page.Counts.Open != 1 || page.Counts.Imported != 30 || page.Counts.All != 31 {
		t.Fatalf("counts = %#v", page.Counts)
	}
}

// Each pile holds the requests it names and no others, so a figure beside a
// filter and the rows behind it are the same answer.
func TestEachViewOfTheDownloadListHoldsWhatItNames(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	params := seedRequestParams(ctx, t, pool)

	asked := time.Now().Add(-time.Hour)
	seedRequestInState(ctx, t, pool, queries, params, `@@peer\Music\A`, "requested", "pending", asked)
	seedRequestInState(ctx, t, pool, queries, params, `@@peer\Music\B`, "started", "pending", asked)
	seedRequestInState(ctx, t, pool, queries, params, `@@peer\Music\C`, "completed", "needs_review", asked)
	seedRequestInState(ctx, t, pool, queries, params, `@@peer\Music\D`, "completed", "imported", asked)
	seedRequestInState(ctx, t, pool, queries, params, `@@peer\Music\E`, "completed", "discarded", asked)
	seedRequestInState(ctx, t, pool, queries, params, `@@peer\Music\F`, "failed", "pending", asked)
	// Neither of these is in a named pile. A cancelled request is not a failure
	// and a copy still being read is not yet an outcome — both are reached
	// through "all", which is why it is offered.
	seedRequestInState(ctx, t, pool, queries, params, `@@peer\Music\G`, "cancelled", "pending", asked)
	seedRequestInState(ctx, t, pool, queries, params, `@@peer\Music\H`, "completed", "validating", asked)

	for view, want := range map[string]int64{
		"open": 2, "review": 1, "imported": 1, "discarded": 1, "failed": 1, "all": 8,
	} {
		page, err := queries.ListDownloadRequests(ctx, ListDownloadRequestsParams{View: view, Limit: 25})
		if err != nil {
			t.Fatalf("view %q: ListDownloadRequests() error = %v", view, err)
		}
		if page.Total != want || int64(len(page.Items)) != want {
			t.Errorf("view %q = %d items / total %d, want %d", view, len(page.Items), page.Total, want)
		}
	}

	page, err := queries.ListDownloadRequests(ctx, ListDownloadRequestsParams{Limit: 25})
	if err != nil {
		t.Fatalf("ListDownloadRequests() error = %v", err)
	}
	want := DownloadRequestCounts{Open: 2, Review: 1, Imported: 1, Discarded: 1, Failed: 1, All: 8}
	if page.Counts != want {
		t.Fatalf("counts = %#v, want %#v", page.Counts, want)
	}
}

// seedSingleFileRequest records and starts a one-file request, which is the
// shortest way to a transfer row the history can be read from.
func seedSingleFileRequest(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, queries *Queries, path string,
) uuid.UUID {
	t.Helper()

	artistID, albumID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name)
		VALUES ($1, $2, 'Radiohead', 'Radiohead')
	`, artistID, uuid.New()); err != nil {
		t.Fatalf("seed artist: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, title, album_type)
		VALUES ($1, $2, 'Amnesiac', 'album')
	`, albumID, artistID); err != nil {
		t.Fatalf("seed album: %v", err)
	}

	requestID, _, err := queries.CreateDownloadRequest(ctx, CreateDownloadRequestParams{
		AlbumID: albumID, Provider: "slskd", SourceUsername: "peer",
		SourceDirectory: `@@peer\Music\Amnesiac`,
		FileCount:       1, ExpectedTrackCount: 1, TotalSizeBytes: 100,
		Format: "flac", Score: 0.9,
		Files: []DownloadRequestFile{{Path: path, Name: "01.flac", Extension: "flac", SizeBytes: 100}},
	})
	if err != nil {
		t.Fatalf("CreateDownloadRequest() error = %v", err)
	}
	if _, err := queries.StartDownloadRequest(ctx, requestID); err != nil {
		t.Fatalf("StartDownloadRequest() error = %v", err)
	}
	return requestID
}

func transferByPath(transfers []FileTransfer, path string) FileTransfer {
	for _, transfer := range transfers {
		if transfer.Path == path {
			return transfer
		}
	}
	return FileTransfer{}
}

// A request serving a want is about no release at all, and every view that reads
// a request has to keep reporting it. The release join used to be an inner one,
// which is how a single-file acquisition would have disappeared from the
// Downloads view, the dashboard and the start path without anything failing.
func TestARequestForAWantIsReadWithTheEntryItServes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	targetID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets (
			id, origin, entry_artist, entry_title, musicbrainz_recording_id,
			status, resolution_method, resolved_at, next_attempt_at
		)
		VALUES ($1, 'playlist', 'Anetha', 'Candy from Strangers', $2,
		        'pending', 'isrc', now(), now())
	`, targetID, uuid.New()); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	requestID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO download_requests (
			id, acquisition_target_id, provider, source_username, source_directory,
			file_count, expected_track_count, total_size_bytes, format, score, files
		)
		VALUES ($1, $2, 'slskd', 'peer', 'Music/Anetha', 1, 1, 9_000_000, 'flac', 0.77,
		        '[{"name":"03 Candy from Strangers.flac","path":"Music\\Anetha\\03.flac"}]'::jsonb)
	`, requestID, targetID); err != nil {
		t.Fatalf("record a request for a want: %v", err)
	}

	queries := New(pool)
	stored, err := queries.DownloadRequest(ctx, requestID)
	if err != nil {
		t.Fatalf("DownloadRequest() error = %v", err)
	}
	if stored.AlbumID.Valid || stored.AlbumTitle != "" || stored.ArtistName != "" {
		t.Errorf("stored = %#v, want no release on a request that serves a want", stored)
	}
	if stored.AcquisitionTargetID.UUID != targetID {
		t.Errorf("target = %v, want the want it serves", stored.AcquisitionTargetID)
	}
	if stored.EntryArtist != "Anetha" || stored.EntryTitle != "Candy from Strangers" {
		t.Errorf("entry = %q — %q, want the words it was asked for in",
			stored.EntryArtist, stored.EntryTitle)
	}

	page, err := queries.ListDownloadRequests(ctx, ListDownloadRequestsParams{Limit: 10})
	if err != nil {
		t.Fatalf("ListDownloadRequests() error = %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != requestID {
		t.Fatalf("page = %+v, want the request listed", page)
	}

	// A transfer row takes its release from the request that asked for it, and
	// this request has none. The row is accounted for by the request itself.
	started, err := queries.StartDownloadRequest(ctx, requestID)
	if err != nil {
		t.Fatalf("StartDownloadRequest() error = %v", err)
	}
	if started.Status != "started" || started.TransferCount != 1 {
		t.Fatalf("started = %#v, want one transfer recorded", started)
	}
}

// One open request per want. A want is pursued one candidate at a time, so a
// second attempt while one is in flight finds the open one rather than starting
// a second transfer of the same music.
func TestAWantCannotHaveTwoOpenRequests(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	targetID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets (
			id, origin, entry_title, musicbrainz_recording_id, status,
			resolution_method, resolved_at, next_attempt_at
		)
		VALUES ($1, 'manual', 'Candy from Strangers', $2, 'pending', 'isrc', now(), now())
	`, targetID, uuid.New()); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	record := func(directory string) error {
		_, err := pool.Exec(ctx, `
			INSERT INTO download_requests (
				acquisition_target_id, provider, source_username, source_directory,
				file_count, expected_track_count, total_size_bytes, format, score
			)
			VALUES ($1, 'slskd', 'peer', $2, 1, 1, 9000000, 'flac', 0.5)
		`, targetID, directory)
		return err
	}

	if err := record("Music/Anetha"); err != nil {
		t.Fatalf("first request: %v", err)
	}
	if err := record("Music/Somebody Else"); err == nil {
		t.Fatal("a second open request for the same want was allowed")
	}
}

// A request is about a release or about a want. One that is about neither is
// provenance with no subject, and the table refuses it rather than leaving it to
// be noticed later.
func TestARequestAboutNothingIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	if _, err := pool.Exec(ctx, `
		INSERT INTO download_requests (
			provider, source_username, source_directory,
			file_count, expected_track_count, total_size_bytes, format, score
		)
		VALUES ('slskd', 'peer', 'Music/Nobody', 1, 1, 1000, 'flac', 0.5)
	`); err == nil {
		t.Fatal("a request about neither a release nor a want was recorded")
	}
}

// The evidence the user was shown and accepted is what allows the request to
// start at all, so it is stored with the request rather than answered for
// afterwards. Without it a request that duplicates owned music is refused until
// somebody answers for it.
func TestARequestRecordedWithAcknowledgedEvidenceIsAllowedToStart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID, albumID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, name, sort_name) VALUES ($1, 'Portishead', 'Portishead')
	`, artistID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, title) VALUES ($1, $2, 'Dummy')
	`, albumID, artistID); err != nil {
		t.Fatal(err)
	}

	queries := New(pool)
	requestID, created, err := queries.CreateDownloadRequest(ctx, CreateDownloadRequestParams{
		AlbumID: albumID, Provider: "slskd", SourceUsername: "peer",
		SourceDirectory: "Music/Dummy", FileCount: 1, ExpectedTrackCount: 1,
		TotalSizeBytes: 100, Format: "flac", Score: 0.9,
		Files: []DownloadRequestFile{{Path: `Music\Dummy\01.flac`, Name: "01.flac", SizeBytes: 100}},
		DuplicateEvidence: &DuplicateEvidence{
			AlbumID: albumID, AlbumTitle: "Dummy", ArtistName: "Portishead",
			TrackCount: 11, OwnedTrackCount: 1,
			Summary: "The library already holds 1 of the 11 tracks of this release.",
			Files: []DuplicateFile{{
				Path: "/music/Dummy/01 Mysterons.flac", State: "owned", MatchStatus: "matched",
				Track: "1. Mysterons", Evidence: "is already matched by ISRC to 1. Mysterons.",
			}},
		},
	})
	if err != nil || !created {
		t.Fatalf("CreateDownloadRequest() = %v, %v, %v", requestID, created, err)
	}

	stored, err := queries.DownloadRequest(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.DuplicateAcknowledgedAt.Valid || stored.DuplicateEvidence == nil {
		t.Fatalf("request = %#v, want the answer recorded with it", stored)
	}
	if len(stored.DuplicateEvidence.Files) != 1 {
		t.Fatalf("evidence = %#v, want what the user was shown", stored.DuplicateEvidence)
	}
}

// A request covering no files has nothing to ask a peer for. Starting it would
// move it to 'started' and leave it there with no transfer to settle it.
func TestStartingARequestThatCoversNoFilesIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	targetID, requestID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets (
			id, origin, entry_artist, entry_title, musicbrainz_recording_id,
			status, resolution_method, resolved_at, next_attempt_at
		)
		VALUES ($1, 'manual', 'Portishead', 'Glory Box', $2, 'pending', 'manual', now(), now())
	`, targetID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO download_requests (
			id, acquisition_target_id, provider, source_username, source_directory,
			file_count, expected_track_count, total_size_bytes, format, score, files
		)
		VALUES ($1, $2, 'slskd', 'peer', 'Music/Dummy', 1, 1, 0, 'flac', 0.5, '[]'::jsonb)
	`, requestID, targetID); err != nil {
		t.Fatal(err)
	}

	if _, err := New(pool).StartDownloadRequest(ctx, requestID); err == nil {
		t.Fatal("a request covering no files was started")
	}

	var status string
	if err := pool.QueryRow(ctx, `
		SELECT status FROM download_requests WHERE id = $1
	`, requestID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "requested" {
		t.Fatalf("status = %q, want the request left where it was", status)
	}
}

// A request whose every file arrived has nothing to retry, whatever became of the
// request as a whole. Retrying nothing is refused rather than reported as a retry
// that covered no files.
func TestRetryingARequestWithNoFailedTransferIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, requestID := importedRelease(ctx, t, pool)
	if _, err := pool.Exec(ctx, `
		UPDATE download_requests SET status = 'failed' WHERE id = $1
	`, requestID); err != nil {
		t.Fatal(err)
	}

	_, _, err := queries.RetryDownloadRequestFiles(ctx, requestID, nil)
	if !errors.Is(err, ErrNothingToRetry) {
		t.Fatalf("RetryDownloadRequestFiles() error = %v, want ErrNothingToRetry", err)
	}
}
