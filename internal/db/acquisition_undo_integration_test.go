package db

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
)

// The sentences the undo paths write. internal/acquisition owns the real ones
// and this package cannot import it, so the tests spell out their own: what is
// under test here is the state each statement leaves behind, never the wording.
const (
	takenBackCopy = "The decision about this copy was taken back."
	takenBackWant = "A copy is waiting for your decision again."
	restoredWant  = "You took back saying this entry names the wrong recording."
)

// acceptedCopy holds one copy for a want and accepts it by hand, which is the
// state an acceptance leaves behind: the decision written, and the import that
// will act on it queued but not run.
func acceptedCopy(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool,
) (*Queries, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	holdOneCopy(ctx, t, queries, targetID, requestID)
	copies, err := queries.AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.AcceptHeldCopy(ctx, copies[0].ID, "you said it is the one"); err != nil {
		t.Fatalf("accept the copy: %v", err)
	}
	return queries, targetID, requestID, copies[0].ID
}

// bringTheCopyIntoTheLibrary does what the import and the scan after it do: a
// library row exists for the copy, and the offer names it. That is the fact the
// refusal below rests on.
func bringTheCopyIntoTheLibrary(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, copyID uuid.UUID,
) uuid.UUID {
	t.Helper()
	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, '/music/Anetha/03.flac', 9000000, now())
	`, fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_target_files SET library_file_id = $2 WHERE id = $1
	`, copyID, fileID); err != nil {
		t.Fatal(err)
	}
	return fileID
}

// 1. Once the file is in the library the acceptance stands. Taking it back then
// would leave a file on disk that no decision claims, and free the want to fetch
// and import the same recording a second time. The way back at that point is to
// remove the file from the library, which is a different action with a different
// name.
func TestTakingBackAnAcceptWhoseCopyIsInTheLibraryIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, _, _, copyID := acceptedCopy(ctx, t, pool)
	bringTheCopyIntoTheLibrary(ctx, t, pool, copyID)

	err := queries.TakeBackAcceptedCopy(ctx, copyID, takenBackCopy, takenBackWant)

	if !errors.Is(err, ErrCopyIsInTheLibrary) {
		t.Fatalf("TakeBackAcceptedCopy() error = %v, want %v", err, ErrCopyIsInTheLibrary)
	}
	after, err := queries.AcquiredCopy(ctx, copyID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Verdict != AcquiredFileAccepted || after.DecidedBy != "user" {
		t.Fatalf("copy = %s by %s, want the acceptance untouched", after.Verdict, after.DecidedBy)
	}
	if after.Summary == "" || !after.LibraryFileID.Valid {
		t.Fatalf("copy = %+v, want it still naming its library file and saying why", after)
	}
}

// 2. Until then it comes back. The copy returns to the state it was in before
// anybody answered: waiting for a decision, decided by nobody, with no sentence
// and its clock restarted — and the want is in the queue again with it.
func TestTakingBackAnAcceptThatHasNotLandedReturnsTheCopyToHeld(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID, copyID := acceptedCopy(ctx, t, pool)
	accepted, err := queries.AcquiredCopy(ctx, copyID)
	if err != nil {
		t.Fatal(err)
	}

	if err := queries.TakeBackAcceptedCopy(
		ctx, copyID, takenBackCopy, takenBackWant,
	); err != nil {
		t.Fatalf("TakeBackAcceptedCopy() error = %v", err)
	}

	after, err := queries.AcquiredCopy(ctx, copyID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Verdict != AcquiredFileHeld {
		t.Fatalf("copy = %s, want it waiting for a decision again", after.Verdict)
	}
	if after.DecidedBy != "schall" || after.Summary != "" {
		t.Fatalf("copy = decided by %q saying %q, want no decision and no sentence",
			after.DecidedBy, after.Summary)
	}
	// decided_at cannot be emptied — every row has one — so clearing it is the
	// clock starting again. It is what orders the queue, and a copy nobody has
	// answered has been waiting since it was returned rather than since it was
	// answered.
	if !after.DecidedAt.After(accepted.DecidedAt) {
		t.Errorf("decided at %s, want it moved on from the accepted %s",
			after.DecidedAt, accepted.DecidedAt)
	}
	if after.Evidence == nil {
		t.Error("the evidence the copy is judged on was thrown away with the decision")
	}

	// The import the acceptance queued goes with the decision. Left behind it
	// would bring the file in anyway, and the decision would be gone.
	stored, err := queries.DownloadRequest(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ImportStatus != "needs_review" {
		t.Errorf("import status = %q, want the request waiting for a decision again",
			stored.ImportStatus)
	}
	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'import_download' AND status = 'queued'
	`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 0 {
		t.Errorf("queued imports = %d, want the one the acceptance queued taken off", queued)
	}

	page, err := queries.AcquisitionReviewQueue(ctx, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Target.ID != targetID {
		t.Fatalf("queue = %+v, want the want asking its question again", page.Items)
	}
}

// 3. A want that has taken another answer since keeps it. Returning the refused
// copies beside an accepted one would put two answered copies on one want, which
// is the state the one-answer-per-want guard exists to prevent.
func TestTakingBackARefusalOnAWantThatHasAcceptedAnotherCopyIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	holdOneCopy(ctx, t, queries, targetID, requestID)
	if err := queries.RefuseHeldCopies(
		ctx, targetID, "none of these are it", "looking for another", time.Now(),
	); err != nil {
		t.Fatal(err)
	}
	// The sweeper found another copy afterwards and the loop proved it on the
	// audio, which is the want moving on.
	if err := queries.RecordAcquiredFile(ctx, RecordAcquiredFileParams{
		AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "another peer",
		RemotePath: `Music\Anetha\05.flac`, FileName: "05.flac", SizeBytes: 9200000,
		Verdict: AcquiredFileAccepted, Summary: "Proven by its audio.",
	}); err != nil {
		t.Fatal(err)
	}

	err := queries.TakeBackRefusedCopies(ctx, targetID, takenBackCopy, takenBackWant)

	if !errors.Is(err, ErrWantAnsweredAnotherWay) {
		t.Fatalf("TakeBackRefusedCopies() error = %v, want %v", err, ErrWantAnsweredAnotherWay)
	}
	copies, err := queries.AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	for _, copied := range copies {
		switch copied.FileName {
		case "03.flac":
			if copied.Verdict != AcquiredFileDiscardedAudio || copied.DecidedBy != "user" {
				t.Errorf("the refused copy = %s by %s, want the refusal untouched",
					copied.Verdict, copied.DecidedBy)
			}
		case "05.flac":
			if copied.Verdict != AcquiredFileAccepted {
				t.Errorf("the answer since = %s, want it untouched", copied.Verdict)
			}
		}
	}
}

// stoppedWant records a want somebody stopped pursuing.
func stoppedWant(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool,
) (*Queries, uuid.UUID) {
	t.Helper()
	queries, targetID, _ := fetchedForAWant(ctx, t, pool)
	if _, err := queries.StopPursuingAcquisitionTarget(
		ctx, targetID, "No longer wanted.",
	); err != nil {
		t.Fatalf("stop pursuing the want: %v", err)
	}
	return queries, targetID
}

// 4. A want whose recording another entry has since asked for keeps the
// decision. That other entry was merged onto this one and explains itself with
// the decision to stop, so restoring the want underneath it would leave a target
// citing a decision that no longer exists.
func TestTakingBackNotWantedOnARecordingAnotherTargetHasSinceWantedIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID := stoppedWant(ctx, t, pool)

	// A second entry resolved to the same recording while this want was stopped.
	// One recording is one want, so it was merged onto this one and superseded.
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets (
			origin, entry_artist, entry_title, musicbrainz_recording_id,
			resolution_method, resolved_at, status, superseded_by_id, summary
		)
		SELECT 'playlist', 'Anetha', 'Candy From Strangers', musicbrainz_recording_id,
		       'isrc', now(), 'superseded', id,
		       'This entry resolved to a recording you said you did not want.'
		FROM acquisition_targets WHERE id = $1
	`, targetID); err != nil {
		t.Fatal(err)
	}

	_, err := queries.TakeBackNotWanted(
		ctx, targetID, "Waiting to be resolved.", "Wanted.", time.Now())

	if !errors.Is(err, ErrRecordingWantedElsewhere) {
		t.Fatalf("TakeBackNotWanted() error = %v, want %v", err, ErrRecordingWantedElsewhere)
	}
	target, err := queries.AcquisitionTarget(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if target.Status != "not_wanted" || !target.NotWantedAt.Valid {
		t.Fatalf("want = %s, want the decision to stop untouched", target.Status)
	}
}

// rejectedResolution records the wrong-target outcome: the entry was resolved to
// a recording it does not name, so that recording is remembered and the entry
// goes back to be looked up again.
//
// The want is given a release group before the rejection, because a resolution
// carries three facts and not one: which recording, which release the answer ran
// through, and how the answer was reached. The row returned is the want as it
// stood with all three, which is what taking the rejection back has to restore.
func rejectedResolution(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool,
) (*Queries, uuid.UUID, AcquisitionTargetRow) {
	t.Helper()
	queries, targetID, _ := fetchedForAWant(ctx, t, pool)
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET musicbrainz_release_group_id = $2 WHERE id = $1
	`, targetID, uuid.New()); err != nil {
		t.Fatalf("give the want a release group: %v", err)
	}
	before, err := queries.AcquisitionTarget(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.RejectAcquisitionTargetResolution(
		ctx, targetID, "Being looked up again, without it.",
		"A person said this entry does not name that recording.", time.Now(),
	); err != nil {
		t.Fatalf("reject the resolution: %v", err)
	}
	return queries, targetID, before
}

// forgetWhatTheRejectionRemembered makes a rejection row look like one written
// before the release group and the method were remembered on it: both columns
// empty, because no such column existed when the row was written.
func forgetWhatTheRejectionRemembered(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID uuid.UUID,
) {
	t.Helper()
	tag, err := pool.Exec(ctx, `
		UPDATE acquisition_target_rejected_recordings
		SET musicbrainz_release_group_id = NULL, resolution_method = NULL
		WHERE acquisition_target_id = $1
	`, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("emptied %d rejections, want the one this want has", tag.RowsAffected())
	}
}

// 5. Once the entry has been resolved again the rejection stands. A new
// recording is being pursued by then, possibly with copies already fetched
// against it, and removing the old exclusion would not remove those.
func TestTakingBackWrongTargetAfterTheEntryWasResolvedAgainIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, before := rejectedResolution(ctx, t, pool)

	instead := uuid.New()
	if _, err := queries.ResolveAcquisitionTarget(ctx, ResolveAcquisitionTargetParams{
		ID:                     targetID,
		MusicBrainzRecordingID: instead,
		ResolutionMethod:       "isrc",
		Summary:                "Wanted. Waiting for a copy to be found.",
		SupersededSummary:      "Already wanted.",
		SupersededNotWanted:    "You said you did not want this recording.",
		Detail:                 "Resolved on its ISRC.",
		NextAttemptAt:          time.Now(),
	}); err != nil {
		t.Fatalf("resolve the entry again: %v", err)
	}

	_, err := queries.TakeBackWrongRecording(ctx, targetID, restoredWant, time.Now())

	if !errors.Is(err, ErrEntryResolvedAgain) {
		t.Fatalf("TakeBackWrongRecording() error = %v, want %v", err, ErrEntryResolvedAgain)
	}
	target, err := queries.AcquisitionTarget(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if target.MusicBrainzRecordingID.UUID != instead {
		t.Fatalf("want names %s, want the recording it was resolved to since",
			target.MusicBrainzRecordingID.UUID)
	}
	excluded, err := queries.RejectedTargetResolutions(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if len(excluded) != 1 || excluded[0] != before.MusicBrainzRecordingID.UUID {
		t.Fatalf("excluded = %v, want the rejected recording still excluded", excluded)
	}
	// The rejection still remembers what the resolution was, untouched by the
	// refusal. Nothing was restored, so nothing it kept for a restoration moved.
	var (
		releaseGroup uuid.NullUUID
		method       pgtype.Text
	)
	if err := pool.QueryRow(ctx, `
		SELECT musicbrainz_release_group_id, resolution_method
		FROM acquisition_target_rejected_recordings
		WHERE acquisition_target_id = $1
	`, targetID).Scan(&releaseGroup, &method); err != nil {
		t.Fatal(err)
	}
	if releaseGroup != before.MusicBrainzReleaseGroupID || method.String != before.ResolutionMethod.String {
		t.Fatalf("the rejection remembers %v by %q, want %v by %q",
			releaseGroup, method.String,
			before.MusicBrainzReleaseGroupID, before.ResolutionMethod.String)
	}
}

// 6. Nothing Schall decided for itself is anybody's to take back, whatever the
// verdict. The loop's refusals are its reading of the audio rather than a
// person's answer, and a person who disagrees answers the question it raised.
func TestNoAnswerSchallGaveItselfCanBeTakenBack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)

	// The loop's own accept: proven by the audio, and never a person's decision.
	if err := queries.RecordAcquiredFile(ctx, RecordAcquiredFileParams{
		AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
		RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac", SizeBytes: 9000000,
		Verdict: AcquiredFileAccepted, Summary: "Proven by its audio.",
		DownloadRequestID: uuid.NullUUID{UUID: requestID, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	// And the loop's own refusal, on another want, so the two paths that read
	// copies are both asked.
	otherQueries, otherTarget, otherRequest := fetchedForAWant(ctx, t, pool)
	if err := otherQueries.RecordAcquiredFile(ctx, RecordAcquiredFileParams{
		AcquisitionTargetID: otherTarget, Provider: "slskd", SourceUsername: "peer",
		RemotePath: `Music\Anetha\04.flac`, FileName: "04.flac", SizeBytes: 9100000,
		Verdict: AcquiredFileDiscardedAudio, Summary: "The audio is a different recording.",
		DownloadRequestID: uuid.NullUUID{UUID: otherRequest, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	copies, err := queries.AcquisitionTargetFiles(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}

	if err := queries.TakeBackAcceptedCopy(
		ctx, copies[0].ID, takenBackCopy, takenBackWant,
	); !errors.Is(err, ErrSchallDecidedThis) {
		t.Errorf("taking back the loop's accept = %v, want %v", err, ErrSchallDecidedThis)
	}
	if err := queries.TakeBackRefusedCopies(
		ctx, otherTarget, takenBackCopy, takenBackWant,
	); !errors.Is(err, ErrSchallDecidedThis) {
		t.Errorf("taking back the loop's refusal = %v, want %v", err, ErrSchallDecidedThis)
	}
	// The other two answers are about the want rather than a copy, and only a
	// person ever records either. A want the loop is still working on carries no
	// answer of anybody's to take back, and says so rather than moving.
	if _, err := queries.TakeBackNotWanted(
		ctx, targetID, "Waiting to be resolved.", "Wanted.", time.Now(),
	); !errors.Is(err, ErrWantAnsweredAnotherWay) {
		t.Errorf("taking back a stop nobody made = %v, want %v", err, ErrWantAnsweredAnotherWay)
	}
	if _, err := queries.TakeBackWrongRecording(
		ctx, targetID, restoredWant, time.Now(),
	); !errors.Is(err, ErrEntryResolvedAgain) {
		t.Errorf("taking back a rejection nobody made = %v, want %v", err, ErrEntryResolvedAgain)
	}

	after, err := queries.AcquiredCopy(ctx, copies[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Verdict != AcquiredFileAccepted || after.DecidedBy != "schall" {
		t.Fatalf("copy = %s by %s, want the loop's verdict untouched",
			after.Verdict, after.DecidedBy)
	}
}

// 7. Two undos of one decision settle as one reversal and one refusal. Every
// condition is in the WHERE clause of the statement that performs the reversal,
// so the second one finds a copy that is no longer accepted and says so — a read
// followed by a write would have both of them see an acceptance and both act.
func TestTwoUndosOfOneDecisionSettleAsOneReversalAndOneRefusal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, _, _, copyID := acceptedCopy(ctx, t, pool)

	var (
		wait    sync.WaitGroup
		results [2]error
	)
	for index := range results {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results[index] = queries.TakeBackAcceptedCopy(
				ctx, copyID, takenBackCopy, takenBackWant)
		}()
	}
	wait.Wait()

	reversed, refused := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			reversed++
		case errors.Is(err, ErrWantAnsweredAnotherWay):
			refused++
		default:
			t.Fatalf("taking the answer back = %v, want it taken or refused", err)
		}
	}
	if reversed != 1 || refused != 1 {
		t.Fatalf("%d reversals and %d refusals, want one of each", reversed, refused)
	}
	after, err := queries.AcquiredCopy(ctx, copyID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Verdict != AcquiredFileHeld {
		t.Fatalf("copy = %s, want it waiting for a decision again", after.Verdict)
	}
}

// 8. A copy that was answered and then unanswered is graded again from scratch,
// and the grader's verdict lands on it exactly as it would have had nobody ever
// answered. Taking an answer back does not launder a copy past a check: what it
// removes is the decision, never the checking.
func TestACopyReturnedToHeldIsGradedAgainFromScratch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, _, copyID := acceptedCopy(ctx, t, pool)
	if err := queries.TakeBackAcceptedCopy(
		ctx, copyID, takenBackCopy, takenBackWant,
	); err != nil {
		t.Fatal(err)
	}

	// What the loop concludes the next time it looks at the file. While the
	// acceptance stood this write was refused, because a person's verdict is
	// never overwritten by a machine one.
	if err := queries.RecordAcquiredFile(ctx, RecordAcquiredFileParams{
		AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
		RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac", SizeBytes: 9000000,
		Verdict: AcquiredFileDiscardedAudio,
		Summary: "The audio is a different recording.",
	}); err != nil {
		t.Fatalf("RecordAcquiredFile() error = %v", err)
	}

	after, err := queries.AcquiredCopy(ctx, copyID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Verdict != AcquiredFileDiscardedAudio || after.DecidedBy != "schall" {
		t.Fatalf("copy = %s by %s, want the grader's own verdict on it",
			after.Verdict, after.DecidedBy)
	}
}

// The other half of outcome 3: a want somebody stopped is pursued again, and the
// sweeper may look for it. Nothing automatic overturns the decision; the person
// who made it takes it back.
func TestTakingBackNotWantedPutsTheWantBackInThePursuit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID := stoppedWant(ctx, t, pool)

	due := time.Now().UTC().Truncate(time.Millisecond)
	target, err := queries.TakeBackNotWanted(
		ctx, targetID, "Waiting to be resolved.", "Wanted.", due)

	if err != nil {
		t.Fatalf("TakeBackNotWanted() error = %v", err)
	}
	if target.Status != "pending" || target.NotWantedAt.Valid {
		t.Fatalf("want = %s stopped at %v, want it pursued again",
			target.Status, target.NotWantedAt.Time)
	}
	if !target.NextAttemptAt.Valid ||
		!target.NextAttemptAt.Time.UTC().Truncate(time.Millisecond).Equal(due) {
		t.Errorf("next attempt = %v, want %s", target.NextAttemptAt, due)
	}
}

// The other half of outcome 4: the recording the entry was said not to name is
// put back, and the exclusion that would have kept the lookup off it is gone.
func TestTakingBackWrongTargetRestoresTheRecordingAndForgetsTheExclusion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, before := rejectedResolution(ctx, t, pool)

	target, err := queries.TakeBackWrongRecording(ctx, targetID, restoredWant, time.Now())

	if err != nil {
		t.Fatalf("TakeBackWrongRecording() error = %v", err)
	}
	if target.Status != "pending" ||
		target.MusicBrainzRecordingID != before.MusicBrainzRecordingID {
		t.Fatalf("want = %s naming %v, want the rejected recording back",
			target.Status, target.MusicBrainzRecordingID)
	}
	excluded, err := queries.RejectedTargetResolutions(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if len(excluded) != 0 {
		t.Fatalf("excluded = %v, want the exclusion forgotten with the answer", excluded)
	}
}

// A resolution is three facts, and taking back the rejection of it restores all
// three. The release group is the release the answer ran through, which decides
// which copy of the recording an import prefers; the method says how the answer
// was reached. Both were erased from the want by the rejection and kept on the
// rejection row, and both come back with the recording.
func TestTakingBackWrongTargetRestoresTheReleaseGroupAndTheMethod(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, before := rejectedResolution(ctx, t, pool)
	if !before.MusicBrainzReleaseGroupID.Valid || before.ResolutionMethod.String != "isrc" {
		t.Fatalf("the want stood as %+v, want it resolved through a release group on its ISRC",
			before)
	}

	target, err := queries.TakeBackWrongRecording(ctx, targetID, restoredWant, time.Now())

	if err != nil {
		t.Fatalf("TakeBackWrongRecording() error = %v", err)
	}
	if target.MusicBrainzReleaseGroupID != before.MusicBrainzReleaseGroupID {
		t.Errorf("release group = %v, want the one the resolution ran through, %v",
			target.MusicBrainzReleaseGroupID, before.MusicBrainzReleaseGroupID)
	}
	if target.ResolutionMethod.String != before.ResolutionMethod.String {
		t.Errorf("method = %q, want the one the answer was reached by, %q",
			target.ResolutionMethod.String, before.ResolutionMethod.String)
	}
}

// A rejection written before the release group and the method were remembered
// remembers neither, and taking it back is what it always was rather than an
// error. The recording comes back, there is no release group, and the method
// reads 'manual' — the recording stands on the say-so of the person who put it
// back, which is the only thing still known about how it got there.
func TestTakingBackAWrongTargetRejectionThatRemembersNothingStillRestoresTheRecording(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, before := rejectedResolution(ctx, t, pool)
	forgetWhatTheRejectionRemembered(ctx, t, pool, targetID)

	target, err := queries.TakeBackWrongRecording(ctx, targetID, restoredWant, time.Now())

	if err != nil {
		t.Fatalf("TakeBackWrongRecording() error = %v", err)
	}
	if target.Status != "pending" ||
		target.MusicBrainzRecordingID != before.MusicBrainzRecordingID {
		t.Fatalf("want = %s naming %v, want the rejected recording back",
			target.Status, target.MusicBrainzRecordingID)
	}
	if target.MusicBrainzReleaseGroupID.Valid {
		t.Errorf("release group = %v, want none: the rejection never recorded one",
			target.MusicBrainzReleaseGroupID)
	}
	if target.ResolutionMethod.String != "manual" {
		t.Errorf("method = %q, want %q", target.ResolutionMethod.String, "manual")
	}
}

// Two undos of one rejection settle as one reversal and one refusal, exactly as
// two undos of one acceptance do. The condition is in the WHERE clause of the
// statement that writes the recording back, and it is repeated there rather than
// only in the rows that statement selected: the second press waits on the want's
// lock, is re-checked against what the first press wrote, and finds an entry that
// has an answer again.
func TestTwoUndosOfOneRejectionSettleAsOneReversalAndOneRefusal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, before := rejectedResolution(ctx, t, pool)

	var (
		wait    sync.WaitGroup
		results [2]error
	)
	for index := range results {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, results[index] = queries.TakeBackWrongRecording(
				ctx, targetID, restoredWant, time.Now())
		}()
	}
	wait.Wait()

	reversed, refused := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			reversed++
		case errors.Is(err, ErrEntryResolvedAgain):
			refused++
		default:
			t.Fatalf("taking the answer back = %v, want it taken or refused", err)
		}
	}
	if reversed != 1 || refused != 1 {
		t.Fatalf("%d reversals and %d refusals, want one of each", reversed, refused)
	}
	target, err := queries.AcquisitionTarget(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if target.MusicBrainzRecordingID != before.MusicBrainzRecordingID ||
		target.MusicBrainzReleaseGroupID != before.MusicBrainzReleaseGroupID {
		t.Fatalf("want names %v through %v, want the resolution restored once, whole",
			target.MusicBrainzRecordingID, target.MusicBrainzReleaseGroupID)
	}
}

// A refused reversal changes nothing at all, including the exclusion it would
// have forgotten.
//
// Taking back "wrong target" is two writes: the recording goes back onto the
// want, and the exclusion that kept the lookup off that recording is deleted.
// One statement does both, and every part of a statement reads the database as
// it was when the statement began. The update is the exception, because it waits
// for the want's row when somebody else is holding it and reads that row again
// afterwards. So there is one interleaving where the two halves disagree:
// resolution runs again after the statement starts and commits before the update
// reaches the row. Everything read says the reversal applies; the update says it
// does not.
//
// The refusal is right — the entry is being pursued as something else by then.
// Losing the exclusion with it is not: nobody took that decision back, and a
// decision nobody took back is permanent. Worse, the person is told nothing was
// changed while the record of them saying the recording was wrong is gone, and
// the next lookup is free to answer with the recording they rejected.
func TestARefusedTakeBackOfWrongTargetLeavesTheExclusionInPlace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, before := rejectedResolution(ctx, t, pool)

	// Resolution runs again and holds the want's row without committing. Nothing
	// reading the database can see it yet; only a write that waits for that row
	// will.
	resolving, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resolving.Rollback(ctx) }()
	instead := uuid.New()
	if _, err := New(resolving).ResolveAcquisitionTarget(ctx, ResolveAcquisitionTargetParams{
		ID:                     targetID,
		MusicBrainzRecordingID: instead,
		ResolutionMethod:       "isrc",
		Summary:                "Wanted. Waiting for a copy to be found.",
		SupersededSummary:      "Already wanted.",
		SupersededNotWanted:    "You said you did not want this recording.",
		Detail:                 "Resolved on its ISRC.",
		NextAttemptAt:          time.Now(),
	}); err != nil {
		t.Fatalf("resolve the entry again: %v", err)
	}

	taken := make(chan error, 1)
	go func() {
		_, err := queries.TakeBackWrongRecording(ctx, targetID, restoredWant, time.Now())
		taken <- err
	}()
	if err := waitForTheUndoToReachTheWant(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := resolving.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	if err := <-taken; !errors.Is(err, ErrEntryResolvedAgain) {
		t.Fatalf("TakeBackWrongRecording() error = %v, want %v", err, ErrEntryResolvedAgain)
	}
	excluded, err := queries.RejectedTargetResolutions(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if len(excluded) != 1 || excluded[0] != before.MusicBrainzRecordingID.UUID {
		t.Fatalf("excluded = %v, want the rejected recording still excluded", excluded)
	}
	target, err := queries.AcquisitionTarget(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if target.MusicBrainzRecordingID.UUID != instead {
		t.Fatalf("want names %s, want the recording it was resolved to since",
			target.MusicBrainzRecordingID.UUID)
	}
}

// waitForTheUndoToReachTheWant returns once the statement that takes a rejection
// back is waiting for the want's row.
//
// That wait is the whole point of the test above: by then the statement has read
// everything it will read, and the only thing left to happen is the update,
// against a row somebody else is holding. Waiting for the state rather than
// sleeping for a while is what makes the interleaving happen every run instead
// of most runs.
func waitForTheUndoToReachTheWant(ctx context.Context, pool *pgxpool.Pool) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_stat_activity
				WHERE datname = current_database()
				  AND wait_event_type = 'Lock'
				  AND query LIKE '%WITH restored AS%'
			)
		`).Scan(&waiting); err != nil {
			return err
		}
		if waiting {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("taking the rejection back never reached the want's row")
		}
		time.Sleep(25 * time.Millisecond)
	}
}
