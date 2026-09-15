-- A label is a company that publishes music, held by its MusicBrainz
-- identifier. Following one says "keep what this label publishes complete", the
-- same statement following an artist makes, and the follow feed acts on both.

-- name: CreateLabel :one
-- Follows a label chosen from a MusicBrainz search.
--
-- A label Schall already has a row for is promoted rather than rejected: the
-- row survives an unfollow, so following again is the same decision made a
-- second time. A label that is already followed returns no row, and the caller
-- reports the conflict that genuinely is one.
--
-- The follow queues the walk over the label's releases, because until now
-- nothing about this label had been fetched.
WITH followed_label AS (
    INSERT INTO labels (musicbrainz_id, name, label_type, country, disambiguation, followed_at)
    VALUES (
        sqlc.arg('musicbrainz_id'), sqlc.arg('name'), sqlc.arg('label_type'),
        sqlc.arg('country'), sqlc.arg('disambiguation'), now()
    )
    ON CONFLICT (musicbrainz_id) DO UPDATE
    SET followed_at = now(),
        name = EXCLUDED.name,
        label_type = EXCLUDED.label_type,
        country = EXCLUDED.country,
        disambiguation = EXCLUDED.disambiguation,
        -- A new follow walks the catalogue from the start again, so a label
        -- that was unfollowed halfway through a walk does not resume into the
        -- middle of one.
        releases_offset = 0,
        updated_at = now()
    WHERE labels.followed_at IS NULL
    RETURNING id, musicbrainz_id, name, label_type, country, disambiguation,
              monitor_level, followed_at, last_refreshed_at
),
queued_job AS (
    INSERT INTO jobs (kind, payload)
    SELECT 'refresh_label_metadata', jsonb_build_object('labelId', id)
    FROM followed_label
)
SELECT id, musicbrainz_id, name, label_type, country, disambiguation,
       monitor_level, followed_at, last_refreshed_at
FROM followed_label;

-- name: ListLabels :many
-- Every label Schall holds, with how much of what it published the library has.
--
-- The list is not paged. Both halves of it are labels somebody chose: a
-- followed label was followed by hand, and a held one is a followed label that
-- was let go and kept its releases. Neither half grows on its own the way the
-- artists list does, where owning one album adds an artist.
SELECT
    labels.id,
    labels.musicbrainz_id,
    labels.name,
    labels.label_type,
    labels.country,
    labels.disambiguation,
    labels.monitor_level,
    labels.followed_at,
    labels.last_refreshed_at,
    COALESCE(counted.release_count, 0)::bigint AS release_count,
    COALESCE(counted.owned_release_count, 0)::bigint AS owned_release_count,
    COALESCE(counted.track_count, 0)::bigint AS track_count,
    COALESCE(counted.owned_track_count, 0)::bigint AS owned_track_count,
    COALESCE(latest_refresh.status, 'pending')::text AS refresh_status
FROM labels
LEFT JOIN LATERAL (
    SELECT
        count(*)::bigint AS release_count,
        count(*) FILTER (
            WHERE album_tracks.track_count > 0
              AND album_tracks.owned_count = album_tracks.track_count
        )::bigint AS owned_release_count,
        COALESCE(sum(album_tracks.track_count), 0)::bigint AS track_count,
        COALESCE(sum(album_tracks.owned_count), 0)::bigint AS owned_track_count
    FROM label_releases
    JOIN albums ON albums.id = label_releases.album_id
    JOIN LATERAL (
    -- What the library holds of this release. Owned means a present file is
    -- that track's — mapped onto it, or proven to be its recording on a file
    -- mapped nowhere or mapped onto another release that carries it. The same
    -- two proofs the acquisition loop settles a want against, so a release the
    -- loop would call complete is counted complete here.
    SELECT
        count(*)::bigint AS track_count,
        count(*) FILTER (WHERE holding.held)::bigint AS owned_count
    FROM tracks
    JOIN LATERAL (
        SELECT (EXISTS (
            SELECT 1
            FROM track_mappings
            JOIN library_files ON library_files.id = track_mappings.library_file_id
            WHERE track_mappings.track_id = tracks.id
              AND library_files.missing_at IS NULL
        ) OR EXISTS (
            SELECT 1
            FROM library_file_identities
            JOIN library_files
                ON library_files.id = library_file_identities.library_file_id
            WHERE library_file_identities.musicbrainz_recording_id
                    = tracks.musicbrainz_recording_id
              AND library_files.missing_at IS NULL
        ) OR EXISTS (
            SELECT 1
            FROM tracks AS carrying
            JOIN track_mappings AS elsewhere ON elsewhere.track_id = carrying.id
            JOIN library_files ON library_files.id = elsewhere.library_file_id
            WHERE carrying.musicbrainz_recording_id = tracks.musicbrainz_recording_id
              AND library_files.missing_at IS NULL
        )) AS held
    ) AS holding ON true
    WHERE tracks.album_id = albums.id
) AS album_tracks ON true
JOIN LATERAL (
    -- What the label's monitor level says this release is, decided by
    -- MusicBrainz's own types exactly as an artist's level decides it.
    SELECT
        CASE labels.monitor_level
            WHEN 'everything' THEN true
            WHEN 'main' THEN albums.album_type IN ('album', 'ep', 'single')
                AND NOT (albums.secondary_types
                    && '{live,compilation,remix,dj-mix,mixtape/street,demo,broadcast}'::text[])
            WHEN 'albums_eps' THEN albums.album_type IN ('album', 'ep')
                AND NOT (albums.secondary_types
                    && '{live,compilation,remix,dj-mix,mixtape/street,demo,broadcast}'::text[])
            ELSE album_tracks.owned_count > 0
        END AS monitored
) AS measured ON true
    WHERE label_releases.label_id = labels.id
      AND measured.monitored
) AS counted ON true
LEFT JOIN LATERAL (
    SELECT status
    FROM jobs
    WHERE kind = 'refresh_label_metadata'
      AND payload->>'labelId' = labels.id::text
    ORDER BY created_at DESC
    LIMIT 1
) AS latest_refresh ON true
ORDER BY labels.followed_at IS NULL, lower(labels.name), labels.id;

-- name: GetLabel :one
SELECT
    labels.id,
    labels.musicbrainz_id,
    labels.name,
    labels.label_type,
    labels.country,
    labels.disambiguation,
    labels.monitor_level,
    labels.followed_at,
    labels.last_refreshed_at,
    COALESCE(latest_refresh.status, 'pending')::text AS refresh_status,
    latest_refresh.error_message AS refresh_error
FROM labels
LEFT JOIN LATERAL (
    SELECT status, error_message
    FROM jobs
    WHERE kind = 'refresh_label_metadata'
      AND payload->>'labelId' = labels.id::text
    ORDER BY created_at DESC
    LIMIT 1
) AS latest_refresh ON true
WHERE labels.id = sqlc.arg('id');

-- name: LabelReleases :many
-- What one label published, newest first, with how much of each the library
-- holds. Every release is listed, whatever the monitor level says: the level
-- decides what counts as missing, never what is shown.
SELECT
    albums.id,
    albums.title,
    albums.musicbrainz_release_group_id,
    albums.release_date,
    albums.album_type,
    albums.secondary_types,
    artists.id AS artist_id,
    artists.name AS artist_name,
    album_tracks.track_count,
    album_tracks.owned_count AS owned_track_count,
    measured.monitored::boolean AS monitored
FROM label_releases
JOIN labels ON labels.id = label_releases.label_id
JOIN albums ON albums.id = label_releases.album_id
JOIN artists ON artists.id = albums.artist_id
JOIN LATERAL (
    -- What the library holds of this release. Owned means a present file is
    -- that track's — mapped onto it, or proven to be its recording on a file
    -- mapped nowhere or mapped onto another release that carries it. The same
    -- two proofs the acquisition loop settles a want against, so a release the
    -- loop would call complete is counted complete here.
    SELECT
        count(*)::bigint AS track_count,
        count(*) FILTER (WHERE holding.held)::bigint AS owned_count
    FROM tracks
    JOIN LATERAL (
        SELECT (EXISTS (
            SELECT 1
            FROM track_mappings
            JOIN library_files ON library_files.id = track_mappings.library_file_id
            WHERE track_mappings.track_id = tracks.id
              AND library_files.missing_at IS NULL
        ) OR EXISTS (
            SELECT 1
            FROM library_file_identities
            JOIN library_files
                ON library_files.id = library_file_identities.library_file_id
            WHERE library_file_identities.musicbrainz_recording_id
                    = tracks.musicbrainz_recording_id
              AND library_files.missing_at IS NULL
        ) OR EXISTS (
            SELECT 1
            FROM tracks AS carrying
            JOIN track_mappings AS elsewhere ON elsewhere.track_id = carrying.id
            JOIN library_files ON library_files.id = elsewhere.library_file_id
            WHERE carrying.musicbrainz_recording_id = tracks.musicbrainz_recording_id
              AND library_files.missing_at IS NULL
        )) AS held
    ) AS holding ON true
    WHERE tracks.album_id = albums.id
) AS album_tracks ON true
JOIN LATERAL (
    -- What the label's monitor level says this release is, decided by
    -- MusicBrainz's own types exactly as an artist's level decides it.
    SELECT
        CASE labels.monitor_level
            WHEN 'everything' THEN true
            WHEN 'main' THEN albums.album_type IN ('album', 'ep', 'single')
                AND NOT (albums.secondary_types
                    && '{live,compilation,remix,dj-mix,mixtape/street,demo,broadcast}'::text[])
            WHEN 'albums_eps' THEN albums.album_type IN ('album', 'ep')
                AND NOT (albums.secondary_types
                    && '{live,compilation,remix,dj-mix,mixtape/street,demo,broadcast}'::text[])
            ELSE album_tracks.owned_count > 0
        END AS monitored
) AS measured ON true
WHERE label_releases.label_id = sqlc.arg('label_id')
ORDER BY albums.release_date DESC NULLS LAST, lower(albums.title), albums.id;

-- name: UnfollowLabel :one
-- Unfollowing is demotion, not deletion — the same rule as for an artist, for a
-- simpler reason. Nothing hangs off a label: `label_releases` says which
-- releases it published, and the releases themselves belong to their artists
-- and would survive the row being deleted. What deleting would throw away is
-- the record of a walk over thousands of releases, so that following again
-- would have to fetch every one of them a second time.
--
-- Work already queued for the label is stopped. A label nobody follows is not
-- one whose catalogue is worth another page of requests; a walk already running
-- is left alone rather than interrupted mid-flight.
WITH target AS (
    SELECT labels.id
    FROM labels
    WHERE labels.id = sqlc.arg('label_id') AND labels.followed_at IS NOT NULL
),
cancelled_jobs AS (
    UPDATE jobs
    SET status = 'cancelled', completed_at = now(), updated_at = now()
    WHERE kind = 'refresh_label_metadata'
      AND status = 'queued'
      AND payload->>'labelId' = (SELECT target.id::text FROM target)
)
UPDATE labels
SET followed_at = NULL, releases_offset = 0, updated_at = now()
WHERE id = (SELECT target.id FROM target)
RETURNING id, musicbrainz_id, name, label_type, country, disambiguation,
          monitor_level, followed_at, last_refreshed_at;

-- name: SetLabelMonitorLevel :one
-- What counts as one of this label's releases. It changes counting and what the
-- feed wants next, and deletes nothing: wants already made were asked for.
UPDATE labels
SET monitor_level = sqlc.arg('monitor_level'), updated_at = now()
WHERE id = sqlc.arg('label_id')
RETURNING id, monitor_level;

-- QueueLabelRefresh is handwritten in internal/db/labels.go: the unique
-- index on active refresh_label_metadata jobs (00070_labels.sql) means two
-- concurrent asks can both attempt the insert, and one of them has to catch
-- the resulting 23505 rather than rely on a read-then-insert that cannot see
-- the other transaction's uncommitted row.

-- name: LabelForRefresh :one
-- What one pass over a label's releases needs: which label MusicBrainz is being
-- asked about, where the last pass stopped, and whether it is still followed.
-- An unfollow can land between one pass queuing the next and that pass
-- running, and the refresher reads followed_at to stop there rather than
-- keep walking a label nobody asked about any more.
SELECT id, musicbrainz_id, name, releases_offset, followed_at
FROM labels
WHERE id = sqlc.arg('label_id');

-- name: LinkLabelRelease :exec
-- Records that a label published a release. The release group is already in the
-- catalogue by the time this is called; this is only the fact that links them,
-- and saying it twice says the same thing.
INSERT INTO label_releases (label_id, album_id)
VALUES (sqlc.arg('label_id'), sqlc.arg('album_id'))
ON CONFLICT (label_id, album_id) DO NOTHING;

-- name: AdvanceLabelRefresh :exec
-- Where the next pass starts. Written after every page, so a walk stopped by a
-- restart or a rate limit resumes at the page it reached.
UPDATE labels
SET releases_offset = sqlc.arg('releases_offset'), updated_at = now()
WHERE id = sqlc.arg('label_id');

-- name: CompleteLabelRefresh :exec
-- The walk reached the end of the label's releases. The position goes back to
-- the start, because the next walk is a fresh look for what has been published
-- since, and the refresh date is what the feed reads to decide when that is.
UPDATE labels
SET releases_offset = 0, last_refreshed_at = now(), updated_at = now()
WHERE id = sqlc.arg('label_id');

-- name: StaleFollowedLabels :many
-- The followed labels nobody has asked MusicBrainz about lately. A label part
-- way through a walk is stale whatever its refresh date says, because the walk
-- has not finished and the rest of its releases have not been seen at all.
SELECT labels.id
FROM labels
WHERE labels.followed_at IS NOT NULL
  AND (
      labels.releases_offset > 0
      OR labels.last_refreshed_at IS NULL
      OR labels.last_refreshed_at < sqlc.arg('refreshed_before')
  )
ORDER BY labels.last_refreshed_at NULLS FIRST, labels.id
LIMIT sqlc.arg('row_limit');

-- name: FollowFeedLabelTracks :many
-- The tracks the feed reaches through a followed label: music published by that
-- label after it was followed, that the library does not hold and nobody has
-- asked for. It is FollowFeedTracks asked through `label_releases` instead of
-- through the release's artist, and every exclusion in it is the same one, for
-- the same reason — the loop settles a want against either kind of proof, and a
-- recording already decided about stays decided.
SELECT
    labels.id AS label_id,
    labels.name AS label_name,
    albums.id AS album_id,
    albums.title AS album_title,
    albums.musicbrainz_release_group_id,
    artists.name AS artist_name,
    tracks.title,
    tracks.duration_ms,
    tracks.isrc,
    tracks.musicbrainz_recording_id
FROM label_releases
JOIN labels ON labels.id = label_releases.label_id
JOIN albums ON albums.id = label_releases.album_id
JOIN artists ON artists.id = albums.artist_id
JOIN tracks ON tracks.album_id = albums.id
JOIN LATERAL (
    -- What the library holds of this release. Owned means a present file is
    -- that track's — mapped onto it, or proven to be its recording on a file
    -- mapped nowhere or mapped onto another release that carries it. The same
    -- two proofs the acquisition loop settles a want against, so a release the
    -- loop would call complete is counted complete here.
    SELECT
        count(*)::bigint AS track_count,
        count(*) FILTER (WHERE holding.held)::bigint AS owned_count
    FROM tracks
    JOIN LATERAL (
        SELECT (EXISTS (
            SELECT 1
            FROM track_mappings
            JOIN library_files ON library_files.id = track_mappings.library_file_id
            WHERE track_mappings.track_id = tracks.id
              AND library_files.missing_at IS NULL
        ) OR EXISTS (
            SELECT 1
            FROM library_file_identities
            JOIN library_files
                ON library_files.id = library_file_identities.library_file_id
            WHERE library_file_identities.musicbrainz_recording_id
                    = tracks.musicbrainz_recording_id
              AND library_files.missing_at IS NULL
        ) OR EXISTS (
            SELECT 1
            FROM tracks AS carrying
            JOIN track_mappings AS elsewhere ON elsewhere.track_id = carrying.id
            JOIN library_files ON library_files.id = elsewhere.library_file_id
            WHERE carrying.musicbrainz_recording_id = tracks.musicbrainz_recording_id
              AND library_files.missing_at IS NULL
        )) AS held
    ) AS holding ON true
    WHERE tracks.album_id = albums.id
) AS album_tracks ON true
JOIN LATERAL (
    -- What the label's monitor level says this release is, decided by
    -- MusicBrainz's own types exactly as an artist's level decides it.
    SELECT
        CASE labels.monitor_level
            WHEN 'everything' THEN true
            WHEN 'main' THEN albums.album_type IN ('album', 'ep', 'single')
                AND NOT (albums.secondary_types
                    && '{live,compilation,remix,dj-mix,mixtape/street,demo,broadcast}'::text[])
            WHEN 'albums_eps' THEN albums.album_type IN ('album', 'ep')
                AND NOT (albums.secondary_types
                    && '{live,compilation,remix,dj-mix,mixtape/street,demo,broadcast}'::text[])
            ELSE album_tracks.owned_count > 0
        END AS monitored
) AS measured ON true
WHERE labels.followed_at IS NOT NULL
  AND albums.release_date IS NOT NULL
  AND albums.release_date > labels.followed_at::date
  AND tracks.musicbrainz_recording_id IS NOT NULL
  AND measured.monitored
  AND NOT EXISTS (
      SELECT 1
      FROM tracks AS carrying
      JOIN track_mappings ON track_mappings.track_id = carrying.id
      JOIN library_files ON library_files.id = track_mappings.library_file_id
      WHERE carrying.musicbrainz_recording_id = tracks.musicbrainz_recording_id
        AND library_files.missing_at IS NULL
  )
  AND NOT EXISTS (
      SELECT 1
      FROM library_file_identities
      JOIN library_files ON library_files.id = library_file_identities.library_file_id
      WHERE library_file_identities.musicbrainz_recording_id = tracks.musicbrainz_recording_id
        AND library_files.missing_at IS NULL
  )
  AND NOT EXISTS (
      SELECT 1
      FROM acquisition_targets
      WHERE acquisition_targets.musicbrainz_recording_id = tracks.musicbrainz_recording_id
        AND acquisition_targets.status <> 'superseded'
  )
ORDER BY albums.release_date, albums.id,
         tracks.disc_number, tracks.track_number NULLS LAST, tracks.id
LIMIT sqlc.arg('row_limit');
