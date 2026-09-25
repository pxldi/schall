package db

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// requeueJobs is the one move that puts a job back on the queue: waiting again,
// runnable now, with nothing left of the run that did not finish it.
//
// Two things make that move — the recovery pass at startup, which sweeps up
// whatever a shutdown left running, and a person retrying a job that spent its
// attempts — and they share the statement rather than each writing their own.
// A retry that requeued differently from the recovery would be a second set of
// semantics for the same thing, and the queue would have two ways of being back
// in it. Both arrive back in it by this one statement; what they disagree about
// is the ladder, and that disagreement is a parameter rather than a second
// statement.
//
// The ladder is where the two part company, which is why $2 is a parameter
// rather than a constant. A shutdown interrupted a run that was never tried:
// nothing was learned, so the job keeps the attempt it was on and is handed no
// fresh ladder for having been restarted. A retry is the opposite — the job
// spent every attempt it had, and a person looked at the reason and asked for
// it again — so its attempts go back to nothing and it climbs the whole ladder
// once more. Granting a single attempt to a job that already failed five times
// would usually fail again within seconds and read as though the button had
// done nothing.
//
// $1 is what to say about the job while it waits, and a caller with nothing to
// say leaves the reason where it is. A job queued again after failing still
// reports what went wrong last time, until it is claimed and the queue has
// something newer to report.
const requeueJobs = `
	UPDATE jobs
	SET status = 'queued',
	    run_after = now(),
	    started_at = NULL,
	    completed_at = NULL,
	    attempts = coalesce($2::int, attempts),
	    error_message = coalesce($1::text, error_message),
	    updated_at = now()
`

// interruptedNote is what a job says about itself after a shutdown stopped it
// mid-run. It is not a failure — nothing was tried and nothing was learned —
// which is why the job goes back on the queue rather than into the failed list.
const interruptedNote = "interrupted by application shutdown"

// stalledNote is what a job says about itself after the sweep found it still
// running long after anything could still have been running it. It is the
// interrupted note's case without the shutdown: nothing was tried and nothing
// was learned, and nobody was there to say so at the time.
const stalledNote = "left running with no worker on it"

// stoppedRunNote is what a source-search job says about itself after a recovery
// pass found the run it belongs to already stopped.
const stoppedRunNote = "the run this release belonged to was stopped"

// stopSearchesOfStoppedRuns ends the running source-search jobs whose run was
// cancelled while they were running, so that the pass which follows has nothing
// of theirs left to put back.
//
// A source-search run is the bulk search behind "find sources for these
// releases": one job per release. Stopping a run cancels the jobs nobody has
// claimed and leaves a claimed one to finish, because cancelling never
// withdraws an answer that already exists. If the application restarts before
// that one finishes, recovery finds a job left `running` and puts it back — and
// a worker then searches a release on behalf of a run the user stopped, which
// spends a provider search Schall rations hard.
//
// The test is the one the claim path uses: a run is stopped if any job of it is
// cancelled. Cancelling those rows here cannot cascade, because an UPDATE reads
// one snapshot of the table and the rows it writes are not rows it can then
// find.
//
// $2 is how long a job must have gone without its lease being renewed. The pass
// on a clock passes it, so that a job a live worker is holding right now is
// never taken out from under it — that worker applies the same rule itself when
// its search returns. The pass at boot passes nothing, because at boot no
// worker is holding anything.
const stopSearchesOfStoppedRuns = `
	UPDATE jobs AS recovered
	SET status = 'cancelled',
	    completed_at = now(),
	    updated_at = now(),
	    error_message = $1::text
	WHERE recovered.kind = 'search_album_sources'
	  AND recovered.status = 'running'
	  AND ($2::double precision IS NULL
	       OR recovered.updated_at < now() - make_interval(secs => $2::double precision))
	  AND EXISTS (
	      SELECT 1 FROM jobs AS stopped
	      WHERE stopped.kind = 'search_album_sources'
	        AND stopped.status = 'cancelled'
	        AND stopped.payload->>'runId' = recovered.payload->>'runId'
	  )
`

// jobColumns is one job as the queue view and the retry answer read it.
const jobColumns = `
	id, kind, status, attempts, max_attempts, run_after, started_at,
	completed_at, error_message, created_at
`

// ErrJobWasStopped refuses to retry a job that was cancelled. Cancelling is not
// failing: the run it belonged to was called off, and putting it back would
// search a release nobody is waiting for.
var ErrJobWasStopped = errors.New("this job was stopped rather than tried, so there is nothing to retry")

// JobRow is one job of the queue.
type JobRow struct {
	ID           uuid.UUID          `json:"id"`
	Kind         string             `json:"kind"`
	Status       string             `json:"status"`
	Attempts     int32              `json:"attempts"`
	MaxAttempts  int32              `json:"max_attempts"`
	RunAfter     pgtype.Timestamptz `json:"run_after"`
	StartedAt    pgtype.Timestamptz `json:"started_at"`
	CompletedAt  pgtype.Timestamptz `json:"completed_at"`
	ErrorMessage pgtype.Text        `json:"error_message"`
	CreatedAt    pgtype.Timestamptz `json:"created_at"`
}

// RequeueInterruptedJobs puts back every job a shutdown left running. It is the
// recovery pass the worker makes once, however many lanes it is running.
//
// The attempt the job was interrupted on stays spent. It was claimed, and a
// restart is not a reason to give the queue more room than it had.
// A source-search job whose run has since been stopped is ended rather than put
// back. See stopSearchesOfStoppedRuns.
func (q *Queries) RequeueInterruptedJobs(ctx context.Context) error {
	if _, err := q.db.Exec(ctx, stopSearchesOfStoppedRuns,
		stoppedRunNote, (*float64)(nil)); err != nil {
		return fmt.Errorf("stop the searches of stopped runs: %w", err)
	}
	_, err := q.db.Exec(ctx, requeueJobs+` WHERE status = 'running'`,
		interruptedNote, (*int32)(nil))
	return err
}

// RequeueStalledJobs puts back every running job whose worker has stopped
// renewing its lease. It is the recovery pass again, on a clock instead of at a
// boot, and it reports how many rows it found.
//
// A shutdown is not the only way a row is left running. A worker killed
// outright, or one whose settlement never reached the database, leaves the same
// row behind, and nothing puts that one back until somebody restarts the
// application. Meanwhile the partial unique indexes read it as live work and
// refuse every replacement for that request or that album, so the piece of work
// it names cannot be queued again at all.
//
// The attempt stays spent, for the reason the boot pass keeps it: the job was
// claimed, and being swept up is not a reason to hand the queue more room than
// it had.
func (q *Queries) RequeueStalledJobs(ctx context.Context, staleFor time.Duration) (int64, error) {
	// The same rule as at boot, bounded by the same staleness this pass is
	// bounded by. What it ends is not requeued, so it is not counted either:
	// the number this reports is how many jobs went back on the queue.
	if _, err := q.db.Exec(ctx, stopSearchesOfStoppedRuns,
		stoppedRunNote, staleFor.Seconds()); err != nil {
		return 0, fmt.Errorf("stop the searches of stopped runs: %w", err)
	}
	tag, err := q.db.Exec(ctx, requeueJobs+`
		WHERE status = 'running'
		  AND updated_at < now() - make_interval(secs => $3::double precision)
	`, stalledNote, (*int32)(nil), staleFor.Seconds())
	if err != nil {
		return 0, fmt.Errorf("requeue stalled jobs: %w", err)
	}
	return tag.RowsAffected(), nil
}

// RequeueFailedJob puts one job that spent its attempts back on the queue, by
// the same statement the recovery pass uses, and reports whether this call was
// the one that did it.
//
// The attempts go back to nothing. A retried job is new work rather than a
// wounded version of the old work: it climbs the whole ladder again, with the
// whole backoff, instead of being handed one run that would usually fail again
// before the person who pressed the button had looked away.
//
// A job that is already waiting, already running, or has since finished is left
// exactly as it is and answered with as much: nothing was requeued, and here is
// what the job is doing instead. That is the second press — of a person who did
// not see the first one land, or of a browser that sent it twice — and it must
// cost the queue nothing rather than stack a second run behind the first, reset
// the ladder under a job that is already climbing it, or pull a running job out
// from under the worker.
func (q *Queries) RequeueFailedJob(ctx context.Context, jobID uuid.UUID) (JobRow, bool, error) {
	row, err := scanJob(q.db.QueryRow(ctx,
		requeueJobs+` WHERE id = $3 AND status = 'failed' RETURNING`+jobColumns,
		(*string)(nil), int32(0), jobID))
	switch {
	case err == nil:
		return row, true, nil
	case isUniqueViolation(err):
		// A kind held to one live copy, whose successor is already queued or
		// running. The work is on its way; the press changes nothing.
		row, err = q.Job(ctx, jobID)
		if err != nil {
			return JobRow{}, false, err
		}
		return row, false, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return JobRow{}, false, fmt.Errorf("requeue job %s: %w", jobID, err)
	}

	// Nothing had failed under that identifier. Which of the reasons for that
	// it was is worth more to the caller than a refusal that does not say.
	row, err = q.Job(ctx, jobID)
	if err != nil {
		return JobRow{}, false, err
	}
	if row.Status == "cancelled" {
		return JobRow{}, false, ErrJobWasStopped
	}
	return row, false, nil
}

// CancelQueuedJob removes one job before a worker ever claims it.
//
// It deletes the row rather than marking it cancelled: nothing about a job
// that never ran is worth keeping, and this is the same move the manual
// remedy was — a `DELETE FROM jobs` against the row, run by hand through a
// port-forward before there was a button for it.
//
// Only a `queued` row is touched. A `running` job is being worked by a worker
// right now and stopping it is a different problem; a `completed` or `failed`
// one is history and this never reaches it, because the statement's own WHERE
// clause is the guard. The second press — of a person who does not see the
// first one land — finds nothing queued under that id and is told what it is
// doing instead, the same shape RequeueFailedJob answers with.
func (q *Queries) CancelQueuedJob(ctx context.Context, jobID uuid.UUID) (JobRow, bool, error) {
	row, err := scanJob(q.db.QueryRow(ctx,
		`DELETE FROM jobs WHERE id = $1 AND status = 'queued' RETURNING`+jobColumns, jobID))
	switch {
	case err == nil:
		return row, true, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return JobRow{}, false, fmt.Errorf("cancel job %s: %w", jobID, err)
	}
	// Nothing queued under that id: it may be running, finished, already
	// cancelled, or never have existed at all — which of those is worth more to
	// the caller than a bare refusal.
	row, err = q.Job(ctx, jobID)
	if err != nil {
		return JobRow{}, false, err
	}
	return row, false, nil
}

// RequeueFailedJobs puts a whole group of failures back on the queue at once,
// by the statement one of them goes back by, and reports how many moved.
//
// It is one press on a screen that lists failures by their cause rather than one
// by one: seventy-one jobs stopped by the same outage are one fact and deserve
// one button. Which jobs those are is decided before this — the caller reads the
// failures, classifies each one and hands over the identifiers — because the
// cause is read out of the recorded error in Go and there is no second copy of
// that reading written in SQL.
//
// A job in the list that is no longer failed is left where it is, exactly as a
// single retry leaves it: the count answers with what moved rather than with
// what was asked for.
//
// One job at a time, for the reason ReviveSpentJobs gives: some kinds are held
// to one live copy by a unique index, and a failed sweep usually sits beside
// the successor that replaced it. One statement for the whole group made that
// refusal everybody's — a press on "MusicBrainz dropped the connection" that
// happened to include a failed sweep answered with a 500 and moved nothing.
// The refused job is already on its way, so it counts as not moved, and the
// rest of the group goes back.
func (q *Queries) RequeueFailedJobs(ctx context.Context, jobIDs []uuid.UUID) (int64, error) {
	var requeued int64
	for _, jobID := range jobIDs {
		tag, err := q.db.Exec(ctx, requeueJobs+` WHERE id = $3 AND status = 'failed'`,
			(*string)(nil), int32(0), jobID)
		if err != nil {
			if isUniqueViolation(err) {
				continue
			}
			return requeued, fmt.Errorf("requeue failed job %s: %w", jobID, err)
		}
		requeued += tag.RowsAffected()
	}
	return requeued, nil
}

// ScheduledSweepKinds are the kinds that hold themselves to one live row —
// queued or running — with a unique index on the kind alone. Each finishes a
// pass and queues its own successor (internal/jobs/worker.go's queue*Sweep
// helpers), so the one queued row of one of these kinds is never a backlog. It
// is the schedule, and for most of them it is the only thing that will ever
// queue that pass again.
//
// The index is the rule, not a judgement about which passes matter: a kind that
// can only ever hold one queued row has no backlog for a bulk cancel to clear,
// so the press can do nothing but delete the schedule.
// TestEveryKindHeldToOneLiveRowIsListedAsAScheduledSweep reads the indexes back
// out of the database and fails if this list has fallen behind them.
//
// Exported so a caller can say why a bulk cancel refused a kind, and kept as
// literal strings rather than the constants internal/jobs already exports for
// them: internal/jobs imports this package, so this package cannot import
// internal/jobs back. Each of these kinds is also fixed by the migration that
// indexed it, so the string is not expected to move under this list.
var ScheduledSweepKinds = []string{
	"scan_library",
	"poll_downloads",
	"notify_player",
	"sweep_acquisition_targets",
	"sweep_cover_art",
	"sweep_follow_feed",
	"sweep_recommendations",
	"sweep_own_recommendations",
	"sweep_lyrics",
	"sync_listens",
	"sweep_preview_anchors",
	"sweep_genres",
	"sweep_copy_fingerprints",
	"sweep_library_witnesses",
	"sweep_loudness",
	"sweep_audio_spectrum",
	"sweep_upgrade_candidates",
	"refresh_weekly_playlist",
	"refresh_new_releases_playlist",
	"tag_library",
	"sweep_transcode",
	"sweep_duplicates",
	"answer_peer_challenges",
}

// CancelQueuedJobsByKind removes every queued job of one kind — the shape a
// runaway sweep takes, clearing a backlog with one press where the only
// remedy used to be a `DELETE FROM jobs` through a port-forward and a wait,
// job by job, at whatever pace the queue drains.
//
// A kind in ScheduledSweepKinds is left untouched, the whole function
// short-circuited before the statement runs: cancelling by kind must never be
// able to reach the row that is a recurring sweep's own schedule, or clearing
// an unrelated backlog would silently switch that sweep off with nothing left
// to queue it again. Cancelling one of those rows one at a time, by id, is
// still possible — CancelQueuedJob has no such guard, because picking one row
// is a person's deliberate choice rather than a sweep over a kind.
func (q *Queries) CancelQueuedJobsByKind(ctx context.Context, kind string) (int64, error) {
	if slices.Contains(ScheduledSweepKinds, kind) {
		return 0, nil
	}
	tag, err := q.db.Exec(ctx, `DELETE FROM jobs WHERE status = 'queued' AND kind = $1`, kind)
	if err != nil {
		return 0, fmt.Errorf("cancel queued %s jobs: %w", kind, err)
	}
	return tag.RowsAffected(), nil
}

// SpentJobRow is a job that failed as often as it was allowed to, read for the
// pass that decides whether to try it once more.
type SpentJobRow struct {
	ID           uuid.UUID
	Kind         string
	ErrorMessage pgtype.Text
}

// SpentJobs lists the failures that are old enough to be worth another attempt
// and that have not already had one.
//
// "Have not already had one" is the attempts column, and nothing else. A job is
// allowed max_attempts tries; the free attempt this pass grants is taken with
// the allowance already spent, so the claim that runs it counts one past the
// allowance and the row can never qualify again. Nothing new is written down to
// remember that, which matters because remembering it would have needed a column
// that does not exist.
func (q *Queries) SpentJobs(
	ctx context.Context, failedBefore time.Time, limit int32,
) ([]SpentJobRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT id, kind, error_message
		FROM jobs
		WHERE status = 'failed'
		  AND attempts <= max_attempts
		  AND completed_at < $1
		ORDER BY completed_at
		LIMIT $2
	`, failedBefore, limit)
	if err != nil {
		return nil, fmt.Errorf("read spent jobs: %w", err)
	}
	defer rows.Close()

	spent := []SpentJobRow{}
	for rows.Next() {
		var row SpentJobRow
		if err := rows.Scan(&row.ID, &row.Kind, &row.ErrorMessage); err != nil {
			return nil, fmt.Errorf("read spent job: %w", err)
		}
		spent = append(spent, row)
	}
	return spent, rows.Err()
}

// ReviveSpentJobs grants one more attempt to jobs an outage stopped.
//
// It is not the retry a person presses. That one gives the whole ladder back;
// this one gives a single attempt, by putting the job back on the queue with its
// attempts left exactly as they were. The claim adds one as it takes the job, so
// the job runs once past its allowance and fails for good if it fails again —
// and the same row is not offered a second free attempt by any later pass.
//
// One job at a time, and each on its own, because some job kinds are allowed
// only one live copy. A live copy is a job that is queued or running, and the
// database holds those kinds to one with a unique index: the recommendation
// sweep, the picture sweep, the transfer poller, an import of one transfer, a
// refresh of one release. Several of them schedule their own successor, so a
// failed copy of such a kind normally sits beside a live successor already, and
// putting it back on the queue is a second live copy the index refuses.
//
// Refusing it is right — the work is already waiting — but one statement for
// the whole batch made that refusal everybody's. The batch was one UPDATE, so
// the collision on the sweep aborted the statement, and the transfer imports an
// outage had stopped stayed in the failed list with it. Every fifteen minutes,
// for as long as the failed sweep was there.
//
// So the collision is read from the index rather than predicted here: a job
// refused for being a second live copy is counted as skipped and the rest of
// the batch goes back on the queue. The index is the only authority on which
// kinds are held to one, and a kind added later is covered without this
// statement knowing about it. A skipped job keeps its place in the failed list
// and is offered again by the next pass, until its successor finishes or the
// failure is old enough to be forgotten.
func (q *Queries) ReviveSpentJobs(ctx context.Context, jobIDs []uuid.UUID) (int64, int64, error) {
	var revived, skipped int64
	for _, jobID := range jobIDs {
		tag, err := q.db.Exec(ctx, requeueJobs+`
			WHERE id = $3 AND status = 'failed' AND attempts <= max_attempts
		`, (*string)(nil), (*int32)(nil), jobID)
		if err != nil {
			if isUniqueViolation(err) {
				skipped++
				continue
			}
			return revived, skipped, fmt.Errorf("revive spent job %s: %w", jobID, err)
		}
		revived += tag.RowsAffected()
	}
	return revived, skipped, nil
}

// ForgetSpentJobs removes the failures older than the window they are kept for.
//
// Only failures, and only old ones. A job that is queued, running or completed
// is not touched, so the pulse of the recurring sweeps — which is read from
// completed rows — says what it always said.
func (q *Queries) ForgetSpentJobs(ctx context.Context, failedBefore time.Time) (int64, error) {
	tag, err := q.db.Exec(ctx, `
		DELETE FROM jobs
		WHERE status = 'failed' AND completed_at < $1
	`, failedBefore)
	if err != nil {
		return 0, fmt.Errorf("forget spent jobs: %w", err)
	}
	return tag.RowsAffected(), nil
}

// JobPayload reads the payload of one job while it is completing, so a worker
// can see a wake recorded while the job was running.
func (q *Queries) JobPayload(ctx context.Context, jobID uuid.UUID) ([]byte, error) {
	var payload []byte
	if err := q.db.QueryRow(ctx, `SELECT payload FROM jobs WHERE id = $1`, jobID).Scan(&payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// Job reads one job, whatever it is doing. It answers pgx.ErrNoRows for an
// identifier the queue has never held.
func (q *Queries) Job(ctx context.Context, jobID uuid.UUID) (JobRow, error) {
	row, err := scanJob(q.db.QueryRow(ctx, `SELECT`+jobColumns+`FROM jobs WHERE id = $1`, jobID))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return JobRow{}, fmt.Errorf("read job %s: %w", jobID, err)
	}
	return row, err
}

func scanJob(row pgx.Row) (JobRow, error) {
	var job JobRow
	err := row.Scan(
		&job.ID, &job.Kind, &job.Status, &job.Attempts, &job.MaxAttempts,
		&job.RunAfter, &job.StartedAt, &job.CompletedAt, &job.ErrorMessage,
		&job.CreatedAt,
	)
	return job, err
}

// FailedReleaseJobs reports how many failed jobs each of these releases has.
//
// A job is one release's when its payload names that release. Two kinds do:
// the pass that fetches a release's track list from MusicBrainz, and the run
// that searches sources for it. Those are exactly the two the Releases screen
// reads its "Problems" filter from, so this counts what that filter counted.
//
// The answer is per release because the press is per release. "Retried 4, 2 had
// nothing to retry" is a true sentence about a selection; "6 releases retried"
// is not.
func (q *Queries) FailedReleaseJobs(
	ctx context.Context, albumIDs []uuid.UUID,
) (map[uuid.UUID][]uuid.UUID, error) {
	byRelease := map[uuid.UUID][]uuid.UUID{}
	if len(albumIDs) == 0 {
		return byRelease, nil
	}
	rows, err := q.db.Query(ctx, `
		SELECT (payload->>'albumId')::uuid AS album_id, id
		FROM jobs
		WHERE status = 'failed'
		  AND payload->>'albumId' IS NOT NULL
		  AND (payload->>'albumId')::uuid = ANY($1::uuid[])
		ORDER BY created_at
	`, albumIDs)
	if err != nil {
		return nil, fmt.Errorf("read the failed jobs of %d releases: %w", len(albumIDs), err)
	}
	defer rows.Close()

	for rows.Next() {
		var albumID, jobID uuid.UUID
		if err := rows.Scan(&albumID, &jobID); err != nil {
			return nil, fmt.Errorf("read a failed release job: %w", err)
		}
		byRelease[albumID] = append(byRelease[albumID], jobID)
	}
	return byRelease, rows.Err()
}
