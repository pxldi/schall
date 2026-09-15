-- name: UpsertRecommendationDismissal :one
INSERT INTO recommendation_dismissals (subject_type, musicbrainz_id, dismissed_at)
VALUES (sqlc.arg('subject_type'), sqlc.arg('musicbrainz_id'), sqlc.arg('dismissed_at'))
ON CONFLICT (subject_type, musicbrainz_id) DO UPDATE SET
    -- Repeating the same dismissal is the same permanent fact. Keep when the
    -- user first said it rather than making a retry look like a new decision.
    dismissed_at = recommendation_dismissals.dismissed_at
RETURNING *;

-- name: RecordRecommendationFeedback :one
-- The candidate supplies all context. A missing current row means the source
-- replaced the suggestion before this press arrived, so no feedback is stored.
INSERT INTO recommendation_feedback (
    source,
    musicbrainz_recording_id,
    musicbrainz_release_group_id,
    musicbrainz_artist_ids,
    signal,
    reason_codes,
    reason_context
)
SELECT
    candidates.source,
    candidates.musicbrainz_recording_id,
    candidates.musicbrainz_release_group_id,
    candidates.musicbrainz_artist_ids,
    sqlc.arg('signal')::text,
    candidates.reason_codes,
    candidates.reason_context
FROM recommendation_candidates AS candidates
WHERE candidates.source = sqlc.arg('source')
  AND candidates.musicbrainz_recording_id = sqlc.arg('musicbrainz_recording_id')
RETURNING *;

-- name: ListRecommendationFeedback :many
SELECT *
FROM recommendation_feedback
WHERE source = sqlc.arg('source')
ORDER BY created_at DESC, id DESC;

-- name: DeleteRecommendationFeedback :exec
DELETE FROM recommendation_feedback
WHERE source = sqlc.arg('source');

-- name: RecordRecommendationImpression :one
-- One row is the whole fatigue cycle. A read inside the minimum interval is a
-- no-op; the first showing after an expired suppression starts at one and clears
-- that stale window in the same update so the ordering constraint stays true.
INSERT INTO recommendation_impressions (
    musicbrainz_recording_id,
    ignored_impressions,
    last_shown_at,
    suppressed_until,
    created_at,
    updated_at
)
VALUES (
    sqlc.arg('musicbrainz_recording_id'),
    1,
    sqlc.arg('observed_at'),
    CASE WHEN sqlc.arg('threshold')::integer <= 1
         THEN sqlc.arg('observed_at')::timestamptz
              + sqlc.arg('suppression_microseconds')::bigint * interval '1 microsecond'
    END,
    sqlc.arg('observed_at'),
    sqlc.arg('observed_at')
)
ON CONFLICT (musicbrainz_recording_id) DO UPDATE SET
    ignored_impressions = CASE
        WHEN (recommendation_impressions.suppressed_until IS NULL
              OR recommendation_impressions.suppressed_until <= sqlc.arg('observed_at'))
         AND recommendation_impressions.last_shown_at
                <= sqlc.arg('observed_at')::timestamptz
                   - sqlc.arg('minimum_gap_microseconds')::bigint * interval '1 microsecond'
            THEN CASE
                WHEN recommendation_impressions.suppressed_until IS NOT NULL
                    THEN 1
                ELSE recommendation_impressions.ignored_impressions + 1
            END
        ELSE recommendation_impressions.ignored_impressions
    END,
    last_shown_at = CASE
        WHEN (recommendation_impressions.suppressed_until IS NULL
              OR recommendation_impressions.suppressed_until <= sqlc.arg('observed_at'))
         AND recommendation_impressions.last_shown_at
                <= sqlc.arg('observed_at')::timestamptz
                   - sqlc.arg('minimum_gap_microseconds')::bigint * interval '1 microsecond'
            THEN sqlc.arg('observed_at')
        ELSE recommendation_impressions.last_shown_at
    END,
    suppressed_until = CASE
        WHEN NOT (
            (recommendation_impressions.suppressed_until IS NULL
             OR recommendation_impressions.suppressed_until <= sqlc.arg('observed_at'))
            AND recommendation_impressions.last_shown_at
                  <= sqlc.arg('observed_at')::timestamptz
                     - sqlc.arg('minimum_gap_microseconds')::bigint * interval '1 microsecond'
        ) THEN recommendation_impressions.suppressed_until
        WHEN recommendation_impressions.suppressed_until IS NOT NULL THEN NULL
        WHEN recommendation_impressions.ignored_impressions + 1 >= sqlc.arg('threshold')::integer
            THEN sqlc.arg('observed_at')::timestamptz
                 + sqlc.arg('suppression_microseconds')::bigint * interval '1 microsecond'
        ELSE NULL
    END,
    updated_at = CASE
        WHEN (recommendation_impressions.suppressed_until IS NULL
              OR recommendation_impressions.suppressed_until <= sqlc.arg('observed_at'))
         AND recommendation_impressions.last_shown_at
                <= sqlc.arg('observed_at')::timestamptz
                   - sqlc.arg('minimum_gap_microseconds')::bigint * interval '1 microsecond'
            THEN sqlc.arg('observed_at')
        ELSE recommendation_impressions.updated_at
    END
RETURNING *;

-- name: ListedRecommendationRecordings :many
-- Which of these recordings a stored list actually offers.
--
-- The fatigue count may only be written about a row that existed to be shown,
-- and the route that writes it takes any UUID a caller sends. No source is
-- named because impressions are not: one recording has one fatigue cycle,
-- whichever list offered it.
SELECT DISTINCT musicbrainz_recording_id
FROM recommendation_candidates
WHERE musicbrainz_recording_id = ANY(sqlc.arg('musicbrainz_recording_ids')::uuid[]);

-- name: UpsertRecommendationSnapshot :one
INSERT INTO recommendation_snapshots (
    source,
    source_snapshot_id,
    source_snapshot_at,
    status,
    detail,
    fetched_at
)
VALUES (
    sqlc.arg('source'),
    sqlc.narg('source_snapshot_id'),
    sqlc.narg('source_snapshot_at'),
    sqlc.arg('status'),
    sqlc.arg('detail'),
    sqlc.arg('fetched_at')
)
ON CONFLICT (source) DO UPDATE SET
    source_snapshot_id = excluded.source_snapshot_id,
    source_snapshot_at = excluded.source_snapshot_at,
    status = excluded.status,
    detail = excluded.detail,
    fetched_at = excluded.fetched_at
RETURNING *;

-- name: DeleteRecommendationCandidates :exec
DELETE FROM recommendation_candidates WHERE source = sqlc.arg('source');

-- name: InsertRecommendationCandidate :one
INSERT INTO recommendation_candidates (
    source,
    musicbrainz_recording_id,
    musicbrainz_release_group_id,
    musicbrainz_artist_ids,
    recording_title,
    release_title,
    artist_name,
    rank,
    source_score,
    reason_codes,
    reason_context,
    fetched_at
)
VALUES (
    sqlc.arg('source'),
    sqlc.arg('musicbrainz_recording_id'),
    sqlc.arg('musicbrainz_release_group_id'),
    sqlc.arg('musicbrainz_artist_ids'),
    sqlc.arg('recording_title'),
    sqlc.arg('release_title'),
    sqlc.arg('artist_name'),
    sqlc.arg('rank'),
    sqlc.narg('source_score'),
    sqlc.arg('reason_codes'),
    sqlc.arg('reason_context'),
    sqlc.arg('fetched_at')
)
RETURNING *;

-- name: ListRecommendationCandidateFacts :many
-- These are the inputs to the pure suppression decision: one per rule, and two
-- for the second, which asks whether the reader has already answered about
-- acquiring this recording — by asking for it, or by stopping. They are read
-- from current local state every time the list is asked for; provider rows are
-- never deleted or annotated merely because one input currently suppresses.
--
-- The first rule, `owned`, asks whether the library already holds the music,
-- and it can be answered four ways: the file's proven identity names the
-- recording or its release group, or the track the file is mapped to names the
-- recording, or that track's album names the release group.
--
-- Those four used to be one EXISTS with an OR across three tables, written
-- beside the candidate it was about. A subquery in the select list is run once
-- per row and is never turned into a join, so every suggestion walked the whole
-- library: 1298 suggestions against 30000 files measured 79 seconds, with the
-- walk repeated 1298 times (issue #331).
--
-- The four answers are collected first instead, and only for the identifiers
-- this list actually carries. Each set is then at most as large as the list, so
-- what a read costs follows the length of the list and no longer the size of
-- the collection: the same 1298 against the same 30000 measured 36 milliseconds.
-- Restricting each set to the list is what keeps it small enough to hash, which
-- is what stops the walk coming back on a larger library.
WITH listed AS (
    SELECT
        candidates.*,
        snapshots.source_snapshot_id,
        snapshots.source_snapshot_at,
        snapshots.status AS snapshot_status,
        snapshots.detail AS snapshot_detail,
        snapshots.fetched_at AS snapshot_fetched_at
    FROM recommendation_candidates AS candidates
    JOIN recommendation_snapshots AS snapshots ON snapshots.source = candidates.source
    WHERE candidates.source = sqlc.arg('source')
),
-- A file that has gone missing proves nothing, here as everywhere else.
owned_recordings AS (
    SELECT library_file_identities.musicbrainz_recording_id AS musicbrainz_id
    FROM library_file_identities
    JOIN library_files ON library_files.id = library_file_identities.library_file_id
    WHERE library_files.missing_at IS NULL
      AND library_file_identities.musicbrainz_recording_id
              IN (SELECT musicbrainz_recording_id FROM listed)
    UNION
    SELECT tracks.musicbrainz_recording_id
    FROM tracks
    JOIN track_mappings ON track_mappings.track_id = tracks.id
    JOIN library_files ON library_files.id = track_mappings.library_file_id
    WHERE library_files.missing_at IS NULL
      AND tracks.musicbrainz_recording_id
              IN (SELECT musicbrainz_recording_id FROM listed)
),
owned_release_groups AS (
    SELECT library_file_identities.musicbrainz_release_group_id AS musicbrainz_id
    FROM library_file_identities
    JOIN library_files ON library_files.id = library_file_identities.library_file_id
    WHERE library_files.missing_at IS NULL
      AND library_file_identities.musicbrainz_release_group_id
              IN (SELECT musicbrainz_release_group_id FROM listed)
    UNION
    SELECT albums.musicbrainz_release_group_id
    FROM albums
    JOIN tracks ON tracks.album_id = albums.id
    JOIN track_mappings ON track_mappings.track_id = tracks.id
    JOIN library_files ON library_files.id = track_mappings.library_file_id
    WHERE library_files.missing_at IS NULL
      AND albums.musicbrainz_release_group_id
              IN (SELECT musicbrainz_release_group_id FROM listed)
)
SELECT
    listed.*,
    (
        listed.musicbrainz_recording_id IN (SELECT musicbrainz_id FROM owned_recordings)
        OR listed.musicbrainz_release_group_id
               IN (SELECT musicbrainz_id FROM owned_release_groups)
    )::boolean AS owned,
    EXISTS (
        SELECT 1
        FROM acquisition_targets
        WHERE acquisition_targets.musicbrainz_recording_id
                = listed.musicbrainz_recording_id
          AND acquisition_targets.status IN (
              'unresolved', 'pending', 'searching', 'awaiting_review'
          )
    )::boolean AS requested,
    -- A want somebody stopped. It is a decision about acquisition, not about
    -- taste, so it is counted apart from a dismissal and read as its own rule:
    -- the only two things this list offers are "find me a copy" and "never
    -- suggest it again", and a reader who already answered the first one has
    -- nothing left here to press (issue #338).
    EXISTS (
        SELECT 1
        FROM acquisition_targets
        WHERE acquisition_targets.musicbrainz_recording_id
                = listed.musicbrainz_recording_id
          AND acquisition_targets.status = 'not_wanted'
    )::boolean AS not_wanted,
    EXISTS (
        SELECT 1
        FROM artists
        WHERE artists.musicbrainz_id = ANY(listed.musicbrainz_artist_ids)
          AND artists.followed_at IS NOT NULL
    )::boolean AS followed_artist,
    -- The same rule again, for a label instead of an artist: the follow feed
    -- already covers this release, so recommending it a second time here is
    -- offering to fetch it twice.
    EXISTS (
        SELECT 1
        FROM label_releases
        JOIN labels ON labels.id = label_releases.label_id
        JOIN albums ON albums.id = label_releases.album_id
        WHERE albums.musicbrainz_release_group_id = listed.musicbrainz_release_group_id
          AND labels.followed_at IS NOT NULL
    )::boolean AS followed_label,
    (
        EXISTS (
            SELECT 1 FROM recommendation_dismissals
            WHERE subject_type = 'recording'
              AND musicbrainz_id = listed.musicbrainz_recording_id
        )
        OR EXISTS (
            SELECT 1 FROM recommendation_dismissals
            WHERE subject_type = 'release_group'
              AND musicbrainz_id = listed.musicbrainz_release_group_id
        )
        OR EXISTS (
            SELECT 1 FROM recommendation_dismissals
            WHERE subject_type = 'artist'
              AND musicbrainz_id = ANY(listed.musicbrainz_artist_ids)
        )
    )::boolean AS dismissed,
    EXISTS (
        SELECT 1
        FROM recommendation_impressions
        WHERE recommendation_impressions.musicbrainz_recording_id
                = listed.musicbrainz_recording_id
          AND recommendation_impressions.suppressed_until > sqlc.arg('observed_at')
    )::boolean AS impression_fatigue,
    -- The sixth rule. The weekly playlist obtained this recording, the user had
    -- it on their disc for a week, and nothing said they wanted to keep it, so
    -- it was removed again (ADR 0021). Offering it back the following week would
    -- buy it a second time. The window is written by the weekly refresh and
    -- expires on its own, because a week of not listening is not the statement
    -- "never suggest this" — that statement is a dismissal, and the reader makes
    -- it themselves.
    EXISTS (
        SELECT 1
        FROM recommendation_unkept
        WHERE recommendation_unkept.musicbrainz_recording_id
                = listed.musicbrainz_recording_id
          AND recommendation_unkept.suppressed_until > sqlc.arg('observed_at')
    )::boolean AS unkept
FROM listed
ORDER BY listed.rank;
