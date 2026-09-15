package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// QueueUpgradeSweep asks for the next page of the library to be checked
// against the quality floor, carrying the position the last pass reached, in
// the same shape QueueLyricsSweep uses.
func (q *Queries) QueueUpgradeSweep(
	ctx context.Context, runAfter time.Time, payload json.RawMessage,
) error {
	if len(payload) == 0 || !json.Valid(payload) {
		return errors.New("upgrade sweep payload must be valid JSON")
	}
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ('sweep_upgrade_candidates', $2::jsonb, 3, $1)
		ON CONFLICT (kind)
		    WHERE kind = 'sweep_upgrade_candidates' AND status IN ('queued', 'running')
		DO UPDATE
		SET run_after = least(jobs.run_after, EXCLUDED.run_after),
		    payload = EXCLUDED.payload,
		    updated_at = now()
		WHERE jobs.status = 'queued'
	`, runAfter, payload)
	return err
}

// EnsureUpgradeSweepQueued adds a pass only when none is queued and none is
// running. Startup is the only caller, in the shape EnsureSpectrumSweepQueued
// uses.
func (q *Queries) EnsureUpgradeSweepQueued(ctx context.Context, runAfter time.Time) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ('sweep_upgrade_candidates', '{}'::jsonb, 3, $1)
		ON CONFLICT (kind)
		    WHERE kind = 'sweep_upgrade_candidates' AND status IN ('queued', 'running')
		DO NOTHING
	`, runAfter)
	return err
}

// UpgradeSettingsRow is whether the low-quality-file sweep runs at all. See
// migration 00072.
type UpgradeSettingsRow struct {
	Enabled   bool
	UpdatedAt time.Time
}

// UpgradeSettings reads the stored switch. A table with no row in it reads as
// off, which is also the column's own default — an installation that has
// never opened the setting is not swept.
func (q *Queries) UpgradeSettings(ctx context.Context) (UpgradeSettingsRow, error) {
	var row UpgradeSettingsRow
	err := q.db.QueryRow(ctx, `
		SELECT enabled, updated_at FROM upgrade_settings WHERE singleton
	`).Scan(&row.Enabled, &row.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return UpgradeSettingsRow{}, nil
	}
	return row, err
}

// SaveUpgradeSettings stores the switch, replacing whatever was there.
func (q *Queries) SaveUpgradeSettings(ctx context.Context, enabled bool) (UpgradeSettingsRow, error) {
	var row UpgradeSettingsRow
	err := q.db.QueryRow(ctx, `
		INSERT INTO upgrade_settings (singleton, enabled)
		VALUES (true, $1)
		ON CONFLICT (singleton) DO UPDATE SET
			enabled = excluded.enabled,
			updated_at = now()
		RETURNING enabled, updated_at
	`, enabled).Scan(&row.Enabled, &row.UpdatedAt)
	return row, err
}

// UpgradeCandidateRow is one library file the sweep can ask "is this under the
// floor". It is the single definition of what a candidate is: the sweep and
// the "files below the floor" count both page through exactly this query, so
// there is never a second answer to what counts.
type UpgradeCandidateRow struct {
	FileID       uuid.UUID
	Path         string
	BitRateKbps  int
	RecordingID  uuid.UUID
	ArtistName   string
	TrackTitle   string
	ReleaseTitle string
	DurationMS   int
	ISRC         string
}

// upgradeStandBackDays is how many days a file stands back from being
// offered as a candidate again after a fetched copy for it turned out no
// better. Without it, a recording whose only copies on offer are no better
// than what is already held would be searched again on every single pass: the
// copy is discarded (ReplaceIfBetter never keeps it), the want that fetched it
// settles, and the file would otherwise qualify again on the very next page.
// Soulseek bans a client that searches the same thing repeatedly (issue
// #193's shape), so this is the same "stand back" reasoning the acquisition
// ladder already uses, flattened to one number because a file is reconsidered
// rarely rather than on a schedule.
const upgradeStandBackDays = 30

// UpgradeCandidates lists files with a settled identity whose bit rate is
// known, whose recording nothing is currently pursuing, and which did not
// just have a copy refused for not being good enough, ordered by id for
// keyset pagination — after is the last id read, or the nil UUID for the
// first page.
//
// A settled identity is exactly the set internal/db/witness.go reads for a
// library witness (settledProofs): a manual decision, AcoustID at the leading
// cluster, measured audio, or a chain resting on one of those. Nothing merely
// tagged, and nothing still unresolved, is offered here.
//
// A file with no stated bit rate is left out rather than treated as under the
// floor. Silence is not agreement, in either direction: it says nothing about
// whether this file is worth upgrading, so it is not read as a "yes".
//
// The exclusion for "something is already pursuing this" is scoped to the
// recording and to the statuses that are actually in flight
// (unresolved/pending/searching/awaiting_review), not to this file and not to
// 'acquired'. Scoping it to the file let a want superseded to pursue a
// different file for the same recording quietly stop excluding the first
// file — the search for one recording never really stopped, but the file
// that had already been tried looked eligible again the moment the want
// moved on. Scoping it to the recording closes that: as long as anything is
// actively out looking for this recording, every file of it stands back,
// whichever one raised the search.
func (q *Queries) UpgradeCandidates(
	ctx context.Context, after uuid.UUID, limit int32,
) ([]UpgradeCandidateRow, error) {
	rows, err := q.db.Query(ctx, fmt.Sprintf(`
		SELECT
			files.id, files.path, files.bit_rate_kbps,
			identities.musicbrainz_recording_id,
			coalesce(nullif(identities.artist_name, ''), files.artist_tag, ''),
			coalesce(nullif(identities.track_title, ''), files.title_tag, ''),
			coalesce(identities.release_title, ''),
			coalesce(files.measured_duration_ms, files.duration_ms, 0),
			coalesce(identities.isrc, '')
		FROM library_files files
		JOIN library_file_identities identities
		    ON identities.library_file_id = files.id
		WHERE files.missing_at IS NULL
		  AND files.resolution_status = 'resolved'
		  AND files.bit_rate_kbps IS NOT NULL
		  AND identities.kind = 'external'
		  AND identities.musicbrainz_recording_id IS NOT NULL
		  AND (
		      identities.is_manual
		      OR regexp_replace(identities.method, '-and-release$', '') = ANY ($1::text[])
		  )
		  AND files.id > $2
		  AND (files.upgrade_refused_at IS NULL
		       OR files.upgrade_refused_at < now() - interval '%d days')
		  AND NOT EXISTS (
		      SELECT 1 FROM acquisition_targets targets
		      WHERE targets.musicbrainz_recording_id = identities.musicbrainz_recording_id
		        AND targets.status IN ('unresolved', 'pending', 'searching', 'awaiting_review')
		  )
		ORDER BY files.id
		LIMIT $3
	`, upgradeStandBackDays), settledProofs, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list files the upgrade sweep may still look at: %w", err)
	}
	defer rows.Close()

	candidates := []UpgradeCandidateRow{}
	for rows.Next() {
		var candidate UpgradeCandidateRow
		if err := rows.Scan(
			&candidate.FileID, &candidate.Path, &candidate.BitRateKbps,
			&candidate.RecordingID, &candidate.ArtistName, &candidate.TrackTitle,
			&candidate.ReleaseTitle, &candidate.DurationMS, &candidate.ISRC,
		); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

// ErrRecordingNotWanted means a person already said stop pursuing this
// recording. That decision wins and the sweep raises no want over it.
var ErrRecordingNotWanted = errors.New("somebody already said not to pursue this recording")

// CreateUpgradeTargetParams is what the sweep found about one file below the
// floor, ready to become a want.
type CreateUpgradeTargetParams struct {
	LibraryFileID   uuid.UUID
	RecordingID     uuid.UUID
	ReleaseGroupID  uuid.NullUUID
	EntryArtist     string
	EntryTitle      string
	EntryAlbum      string
	EntryDurationMS pgtype.Int4
	EntryISRC       pgtype.Text
	Summary         string
}

// CreateUpgradeTarget raises a want for a recording an owned file falls under
// the floor for, reusing whatever the recording already has open rather than
// ever raising a second want for it.
//
// A recording with no target, or one already unresolved/pending/searching/
// awaiting a decision, gets an upgrade-linked want: the first case a fresh
// one, the second the existing one reused as-is — its own origin is left
// alone, so a want a person or a playlist already raised keeps saying so, and
// this sweep is not the reason a copy gets fetched for it. Neither case is
// "created" in the second sense, but only the fresh insert reports true; a
// caller that wants to know whether the recording is now newly being pursued
// reads that.
//
// A recording already acquired — the ordinary case this feature exists for,
// since the file being below the floor implies it was obtained before there
// was one — is superseded: the acquired target moves to 'superseded' and a
// fresh want takes its place, in the same transaction, so a recording is
// never briefly wanted by two live rows at once. A recording somebody said
// not to pursue is left alone entirely and reported as ErrRecordingNotWanted:
// a manual decision wins over an automatic sweep, permanently.
func (q *Queries) CreateUpgradeTarget(
	ctx context.Context, params CreateUpgradeTargetParams,
) (id uuid.UUID, created bool, err error) {
	err = q.inTransaction(ctx, "raising an upgrade want", func(tx pgx.Tx) error {
		var (
			existingID     uuid.UUID
			existingStatus string
		)
		lookErr := tx.QueryRow(ctx, `
			SELECT id, status FROM acquisition_targets
			WHERE musicbrainz_recording_id = $1 AND status <> 'superseded'
			FOR UPDATE
		`, params.RecordingID).Scan(&existingID, &existingStatus)
		switch {
		case errors.Is(lookErr, pgx.ErrNoRows):
			// Nothing wants this recording yet.
		case lookErr != nil:
			return lookErr
		case existingStatus == "not_wanted":
			return ErrRecordingNotWanted
		case existingStatus != "acquired":
			// A live want already covers this recording — reused, not
			// duplicated. It is not linked to this file: only a want the sweep
			// itself raised replaces a file when it lands, and a want already
			// pursued for some other reason must not start deleting music on
			// this feature's say-so.
			id = existingID
			return nil
		default:
			// This is done as three statements rather than one, and in this
			// order, because two constraints would otherwise be unsatisfiable
			// at once: the new row cannot be given a superseded_by_id that
			// does not exist yet (the FK), and it cannot be given this
			// recording while the old row still holds it (the one-live-want-
			// per-recording index). So the new row is inserted first, naming
			// no recording yet; the old row is superseded next, which is what
			// frees the recording for the index to allow; and only then does
			// the new row take it.
			var newID uuid.UUID
			if insertErr := tx.QueryRow(ctx, `
				INSERT INTO acquisition_targets (
					origin, entry_artist, entry_title, entry_album,
					entry_duration_ms, entry_isrc, status, summary
				)
				VALUES ('upgrade', $1, $2, $3, $4, $5, 'unresolved', $6)
				RETURNING id
			`, params.EntryArtist, params.EntryTitle, params.EntryAlbum,
				params.EntryDurationMS, params.EntryISRC, params.Summary,
			).Scan(&newID); insertErr != nil {
				return insertErr
			}
			if _, updateErr := tx.Exec(ctx, `
				UPDATE acquisition_targets
				SET status = 'superseded', superseded_by_id = $2, updated_at = now()
				WHERE id = $1
			`, existingID, newID); updateErr != nil {
				return updateErr
			}
			if _, updateErr := tx.Exec(ctx, `
				UPDATE acquisition_targets
				SET musicbrainz_recording_id = $2, musicbrainz_release_group_id = $3,
				    resolution_method = 'upgrade', resolved_at = now(),
				    status = 'pending', next_attempt_at = now(),
				    upgrade_of_library_file_id = $4, updated_at = now()
				WHERE id = $1
			`, newID, params.RecordingID, params.ReleaseGroupID, params.LibraryFileID,
			); updateErr != nil {
				return updateErr
			}
			id, created = newID, true
			return nil
		}

		insertErr := tx.QueryRow(ctx, `
			INSERT INTO acquisition_targets (
				origin, entry_artist, entry_title, entry_album,
				entry_duration_ms, entry_isrc, musicbrainz_recording_id,
				musicbrainz_release_group_id, resolution_method, resolved_at,
				status, summary, next_attempt_at, upgrade_of_library_file_id
			)
			VALUES (
				'upgrade', $1, $2, $3, $4, $5, $6, $7, 'upgrade', now(),
				'pending', $8, now(), $9
			)
			RETURNING id
		`, params.EntryArtist, params.EntryTitle, params.EntryAlbum,
			params.EntryDurationMS, params.EntryISRC, params.RecordingID,
			params.ReleaseGroupID, params.Summary, params.LibraryFileID,
		).Scan(&id)
		created = true
		return insertErr
	})
	return id, created, err
}
