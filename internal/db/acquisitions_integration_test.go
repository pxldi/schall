package db

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
)

// A recording asked for twice is one want. Playlists are re-imported, and a
// second import that created a second target would acquire the same song twice.
func TestAskingForOneRecordingTwiceRecordsOneTarget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	recordingID := uuid.New()
	first, created, err := queries.CreateAcquisitionTarget(ctx, wantParams("Glass", recordingID))
	if err != nil || !created {
		t.Fatalf("CreateAcquisitionTarget() = %v, %v, %v", first, created, err)
	}

	second, created, err := queries.CreateAcquisitionTarget(ctx, wantParams("Glass", recordingID))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}
	if created {
		t.Fatal("asking for the same recording again recorded a second want")
	}
	if second != first {
		t.Fatalf("second request = %s, want the existing target %s", second, first)
	}
}

// Two callers asking for the same recording at the same moment is what
// importing one playlist twice looks like. The loser waits on the unique index
// and must be told about the target the winner recorded — the statement's own
// fallback cannot see it, because it reads the snapshot it started with.
func TestTwoCallersAskingForOneRecordingAtOnceGetOneTarget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	recordingID := uuid.New()

	transaction, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	winner, created, err := New(transaction).CreateAcquisitionTarget(ctx, wantParams("Glass", recordingID))
	if err != nil || !created {
		t.Fatalf("CreateAcquisitionTarget() = %v, %v, %v", winner, created, err)
	}

	type answer struct {
		id      uuid.UUID
		created bool
		err     error
	}
	loser := make(chan answer, 1)
	go func() {
		id, created, err := New(pool).CreateAcquisitionTarget(ctx, wantParams("Glass", recordingID))
		loser <- answer{id, created, err}
	}()

	waitForBlockedInsert(ctx, t, pool, "acquisition_targets")
	if err := transaction.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	second := <-loser
	if second.err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", second.err)
	}
	if second.created {
		t.Fatal("both callers recorded a want for the same recording")
	}
	if second.id != winner {
		t.Fatalf("second caller was told about %s, want the recorded target %s", second.id, winner)
	}
}

// waitForBlockedInsert waits until another connection is stuck on a lock, which
// is how the second caller reaching the unique index first shows up.
func waitForBlockedInsert(ctx context.Context, t *testing.T, pool *pgxpool.Pool, table string) {
	t.Helper()

	for attempt := 0; attempt < 100; attempt++ {
		var blocked int
		if err := pool.QueryRow(ctx, `
			SELECT count(*)
			FROM pg_stat_activity
			WHERE datname = current_database()
			  AND wait_event_type = 'Lock'
			  AND query ILIKE '%' || $1 || '%'
		`, table).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the second caller never reached the unique index")
}

// Stopping is a decision, and a decision the user made outlives whatever
// created the target. Re-importing the playlist it came from must not quietly
// start pursuing it again.
func TestARecordingNobodyWantsStaysThatWayWhenItIsAskedForAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	recordingID := uuid.New()
	id, _, err := queries.CreateAcquisitionTarget(ctx, wantParams("Glass", recordingID))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}
	if _, err := queries.StopPursuingAcquisitionTarget(ctx, id, "no longer wanted"); err != nil {
		t.Fatalf("StopPursuingAcquisitionTarget() error = %v", err)
	}

	again, created, err := queries.CreateAcquisitionTarget(ctx, wantParams("Glass", recordingID))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}
	if created {
		t.Fatal("a recording somebody stopped pursuing was wanted again by asking for it")
	}
	target, err := queries.AcquisitionTarget(ctx, again)
	if err != nil {
		t.Fatalf("AcquisitionTarget() error = %v", err)
	}
	if target.Status != "not_wanted" {
		t.Fatalf("status = %s, want not_wanted", target.Status)
	}
}

// A decision may be taken back by the person who made it, exactly as a manual
// file identity may be withdrawn — and taking it back has to put the target
// back in the queue, or it would be wanted and never looked for.
func TestWantingATargetAgainSchedulesIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	id, _, err := queries.CreateAcquisitionTarget(ctx, wantParams("Glass", uuid.New()))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}
	if _, err := queries.StopPursuingAcquisitionTarget(ctx, id, "no longer wanted"); err != nil {
		t.Fatalf("StopPursuingAcquisitionTarget() error = %v", err)
	}

	due := time.Now().UTC().Truncate(time.Second)
	target, err := queries.PursueAcquisitionTargetAgain(
		ctx, id, "waiting to be resolved", "wanted again", due,
	)
	if err != nil {
		t.Fatalf("PursueAcquisitionTargetAgain() error = %v", err)
	}
	if target.Status != "pending" {
		t.Fatalf("status = %s, want pending", target.Status)
	}
	if !target.NextAttemptAt.Valid || !target.NextAttemptAt.Time.Equal(due) {
		t.Fatalf("next attempt = %v, want %v", target.NextAttemptAt, due)
	}
	if target.NotWantedAt.Valid {
		t.Fatal("a target wanted again still says when it stopped being wanted")
	}
}

// The requirement the whole entity exists for: an attempt that found nothing
// leaves a target wanted and scheduled, never failed. Peers come and go, and
// the copy that was not there this evening is often there tomorrow.
func TestAnAttemptThatFoundNothingLeavesTheTargetWantedAndScheduled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	id, _, err := queries.CreateAcquisitionTarget(ctx, wantParams("Glass", uuid.New()))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}

	nextAttempt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	if err := queries.RequeueAcquisitionTarget(
		ctx, id, "none", "not in your library yet", "nobody had it", "", nextAttempt,
	); err != nil {
		t.Fatalf("RequeueAcquisitionTarget() error = %v", err)
	}

	target, err := queries.AcquisitionTarget(ctx, id)
	if err != nil {
		t.Fatalf("AcquisitionTarget() error = %v", err)
	}
	if target.Status != "pending" {
		t.Fatalf("status = %s, want pending", target.Status)
	}
	if target.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", target.Attempts)
	}
	if !target.NextAttemptAt.Valid || !target.NextAttemptAt.Time.Equal(nextAttempt) {
		t.Fatalf("next attempt = %v, want %v", target.NextAttemptAt, nextAttempt)
	}
}

// The record of what an attempt came to is written with the state it produced,
// in one statement, so a target can never say it was tried without saying what
// happened.
func TestEachAttemptIsRecordedBesideTheTarget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	id, _, err := queries.CreateAcquisitionTarget(ctx, wantParams("Glass", uuid.New()))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if err := queries.RequeueAcquisitionTarget(
			ctx, id, "none", "not in your library yet", "nobody had it", "",
			time.Now().Add(time.Hour),
		); err != nil {
			t.Fatalf("RequeueAcquisitionTarget() error = %v", err)
		}
	}

	rows, err := pool.Query(ctx, `
		SELECT attempt, outcome FROM acquisition_target_attempts
		WHERE acquisition_target_id = $1
		ORDER BY attempt
	`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	attempts := make([]int, 0, 2)
	for rows.Next() {
		var attempt int
		var outcome string
		if err := rows.Scan(&attempt, &outcome); err != nil {
			t.Fatal(err)
		}
		if outcome != "none" {
			t.Fatalf("outcome = %s, want none", outcome)
		}
		attempts = append(attempts, attempt)
	}
	if len(attempts) != 2 || attempts[0] != 1 || attempts[1] != 2 {
		t.Fatalf("recorded attempts = %v, want [1 2]", attempts)
	}
}

// A target somebody stopped pursuing is not attempted again, whatever a sweep
// that was already running thinks. The user's decision is the later fact.
func TestAnAttemptIsRefusedOnceTheTargetIsNoLongerWanted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	id, _, err := queries.CreateAcquisitionTarget(ctx, wantParams("Glass", uuid.New()))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}
	if _, err := queries.StopPursuingAcquisitionTarget(ctx, id, "no longer wanted"); err != nil {
		t.Fatalf("StopPursuingAcquisitionTarget() error = %v", err)
	}

	err = queries.RequeueAcquisitionTarget(
		ctx, id, "none", "not in your library yet", "nobody had it", "",
		time.Now().Add(time.Hour),
	)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("RequeueAcquisitionTarget() error = %v, want %v", err, pgx.ErrNoRows)
	}
}

// A file whose identity was decided is a file Schall can answer for, and it
// satisfies the want without anybody searching for a copy that is already
// there.
func TestAResolvedIdentitySatisfiesAWantForThatRecording(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	recordingID := uuid.New()
	fileID := seedLibraryFileWithIdentity(ctx, t, pool, "/music/glass.flac", recordingID)

	owned, err := queries.OwnedFileForRecording(ctx, recordingID)
	if err != nil {
		t.Fatalf("OwnedFileForRecording() error = %v", err)
	}
	if !owned.Valid || owned.UUID != fileID {
		t.Fatalf("owned file = %v, want %s", owned, fileID)
	}
}

// The identifier a file carries in its own tags is not a decision about what it
// is: an uncontradicted tag is what identity resolution grades, and reading it
// here would answer a settled question with weaker evidence.
func TestAFileTaggedWithARecordingDoesNotSatisfyAWant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	recordingID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at, musicbrainz_recording_id)
		VALUES ($1, '/music/tagged.flac', 1024, now(), $2)
	`, uuid.New(), recordingID); err != nil {
		t.Fatalf("seed library file: %v", err)
	}

	owned, err := New(pool).OwnedFileForRecording(ctx, recordingID)
	if err != nil {
		t.Fatalf("OwnedFileForRecording() error = %v", err)
	}
	if owned.Valid {
		t.Fatal("a file that merely claims the recording in its tags satisfied the want")
	}
}

// A file the library no longer has is not owned music. Its identity is kept —
// the file may come back — but a want it used to satisfy is wanted again.
func TestAMissingFileDoesNotSatisfyAWant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	recordingID := uuid.New()
	fileID := seedLibraryFileWithIdentity(ctx, t, pool, "/music/gone.flac", recordingID)
	if _, err := pool.Exec(ctx,
		`UPDATE library_files SET missing_at = now() WHERE id = $1`, fileID); err != nil {
		t.Fatal(err)
	}

	owned, err := New(pool).OwnedFileForRecording(ctx, recordingID)
	if err != nil {
		t.Fatalf("OwnedFileForRecording() error = %v", err)
	}
	if owned.Valid {
		t.Fatal("a file the library no longer has satisfied the want")
	}
}

// A want satisfied by a file the library already holds is finished, and says
// which file finished it.
func TestSettlingATargetNamesTheFileThatSatisfiedIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	recordingID := uuid.New()
	fileID := seedLibraryFileWithIdentity(ctx, t, pool, "/music/glass.flac", recordingID)
	id, _, err := queries.CreateAcquisitionTarget(ctx, wantParams("Glass", recordingID))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}

	if err := queries.SettleAcquiredTarget(ctx, id, fileID, "in your library", "already held"); err != nil {
		t.Fatalf("SettleAcquiredTarget() error = %v", err)
	}
	target, err := queries.AcquisitionTarget(ctx, id)
	if err != nil {
		t.Fatalf("AcquisitionTarget() error = %v", err)
	}
	if target.Status != "acquired" {
		t.Fatalf("status = %s, want acquired", target.Status)
	}
	if !target.AcquiredLibraryFileID.Valid || target.AcquiredLibraryFileID.UUID != fileID {
		t.Fatalf("acquired file = %v, want %s", target.AcquiredLibraryFileID, fileID)
	}
	if target.NextAttemptAt.Valid {
		t.Fatal("an acquired target is still scheduled for another attempt")
	}
}

// One sweeper at a time, and a want created now must not wait out a sweep that
// was scheduled for three days' time.
func TestQueueingASweepMovesAnExistingOneEarlier(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	later := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)
	sooner := time.Now().Add(time.Minute).UTC().Truncate(time.Second)
	if err := queries.QueueAcquisitionSweep(ctx, later); err != nil {
		t.Fatalf("QueueAcquisitionSweep() error = %v", err)
	}
	if err := queries.QueueAcquisitionSweep(ctx, sooner); err != nil {
		t.Fatalf("QueueAcquisitionSweep() error = %v", err)
	}

	var sweeps int
	var runAfter time.Time
	if err := pool.QueryRow(ctx, `
		SELECT count(*), min(run_after)
		FROM jobs
		WHERE kind = 'sweep_acquisition_targets' AND status IN ('queued', 'running')
	`).Scan(&sweeps, &runAfter); err != nil {
		t.Fatal(err)
	}
	if sweeps != 1 {
		t.Fatalf("%d sweepers are queued, want exactly one", sweeps)
	}
	if !runAfter.Equal(sooner) {
		t.Fatalf("sweep runs after %v, want %v", runAfter, sooner)
	}
}

// A schedule belongs to a target that is waiting for its next chance. One that
// is finished with carrying a next attempt would be a second opinion about
// what happens to it.
func TestAFinishedTargetCannotBeScheduled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	id, _, err := New(pool).CreateAcquisitionTarget(ctx, wantParams("Glass", uuid.New()))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}
	_, err = pool.Exec(ctx, `
		UPDATE acquisition_targets
		SET status = 'not_wanted', not_wanted_at = now(), next_attempt_at = now()
		WHERE id = $1
	`, id)
	if err == nil {
		t.Fatal("a target nobody wants was left scheduled for another attempt")
	}
}

func wantParams(title string, recordingID uuid.UUID) CreateAcquisitionTargetParams {
	return CreateAcquisitionTargetParams{
		Origin:                 "manual",
		EntryArtist:            "Portishead",
		EntryTitle:             title,
		MusicBrainzRecordingID: uuid.NullUUID{UUID: recordingID, Valid: true},
		Summary:                "wanted",
	}
}

func seedLibraryFileWithIdentity(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, path string, recordingID uuid.UUID,
) uuid.UUID {
	t.Helper()

	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, $2, 1024, now())
	`, fileID, path); err != nil {
		t.Fatalf("seed library file: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, method, summary
		)
		VALUES ($1, 'external', $2, 'recording-id', 'proven by identifier')
	`, fileID, recordingID); err != nil {
		t.Fatalf("seed file identity: %v", err)
	}
	return fileID
}

// The point of the wrong-target outcome: the answer is remembered as rejected and
// the entry goes back to being looked up. Either half alone is broken — remembering
// without re-opening leaves the want pointed at a recording its owner disowned, and
// re-opening without remembering hands the resolver the same wrong answer to find.
func TestRejectingAResolutionRemembersItAndAsksAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	recordingID := uuid.New()
	id, _, err := queries.CreateAcquisitionTarget(ctx, wantParams("Glass", recordingID))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}

	due := time.Now().UTC().Truncate(time.Second)
	target, err := queries.RejectAcquisitionTargetResolution(
		ctx, id, "being looked up again", "a person said so", due,
	)
	if err != nil {
		t.Fatalf("RejectAcquisitionTargetResolution() error = %v", err)
	}
	if target.Status != "unresolved" || target.MusicBrainzRecordingID.Valid {
		t.Fatalf("target = %s carrying %v, want it unresolved and carrying nothing",
			target.Status, target.MusicBrainzRecordingID)
	}

	excluded, err := queries.RejectedTargetResolutions(ctx, id)
	if err != nil {
		t.Fatalf("RejectedTargetResolutions() error = %v", err)
	}
	if len(excluded) != 1 || excluded[0] != recordingID {
		t.Fatalf("excluded = %v, want the rejected recording %s", excluded, recordingID)
	}
}

// Only a person may rule a recording out. An exclusion narrows what the resolver
// may conclude from, so one Schall wrote itself would be it discarding its own
// candidates and then concluding from what it had left.
func TestOnlyAPersonCanRuleARecordingOut(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	id, _, err := queries.CreateAcquisitionTarget(ctx, wantParams("Glass", uuid.New()))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_target_rejected_recordings (
			acquisition_target_id, musicbrainz_recording_id, decided_by
		)
		VALUES ($1, $2, 'schall')
	`, id, uuid.New()); err == nil {
		t.Fatal("Schall was allowed to rule out a recording for itself")
	}
}

// Rejecting something nobody has answered is refused with a sentence rather than
// silently re-opening a want that was never resolved.
func TestRejectingAResolutionThatDoesNotExistIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	id, _, err := queries.CreateAcquisitionTarget(ctx, CreateAcquisitionTargetParams{
		Origin: "manual", EntryArtist: "Portishead", EntryTitle: "Glass", Summary: "waiting",
	})
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}

	if _, err := queries.RejectAcquisitionTargetResolution(
		ctx, id, "being looked up again", "a person said so", time.Now(),
	); !errors.Is(err, ErrNothingToReject) {
		t.Fatalf("rejecting = %v, want ErrNothingToReject", err)
	}
}

// asking stores an entry that fits several recordings, stopped with the answers
// it could have — the state askTheUser leaves behind.
func asking(ctx context.Context, t *testing.T, pool *pgxpool.Pool) (*Queries, uuid.UUID, uuid.UUID) {
	t.Helper()
	targetID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets (
			id, origin, entry_artist, entry_title, status, attempts, summary
		)
		VALUES ($1, 'playlist', 'Randomer', 'Sleep Of Reason', 'unresolved', 0, 'Waiting.')
	`, targetID); err != nil {
		t.Fatalf("seed the entry: %v", err)
	}
	queries := New(pool)
	wanted, other := uuid.New(), uuid.New()
	if _, err := queries.SendAcquisitionTargetToReview(ctx, ReviewAcquisitionTargetParams{
		ID:      targetID,
		Summary: "2 recordings fit this entry equally well.",
		Detail:  "Two recordings fit.",
		Candidates: []AcquisitionTargetCandidateRow{
			{
				MusicBrainzRecordingID: wanted, ArtistName: "Randomer",
				TrackTitle: "Sleep Of Reason", Rank: 1,
				Agrees: []string{"artist", "title"}, Differs: []string{},
			},
			{
				MusicBrainzRecordingID: other, ArtistName: "Randomer",
				TrackTitle: "Sleep Of Reason", Rank: 2,
				Agrees: []string{"artist", "title"}, Differs: []string{},
			},
		},
	}); err != nil {
		t.Fatalf("ask the question: %v", err)
	}
	return queries, targetID, wanted
}

// The answer a person gives is the resolution. It is written as a manual one,
// because nothing proved it: the evidence is exactly what could not choose.
func TestChoosingAnOfferedRecordingResolvesTheWantByHand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, wanted := asking(ctx, t, pool)

	target, err := queries.ChooseAcquisitionTargetResolution(ctx, ResolveAcquisitionTargetParams{
		ID: targetID, MusicBrainzRecordingID: wanted,
		Summary: "You chose which recording this entry names.",
		Detail:  "A person chose it.", NextAttemptAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("ChooseAcquisitionTargetResolution() error = %v", err)
	}
	if !target.MusicBrainzRecordingID.Valid || target.MusicBrainzRecordingID.UUID != wanted {
		t.Fatalf("recording = %v, want the one that was chosen (%s)",
			target.MusicBrainzRecordingID, wanted)
	}
	if target.Status != "pending" {
		t.Fatalf("status = %q, want pending: an answered want is one to look for", target.Status)
	}
	if target.ResolutionMethod.String != "manual" {
		t.Fatalf("method = %q, want manual", target.ResolutionMethod.String)
	}
	if target.ReviewReason.Valid {
		t.Fatalf("review reason = %q, want none: the question has been answered",
			target.ReviewReason.String)
	}

	// The question is gone with it. Candidates left behind would offer an answer
	// to something nobody is being asked any more.
	left, err := queries.AcquisitionTargetCandidates(ctx, targetID)
	if err != nil {
		t.Fatalf("read the candidates: %v", err)
	}
	if len(left) != 0 {
		t.Fatalf("candidates = %d, want none once the question is answered", len(left))
	}
}

// Only the recordings this want offered can answer it. Anything else is a want
// for music nobody weighed the evidence for, which is a new want and not an
// answer to this one.
func TestChoosingARecordingThatWasNotOfferedIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, _ := asking(ctx, t, pool)

	_, err := queries.ChooseAcquisitionTargetResolution(ctx, ResolveAcquisitionTargetParams{
		ID: targetID, MusicBrainzRecordingID: uuid.New(),
		Summary: "Chosen.", Detail: "A person chose it.", NextAttemptAt: time.Now(),
	})
	if !errors.Is(err, ErrNotOfferedForReview) {
		t.Fatalf("error = %v, want ErrNotOfferedForReview", err)
	}

	target, err := queries.AcquisitionTarget(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if target.Status != "awaiting_review" || target.MusicBrainzRecordingID.Valid {
		t.Fatalf("target = %q with recording %v, want it still waiting on an answer",
			target.Status, target.MusicBrainzRecordingID)
	}
}

// A want that is not waiting on an answer cannot be answered. Something already
// decided about it, and an answer is never re-asked.
func TestChoosingForAWantThatIsNotWaitingIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, wanted := asking(ctx, t, pool)

	if _, err := queries.StopPursuingAcquisitionTarget(ctx, targetID, "No longer wanted."); err != nil {
		t.Fatalf("stop pursuing it: %v", err)
	}

	_, err := queries.ChooseAcquisitionTargetResolution(ctx, ResolveAcquisitionTargetParams{
		ID: targetID, MusicBrainzRecordingID: wanted,
		Summary: "Chosen.", Detail: "A person chose it.", NextAttemptAt: time.Now(),
	})
	if !errors.Is(err, ErrNotOfferedForReview) {
		t.Fatalf("error = %v, want the answer refused", err)
	}
}

// Two entries can name one recording, and one recording is one want. An answer
// that lands on a recording another target already carries merges into it rather
// than making a second want for the same music.
func TestChoosingARecordingAnotherWantAlreadyCarriesMergesIntoIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, wanted := asking(ctx, t, pool)

	survivor := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets (
			id, origin, entry_artist, entry_title, musicbrainz_recording_id,
			status, resolution_method, resolved_at, next_attempt_at, attempts, summary
		)
		VALUES ($1, 'manual', 'Randomer', 'Sleep Of Reason', $2, 'pending', 'isrc',
		        now(), now(), 0, 'Wanted.')
	`, survivor, wanted); err != nil {
		t.Fatalf("seed the want that already carries it: %v", err)
	}

	target, err := queries.ChooseAcquisitionTargetResolution(ctx, ResolveAcquisitionTargetParams{
		ID: targetID, MusicBrainzRecordingID: wanted,
		Summary: "Chosen.", SupersededSummary: "Already wanted.",
		SupersededNotWanted: "You said you did not want it.",
		Detail:              "A person chose it.", NextAttemptAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("ChooseAcquisitionTargetResolution() error = %v", err)
	}
	if target.Status != "superseded" {
		t.Fatalf("status = %q, want superseded", target.Status)
	}
	if !target.SupersededByID.Valid || target.SupersededByID.UUID != survivor {
		t.Fatalf("superseded by = %v, want the want that already carries the recording (%s)",
			target.SupersededByID, survivor)
	}
}

// The sweeper walks what is due rather than every want ever recorded: a want
// scheduled for tomorrow is invisible to it today, which is what keeps a pass
// proportional to the work rather than to the history.
func TestOnlyAWantWhoseTurnHasComeIsDue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	due, _, err := queries.CreateAcquisitionTarget(ctx, wantParams("Glass", uuid.New()))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}
	tomorrow, _, err := queries.CreateAcquisitionTarget(ctx, wantParams("Roads", uuid.New()))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET next_attempt_at = now() + interval '1 day' WHERE id = $1
	`, tomorrow); err != nil {
		t.Fatal(err)
	}

	targets, err := queries.DueAcquisitionTargets(ctx, time.Now(), 10)
	if err != nil {
		t.Fatalf("DueAcquisitionTargets() error = %v", err)
	}
	if len(targets) != 1 || targets[0].ID != due {
		t.Fatalf("due = %v, want only the want whose turn it is (%s)", targets, due)
	}
}

// A pass is bounded, so the oldest schedule goes first: a want that has been
// waiting longest has the strongest claim on the next attempt.
func TestTheOldestScheduleIsAttemptedFirst(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	recent, _, err := queries.CreateAcquisitionTarget(ctx, wantParams("Glass", uuid.New()))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}
	waiting, _, err := queries.CreateAcquisitionTarget(ctx, wantParams("Roads", uuid.New()))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET next_attempt_at = now() - interval '1 day' WHERE id = $1
	`, waiting); err != nil {
		t.Fatal(err)
	}

	targets, err := queries.DueAcquisitionTargets(ctx, time.Now(), 1)
	if err != nil {
		t.Fatalf("DueAcquisitionTargets() error = %v", err)
	}
	if len(targets) != 1 || targets[0].ID != waiting {
		t.Fatalf("first due = %v, want the want that has waited longest (%s, not %s)",
			targets, waiting, recent)
	}
}

// Resolving an entry costs rate-limited provider requests while looking for an
// owned copy costs a query, so the two queues are asked for separately. An entry
// nobody has resolved yet belongs to the first.
func TestAnEntryWaitingToBeResolvedIsDueToBeResolved(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	id, _, err := queries.CreateAcquisitionTarget(ctx, CreateAcquisitionTargetParams{
		Origin: "playlist", EntryArtist: "Portishead", EntryTitle: "Glass", Summary: "waiting",
	})
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}

	entries, err := queries.DueUnresolvedTargets(ctx, time.Now(), 10)
	if err != nil {
		t.Fatalf("DueUnresolvedTargets() error = %v", err)
	}
	if len(entries) != 1 || entries[0].ID != id {
		t.Fatalf("due entries = %v, want the one waiting to be resolved (%s)", entries, id)
	}
}

// And not to the second. Searching a peer for a recording nothing has named yet
// is a search with nothing to prove a copy against.
func TestAnEntryWaitingToBeResolvedIsNotDueForACopy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	if _, _, err := queries.CreateAcquisitionTarget(ctx, CreateAcquisitionTargetParams{
		Origin: "playlist", EntryArtist: "Portishead", EntryTitle: "Glass", Summary: "waiting",
	}); err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}

	wants, err := queries.DueAcquisitionTargets(ctx, time.Now(), 10)
	if err != nil {
		t.Fatalf("DueAcquisitionTargets() error = %v", err)
	}
	if len(wants) != 0 {
		t.Fatalf("due wants = %v, want an unresolved entry left out of the copy queue", wants)
	}
}

// The sweeper asks when to wake rather than running on a timer, so the earliest
// schedule of every want is what it is told.
func TestTheNextCopyIsDueWhenTheEarliestWantIs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	if _, _, err := queries.CreateAcquisitionTarget(ctx, wantParams("Glass", uuid.New())); err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}
	soon, _, err := queries.CreateAcquisitionTarget(ctx, wantParams("Roads", uuid.New()))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}
	earliest := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET next_attempt_at = $2 WHERE id = $1
	`, soon, earliest); err != nil {
		t.Fatal(err)
	}

	due, err := queries.NextAcquisitionTargetDue(ctx)
	if err != nil {
		t.Fatalf("NextAcquisitionTargetDue() error = %v", err)
	}
	if !due.Valid || !due.Time.Equal(earliest) {
		t.Fatalf("next due = %v, want the earliest schedule %v", due, earliest)
	}
}

// Nothing scheduled is what lets the sweeper stop rather than wake up forever to
// find nothing to do.
func TestNothingWaitingForACopyLeavesNoNextTime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	due, err := New(pool).NextAcquisitionTargetDue(ctx)
	if err != nil {
		t.Fatalf("NextAcquisitionTargetDue() error = %v", err)
	}
	if due.Valid {
		t.Fatalf("next due = %v, want nothing scheduled", due.Time)
	}
}

// The entries waiting to be resolved keep their own schedule, because only an
// installation that has something to resolve them with should be woken for them.
func TestTheNextResolutionIsDueWhenTheEarliestEntryIs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	id, _, err := queries.CreateAcquisitionTarget(ctx, CreateAcquisitionTargetParams{
		Origin: "playlist", EntryArtist: "Portishead", EntryTitle: "Glass", Summary: "waiting",
	})
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}
	earliest := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET next_attempt_at = $2 WHERE id = $1
	`, id, earliest); err != nil {
		t.Fatal(err)
	}

	due, err := queries.NextUnresolvedTargetDue(ctx)
	if err != nil {
		t.Fatalf("NextUnresolvedTargetDue() error = %v", err)
	}
	if !due.Valid || !due.Time.Equal(earliest) {
		t.Fatalf("next due = %v, want the earliest entry's schedule %v", due, earliest)
	}
}

// The wants list narrows to one state, and counts that state past the end of the
// page.
func TestListingWantsNarrowsToOneState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	wanted, _, err := queries.CreateAcquisitionTarget(ctx, wantParams("Glass", uuid.New()))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}
	stopped, _, err := queries.CreateAcquisitionTarget(ctx, wantParams("Roads", uuid.New()))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}
	if _, err := queries.StopPursuingAcquisitionTarget(ctx, stopped, "no longer wanted"); err != nil {
		t.Fatalf("StopPursuingAcquisitionTarget() error = %v", err)
	}

	page, err := queries.ListAcquisitionTargets(ctx, ListAcquisitionTargetsParams{
		Statuses: []string{"pending"}, Limit: 20,
	})
	if err != nil {
		t.Fatalf("ListAcquisitionTargets() error = %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != wanted {
		t.Fatalf("page = %d of %d, want only the want still being pursued (%s)",
			len(page.Items), page.Total, wanted)
	}
}

// The list has to tell a want being looked for from one that has its music and
// is waiting on a person, because a row that says it is looking when nothing is
// looking is a row nobody can trust.
func TestListingWantsSaysWhichOnesAreWaitingOnAPerson(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	looking, _, err := queries.CreateAcquisitionTarget(ctx, wantParams("Glass", uuid.New()))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}
	scannedAcceptedCopy(ctx, t, pool, queries, targetID, requestID,
		&AcquiredFileEvidence{Name: "03.flac", Method: "fingerprint", Confidence: 1})
	// The copy is on the disc and the want stopped on it, which is the state the
	// disagreement leaves behind.
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_target_files
		SET library_file_id = (SELECT library_file_id FROM downloads
		                       WHERE download_request_id = $2)
		WHERE acquisition_target_id = $1 AND verdict = 'accepted'
	`, targetID, requestID); err != nil {
		t.Fatal(err)
	}
	if err := queries.StopLookingForAcquisitionTarget(
		ctx, targetID, "your library calls that file a different recording"); err != nil {
		t.Fatalf("StopLookingForAcquisitionTarget() error = %v", err)
	}

	page, err := queries.ListAcquisitionTargets(ctx, ListAcquisitionTargetsParams{
		Statuses: []string{"pending"}, Limit: 20,
	})
	if err != nil {
		t.Fatalf("ListAcquisitionTargets() error = %v", err)
	}
	holding := map[uuid.UUID]bool{}
	for _, item := range page.Items {
		holding[item.ID] = item.HoldsAnImportedCopy
	}
	if !holding[targetID] {
		t.Errorf("want %s reads as still looking, and its copy is already a file", targetID)
	}
	if holding[looking] {
		t.Errorf("want %s holds no copy and reads as though it does", looking)
	}
}

// A want being looked for is in one of three states — waiting to be resolved,
// waiting for a copy, or being searched for right now — and to whoever asked
// for the music those are one pile. The list takes all three at once so that
// the screen showing it is one read with one count.
func TestListingWantsTakesSeveralStatesAtOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	looking := make([]uuid.UUID, 0, 2)
	for _, title := range []string{"Glass", "Roads"} {
		id, _, err := queries.CreateAcquisitionTarget(ctx, wantParams(title, uuid.New()))
		if err != nil {
			t.Fatalf("CreateAcquisitionTarget() error = %v", err)
		}
		looking = append(looking, id)
	}
	// One of them is out searching this minute, which is the same pile. A want
	// in flight carries no schedule — the schedule is what says when the next
	// attempt is due, and this one is the attempt.
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets
		SET status = 'searching', next_attempt_at = NULL
		WHERE id = $1
	`, looking[0]); err != nil {
		t.Fatal(err)
	}
	stopped, _, err := queries.CreateAcquisitionTarget(ctx, wantParams("Wandering Star", uuid.New()))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}
	if _, err := queries.StopPursuingAcquisitionTarget(ctx, stopped, "no longer wanted"); err != nil {
		t.Fatalf("StopPursuingAcquisitionTarget() error = %v", err)
	}

	page, err := queries.ListAcquisitionTargets(ctx, ListAcquisitionTargetsParams{
		Statuses: []string{"unresolved", "pending", "searching"}, Limit: 20,
	})
	if err != nil {
		t.Fatalf("ListAcquisitionTargets() error = %v", err)
	}
	if page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("page = %d of %d, want the two wants still being looked for",
			len(page.Items), page.Total)
	}
	for _, item := range page.Items {
		if item.ID == stopped {
			t.Fatalf("the want somebody stopped (%s) is in a list of wants being looked for", stopped)
		}
	}
}

// An empty state is every want, whatever it is in: the list is a view of the
// whole queue unless it is asked to narrow.
func TestListingWantsWithoutAStateReturnsEveryWant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	for _, title := range []string{"Glass", "Roads"} {
		if _, _, err := queries.CreateAcquisitionTarget(ctx, wantParams(title, uuid.New())); err != nil {
			t.Fatalf("CreateAcquisitionTarget() error = %v", err)
		}
	}

	page, err := queries.ListAcquisitionTargets(ctx, ListAcquisitionTargetsParams{Limit: 20})
	if err != nil {
		t.Fatalf("ListAcquisitionTargets() error = %v", err)
	}
	if page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("page = %d of %d, want both wants", len(page.Items), page.Total)
	}
}

// MusicBrainz gains recordings, so an entry nothing matches today may match in
// three days. It stays unresolved and comes back on the ladder rather than
// becoming a question nobody can answer.
func TestAResolutionThatFoundNothingLeavesTheEntryWaitingToBeAskedAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	id, _, err := queries.CreateAcquisitionTarget(ctx, CreateAcquisitionTargetParams{
		Origin: "playlist", EntryArtist: "Portishead", EntryTitle: "Glass", Summary: "waiting",
	})
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}

	nextAttempt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	if err := queries.RequeueUnresolvedTarget(
		ctx, id, "none", "nothing matches it yet", "no recording fits", "", nextAttempt,
	); err != nil {
		t.Fatalf("RequeueUnresolvedTarget() error = %v", err)
	}

	target, err := queries.AcquisitionTarget(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if target.Status != "unresolved" || target.Attempts != 1 {
		t.Fatalf("target = %s on attempt %d, want it unresolved having spent one",
			target.Status, target.Attempts)
	}
	if !target.NextAttemptAt.Valid || !target.NextAttemptAt.Time.Equal(nextAttempt) {
		t.Fatalf("next attempt = %v, want %v", target.NextAttemptAt, nextAttempt)
	}
}

// A resolution attempt against an entry somebody has since decided about is
// dropped rather than applied over their decision.
func TestAResolutionAttemptIsRefusedOnceTheEntryIsNoLongerUnresolved(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	id, _, err := queries.CreateAcquisitionTarget(ctx, CreateAcquisitionTargetParams{
		Origin: "playlist", EntryArtist: "Portishead", EntryTitle: "Glass", Summary: "waiting",
	})
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}
	if _, err := queries.StopPursuingAcquisitionTarget(ctx, id, "no longer wanted"); err != nil {
		t.Fatalf("StopPursuingAcquisitionTarget() error = %v", err)
	}

	err = queries.RequeueUnresolvedTarget(
		ctx, id, "none", "nothing matches it yet", "no recording fits", "",
		time.Now().Add(time.Hour),
	)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("RequeueUnresolvedTarget() error = %v, want %v", err, pgx.ErrNoRows)
	}
}

// A provider refusing to be asked says nothing about the music, so it costs the
// entry nothing: the ladder does not move, only the time it comes back at.
func TestDeferringAnEntryMovesItsTurnWithoutSpendingAnAttempt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	id, _, err := queries.CreateAcquisitionTarget(ctx, CreateAcquisitionTargetParams{
		Origin: "playlist", EntryArtist: "Portishead", EntryTitle: "Glass", Summary: "waiting",
	})
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}

	nextAttempt := time.Now().Add(5 * time.Minute).UTC().Truncate(time.Second)
	if err := queries.DeferUnresolvedTarget(
		ctx, id, "MusicBrainz is busy.", "429 too many requests", nextAttempt,
	); err != nil {
		t.Fatalf("DeferUnresolvedTarget() error = %v", err)
	}

	target, err := queries.AcquisitionTarget(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if target.Attempts != 0 {
		t.Fatalf("attempts = %d, want none spent on a question nobody asked", target.Attempts)
	}
	if !target.NextAttemptAt.Valid || !target.NextAttemptAt.Time.Equal(nextAttempt) {
		t.Fatalf("next attempt = %v, want %v", target.NextAttemptAt, nextAttempt)
	}
	if target.LastError.String != "429 too many requests" {
		t.Fatalf("last error = %q, want the refusal readable", target.LastError.String)
	}
}

// One live target per recording, and the database is what holds it: without the
// index a second want for the same music is a second acquisition of it.
func TestASecondLiveWantForOneRecordingIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	recordingID := uuid.New()
	if _, _, err := New(pool).CreateAcquisitionTarget(ctx, wantParams("Glass", recordingID)); err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets (
			origin, entry_artist, entry_title, musicbrainz_recording_id,
			status, resolution_method, resolved_at, next_attempt_at, summary
		)
		VALUES ('manual', 'Portishead', 'Glass', $1, 'pending', 'manual', now(), now(), 'Wanted.')
	`, recordingID); err == nil {
		t.Fatal("a second live want was recorded for a recording that is already wanted")
	}
}

// A superseded row is exempt: it holds the same recording as the survivor it
// points at, which is what makes it the record of a merge rather than a want.
func TestASupersededWantMayHoldARecordingThatIsStillWanted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	recordingID := uuid.New()
	survivor, _, err := New(pool).CreateAcquisitionTarget(ctx, wantParams("Glass", recordingID))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets (
			origin, entry_artist, entry_title, musicbrainz_recording_id,
			status, superseded_by_id, resolution_method, resolved_at, summary
		)
		VALUES ('playlist', 'Portishead', 'Glass', $1, 'superseded', $2, 'manual', now(),
		        'Already wanted.')
	`, recordingID, survivor); err != nil {
		t.Fatalf("record the merge: %v", err)
	}
}

// One picture sweeper. The whole point of it is that the rate is deliberate, and
// a second copy would double it.
func TestOneCoverArtSweepIsQueuedHoweverOftenItIsAskedFor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	later := time.Now().Add(6 * time.Hour).UTC().Truncate(time.Second)
	sooner := time.Now().Add(time.Minute).UTC().Truncate(time.Second)
	if err := queries.QueueCoverArtSweep(ctx, later); err != nil {
		t.Fatalf("QueueCoverArtSweep() error = %v", err)
	}
	if err := queries.QueueCoverArtSweep(ctx, sooner); err != nil {
		t.Fatalf("QueueCoverArtSweep() error = %v", err)
	}

	sweeps, runAfter := queuedSweeps(ctx, t, pool, "sweep_cover_art")
	if sweeps != 1 {
		t.Fatalf("%d picture sweeps are queued, want exactly one", sweeps)
	}
	if !runAfter.Equal(sooner) {
		t.Fatalf("sweep runs after %v, want the earlier request %v", runAfter, sooner)
	}
}

// One follow-feed sweeper. This job schedules its own replacement, so a second
// copy would double the rate every pass rather than run twice.
func TestOneFollowFeedSweepIsQueuedHoweverOftenItIsAskedFor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	later := time.Now().Add(6 * time.Hour).UTC().Truncate(time.Second)
	sooner := time.Now().Add(time.Minute).UTC().Truncate(time.Second)
	if err := queries.QueueFollowFeedSweep(ctx, later); err != nil {
		t.Fatalf("QueueFollowFeedSweep() error = %v", err)
	}
	if err := queries.QueueFollowFeedSweep(ctx, sooner); err != nil {
		t.Fatalf("QueueFollowFeedSweep() error = %v", err)
	}

	sweeps, runAfter := queuedSweeps(ctx, t, pool, "sweep_follow_feed")
	if sweeps != 1 {
		t.Fatalf("%d follow sweeps are queued, want exactly one", sweeps)
	}
	if !runAfter.Equal(sooner) {
		t.Fatalf("sweep runs after %v, want the earlier request %v", runAfter, sooner)
	}
}

// Startup asks for a picture pass so that a fresh installation looks for covers
// without anybody pressing anything. With nothing queued, that ask writes the row.
func TestStartupQueuesACoverArtSweepWhenNoneIsWaiting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	startup := time.Now().UTC().Truncate(time.Second)
	if err := queries.EnsureCoverArtSweepQueued(ctx, startup); err != nil {
		t.Fatalf("EnsureCoverArtSweepQueued() error = %v", err)
	}

	sweeps, runAfter := queuedSweeps(ctx, t, pool, "sweep_cover_art")
	if sweeps != 1 || !runAfter.Equal(startup) {
		t.Fatalf("live sweeps = %d at %v, want one at %v", sweeps, runAfter, startup)
	}
}

// The picture sweep is the only thing that asks an archive on its own
// initiative, and it waits six hours between passes. Restarting the process must
// not spend that wait, or the rate becomes a question of how often Schall is
// deployed.
func TestStartupLeavesAWaitingCoverArtSweepAtItsOwnTime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	waited := time.Now().Add(6 * time.Hour).UTC().Truncate(time.Second)
	if err := queries.QueueCoverArtSweep(ctx, waited); err != nil {
		t.Fatalf("QueueCoverArtSweep() error = %v", err)
	}
	if err := queries.EnsureCoverArtSweepQueued(ctx, time.Now()); err != nil {
		t.Fatalf("EnsureCoverArtSweepQueued() error = %v", err)
	}

	sweeps, runAfter := queuedSweeps(ctx, t, pool, "sweep_cover_art")
	if sweeps != 1 || !runAfter.Equal(waited) {
		t.Fatalf("live sweeps = %d at %v, want one still at %v", sweeps, runAfter, waited)
	}
}

// A pass whose time went by while Schall was down is due already. Startup adds
// nothing to it, and the worker takes it as soon as it starts, so an
// installation that was switched off over a weekend still catches up.
func TestACoverArtSweepThatFellDueWhileSchallWasDownStaysDue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	missed := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
	if err := queries.QueueCoverArtSweep(ctx, missed); err != nil {
		t.Fatalf("QueueCoverArtSweep() error = %v", err)
	}
	if err := queries.EnsureCoverArtSweepQueued(ctx, time.Now()); err != nil {
		t.Fatalf("EnsureCoverArtSweepQueued() error = %v", err)
	}

	sweeps, runAfter := queuedSweeps(ctx, t, pool, "sweep_cover_art")
	if sweeps != 1 || !runAfter.Equal(missed) {
		t.Fatalf("live sweeps = %d at %v, want the overdue one at %v", sweeps, runAfter, missed)
	}
	if runAfter.After(time.Now()) {
		t.Fatalf("sweep runs after %v, which is still in the future", runAfter)
	}
}

// Ingesting a release wakes the picture sweep, because a record added a minute
// after a pass went by would otherwise be a grey rectangle all evening. That ask
// means "now" and still moves a waiting pass forward.
func TestIngestingAReleaseStillMovesAWaitingCoverArtSweepEarlier(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	later := time.Now().Add(6 * time.Hour).UTC().Truncate(time.Second)
	ingested := time.Now().UTC().Truncate(time.Second)
	if err := queries.EnsureCoverArtSweepQueued(ctx, later); err != nil {
		t.Fatalf("EnsureCoverArtSweepQueued() error = %v", err)
	}
	if err := queries.QueueCoverArtSweep(ctx, ingested); err != nil {
		t.Fatalf("QueueCoverArtSweep() error = %v", err)
	}

	sweeps, runAfter := queuedSweeps(ctx, t, pool, "sweep_cover_art")
	if sweeps != 1 || !runAfter.Equal(ingested) {
		t.Fatalf("live sweeps = %d at %v, want one brought forward to %v", sweeps, runAfter, ingested)
	}
}

// Startup asks for a follow-feed pass so that a followed artist keeps meaning
// something on a fresh installation. With nothing queued, that ask writes the row.
func TestStartupQueuesAFollowFeedSweepWhenNoneIsWaiting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	startup := time.Now().UTC().Truncate(time.Second)
	if err := queries.EnsureFollowFeedSweepQueued(ctx, startup); err != nil {
		t.Fatalf("EnsureFollowFeedSweepQueued() error = %v", err)
	}

	sweeps, runAfter := queuedSweeps(ctx, t, pool, "sweep_follow_feed")
	if sweeps != 1 || !runAfter.Equal(startup) {
		t.Fatalf("live sweeps = %d at %v, want one at %v", sweeps, runAfter, startup)
	}
}

// The follow feed asks MusicBrainz about every followed artist and waits six
// hours between passes. A restart must not spend that wait either.
func TestStartupLeavesAWaitingFollowFeedSweepAtItsOwnTime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	waited := time.Now().Add(6 * time.Hour).UTC().Truncate(time.Second)
	if err := queries.QueueFollowFeedSweep(ctx, waited); err != nil {
		t.Fatalf("QueueFollowFeedSweep() error = %v", err)
	}
	if err := queries.EnsureFollowFeedSweepQueued(ctx, time.Now()); err != nil {
		t.Fatalf("EnsureFollowFeedSweepQueued() error = %v", err)
	}

	sweeps, runAfter := queuedSweeps(ctx, t, pool, "sweep_follow_feed")
	if sweeps != 1 || !runAfter.Equal(waited) {
		t.Fatalf("live sweeps = %d at %v, want one still at %v", sweeps, runAfter, waited)
	}
}

// A follow-feed pass claimed by the worker is under way. Startup has nothing to
// add to it, and asking is not an error: the row stays as it is.
func TestStartupLeavesARunningFollowFeedSweepAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	claimed := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	if err := queries.QueueFollowFeedSweep(ctx, claimed); err != nil {
		t.Fatalf("QueueFollowFeedSweep() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE jobs SET status = 'running'
		WHERE kind = 'sweep_follow_feed' AND status = 'queued'
	`); err != nil {
		t.Fatalf("start follow feed sweep: %v", err)
	}

	if err := queries.EnsureFollowFeedSweepQueued(ctx, time.Now()); err != nil {
		t.Fatalf("EnsureFollowFeedSweepQueued() while a sweep runs: %v", err)
	}

	sweeps, runAfter := queuedSweeps(ctx, t, pool, "sweep_follow_feed")
	if sweeps != 1 || !runAfter.Equal(claimed) {
		t.Fatalf("live sweeps = %d at %v, want the running one at %v", sweeps, runAfter, claimed)
	}
}

// One recommendation sweeper is the external request budget in executable
// form. The queued row may move earlier, but even a request arriving while it
// is running cannot create a second live pass.
func TestOneRecommendationSweepIsQueuedOrRunningHoweverOftenItIsAskedFor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	later := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	sooner := time.Now().Add(time.Minute).UTC().Truncate(time.Second)
	if err := queries.QueueRecommendationSweep(ctx, later); err != nil {
		t.Fatalf("QueueRecommendationSweep() error = %v", err)
	}
	if err := queries.QueueRecommendationSweep(ctx, sooner); err != nil {
		t.Fatalf("queueing an earlier recommendation sweep: %v", err)
	}

	sweeps, runAfter := queuedSweeps(ctx, t, pool, "sweep_recommendations")
	if sweeps != 1 || !runAfter.Equal(sooner) {
		t.Fatalf("queued sweeps = %d at %v, want one at %v", sweeps, runAfter, sooner)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE jobs SET status = 'running'
		WHERE kind = 'sweep_recommendations' AND status = 'queued'
	`); err != nil {
		t.Fatalf("start recommendation sweep: %v", err)
	}
	if err := queries.QueueRecommendationSweep(ctx, time.Now()); err != nil {
		t.Fatalf("queueing while the sweep is running: %v", err)
	}

	var live int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'sweep_recommendations' AND status IN ('queued', 'running')
	`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Fatalf("live recommendation sweeps = %d, want exactly one", live)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE jobs SET status = 'completed'
		WHERE kind = 'sweep_recommendations' AND status = 'running'
	`); err != nil {
		t.Fatalf("complete recommendation sweep: %v", err)
	}
	payload := json.RawMessage(`{"offset":25,"fingerprint":"answer","hasCandidates":true}`)
	if err := queries.QueueRecommendationSweepContinuation(ctx, sooner, payload); err != nil {
		t.Fatalf("queueing a recommendation continuation: %v", err)
	}
	var storedPayload []byte
	if err := pool.QueryRow(ctx, `
		SELECT payload FROM jobs
		WHERE kind = 'sweep_recommendations' AND status = 'queued'
	`).Scan(&storedPayload); err != nil {
		t.Fatal(err)
	}
	var stored, wanted map[string]any
	if err := json.Unmarshal(storedPayload, &stored); err != nil {
		t.Fatalf("decode stored payload: %v", err)
	}
	if err := json.Unmarshal(payload, &wanted); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored, wanted) {
		t.Fatalf("stored payload = %s, want %s", storedPayload, payload)
	}
}

// Startup asks for a recommendation pass so that an enabled account always has
// one on the books. With nothing queued, that ask has to write the row.
func TestStartupQueuesARecommendationSweepWhenNoneIsWaiting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	startup := time.Now().UTC().Truncate(time.Second)
	if err := queries.EnsureRecommendationSweepQueued(ctx, startup); err != nil {
		t.Fatalf("EnsureRecommendationSweepQueued() error = %v", err)
	}

	sweeps, runAfter := queuedSweeps(ctx, t, pool, "sweep_recommendations")
	if sweeps != 1 || !runAfter.Equal(startup) {
		t.Fatalf("live sweeps = %d at %v, want one at %v", sweeps, runAfter, startup)
	}
}

// A pass that met a service in difficulty queues its successor half an hour out
// so that Schall does not press it again at once. Restarting the process must
// not spend that wait: startup means "there must be a pass", not "sweep now".
func TestStartupLeavesAWaitingRecommendationSweepAtItsOwnTime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	waited := time.Now().Add(30 * time.Minute).UTC().Truncate(time.Second)
	if err := queries.QueueRecommendationSweep(ctx, waited); err != nil {
		t.Fatalf("QueueRecommendationSweep() error = %v", err)
	}
	if err := queries.EnsureRecommendationSweepQueued(ctx, time.Now()); err != nil {
		t.Fatalf("EnsureRecommendationSweepQueued() error = %v", err)
	}

	sweeps, runAfter := queuedSweeps(ctx, t, pool, "sweep_recommendations")
	if sweeps != 1 || !runAfter.Equal(waited) {
		t.Fatalf("live sweeps = %d at %v, want one still at %v", sweeps, runAfter, waited)
	}
}

// A pass claimed by the worker is under way. Startup has nothing to add to it,
// and asking is not an error: the row stays as it is and no second pass appears.
func TestStartupLeavesARunningRecommendationSweepAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	claimed := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	if err := queries.QueueRecommendationSweep(ctx, claimed); err != nil {
		t.Fatalf("QueueRecommendationSweep() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE jobs SET status = 'running'
		WHERE kind = 'sweep_recommendations' AND status = 'queued'
	`); err != nil {
		t.Fatalf("start recommendation sweep: %v", err)
	}

	if err := queries.EnsureRecommendationSweepQueued(ctx, time.Now()); err != nil {
		t.Fatalf("EnsureRecommendationSweepQueued() while a sweep runs: %v", err)
	}

	sweeps, runAfter := queuedSweeps(ctx, t, pool, "sweep_recommendations")
	if sweeps != 1 || !runAfter.Equal(claimed) {
		t.Fatalf("live sweeps = %d at %v, want the running one at %v", sweeps, runAfter, claimed)
	}
	var status string
	if err := pool.QueryRow(ctx, `
		SELECT status FROM jobs WHERE kind = 'sweep_recommendations'
	`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "running" {
		t.Fatalf("sweep status = %q, want it left running", status)
	}
}

// Saving a ListenBrainz account is the moment to read it. The pass already on
// the books may be a day away, so this ask, unlike startup's, moves it forward.
func TestConnectingAnAccountMovesAWaitingRecommendationSweepEarlier(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	tomorrow := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	saved := time.Now().UTC().Truncate(time.Second)
	if err := queries.EnsureRecommendationSweepQueued(ctx, tomorrow); err != nil {
		t.Fatalf("EnsureRecommendationSweepQueued() error = %v", err)
	}
	if err := queries.QueueRecommendationSweep(ctx, saved); err != nil {
		t.Fatalf("QueueRecommendationSweep() error = %v", err)
	}

	sweeps, runAfter := queuedSweeps(ctx, t, pool, "sweep_recommendations")
	if sweeps != 1 || !runAfter.Equal(saved) {
		t.Fatalf("live sweeps = %d at %v, want one brought forward to %v", sweeps, runAfter, saved)
	}
}

func queuedSweeps(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, kind string,
) (int, time.Time) {
	t.Helper()

	var sweeps int
	var runAfter time.Time
	if err := pool.QueryRow(ctx, `
		SELECT count(*), coalesce(min(run_after), '-infinity'::timestamptz)
		FROM jobs
		WHERE kind = $1 AND status IN ('queued', 'running')
	`, kind).Scan(&sweeps, &runAfter); err != nil {
		t.Fatal(err)
	}
	return sweeps, runAfter
}

// wantOnRelease records one pending want for a named track of a shared release.
func wantOnRelease(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, title string, releaseGroupID uuid.UUID,
) uuid.UUID {
	t.Helper()

	id, _, err := New(pool).CreateAcquisitionTarget(ctx, CreateAcquisitionTargetParams{
		Origin:                    "manual",
		EntryArtist:               "Portishead",
		EntryTitle:                title,
		MusicBrainzRecordingID:    uuid.NullUUID{UUID: uuid.New(), Valid: true},
		MusicBrainzReleaseGroupID: uuid.NullUUID{UUID: releaseGroupID, Valid: true},
		Summary:                   "Wanted.",
	})
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}
	return id
}

// A peer shares a folder rather than a track, so the search that found one want's
// copy found its siblings' too. Asking each of them to find it again on its own
// turn asks a network that answers the same question differently every time.
func TestTheWantsSharingAReleaseAreReadBackTogether(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	releaseGroupID := uuid.New()
	glory := wantOnRelease(ctx, t, pool, "Glory Box", releaseGroupID)
	mysterons := wantOnRelease(ctx, t, pool, "Mysterons", releaseGroupID)
	wantOnRelease(ctx, t, pool, "Teardrop", uuid.New())

	siblings, err := New(pool).WantsSharingAReleaseWith(ctx, glory)
	if err != nil {
		t.Fatalf("WantsSharingAReleaseWith() error = %v", err)
	}
	if len(siblings) != 1 || siblings[0].ID != mysterons {
		t.Fatalf("siblings = %v, want only the want on the same release (%s)", siblings, mysterons)
	}
}

// A want with a copy already on its way is a want already answered, and handing
// it a second one out of a folder is the duplicate import the one-open-request
// rule exists to prevent.
func TestAWantWithACopyInFlightIsNotOfferedASiblingsFolder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	releaseGroupID := uuid.New()
	glory := wantOnRelease(ctx, t, pool, "Glory Box", releaseGroupID)
	mysterons := wantOnRelease(ctx, t, pool, "Mysterons", releaseGroupID)
	if _, err := pool.Exec(ctx, `
		INSERT INTO download_requests (
			acquisition_target_id, provider, source_username, source_directory,
			file_count, expected_track_count, total_size_bytes, format, score, files, status
		)
		VALUES ($1, 'slskd', 'peer', 'Music/Portishead', 1, 1, 25000000, 'flac', 0.8,
		        '[]'::jsonb, 'started')
	`, mysterons); err != nil {
		t.Fatal(err)
	}

	siblings, err := New(pool).WantsSharingAReleaseWith(ctx, glory)
	if err != nil {
		t.Fatalf("WantsSharingAReleaseWith() error = %v", err)
	}
	if len(siblings) != 0 {
		t.Fatalf("siblings = %v, want a want already following a copy left alone", siblings)
	}
}

// A failed import is not an answer, so the want it was for is eligible for the
// next folder that turns up rather than being buried behind a copy that came to
// nothing.
func TestAWantWhoseImportFailedIsOfferedASiblingsFolderAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	releaseGroupID := uuid.New()
	glory := wantOnRelease(ctx, t, pool, "Glory Box", releaseGroupID)
	mysterons := wantOnRelease(ctx, t, pool, "Mysterons", releaseGroupID)
	if _, err := pool.Exec(ctx, `
		INSERT INTO download_requests (
			acquisition_target_id, provider, source_username, source_directory,
			file_count, expected_track_count, total_size_bytes, format, score, files,
			status, import_status, import_error
		)
		VALUES ($1, 'slskd', 'peer', 'Music/Portishead', 1, 1, 25000000, 'flac', 0.8,
		        '[]'::jsonb, 'completed', 'pending', 'the copy could not be read')
	`, mysterons); err != nil {
		t.Fatal(err)
	}

	siblings, err := New(pool).WantsSharingAReleaseWith(ctx, glory)
	if err != nil {
		t.Fatalf("WantsSharingAReleaseWith() error = %v", err)
	}
	if len(siblings) != 1 || siblings[0].ID != mysterons {
		t.Fatalf("siblings = %v, want the want whose import failed offered again", siblings)
	}
}

// The answers offered for an entry are read back in the order the attempt ranked
// them, with the evidence that stood for each: a question stored without its
// evidence is a list of titles and a request to guess.
func TestTheAnswersOfferedForAnEntryAreReadBackInRankOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, wanted := asking(ctx, t, pool)

	candidates, err := queries.AcquisitionTargetCandidates(ctx, targetID)
	if err != nil {
		t.Fatalf("AcquisitionTargetCandidates() error = %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates = %d, want both answers the entry could name", len(candidates))
	}
	if candidates[0].MusicBrainzRecordingID != wanted || candidates[0].Rank != 1 {
		t.Fatalf("first candidate = %+v, want the one ranked first", candidates[0])
	}
	if len(candidates[0].Agrees) != 2 {
		t.Fatalf("agrees = %v, want the evidence for it stored beside it", candidates[0].Agrees)
	}
}

// A want that has been settled is not settled again, whatever a sweep that was
// already running thinks. The decision that finished it is the later fact.
func TestSettlingAWantThatIsAlreadyFinishedIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	recordingID := uuid.New()
	fileID := seedLibraryFileWithIdentity(ctx, t, pool, "/music/glass.flac", recordingID)
	id, _, err := queries.CreateAcquisitionTarget(ctx, wantParams("Glass", recordingID))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() error = %v", err)
	}
	if err := queries.SettleAcquiredTarget(ctx, id, fileID, "in your library", "already held"); err != nil {
		t.Fatalf("SettleAcquiredTarget() error = %v", err)
	}

	err = queries.SettleAcquiredTarget(ctx, id, fileID, "in your library", "already held")
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("SettleAcquiredTarget() error = %v, want %v", err, pgx.ErrNoRows)
	}
}

// A file mapped to a catalogue track carrying the recording is owned music even
// though nothing wrote an identity for it: the mapping is the decision.
func TestAMappingOntoACarryingTrackSatisfiesAWant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	recordingID, fileID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, '/music/mapped.flac', 1024, now())
	`, fileID); err != nil {
		t.Fatal(err)
	}
	artistID, albumID, trackID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, name, sort_name, catalogue_summary)
		VALUES ($1, 'Portishead', 'Portishead', 'held because the library has their music')
	`, artistID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, title) VALUES ($1, $2, 'Dummy')
	`, albumID, artistID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO tracks (id, album_id, title, track_number, musicbrainz_recording_id)
		VALUES ($1, $2, 'Glory Box', 5, $3)
	`, trackID, albumID, recordingID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO track_mappings (library_file_id, track_id, method, confidence)
		VALUES ($1, $2, 'musicbrainz_recording_id', 1)
	`, fileID, trackID); err != nil {
		t.Fatal(err)
	}

	owned, err := New(pool).OwnedFileForRecording(ctx, recordingID)
	if err != nil {
		t.Fatalf("OwnedFileForRecording() error = %v", err)
	}
	if !owned.Valid || owned.UUID != fileID {
		t.Fatalf("owned file = %v, want the mapped file %s", owned, fileID)
	}
}
