package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
)

// A want in the review queue is either asking a person something or waiting for
// evidence that could still arrive. These tests are about the second kind: how
// long it waits, what wakes it, and when it stops waiting and becomes a
// question. Nothing here admits anything — every wake queues the same
// judge_want_copies job, which puts the copies back through ImportOne.

// heldOnAnUnaskedRegistration is a want holding a copy whose audio named another
// MusicBrainz row that nobody could ask about. It is the 38 wants the queue held
// on 2026-09-08.
func heldOnAnUnaskedRegistration(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool,
) (*Queries, uuid.UUID) {
	t.Helper()
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_target_files (
			acquisition_target_id, provider, source_username, remote_path,
			file_name, size_bytes, verdict, decided_by, summary, download_request_id
		)
		VALUES ($1, 'slskd', 'peer', $2, '03.flac', 9000000, 'held', 'schall', $3, $4)
	`, targetID, `Music\Anetha\03.flac`,
		unaskedRegistrationSummary+" whether that is this track entered twice.",
		requestID); err != nil {
		t.Fatalf("record the held copy: %v", err)
	}
	return queries, targetID
}

// heldTimeoutCopy is another copy of the same want stopped on the same
// unanswerable question. A want that fetched five of them holds five rows.
func heldTimeoutCopy(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID uuid.UUID, name string,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_target_files (
			acquisition_target_id, provider, source_username, remote_path,
			file_name, size_bytes, verdict, decided_by, summary
		)
		VALUES ($1, 'slskd', 'peer', $2, $3, 9000000, 'held', 'schall', $4)
	`, targetID, `Music\Anetha\`+name, name,
		unaskedRegistrationSummary+" whether that is this track entered twice."); err != nil {
		t.Fatalf("record another held copy: %v", err)
	}
}

// judgingRunAfter reads when the one queued judge for this want is due, and
// fails when there is none.
func judgingRunAfter(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID uuid.UUID,
) time.Time {
	t.Helper()
	var runAfter time.Time
	if err := pool.QueryRow(ctx, `
		SELECT run_after FROM jobs
		WHERE kind = 'judge_want_copies'
		  AND status = 'queued'
		  AND payload->>'acquisition_target_id' = $1::text
	`, targetID).Scan(&runAfter); err != nil {
		t.Fatalf("read the queued judge for want %s: %v", targetID, err)
	}
	return runAfter
}

// takeTheJudge marks the queued judge done, the way the worker leaves it once
// the copies have been through validation again.
func takeTheJudge(ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		UPDATE jobs SET status = 'completed'
		WHERE kind = 'judge_want_copies' AND status = 'queued'
		  AND payload->>'acquisition_target_id' = $1::text
	`, targetID); err != nil {
		t.Fatalf("complete the judge: %v", err)
	}
}

// recheckState is what the want says about the wait it is in.
func recheckState(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID uuid.UUID,
) (string, int32, string) {
	t.Helper()
	var (
		reason   *string
		attempts int32
		summary  string
	)
	if err := pool.QueryRow(ctx, `
		SELECT recheck_reason, recheck_attempts, summary
		FROM acquisition_targets WHERE id = $1
	`, targetID).Scan(&reason, &attempts, &summary); err != nil {
		t.Fatalf("read the want: %v", err)
	}
	if reason == nil {
		return "", attempts, summary
	}
	return *reason, attempts, summary
}

// The schedule: an hour, six hours, then a day and a day. Every step queues one
// judge for that want and no more, and the want says it is waiting on the
// provider while it has patience left.
func TestAWantWaitingOnMusicBrainzIsAskedAgainOnABackingOffSchedule(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID := heldOnAnUnaskedRegistration(ctx, t, pool)

	// The clock moves to each look's own due time, because that is when the pass
	// that asks the next question runs.
	now := time.Now().UTC().Truncate(time.Second)
	want := []time.Duration{time.Hour, 6 * time.Hour, 24 * time.Hour, 24 * time.Hour}
	for step, wait := range want {
		queued, err := queries.RecheckAfterMusicBrainzSilence(ctx, targetID, now, "spent")
		if err != nil {
			t.Fatalf("RecheckAfterMusicBrainzSilence() error = %v", err)
		}
		if !queued {
			t.Fatalf("step %d queued nothing; want a judge %s from now", step+1, wait)
		}
		due := judgingRunAfter(ctx, t, pool, targetID)
		if delta := due.Sub(now); delta != wait {
			t.Errorf("step %d is due in %s, want %s", step+1, delta, wait)
		}
		reason, attempts, _ := recheckState(ctx, t, pool, targetID)
		if reason != RecheckForMusicBrainz {
			t.Errorf("step %d reason = %q, want %q", step+1, reason, RecheckForMusicBrainz)
		}
		if attempts != int32(step+1) {
			t.Errorf("step %d attempts = %d, want %d", step+1, attempts, step+1)
		}
		takeTheJudge(ctx, t, pool, targetID)
		now = due
	}
}

// A judging pass walks every held copy of a want and puts each one back through
// validation, so the want is asked to schedule its next look once per copy. It
// spends one step of the schedule, not one per copy: a want holding three of
// these copies was at the cap after one pass before this.
func TestOnePassOverSeveralHeldCopiesSpendsOneAttempt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID := heldOnAnUnaskedRegistration(ctx, t, pool)
	heldTimeoutCopy(ctx, t, pool, targetID, "04.flac")
	heldTimeoutCopy(ctx, t, pool, targetID, "05.flac")

	now := time.Now().UTC().Truncate(time.Second)
	for copies := 0; copies < 3; copies++ {
		queued, err := queries.RecheckAfterMusicBrainzSilence(ctx, targetID, now, "spent")
		if err != nil {
			t.Fatalf("RecheckAfterMusicBrainzSilence() error = %v", err)
		}
		if !queued {
			t.Fatalf("copy %d was told the want has stopped waiting", copies+1)
		}
	}

	_, attempts, _ := recheckState(ctx, t, pool, targetID)
	if attempts != 1 {
		t.Fatalf("attempts = %d after one pass over three copies, want 1", attempts)
	}
	if judged := countJudges(ctx, t, pool, targetID); judged != 1 {
		t.Fatalf("queued judges = %d, want the one this pass asks for", judged)
	}
	if due := judgingRunAfter(ctx, t, pool, targetID); !due.Equal(now.Add(time.Hour)) {
		t.Fatalf("due = %s, want the first step %s", due, now.Add(time.Hour))
	}
}

// The schedule runs out. The want stops waiting for a provider that has had two
// days to answer, queues nothing more, and says what is left to do.
func TestAWantThatHasAskedMusicBrainzEnoughBecomesAQuestion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID := heldOnAnUnaskedRegistration(ctx, t, pool)

	now := time.Now().UTC()
	for step := 0; step < len(recheckSchedule); step++ {
		if _, err := queries.RecheckAfterMusicBrainzSilence(ctx, targetID, now, "spent"); err != nil {
			t.Fatalf("RecheckAfterMusicBrainzSilence() error = %v", err)
		}
		now = judgingRunAfter(ctx, t, pool, targetID)
		takeTheJudge(ctx, t, pool, targetID)
	}

	queued, err := queries.RecheckAfterMusicBrainzSilence(
		ctx, targetID, now, "Listen to the copy and decide.")
	if err != nil {
		t.Fatalf("RecheckAfterMusicBrainzSilence() error = %v", err)
	}
	if queued {
		t.Fatal("a spent schedule queued another judge")
	}
	reason, attempts, summary := recheckState(ctx, t, pool, targetID)
	if reason != "" {
		t.Errorf("reason = %q, want a want that is waiting for nobody", reason)
	}
	if attempts != int32(len(recheckSchedule)) {
		t.Errorf("attempts = %d, want the spent schedule kept at %d",
			attempts, len(recheckSchedule))
	}
	if summary != "Listen to the copy and decide." {
		t.Errorf("summary = %q, want the sentence a person reads", summary)
	}
	var queuedJudges int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'judge_want_copies' AND status = 'queued'
		  AND payload->>'acquisition_target_id' = $1::text
	`, targetID).Scan(&queuedJudges); err != nil {
		t.Fatal(err)
	}
	if queuedJudges != 0 {
		t.Errorf("queued judges = %d, want none once the schedule is spent", queuedJudges)
	}
}

// A want somebody answered while its copy was being judged is left alone. The
// schedule is not written over an answer.
func TestASettledWantIsNotGivenARecheckSchedule(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID := heldOnAnUnaskedRegistration(ctx, t, pool)
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets
		SET status = 'not_wanted', not_wanted_at = now(), next_attempt_at = NULL
		WHERE id = $1
	`, targetID); err != nil {
		t.Fatal(err)
	}

	queued, err := queries.RecheckAfterMusicBrainzSilence(ctx, targetID, time.Now(), "spent")
	if err != nil {
		t.Fatalf("RecheckAfterMusicBrainzSilence() error = %v", err)
	}
	if queued {
		t.Fatal("a want that is no longer pending was given a schedule")
	}
	reason, attempts, _ := recheckState(ctx, t, pool, targetID)
	if reason != "" || attempts != 0 {
		t.Errorf("reason = %q, attempts = %d, want the answered want untouched", reason, attempts)
	}
}

// The want that stopped because the library calls its file another recording is
// waiting for the library, not for a clock. It says so, and it is given no time.
func TestAWantStoppedOnItsLibraryFileSaysWhatItIsWaitingFor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, _, _ := stoppedOnADisagreement(ctx, t, pool)
	if err := queries.StopLookingForAcquisitionTarget(
		ctx, targetID, "Your library calls that copy something else."); err != nil {
		t.Fatalf("StopLookingForAcquisitionTarget() error = %v", err)
	}

	var (
		reason string
		after  *time.Time
	)
	if err := pool.QueryRow(ctx, `
		SELECT recheck_reason, recheck_after FROM acquisition_targets WHERE id = $1
	`, targetID).Scan(&reason, &after); err != nil {
		t.Fatal(err)
	}
	if reason != RecheckForLibraryIdentity {
		t.Errorf("reason = %q, want %q", reason, RecheckForLibraryIdentity)
	}
	if after != nil {
		t.Errorf("recheck_after = %v, want no clock on a wait a fact ends", after)
	}
}

// The library identity changing is the fact that want was waiting for. It gets
// its next look back, and its patience with it.
func TestAChangedLibraryIdentityEndsTheWait(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, fileID, _ := stoppedOnADisagreement(ctx, t, pool)
	if err := queries.StopLookingForAcquisitionTarget(
		ctx, targetID, "Your library calls that copy something else."); err != nil {
		t.Fatalf("StopLookingForAcquisitionTarget() error = %v", err)
	}

	transaction, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	if err := RearmWantsHoldingFile(
		ctx, transaction, fileID, RearmedByAResolutionSummary); err != nil {
		t.Fatalf("RearmWantsHoldingFile() error = %v", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var (
		reason *string
		next   *time.Time
	)
	if err := pool.QueryRow(ctx, `
		SELECT recheck_reason, next_attempt_at FROM acquisition_targets WHERE id = $1
	`, targetID).Scan(&reason, &next); err != nil {
		t.Fatal(err)
	}
	if reason != nil {
		t.Errorf("reason = %q, want nothing left to wait for", *reason)
	}
	if next == nil {
		t.Error("the want has no next look; it was waiting for exactly this")
	}
}

// A settled file for the recording a want asks for is a witness its copies were
// judged without, so those copies are offered to the judge again. Before this,
// only an anchor or an unasked registration could offer them.
func TestASettledLibraryWitnessOffersAWantsCopiesAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	settledCopy(ctx, t, pool, targetID, requestID, AcquiredFileHeld, `{}`)

	offered, err := queries.CopiesToJudgeAgainForWant(ctx, targetID)
	if err != nil {
		t.Fatalf("CopiesToJudgeAgainForWant() error = %v", err)
	}
	if len(offered) != 0 {
		t.Fatalf("offered = %+v, want nothing while the library can say nothing", offered)
	}

	settledWitnessFor(ctx, t, pool, recordingOf(ctx, t, pool, targetID))

	offered, err = queries.CopiesToJudgeAgainForWant(ctx, targetID)
	if err != nil {
		t.Fatalf("CopiesToJudgeAgainForWant() error = %v", err)
	}
	if len(offered) != 1 || offered[0].RequestID != requestID {
		t.Fatalf("offered = %+v, want the copy the library can now speak to", offered)
	}
}

// The pass a person presses keeps its old bounds. It walks the whole queue, and
// a settled witness must not turn one press into a decode of most of it.
func TestTheWholeCollectionPassIgnoresASettledWitness(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	settledCopy(ctx, t, pool, targetID, requestID, AcquiredFileHeld, `{}`)
	settledWitnessFor(ctx, t, pool, recordingOf(ctx, t, pool, targetID))

	offered, err := queries.CopiesToJudgeAgain(ctx)
	if err != nil {
		t.Fatalf("CopiesToJudgeAgain() error = %v", err)
	}
	if len(offered) != 0 {
		t.Fatalf("offered = %+v, want the whole-collection pass unchanged", offered)
	}
}

// The library settling a file is a fact, so the wants holding copies of that
// recording are judged again without anybody pressing anything.
func TestASettledWitnessQueuesTheJudgesForItsRecording(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	settledCopy(ctx, t, pool, targetID, requestID, AcquiredFileHeld, `{}`)
	recordingID := recordingOf(ctx, t, pool, targetID)

	if err := queries.QueueJudgingForSettledWitness(ctx, recordingID, time.Now()); err != nil {
		t.Fatalf("QueueJudgingForSettledWitness() error = %v", err)
	}
	if queued := countJudges(ctx, t, pool, targetID); queued != 0 {
		t.Fatalf("queued = %d, want nothing while no file is settled for it", queued)
	}

	settledWitnessFor(ctx, t, pool, recordingID)
	if err := queries.QueueJudgingForSettledWitness(ctx, recordingID, time.Now()); err != nil {
		t.Fatalf("QueueJudgingForSettledWitness() error = %v", err)
	}
	if queued := countJudges(ctx, t, pool, targetID); queued != 1 {
		t.Fatalf("queued = %d, want one judge for the want the file can speak to", queued)
	}

	// Twice is once. A want already waiting for a judge is not queued a second.
	if err := queries.QueueJudgingForSettledWitness(ctx, recordingID, time.Now()); err != nil {
		t.Fatalf("QueueJudgingForSettledWitness() error = %v", err)
	}
	if queued := countJudges(ctx, t, pool, targetID); queued != 1 {
		t.Fatalf("queued = %d, want the second ask to add nothing", queued)
	}
}

// Learning that MusicBrainz merged two rows answers the question these copies
// stopped on, so they are put back through the grader.
func TestALearnedMergeJudgesTheWantsThatStoppedOnIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID := heldOnAnUnaskedRegistration(ctx, t, pool)
	recordingID := recordingOf(ctx, t, pool, targetID)

	if err := queries.RecordRecordingAlias(ctx, recordingID, uuid.New()); err != nil {
		t.Fatalf("RecordRecordingAlias() error = %v", err)
	}
	if queued := countJudges(ctx, t, pool, targetID); queued != 1 {
		t.Fatalf("queued = %d, want the want that stopped on the question judged again", queued)
	}

	// The same answer arriving twice is one answer. Nothing is queued for a row
	// already known.
	takeTheJudge(ctx, t, pool, targetID)
	if err := queries.RecordRecordingAlias(ctx, recordingID, uuid.New()); err != nil {
		t.Fatalf("RecordRecordingAlias() error = %v", err)
	}
	if queued := countJudges(ctx, t, pool, targetID); queued != 0 {
		t.Fatalf("queued = %d, want a merge already known to queue nothing", queued)
	}
}

// The other copy a merge answers: one MusicBrainz did answer about, whose two
// rows read alike, and which was held anyway because nothing had proved the audio
// is the recording the want names. That copy's summary says nothing about an
// unanswerable question, so it is found by the row its audio named — which is the
// other end of this merge.
func TestALearnedMergeJudgesAWantHeldOnTheRowItsAudioNamed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	namedByTheAudio := uuid.New()
	settledCopy(ctx, t, pool, targetID, requestID, AcquiredFileHeld,
		`{"acoustic":{"recordingIds":["`+namedByTheAudio.String()+`"]}}`)

	if err := queries.RecordRecordingAlias(
		ctx, recordingOf(ctx, t, pool, targetID), namedByTheAudio); err != nil {
		t.Fatalf("RecordRecordingAlias() error = %v", err)
	}

	if queued := countJudges(ctx, t, pool, targetID); queued != 1 {
		t.Fatalf("queued = %d, want the want holding that copy judged again", queued)
	}
}

// Both ends have to meet. A merge between the want's row and some third
// recording says nothing about the row this copy's audio named, so the copy is
// left where it is.
func TestAMergeThatMissesTheRowTheAudioNamedQueuesNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	settledCopy(ctx, t, pool, targetID, requestID, AcquiredFileHeld,
		`{"acoustic":{"recordingIds":["`+uuid.New().String()+`"]}}`)

	if err := queries.RecordRecordingAlias(
		ctx, recordingOf(ctx, t, pool, targetID), uuid.New()); err != nil {
		t.Fatalf("RecordRecordingAlias() error = %v", err)
	}

	if queued := countJudges(ctx, t, pool, targetID); queued != 0 {
		t.Fatalf("queued = %d, want nothing for a copy the merge says nothing about", queued)
	}
}

// A merge nothing was waiting on queues nothing. Only the wants holding a copy
// stopped on that exact question are put back.
func TestALearnedMergeLeavesEveryOtherWantAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	settledCopy(ctx, t, pool, targetID, requestID, AcquiredFileHeld, `{}`)
	recordingID := recordingOf(ctx, t, pool, targetID)

	if err := queries.RecordRecordingAlias(ctx, recordingID, uuid.New()); err != nil {
		t.Fatalf("RecordRecordingAlias() error = %v", err)
	}
	if queued := countJudges(ctx, t, pool, targetID); queued != 0 {
		t.Fatalf("queued = %d, want nothing for a copy that stopped on something else", queued)
	}
}

func recordingOf(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID uuid.UUID,
) uuid.UUID {
	t.Helper()
	var recordingID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT musicbrainz_recording_id FROM acquisition_targets WHERE id = $1
	`, targetID).Scan(&recordingID); err != nil {
		t.Fatal(err)
	}
	return recordingID
}

// settledWitnessFor puts a file the library has proven to be this recording on
// the disc: resolved, still here, and with a fingerprint to compare against.
func settledWitnessFor(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, recordingID uuid.UUID,
) uuid.UUID {
	t.Helper()
	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at, fingerprint,
		                           duration_ms, resolution_status)
		VALUES ($1, '/music/Anetha/witness.flac', 9000000, now(),
		        'AQABz0mUSEkSHV-OH0d_HG9wJXo0Hz-uI9-C_sfx4XmC', 210000, 'resolved')
	`, fileID); err != nil {
		t.Fatalf("seed the witness: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, method, summary
		)
		VALUES ($1, 'external', $2, 'fingerprint', 'the audio named it')
	`, fileID, recordingID); err != nil {
		t.Fatalf("seed what the library says the witness is: %v", err)
	}
	return fileID
}

func countJudges(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID uuid.UUID,
) int {
	t.Helper()
	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'judge_want_copies' AND status = 'queued'
		  AND payload->>'acquisition_target_id' = $1::text
	`, targetID).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	return queued
}

// MusicBrainz recovering ends the wait. The copy goes back through the grader,
// the provider answers, and whatever the copy turns out to be the want is no
// longer waiting on a provider. Before this the reason, the time and the spent
// attempts stayed on the row for ever.
func TestAnAnsweredRegistrationQuestionEndsTheWait(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID := heldOnAnUnaskedRegistration(ctx, t, pool)
	if _, err := queries.RecheckAfterMusicBrainzSilence(
		ctx, targetID, time.Now(), "spent"); err != nil {
		t.Fatalf("RecheckAfterMusicBrainzSilence() error = %v", err)
	}

	// Still stopped on the question, so nothing is taken off the want.
	if err := queries.EndTheMusicBrainzWaitIfAnswered(ctx, targetID); err != nil {
		t.Fatalf("EndTheMusicBrainzWaitIfAnswered() error = %v", err)
	}
	reason, attempts, _ := recheckState(ctx, t, pool, targetID)
	if reason != RecheckForMusicBrainz || attempts != 1 {
		t.Fatalf("reason = %q attempts = %d, want the want still waiting", reason, attempts)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_target_files
		SET verdict = 'discarded_audio', summary = 'The audio is a different recording.'
		WHERE acquisition_target_id = $1
	`, targetID); err != nil {
		t.Fatal(err)
	}
	if err := queries.EndTheMusicBrainzWaitIfAnswered(ctx, targetID); err != nil {
		t.Fatalf("EndTheMusicBrainzWaitIfAnswered() error = %v", err)
	}
	reason, attempts, _ = recheckState(ctx, t, pool, targetID)
	if reason != "" {
		t.Errorf("reason = %q, want nothing left to wait for", reason)
	}
	if attempts != 0 {
		t.Errorf("attempts = %d, want the patience back for a later question", attempts)
	}
	var after *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT recheck_after FROM acquisition_targets WHERE id = $1
	`, targetID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != nil {
		t.Errorf("recheck_after = %v, want no time on a wait that is over", after)
	}
}

// One of two copies answered leaves the want waiting. The wait is about the
// question, and the question is still on the row that still asks it.
func TestOneAnsweredCopyOfTwoLeavesTheWaitStanding(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID := heldOnAnUnaskedRegistration(ctx, t, pool)
	heldTimeoutCopy(ctx, t, pool, targetID, "04.flac")
	if _, err := queries.RecheckAfterMusicBrainzSilence(
		ctx, targetID, time.Now(), "spent"); err != nil {
		t.Fatalf("RecheckAfterMusicBrainzSilence() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_target_files
		SET verdict = 'discarded_audio', summary = 'The audio is a different recording.'
		WHERE acquisition_target_id = $1 AND file_name = '04.flac'
	`, targetID); err != nil {
		t.Fatal(err)
	}

	if err := queries.EndTheMusicBrainzWaitIfAnswered(ctx, targetID); err != nil {
		t.Fatalf("EndTheMusicBrainzWaitIfAnswered() error = %v", err)
	}
	reason, _, _ := recheckState(ctx, t, pool, targetID)
	if reason != RecheckForMusicBrainz {
		t.Fatalf("reason = %q, want the want still waiting on its other copy", reason)
	}
}
