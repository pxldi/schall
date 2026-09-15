-- name: ListActiveJobs :many
-- What the worker is doing and what is waiting for it.
--
-- Which lane a job is on is not decided here. The worker divides the searching
-- from everything else and that division is passed in as an array, so there is
-- one answer to which lane a kind belongs to rather than a second one written
-- in SQL that drifts from the first the next time a kind is added.
--
-- Nothing in this file is indexed by kind or by status, so each query is a scan
-- of a table that grows with every scan and every resolution. That is
-- affordable because it is asked by one person looking at one page, and an
-- index for it would be a migration for a view.
SELECT
    id,
    kind,
    status,
    attempts,
    max_attempts,
    run_after,
    started_at,
    created_at,
    -- What the last attempt failed with. A queued job carrying one is between
    -- retries rather than new, and run_after is how much of the backoff is
    -- left to run.
    error_message,
    (count(*) OVER ())::bigint AS total
FROM jobs
WHERE status IN ('queued', 'running')
-- What is happening now, then what is due soonest, which is the order the
-- worker itself claims in.
ORDER BY (status = 'running') DESC, run_after, created_at
LIMIT sqlc.arg('row_limit');

-- name: ListFailedJobs :many
-- The jobs that spent their attempts, newest first.
--
-- 'cancelled' is not here. A cancelled job was stopped because the work it
-- belonged to was called off, and it is not waiting for anybody to do anything
-- about it.
SELECT
    id,
    kind,
    -- What the job was about. A release group ingest that failed is only worth
    -- reading next to the release group it failed on.
    payload,
    attempts,
    max_attempts,
    error_message,
    completed_at,
    created_at,
    (count(*) OVER ())::bigint AS total
FROM jobs
WHERE status = 'failed'
ORDER BY completed_at DESC NULLS LAST, created_at DESC
LIMIT sqlc.arg('row_limit');

-- name: JobSubjects :many
-- What each of these jobs is about, in the words the catalogue already holds.
--
-- A job row carries a kind and a payload of identifiers, which is everything
-- the worker needs and nothing a person can read: import_download over a UUID
-- says only that something somewhere is being fetched. The subject is the join
-- back to whatever that identifier names — a release, an artist, a file, a
-- list — and it is made for a whole page at once, because a view that resolved
-- one row at a time would ask the database two hundred questions to draw one
-- screen.
--
-- Every cast sits inside a CASE on the kind, and deliberately. Two kinds may
-- write different things under the same payload key, and a cast evaluated on a
-- row it was not written for would fail the whole page rather than leave one
-- row unnamed.
--
-- A kind whose payload names nothing, and a payload naming a row that has since
-- been deleted, are absent from the answer rather than present and blank. There
-- is no fallback to the identifier: a row reading "ingest 8f4e2c1a" has told
-- the reader only that Schall cannot say what it is doing, which is worse than
-- a row that says "ingest" and stops.
WITH listed AS (
    SELECT id, kind, payload
    FROM jobs
    WHERE id = ANY(sqlc.arg('job_ids')::uuid[])
),
named AS (
    SELECT
        listed.id,
        CASE WHEN listed.kind = 'refresh_artist_metadata'
             THEN (listed.payload ->> 'artistId')::uuid END AS artist_id,
        CASE WHEN listed.kind IN ('refresh_album_metadata', 'search_album_sources')
             THEN (listed.payload ->> 'albumId')::uuid END AS album_id,
        CASE WHEN listed.kind IN ('resolve_library_file', 'tag_file')
             THEN (listed.payload ->> 'fileId')::uuid END AS file_id,
        CASE WHEN listed.kind = 'import_download'
             THEN (listed.payload ->> 'requestId')::uuid END AS request_id,
        -- A playlist sync with no list named is the pass over the whole player,
        -- which is about every list and therefore about none of them.
        CASE WHEN listed.kind IN ('import_playlist', 'sync_navidrome_playlists')
             THEN (listed.payload ->> 'playlistId')::uuid END AS playlist_id,
        CASE WHEN listed.kind = 'ingest_release_group'
             THEN (listed.payload ->> 'releaseGroupId')::uuid END AS release_group_id,
        -- The one kind whose payload is its own subject. An upload has no row
        -- anywhere — the job is the record of it — so the names it staged are
        -- read back out of the payload rather than joined to.
        CASE WHEN listed.kind = 'import_upload'
              AND jsonb_typeof(listed.payload -> 'files') = 'array'
             THEN listed.payload -> 'files' END AS upload_files
    FROM listed
),
resolved AS (
    SELECT
        named.id,
        -- The name of the thing, and never the name of anything else. The
        -- artist is carried beside it rather than folded in, because a search
        -- run spanning twelve artists wants both and a transfer of one release
        -- wants the title.
        coalesce(
            artist.name,
            album.title,
            file.path,
            playlist.name,
            ingested.title,
            request_album.title,
            nullif(btrim(want.entry_title), ''),
            named.upload_files ->> 0,
            ''
        )::text AS subject,
        coalesce(
            album_artist.name,
            ingested_artist.name,
            request_artist.name,
            nullif(btrim(want.entry_artist), ''),
            ''
        )::text AS subject_artist,
        -- Files, and only files. A transfer knows how many it agreed to take
        -- and an upload names the ones it staged; a playlist's tracks are
        -- counted in a different unit and are not smuggled in under this one.
        coalesce(
            request.file_count,
            jsonb_array_length(named.upload_files),
            0
        )::integer AS subject_file_count
    FROM named
    LEFT JOIN artists artist ON artist.id = named.artist_id
    LEFT JOIN albums album ON album.id = named.album_id
    LEFT JOIN artists album_artist ON album_artist.id = album.artist_id
    LEFT JOIN library_files file ON file.id = named.file_id
    LEFT JOIN playlists playlist ON playlist.id = named.playlist_id
    -- An ingest exists because the release group is not in the catalogue yet,
    -- so this joins nothing most of the time. It is asked anyway for the retry
    -- of a job that failed after the album had landed, which is the one case
    -- where the queue can say what the row is about.
    LEFT JOIN albums ingested
        ON ingested.musicbrainz_release_group_id = named.release_group_id
    LEFT JOIN artists ingested_artist ON ingested_artist.id = ingested.artist_id
    LEFT JOIN download_requests request ON request.id = named.request_id
    LEFT JOIN albums request_album ON request_album.id = request.album_id
    LEFT JOIN artists request_artist ON request_artist.id = request_album.artist_id
    -- A request fetching one file for a want has no release behind it, and
    -- what it is about is the entry somebody asked for.
    LEFT JOIN acquisition_targets want ON want.id = request.acquisition_target_id
)
SELECT id, subject, subject_artist, subject_file_count
FROM resolved
WHERE subject <> '';

-- name: RecurringJobPulse :many
-- When each recurring pass last ran and when the next one is due.
--
-- Next is read off the queue rather than worked out from an interval. The
-- sweeps schedule themselves, so the row one of them queued is already the
-- answer, and an interval would be a guess about a worker that may not be
-- running at all. A kind with no queued row has no next time, and the absence
-- is reported as the absence it is.
--
-- A kind that has never been queued does not appear in the answer. The caller
-- knows which passes it asked about and fills that in itself, because a pass
-- missing from the queue is a fact about the installation rather than a gap.
SELECT
    kind,
    (max(completed_at) FILTER (
        WHERE status IN ('completed', 'failed', 'cancelled')
    ))::timestamptz AS last_finished_at,
    -- Told apart from the last run, so that a pass which has been failing since
    -- Tuesday reads as one rather than as a pass that ran an hour ago.
    (max(completed_at) FILTER (WHERE status = 'completed'))::timestamptz
        AS last_completed_at,
    (max(started_at) FILTER (WHERE status = 'running'))::timestamptz
        AS running_since,
    (min(run_after) FILTER (WHERE status = 'queued'))::timestamptz
        AS next_run_after
FROM jobs
WHERE kind = ANY(sqlc.arg('kinds')::text[])
GROUP BY kind;
