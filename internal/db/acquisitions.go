package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// AcquisitionTargetRow is one wanted recording. The entry columns are what
// somebody asked for; the MusicBrainz columns are what that was resolved to,
// and the two are kept apart so a wrong resolution can be replaced without
// losing the request behind it.
type AcquisitionTargetRow struct {
	ID              uuid.UUID
	Origin          string
	OriginAlbumID   uuid.NullUUID
	EntryArtist     string
	EntryTitle      string
	EntryAlbum      string
	EntryDurationMS pgtype.Int4
	EntryISRC       pgtype.Text
	// EntryExplicit is the source saying the track somebody asked for is the
	// explicit recording. Null is silence, which is every entry no source said
	// it of.
	EntryExplicit             pgtype.Bool
	MusicBrainzRecordingID    uuid.NullUUID
	MusicBrainzReleaseGroupID uuid.NullUUID
	ResolutionMethod          pgtype.Text
	ResolvedAt                pgtype.Timestamptz
	Status                    string
	ReviewReason              pgtype.Text
	Summary                   string
	LastError                 pgtype.Text
	Attempts                  int32
	LastAttemptAt             pgtype.Timestamptz
	NextAttemptAt             pgtype.Timestamptz
	AcquiredLibraryFileID     uuid.NullUUID
	AcquiredAt                pgtype.Timestamptz
	NotWantedAt               pgtype.Timestamptz
	SupersededByID            uuid.NullUUID
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
	// AnchorUnavailable says why the sample could not be stored, when it could
	// not be had. AnchorAttempts counts sample attempts, and AnchorNextAttemptAt
	// is the next time a deferred sample fetch will run.
	AnchorUnavailable   pgtype.Text
	AnchorAttempts      int32
	AnchorNextAttemptAt pgtype.Timestamptz
	// UpgradeOfLibraryFileID names the file this want exists to replace, set
	// only when Origin is 'upgrade'. See migration 00072.
	UpgradeOfLibraryFileID uuid.NullUUID
	// RecheckReason says what a stopped want is waiting for, and is empty when
	// it is waiting for a person. RecheckAfter is when the next automatic
	// re-judge is due, and RecheckAttempts how many have been spent. See
	// migration 00096.
	RecheckReason    pgtype.Text
	RecheckAfter     pgtype.Timestamptz
	RecheckAttempts  int32
	BelowFloorStreak int16
	BelowFloorSince  pgtype.Timestamptz
	FloorWaivedAt    pgtype.Timestamptz
	// HoldsAnImportedCopy says a copy of this want was accepted and has become a
	// file in the library. Only ListAcquisitionTargets reads it, because only the
	// list of wants has to tell a want that is being looked for from one that has
	// its music and is waiting on a person; everywhere else it is false and means
	// nothing.
	HoldsAnImportedCopy bool
	// UpgradeOfPath is where the file named by UpgradeOfLibraryFileID sits, for
	// a person reading the want list. Only ListAcquisitionTargets reads it. It
	// is read by a LEFT JOIN, so a want whose old file has since been deleted —
	// the upgrade already succeeded and replaced it — reads as an empty string
	// rather than failing the whole row.
	UpgradeOfPath string
}

type AcquisitionTargetPage struct {
	Items []AcquisitionTargetRow
	Total int64
}

const acquisitionTargetColumns = `
	acquisition_targets.id,
	acquisition_targets.origin,
	acquisition_targets.origin_album_id,
	acquisition_targets.entry_artist,
	acquisition_targets.entry_title,
	acquisition_targets.entry_album,
	acquisition_targets.entry_duration_ms,
	acquisition_targets.entry_isrc,
	acquisition_targets.entry_explicit,
	acquisition_targets.musicbrainz_recording_id,
	acquisition_targets.musicbrainz_release_group_id,
	acquisition_targets.resolution_method,
	acquisition_targets.resolved_at,
	acquisition_targets.status,
	acquisition_targets.review_reason,
	acquisition_targets.summary,
	acquisition_targets.last_error,
	acquisition_targets.attempts,
	acquisition_targets.last_attempt_at,
	acquisition_targets.next_attempt_at,
	acquisition_targets.acquired_library_file_id,
	acquisition_targets.acquired_at,
	acquisition_targets.not_wanted_at,
	acquisition_targets.superseded_by_id,
	acquisition_targets.created_at,
	acquisition_targets.updated_at,
	acquisition_targets.anchor_unavailable,
	acquisition_targets.anchor_attempts,
	acquisition_targets.anchor_next_attempt_at,
	acquisition_targets.upgrade_of_library_file_id,
	acquisition_targets.recheck_reason,
	acquisition_targets.recheck_after,
	acquisition_targets.recheck_attempts,
	acquisition_targets.below_floor_streak,
	acquisition_targets.below_floor_since,
	acquisition_targets.floor_waived_at
`

func scanAcquisitionTarget(row pgx.Row) (AcquisitionTargetRow, error) {
	var target AcquisitionTargetRow
	return target, scanAcquisitionTargetInto(row, &target)
}

// scanAcquisitionTargetInto reads a target from a row that may carry more
// columns after it. rest are scanned in the order the query listed them.
func scanAcquisitionTargetInto(row pgx.Row, target *AcquisitionTargetRow, rest ...any) error {
	columns := append([]any{
		&target.ID, &target.Origin, &target.OriginAlbumID,
		&target.EntryArtist, &target.EntryTitle, &target.EntryAlbum,
		&target.EntryDurationMS, &target.EntryISRC, &target.EntryExplicit,
		&target.MusicBrainzRecordingID, &target.MusicBrainzReleaseGroupID,
		&target.ResolutionMethod, &target.ResolvedAt,
		&target.Status, &target.ReviewReason, &target.Summary, &target.LastError,
		&target.Attempts, &target.LastAttemptAt, &target.NextAttemptAt,
		&target.AcquiredLibraryFileID, &target.AcquiredAt, &target.NotWantedAt,
		&target.SupersededByID, &target.CreatedAt, &target.UpdatedAt,
		&target.AnchorUnavailable, &target.AnchorAttempts, &target.AnchorNextAttemptAt,
		&target.UpgradeOfLibraryFileID,
		&target.RecheckReason, &target.RecheckAfter, &target.RecheckAttempts,
		&target.BelowFloorStreak, &target.BelowFloorSince, &target.FloorWaivedAt,
	}, rest...)
	return row.Scan(columns...)
}

type CreateAcquisitionTargetParams struct {
	Origin                    string
	OriginAlbumID             uuid.NullUUID
	EntryArtist               string
	EntryTitle                string
	EntryAlbum                string
	EntryDurationMS           pgtype.Int4
	EntryISRC                 pgtype.Text
	EntryExplicit             pgtype.Bool
	MusicBrainzRecordingID    uuid.NullUUID
	MusicBrainzReleaseGroupID uuid.NullUUID
	ResolutionMethod          pgtype.Text
	Summary                   string
	// UpgradeOfLibraryFileID names the file this want exists to replace. See
	// AcquisitionTargetRow.UpgradeOfLibraryFileID.
	UpgradeOfLibraryFileID uuid.NullUUID
}

// CreateAcquisitionTarget records a want, reporting whether it is a new one.
// Asking twice for the same recording is not two wants, so the second call
// returns the target that already exists — including one somebody marked as not
// wanted, because re-importing the playlist that created it must not quietly
// resurrect a decision to stop pursuing it.
//
// A target whose entry has not been resolved yet has no recording to be the
// same as, so it is always new. Two entries that later resolve to one recording
// are merged by whatever resolved them.
func (q *Queries) CreateAcquisitionTarget(
	ctx context.Context, params CreateAcquisitionTargetParams,
) (uuid.UUID, bool, error) {
	var (
		id      uuid.UUID
		created bool
	)
	err := q.db.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO acquisition_targets (
				origin, origin_album_id, entry_artist, entry_title, entry_album,
				entry_duration_ms, entry_isrc, musicbrainz_recording_id,
				musicbrainz_release_group_id, resolution_method, resolved_at,
				status, summary, next_attempt_at, upgrade_of_library_file_id,
				entry_explicit
			)
			VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
				CASE WHEN $8::uuid IS NULL THEN NULL ELSE now() END,
				CASE WHEN $8::uuid IS NULL THEN 'unresolved' ELSE 'pending' END,
				$11, now(), $12, $13
			)
			ON CONFLICT (musicbrainz_recording_id)
				WHERE musicbrainz_recording_id IS NOT NULL AND status <> 'superseded'
			DO NOTHING
			RETURNING id
		)
		SELECT id, true FROM inserted
		UNION ALL
		SELECT id, false
		FROM acquisition_targets
		WHERE musicbrainz_recording_id = $8
		  AND status <> 'superseded'
		  AND NOT EXISTS (SELECT 1 FROM inserted)
		LIMIT 1
	`,
		params.Origin, params.OriginAlbumID, params.EntryArtist, params.EntryTitle,
		params.EntryAlbum, params.EntryDurationMS, params.EntryISRC,
		params.MusicBrainzRecordingID, params.MusicBrainzReleaseGroupID,
		params.ResolutionMethod, params.Summary, params.UpgradeOfLibraryFileID,
		params.EntryExplicit,
	).Scan(&id, &created)
	// The fallback above reads the snapshot this statement started with, so a
	// row another caller committed while ON CONFLICT was waiting on its lock is
	// invisible to it and the statement returns nothing at all. Two imports of
	// the same playlist at once is exactly that race, and it must not read as
	// "the want does not exist". A second statement sees the committed row.
	if errors.Is(err, pgx.ErrNoRows) && params.MusicBrainzRecordingID.Valid {
		err = q.db.QueryRow(ctx, `
			SELECT id
			FROM acquisition_targets
			WHERE musicbrainz_recording_id = $1 AND status <> 'superseded'
		`, params.MusicBrainzRecordingID.UUID).Scan(&id)
		return id, false, err
	}
	return id, created, err
}

// AcquisitionTarget returns one target, or pgx.ErrNoRows when it does not
// exist.
func (q *Queries) AcquisitionTarget(ctx context.Context, id uuid.UUID) (AcquisitionTargetRow, error) {
	return scanAcquisitionTarget(q.db.QueryRow(ctx, `
		SELECT`+acquisitionTargetColumns+`
		FROM acquisition_targets
		WHERE acquisition_targets.id = $1
	`, id))
}

// ListAcquisitionTargetsParams asks for a page of wants.
//
// Statuses names the states to include, and an empty list means every state.
// It is a list rather than one name because the states a want passes through
// while nobody has to do anything about it — waiting to be resolved, waiting
// for a copy, being searched for right now — are one thing to the person who
// asked for the music, and asking for them one at a time would be three reads
// and three counts of the same list.
type ListAcquisitionTargetsParams struct {
	Statuses []string
	Limit    int32
	Offset   int32
}

func (q *Queries) ListAcquisitionTargets(
	ctx context.Context, params ListAcquisitionTargetsParams,
) (AcquisitionTargetPage, error) {
	var result AcquisitionTargetPage
	statuses := params.Statuses
	if statuses == nil {
		statuses = []string{}
	}
	filter := `
		WHERE (cardinality($1::text[]) = 0 OR acquisition_targets.status = ANY($1::text[]))
	`
	if err := q.db.QueryRow(ctx, `
		SELECT count(*)
		FROM acquisition_targets`+filter,
		statuses,
	).Scan(&result.Total); err != nil {
		return result, err
	}

	rows, err := q.db.Query(ctx, `
		SELECT`+acquisitionTargetColumns+`,
		       EXISTS (
		           -- Whether the want has its music already. A want reads as being
		           -- looked for otherwise, and one holding a copy the library files
		           -- under another recording is not being looked for at all.
		           SELECT 1 FROM acquisition_target_files
		           WHERE acquisition_target_files.acquisition_target_id
		                 = acquisition_targets.id
		             AND acquisition_target_files.verdict = 'accepted'
		             AND acquisition_target_files.library_file_id IS NOT NULL
		       ),
		       -- Where the file this upgrade want exists to replace sits, for a
		       -- person reading the list. The LEFT JOIN means a want whose old
		       -- file has since gone — the upgrade already succeeded and deleted
		       -- it — reads back as an empty string rather than failing the row.
		       coalesce(upgrade_of.path, '')
		FROM acquisition_targets
		LEFT JOIN library_files upgrade_of
		    ON upgrade_of.id = acquisition_targets.upgrade_of_library_file_id
	`+filter+`
		ORDER BY acquisition_targets.created_at DESC, acquisition_targets.id
		LIMIT $2 OFFSET $3
	`, statuses, params.Limit, params.Offset)
	if err != nil {
		return result, err
	}
	defer rows.Close()

	result.Items = make([]AcquisitionTargetRow, 0)
	for rows.Next() {
		var target AcquisitionTargetRow
		if err := scanAcquisitionTargetInto(rows, &target,
			&target.HoldsAnImportedCopy, &target.UpgradeOfPath); err != nil {
			return result, err
		}
		result.Items = append(result.Items, target)
	}
	return result, rows.Err()
}

// StopPursuingAcquisitionTarget records that the user does not want this
// recording after all. It is scoped to the one recording and touches nothing
// else: the release it came from and the artist behind it are untouched, and a
// file already acquired stays in the library, because this is a decision about
// pursuit rather than about owned music.
//
// pgx.ErrNoRows means the target was already finished with — acquired, stopped
// once already, or merged into another.
func (q *Queries) StopPursuingAcquisitionTarget(
	ctx context.Context, id uuid.UUID, summary string,
) (AcquisitionTargetRow, error) {
	return scanAcquisitionTarget(q.db.QueryRow(ctx, `
		UPDATE acquisition_targets
		SET status = 'not_wanted',
		    not_wanted_at = now(),
		    next_attempt_at = NULL,
		    review_reason = NULL,
		    summary = $2,
		    updated_at = now()
		WHERE id = $1
		  AND status IN ('unresolved', 'pending', 'searching', 'awaiting_review')
		RETURNING`+acquisitionTargetColumns, id, summary))
}

// PursueAcquisitionTargetAgain withdraws a decision to stop. A decision the
// user made is never overwritten by anything automatic, but they may take it
// back themselves, exactly as a manual file identity may be withdrawn.
//
// A target stopped before anybody resolved it goes back to waiting to be
// resolved, not to waiting for a copy, and it says so: the sentence is chosen
// by the same expression that chooses the state, so the two cannot disagree.
//
// pgx.ErrNoRows means the target was not one somebody had stopped pursuing.
func (q *Queries) PursueAcquisitionTargetAgain(
	ctx context.Context, id uuid.UUID, unresolvedSummary, wantedSummary string, nextAttemptAt time.Time,
) (AcquisitionTargetRow, error) {
	return scanAcquisitionTarget(q.db.QueryRow(ctx, `
		UPDATE acquisition_targets
		SET status = CASE
		        WHEN musicbrainz_recording_id IS NULL THEN 'unresolved'
		        ELSE 'pending'
		    END,
		    not_wanted_at = NULL,
		    next_attempt_at = $4,
		    last_error = NULL,
		    summary = CASE
		        WHEN musicbrainz_recording_id IS NULL THEN $2
		        ELSE $3
		    END,
		    updated_at = now()
		WHERE id = $1 AND status = 'not_wanted'
		RETURNING`+acquisitionTargetColumns, id, unresolvedSummary, wantedSummary, nextAttemptAt))
}

// TakeBestAvailableAcquisitionTarget records that the user accepts a copy below
// the bitrate floor for this want only. It is a pending-only decision: anything
// settled, stopped, or being resolved has no copy for this control to take.
func (q *Queries) TakeBestAvailableAcquisitionTarget(
	ctx context.Context, id uuid.UUID,
) (AcquisitionTargetRow, error) {
	return scanAcquisitionTarget(q.db.QueryRow(ctx, `
		UPDATE acquisition_targets
		SET floor_waived_at = now(),
		    next_attempt_at = now(),
		    below_floor_streak = 0,
		    below_floor_since = NULL,
		    updated_at = now()
		WHERE id = $1 AND status = 'pending'
		RETURNING`+acquisitionTargetColumns, id))
}

// KeepAcquisitionTargetFloor withdraws the user's bitrate-floor waiver and
// gives the want an immediate look under the restored floor.
func (q *Queries) KeepAcquisitionTargetFloor(
	ctx context.Context, id uuid.UUID,
) (AcquisitionTargetRow, error) {
	return scanAcquisitionTarget(q.db.QueryRow(ctx, `
		UPDATE acquisition_targets
		SET floor_waived_at = NULL,
		    next_attempt_at = now(),
		    below_floor_streak = 0,
		    below_floor_since = NULL,
		    updated_at = now()
		WHERE id = $1 AND status = 'pending' AND floor_waived_at IS NOT NULL
		RETURNING`+acquisitionTargetColumns, id))
}

// ErrNothingToReject reports a wrong-target decision about a target that has no
// resolution to reject — nobody has answered it yet, or somebody withdrew the
// answer meanwhile. There is nothing to remember and nothing to re-open.
var ErrNothingToReject = errors.New("this wishlist entry has no resolution to reject")

// RejectAcquisitionTargetResolution records that the recording an entry was
// resolved to is the wrong one, and sends the entry back to be looked up again.
//
// The rejection and the re-opening are one statement because either alone is
// wrong in the same direction. Remembering without re-opening leaves the target
// pointing at a recording its owner has just disowned; re-opening without
// remembering hands the resolver the same wrong answer to find again. The
// candidates offered for the old answer go too: they were the reasons for a
// question that has now been answered differently.
//
// The rejection also keeps the release group the resolution ran through and the
// method it was reached by. The re-opening below clears both from the want,
// because an unresolved want holds no answer, and they exist nowhere else — so
// the only moment they can be written down is this one. Taking the rejection
// back puts them back on the want. They are remembered, never read: nothing
// resolves, grades or matches on them while the rejection stands.
//
// ErrNothingToReject means there was no answer to reject.
func (q *Queries) RejectAcquisitionTargetResolution(
	ctx context.Context, id uuid.UUID, summary, detail string, nextAttemptAt time.Time,
) (AcquisitionTargetRow, error) {
	row, err := scanAcquisitionTarget(q.db.QueryRow(ctx, `
		WITH rejected AS (
			SELECT id, musicbrainz_recording_id, musicbrainz_release_group_id,
			       resolution_method
			FROM acquisition_targets
			WHERE id = $1
			  AND musicbrainz_recording_id IS NOT NULL
			  AND status IN ('pending', 'searching', 'awaiting_review')
		),
		remembered AS (
			INSERT INTO acquisition_target_rejected_recordings (
				acquisition_target_id, musicbrainz_recording_id, summary, decided_by,
				musicbrainz_release_group_id, resolution_method
			)
			SELECT id, musicbrainz_recording_id, $3, 'user',
			       musicbrainz_release_group_id, resolution_method
			FROM rejected
			ON CONFLICT (acquisition_target_id, musicbrainz_recording_id) DO NOTHING
		),
		withdrawn AS (
			DELETE FROM acquisition_target_candidates
			WHERE acquisition_target_id = (SELECT id FROM rejected)
		),
		reopened AS (
			UPDATE acquisition_targets
			SET status = 'unresolved',
			    musicbrainz_recording_id = NULL,
			    musicbrainz_release_group_id = NULL,
			    resolution_method = NULL,
			    resolved_at = NULL,
			    review_reason = NULL,
			    summary = $2,
			    last_error = NULL,
			    next_attempt_at = $4,
			    updated_at = now()
			WHERE id = (SELECT id FROM rejected)
			RETURNING`+acquisitionTargetColumns+`
		)
		SELECT`+acquisitionTargetColumns+`FROM reopened AS acquisition_targets
	`, id, summary, detail, nextAttemptAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return AcquisitionTargetRow{}, ErrNothingToReject
	}
	return row, err
}

// RejectedTargetResolutions reads the recordings somebody has said this entry is
// not, so the resolver can drop them before grading rather than after. Dropping
// them after would let an excluded recording be the one conclusive answer and
// leave the entry stuck on it.
func (q *Queries) RejectedTargetResolutions(
	ctx context.Context, id uuid.UUID,
) ([]uuid.UUID, error) {
	rows, err := q.db.Query(ctx, `
		SELECT musicbrainz_recording_id
		FROM acquisition_target_rejected_recordings
		WHERE acquisition_target_id = $1
		ORDER BY decided_at
	`, id)
	if err != nil {
		return nil, fmt.Errorf("read rejected resolutions for want %s: %w", id, err)
	}
	defer rows.Close()

	excluded := make([]uuid.UUID, 0, 2)
	for rows.Next() {
		var recordingID uuid.UUID
		if err := rows.Scan(&recordingID); err != nil {
			return nil, err
		}
		excluded = append(excluded, recordingID)
	}
	return excluded, rows.Err()
}

// DueUnresolvedTargets returns the entries waiting to be resolved to a
// recording, oldest schedule first.
//
// They are asked for separately from the targets waiting for a copy, and
// separately bounded, because resolving one costs rate-limited provider requests
// while looking for an owned copy costs a query. One batch size for both would
// either park interactive searches behind a minute of throttling or check the
// library in pointlessly small slices.
func (q *Queries) DueUnresolvedTargets(
	ctx context.Context, now time.Time, limit int32,
) ([]AcquisitionTargetRow, error) {
	return q.dueTargets(ctx, "unresolved", now, limit)
}

// DueAcquisitionTargets returns the targets whose turn it is, oldest schedule
// first. It is bounded because every attempt costs work, and a want that waits
// one more sweep has waited hours already.
func (q *Queries) DueAcquisitionTargets(
	ctx context.Context, now time.Time, limit int32,
) ([]AcquisitionTargetRow, error) {
	return q.dueTargets(ctx, "pending", now, limit)
}

// StoppedTargetsHoldingAFiledCopy returns the wants that have stopped holding a
// copy the library made a file of, oldest first.
//
// A stopped want has no next attempt, so no sweep reaches it and nothing
// automatic ever looks at it again — which is the point of stopping. This is the
// one way back that is not a person: it is asked once per start, so a want
// stopped under a rule that has since changed is judged again under the rule as
// it is now. Nothing here decides anything; it is a list.
func (q *Queries) StoppedTargetsHoldingAFiledCopy(
	ctx context.Context, limit int32,
) ([]AcquisitionTargetRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT`+acquisitionTargetColumns+`
		FROM acquisition_targets
		WHERE status = 'pending'
		  AND next_attempt_at IS NULL
		  AND musicbrainz_recording_id IS NOT NULL
		  AND EXISTS (
		      SELECT 1 FROM acquisition_target_files files
		      WHERE files.acquisition_target_id = acquisition_targets.id
		        AND files.verdict = 'accepted'
		        AND files.library_file_id IS NOT NULL
		  )
		ORDER BY acquisition_targets.created_at
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list the wants stopped on a filed copy: %w", err)
	}
	defer rows.Close()

	targets := make([]AcquisitionTargetRow, 0)
	for rows.Next() {
		target, err := scanAcquisitionTarget(rows)
		if err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

func (q *Queries) dueTargets(
	ctx context.Context, status string, now time.Time, limit int32,
) ([]AcquisitionTargetRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT`+acquisitionTargetColumns+`
		FROM acquisition_targets
		WHERE status = $1 AND next_attempt_at <= $2
		ORDER BY acquisition_targets.next_attempt_at, acquisition_targets.created_at
		LIMIT $3
	`, status, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	targets := make([]AcquisitionTargetRow, 0)
	for rows.Next() {
		target, err := scanAcquisitionTarget(rows)
		if err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

// NextAcquisitionTargetDue reports when the earliest want waiting for a copy is
// due. An invalid timestamp means nothing is scheduled, which is what lets the
// sweeper stop rather than wake up to find nothing to do.
func (q *Queries) NextAcquisitionTargetDue(ctx context.Context) (pgtype.Timestamptz, error) {
	return q.nextTargetDue(ctx, "pending")
}

// NextUnresolvedTargetDue reports when the earliest entry waiting to be resolved
// is due.
//
// It is asked separately from the wants waiting for a copy, because the two are
// answered by different things and one of them is optional. Counting an
// unresolved entry as due in an installation with no resolver would wake the
// sweeper forever to do nothing about it.
func (q *Queries) NextUnresolvedTargetDue(ctx context.Context) (pgtype.Timestamptz, error) {
	return q.nextTargetDue(ctx, "unresolved")
}

func (q *Queries) nextTargetDue(ctx context.Context, status string) (pgtype.Timestamptz, error) {
	var due pgtype.Timestamptz
	err := q.db.QueryRow(ctx, `
		SELECT min(next_attempt_at)
		FROM acquisition_targets
		WHERE status = $1
	`, status).Scan(&due)
	return due, err
}

// OwnedFileForRecording reports a present library file already proven to be
// this recording, if the library holds one.
//
// Only decided identities count: a resolved identity, or a mapping onto a
// catalogue track carrying the recording. The identifier a file merely has in
// its own tags is deliberately not read here — an uncorroborated tag is exactly
// what identity resolution exists to grade, and answering from it would be a
// second, weaker answer to a question that already has one.
func (q *Queries) OwnedFileForRecording(
	ctx context.Context, recordingID uuid.UUID,
) (uuid.NullUUID, error) {
	var fileID uuid.NullUUID
	err := q.db.QueryRow(ctx, `
		SELECT library_files.id
		FROM library_files
		LEFT JOIN library_file_identities
		    ON library_file_identities.library_file_id = library_files.id
		LEFT JOIN track_mappings ON track_mappings.library_file_id = library_files.id
		LEFT JOIN tracks ON tracks.id = track_mappings.track_id
		WHERE library_files.missing_at IS NULL
		  AND (library_file_identities.musicbrainz_recording_id = $1
		       OR tracks.musicbrainz_recording_id = $1)
		ORDER BY library_files.created_at, library_files.id
		LIMIT 1
	`, recordingID).Scan(&fileID)
	// Nothing owned is an answer, not a failure: it is the ordinary case for a
	// want that has not been satisfied yet.
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.NullUUID{}, nil
	}
	return fileID, err
}

// AcquisitionTargetHasAnchor reports whether a want has audio recorded that a
// fetched copy can be compared against.
func (q *Queries) AcquisitionTargetHasAnchor(ctx context.Context, targetID uuid.UUID) (bool, error) {
	var found bool
	err := q.db.QueryRow(ctx, `
		SELECT anchor_fingerprint IS NOT NULL
		FROM acquisition_targets
		WHERE id = $1
	`, targetID).Scan(&found)
	return found, err
}

// SettleAcquiredTarget records that the want is satisfied by a file the library
// holds, and appends what this attempt came to in the same statement, so an
// outcome can never be recorded without the state it produced.
func (q *Queries) SettleAcquiredTarget(
	ctx context.Context, id, libraryFileID uuid.UUID, summary, detail string,
) error {
	tag, err := q.db.Exec(ctx, `
		WITH settled AS (
			UPDATE acquisition_targets
			SET status = 'acquired',
			    acquired_at = now(),
			    acquired_library_file_id = $2,
			    attempts = attempts + 1,
			    last_attempt_at = now(),
			    next_attempt_at = NULL,
			    last_error = NULL,
			    below_floor_streak = 0,
			    below_floor_since = NULL,
			    summary = $3,
			    -- Answered, so there is nothing left to wait for.
			    recheck_reason = NULL,
			    recheck_after = NULL,
			    updated_at = now()
			WHERE id = $1 AND status = 'pending'
			RETURNING id, attempts
		)
		INSERT INTO acquisition_target_attempts (
			acquisition_target_id, attempt, outcome, detail
		)
		SELECT id, attempts, 'acquired', $4 FROM settled
	`, id, libraryFileID, summary, detail)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// RequeueAcquisitionTarget records an attempt that did not find the recording
// and schedules the next one. There is no failure state to move to: Soulseek
// peers come and go, and a want nobody could satisfy today is still wanted
// tomorrow.
func (q *Queries) RequeueAcquisitionTarget(
	ctx context.Context, id uuid.UUID, outcome, summary, detail, lastError string,
	nextAttemptAt time.Time,
) error {
	tag, err := q.db.Exec(ctx, `
		WITH requeued AS (
			UPDATE acquisition_targets
			SET attempts = attempts + 1,
			    last_attempt_at = now(),
			    next_attempt_at = $2,
			    summary = $3,
			    last_error = nullif($6, ''),
			    below_floor_streak = 0,
			    below_floor_since = NULL,
			    updated_at = now()
			WHERE id = $1 AND status = 'pending'
			RETURNING id, attempts
		)
		INSERT INTO acquisition_target_attempts (
			acquisition_target_id, attempt, outcome, detail
		)
		SELECT id, attempts, $5, $4 FROM requeued
	`, id, nextAttemptAt, summary, detail, outcome, lastError)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// RequeueBelowFloor records a below-floor attempt and parks the want on its
// third consecutive answer. A parked want keeps its first parked timestamp and
// gets the same weekly rung when the next search finds more copies below it.
func (q *Queries) RequeueBelowFloor(
	ctx context.Context, id uuid.UUID, summary, parkedSummary, detail string,
	nextAttemptAt, parkedUntil time.Time,
) error {
	tag, err := q.db.Exec(ctx, `
		WITH requeued AS (
			UPDATE acquisition_targets
			SET attempts = attempts + 1,
			    last_attempt_at = now(),
			    below_floor_streak = below_floor_streak + 1,
			    below_floor_since = CASE
			        WHEN below_floor_streak + 1 >= 3
			        THEN coalesce(below_floor_since, now()::timestamptz)
			        ELSE NULL
			    END,
			    next_attempt_at = CASE
			        WHEN below_floor_streak + 1 >= 3 THEN $3::timestamptz
			        ELSE $2::timestamptz
			    END,
			    summary = CASE
			        WHEN below_floor_streak + 1 >= 3 THEN $4
			        ELSE $5
			    END,
			    last_error = NULL,
			    updated_at = now()
			WHERE id = $1 AND status = 'pending'
			RETURNING id, attempts
		)
		INSERT INTO acquisition_target_attempts (
			acquisition_target_id, attempt, outcome, detail
		)
		SELECT id, attempts, 'none', $6 FROM requeued
	`, id, nextAttemptAt, parkedUntil, parkedSummary, summary, detail)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// RequeueParkedAcquisitionTarget keeps a parked want on its weekly rung when a
// later search finds no copy at all. It records the attempt without losing the
// fact that the want is parked.
func (q *Queries) RequeueParkedAcquisitionTarget(
	ctx context.Context, id uuid.UUID, outcome, summary, detail, lastError string,
	nextAttemptAt time.Time,
) error {
	tag, err := q.db.Exec(ctx, `
		WITH requeued AS (
			UPDATE acquisition_targets
			SET attempts = attempts + 1,
			    last_attempt_at = now(),
			    next_attempt_at = $2,
			    summary = $3,
			    last_error = nullif($6, ''),
			    updated_at = now()
			WHERE id = $1 AND status = 'pending' AND below_floor_since IS NOT NULL
			RETURNING id, attempts
		)
		INSERT INTO acquisition_target_attempts (
			acquisition_target_id, attempt, outcome, detail
		)
		SELECT id, attempts, $5, $4 FROM requeued
	`, id, nextAttemptAt, summary, detail, outcome, lastError)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// AcquisitionTargetCandidateRow is one recording an entry might name, together
// with what stood for and against it when the question was asked.
//
// The JSON names are how a whole set of candidates reaches Postgres as one
// parameter, so that the question and the evidence behind it are stored in the
// same statement. They are read back by jsonb_to_recordset and are not an API
// shape.
type AcquisitionTargetCandidateRow struct {
	MusicBrainzRecordingID    uuid.UUID  `json:"recording_id"`
	MusicBrainzReleaseGroupID *uuid.UUID `json:"release_group_id"`
	ArtistName                string     `json:"artist_name"`
	ReleaseTitle              *string    `json:"release_title"`
	TrackTitle                string     `json:"track_title"`
	DurationMS                *int32     `json:"duration_ms"`
	ISRC                      *string    `json:"isrc"`
	Rank                      int32      `json:"rank"`
	Agrees                    []string   `json:"agrees"`
	Differs                   []string   `json:"differs"`
	Summary                   string     `json:"summary"`
}

// AcquisitionTargetCandidates reads back the recordings one entry could name, in
// the order the attempt ranked them, so the question can be shown together with
// the evidence behind it.
func (q *Queries) AcquisitionTargetCandidates(
	ctx context.Context, targetID uuid.UUID,
) ([]AcquisitionTargetCandidateRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT musicbrainz_recording_id, musicbrainz_release_group_id, artist_name,
		       release_title, track_title, duration_ms, isrc, rank, agrees, differs,
		       summary
		FROM acquisition_target_candidates
		WHERE acquisition_target_id = $1
		ORDER BY rank
	`, targetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	candidates := make([]AcquisitionTargetCandidateRow, 0)
	for rows.Next() {
		var candidate AcquisitionTargetCandidateRow
		if err := rows.Scan(
			&candidate.MusicBrainzRecordingID, &candidate.MusicBrainzReleaseGroupID,
			&candidate.ArtistName, &candidate.ReleaseTitle, &candidate.TrackTitle,
			&candidate.DurationMS, &candidate.ISRC, &candidate.Rank,
			&candidate.Agrees, &candidate.Differs, &candidate.Summary,
		); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

// candidatesFor reads the offered answers of several wants at once, keyed by the
// want they belong to. It is the review queue's read: a page of questions costs
// one query for the candidates rather than one per question.
func (q *Queries) candidatesFor(
	ctx context.Context, targetIDs []uuid.UUID,
) (map[uuid.UUID][]AcquisitionTargetCandidateRow, error) {
	offered := make(map[uuid.UUID][]AcquisitionTargetCandidateRow, len(targetIDs))
	if len(targetIDs) == 0 {
		return offered, nil
	}
	rows, err := q.db.Query(ctx, `
		SELECT acquisition_target_id, musicbrainz_recording_id,
		       musicbrainz_release_group_id, artist_name, release_title, track_title,
		       duration_ms, isrc, rank, agrees, differs, summary
		FROM acquisition_target_candidates
		WHERE acquisition_target_id = ANY($1)
		ORDER BY acquisition_target_id, rank
	`, targetIDs)
	if err != nil {
		return nil, fmt.Errorf("read the answers offered for a page of wants: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			targetID  uuid.UUID
			candidate AcquisitionTargetCandidateRow
		)
		if err := rows.Scan(
			&targetID,
			&candidate.MusicBrainzRecordingID, &candidate.MusicBrainzReleaseGroupID,
			&candidate.ArtistName, &candidate.ReleaseTitle, &candidate.TrackTitle,
			&candidate.DurationMS, &candidate.ISRC, &candidate.Rank,
			&candidate.Agrees, &candidate.Differs, &candidate.Summary,
		); err != nil {
			return nil, err
		}
		offered[targetID] = append(offered[targetID], candidate)
	}
	return offered, rows.Err()
}

type ResolveAcquisitionTargetParams struct {
	ID                        uuid.UUID
	MusicBrainzRecordingID    uuid.UUID
	MusicBrainzReleaseGroupID uuid.NullUUID
	ResolutionMethod          string
	Summary                   string
	// The sentence a merged target explains itself with, chosen by whether the
	// target that already wants this recording is one somebody stopped pursuing.
	// It is chosen here rather than by the caller, because only this statement
	// knows which target survived.
	SupersededSummary   string
	SupersededNotWanted string
	Detail              string
	NextAttemptAt       time.Time
}

// ResolveAcquisitionTarget writes the recording an entry was resolved to and
// makes the want actionable.
//
// The attempt counter restarts, because it measures how long a want has gone
// unsatisfied by peers and no peer has been asked yet — resolution retries must
// not arrive at the acquisition ladder already halfway up it. The append-only
// attempt log keeps the attempt that resolved it, numbered as it stood, so the
// restart is visible rather than lost.
//
// Two entries can resolve to one recording, and one recording is one want. When
// that happens this target becomes the loser: it keeps its own entry and origin,
// records the recording it resolved to, and points at the survivor, so whatever
// asked for it can follow the trail instead of finding a row that silently
// stopped moving. If the survivor is a target somebody stopped pursuing, the
// answer is that this recording is not wanted — that is the decision sticking,
// which is the whole point of holding one live target per recording.
//
// pgx.ErrNoRows means the target stopped being unresolved while this was being
// worked out, which means somebody decided something about it. Their decision
// stands.
func (q *Queries) ResolveAcquisitionTarget(
	ctx context.Context, params ResolveAcquisitionTargetParams,
) (AcquisitionTargetRow, error) {
	return q.resolveTarget(ctx, params, "unresolved")
}

// resolveTarget writes a resolution onto a target that is still in the state the
// caller read it in.
//
// from is that state, and it is the whole difference between the two ways an
// entry gets its recording: the sweeper answers one nobody has answered yet
// ('unresolved'), and a person choosing between the candidates answers one that
// was handed to them ('awaiting_review'). Both write the same row, so neither can
// drift from the other, and each refuses to overwrite the state the other owns.
func (q *Queries) resolveTarget(
	ctx context.Context, params ResolveAcquisitionTargetParams, from string,
) (AcquisitionTargetRow, error) {
	target, err := scanAcquisitionTarget(q.db.QueryRow(ctx, `
		WITH before AS (
			SELECT id, attempts
			FROM acquisition_targets
			WHERE id = $1 AND status = $8
		),
		resolved AS (
			UPDATE acquisition_targets
			SET musicbrainz_recording_id = $2,
			    musicbrainz_release_group_id = $3,
			    resolution_method = $4,
			    resolved_at = now(),
			    status = 'pending',
			    review_reason = NULL,
			    summary = $5,
			    last_error = NULL,
			    attempts = 0,
			    last_attempt_at = now(),
			    next_attempt_at = $6,
			    updated_at = now()
			WHERE id = (SELECT id FROM before) AND status = $8
			RETURNING`+acquisitionTargetColumns+`
		),
		logged AS (
			INSERT INTO acquisition_target_attempts (
				acquisition_target_id, attempt, outcome, detail
			)
			SELECT resolved.id, before.attempts + 1, 'resolved', $7
			FROM resolved JOIN before ON before.id = resolved.id
		),
		cleared AS (
			DELETE FROM acquisition_target_candidates
			WHERE acquisition_target_id = (SELECT id FROM resolved)
		)
		SELECT`+acquisitionTargetColumns+`FROM resolved AS acquisition_targets
	`,
		params.ID, params.MusicBrainzRecordingID, params.MusicBrainzReleaseGroupID,
		params.ResolutionMethod, params.Summary, params.NextAttemptAt, params.Detail,
		from,
	))
	if !isUniqueViolation(err) {
		return target, err
	}
	return q.supersedeAcquisitionTarget(ctx, params, from)
}

// supersedeAcquisitionTarget records that this entry turned out to want a
// recording another target already wants.
func (q *Queries) supersedeAcquisitionTarget(
	ctx context.Context, params ResolveAcquisitionTargetParams, from string,
) (AcquisitionTargetRow, error) {
	return scanAcquisitionTarget(q.db.QueryRow(ctx, `
		WITH before AS (
			SELECT id, attempts
			FROM acquisition_targets
			WHERE id = $1 AND status = $8
		),
		survivor AS (
			SELECT id, status
			FROM acquisition_targets
			WHERE musicbrainz_recording_id = $2
			  AND status <> 'superseded'
			  AND id <> $1
			LIMIT 1
		),
		merged AS (
			UPDATE acquisition_targets
			SET musicbrainz_recording_id = $2,
			    musicbrainz_release_group_id = $3,
			    resolution_method = $4,
			    resolved_at = now(),
			    status = 'superseded',
			    superseded_by_id = (SELECT id FROM survivor),
			    review_reason = NULL,
			    summary = CASE
			        WHEN (SELECT status FROM survivor) = 'not_wanted' THEN $6
			        ELSE $5
			    END,
			    last_error = NULL,
			    last_attempt_at = now(),
			    next_attempt_at = NULL,
			    updated_at = now()
			WHERE id = (SELECT id FROM before)
			  AND status = $8
			  AND EXISTS (SELECT 1 FROM survivor)
			RETURNING`+acquisitionTargetColumns+`
		),
		logged AS (
			INSERT INTO acquisition_target_attempts (
				acquisition_target_id, attempt, outcome, detail
			)
			SELECT merged.id, before.attempts + 1, 'resolved', $7
			FROM merged JOIN before ON before.id = merged.id
		),
		cleared AS (
			DELETE FROM acquisition_target_candidates
			WHERE acquisition_target_id = (SELECT id FROM merged)
		)
		SELECT`+acquisitionTargetColumns+`FROM merged AS acquisition_targets
	`,
		params.ID, params.MusicBrainzRecordingID, params.MusicBrainzReleaseGroupID,
		params.ResolutionMethod, params.SupersededSummary, params.SupersededNotWanted,
		params.Detail, from,
	))
}

// ErrNotOfferedForReview reports a chosen recording that was not one of the
// answers this want offered.
//
// It is refused rather than accepted quietly, because the offer is what makes
// the choice a decision about a question somebody was actually asked. A
// recording from anywhere else is a want for something nobody weighed the
// evidence for, and that is a new want rather than an answer to this one.
var ErrNotOfferedForReview = errors.New(
	"that recording is not one of the answers offered for this wishlist entry")

// ChooseAcquisitionTargetResolution records the recording a person picked from
// the ones this entry could have named.
//
// Nothing here decides anything: the candidates were stored precisely because
// the evidence did not choose between them, and this writes down which one a
// person said it is. That makes it a manual resolution — 'manual' is the method,
// and nothing automatic re-opens a target that has a recording — so the answer
// is permanent in the same way a manual mapping is.
//
// The release group is taken from the candidate rather than from the caller: the
// choice is of an answer that was offered, and the rest of that answer comes with
// it instead of being supplied again by whoever clicked.
//
// pgx.ErrNoRows means the want stopped waiting for an answer while this one was
// being given.
func (q *Queries) ChooseAcquisitionTargetResolution(
	ctx context.Context, params ResolveAcquisitionTargetParams,
) (AcquisitionTargetRow, error) {
	var releaseGroup uuid.NullUUID
	err := q.db.QueryRow(ctx, `
		SELECT candidates.musicbrainz_release_group_id
		FROM acquisition_target_candidates candidates
		JOIN acquisition_targets ON acquisition_targets.id = candidates.acquisition_target_id
		WHERE candidates.acquisition_target_id = $1
		  AND candidates.musicbrainz_recording_id = $2
		  AND acquisition_targets.status = 'awaiting_review'
		  AND acquisition_targets.review_reason = 'resolution'
	`, params.ID, params.MusicBrainzRecordingID).Scan(&releaseGroup)
	if errors.Is(err, pgx.ErrNoRows) {
		return AcquisitionTargetRow{}, ErrNotOfferedForReview
	}
	if err != nil {
		return AcquisitionTargetRow{}, fmt.Errorf(
			"read the answer chosen for acquisition target %s: %w", params.ID, err)
	}
	params.MusicBrainzReleaseGroupID = releaseGroup
	params.ResolutionMethod = "manual"
	return q.resolveTarget(ctx, params, "awaiting_review")
}

type ReviewAcquisitionTargetParams struct {
	ID         uuid.UUID
	Summary    string
	Detail     string
	Candidates []AcquisitionTargetCandidateRow
}

// SendAcquisitionTargetToReview records that the entry has several plausible
// answers, or an answer that contradicts itself, and that a person has to choose.
//
// The candidates go with it in the same statement: a question stored without the
// evidence behind it is a list of titles and a request to guess. They are
// replaced rather than added to, because they describe what the provider said on
// this attempt, and stale ones would be a second opinion nobody asked for.
//
// pgx.ErrNoRows means somebody decided something about the target meanwhile.
func (q *Queries) SendAcquisitionTargetToReview(
	ctx context.Context, params ReviewAcquisitionTargetParams,
) (AcquisitionTargetRow, error) {
	offered, err := json.Marshal(params.Candidates)
	if err != nil {
		return AcquisitionTargetRow{}, fmt.Errorf("encode acquisition candidates: %w", err)
	}
	return scanAcquisitionTarget(q.db.QueryRow(ctx, `
		WITH before AS (
			SELECT id, attempts
			FROM acquisition_targets
			WHERE id = $1 AND status = 'unresolved'
		),
		reviewed AS (
			UPDATE acquisition_targets
			SET status = 'awaiting_review',
			    review_reason = 'resolution',
			    summary = $2,
			    last_error = NULL,
			    attempts = attempts + 1,
			    last_attempt_at = now(),
			    next_attempt_at = NULL,
			    updated_at = now()
			WHERE id = (SELECT id FROM before) AND status = 'unresolved'
			RETURNING`+acquisitionTargetColumns+`
		),
		logged AS (
			INSERT INTO acquisition_target_attempts (
				acquisition_target_id, attempt, outcome, detail
			)
			SELECT reviewed.id, reviewed.attempts, 'inconclusive', $3
			FROM reviewed
		),
		offered AS (
			INSERT INTO acquisition_target_candidates (
				acquisition_target_id, musicbrainz_recording_id,
				musicbrainz_release_group_id, artist_name, release_title, track_title,
				duration_ms, isrc, rank, agrees, differs, summary
			)
			SELECT reviewed.id, candidate.recording_id, candidate.release_group_id,
			       candidate.artist_name, candidate.release_title, candidate.track_title,
			       candidate.duration_ms, candidate.isrc, candidate.rank,
			       candidate.agrees, candidate.differs, candidate.summary
			FROM reviewed, jsonb_to_recordset($4::jsonb) AS candidate(
				recording_id UUID, release_group_id UUID, artist_name TEXT,
				release_title TEXT, track_title TEXT, duration_ms INTEGER, isrc TEXT,
				rank INTEGER, agrees TEXT[], differs TEXT[], summary TEXT
			)
			ON CONFLICT (acquisition_target_id, musicbrainz_recording_id) DO UPDATE
			SET musicbrainz_release_group_id = EXCLUDED.musicbrainz_release_group_id,
			    artist_name = EXCLUDED.artist_name,
			    release_title = EXCLUDED.release_title,
			    track_title = EXCLUDED.track_title,
			    duration_ms = EXCLUDED.duration_ms,
			    isrc = EXCLUDED.isrc,
			    rank = EXCLUDED.rank,
			    agrees = EXCLUDED.agrees,
			    differs = EXCLUDED.differs,
			    summary = EXCLUDED.summary
			RETURNING musicbrainz_recording_id
		),
		withdrawn AS (
			-- The candidates this attempt no longer offers. Only rows that existed
			-- before this statement are visible to it, so the ones just written
			-- cannot be swept away by their own arrival.
			DELETE FROM acquisition_target_candidates
			WHERE acquisition_target_id = (SELECT id FROM reviewed)
			  AND musicbrainz_recording_id NOT IN (SELECT musicbrainz_recording_id FROM offered)
		)
		SELECT`+acquisitionTargetColumns+`FROM reviewed AS acquisition_targets
	`, params.ID, params.Summary, params.Detail, offered))
}

// RequeueUnresolvedTarget records a resolution attempt that produced no answer
// and schedules the next one.
//
// The target stays unresolved rather than going to review, and that is the
// difference between a question a person can answer and one nobody can yet:
// MusicBrainz gains recordings, so an entry nothing matches today may match in
// three days, and asking a user to choose between zero candidates is not a
// question. lastError is only ever about Schall failing to ask — never about the
// music — so it is written on its own and cleared by anything that succeeds.
func (q *Queries) RequeueUnresolvedTarget(
	ctx context.Context, id uuid.UUID, outcome, summary, detail, lastError string,
	nextAttemptAt time.Time,
) error {
	tag, err := q.db.Exec(ctx, `
		WITH requeued AS (
			UPDATE acquisition_targets
			SET attempts = attempts + 1,
			    last_attempt_at = now(),
			    next_attempt_at = $2,
			    summary = $3,
			    last_error = nullif($4, ''),
			    updated_at = now()
			WHERE id = $1 AND status = 'unresolved'
			RETURNING id, attempts
		)
		INSERT INTO acquisition_target_attempts (
			acquisition_target_id, attempt, outcome, detail
		)
		SELECT id, attempts, $5, $6 FROM requeued
	`, id, nextAttemptAt, summary, lastError, outcome, detail)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// DeferUnresolvedTarget moves when an entry is next asked about and records why,
// without counting an attempt.
//
// It is RequeueUnresolvedTarget's opposite number for the case where nothing was
// attempted: MusicBrainz refused to answer at all, because it is being asked too
// often or is shedding load, which says nothing about whether it knows the
// recording. Counting that as an attempt would push the entry down a ladder that
// climbs to a day, so a playlist big enough to be throttled would punish itself
// for being imported and the entries refused first would be asked about least
// often afterwards. That is DeferAcquisitionTarget's reasoning exactly, one step
// earlier in a want's life.
func (q *Queries) DeferUnresolvedTarget(
	ctx context.Context, id uuid.UUID, summary, reason string, nextAttemptAt time.Time,
) error {
	_, err := q.db.Exec(ctx, `
		UPDATE acquisition_targets
		SET summary = $2, last_error = nullif($3, ''), next_attempt_at = $4,
		    updated_at = now()
		WHERE id = $1 AND status = 'unresolved'
	`, id, summary, reason, nextAttemptAt)
	if err != nil {
		return fmt.Errorf("defer unresolved want %s: %w", id, err)
	}
	return nil
}

// isUniqueViolation reports a write refused because something is already there.
// 23505 is unique_violation; the code is spelled out rather than pulled from a
// constants package Schall does not otherwise depend on.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// QueueAcquisitionSweep asks for the sweeper to run no later than the given
// time, mirroring how the transfer poller is queued.
//
// It moves an existing sweep earlier rather than leaving it where it is. The
// partial unique index allows one live sweeper, and a target created now must
// not have to wait out a sweep that was scheduled for three days' time.
//
// A sweep already running cannot be moved — there is nothing to move, it is
// live — so the call leaves a mark on that row's payload instead. Nothing here
// reads the mark; ProcessAcquisitionSweep does, in the same statement that
// retires the row, so a wake landing mid-pass is picked up the moment the pass
// ends rather than waiting for whatever else eventually asks again.
func (q *Queries) QueueAcquisitionSweep(ctx context.Context, runAfter time.Time) error {
	_, err := q.db.Exec(ctx, queueAcquisitionSweep, runAfter)
	return err
}

// The one statement that asks for a sweep. It is a constant because two callers
// say it — this one, and the decision about a file that gives a stopped want its
// next look back — and two spellings of "ask for a sweep" is one too many for a
// statement whose whole job is that there is only ever one sweep.
const queueAcquisitionSweep = `
	INSERT INTO jobs (kind, payload, max_attempts, run_after)
	VALUES ('sweep_acquisition_targets', '{}'::jsonb, 1, $1)
	ON CONFLICT (kind)
	    WHERE kind = 'sweep_acquisition_targets' AND status IN ('queued', 'running')
	DO UPDATE
	SET run_after = CASE WHEN jobs.status = 'queued'
	                      THEN least(jobs.run_after, EXCLUDED.run_after)
	                      ELSE jobs.run_after END,
	    payload = CASE WHEN jobs.status = 'running'
	                    THEN '{"woken_while_running": true}'::jsonb
	                    ELSE jobs.payload END,
	    updated_at = now()
	WHERE jobs.status IN ('queued', 'running')
`

// QueueCoverArtSweep asks for the picture cache to be filled, moving an existing
// request earlier rather than adding a second one. One sweeper is what the index
// allows, and the whole point of the sweep is that the rate is deliberate.
//
// Say it this way where something has just happened that a picture is wanted
// for — a release was ingested a minute after the last pass went by. A caller
// that only wants a pass to exist wants EnsureCoverArtSweepQueued, because this
// one spends the wait the previous pass chose.
func (q *Queries) QueueCoverArtSweep(ctx context.Context, runAfter time.Time) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ('sweep_cover_art', '{}'::jsonb, 3, $1)
		ON CONFLICT (kind)
		    WHERE kind = 'sweep_cover_art' AND status IN ('queued', 'running')
		DO UPDATE
		SET run_after = least(jobs.run_after, EXCLUDED.run_after),
		    updated_at = now()
		WHERE jobs.status = 'queued'
	`, runAfter)
	return err
}

// QueueLoudnessSweep asks for another batch of the library to be measured for
// loudness, and leaves an existing request exactly as it is.
//
// One measuring pass is what migration 00059's index allows, and one is what
// the machine can afford: measuring means decoding whole files, so a second
// pass beside the first is two decodes competing for the same processor. Both
// callers say it this way — startup, which means "there must be a pass on the
// books", and the pass itself, which schedules its own successor. Neither has a
// reason to move a wait somebody else chose.
func (q *Queries) QueueLoudnessSweep(ctx context.Context, runAfter time.Time) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ('sweep_loudness', '{}'::jsonb, 3, $1)
		ON CONFLICT (kind)
		    WHERE kind = 'sweep_loudness' AND status IN ('queued', 'running')
		DO NOTHING
	`, runAfter)
	return err
}

// EnsureCoverArtSweepQueued adds a picture pass only when none is queued and
// none is running. An existing pass keeps the time it holds.
//
// Startup is the only caller. It means "there must be a pass on the books", not
// "look for pictures now". The sweep asks an archive on its own initiative and
// schedules itself six hours out, so a restart that moved that pass to now would
// make the rate a question of how often Schall is deployed (issue #334).
//
// An installation that was switched off still catches up. A pass whose time
// passed while the process was down is already due, and the worker takes it as
// soon as it starts; leaving it alone only keeps the passes that are still in
// the future waiting.
func (q *Queries) EnsureCoverArtSweepQueued(ctx context.Context, runAfter time.Time) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ('sweep_cover_art', '{}'::jsonb, 3, $1)
		ON CONFLICT (kind)
		    WHERE kind = 'sweep_cover_art' AND status IN ('queued', 'running')
		DO NOTHING
	`, runAfter)
	return err
}

// QueueFollowFeedSweep asks for the followed artists to be looked at again,
// moving an existing request earlier rather than adding a second one. One
// sweeper is what the index allows: this job schedules its own replacement, so
// a second copy would double the rate every pass rather than run twice.
//
// The sweep's own successor is queued this way, which is what lets a pass that
// filled its batch come straight back. A caller that only wants a pass to exist
// wants EnsureFollowFeedSweepQueued.
func (q *Queries) QueueFollowFeedSweep(ctx context.Context, runAfter time.Time) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ('sweep_follow_feed', '{}'::jsonb, 3, $1)
		ON CONFLICT (kind)
		    WHERE kind = 'sweep_follow_feed' AND status IN ('queued', 'running')
		DO UPDATE
		SET run_after = least(jobs.run_after, EXCLUDED.run_after),
		    updated_at = now()
		WHERE jobs.status = 'queued'
	`, runAfter)
	return err
}

// EnsureFollowFeedSweepQueued adds a follow-feed pass only when none is queued
// and none is running. An existing pass keeps the time it holds.
//
// Startup is the only caller, and it wants the same thing here as it wants of
// the picture sweep: a pass on the books, at the rate the feed chose rather than
// at the rate the pod is replaced.
func (q *Queries) EnsureFollowFeedSweepQueued(ctx context.Context, runAfter time.Time) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ('sweep_follow_feed', '{}'::jsonb, 3, $1)
		ON CONFLICT (kind)
		    WHERE kind = 'sweep_follow_feed' AND status IN ('queued', 'running')
		DO NOTHING
	`, runAfter)
	return err
}

// QueueRecommendationSweep asks the configured recommendation source for a
// fresh snapshot as soon as the given time, moving a pass that is already
// waiting earlier rather than adding a second one. The sweep schedules itself,
// so allowing two live rows would make every pass double the requests sent to
// the external services.
//
// Say it this way only where the point is to sweep now — somebody has just
// connected their ListenBrainz account, and the pass already on the books may
// be a day away. A caller that only wants a pass to exist wants
// EnsureRecommendationSweepQueued, because this one throws away the wait the
// previous pass chose.
func (q *Queries) QueueRecommendationSweep(ctx context.Context, runAfter time.Time) error {
	return q.QueueRecommendationSweepContinuation(ctx, runAfter, json.RawMessage(`{}`))
}

// EnsureRecommendationSweepQueued adds a recommendation pass only when none is
// queued and none is running. An existing pass keeps the time it holds.
//
// Startup is the only caller. It means "there must be a pass on the books", not
// "sweep now". A pass that met a service in difficulty queues its successor
// thirty minutes out, so that Schall does not press a service that is
// struggling; a pod replaced inside that half hour must not cancel the wait.
func (q *Queries) EnsureRecommendationSweepQueued(ctx context.Context, runAfter time.Time) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ('sweep_recommendations', '{}'::jsonb, 3, $1)
		ON CONFLICT (kind)
		    WHERE kind = 'sweep_recommendations' AND status IN ('queued', 'running')
		DO NOTHING
	`, runAfter)
	return err
}

// QueueRecommendationSweepContinuation carries the bounded MusicBrainz
// expansion position into the successor job. A normal scheduled sweep uses an
// empty payload and starts from the current provider answer's first candidate.
//
// The insert ends one of three ways. With no pass queued and none running it
// writes the row. With a pass queued it keeps the earlier of the two times.
// With a pass running it writes nothing and reports no error: the row the
// unique index found is `running`, so the `WHERE jobs.status = 'queued'` on the
// update matches nothing. That is the wanted answer — the work being asked for
// is already under way — but note that it is silence, not a refusal the caller
// can read.
func (q *Queries) QueueRecommendationSweepContinuation(
	ctx context.Context, runAfter time.Time, payload json.RawMessage,
) error {
	if len(payload) == 0 || !json.Valid(payload) {
		return errors.New("recommendation sweep payload must be valid JSON")
	}
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ('sweep_recommendations', $2::jsonb, 3, $1)
		ON CONFLICT (kind)
		    WHERE kind = 'sweep_recommendations' AND status IN ('queued', 'running')
		DO UPDATE
		SET run_after = least(jobs.run_after, EXCLUDED.run_after),
		    updated_at = now()
		WHERE jobs.status = 'queued'
	`, runAfter, payload)
	return err
}

// QueueLyricsSweep asks for the next page of the library to be worded, carrying
// the position the last pass reached.
//
// One sweeper, enforced by jobs_lyrics_sweep_idx, because the pace is the point:
// LRCLIB is a free public service and two passes running would be two
// installations' worth of requests arriving there at once. A pass already queued
// keeps the earlier of the two times and takes the new position, because the
// position is where the walk has actually got to.
//
// A pass that is running writes nothing and reports no error: the row the index
// found is 'running', so the WHERE on the update matches nothing. That is the
// wanted answer — the work is already under way — and it is silence rather than
// a refusal the caller can read.
func (q *Queries) QueueLyricsSweep(
	ctx context.Context, runAfter time.Time, payload json.RawMessage,
) error {
	if len(payload) == 0 || !json.Valid(payload) {
		return errors.New("lyrics sweep payload must be valid JSON")
	}
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ('sweep_lyrics', $2::jsonb, 3, $1)
		ON CONFLICT (kind)
		    WHERE kind = 'sweep_lyrics' AND status IN ('queued', 'running')
		DO UPDATE
		SET run_after = least(jobs.run_after, EXCLUDED.run_after),
		    payload = EXCLUDED.payload,
		    updated_at = now()
		WHERE jobs.status = 'queued'
	`, runAfter, payload)
	return err
}

// EnsureLyricsSweepQueued adds a lyrics pass only when none is queued and none
// is running. An existing pass keeps the time and the position it holds.
//
// Startup is the only caller. It means "there must be a pass on the books", not
// "word the library now": a pass that met LRCLIB in difficulty waits an hour
// before asking again, and a pod replaced inside that hour must not cancel the
// wait.
func (q *Queries) EnsureLyricsSweepQueued(ctx context.Context, runAfter time.Time) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ('sweep_lyrics', '{}'::jsonb, 3, $1)
		ON CONFLICT (kind)
		    WHERE kind = 'sweep_lyrics' AND status IN ('queued', 'running')
		DO NOTHING
	`, runAfter)
	return err
}

// WantsSharingAReleaseWith reads the other wants whose music would sit in the
// same folder as one that has just been found: still being pursued, naming the
// same release, and with nothing already on its way.
//
// It exists because a peer shares a folder rather than a track. A search that
// found the folder for one want found it for its siblings too, and asking each
// of them to go and find it again on its own turn is asking a network that
// answers the same question differently every time.
//
// A want with a copy already in flight — or imported and waiting for the scan
// to settle it — is left alone: one open request per want is what stops two
// copies of one recording arriving at once, and this is not the place to make
// an exception to it. The want's own hunt already stands back in that window
// (completeAcquired runs first); this keeps the harvest from reaching around it.
func (q *Queries) WantsSharingAReleaseWith(
	ctx context.Context, targetID uuid.UUID,
) ([]AcquisitionTargetRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT`+acquisitionTargetColumns+`
		FROM acquisition_targets
		WHERE acquisition_targets.id <> $1
		  AND acquisition_targets.status = 'pending'
		  AND acquisition_targets.musicbrainz_recording_id IS NOT NULL
		  AND acquisition_targets.musicbrainz_release_group_id IS NOT NULL
		  AND acquisition_targets.musicbrainz_release_group_id = (
		      SELECT musicbrainz_release_group_id
		      FROM acquisition_targets WHERE id = $1
		  )
		  AND NOT EXISTS (
		      -- The same guard the review queue applies: a want whose copy is in
		      -- flight, being validated, or imported and waiting on the scan is a
		      -- want already answered, and handing it a second copy out of a
		      -- folder is the duplicate import that guard exists to prevent. A
		      -- failed import is not an answer, so an errored request leaves the
		      -- want eligible again.
		      SELECT 1 FROM download_requests
		      WHERE download_requests.acquisition_target_id = acquisition_targets.id
		        AND (download_requests.status IN ('requested', 'started')
		             OR (download_requests.status = 'completed'
		                 AND download_requests.import_status
		                     IN ('pending', 'validating', 'imported')
		                 AND (download_requests.import_status = 'imported'
		                      OR download_requests.import_error IS NULL)))
		  )
		  -- A sibling already holding a copy nobody has answered, with no anchor
		  -- to answer it with, is left out for the same reason its own turn would
		  -- stop it (StopWhileACopyWaits). An open folder must not reach around
		  -- the rule; the sibling's own turn writes the sentence.
		  AND NOT (
		      acquisition_targets.anchor_fingerprint IS NULL
		      AND`+wantHoldsAnUnansweredCopy+`
		  )
		ORDER BY acquisition_targets.entry_title, acquisition_targets.id
	`, targetID)
	if err != nil {
		return nil, fmt.Errorf("read the wants sharing a release with %s: %w", targetID, err)
	}
	defer rows.Close()

	var result []AcquisitionTargetRow
	for rows.Next() {
		var target AcquisitionTargetRow
		if err := scanAcquisitionTargetInto(rows, &target); err != nil {
			return nil, err
		}
		result = append(result, target)
	}
	return result, rows.Err()
}

// AnchorDueRow is one want waiting for its audio anchor.
//
// An anchor is the fingerprint of the thirty-second preview a distributor
// publishes for the recording the want names, fetched by the want's own ISRC and
// kept on the want. It is what lets a copy a stranger sent be proven, or
// refused, for music MusicBrainz has never heard of. See
// ADR 0024.
//
// Only what the fetch needs is read: the code to ask by, the words to search by
// and to say which want this was in a log, the length that keys an upload, and
// how many times it has been tried.
type AnchorDueRow struct {
	ID     uuid.UUID
	ISRC   string
	Artist string
	Title  string
	// DurationMS is what the entry says the recording runs for. It is half of
	// what chooses an upload found by name — the other half is the title — and a
	// want without it cannot be anchored that way at all
	// (ADR 0029 §2).
	DurationMS int
	Attempts   int32
}

// DueAnchors returns the wants whose turn it is to have a preview fetched,
// oldest schedule first.
//
// It is bounded because every one of these is a request to a free public service
// that did not ask to be asked, and a want that waits one more pass has waited
// hours already.
func (q *Queries) DueAnchors(
	ctx context.Context, now time.Time, limit int32,
) ([]AnchorDueRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT id, coalesce(entry_isrc, ''), entry_artist, entry_title,
		       coalesce(entry_duration_ms, 0), anchor_attempts
		FROM acquisition_targets
		WHERE anchor_fingerprint IS NULL
		  AND anchor_next_attempt_at IS NOT NULL
		  AND anchor_next_attempt_at <= $1
		ORDER BY anchor_next_attempt_at, created_at
		LIMIT $2
	`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("read the wants due an anchor: %w", err)
	}
	defer rows.Close()

	due := make([]AnchorDueRow, 0)
	for rows.Next() {
		var row AnchorDueRow
		if err := rows.Scan(
			&row.ID, &row.ISRC, &row.Artist, &row.Title, &row.DurationMS, &row.Attempts,
		); err != nil {
			return nil, err
		}
		due = append(due, row)
	}
	return due, rows.Err()
}

// AnchorParams is one fetched preview, ready to be kept.
type AnchorParams struct {
	TargetID uuid.UUID
	// Fingerprint is the compressed form fpcalc emitted, which is the form every
	// other fingerprint in Schall is stored in.
	Fingerprint string
	Source      string
	// Reference is the distributor's own identifier for the track, or the video
	// id where the reference was found by name, so an anchor can be traced back
	// to the audio it was cut from.
	Reference string
	// Label and Views describe a reference found by name: the upload's own title
	// and channel, and how many views it had when it was chosen. A keyed
	// reference has neither — its identifier is the whole of what there is to
	// say about it — and they exist because an upload chosen by name can be the
	// wrong version, and the person who can see that is the person reading the
	// row (ADR 0029 §4).
	Label   string
	Views   int64
	Seconds int
}

// RecordAnchor writes a fetched preview onto the want and takes the want off the
// schedule. Nothing else about the want moves: an anchor is evidence added to a
// want, never an answer about it.
func (q *Queries) RecordAnchor(ctx context.Context, params AnchorParams) error {
	_, err := q.db.Exec(ctx, `
		UPDATE acquisition_targets
		SET anchor_fingerprint = $2,
		    anchor_source = $3,
		    anchor_reference = $4,
		    anchor_seconds = $5,
		    anchor_label = nullif($6, ''),
		    anchor_views = nullif($7, 0::bigint),
		    anchor_fetched_at = now(),
		    anchor_unavailable = NULL,
		    anchor_next_attempt_at = NULL,
		    updated_at = now()
		WHERE id = $1
	`, params.TargetID, params.Fingerprint, params.Source, params.Reference,
		params.Seconds, params.Label, params.Views)
	if err != nil {
		return fmt.Errorf("record the anchor for want %s: %w", params.TargetID, err)
	}
	return nil
}

// RecordAnchorAbsent writes down that there is no preview to be had, and stops
// asking.
//
// This is silence, not disagreement. A want with no anchor is judged by whatever
// else can speak about its copies, exactly as it is today, and the sentence kept
// here is what a person reads instead of finding an empty field
// (ADR 0002).
func (q *Queries) RecordAnchorAbsent(
	ctx context.Context, targetID uuid.UUID, reason string,
) error {
	_, err := q.db.Exec(ctx, `
		UPDATE acquisition_targets
		SET anchor_unavailable = $2,
		    anchor_attempts = anchor_attempts + 1,
		    anchor_next_attempt_at = NULL,
		    updated_at = now()
		WHERE id = $1
	`, targetID, reason)
	if err != nil {
		return fmt.Errorf("record that want %s has no anchor: %w", targetID, err)
	}
	return nil
}

// DeferAnchor puts a want back on the schedule after a fetch that could not run.
//
// It is deliberately not the same call as RecordAnchorAbsent. A service that
// refused to answer this minute has said nothing about whether a preview exists,
// and recording that as "no preview" would turn a busy afternoon into a
// permanent fact about the music.
func (q *Queries) DeferAnchor(
	ctx context.Context, targetID uuid.UUID, at time.Time, reason string,
) error {
	_, err := q.db.Exec(ctx, `
		UPDATE acquisition_targets
		SET anchor_unavailable = $3,
		    anchor_attempts = anchor_attempts + 1,
		    anchor_next_attempt_at = $2,
		    updated_at = now()
		WHERE id = $1
	`, targetID, at, reason)
	if err != nil {
		return fmt.Errorf("defer the anchor for want %s: %w", targetID, err)
	}
	return nil
}

// ScheduleAnchor puts a want on the anchor schedule. It is called when a want is
// created, and does nothing to a want that already has an anchor, has been
// found to have none, or is already waiting for one. A want without an ISRC is
// scheduled too: the distributor has nothing for it, and the upload search of
// ADR 0029 is what answers then.
func (q *Queries) ScheduleAnchor(
	ctx context.Context, targetID uuid.UUID, at time.Time,
) error {
	_, err := q.db.Exec(ctx, `
		UPDATE acquisition_targets
		SET anchor_next_attempt_at = $2, updated_at = now()
		WHERE id = $1
		  AND anchor_fingerprint IS NULL
		  AND anchor_unavailable IS NULL
		  AND anchor_next_attempt_at IS NULL
	`, targetID, at)
	if err != nil {
		return fmt.Errorf("schedule the anchor for want %s: %w", targetID, err)
	}
	return nil
}

// NextAnchorDue reports when the earliest want waiting for a preview is due. An
// invalid timestamp means nothing is scheduled, which is what lets the sweep
// stop rather than wake up to find nothing to do.
func (q *Queries) NextAnchorDue(ctx context.Context) (pgtype.Timestamptz, error) {
	var due pgtype.Timestamptz
	err := q.db.QueryRow(ctx, `
		SELECT min(anchor_next_attempt_at)
		FROM acquisition_targets
		WHERE anchor_fingerprint IS NULL
	`).Scan(&due)
	return due, err
}

// QueuePreviewAnchorSweep asks for a pass over the wants due an anchor. A pass
// already on the books keeps the earlier of the two times.
func (q *Queries) QueuePreviewAnchorSweep(ctx context.Context, runAfter time.Time) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ('sweep_preview_anchors', '{}'::jsonb, 3, $1)
		ON CONFLICT (kind)
		    WHERE kind = 'sweep_preview_anchors' AND status IN ('queued', 'running')
		DO UPDATE
		SET run_after = least(jobs.run_after, EXCLUDED.run_after),
		    updated_at = now()
		WHERE jobs.status = 'queued'
	`, runAfter)
	return err
}

// QueueCopyFingerprintSweep asks for a pass over the held copies whose audio has
// never been measured. A pass already on the books keeps the earlier of the two
// times.
func (q *Queries) QueueCopyFingerprintSweep(ctx context.Context, runAfter time.Time) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ('sweep_copy_fingerprints', '{}'::jsonb, 3, $1)
		ON CONFLICT (kind)
		    WHERE kind = 'sweep_copy_fingerprints' AND status IN ('queued', 'running')
		DO UPDATE
		SET run_after = least(jobs.run_after, EXCLUDED.run_after),
		    updated_at = now()
		WHERE jobs.status = 'queued'
	`, runAfter)
	return err
}

// EnsureCopyFingerprintSweepQueued adds a pass only when none is queued and none
// is running. Startup is the only caller, and it means "there must be a pass on
// the books" rather than "measure everything now": a pass that has just run and
// scheduled its own replacement must not be brought forward by a pod restart.
func (q *Queries) EnsureCopyFingerprintSweepQueued(ctx context.Context, runAfter time.Time) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ('sweep_copy_fingerprints', '{}'::jsonb, 3, $1)
		ON CONFLICT (kind)
		    WHERE kind = 'sweep_copy_fingerprints' AND status IN ('queued', 'running')
		DO NOTHING
	`, runAfter)
	return err
}

// EnsurePreviewAnchorSweepQueued adds a pass only when none is queued and none
// is running. Startup is the only caller, and it means "there must be a pass on
// the books" rather than "fetch every preview now": a pass that met Deezer
// rate-limiting waits before asking again, and a pod replaced inside that wait
// must not cancel it.
func (q *Queries) EnsurePreviewAnchorSweepQueued(ctx context.Context, runAfter time.Time) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ('sweep_preview_anchors', '{}'::jsonb, 3, $1)
		ON CONFLICT (kind)
		    WHERE kind = 'sweep_preview_anchors' AND status IN ('queued', 'running')
		DO NOTHING
	`, runAfter)
	return err
}
