package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Taking an answer back, for as long as the thing it asked for has not
// happened.
//
// The review queue is the list of questions Schall could not answer for itself.
// A person answers them by hand, one at a time, and four of the five answers are
// written down: accept this copy, none of these copies, this recording is not
// wanted, this entry was resolved to the wrong recording. Each is a row somebody
// pressed a key for, and until now there was no way back from a mis-press.
//
// ADR 0019 says a recorded answer may be taken back for as long as the thing it
// asked for has not happened, and that "has happened" is a fact in the database
// rather than a length of time. The four facts are different, one per answer, and
// each is written into the WHERE clause of the statement that performs the
// reversal — never read first and acted on afterwards. Two people pressing undo
// on one answer at the same moment settle as one reversal and one refusal
// because of that, and for no other reason.
//
// Nothing here creates a match. Every statement below either deletes a decision
// or returns a copy to 'held', which is the state meaning nobody has decided
// this. A copy returned to 'held' is graded again from scratch the next time
// anything looks at it.

// ErrCopyIsInTheLibrary reports an accepted copy that has already been brought
// in. Taking the acceptance back now would leave a file in the library that no
// decision claims, and let the want fetch the same recording a second time.
var ErrCopyIsInTheLibrary = errors.New("this copy is already in your library")

// ErrWantAnsweredAnotherWay reports an answer that is no longer the want's last
// word: another copy has been accepted or imported since, or the decision has
// already been taken back. Reversing now would leave two answered copies on one
// want, which is the state the one-answer-per-want guard exists to prevent.
var ErrWantAnsweredAnotherWay = errors.New(
	"this wishlist entry has already been answered another way since")

// ErrRecordingWantedElsewhere reports a recording another want is already
// looking for. Restoring this one would have Schall chase one recording twice.
var ErrRecordingWantedElsewhere = errors.New(
	"another wishlist entry is already looking for this recording")

// ErrEntryResolvedAgain reports an entry that has been resolved again since the
// wrong-recording answer was given. A new recording is being pursued, possibly
// with copies already fetched against it, and removing the old exclusion would
// not remove those.
var ErrEntryResolvedAgain = errors.New("this entry has already been resolved again")

// ErrSchallDecidedThis reports an attempt to take back the acquisition loop's own
// reading of the audio. Those are not a person's answers, so they are not a
// person's to withdraw: somebody who disagrees answers the question the loop
// raised instead.
var ErrSchallDecidedThis = errors.New("Schall decided this, not you")

// Answer names one of the four review outcomes that are written down. It is the
// same word the route that records the outcome uses, so the answer and the way
// back from it cannot be given two different names.
type Answer string

const (
	AnswerAccept         Answer = "accept"
	AnswerNoneOfThese    Answer = "none-of-these"
	AnswerNotWanted      Answer = "not-wanted"
	AnswerWrongRecording Answer = "wrong-recording"
)

// KnownAnswer reports whether a word names one of the four.
func KnownAnswer(answer Answer) bool {
	switch answer {
	case AnswerAccept, AnswerNoneOfThese, AnswerNotWanted, AnswerWrongRecording:
		return true
	}
	return false
}

// The four conditions, each written once.
//
// Each of these appears twice: in the WHERE clause of the statement that
// performs the reversal, and in the question the answer bar asks to decide
// whether to offer the control at all. Spelling one of them twice is how the
// button and the statement behind it would come to disagree, so they are
// spelled here and used from both places. The question is never a gate on the
// write — the write carries the condition itself.
const (
	// An accepted copy, while nothing has moved on disk. library_file_id is
	// written when the scan has made a library row of the imported file, and
	// import_status is 'imported' from the moment the bytes are copied in, which
	// is the earlier of the two. Both are asked about, because the file is in the
	// library from the earlier moment and the refusal is about the file.
	reversibleAccept = `
		acquisition_target_files.verdict = 'accepted'
		AND acquisition_target_files.decided_by = 'user'
		AND acquisition_target_files.library_file_id IS NULL
		AND NOT EXISTS (
		    SELECT 1 FROM download_requests
		    WHERE download_requests.id = acquisition_target_files.download_request_id
		      AND download_requests.import_status = 'imported'
		)
	`

	// A copy a person refused, while the want has taken no other answer. An
	// accepted verdict is what both an acceptance and a finished import write, so
	// one condition covers "accepted since" and "imported since" together.
	reversibleRefusal = `
		acquisition_target_files.verdict = 'discarded_audio'
		AND acquisition_target_files.decided_by = 'user'
		AND NOT EXISTS (
		    SELECT 1 FROM acquisition_target_files answered
		    WHERE answered.acquisition_target_id
		          = acquisition_target_files.acquisition_target_id
		      AND answered.verdict = 'accepted'
		)
	`

	// A want somebody stopped pursuing, while nothing else has claimed its
	// recording.
	//
	// Any other target naming the recording is enough to refuse, superseded ones
	// included, and that is the ADR's condition rather than a wider one. A second
	// entry that resolved to this recording while the want was stopped is exactly
	// the target that ends 'superseded' onto it — "the recording has since been
	// wanted by another target" — and it carries the sentence saying the
	// recording was one the user did not want. Restoring the want underneath it
	// would leave that other entry explaining itself with a decision that no
	// longer exists.
	reversibleNotWanted = `
		acquisition_targets.status = 'not_wanted'
		AND NOT EXISTS (
		    SELECT 1 FROM acquisition_targets others
		    WHERE others.id <> acquisition_targets.id
		      AND others.musicbrainz_recording_id
		          = acquisition_targets.musicbrainz_recording_id
		)
	`

	// A rejected resolution, while the entry has not been resolved again. An
	// entry that went back to the queue of questions without an answer has not
	// been re-resolved: resolution ran and produced nothing, which is not a new
	// recording being pursued. An entry a person has since keyed to an address
	// has been answered another way, and a key and a recording exclude each
	// other (ADR 0038 §1).
	reversibleWrongRecording = `
		acquisition_targets.musicbrainz_recording_id IS NULL
		AND acquisition_targets.status IN ('unresolved', 'awaiting_review')
		AND acquisition_targets.source IS NULL
	`
)

// AnswerIsReversible reports whether one recorded answer can still be taken
// back. The answer bar asks it so that the control is offered only while it
// would work; nothing writes on the strength of it.
func (q *Queries) AnswerIsReversible(
	ctx context.Context, answer Answer, id uuid.UUID,
) (bool, error) {
	var query string
	switch answer {
	case AnswerAccept:
		query = `
			SELECT EXISTS (
				SELECT 1 FROM acquisition_target_files
				WHERE acquisition_target_files.id = $1 AND` + reversibleAccept + `
			)
		`
	case AnswerNoneOfThese:
		query = `
			SELECT EXISTS (
				SELECT 1 FROM acquisition_target_files
				WHERE acquisition_target_files.acquisition_target_id = $1
				  AND` + reversibleRefusal + `
			)
		`
	case AnswerNotWanted:
		query = `
			SELECT EXISTS (
				SELECT 1 FROM acquisition_targets
				WHERE acquisition_targets.id = $1 AND` + reversibleNotWanted + `
			)
		`
	case AnswerWrongRecording:
		query = `
			SELECT EXISTS (
				SELECT 1 FROM acquisition_target_rejected_recordings rejections
				JOIN acquisition_targets
				    ON acquisition_targets.id = rejections.acquisition_target_id
				WHERE rejections.acquisition_target_id = $1
				  AND rejections.decided_by = 'user'
				  AND` + reversibleWrongRecording + `
				  AND NOT EXISTS (
				      SELECT 1 FROM acquisition_targets others
				      WHERE others.musicbrainz_recording_id
				            = rejections.musicbrainz_recording_id
				        AND others.status <> 'superseded'
				  )
			)
		`
	default:
		return false, fmt.Errorf("%q is not an answer that is written down", answer)
	}
	var reversible bool
	if err := q.db.QueryRow(ctx, query, id).Scan(&reversible); err != nil {
		return false, fmt.Errorf("read whether %s can still be taken back: %w", id, err)
	}
	return reversible, nil
}

// TakeBackAcceptedCopy returns an accepted copy to the state it was in before
// somebody accepted it: 'held', decided by nobody, with no sentence and its
// clock restarted, and the want back in the queue with the same copies it had.
//
// The import is stood down with it. Accepting re-opened the request and queued
// the job that brings the file in, and leaving either behind would import the
// copy anyway — the decision would be gone and the music would arrive regardless.
// A job already running is left alone rather than chased: the copy is no longer
// accepted by anybody, so the import grades it from scratch and holds it again
// if nothing can decide, which is exactly what would have happened had the
// acceptance never been given.
//
// heldSummary is what the request says while it waits for a decision again, and
// targetSummary what the want says. Both are written by internal/acquisition,
// which owns every sentence a want wears.
//
// ErrCopyIsInTheLibrary means the file has landed and the way back is to remove
// it from the library. ErrSchallDecidedThis means the verdict is the loop's own.
// ErrWantAnsweredAnotherWay means it is no longer accepted at all.
func (q *Queries) TakeBackAcceptedCopy(
	ctx context.Context, copyID uuid.UUID, heldSummary, targetSummary string,
) error {
	err := q.inTransaction(ctx, "taking back an accepted copy", func(tx pgx.Tx) error {
		var (
			targetID  uuid.UUID
			requestID uuid.NullUUID
		)
		if err := tx.QueryRow(ctx, `
			UPDATE acquisition_target_files
			SET verdict = 'held', decided_by = 'schall', summary = '',
			    decided_at = now(), updated_at = now()
			WHERE acquisition_target_files.id = $1 AND`+reversibleAccept+`
			RETURNING acquisition_target_id, download_request_id
		`, copyID).Scan(&targetID, &requestID); err != nil {
			return err
		}
		if requestID.Valid {
			// Only a request the acceptance itself re-opened is closed again. One
			// that has moved on to 'validating' is being read right now, and its own
			// settling is the honest account of what the copy turns out to be.
			if _, err := tx.Exec(ctx, `
				UPDATE download_requests
				SET import_status = 'needs_review', import_error = $2, updated_at = now()
				WHERE id = $1 AND import_status = 'pending'
			`, requestID.UUID, heldSummary); err != nil {
				return err
			}
			// The queued import goes too. Left on the queue it would claim a request
			// that is no longer waiting to be imported, fail for it, and spend its
			// attempts saying so.
			if _, err := tx.Exec(ctx, `
				DELETE FROM jobs
				WHERE kind = 'import_download'
				  AND status = 'queued'
				  AND payload->>'requestId' = $1::text
			`, requestID.UUID.String()); err != nil {
				return err
			}
			if err := recordImportReview(
				ctx, tx, requestID.UUID, "withdrawn", heldSummary, nil,
			); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `
			UPDATE acquisition_targets
			SET summary = $2, updated_at = now()
			WHERE id = $1 AND `+searchedWant("")+`
		`, targetID, targetSummary)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return q.whyTheCopyStands(ctx, copyID)
	}
	return err
}

// whyTheCopyStands says which refusal an accepted copy that would not move is.
//
// It runs after the statement above has already refused, so it is a sentence
// rather than a gate: the guard was in the write, and reading now cannot change
// what was written. pgx.ErrNoRows means there is no such copy at all.
func (q *Queries) whyTheCopyStands(ctx context.Context, copyID uuid.UUID) error {
	var (
		verdict   string
		decidedBy string
		landed    bool
	)
	if err := q.db.QueryRow(ctx, `
		SELECT verdict, decided_by,
		       library_file_id IS NOT NULL OR EXISTS (
		           SELECT 1 FROM download_requests
		           WHERE download_requests.id = acquisition_target_files.download_request_id
		             AND download_requests.import_status = 'imported'
		       )
		FROM acquisition_target_files
		WHERE id = $1
	`, copyID).Scan(&verdict, &decidedBy, &landed); err != nil {
		return err
	}
	switch {
	case landed:
		return ErrCopyIsInTheLibrary
	// No longer an acceptance at all. Two people pressing undo at the same moment
	// is the ordinary way to arrive here, and the copy is back to waiting for a
	// decision — which the loop, not a person, is now the last to have touched.
	// Reading that as Schall's own decision would blame it for the undo.
	case verdict != AcquiredFileAccepted:
		return ErrWantAnsweredAnotherWay
	case decidedBy == "schall":
		return ErrSchallDecidedThis
	default:
		return ErrWantAnsweredAnotherWay
	}
}

// TakeBackRefusedCopies returns to 'held' every copy a person refused with "none
// of these", and puts the want back in front of them with those copies waiting.
//
// Every row it wrote has to be exactly as it left it — still 'discarded_audio',
// still the person's — and the want must have taken no other answer since. A
// want the sweeper has fetched and accepted another copy for is refused rather
// than reversed: restoring the old copies beside an accepted one would put two
// answered copies on one want.
//
// The refusal is a single press covering every copy held at the time, so taking
// it back covers the same set. A copy the loop refused on the audio is not part
// of it and is never touched.
//
// ErrWantAnsweredAnotherWay means the want has moved on. ErrSchallDecidedThis
// means the refusals on this want are the loop's own reading of the audio.
func (q *Queries) TakeBackRefusedCopies(
	ctx context.Context, targetID uuid.UUID, heldSummary, targetSummary string,
) error {
	err := q.inTransaction(ctx, "taking back a refusal of every copy", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE acquisition_target_files
			SET verdict = 'held', decided_by = 'schall', summary = '',
			    decided_at = now(), updated_at = now()
			WHERE acquisition_target_files.acquisition_target_id = $1
			  AND`+reversibleRefusal+`
			RETURNING download_request_id
		`, targetID)
		if err != nil {
			return err
		}
		// Every row the statement moved is counted, not only the ones naming a
		// request: a copy refused before anything recorded which request fetched it
		// is still a copy that has just been returned, and reading none of them as
		// nothing moved would refuse a reversal that had already happened.
		returned := 0
		requestIDs := make([]uuid.UUID, 0, 4)
		for rows.Next() {
			var requestID uuid.NullUUID
			if err := rows.Scan(&requestID); err != nil {
				rows.Close()
				return err
			}
			returned++
			if requestID.Valid {
				requestIDs = append(requestIDs, requestID.UUID)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if returned == 0 {
			return pgx.ErrNoRows
		}
		// The requests the refusal closed are opened again, and only those: a
		// request discarded for any other reason is another file's story.
		if _, err := tx.Exec(ctx, `
			UPDATE download_requests
			SET import_status = 'needs_review', import_error = $2, updated_at = now()
			WHERE id = ANY($1) AND import_status = 'discarded'
		`, requestIDs, heldSummary); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			UPDATE acquisition_targets
			SET summary = $2, updated_at = now()
			WHERE id = $1 AND `+searchedWant("")+`
		`, targetID, targetSummary)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return q.whyTheRefusalStands(ctx, targetID)
	}
	return err
}

// whyTheRefusalStands says which refusal a want whose copies would not move is.
func (q *Queries) whyTheRefusalStands(ctx context.Context, targetID uuid.UUID) error {
	var bySchall bool
	if err := q.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM acquisition_target_files
			WHERE acquisition_target_id = $1
			  AND verdict = 'discarded_audio' AND decided_by = 'schall'
		)
		AND NOT EXISTS (
			SELECT 1 FROM acquisition_target_files
			WHERE acquisition_target_id = $1
			  AND verdict = 'discarded_audio' AND decided_by = 'user'
		)
	`, targetID).Scan(&bySchall); err != nil {
		return err
	}
	if bySchall {
		return ErrSchallDecidedThis
	}
	return ErrWantAnsweredAnotherWay
}

// TakeBackNotWanted returns a want somebody stopped pursuing to being pursued,
// and lets the sweeper look for it again.
//
// It is the same return PursueAcquisitionTargetAgain makes from the want's own
// page, with one condition added: nothing else may have claimed the recording in
// the meantime. Schall holds one live want per recording, so restoring into a
// recording another want is already looking for would be two wants for one piece
// of music — and the unique index would refuse it at the last moment rather than
// with a sentence.
//
// A want stopped before anybody resolved it goes back to waiting to be resolved,
// not to waiting for a copy, and the sentence it wears is chosen by the same
// expression that chooses the state so the two cannot disagree.
//
// ErrRecordingWantedElsewhere means another want has the recording.
// ErrWantAnsweredAnotherWay means this one is no longer stopped.
func (q *Queries) TakeBackNotWanted(
	ctx context.Context, id uuid.UUID, unresolvedSummary, wantedSummary string,
	nextAttemptAt time.Time,
) (AcquisitionTargetRow, error) {
	target, err := scanAcquisitionTarget(q.db.QueryRow(ctx, `
		UPDATE acquisition_targets
		SET status = CASE
		        WHEN musicbrainz_recording_id IS NULL THEN 'unresolved'
		        ELSE 'pending'
		    END,
		    not_wanted_at = NULL,
		    `+resolutionSchedule("$4::timestamptz")+`,
		    `+armSearch(`CASE WHEN musicbrainz_recording_id IS NULL THEN 'unresolved' END`)+`,
		    last_error = NULL,
		    summary = CASE
		        WHEN source IS NOT NULL THEN $3
		        WHEN musicbrainz_recording_id IS NULL THEN $2
		        ELSE $3
		    END,
		    updated_at = now()
		WHERE acquisition_targets.id = $1 AND`+reversibleNotWanted+`
		RETURNING`+acquisitionTargetColumns,
		id, unresolvedSummary, wantedSummary, nextAttemptAt))
	// The index catches what no condition inside one statement can: a want that
	// claimed the recording between this statement's snapshot and this write. It
	// means what the condition means, so it is told the same way.
	if isUniqueViolation(err) {
		return AcquisitionTargetRow{}, ErrRecordingWantedElsewhere
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return AcquisitionTargetRow{}, q.whyTheStopStands(ctx, id)
	}
	return target, err
}

// whyTheStopStands says which refusal a want that would not come back is.
func (q *Queries) whyTheStopStands(ctx context.Context, id uuid.UUID) error {
	var claimed bool
	if err := q.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM acquisition_targets others
			WHERE others.id <> acquisition_targets.id
			  AND others.musicbrainz_recording_id
			      = acquisition_targets.musicbrainz_recording_id
		)
		FROM acquisition_targets WHERE id = $1
	`, id).Scan(&claimed); err != nil {
		return err
	}
	if claimed {
		return ErrRecordingWantedElsewhere
	}
	return ErrWantAnsweredAnotherWay
}

// TakeBackWrongRecording forgets that somebody said an entry names the wrong
// recording, and puts that recording back as the entry's answer.
//
// Saying "wrong song" does two things at once: it remembers the recording so the
// lookup that follows cannot return it again, and it sends the entry back to be
// looked up. Taking it back undoes both, in one statement — the exclusion is
// deleted and the recording is written back onto the want, which becomes pending
// and due. The candidates a re-run offered go too: they were answers to a
// question that is no longer being asked.
//
// One step back, not a history: the most recent exclusion a person recorded is
// the one taken back, and any older one stays.
//
// The two things the rejection erased come back with it. Recording a rejection
// clears the release group the resolution ran through and the method it was
// reached by, because an unresolved want holds no answer at all; the rejection
// row keeps a copy of both from the moment it is written, and this statement
// puts them back. Nothing is inferred from them on the way back — the values
// written are the values the want had before anybody said the recording was
// wrong.
//
// A rejection recorded before those two columns existed remembers neither, and
// it is taken back exactly as it was before they existed: the want comes back
// with no release group, and its method reads 'manual', which says the recording
// stands on the say-so of the person who put it back. That is the only thing
// still known to be true about how it got there, and a fuller answer would have
// to be invented.
//
// ErrEntryResolvedAgain means resolution has run again and produced a recording,
// which is the decision having been acted on. ErrRecordingWantedElsewhere means
// another want is already looking for the recording being restored.
func (q *Queries) TakeBackWrongRecording(
	ctx context.Context, id uuid.UUID, summary string, nextAttemptAt time.Time,
) (AcquisitionTargetRow, error) {
	target, err := scanAcquisitionTarget(q.db.QueryRow(ctx, `
		WITH restored AS (
			SELECT rejections.id AS rejection_id,
			       rejections.musicbrainz_recording_id AS recording_id,
			       rejections.musicbrainz_release_group_id AS release_group_id,
			       -- A rejection written before the release group and the method
			       -- were remembered has neither, and 'manual' is what the want
			       -- wore in that case before they were remembered at all.
			       COALESCE(rejections.resolution_method, 'manual') AS resolution_method,
			       acquisition_targets.id AS target_id
			FROM acquisition_target_rejected_recordings rejections
			JOIN acquisition_targets
			    ON acquisition_targets.id = rejections.acquisition_target_id
			WHERE rejections.acquisition_target_id = $1
			  AND rejections.decided_by = 'user'
			  AND`+reversibleWrongRecording+`
			  AND NOT EXISTS (
			      SELECT 1 FROM acquisition_targets others
			      WHERE others.musicbrainz_recording_id = rejections.musicbrainz_recording_id
			        AND others.status <> 'superseded'
			  )
			ORDER BY rejections.decided_at DESC
			LIMIT 1
		),
		wanted AS (
			UPDATE acquisition_targets
			SET musicbrainz_recording_id = (SELECT recording_id FROM restored),
			    musicbrainz_release_group_id = (SELECT release_group_id FROM restored),
			    resolution_method = (SELECT resolution_method FROM restored),
			    resolved_at = now(),
			    status = 'pending',
			    review_reason = NULL,
			    summary = $2,
			    last_error = NULL,
			    next_attempt_at = $3,
			    next_search_at = NULL,
			    search_attempts = 0,
			    updated_at = now()
			-- The condition is repeated here, and not only in the row this
			-- statement selected above. A second undo of the same rejection waits
			-- on this row's lock and is re-checked against what the first one
			-- wrote, and it is this clause that is re-checked -- the rows chosen
			-- above are chosen once and cannot say that the entry has an answer
			-- again. Without it the second press would write the same answer
			-- twice and report both as having done something.
			WHERE id = (SELECT target_id FROM restored)
			  AND`+reversibleWrongRecording+`
			RETURNING`+acquisitionTargetColumns+`,
			    (SELECT rejection_id FROM restored) AS rejection_id
		),
		-- Both deletions take their rows from the update, and that is what makes
		-- the reversal one act rather than three. Each part of a WITH sees the
		-- same snapshot of the database, so a deletion written beside the update
		-- rather than downstream of it decides what to delete from the state the
		-- statement started in, while the update decides whether to write from
		-- the state it finds once it holds the want's lock. Those two states
		-- differ exactly when somebody resolved the entry again in between, and
		-- that is the case where the deletions would run while the update
		-- refused: the record of a person saying the recording was wrong would be
		-- gone, and they would be told nothing had changed. Reading the rejection
		-- out of the update's own RETURNING removes the choice -- there is
		-- nothing to delete unless the want was written back.
		forgotten AS (
			DELETE FROM acquisition_target_rejected_recordings
			WHERE id IN (SELECT rejection_id FROM wanted)
		),
		withdrawn AS (
			DELETE FROM acquisition_target_candidates
			WHERE acquisition_target_id IN (SELECT id FROM wanted)
		)
		SELECT`+acquisitionTargetColumns+`FROM wanted AS acquisition_targets
	`, id, summary, nextAttemptAt))
	if isUniqueViolation(err) {
		return AcquisitionTargetRow{}, ErrRecordingWantedElsewhere
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return AcquisitionTargetRow{}, q.whyTheRejectionStands(ctx, id)
	}
	return target, err
}

// whyTheRejectionStands says which refusal an entry that would not take its old
// recording back is.
func (q *Queries) whyTheRejectionStands(ctx context.Context, id uuid.UUID) error {
	var claimed bool
	if err := q.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM acquisition_target_rejected_recordings rejections
			JOIN acquisition_targets others
			    ON others.musicbrainz_recording_id = rejections.musicbrainz_recording_id
			WHERE rejections.acquisition_target_id = $1
			  AND rejections.decided_by = 'user'
			  AND others.status <> 'superseded'
		)
		FROM acquisition_targets WHERE id = $1
	`, id).Scan(&claimed); err != nil {
		return err
	}
	if claimed {
		return ErrRecordingWantedElsewhere
	}
	return ErrEntryResolvedAgain
}
