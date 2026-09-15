package db

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
)

// insertJob puts one job in whatever state the test needs it in. Written out
// rather than driven through the worker because these are queries about the
// table, and every state a job can be left in has to be reachable directly.
func insertJob(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool,
	kind, status string, attempts int, runAfter time.Time, message any,
) uuid.UUID {
	t.Helper()
	jobID := uuid.New()
	var startedAt, completedAt any
	switch status {
	case "running":
		startedAt = time.Now()
	case "completed", "failed", "cancelled":
		startedAt, completedAt = time.Now(), time.Now()
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO jobs (
		    id, kind, status, payload, attempts, max_attempts, run_after,
		    started_at, completed_at, error_message
		)
		VALUES ($1::uuid, $2, $3, jsonb_build_object('albumId', ($1::uuid)::text),
		        $4, 3, $5, $6, $7, $8)
	`, jobID, kind, status, attempts, runAfter, startedAt, completedAt, message); err != nil {
		t.Fatal(err)
	}
	return jobID
}

func readJob(ctx context.Context, t *testing.T, pool *pgxpool.Pool, jobID uuid.UUID) JobRow {
	t.Helper()
	row, err := New(pool).Job(ctx, jobID)
	if err != nil {
		t.Fatalf("read job %s: %v", jobID, err)
	}
	return row
}

// The queue is what is running and what is waiting for a worker, and nothing
// else. A job that finished — however it finished — is not in the queue.
func TestTheActiveQueueHoldsOnlyWhatIsRunningAndWaiting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	now := time.Now()
	searchID := insertJob(ctx, t, pool, "search_album_sources", "running", 1, now, nil)
	scanID := insertJob(ctx, t, pool, "scan_library", "queued", 0, now.Add(time.Minute), nil)
	insertJob(ctx, t, pool, "refresh_artist_metadata", "completed", 1, now, nil)
	insertJob(ctx, t, pool, "ingest_release_group", "failed", 3, now, "fetch release group: 502")
	insertJob(ctx, t, pool, "search_album_sources", "cancelled", 2, now, "the run was called off")

	rows, err := New(pool).ListActiveJobs(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want only the running search and the queued scan", len(rows))
	}
	// Running first, then what is due soonest, which is the order the worker
	// itself claims in.
	if rows[0].ID != searchID || rows[1].ID != scanID {
		t.Fatalf("order = %s then %s, want the running job first", rows[0].ID, rows[1].ID)
	}
	if rows[0].Total != 2 {
		t.Fatalf("total = %d, want the whole queue counted alongside the page", rows[0].Total)
	}
}

// Terminal failure is a job that spent its attempts. Cancelling is not that:
// the work it belonged to was called off, and nobody is waiting for anybody to
// do something about it.
func TestOnlySpentJobsAreListedAsFailed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	now := time.Now()
	failedID := insertJob(ctx, t, pool, "ingest_release_group", "failed", 3, now,
		"fetch release group: 502")
	insertJob(ctx, t, pool, "search_album_sources", "cancelled", 2, now, "the run was called off")
	// A job waiting out its backoff carries the same kind of error and is not
	// finished with: it is still in the queue, and listing it here would ask for
	// a decision about something that is already going to happen.
	insertJob(ctx, t, pool, "refresh_artist_metadata", "queued", 1, now.Add(time.Minute),
		"fetch artist catalogue: 503")

	rows, err := New(pool).ListFailedJobs(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != failedID {
		t.Fatalf("rows = %+v, want only the job that spent its attempts", rows)
	}
	if rows[0].ErrorMessage.String != "fetch release group: 502" {
		t.Fatalf("error = %q, want the error that ended it", rows[0].ErrorMessage.String)
	}
	if len(rows[0].Payload) == 0 {
		t.Fatal("the payload was dropped; the error alone does not say what failed")
	}
}

// insertJobAbout puts one job in the queue carrying the payload the test cares
// about, which is the whole of what a subject is read from.
func insertJobAbout(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, kind, payload string,
) uuid.UUID {
	t.Helper()
	jobID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO jobs (id, kind, payload) VALUES ($1, $2, $3::jsonb)
	`, jobID, kind, payload); err != nil {
		t.Fatal(err)
	}
	return jobID
}

func subjectsOf(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, jobIDs ...uuid.UUID,
) map[uuid.UUID]JobSubjectsRow {
	t.Helper()
	rows, err := New(pool).JobSubjects(ctx, jobIDs)
	if err != nil {
		t.Fatalf("read job subjects: %v", err)
	}
	subjects := make(map[uuid.UUID]JobSubjectsRow, len(rows))
	for _, row := range rows {
		subjects[row.ID] = row
	}
	return subjects
}

// A payload of identifiers is what the worker needs and nothing anybody can
// read. The join back to what those identifiers name is the whole point of the
// query, and it is made for a page at a time.
func TestAJobIsNamedByWhatItsPayloadPointsAt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID, albumID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		WITH artist AS (
		    INSERT INTO artists (id, name, sort_name)
		    VALUES ($1, 'Talk Talk', 'Talk Talk')
		)
		INSERT INTO albums (id, artist_id, title) VALUES ($2, $1, 'Spirit of Eden')
	`, artistID, albumID); err != nil {
		t.Fatal(err)
	}
	searchID := insertJobAbout(ctx, t, pool, "search_album_sources",
		`{"runId": "`+uuid.New().String()+`", "albumId": "`+albumID.String()+`"}`)

	subject := subjectsOf(ctx, t, pool, searchID)[searchID]

	if subject.Subject != "Spirit of Eden" {
		t.Fatalf("subject = %q, want the release the search is for", subject.Subject)
	}
	if subject.SubjectArtist != "Talk Talk" {
		t.Fatalf("artist = %q, want who the release is by", subject.SubjectArtist)
	}
}

// The job table is never purged, so a failure outlives what it was about. A row
// whose subject has been deleted is answered with nothing rather than with the
// identifier, which would tell the reader only that Schall cannot name it.
func TestAJobWhoseSubjectHasBeenDeletedIsNotNamed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	albumID := uuid.New()
	refreshID := insertJobAbout(ctx, t, pool, "refresh_album_metadata",
		`{"albumId": "`+albumID.String()+`"}`)

	if _, named := subjectsOf(ctx, t, pool, refreshID)[refreshID]; named {
		t.Fatal("a job was named after a release the catalogue does not hold")
	}
}

// A sweep is about the whole installation, so its payload names nothing and
// there is nothing here for it. The row reads as its bare kind, which is all
// the queue ever knew about it.
func TestASweepHasNoSubject(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	sweepID := insertJobAbout(ctx, t, pool, "sweep_cover_art", `{}`)

	if _, named := subjectsOf(ctx, t, pool, sweepID)[sweepID]; named {
		t.Fatal("a sweep was given a subject it has no way of having")
	}
}

// How much of a release is coming is what tells a transfer worth waiting for
// from one worth looking into, and the request recorded it when somebody chose
// the copy.
func TestATransferIsNamedWithTheFilesItAgreedToTake(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID, albumID, requestID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		WITH artist AS (
		    INSERT INTO artists (id, name, sort_name)
		    VALUES ($1, 'Radiohead', 'Radiohead')
		), album AS (
		    INSERT INTO albums (id, artist_id, title) VALUES ($2, $1, 'Kid A')
		)
		INSERT INTO download_requests (
		    id, album_id, provider, source_username, source_directory,
		    file_count, expected_track_count, total_size_bytes, format, score
		)
		VALUES ($3, $2, 'slskd', 'peer', '/music/Kid A', 10, 10, 400, 'flac', 0.9)
	`, artistID, albumID, requestID); err != nil {
		t.Fatal(err)
	}
	transferID := insertJobAbout(ctx, t, pool, "import_download",
		`{"requestId": "`+requestID.String()+`"}`)

	subject := subjectsOf(ctx, t, pool, transferID)[transferID]

	if subject.Subject != "Kid A" || subject.SubjectFileCount != 10 {
		t.Fatalf("subject = %q of %d files, want Kid A of 10",
			subject.Subject, subject.SubjectFileCount)
	}
}

// A request fetching one file for a want has no release behind it at all, and
// what it is about is the entry somebody asked for.
func TestATransferForOneWantIsNamedByTheEntryThatAskedForIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	targetID, requestID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		WITH want AS (
		    INSERT INTO acquisition_targets (id, origin, entry_artist, entry_title)
		    VALUES ($1, 'playlist', 'Stereolab', 'Metronomic Underground')
		)
		INSERT INTO download_requests (
		    id, acquisition_target_id, provider, source_username, source_directory,
		    file_count, expected_track_count, total_size_bytes, format, score
		)
		VALUES ($2, $1, 'slskd', 'peer', '/music/Emperor Tomato Ketchup',
		        1, 1, 40, 'flac', 0.9)
	`, targetID, requestID); err != nil {
		t.Fatal(err)
	}
	transferID := insertJobAbout(ctx, t, pool, "import_download",
		`{"requestId": "`+requestID.String()+`"}`)

	subject := subjectsOf(ctx, t, pool, transferID)[transferID]

	if subject.Subject != "Metronomic Underground" || subject.SubjectArtist != "Stereolab" {
		t.Fatalf("subject = %q by %q, want the entry the want was created from",
			subject.Subject, subject.SubjectArtist)
	}
}

// An upload has no row anywhere — the job is the record of it — so its subject
// is read back out of the payload the browser's request wrote.
func TestAnUploadIsNamedByTheFilesItStaged(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	uploadID := insertJobAbout(ctx, t, pool, "import_upload",
		`{"uploadId": "`+uuid.New().String()+`", "files": ["01 The Rainbow.flac", "02 Eden.flac"]}`)

	subject := subjectsOf(ctx, t, pool, uploadID)[uploadID]

	if subject.Subject != "01 The Rainbow.flac" || subject.SubjectFileCount != 2 {
		t.Fatalf("subject = %q of %d files, want the first of the two staged",
			subject.Subject, subject.SubjectFileCount)
	}
}

// Two kinds may write different things under one payload key, and a cast
// evaluated on a row it was not written for would fail the whole page rather
// than leave one row unnamed. Every cast is guarded by the kind, so a payload
// that is not a UUID costs nothing but its own subject.
func TestAPayloadThatIsNotAnIdentifierDoesNotCostThePageItsSubjects(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, name, sort_name) VALUES ($1, 'Broadcast', 'Broadcast')
	`, artistID); err != nil {
		t.Fatal(err)
	}
	refreshID := insertJobAbout(ctx, t, pool, "refresh_artist_metadata",
		`{"artistId": "`+artistID.String()+`"}`)
	// A kind that has never held an identifier under this key, which is what
	// makes the cast a question about the kind rather than about the value.
	oddID := insertJobAbout(ctx, t, pool, "tag_library", `{"artistId": "every one of them"}`)

	subjects := subjectsOf(ctx, t, pool, refreshID, oddID)

	if subjects[refreshID].Subject != "Broadcast" {
		t.Fatalf("subject = %q, want the artist the refresh is for",
			subjects[refreshID].Subject)
	}
	if _, named := subjects[oddID]; named {
		t.Fatal("a kind that names nothing was named from another kind's key")
	}
}

// The next run is the queued row's own due time. Nothing here works one out
// from an interval, because the sweeps schedule themselves and a sweep nobody
// queued has no next time at all.
func TestTheRecurringPulseReadsTheQueuedRowRatherThanAnInterval(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	due := time.Now().Add(6 * time.Hour)
	insertJob(ctx, t, pool, "sweep_cover_art", "completed", 1, time.Now(), nil)
	insertJob(ctx, t, pool, "sweep_cover_art", "queued", 0, due, nil)
	// A sweep whose last pass failed still ran. The two are told apart so that a
	// pass which has been failing for days does not read as one that ran an hour
	// ago and worked.
	insertJob(ctx, t, pool, "sweep_follow_feed", "failed", 3, time.Now(), "sweep follow feed: 503")

	rows, err := New(pool).RecurringJobPulse(ctx,
		[]string{"poll_downloads", "sweep_acquisition_targets", "sweep_follow_feed", "sweep_cover_art"})
	if err != nil {
		t.Fatal(err)
	}
	pulse := make(map[string]RecurringJobPulseRow, len(rows))
	for _, row := range rows {
		pulse[row.Kind] = row
	}
	if len(pulse) != 2 {
		t.Fatalf("kinds = %d, want only the two the queue holds anything for", len(pulse))
	}
	covers := pulse["sweep_cover_art"]
	if !covers.NextRunAfter.Valid || covers.NextRunAfter.Time.Sub(due).Abs() > time.Second {
		t.Fatalf("cover art next = %v, want the queued row's own time %v",
			covers.NextRunAfter, due)
	}
	if !covers.LastCompletedAt.Valid {
		t.Fatal("cover art reported no last successful pass")
	}
	feed := pulse["sweep_follow_feed"]
	if !feed.LastFinishedAt.Valid {
		t.Fatal("the follow feed's failed pass was not counted as having run")
	}
	if feed.LastCompletedAt.Valid {
		t.Fatal("a failed pass was reported as a successful one")
	}
	if feed.NextRunAfter.Valid {
		t.Fatalf("follow feed next = %v, want nothing rather than a guess", feed.NextRunAfter)
	}
}

// The claim this whole endpoint rests on: a retried job and a job the recovery
// pass picked up after a shutdown arrive back in the queue by one statement,
// waiting again with nothing left of the run that did not finish them, each
// still saying for itself why it is there.
//
// What they do not share is the ladder, which the two tests after this one hold
// apart.
func TestARetriedJobAndAnInterruptedOneBothReturnToTheQueue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	failedID := insertJob(ctx, t, pool, "ingest_release_group", "failed", 3, time.Now(),
		"fetch release group: 502")
	interruptedID := insertJob(ctx, t, pool, "scan_library", "running", 1, time.Now(), nil)

	requeued, wasRequeued, err := New(pool).RequeueFailedJob(ctx, failedID)
	if err != nil {
		t.Fatal(err)
	}
	if !wasRequeued {
		t.Fatal("a failed job was not reported as having been put back")
	}
	if err := New(pool).RequeueInterruptedJobs(ctx); err != nil {
		t.Fatal(err)
	}
	recovered := readJob(ctx, t, pool, interruptedID)

	for _, job := range []JobRow{requeued, recovered} {
		if job.Status != "queued" {
			t.Fatalf("status = %q, want the job waiting again", job.Status)
		}
		if job.StartedAt.Valid || job.CompletedAt.Valid {
			t.Fatalf("job %s kept traces of the run that did not finish it: %+v", job.ID, job)
		}
	}
	if requeued.ErrorMessage.String != "fetch release group: 502" {
		t.Fatalf("error = %q, want the reason it failed left where it is",
			requeued.ErrorMessage.String)
	}
	if recovered.ErrorMessage.String != interruptedNote {
		t.Fatalf("error = %q, want %q", recovered.ErrorMessage.String, interruptedNote)
	}
}

// A retry is new work rather than a wounded version of the old work, so the
// attempts go back to nothing and the whole ladder is ahead of it again. One
// more run on a job that already failed five times would usually fail again
// within seconds, and read as though the button had done nothing.
func TestARetryStartsTheAttemptLadderAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	jobID := insertJob(ctx, t, pool, "ingest_release_group", "failed", 5, time.Now(),
		"fetch release group: 502")

	requeued, _, err := New(pool).RequeueFailedJob(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}

	if requeued.Attempts != 0 {
		t.Fatalf("attempts = %d, want the five it spent given back", requeued.Attempts)
	}
}

// The recovery pass is not a retry and must not be read as one. A shutdown
// interrupted a run nobody learned anything from, and the job keeps the attempt
// it was claimed on: being restarted is not a reason to hand the queue more
// room than it had.
func TestAnInterruptedJobKeepsTheAttemptItWasClaimedOn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	jobID := insertJob(ctx, t, pool, "scan_library", "running", 3, time.Now(), nil)

	if err := New(pool).RequeueInterruptedJobs(ctx); err != nil {
		t.Fatal(err)
	}

	if recovered := readJob(ctx, t, pool, jobID); recovered.Attempts != 3 {
		t.Fatalf("attempts = %d, want the three it had kept", recovered.Attempts)
	}
}

// A row left running by a worker that stopped without a shutdown is put back by
// the same statement once its ownership lease has expired.
// Nothing else ever puts it back: the recovery pass runs at a boot, and until
// one happens the partial unique indexes read the row as live work and refuse
// every replacement for the album it names.
func TestAJobWhoseOwnershipLeaseExpiredIsPutBack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	jobID := insertJob(ctx, t, pool, "import_download", "running", 1, time.Now(), nil)
	leaseExpiredSince(ctx, t, pool, jobID, 6*time.Hour)

	swept, err := New(pool).RequeueStalledJobs(ctx, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if swept != 1 {
		t.Fatalf("rows put back = %d, want the one that was left running", swept)
	}

	stalled := readJob(ctx, t, pool, jobID)
	if stalled.Status != "queued" {
		t.Fatalf("status = %q, want the job waiting again", stalled.Status)
	}
	if stalled.StartedAt.Valid {
		t.Fatalf("job %s kept traces of the run that did not finish it: %+v", jobID, stalled)
	}
	if stalled.ErrorMessage.String != stalledNote {
		t.Fatalf("error = %q, want %q", stalled.ErrorMessage.String, stalledNote)
	}
}

// The sweep is made while workers are working, so a job whose lease is still
// fresh must be left exactly where it is. Putting one back would hand the same
// work to a second lane while the first is still doing it.
func TestAJobWhoseOwnershipLeaseIsFreshIsLeftAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	jobID := insertJob(ctx, t, pool, "scan_library", "running", 1, time.Now(), nil)
	leaseExpiredSince(ctx, t, pool, jobID, 30*time.Minute)

	if _, err := New(pool).RequeueStalledJobs(ctx, 2*time.Hour); err != nil {
		t.Fatal(err)
	}

	if slow := readJob(ctx, t, pool, jobID); slow.Status != "running" {
		t.Fatalf("status = %q, want the job left running", slow.Status)
	}
}

// The sweep is not a retry, for the reason the recovery pass is not one: the
// job was claimed, and being swept up is no reason to hand the queue more room
// than it had.
func TestAJobTheSweepPutBackKeepsTheAttemptItWasClaimedOn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	jobID := insertJob(ctx, t, pool, "refresh_album_metadata", "running", 2, time.Now(), nil)
	leaseExpiredSince(ctx, t, pool, jobID, 6*time.Hour)

	if _, err := New(pool).RequeueStalledJobs(ctx, 2*time.Hour); err != nil {
		t.Fatal(err)
	}

	if stalled := readJob(ctx, t, pool, jobID); stalled.Attempts != 2 {
		t.Fatalf("attempts = %d, want the two it had kept", stalled.Attempts)
	}
}

// leaseExpiredSince backdates both the claim and its last heartbeat. A long
// runtime alone is not stale; the sweep reads only the last proof of ownership.
func leaseExpiredSince(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, jobID uuid.UUID, ago time.Duration,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		UPDATE jobs
		SET started_at = now() - make_interval(secs => $2::double precision),
		    updated_at = now() - make_interval(secs => $2::double precision)
		WHERE id = $1
	`, jobID, ago.Seconds()); err != nil {
		t.Fatal(err)
	}
}

// Pressing retry twice. The second press must not queue a second job, must not
// reset one the worker has since claimed, and must say plainly what it found
// rather than refusing in a way nobody can act on.
func TestPressingRetryTwiceAddsNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	jobID := insertJob(ctx, t, pool, "ingest_release_group", "failed", 3, time.Now(),
		"fetch release group: 502")

	first, wasRequeued, err := New(pool).RequeueFailedJob(ctx, jobID)
	if err != nil || !wasRequeued {
		t.Fatalf("first press: requeued = %v, err = %v", wasRequeued, err)
	}

	second, wasRequeued, err := New(pool).RequeueFailedJob(ctx, jobID)
	if err != nil {
		t.Fatalf("second press: %v", err)
	}
	if wasRequeued {
		t.Fatal("the second press reported having queued the job again")
	}
	if second.Status != "queued" {
		t.Fatalf("status = %q, want the job left waiting as the first press left it",
			second.Status)
	}
	if !second.RunAfter.Time.Equal(first.RunAfter.Time) {
		t.Fatalf("runAfter moved from %v to %v; the second press changed the schedule",
			first.RunAfter.Time, second.RunAfter.Time)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM jobs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("jobs = %d, want the one that was retried and no copy of it", count)
	}
}

// The other second press: one that arrives after a worker has claimed the job.
// Putting it back now would run the work twice, so the running job is left
// exactly as it is and the press is answered with what it found.
func TestRetryLeavesARunningJobAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	jobID := insertJob(ctx, t, pool, "ingest_release_group", "running", 4, time.Now(), nil)
	before := readJob(ctx, t, pool, jobID)

	row, wasRequeued, err := New(pool).RequeueFailedJob(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if wasRequeued {
		t.Fatal("a running job was pulled back out of the worker's hands")
	}
	if row.Status != "running" {
		t.Fatalf("status = %q, want it left running", row.Status)
	}
	if !row.StartedAt.Time.Equal(before.StartedAt.Time) {
		t.Fatalf("startedAt moved from %v to %v; the run was reset under the worker",
			before.StartedAt.Time, row.StartedAt.Time)
	}
}

// A cancelled job was stopped rather than tried. Requeueing it would search a
// release whose run was called off, so it is refused in a sentence somebody can
// read.
func TestRetryRefusesAJobThatWasStopped(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	jobID := insertJob(ctx, t, pool, "search_album_sources", "cancelled", 2, time.Now(),
		"the run was called off")

	if _, _, err := New(pool).RequeueFailedJob(ctx, jobID); !errors.Is(err, ErrJobWasStopped) {
		t.Fatalf("err = %v, want %v", err, ErrJobWasStopped)
	}
	if readJob(ctx, t, pool, jobID).Status != "cancelled" {
		t.Fatal("a refused retry changed the job anyway")
	}
}

// An identifier the queue has never held is a missing job rather than a failure
// of the queue, and the caller is told which.
func TestRetryOfAJobTheQueueNeverHeldIsNotFound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	if _, _, err := New(pool).RequeueFailedJob(ctx, uuid.New()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("err = %v, want %v", err, pgx.ErrNoRows)
	}
}

// failedSince backdates when a job stopped, so a test can say how long ago it
// spent its attempts without waiting for the clock.
func failedSince(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, jobID uuid.UUID, ago time.Duration,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		UPDATE jobs
		SET completed_at = now() - make_interval(secs => $2::double precision)
		WHERE id = $1
	`, jobID, ago.Seconds()); err != nil {
		t.Fatal(err)
	}
}

// The pass over the failed list only looks at failures old enough for whatever
// stopped them to have passed. A job that failed a minute ago is answered by its
// own retry ladder, not by this.
func TestOnlyFailuresOlderThanTheWindowAreOfferedAnotherAttempt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	old := insertJob(ctx, t, pool, "import_download", "failed", 3, time.Now(), "lookup failed")
	failedSince(ctx, t, pool, old, time.Hour)
	insertJob(ctx, t, pool, "import_download", "failed", 3, time.Now(), "lookup failed")

	spent, err := New(pool).SpentJobs(ctx, time.Now().Add(-30*time.Minute), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(spent) != 1 || spent[0].ID != old {
		t.Fatalf("spent = %v, want only the failure old enough to be worth repeating", spent)
	}
}

// The free attempt is granted once. A job that has already had one has been
// claimed past its allowance, and that is the whole of the record kept of it.
func TestAJobThatHasAlreadyHadAFreeAttemptIsNotOfferedAnother(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	jobID := insertJob(ctx, t, pool, "import_download", "failed", 4, time.Now(), "lookup failed")
	failedSince(ctx, t, pool, jobID, time.Hour)

	spent, err := New(pool).SpentJobs(ctx, time.Now().Add(-30*time.Minute), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(spent) != 0 {
		t.Fatalf("spent = %v, want nothing: this job has been tried past its allowance", spent)
	}
}

// Reviving is not the retry a person presses. It puts the job back with its
// attempts exactly as they were, so the claim that runs it counts one past the
// allowance and the job fails for good if it fails again.
func TestARevivedJobKeepsTheAttemptsItSpent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	jobID := insertJob(ctx, t, pool, "import_download", "failed", 3, time.Now(), "lookup failed")

	revived, _, err := New(pool).ReviveSpentJobs(ctx, []uuid.UUID{jobID})
	if err != nil || revived != 1 {
		t.Fatalf("revived = %d, err = %v", revived, err)
	}
	row := readJob(ctx, t, pool, jobID)
	if row.Status != "queued" {
		t.Fatalf("status = %q, want it waiting to be claimed", row.Status)
	}
	if row.Attempts != 3 {
		t.Fatalf("attempts = %d, want the three it spent kept", row.Attempts)
	}
}

// Some job kinds are held to one live copy — one queued or running row — by a
// unique index. The recommendation sweep is one of them, and it schedules its
// own successor, so a failed sweep sits beside a live one and cannot go back on
// the queue. That refusal is right for the sweep and wrong for everything
// beside it: reviving used to be a single statement, so the sweep's collision
// aborted it and the transfer imports in the same batch stayed failed.
func TestOneJobWhoseWorkIsQueuedAlreadyDoesNotStopTheOthersFromGoingBack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	insertJob(ctx, t, pool, "sweep_recommendations", "queued", 0, time.Now(), nil)
	sweep := insertJob(ctx, t, pool, "sweep_recommendations", "failed", 3, time.Now(),
		"lookup listenbrainz.org: no such host")
	transfer := insertJob(ctx, t, pool, "import_download", "failed", 3, time.Now(),
		"lookup musicbrainz.org: no such host")

	revived, skipped, err := New(pool).ReviveSpentJobs(ctx, []uuid.UUID{sweep, transfer})
	if err != nil {
		t.Fatalf("ReviveSpentJobs() error = %v, want the batch to go through", err)
	}
	if revived != 1 || skipped != 1 {
		t.Fatalf("revived = %d, skipped = %d, want one of each", revived, skipped)
	}
	if status := readJob(ctx, t, pool, transfer).Status; status != "queued" {
		t.Fatalf("the transfer import is %q, want it waiting to be claimed", status)
	}
	if status := readJob(ctx, t, pool, sweep).Status; status != "failed" {
		t.Fatalf("the failed sweep is %q, want it left where it is beside its successor", status)
	}
}

// Nothing else ever removed a spent job, so the failed list only grew. Only
// failures, and only old ones: the pulse of the recurring sweeps is read from
// completed rows and must not be swept up with them.
func TestForgettingFailuresLeavesEverythingElseAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	old := insertJob(ctx, t, pool, "import_download", "failed", 3, time.Now(), "lookup failed")
	failedSince(ctx, t, pool, old, 30*24*time.Hour)
	recent := insertJob(ctx, t, pool, "import_download", "failed", 3, time.Now(), "lookup failed")
	finished := insertJob(ctx, t, pool, "sweep_cover_art", "completed", 1, time.Now(), nil)
	failedSince(ctx, t, pool, finished, 30*24*time.Hour)

	forgotten, err := New(pool).ForgetSpentJobs(ctx, time.Now().Add(-14*24*time.Hour))
	if err != nil || forgotten != 1 {
		t.Fatalf("forgotten = %d, err = %v; want the one failure older than the window",
			forgotten, err)
	}
	if _, err := New(pool).Job(ctx, recent); err != nil {
		t.Fatalf("the recent failure was forgotten too: %v", err)
	}
	if _, err := New(pool).Job(ctx, finished); err != nil {
		t.Fatalf("a completed job was forgotten with the failures: %v", err)
	}
}

// One press for a whole cause. Every job it names that is still failing goes
// back with its attempts returned; anything else is left exactly where it is.
func TestRetryingAWholeCauseMovesOnlyTheJobsStillFailing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	first := insertJob(ctx, t, pool, "import_download", "failed", 3, time.Now(), "lookup failed")
	second := insertJob(ctx, t, pool, "resolve_library_file", "failed", 3, time.Now(), "lookup failed")
	running := insertJob(ctx, t, pool, "import_download", "running", 1, time.Now(), nil)

	requeued, err := New(pool).RequeueFailedJobs(ctx, []uuid.UUID{first, second, running})
	if err != nil {
		t.Fatal(err)
	}
	if requeued != 2 {
		t.Fatalf("requeued = %d, want the two that were failing", requeued)
	}
	if row := readJob(ctx, t, pool, first); row.Status != "queued" || row.Attempts != 0 {
		t.Fatalf("status = %q with %d attempts, want it queued with the whole ladder back",
			row.Status, row.Attempts)
	}
	if readJob(ctx, t, pool, running).Status != "running" {
		t.Fatal("a running job was pulled back out of the worker's hands")
	}
}

// A failed sweep beside its live successor is refused by the index that holds
// that kind to one copy. The refusal belongs to that one job: the rest of the
// cause goes back on the queue, and a single press on the sweep answers that
// nothing moved rather than with an error.
func TestRetryingACauseSkipsTheSweepWhoseSuccessorIsAlreadyWaiting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	failedSweep := insertJob(ctx, t, pool, "sweep_recommendations", "failed", 3, time.Now(), "lookup failed")
	insertJob(ctx, t, pool, "sweep_recommendations", "queued", 0, time.Now(), nil)
	stopped := insertJob(ctx, t, pool, "import_download", "failed", 3, time.Now(), "lookup failed")

	requeued, err := New(pool).RequeueFailedJobs(ctx, []uuid.UUID{failedSweep, stopped})
	if err != nil {
		t.Fatalf("the whole press failed on the one job the index refused: %v", err)
	}
	if requeued != 1 {
		t.Fatalf("requeued = %d, want the one job that had nothing waiting for it", requeued)
	}
	if readJob(ctx, t, pool, stopped).Status != "queued" {
		t.Fatal("the stopped import was left in the failed list")
	}
	if readJob(ctx, t, pool, failedSweep).Status != "failed" {
		t.Fatal("the failed sweep was put beside its live successor")
	}

	_, moved, err := New(pool).RequeueFailedJob(ctx, failedSweep)
	if err != nil {
		t.Fatalf("one press on the failed sweep answered with an error: %v", err)
	}
	if moved {
		t.Fatal("the failed sweep was reported as requeued beside its successor")
	}
}

// Cancelling one job before a worker ever claims it is the whole of what a
// person picking one row deliberately can do: it goes back to nothing rather
// than to a cancelled status, because nothing about a queued row that never
// ran is worth keeping.
func TestCancellingAQueuedJobRemovesIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	jobID := insertJob(ctx, t, pool, "refresh_album_metadata", "queued", 0, time.Now(), nil)

	row, cancelled, err := New(pool).CancelQueuedJob(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if !cancelled {
		t.Fatal("a queued job was not reported as cancelled")
	}
	if row.ID != jobID {
		t.Fatalf("cancelled = %s, want %s", row.ID, jobID)
	}
	if _, err := New(pool).Job(ctx, jobID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("err = %v, want %v: the row must be gone rather than marked", err, pgx.ErrNoRows)
	}
}

// A running job is being worked by a worker right now. Cancelling it is a
// different problem and out of scope here, so the row is left exactly as it
// is and the press is answered with what it found.
func TestCancellingLeavesARunningJobAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	jobID := insertJob(ctx, t, pool, "ingest_release_group", "running", 1, time.Now(), nil)

	row, cancelled, err := New(pool).CancelQueuedJob(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled {
		t.Fatal("a running job was cancelled out from under the worker")
	}
	if row.Status != "running" {
		t.Fatalf("status = %q, want it left running", row.Status)
	}
	if readJob(ctx, t, pool, jobID).Status != "running" {
		t.Fatal("the running job was removed")
	}
}

// An identifier the queue has never held is a missing job rather than a
// failure of the queue, the same answer a retry of one gives.
func TestCancelOfAJobTheQueueNeverHeldIsNotFound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	if _, _, err := New(pool).CancelQueuedJob(ctx, uuid.New()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("err = %v, want %v", err, pgx.ErrNoRows)
	}
}

// The bulk control is the shape a runaway sweep takes: many rows of one kind,
// queued by a bug rather than by anyone waiting on them. Every queued row of
// that kind goes, and nothing else does.
func TestCancellingByKindRemovesEveryQueuedRowOfThatKind(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	first := insertJob(ctx, t, pool, "refresh_album_metadata", "queued", 0, time.Now(), nil)
	second := insertJob(ctx, t, pool, "refresh_album_metadata", "queued", 0, time.Now(), nil)
	other := insertJob(ctx, t, pool, "refresh_artist_metadata", "queued", 0, time.Now(), nil)

	cancelled, err := New(pool).CancelQueuedJobsByKind(ctx, "refresh_album_metadata")
	if err != nil {
		t.Fatal(err)
	}
	if cancelled != 2 {
		t.Fatalf("cancelled = %d, want the two queued rows of that kind", cancelled)
	}
	for _, jobID := range []uuid.UUID{first, second} {
		if _, err := New(pool).Job(ctx, jobID); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("job %s survived a bulk cancel of its own kind", jobID)
		}
	}
	if readJob(ctx, t, pool, other).Status != "queued" {
		t.Fatal("a job of a different kind was cancelled by the bulk press")
	}
}

// A running job is being worked whatever kind it is, so the bulk control
// leaves it exactly where a single cancel would.
func TestCancellingByKindLeavesARunningJobOfThatKindAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	queued := insertJob(ctx, t, pool, "resolve_library_file", "queued", 0, time.Now(), nil)
	running := insertJob(ctx, t, pool, "resolve_library_file", "running", 1, time.Now(), nil)

	cancelled, err := New(pool).CancelQueuedJobsByKind(ctx, "resolve_library_file")
	if err != nil {
		t.Fatal(err)
	}
	if cancelled != 1 {
		t.Fatalf("cancelled = %d, want only the one that was queued", cancelled)
	}
	if readJob(ctx, t, pool, running).Status != "running" {
		t.Fatal("a running job was cancelled by the bulk press")
	}
	if _, err := New(pool).Job(ctx, queued); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("the queued job survived the bulk cancel")
	}
}

// The point of the bulk control: a recurring sweep schedules itself by
// queueing its own successor, and that queued row is held to one live copy by
// a kind-only unique index. Clearing a runaway kind's backlog must never be
// able to reach it, or clearing an unrelated backlog would silently switch a
// weekly sweep off with nothing left to schedule it again.
func TestCancellingByKindLeavesTheScheduledSweepRowIntact(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	due := time.Now().Add(12 * time.Hour)
	sweep := insertJob(ctx, t, pool, "sweep_genres", "queued", 0, due, nil)

	cancelled, err := New(pool).CancelQueuedJobsByKind(ctx, "sweep_genres")
	if err != nil {
		t.Fatal(err)
	}
	if cancelled != 0 {
		t.Fatalf("cancelled = %d, want the schedule left standing", cancelled)
	}
	row := readJob(ctx, t, pool, sweep)
	if row.Status != "queued" {
		t.Fatalf("status = %q, want the scheduled sweep left waiting for its next run", row.Status)
	}
	if row.RunAfter.Time.Sub(due).Abs() > time.Second {
		t.Fatalf("runAfter = %v, want the schedule's own time %v left untouched", row.RunAfter.Time, due)
	}
}

// The guard is on the kind, not the clock. A worker outage can leave a
// recurring sweep's own row overdue — due in the past rather than in the
// future — and it is still the schedule: nothing else will ever queue that
// pass again, so it must survive a bulk cancel exactly as a row due twelve
// hours from now does. A predicate written as "run_after in the future"
// would delete this row and would still pass the test above it.
func TestCancellingByKindLeavesAnOverdueScheduledSweepRowIntact(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	overdue := time.Now().Add(-3 * time.Hour)
	sweep := insertJob(ctx, t, pool, "sweep_genres", "queued", 0, overdue, nil)

	cancelled, err := New(pool).CancelQueuedJobsByKind(ctx, "sweep_genres")
	if err != nil {
		t.Fatal(err)
	}
	if cancelled != 0 {
		t.Fatalf("cancelled = %d, want the overdue schedule left standing", cancelled)
	}
	if readJob(ctx, t, pool, sweep).Status != "queued" {
		t.Fatal("an overdue scheduled sweep row was cancelled, leaving nothing to queue it again")
	}
}

// ScheduledSweepKinds is a list of strings and the thing it describes is a set
// of indexes, so the two can drift. A kind indexed on the kind alone can hold
// one queued row and no more, so it has no backlog for a bulk cancel to clear
// and the press could only ever delete the schedule. This reads the indexes
// back out of the database and fails when a new one is not on the list.
func TestEveryKindHeldToOneLiveRowIsListedAsAScheduledSweep(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	rows, err := pool.Query(ctx, `
		SELECT indexdef FROM pg_indexes
		WHERE tablename = 'jobs' AND indexdef LIKE '%UNIQUE%USING btree (kind) WHERE%'
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	named := regexp.MustCompile(`kind = '([a-z_]+)'::text`)
	held := make([]string, 0)
	for rows.Next() {
		var definition string
		if err := rows.Scan(&definition); err != nil {
			t.Fatal(err)
		}
		match := named.FindStringSubmatch(definition)
		if match == nil {
			t.Fatalf("no kind in %s", definition)
		}
		held = append(held, match[1])
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(held) == 0 {
		t.Fatal("no kind-only unique index was read; the query no longer matches any")
	}

	for _, kind := range held {
		if !slices.Contains(ScheduledSweepKinds, kind) {
			t.Errorf("%s is held to one live row and is not in ScheduledSweepKinds, "+
				"so a bulk cancel would delete its schedule", kind)
		}
	}
	for _, kind := range ScheduledSweepKinds {
		if !slices.Contains(held, kind) {
			t.Errorf("%s is in ScheduledSweepKinds and is not held to one live row, "+
				"so a real backlog of it could never be cleared", kind)
		}
	}
}
