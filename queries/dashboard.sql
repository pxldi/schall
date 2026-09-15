-- name: DashboardSummary :one
SELECT
    (SELECT count(*) FROM artists WHERE followed_at IS NOT NULL)::bigint AS artist_count,
    (SELECT count(*) FROM artists WHERE followed_at IS NULL)::bigint AS catalogue_artist_count,
    (SELECT count(*) FROM albums)::bigint AS album_count,
    (SELECT count(*) FROM tracks)::bigint AS track_count,
    -- The badge and the Wishlist tab count the same set: wants whose status is
    -- unresolved, pending, or searching. These are the wants the list calls
    -- open, so the number names exactly what the linked tab shows.
    (SELECT count(*) FROM acquisition_targets
        WHERE status IN ('unresolved', 'pending', 'searching'))::bigint
        AS open_want_count,
    -- What the Downloads page calls open, counted the way that page counts it:
    -- one per request that has not finished, never one per file inside it. The
    -- badge and the list have to agree or the badge is not about anything the
    -- reader can go and look at — it used to add the individual transfers to the
    -- requests, so a single album being fetched read as a dozen things happening.
    -- The same rule is written once more as downloadViewOpen in
    -- internal/db/downloads.go, which is the filter the badge links to. Change
    -- one and change the other, or the badge counts what the screen will not show.
    (SELECT count(*) FROM download_requests
        WHERE status IN ('requested', 'started'))::bigint
        AS active_download_count,
    -- Running and waiting are counted apart because they are not the same news.
    -- A catalogue refresh queues one job per album, so a healthy installation
    -- routinely has hundreds waiting behind three workers — and the overview
    -- used to add them together and call the total "jobs running", which read
    -- as six hundred things happening at once.
    (SELECT count(*) FROM jobs WHERE status = 'running')::bigint
        AS running_job_count,
    (SELECT count(*) FROM jobs WHERE status = 'queued')::bigint
        AS queued_job_count;
