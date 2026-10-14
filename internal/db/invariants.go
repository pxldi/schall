package db

import (
	"context"
	"fmt"
	"time"
)

// Invariant is one rule Schall's data keeps at all times, written as a query
// that counts the rows breaking it. Every count is zero on a healthy database.
// The invariant check job runs them all, stores the counts and logs each one above zero, and
// the seed's integration test requires every one to be zero on the fixture.
type Invariant struct {
	Name  string
	Rule  string
	Query string
}

// InvariantCount is what one Invariant found.
type InvariantCount struct {
	Name       string
	Rule       string
	Violations int64
}

// heldRecordings is every recording a present file holds, in the sense the
// release page uses (ListAlbumTracks): a file whose identity names it, or a
// file mapped onto any catalogue track carrying it. A second row of the
// file's song (library_file_second_rows, migration 00126) counts as held too,
// as it does for a want (OwnedFileForRecording). Retired recording ids count
// as their surviving id.
const heldRecordings = `
	SELECT canonical_recording_id(identity.musicbrainz_recording_id) AS recording_id
	FROM library_file_identities AS identity
	JOIN library_files AS file ON file.id = identity.library_file_id AND file.missing_at IS NULL
	WHERE identity.musicbrainz_recording_id IS NOT NULL
	UNION
	SELECT canonical_recording_id(second_row.musicbrainz_recording_id)
	FROM library_file_second_rows AS second_row
	JOIN library_file_identities AS identity ON identity.id = second_row.identity_id
	JOIN library_files AS file ON file.id = identity.library_file_id AND file.missing_at IS NULL
	UNION
	SELECT canonical_recording_id(track.musicbrainz_recording_id)
	FROM track_mappings AS mapping
	JOIN library_files AS file ON file.id = mapping.library_file_id AND file.missing_at IS NULL
	JOIN tracks AS track ON track.id = mapping.track_id
	WHERE track.musicbrainz_recording_id IS NOT NULL`

// ExplicitEditionDetail is the attempt detail a want for a clean edition
// settles with when the file holds the explicit one (ADR 0032). The data check
// reads it back, so the acquisition service writes this constant.
const ExplicitEditionDetail = "Downloaded, audio verified, imported. Kept as the explicit version."

// Invariants are the rules a live read on 2026-10-14 checked (the queries in
// the project's data-checks/invariants.sql, less the ones that only find cases
// to read). Each one stays cheap on a library of ~70k files: the whole set ran
// in about a second there.
var Invariants = []Invariant{
	{
		Name: "acquired_recording_not_held",
		Rule: "an acquired want's recording is held by a present file",
		Query: `
			WITH held AS (` + heldRecordings + `)
			SELECT count(*) FROM acquisition_targets AS want
			LEFT JOIN library_files AS settled_on
			    ON settled_on.id = want.acquired_library_file_id AND settled_on.missing_at IS NULL
			LEFT JOIN library_file_identities AS settled_identity
			    ON settled_identity.library_file_id = settled_on.id
			WHERE want.status = 'acquired' AND want.musicbrainz_recording_id IS NOT NULL
			  AND canonical_recording_id(want.musicbrainz_recording_id)
			      NOT IN (SELECT recording_id FROM held WHERE recording_id IS NOT NULL)
			  -- Two settlements leave the want's row unheld on purpose, and LostWants
			  -- leaves both alone. A want for a clean edition settles on the explicit
			  -- file it holds (ADR 0032), and the file keeps the explicit row until
			  -- something answers it again. A want can also settle on a file with no
			  -- identity that the matcher mapped onto another row of the song.
			  AND NOT (settled_on.id IS NOT NULL AND settled_identity.id IS NOT NULL
			           AND settled_identity.created_at <= want.acquired_at
			           AND (SELECT attempt.detail FROM acquisition_target_attempts AS attempt
			                WHERE attempt.acquisition_target_id = want.id AND attempt.outcome = 'acquired'
			                ORDER BY attempt.recorded_at DESC LIMIT 1)
			               = '` + ExplicitEditionDetail + `')
			  AND NOT (settled_on.id IS NOT NULL AND settled_identity.id IS NULL
			           AND EXISTS (SELECT 1 FROM track_mappings AS mapping
			                       WHERE mapping.library_file_id = settled_on.id))`,
	},
	{
		Name: "acquired_source_file_gone",
		Rule: "an acquired source-keyed want's file is present",
		Query: `
			SELECT count(*) FROM acquisition_targets AS want
			LEFT JOIN library_files AS file ON file.id = want.acquired_library_file_id
			WHERE want.status = 'acquired' AND want.source IS NOT NULL
			  AND (file.id IS NULL OR file.missing_at IS NOT NULL)`,
	},
	{
		Name: "open_want_already_held",
		Rule: "no open want asks for a recording a present file holds",
		Query: `
			WITH held AS (` + heldRecordings + `)
			SELECT count(*) FROM acquisition_targets AS want
			WHERE want.status IN ('unresolved', 'pending', 'searching', 'awaiting_review')
			  AND want.origin <> 'upgrade'
			  AND want.musicbrainz_recording_id IS NOT NULL
			  AND canonical_recording_id(want.musicbrainz_recording_id)
			      IN (SELECT recording_id FROM held WHERE recording_id IS NOT NULL)`,
	},
	{
		Name: "open_source_want_already_held",
		Rule: "no open source-keyed want asks for a source track a present file holds",
		Query: `
			SELECT count(*) FROM acquisition_targets AS want
			WHERE want.status IN ('unresolved', 'pending', 'searching', 'awaiting_review')
			  AND want.source IS NOT NULL
			  AND EXISTS (
			      SELECT 1 FROM library_file_identities AS identity
			      JOIN library_files AS file ON file.id = identity.library_file_id AND file.missing_at IS NULL
			      WHERE identity.kind = 'source'
			        AND identity.source = want.source AND identity.external_id = want.external_id)`,
	},
	{
		// A want, a track or a tag may keep a retired recording id for good:
		// every comparison reads it through recording_aliases (migration
		// 00056). What a merge must not leave is two wants for one song, which
		// acquisition_targets_recording_idx cannot see because it keys the id
		// as stored.
		Name: "one_want_per_song",
		Rule: "a recording and the ids MusicBrainz merged into it have one want between them",
		Query: `
			SELECT count(*) FROM (
			    SELECT canonical_recording_id(musicbrainz_recording_id)
			    FROM acquisition_targets
			    WHERE musicbrainz_recording_id IS NOT NULL AND status <> 'superseded'
			    GROUP BY 1
			    HAVING count(*) > 1
			) AS twice`,
	},
	{
		// A second accepted copy of a want that had already settled on another
		// copy never records its file on the copy. Its transfer does.
		Name: "accepted_copy_file_gone",
		Rule: "an accepted copy's library file is present, or the copy is marked removed",
		Query: `
			SELECT count(*) FROM acquisition_target_files AS copy
			LEFT JOIN downloads AS transfer
			  ON transfer.download_request_id = copy.download_request_id
			 AND transfer.remote_path = copy.remote_path
			LEFT JOIN library_files AS file
			  ON file.id = coalesce(copy.library_file_id, transfer.library_file_id)
			WHERE copy.verdict = 'accepted' AND copy.file_removed_at IS NULL
			  AND copy.download_request_id IS NOT NULL
			  AND (file.id IS NULL OR file.missing_at IS NOT NULL)`,
	},
	{
		Name: "scheduled_removal_stale",
		Rule: "a scheduled removal belongs to an unwanted want, its file is present, and it is not past due",
		Query: `
			SELECT count(*) FROM scheduled_removals AS removal
			JOIN acquisition_targets AS want ON want.id = removal.acquisition_target_id
			LEFT JOIN library_files AS file ON file.id = removal.library_file_id
			WHERE removal.state = 'scheduled'
			  AND (want.status <> 'not_wanted' OR file.id IS NULL OR file.missing_at IS NOT NULL
			       OR removal.removes_at < now() - interval '1 day')`,
	},
	{
		Name: "upgrade_want_file_gone",
		Rule: "an open upgrade want's file to upgrade is present",
		Query: `
			SELECT count(*) FROM acquisition_targets AS want
			LEFT JOIN library_files AS file ON file.id = want.upgrade_of_library_file_id
			WHERE want.origin = 'upgrade'
			  AND want.status IN ('unresolved', 'pending', 'searching', 'awaiting_review')
			  AND (file.id IS NULL OR file.missing_at IS NOT NULL)`,
	},
	{
		Name: "recording_question_without_choice",
		Rule: "a which-recording question has a candidate left to pick",
		Query: `
			SELECT count(*) FROM acquisition_targets AS want
			WHERE want.status = 'awaiting_review' AND want.review_reason = 'resolution'
			  AND NOT EXISTS (
			      SELECT 1 FROM acquisition_target_candidates AS candidate
			      WHERE candidate.acquisition_target_id = want.id
			        AND NOT EXISTS (
			            SELECT 1 FROM acquisition_target_rejected_recordings AS rejected
			            WHERE rejected.acquisition_target_id = want.id
			              AND rejected.musicbrainz_recording_id = candidate.musicbrainz_recording_id))`,
	},
	{
		Name:  "download_request_open_too_long",
		Rule:  "a download request is settled within 2 days",
		Query: `SELECT count(*) FROM download_requests WHERE status IN ('requested', 'started') AND requested_at < now() - interval '2 days'`,
	},
	{
		Name:  "import_validating_too_long",
		Rule:  "an import finishes validating within an hour",
		Query: `SELECT count(*) FROM download_requests WHERE import_status = 'validating' AND updated_at < now() - interval '1 hour'`,
	},
	{
		Name:  "copy_fetching_too_long",
		Rule:  "a copy is delivered or given up within a day",
		Query: `SELECT count(*) FROM acquisition_target_files WHERE verdict = 'fetching' AND updated_at < now() - interval '1 day'`,
	},
	{
		Name:  "want_searching_too_long",
		Rule:  "a want leaves searching within a day",
		Query: `SELECT count(*) FROM acquisition_targets WHERE status = 'searching' AND updated_at < now() - interval '1 day'`,
	},
	{
		Name: "match_status_disagrees",
		Rule: "a present file is matched exactly when it is mapped onto a track",
		Query: `
			SELECT count(*) FROM library_files AS file
			WHERE file.missing_at IS NULL
			  AND (file.match_status = 'matched')
			      <> EXISTS (SELECT 1 FROM track_mappings AS mapping WHERE mapping.library_file_id = file.id)`,
	},
	{
		// Manual mappings win (CLAUDE.md). A track carrying a second row of the
		// file's song is the file's own on purpose: the matcher maps the file to
		// those rows' tracks beside its own (migration 00126).
		Name: "mapping_contradicts_identity",
		Rule: "an automatic mapping's track carries the recording the file's identity names, or a second row of it",
		Query: `
			SELECT count(*) FROM track_mappings AS mapping
			JOIN library_files AS file ON file.id = mapping.library_file_id AND file.missing_at IS NULL
			JOIN tracks AS track ON track.id = mapping.track_id
			JOIN library_file_identities AS identity ON identity.library_file_id = file.id
			WHERE NOT mapping.is_manual
			  AND identity.musicbrainz_recording_id IS NOT NULL
			  AND track.musicbrainz_recording_id IS NOT NULL
			  AND canonical_recording_id(identity.musicbrainz_recording_id)
			      <> canonical_recording_id(track.musicbrainz_recording_id)
			  AND NOT EXISTS (
			      SELECT 1 FROM library_file_second_rows AS second_row
			      WHERE second_row.identity_id = identity.id
			        AND canonical_recording_id(second_row.musicbrainz_recording_id)
			            = canonical_recording_id(track.musicbrainz_recording_id))`,
	},
	{
		Name: "recording_held_twice_unsettled",
		Rule: "two present files of one recording are settled as duplicates",
		Query: `
			SELECT count(*) FROM (
			    SELECT identity.musicbrainz_recording_id
			    FROM library_file_identities AS identity
			    JOIN library_files AS file ON file.id = identity.library_file_id AND file.missing_at IS NULL
			    WHERE identity.musicbrainz_recording_id IS NOT NULL
			    GROUP BY 1
			    HAVING count(*) > 1
			       AND count(*) FILTER (WHERE file.match_status = 'duplicate' OR file.duplicate_kept_at IS NOT NULL) = 0
			) AS twice`,
	},
	{
		// A resolved file with neither row is fine while it waits in Review as
		// an ambiguous or duplicate file. An automatic local_only answer writes
		// no identity row (identity.StatusLocalOnly), so it is not checked.
		Name: "resolution_disagrees_with_identity",
		Rule: "a resolved file outside Review has an identity or a mapping, and a source file has a source identity",
		Query: `
			SELECT count(*) FROM library_files AS file
			LEFT JOIN library_file_identities AS identity ON identity.library_file_id = file.id
			WHERE file.missing_at IS NULL
			  AND ((file.resolution_status = 'resolved' AND identity.id IS NULL
			        AND file.match_status NOT IN ('ambiguous', 'duplicate')
			        AND NOT EXISTS (SELECT 1 FROM track_mappings AS mapping WHERE mapping.library_file_id = file.id))
			    OR (file.resolution_status = 'source' AND identity.kind IS DISTINCT FROM 'source'))`,
	},
	{
		Name: "resolution_stuck",
		Rule: "a present file is resolved within a day of arriving",
		Query: `
			SELECT count(*) FROM library_files
			WHERE missing_at IS NULL AND resolution_status IN ('pending', 'failed')
			  AND created_at < now() - interval '1 day'`,
	},
	{
		// An ambiguous or duplicate file is a question in Review, which is
		// where its unmapped identity is meant to wait.
		Name: "proven_file_unmapped",
		Rule: "a present file whose proven recording is in the catalogue is mapped onto a track",
		Query: `
			SELECT count(*) FROM library_files AS file
			JOIN library_file_identities AS identity
			  ON identity.library_file_id = file.id AND identity.musicbrainz_recording_id IS NOT NULL
			WHERE file.missing_at IS NULL AND file.match_status NOT IN ('ambiguous', 'duplicate')
			  AND NOT EXISTS (SELECT 1 FROM track_mappings AS mapping WHERE mapping.library_file_id = file.id)
			  AND EXISTS (SELECT 1 FROM tracks AS track
			              WHERE track.musicbrainz_recording_id = identity.musicbrainz_recording_id)`,
	},
	{
		Name:  "removal_not_unlinked",
		Rule:  "a recorded removal is unlinked within a day",
		Query: `SELECT count(*) FROM library_file_removals WHERE unlinked_at IS NULL AND removed_at < now() - interval '1 day'`,
	},
	{
		Name:  "file_without_root",
		Rule:  "a present file sits under a library root",
		Query: `SELECT count(*) FROM library_files WHERE missing_at IS NULL AND library_root_id IS NULL`,
	},
	{
		Name: "missing_file_mapped",
		Rule: "a missing file is mapped onto no track",
		Query: `
			SELECT count(*) FROM library_files AS file
			WHERE file.missing_at IS NOT NULL
			  AND EXISTS (SELECT 1 FROM track_mappings AS mapping WHERE mapping.library_file_id = file.id)`,
	},
	{
		Name: "album_without_tracks",
		Rule: "an album a day old has tracks",
		Query: `
			SELECT count(*) FROM albums AS album
			WHERE album.created_at < now() - interval '1 day'
			  AND NOT EXISTS (SELECT 1 FROM tracks AS track WHERE track.album_id = album.id)`,
	},
	{
		Name: "album_without_edition",
		Rule: "an album with a release group has a chosen edition",
		Query: `
			SELECT count(*) FROM albums AS album
			WHERE album.musicbrainz_release_group_id IS NOT NULL
			  AND NOT EXISTS (SELECT 1 FROM release_editions AS edition WHERE edition.album_id = album.id)`,
	},
	{
		// The same three counts album_counts_recompute writes, read instead,
		// and like it on the recording ids as stored.
		Name: "album_counts_drift",
		Rule: "an album's stored track, owned and dismissed counts match its tracks",
		Query: `
			WITH held AS (
			    SELECT identity.musicbrainz_recording_id AS recording_id
			    FROM library_file_identities AS identity
			    JOIN library_files AS file ON file.id = identity.library_file_id AND file.missing_at IS NULL
			    UNION
			    SELECT track.musicbrainz_recording_id
			    FROM track_mappings AS mapping
			    JOIN library_files AS file ON file.id = mapping.library_file_id AND file.missing_at IS NULL
			    JOIN tracks AS track ON track.id = mapping.track_id
			),
			mapped AS (
			    SELECT mapping.track_id FROM track_mappings AS mapping
			    JOIN library_files AS file ON file.id = mapping.library_file_id AND file.missing_at IS NULL
			),
			dismissed AS (
			    SELECT DISTINCT musicbrainz_recording_id AS recording_id FROM acquisition_targets
			    WHERE status = 'not_wanted' AND musicbrainz_recording_id IS NOT NULL
			),
			counted AS (
			    SELECT album.track_count, album.owned_track_count, album.dismissed_track_count,
			           count(track.id) AS tracks,
			           count(track.id) FILTER (WHERE owned) AS owned,
			           count(track.id) FILTER (WHERE NOT owned AND dismissed.recording_id IS NOT NULL) AS dismissed
			    FROM albums AS album
			    LEFT JOIN tracks AS track ON track.album_id = album.id
			    LEFT JOIN mapped ON mapped.track_id = track.id
			    LEFT JOIN held ON held.recording_id = track.musicbrainz_recording_id
			    LEFT JOIN dismissed ON dismissed.recording_id = track.musicbrainz_recording_id
			    CROSS JOIN LATERAL (SELECT mapped.track_id IS NOT NULL OR held.recording_id IS NOT NULL AS owned) AS decided
			    GROUP BY album.id
			)
			SELECT count(*) FROM counted
			WHERE (track_count, owned_track_count, dismissed_track_count) <> (tracks, owned, dismissed)`,
	},
}

// CountInvariantViolations runs every Invariant in order. Each is a plain
// count. A query that fails stops the run, because a check that cannot be read
// is a fault in the check.
func (q *Queries) CountInvariantViolations(ctx context.Context) ([]InvariantCount, error) {
	counts := make([]InvariantCount, 0, len(Invariants))
	for _, invariant := range Invariants {
		var violations int64
		if err := q.db.QueryRow(ctx, invariant.Query).Scan(&violations); err != nil {
			return counts, fmt.Errorf("count invariant %s: %w", invariant.Name, err)
		}
		counts = append(counts, InvariantCount{Name: invariant.Name, Rule: invariant.Rule, Violations: violations})
	}
	return counts, nil
}

// invariantResultsKept is how long RecordInvariantCounts keeps a run's counts.
const invariantResultsKept = "90 days"

// RecordInvariantCounts stores one run's counts, zeros included, under the
// time the run was taken, and drops runs older than invariantResultsKept.
func (q *Queries) RecordInvariantCounts(ctx context.Context, checkedAt time.Time, counts []InvariantCount) error {
	names := make([]string, len(counts))
	violations := make([]int64, len(counts))
	for i, count := range counts {
		names[i], violations[i] = count.Name, count.Violations
	}
	if _, err := q.db.Exec(ctx, `
		INSERT INTO invariant_check_results (checked_at, invariant, violations)
		SELECT $1, name, violations
		FROM unnest($2::text[], $3::bigint[]) AS run (name, violations)
		ON CONFLICT (checked_at, invariant) DO UPDATE SET violations = EXCLUDED.violations
	`, checkedAt, names, violations); err != nil {
		return fmt.Errorf("record invariant counts at %s: %w", checkedAt, err)
	}
	if _, err := q.db.Exec(ctx, `
		DELETE FROM invariant_check_results
		WHERE checked_at < $1::timestamptz - $2::interval
	`, checkedAt, invariantResultsKept); err != nil {
		return fmt.Errorf("drop invariant counts older than %s: %w", invariantResultsKept, err)
	}
	return nil
}

// QueueInvariantCheck schedules the next invariant check, in the shape
// QueueDecodeSweep uses. Migration 00128's partial unique index keeps it to
// one check at a time, and a queued check moves to the earlier time. Startup
// asks for now, so a check queued for tomorrow runs at once.
func (q *Queries) QueueInvariantCheck(ctx context.Context, runAfter time.Time) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ('check_invariants', '{}'::jsonb, 3, $1)
		ON CONFLICT (kind)
		    WHERE kind = 'check_invariants' AND status IN ('queued', 'running')
		DO UPDATE
		SET run_after = least(jobs.run_after, EXCLUDED.run_after),
		    updated_at = now()
		WHERE jobs.status = 'queued'
	`, runAfter)
	return err
}
