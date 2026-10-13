// Package removals takes music back out of the collection when a person
// removes it.
//
// Removing does two things on two clocks. The want goes at once, so nothing
// looks for the song any more. A file Schall downloaded for it goes 30 days
// later, so a slip of the hand costs nothing: wanting the song again inside
// those days keeps the file. A file somebody put in the library themselves is
// never deleted from here.
package removals

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/events"
	"github.com/pxldi/schall/internal/library"
	"github.com/rs/zerolog"
)

// Grace is how long a downloaded file stays after its want was removed.
const Grace = 30 * 24 * time.Hour

// ErrNothingToRemove is a want that was merged into another one. The other one
// is the want to remove.
var ErrNothingToRemove = errors.New("this want was merged into another one")

// removedSummary is what a removed want says about itself.
const removedSummary = "Removed from wishlist."

// FileRemover deletes one library file once allowed, run in the deleting
// transaction, agrees. library.Remover is the one in use, so every deletion
// goes through the same path the duplicates screen and the weekly playlist use.
type FileRemover interface {
	RemoveIf(ctx context.Context, fileID uuid.UUID, allowed func(context.Context, pgx.Tx) error) (string, error)
}

// errNotScheduled and errStillNeeded are the two reasons the last check before
// a delete refuses it.
var (
	errNotScheduled = errors.New("the removal was cancelled before its file went")
	errStillNeeded  = errors.New("something still needs the file")
)

// Scheduled is one file waiting to be deleted.
type Scheduled struct {
	FileID    uuid.UUID `json:"fileId"`
	Path      string    `json:"path"`
	RemovesAt time.Time `json:"removesAt"`
}

// Outcome is what one remove did.
type Outcome struct {
	TargetID uuid.UUID `json:"targetId"`
	// Scheduled is every file of this music that is now waiting to be deleted,
	// including ones an earlier remove already scheduled.
	Scheduled []Scheduled `json:"scheduled"`
	// Kept counts files of this music that stay because Schall did not
	// download them.
	Kept int `json:"kept"`
}

// Swept is what one pass over due removals did.
type Swept struct {
	Removed int
	Spared  int
	Gone    int
}

type Service struct {
	pool    *pgxpool.Pool
	remover FileRemover
	events  *events.Hub
	logger  zerolog.Logger
	now     func() time.Time
}

func NewService(pool *pgxpool.Pool, remover FileRemover, logger zerolog.Logger) *Service {
	return &Service{pool: pool, remover: remover, logger: logger, now: time.Now}
}

// WithEvents registers the hub open pages listen on.
func (service *Service) WithEvents(hub *events.Hub) *Service {
	service.events = hub
	return service
}

// announce tells open pages that what a want or a release shows changed.
func (service *Service) announce(library bool) {
	if service.events == nil {
		return
	}
	service.events.Publish(events.TopicAcquisitions)
	service.events.Publish(events.TopicCatalogue)
	if library {
		service.events.Publish(events.TopicLibrary)
	}
}

func (service *Service) WithClock(now func() time.Time) *Service {
	service.now = now
	return service
}

// heldFiles are the present library files holding a want's music: the copy
// the want was settled with, a file identified as its recording, and a file
// mapped onto a track carrying it. downloaded says a download or an accepted
// copy brought the file in; an upload or a file found by a scan never did.
const heldFiles = `
	WITH target AS (
	    SELECT id, musicbrainz_recording_id AS recording, acquired_library_file_id
	    FROM acquisition_targets WHERE id = $1
	),
	held AS (
	    SELECT acquired_library_file_id AS id FROM target
	    WHERE acquired_library_file_id IS NOT NULL
	    UNION
	    SELECT identities.library_file_id
	    FROM library_file_identities identities, target
	    WHERE identities.musicbrainz_recording_id = target.recording
	    UNION
	    SELECT mappings.library_file_id
	    FROM track_mappings mappings
	    JOIN tracks ON tracks.id = mappings.track_id, target
	    WHERE tracks.musicbrainz_recording_id = target.recording
	)
	SELECT files.id, files.path,
	       files.arrived_by_upload_id IS NULL AND (
	           EXISTS (SELECT 1 FROM downloads WHERE downloads.library_file_id = files.id)
	           OR EXISTS (
	               SELECT 1 FROM acquisition_target_files copies
	               WHERE copies.library_file_id = files.id AND copies.verdict = 'accepted'
	           )
	       ) AS downloaded
	FROM held
	JOIN library_files files ON files.id = held.id AND files.missing_at IS NULL
	ORDER BY files.path
`

// Remove takes the want away now and schedules every file Schall downloaded
// for its music to be deleted after Grace.
//
// A want already removed is removed again without moving its date: the first
// press set the day the person was shown. An acquired want counts as a want
// here too, because the person is saying they no longer want the song they
// have.
func (service *Service) Remove(ctx context.Context, targetID uuid.UUID) (Outcome, error) {
	transaction, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Outcome{}, err
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	var status string
	var recording uuid.NullUUID
	err = transaction.QueryRow(ctx, `
		SELECT status, musicbrainz_recording_id FROM acquisition_targets
		WHERE id = $1 FOR UPDATE
	`, targetID).Scan(&status, &recording)
	if err != nil {
		return Outcome{}, err
	}
	if status == "superseded" {
		return Outcome{}, ErrNothingToRemove
	}
	if status != "not_wanted" {
		// acquired_at and the acquired file stay: they say what the want held,
		// and pressing want again settles on that file while it is there.
		if _, err := transaction.Exec(ctx, `
			UPDATE acquisition_targets
			SET status = 'not_wanted',
			    not_wanted_at = $2,
			    next_attempt_at = NULL,
			    next_search_at = NULL,
			    review_reason = NULL,
			    recheck_reason = NULL,
			    recheck_after = NULL,
			    summary = $3,
			    updated_at = $2
			WHERE id = $1
		`, targetID, service.now(), removedSummary); err != nil {
			return Outcome{}, fmt.Errorf("remove want %s: %w", targetID, err)
		}
	}

	type held struct {
		id         uuid.UUID
		path       string
		downloaded bool
	}
	rows, err := transaction.Query(ctx, heldFiles, targetID)
	if err != nil {
		return Outcome{}, err
	}
	files, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (held, error) {
		var file held
		return file, row.Scan(&file.id, &file.path, &file.downloaded)
	})
	if err != nil {
		return Outcome{}, fmt.Errorf("read the files of want %s: %w", targetID, err)
	}

	outcome := Outcome{TargetID: targetID, Scheduled: []Scheduled{}}
	removesAt := service.now().Add(Grace)
	for _, file := range files {
		if !file.downloaded {
			outcome.Kept++
			continue
		}
		if _, err := transaction.Exec(ctx, `
			INSERT INTO scheduled_removals (
			    acquisition_target_id, musicbrainz_recording_id, library_file_id,
			    library_path, requested_at, removes_at
			)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (library_file_id) WHERE state = 'scheduled' DO NOTHING
		`, targetID, recording, file.id, file.path, service.now(), removesAt); err != nil {
			return Outcome{}, fmt.Errorf("schedule %s for removal: %w", file.path, err)
		}
		var scheduled Scheduled
		if err := transaction.QueryRow(ctx, `
			SELECT library_file_id, library_path, removes_at FROM scheduled_removals
			WHERE library_file_id = $1 AND state = 'scheduled'
		`, file.id).Scan(&scheduled.FileID, &scheduled.Path, &scheduled.RemovesAt); err != nil {
			return Outcome{}, err
		}
		outcome.Scheduled = append(outcome.Scheduled, scheduled)
	}
	if err := transaction.Commit(ctx); err != nil {
		return Outcome{}, err
	}
	service.logger.Info().
		Str("target_id", targetID.String()).
		Int("scheduled", len(outcome.Scheduled)).
		Int("kept", outcome.Kept).
		Msg("want removed")
	service.announce(false)
	return outcome, nil
}

// Keep cancels every scheduled deletion of a want's music, and returns how many
// it cancelled. It is the first half of wanting the song again; the caller
// then takes back the not-wanted decision itself.
func (service *Service) Keep(ctx context.Context, targetID uuid.UUID) (int, error) {
	tag, err := service.pool.Exec(ctx, `
		UPDATE scheduled_removals
		SET state = 'cancelled', settled_at = $2, settled_reason = 'wanted again'
		WHERE state = 'scheduled'
		  AND (acquisition_target_id = $1
		       OR musicbrainz_recording_id = (
		           SELECT musicbrainz_recording_id FROM acquisition_targets WHERE id = $1
		       ))
	`, targetID, service.now())
	if err != nil {
		return 0, fmt.Errorf("keep the files of want %s: %w", targetID, err)
	}
	if tag.RowsAffected() > 0 {
		service.announce(false)
	}
	return int(tag.RowsAffected()), nil
}

// KeepRecording cancels every scheduled deletion of one recording's files and
// returns the wants that were removed for it, so the caller can want them
// again. It is what wanting an owned track again goes through: the track has a
// file and no open want, so the recording is all the page has to name.
func (service *Service) KeepRecording(ctx context.Context, recordingID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := service.pool.Query(ctx, `
		UPDATE scheduled_removals
		SET state = 'cancelled', settled_at = $2, settled_reason = 'wanted again'
		WHERE state = 'scheduled' AND musicbrainz_recording_id = $1
		RETURNING acquisition_target_id
	`, recordingID, service.now())
	if err != nil {
		return nil, fmt.Errorf("keep the files of recording %s: %w", recordingID, err)
	}
	cancelled, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, fmt.Errorf("keep the files of recording %s: %w", recordingID, err)
	}
	seen := make(map[uuid.UUID]struct{}, len(cancelled))
	targets := make([]uuid.UUID, 0, len(cancelled))
	for _, id := range cancelled {
		if _, repeat := seen[id]; !repeat {
			seen[id] = struct{}{}
			targets = append(targets, id)
		}
	}
	if len(targets) > 0 {
		service.announce(false)
	}
	return targets, nil
}

// due is one scheduled removal whose day has come.
type due struct {
	id        uuid.UUID
	targetID  uuid.UUID
	recording uuid.NullUUID
	fileID    uuid.NullUUID
	path      string
}

// stillNeeded says why a due file has to stay, or "" when nothing needs it.
//
// Three things keep a file: a live want holding it or asking for its
// recording, a playlist the person keeps that lists it, and the file also being
// a track of other music than the one removed. Schall's own rotating lists
// (weekly, new releases) do not count; they drop a song that is gone.
const stillNeeded = `
	SELECT CASE
	    WHEN EXISTS (
	        SELECT 1 FROM acquisition_targets
	        WHERE status NOT IN ('not_wanted', 'superseded')
	          AND (acquired_library_file_id = $1
	               OR ($2::uuid IS NOT NULL AND musicbrainz_recording_id = $2))
	    ) THEN 'another want holds it'
	    WHEN EXISTS (
	        SELECT 1 FROM playlist_entries entries
	        JOIN playlists ON playlists.id = entries.playlist_id
	        WHERE entries.owned_library_file_id = $1
	          AND playlists.source NOT IN ('weekly', 'new_releases')
	    ) THEN 'a playlist lists it'
	    WHEN EXISTS (
	        SELECT 1 FROM track_mappings mappings
	        JOIN tracks ON tracks.id = mappings.track_id
	        WHERE mappings.library_file_id = $1
	          AND tracks.musicbrainz_recording_id IS DISTINCT FROM $2
	    ) THEN 'it is also other music'
	    ELSE ''
	END
`

// Sweep deletes every file whose day has come and nothing else needs.
//
// Each row is judged again just before its file goes, because 30 days is long
// enough for anything to have changed. The row is settled as removed in the
// transaction that deletes the file, which commits only after the audio left
// the disc: a row saying removed beside a file still on disc would be a lie.
func (service *Service) Sweep(ctx context.Context) (Swept, error) {
	var swept Swept
	rows, err := service.pool.Query(ctx, `
		SELECT id, acquisition_target_id, musicbrainz_recording_id, library_file_id, library_path
		FROM scheduled_removals
		WHERE state = 'scheduled' AND removes_at <= $1
		ORDER BY removes_at, id
	`, service.now())
	if err != nil {
		return swept, err
	}
	pending, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (due, error) {
		var item due
		return item, row.Scan(&item.id, &item.targetID, &item.recording, &item.fileID, &item.path)
	})
	if err != nil {
		return swept, fmt.Errorf("read due removals: %w", err)
	}

	for _, item := range pending {
		if err := service.settle(ctx, item, &swept); err != nil {
			return swept, err
		}
	}
	if swept.Removed+swept.Spared+swept.Gone > 0 {
		service.announce(true)
	}
	return swept, nil
}

func (service *Service) settle(ctx context.Context, item due, swept *Swept) error {
	if !item.fileID.Valid {
		swept.Gone++
		return service.close(ctx, item.id, "gone", "the file left the library first")
	}
	// Judged inside the transaction that deletes the file, with the row
	// locked: a Keep waits for the delete to commit, and a press of want or a
	// new want committed before this check spares the file.
	var reason string
	_, err := service.remover.RemoveIf(ctx, item.fileID.UUID, func(ctx context.Context, tx pgx.Tx) error {
		var state string
		if err := tx.QueryRow(ctx, `
			SELECT state FROM scheduled_removals WHERE id = $1 FOR UPDATE
		`, item.id).Scan(&state); err != nil {
			return fmt.Errorf("lock removal %s: %w", item.id, err)
		}
		if state != "scheduled" {
			return errNotScheduled
		}
		if err := tx.QueryRow(ctx, stillNeeded, item.fileID.UUID, item.recording).Scan(&reason); err != nil {
			return fmt.Errorf("check whether %s is still needed: %w", item.path, err)
		}
		if reason != "" {
			return errStillNeeded
		}
		_, err := tx.Exec(ctx, `
			UPDATE scheduled_removals
			SET state = 'removed', settled_at = $2, settled_reason = $3
			WHERE id = $1
		`, item.id, service.now(), "removed 30 days earlier and not wanted again")
		return err
	})
	switch {
	case err == nil:
	case errors.Is(err, errNotScheduled):
		return nil
	case errors.Is(err, errStillNeeded):
		swept.Spared++
		service.logger.Info().Str("path", item.path).Str("reason", reason).
			Msg("removal spared")
		return service.close(ctx, item.id, "spared", reason)
	case errors.Is(err, library.ErrFileGone):
		swept.Gone++
		return service.close(ctx, item.id, "gone", "the file left the library first")
	default:
		return fmt.Errorf("delete %s: %w", item.path, err)
	}
	swept.Removed++
	service.logger.Info().Str("path", item.path).
		Str("target_id", item.targetID.String()).
		Msg("removed file deleted")
	return nil
}

func (service *Service) close(ctx context.Context, id uuid.UUID, state, reason string) error {
	_, err := service.pool.Exec(ctx, `
		UPDATE scheduled_removals
		SET state = $2, settled_at = $3, settled_reason = $4
		WHERE id = $1 AND state = 'scheduled'
	`, id, state, service.now(), reason)
	if err != nil {
		return fmt.Errorf("settle removal %s as %s: %w", id, state, err)
	}
	return nil
}

// Run sweeps once now and then every interval until ctx ends. A failed pass is
// logged and tried again at the next tick; nothing is deleted by a pass that
// failed before reaching it.
func (service *Service) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		swept, err := service.Sweep(ctx)
		if err != nil && ctx.Err() == nil {
			service.logger.Error().Err(err).Msg("sweep removed files")
		} else if swept.Removed+swept.Spared+swept.Gone > 0 {
			service.logger.Info().
				Int("removed", swept.Removed).
				Int("spared", swept.Spared).
				Int("gone", swept.Gone).
				Msg("removed files swept")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
