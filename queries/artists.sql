-- name: ListArtists :many
-- An explicit limit pages the list for callers that need it. A zero limit
-- returns every row for the artists landing page. Only one half of the list
-- grows on purpose. Following an
-- artist is a deliberate act and that set stays about as large as the user's
-- attention; holding one is a side effect of owning their music, and a
-- collection acquired album by album holds a long tail of artists with a single
-- release each. Scope narrows the page to one of those two claims, and an empty
-- scope means everyone, because every artist listed is either followed or held
-- and neither is noise the user never asked for.
--
-- Completeness is aggregated over the whole catalogue rather than over the page,
-- because the page can be ordered and narrowed by it, and a number computed
-- after the page was chosen could do neither. One grouped pass over every
-- artist's tracks is what that costs, which is affordable at the size a
-- self-hosted library reaches. If it ever stops being, the answer is a
-- maintained column, not a cheaper approximation of how much is owned.
WITH completeness AS (
    -- Two decisions shape these numbers, and both narrow the denominator
    -- rather than counting as missing. A dismissed track — not owned, and its
    -- recording carries a not_wanted want — was decided about per track; a
    -- release the artist's monitor level does not reach was decided about up
    -- front. Neither hides anything: the discography stays browsable, and the
    -- header states the basis the fraction was computed over.
    SELECT
        albums.artist_id,
        count(*) FILTER (
            WHERE measured.monitored
              AND (album_tracks.track_count = 0 OR measured.countable_count > 0)
        )::bigint AS release_count,
        count(*) FILTER (
            WHERE measured.monitored
              AND measured.countable_count > 0
              AND album_tracks.owned_count = measured.countable_count
        )::bigint AS owned_release_count,
        COALESCE(sum(measured.countable_count) FILTER (WHERE measured.monitored), 0)::bigint
            AS track_count,
        COALESCE(sum(album_tracks.owned_count) FILTER (WHERE measured.monitored), 0)::bigint
            AS owned_track_count
    FROM albums
    JOIN artists AS monitoring ON monitoring.id = albums.artist_id
    JOIN LATERAL (
        -- Owned means the library still has a file that is this track, whether
        -- it is mapped onto it or proven to be its recording elsewhere. Still
        -- has, always: a file that went missing leaves its mapping behind, and
        -- counting it would call a release complete when nothing can play it —
        -- the same rule the releases browser counts by (albumJoins in
        -- internal/db/catalogue.go).
        SELECT
            count(*)::bigint AS track_count,
            count(*) FILTER (WHERE holding.held)::bigint AS owned_count,
            count(*) FILTER (
                WHERE NOT holding.held AND dismissals.id IS NOT NULL
            )::bigint AS dismissed_count
        FROM tracks
        LEFT JOIN track_mappings
            ON track_mappings.track_id = tracks.id
           AND EXISTS (
               SELECT 1 FROM library_files
               WHERE library_files.id = track_mappings.library_file_id
                 AND library_files.missing_at IS NULL
           )
        -- The library can hold a track without mapping it onto this release:
        -- the same recording proven on a file whose identity names it, or on a
        -- file mapped onto another release's track that carries it. Counting
        -- only the mapping would leave a recording that sits on a single, an EP
        -- and a compilation owned on one of the three and missing on the other
        -- two forever, and the acquisition loop already settles those wants
        -- against either proof (OwnedFileForRecording).
        JOIN LATERAL (
            SELECT (track_mappings.id IS NOT NULL OR EXISTS (
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
                JOIN track_mappings AS elsewhere
                    ON elsewhere.track_id = carrying.id
                JOIN library_files ON library_files.id = elsewhere.library_file_id
                WHERE carrying.musicbrainz_recording_id
                        = tracks.musicbrainz_recording_id
                  AND library_files.missing_at IS NULL
            )) AS held
        ) AS holding ON true
        -- At most one target exists per recording outside 'superseded'
        -- (acquisition_targets_recording_idx), so this join cannot fan out.
        LEFT JOIN acquisition_targets AS dismissals
            ON tracks.musicbrainz_recording_id IS NOT NULL
           AND dismissals.musicbrainz_recording_id = tracks.musicbrainz_recording_id
           AND dismissals.status = 'not_wanted'
        WHERE tracks.album_id = albums.id
    ) AS album_tracks ON true
    JOIN LATERAL (
        -- What the artist's monitor level says this release is. MusicBrainz's
        -- own types decide "main": albums, EPs and singles that are not one of
        -- the secondary shapes nobody means when they ask for a discography.
        -- "owned" counts only what the library already holds — every release it
        -- reaches is complete by construction, and nothing else exists to be
        -- missing.
        SELECT
            CASE monitoring.monitor_level
                WHEN 'everything' THEN true
                WHEN 'main' THEN albums.album_type IN ('album', 'ep', 'single')
                    AND NOT (albums.secondary_types
                        && '{live,compilation,remix,dj-mix,mixtape/street,demo,broadcast}'::text[])
                WHEN 'albums_eps' THEN albums.album_type IN ('album', 'ep')
                    AND NOT (albums.secondary_types
                        && '{live,compilation,remix,dj-mix,mixtape/street,demo,broadcast}'::text[])
                ELSE album_tracks.owned_count > 0
            END AS monitored,
            CASE WHEN monitoring.monitor_level = 'owned'
                 THEN album_tracks.owned_count
                 ELSE album_tracks.track_count - album_tracks.dismissed_count
            END AS countable_count
    ) AS measured ON true
    GROUP BY albums.artist_id
),
in_flight AS (
    -- Transfers actually moving, not requests on record. A download request
    -- stays 'requested' after its files arrive, so it says what was asked for
    -- rather than what is happening now, and a card reading "downloading"
    -- forever would be worse than one that never said it.
    SELECT
        COALESCE(albums.artist_id, track_albums.artist_id) AS artist_id,
        count(*)::bigint AS in_flight_count
    FROM downloads
    LEFT JOIN albums ON albums.id = downloads.album_id
    LEFT JOIN tracks ON tracks.id = downloads.track_id
    LEFT JOIN albums AS track_albums ON track_albums.id = tracks.album_id
    WHERE downloads.status IN ('queued', 'searching', 'downloading', 'importing')
      AND COALESCE(albums.artist_id, track_albums.artist_id) IS NOT NULL
    GROUP BY COALESCE(albums.artist_id, track_albums.artist_id)
),
awaiting_review AS (
    -- A want whose copy nobody could identify is held for a person to listen to.
    -- Reaching the artist from it goes through the release the want was resolved
    -- against, either the album it came from or the release group it named. The
    -- join can match both, so targets are counted once per artist and not once
    -- per way of getting there.
    SELECT
        albums.artist_id,
        count(DISTINCT held_copies.acquisition_target_id)::bigint AS review_count
    FROM acquisition_target_files AS held_copies
    JOIN acquisition_targets AS targets
        ON targets.id = held_copies.acquisition_target_id
    JOIN albums
        ON albums.id = targets.origin_album_id
        OR (targets.musicbrainz_release_group_id IS NOT NULL
            AND albums.musicbrainz_release_group_id = targets.musicbrainz_release_group_id)
    WHERE held_copies.verdict = 'held'
      -- Only wants still being pursued, the same rule the review queue reads by
      -- (AcquisitionReviewQueue). Accepting one copy leaves its siblings held, so
      -- counting held rows alone would badge an artist for a question the queue
      -- no longer asks and send somebody looking for something that is not there.
      AND targets.status NOT IN ('acquired', 'not_wanted', 'superseded')
    GROUP BY albums.artist_id
),
listed AS (
    SELECT
        artists.id,
        artists.musicbrainz_id,
        artists.name,
        artists.sort_name,
        artists.followed_at,
        COALESCE(artists.catalogue_summary, '')::text AS catalogue_summary,
        -- Which service the artist is an account on, where they are one. An
        -- uploader on SoundCloud is in the library because a track of theirs is,
        -- and MusicBrainz has never heard of them: there is no discography to
        -- compare the library against and nothing to refresh, so completeness is
        -- not a question that can be asked about them.
        COALESCE(artists.source, '')::text AS source,
        artists.last_refreshed_at,
        COALESCE(latest_refresh.status, 'pending')::text AS refresh_status,
        latest_refresh.error_message AS refresh_error,
        COALESCE(completeness.release_count, 0)::bigint AS release_count,
        COALESCE(completeness.owned_release_count, 0)::bigint AS owned_release_count,
        COALESCE(completeness.track_count, 0)::bigint AS track_count,
        COALESCE(completeness.owned_track_count, 0)::bigint AS owned_track_count,
        COALESCE(in_flight.in_flight_count, 0)::bigint AS in_flight_count,
        COALESCE(awaiting_review.review_count, 0)::bigint AS review_count
    FROM artists
    LEFT JOIN LATERAL (
        SELECT status, error_message
        FROM jobs
        WHERE kind = 'refresh_artist_metadata'
          AND payload->>'artistId' = artists.id::text
        ORDER BY created_at DESC
        LIMIT 1
    ) AS latest_refresh ON true
    LEFT JOIN completeness ON completeness.artist_id = artists.id
    LEFT JOIN in_flight ON in_flight.artist_id = artists.id
    LEFT JOIN awaiting_review ON awaiting_review.artist_id = artists.id
    WHERE (
            sqlc.arg('scope')::text = ''
            OR (sqlc.arg('scope')::text = 'followed' AND artists.followed_at IS NOT NULL)
            OR (sqlc.arg('scope')::text = 'held' AND artists.followed_at IS NULL)
        )
      AND (
            sqlc.arg('query')::text = ''
            -- A search box is not a pattern language, so % and _ have to be
            -- escaped to match themselves, and so does the escape character or it
            -- would eat the escapes put in beside it. One regexp pass over all
            -- three is what keeps that last part from depending on the order.
            OR concat_ws(' ', artists.name, artists.sort_name)
               ILIKE '%' || regexp_replace(sqlc.arg('query')::text, '([\\%_])', '\\\1', 'g') || '%'
               ESCAPE '\'
        )
      -- A genre is picked from the list of genres the catalogue holds, so it is
      -- compared whole rather than searched for. Nothing here is a pattern, so
      -- nothing here needs escaping.
      AND (
            sqlc.arg('genre')::text = ''
            OR sqlc.arg('genre')::text = ANY(COALESCE(artists.genres, '{}'::text[]))
        )
)
SELECT
    listed.*,
    -- An artist wanting attention is one the page can point at something for:
    -- copies waiting for an ear, a transfer under way, a refresh that failed, or
    -- a discography never fetched. The last one belongs here because an artist
    -- nothing has been fetched for cannot be called complete or incomplete — the
    -- catalogue has nothing to compare the library against.
    --
    -- An artist who is an account on another service is outside all of it. The
    -- last rule above would badge every one of them, because they have no
    -- releases and never will; there is nothing to point them at.
    (
        listed.source = ''
        AND (
            listed.review_count > 0
            OR listed.in_flight_count > 0
            OR listed.refresh_status = 'failed'
            OR listed.release_count = 0
        )
    )::boolean AS needs_attention,
    -- Whether a picture of the artist is cached. The index asks for the
    -- pictures that exist and not for every card; a recorded absence is a row
    -- with no image and counts as none.
    EXISTS (
        SELECT 1 FROM artist_images
        WHERE artist_images.artist_id = listed.id
          AND artist_images.image IS NOT NULL
    )::boolean AS has_image
FROM listed
WHERE sqlc.arg('completeness')::text = ''
   OR (sqlc.arg('completeness')::text = 'incomplete'
       AND listed.release_count > 0
       AND listed.owned_release_count < listed.release_count)
   OR (sqlc.arg('completeness')::text = 'complete'
       AND listed.release_count > 0
       AND listed.owned_release_count = listed.release_count)
   OR (sqlc.arg('completeness')::text = 'attention'
       AND listed.source = ''
       AND (listed.review_count > 0
            OR listed.in_flight_count > 0
            OR listed.refresh_status = 'failed'
            OR listed.release_count = 0))
-- Each sort is its own key, so the one that is not asked for is a column of
-- NULLs and decides nothing. An artist with no releases has no completion to
-- sort by and comes first under "least complete", which is where somebody
-- looking for the least complete artists would want to be told that nothing is
-- known about this one at all.
--
-- Releases decide the order and tracks break the tie. Releases first because
-- that is the fraction the card states, and a list ordered by a number it does
-- not show is a list nobody can check. Tracks second because release
-- completeness is nearly all-or-nothing — an artist missing one track of one
-- album and an artist missing everything both own no complete release — and
-- without the second key those two would sort by name, which is no answer to
-- "who is least complete".
--
-- Followed artists first was meant to be the last tiebreaker rather than the
-- rule, and under the name sort it was the rule: every CASE above is NULL for
-- every row then, so `followed_at IS NULL` led the sort. The page drew the
-- seventeen followed artists in alphabetical order, then started at A again
-- with the thirty-two the library merely holds, while the control said "Name".
-- The reader has a scope picker for "only the ones I follow"; the sort is now
-- the sort, and being followed breaks a tie.
--
-- The name is folded before it is compared. This database sorts by byte, so
-- every capital came before every small letter: AZALEA before American
-- Football, COMPUTER DATA before Childish Gambino, and nthng after Yung Lean at
-- the very end of the list. That is not an order anybody reads a list in.
--
-- Sorting is still on sort_name, which is MusicBrainz's filing name — Porter
-- Robinson under R, Kendrick Lamar under L, the convention a record shop uses.
-- That is deliberate and it is the one thing here the reader cannot see from
-- the tile.
--
-- No index answers `lower(sort_name)`; artists_list_order_idx (00020) is on
-- ((followed_at IS NULL), sort_name, id) and no longer matches. At this
-- collection's size the sort is nothing, but a matching expression index is a
-- migration, and migrations are proposed rather than written.
ORDER BY
    CASE WHEN sqlc.arg('sort')::text = 'least-complete'
         THEN listed.owned_release_count::numeric / nullif(listed.release_count, 0)
    END ASC NULLS FIRST,
    CASE WHEN sqlc.arg('sort')::text = 'least-complete'
         THEN listed.owned_track_count::numeric / nullif(listed.track_count, 0)
    END ASC NULLS FIRST,
    CASE WHEN sqlc.arg('sort')::text = 'most-missing'
         THEN listed.release_count - listed.owned_release_count
    END DESC NULLS LAST,
    CASE WHEN sqlc.arg('sort')::text = 'most-missing'
         THEN listed.track_count - listed.owned_track_count
    END DESC NULLS LAST,
    lower(listed.sort_name), (listed.followed_at IS NULL), listed.id
LIMIT NULLIF(sqlc.arg('limit')::int, 0) OFFSET sqlc.arg('offset');

-- name: ArtistListTotals :one
-- What a page of artists is a page of. Counted apart from the page itself: a
-- window function would lose the total exactly when the page is empty, which is
-- when the number matters most.
--
-- Both halves are counted whichever scope is showing, because the page has
-- always said how many artists are followed and how many are held, and a scope
-- that hid one of those numbers would make the collection look smaller than it
-- is. The search does apply: the counts describe the list the user is looking
-- at, not the catalogue in general.
--
-- The four completeness counts are the same, and for the same reason: they label
-- the choices between which the user is picking, so a count that changed when a
-- choice was made would be describing the answer instead of the question. They
-- do honour scope, because scope is the wider question these narrow.
--
-- The refresh count is deliberately not scoped or searched. It answers "is
-- anything still being fetched?", which the page uses to decide whether to keep
-- polling, and an artist refreshing on some other page is still refreshing.
--
-- The completeness and attention CTEs are the ones in ListArtists, repeated
-- rather than shared: a count that disagreed with the list it labels would be
-- worse than the duplication. Change one and change the other.
WITH completeness AS (
    -- Dismissed tracks, fully dismissed releases and unmonitored releases
    -- leave the denominators, exactly as in ListArtists above; see the
    -- comments there.
    SELECT
        albums.artist_id,
        count(*) FILTER (
            WHERE measured.monitored
              AND (album_tracks.track_count = 0 OR measured.countable_count > 0)
        )::bigint AS release_count,
        count(*) FILTER (
            WHERE measured.monitored
              AND measured.countable_count > 0
              AND album_tracks.owned_count = measured.countable_count
        )::bigint AS owned_release_count
    FROM albums
    JOIN artists AS monitoring ON monitoring.id = albums.artist_id
    JOIN LATERAL (
        SELECT
            count(*)::bigint AS track_count,
            count(*) FILTER (WHERE holding.held)::bigint AS owned_count,
            count(*) FILTER (
                WHERE NOT holding.held AND dismissals.id IS NOT NULL
            )::bigint AS dismissed_count
        FROM tracks
        LEFT JOIN track_mappings
            ON track_mappings.track_id = tracks.id
           AND EXISTS (
               SELECT 1 FROM library_files
               WHERE library_files.id = track_mappings.library_file_id
                 AND library_files.missing_at IS NULL
           )
        -- The library can hold a track without mapping it onto this release:
        -- the same recording proven on a file whose identity names it, or on a
        -- file mapped onto another release's track that carries it. Counting
        -- only the mapping would leave a recording that sits on a single, an EP
        -- and a compilation owned on one of the three and missing on the other
        -- two forever, and the acquisition loop already settles those wants
        -- against either proof (OwnedFileForRecording).
        JOIN LATERAL (
            SELECT (track_mappings.id IS NOT NULL OR EXISTS (
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
                JOIN track_mappings AS elsewhere
                    ON elsewhere.track_id = carrying.id
                JOIN library_files ON library_files.id = elsewhere.library_file_id
                WHERE carrying.musicbrainz_recording_id
                        = tracks.musicbrainz_recording_id
                  AND library_files.missing_at IS NULL
            )) AS held
        ) AS holding ON true
        LEFT JOIN acquisition_targets AS dismissals
            ON tracks.musicbrainz_recording_id IS NOT NULL
           AND dismissals.musicbrainz_recording_id = tracks.musicbrainz_recording_id
           AND dismissals.status = 'not_wanted'
        WHERE tracks.album_id = albums.id
    ) AS album_tracks ON true
    JOIN LATERAL (
        SELECT
            CASE monitoring.monitor_level
                WHEN 'everything' THEN true
                WHEN 'main' THEN albums.album_type IN ('album', 'ep', 'single')
                    AND NOT (albums.secondary_types
                        && '{live,compilation,remix,dj-mix,mixtape/street,demo,broadcast}'::text[])
                WHEN 'albums_eps' THEN albums.album_type IN ('album', 'ep')
                    AND NOT (albums.secondary_types
                        && '{live,compilation,remix,dj-mix,mixtape/street,demo,broadcast}'::text[])
                ELSE album_tracks.owned_count > 0
            END AS monitored,
            CASE WHEN monitoring.monitor_level = 'owned'
                 THEN album_tracks.owned_count
                 ELSE album_tracks.track_count - album_tracks.dismissed_count
            END AS countable_count
    ) AS measured ON true
    GROUP BY albums.artist_id
),
in_flight AS (
    SELECT
        COALESCE(albums.artist_id, track_albums.artist_id) AS artist_id,
        count(*)::bigint AS in_flight_count
    FROM downloads
    LEFT JOIN albums ON albums.id = downloads.album_id
    LEFT JOIN tracks ON tracks.id = downloads.track_id
    LEFT JOIN albums AS track_albums ON track_albums.id = tracks.album_id
    WHERE downloads.status IN ('queued', 'searching', 'downloading', 'importing')
      AND COALESCE(albums.artist_id, track_albums.artist_id) IS NOT NULL
    GROUP BY COALESCE(albums.artist_id, track_albums.artist_id)
),
awaiting_review AS (
    SELECT
        albums.artist_id,
        count(DISTINCT held_copies.acquisition_target_id)::bigint AS review_count
    FROM acquisition_target_files AS held_copies
    JOIN acquisition_targets AS targets
        ON targets.id = held_copies.acquisition_target_id
    JOIN albums
        ON albums.id = targets.origin_album_id
        OR (targets.musicbrainz_release_group_id IS NOT NULL
            AND albums.musicbrainz_release_group_id = targets.musicbrainz_release_group_id)
    WHERE held_copies.verdict = 'held'
      -- Only wants still being pursued, the same rule the review queue reads by
      -- (AcquisitionReviewQueue). Accepting one copy leaves its siblings held, so
      -- counting held rows alone would badge an artist for a question the queue
      -- no longer asks and send somebody looking for something that is not there.
      AND targets.status NOT IN ('acquired', 'not_wanted', 'superseded')
    GROUP BY albums.artist_id
),
counted AS (
    SELECT
        artists.followed_at,
        COALESCE(artists.source, '')::text AS source,
        COALESCE(latest_refresh.status, 'pending')::text AS refresh_status,
        COALESCE(completeness.release_count, 0)::bigint AS release_count,
        COALESCE(completeness.owned_release_count, 0)::bigint AS owned_release_count,
        COALESCE(in_flight.in_flight_count, 0)::bigint AS in_flight_count,
        COALESCE(awaiting_review.review_count, 0)::bigint AS review_count
    FROM artists
    LEFT JOIN LATERAL (
        SELECT status
        FROM jobs
        WHERE kind = 'refresh_artist_metadata'
          AND payload->>'artistId' = artists.id::text
        ORDER BY created_at DESC
        LIMIT 1
    ) AS latest_refresh ON true
    LEFT JOIN completeness ON completeness.artist_id = artists.id
    LEFT JOIN in_flight ON in_flight.artist_id = artists.id
    LEFT JOIN awaiting_review ON awaiting_review.artist_id = artists.id
    WHERE (
            sqlc.arg('query')::text = ''
            -- Escaped the same way as the page it counts; see ListArtists.
            OR concat_ws(' ', artists.name, artists.sort_name)
               ILIKE '%' || regexp_replace(sqlc.arg('query')::text, '([\\%_])', '\\\1', 'g') || '%'
               ESCAPE '\'
        )
      -- The genre narrows the counts too, for the reason the search does: the
      -- numbers describe the list the reader is looking at.
      AND (
            sqlc.arg('genre')::text = ''
            OR sqlc.arg('genre')::text = ANY(COALESCE(artists.genres, '{}'::text[]))
        )
),
scoped AS (
    SELECT * FROM counted
    WHERE sqlc.arg('scope')::text = ''
       OR (sqlc.arg('scope')::text = 'followed' AND followed_at IS NOT NULL)
       OR (sqlc.arg('scope')::text = 'held' AND followed_at IS NULL)
)
SELECT
    (SELECT count(*) FILTER (WHERE followed_at IS NOT NULL) FROM counted)::bigint
        AS followed_count,
    (SELECT count(*) FILTER (WHERE followed_at IS NULL) FROM counted)::bigint
        AS held_count,
    (SELECT count(*) FROM scoped)::bigint AS all_count,
    (SELECT count(*) FROM scoped
      WHERE release_count > 0 AND owned_release_count < release_count)::bigint
        AS incomplete_count,
    (SELECT count(*) FROM scoped
      WHERE release_count > 0 AND owned_release_count = release_count)::bigint
        AS complete_count,
    (SELECT count(*) FROM scoped
      WHERE source = ''
        AND (review_count > 0 OR in_flight_count > 0
             OR refresh_status = 'failed' OR release_count = 0))::bigint
        AS attention_count,
    -- What the header states above the grid: how much of the shown list is
    -- owned. Summed over the scope rather than the page, because a page is an
    -- accident of where the user stopped scrolling.
    (SELECT COALESCE(sum(release_count), 0) FROM scoped)::bigint AS release_total,
    (SELECT COALESCE(sum(owned_release_count), 0) FROM scoped)::bigint
        AS owned_release_total,
    (
        SELECT count(*)
        FROM jobs
        WHERE kind = 'refresh_artist_metadata'
          AND status IN ('queued', 'running')
    )::bigint AS refreshing_count;

-- name: CreateArtist :one
-- Following an artist Schall is already holding is a promotion, not a
-- conflict: the artist is in the catalogue because the library contains their
-- music, and the user is now asking for their releases to be kept complete.
-- Promoting queues the full artist refresh, because until now only the one
-- release behind an owned file had been fetched.
--
-- An artist who is already followed returns no row, and the caller reports the
-- conflict that genuinely is one.
WITH followed_artist AS (
    INSERT INTO artists (musicbrainz_id, name, sort_name, followed_at)
    VALUES (sqlc.arg('musicbrainz_id'), sqlc.arg('name'), sqlc.arg('sort_name'), now())
    ON CONFLICT (musicbrainz_id) DO UPDATE
    SET followed_at = now(),
        catalogue_summary = NULL,
        name = EXCLUDED.name,
        sort_name = EXCLUDED.sort_name,
        updated_at = now()
    WHERE artists.followed_at IS NULL
    RETURNING id, musicbrainz_id, name, sort_name, followed_at, last_refreshed_at
),
queued_job AS (
    INSERT INTO jobs (kind, payload)
    SELECT 'refresh_artist_metadata', jsonb_build_object('artistId', id)
    FROM followed_artist
)
SELECT id, musicbrainz_id, name, sort_name, followed_at, last_refreshed_at
FROM followed_artist;

-- name: GetArtist :one
SELECT
    artists.id,
    artists.musicbrainz_id,
    artists.name,
    artists.sort_name,
    artists.followed_at,
    artists.monitor_level,
    -- When the standing "want what is missing" was set, and NULL when it is
    -- off. The page shows it as a state rather than a date; the date is here
    -- because a want made under it should be traceable to when it was asked
    -- for.
    artists.want_missing_since,
    COALESCE(artists.catalogue_summary, '')::text AS catalogue_summary,
    -- See ListArtists: an artist who is an account on another service has no
    -- discography and no completeness.
    COALESCE(artists.source, '')::text AS source,
    COALESCE(artists.external_id, '')::text AS external_id,
    artists.last_refreshed_at,
    -- What MusicBrainz's community voted this artist is, most voted first. An
    -- artist nobody has asked about yet and an artist nobody voted for both
    -- come back as an empty list here: the page shows no chips either way, and
    -- the difference only matters to the sweep that does the asking.
    COALESCE(artists.genres, '{}')::text[] AS genres,
    -- The Wikipedia summary and the article it came from. Both empty until the
    -- sweep has asked, and both stay empty for an artist no encyclopaedia has
    -- heard of.
    COALESCE(artists.biography, '')::text AS biography,
    COALESCE(artists.biography_source_url, '')::text AS biography_source_url,
    count(albums.id) AS album_count,
    COALESCE(latest_refresh.status, 'pending') AS refresh_status,
    latest_refresh.error_message AS refresh_error
FROM artists
LEFT JOIN albums ON albums.artist_id = artists.id
LEFT JOIN LATERAL (
    SELECT status, error_message
    FROM jobs
    WHERE kind = 'refresh_artist_metadata'
      AND payload->>'artistId' = artists.id::text
    ORDER BY created_at DESC
    LIMIT 1
) AS latest_refresh ON true
WHERE artists.id = sqlc.arg('id')
GROUP BY artists.id, latest_refresh.status, latest_refresh.error_message;

-- name: FollowArtistByID :one
-- Follows an artist Schall is already holding, from the artist's own page,
-- where the user is looking at the music that put them in the catalogue rather
-- than at a MusicBrainz search result.
WITH followed_artist AS (
    UPDATE artists
    SET followed_at = now(), catalogue_summary = NULL, updated_at = now()
    WHERE artists.id = sqlc.arg('artist_id') AND artists.followed_at IS NULL
    RETURNING artists.id, artists.musicbrainz_id, artists.name, artists.sort_name,
              artists.followed_at, artists.last_refreshed_at
),
queued_job AS (
    INSERT INTO jobs (kind, payload)
    SELECT 'refresh_artist_metadata', jsonb_build_object('artistId', followed_artist.id)
    FROM followed_artist
    WHERE followed_artist.musicbrainz_id IS NOT NULL
)
SELECT id, musicbrainz_id, name, sort_name, followed_at, last_refreshed_at
FROM followed_artist;

-- name: UnfollowArtist :one
-- Unfollowing is demotion, not deletion.
--
-- Following says "keep this artist's releases complete". Unfollowing withdraws
-- exactly that intent, and nothing else. It must not withdraw the separate fact
-- that the library contains their music, because `albums` and `tracks` cascade
-- from `artists`: deleting a followed artist would take the mappings for owned
-- files with it, and a user who unfollowed an artist to stop tracking releases
-- would silently lose the record of the music they already have.
--
-- So the question unfollowing asks is not "did you want this artist?" but "does
-- the library still reach them?". Three things count as reach, and each of them
-- is something the cascade would destroy:
--
--   * a track mapping, present or missing. A missing file is a disconnected
--     drive, not a decision, and a manual mapping is permanent by rule.
--   * a proven external identity naming one of their recordings. The file is
--     resolved and waiting for matching to run; the catalogue is what is late,
--     not the evidence.
--   * an open download request for one of their albums. `download_requests`
--     cascades too, so releasing the artist mid-acquisition would take the
--     request, its transfers, and its import history with it. Asking for the
--     music is a statement that it is wanted, and once it lands the owned files
--     make the hold genuine on their own.
--
-- Reached, the artist is demoted to held and keeps everything: every release,
-- including the ones the library does not reach. Pruning those would discard
-- manually selected editions, which are permanent by the same rule as manual
-- mappings, and it would buy nothing — missing counts are already scoped to
-- followed artists, so a release nobody follows reports nothing as missing.
-- Keeping them also makes re-following free of a MusicBrainz refetch.
--
-- Unreached, there is nothing to keep and no honest sentence to write about why
-- the artist is still listed, so the row is deleted. That delete is safe
-- precisely because the reach checks found nothing for the cascade to take.
WITH target AS (
    SELECT artists.id, artists.musicbrainz_id, artists.name, artists.sort_name
    FROM artists
    WHERE artists.id = sqlc.arg('artist_id') AND artists.followed_at IS NOT NULL
),
reach AS (
    SELECT
        (
            SELECT count(*)
            FROM track_mappings
            JOIN tracks ON tracks.id = track_mappings.track_id
            JOIN albums ON albums.id = tracks.album_id
            WHERE albums.artist_id = target.id
        )::bigint AS mapped_track_count,
        EXISTS (
            SELECT 1
            FROM library_file_identities
            JOIN tracks
              ON tracks.musicbrainz_recording_id
               = library_file_identities.musicbrainz_recording_id
            JOIN albums ON albums.id = tracks.album_id
            WHERE albums.artist_id = target.id
              AND library_file_identities.kind = 'external'
        ) AS has_identified_file,
        EXISTS (
            SELECT 1
            FROM download_requests
            JOIN albums ON albums.id = download_requests.album_id
            WHERE albums.artist_id = target.id
              AND download_requests.status = 'requested'
        ) AS has_open_download
    FROM target
),
decision AS (
    -- One sentence saying why an artist nobody follows is still listed. The
    -- reasons are ordered by how directly the user can see them: owned tracks
    -- first, then a file that is resolved but not yet matched, then music that
    -- has not arrived yet.
    SELECT
        (
            reach.mapped_track_count > 0
            OR reach.has_identified_file
            OR reach.has_open_download
        ) AS held,
        CASE
            WHEN reach.mapped_track_count = 1 THEN format(
                'Unfollowed on %s. Kept because your library holds 1 of their tracks.',
                to_char(now(), 'FMDD Mon YYYY'))
            WHEN reach.mapped_track_count > 1 THEN format(
                'Unfollowed on %s. Kept because your library holds %s of their tracks.',
                to_char(now(), 'FMDD Mon YYYY'), reach.mapped_track_count)
            WHEN reach.has_identified_file THEN format(
                'Unfollowed on %s. Kept because a file in your library is identified as their music.',
                to_char(now(), 'FMDD Mon YYYY'))
            WHEN reach.has_open_download THEN format(
                'Unfollowed on %s. Kept while a download of their music is still in progress.',
                to_char(now(), 'FMDD Mon YYYY'))
            ELSE ''
        END AS catalogue_summary
    FROM reach
),
-- A demoted artist is no longer tracked for completeness, so the refresh that
-- would fetch their full discography is no longer wanted. A released artist is
-- about to stop existing. Neither should leave work queued behind them; a
-- refresh already running is left alone rather than interrupted mid-flight.
cancelled_artist_jobs AS (
    UPDATE jobs
    SET status = 'cancelled', completed_at = now(), updated_at = now()
    WHERE kind = 'refresh_artist_metadata'
      AND status = 'queued'
      AND payload->>'artistId' = (SELECT target.id::text FROM target)
),
-- Album refreshes are cancelled only for a released artist, whose albums are
-- about to be cascaded away, leaving the job with nothing to refresh. A demoted
-- artist keeps their releases, so work already queued against them still has a
-- subject and still produces the tracks an owned file may need.
cancelled_album_jobs AS (
    UPDATE jobs
    SET status = 'cancelled', completed_at = now(), updated_at = now()
    WHERE kind = 'refresh_album_metadata'
      AND status = 'queued'
      AND NOT (SELECT held FROM decision)
      AND payload->>'albumId' IN (
          SELECT albums.id::text FROM albums
          WHERE albums.artist_id = (SELECT target.id FROM target)
      )
),
held_artist AS (
    UPDATE artists
    SET followed_at = NULL,
        catalogue_summary = (SELECT catalogue_summary FROM decision),
        updated_at = now()
    WHERE id = (SELECT target.id FROM target) AND (SELECT held FROM decision)
    RETURNING id
),
released_artist AS (
    DELETE FROM artists
    WHERE id = (SELECT target.id FROM target) AND NOT (SELECT held FROM decision)
    RETURNING id
)
SELECT
    target.id,
    target.musicbrainz_id,
    target.name,
    target.sort_name,
    decision.held,
    decision.catalogue_summary,
    reach.mapped_track_count
FROM target, decision, reach;

-- The monitor level decides what counts as missing and what "want everything"
-- wants — never what is shown or fetched. Changing it deletes nothing:
-- existing wants stay (they were asked for), owned music stays counted, and
-- per-track not-wanted decisions stay permanent underneath it.
-- name: SetArtistMonitorLevel :one
UPDATE artists
SET monitor_level = sqlc.arg('monitor_level'), updated_at = now()
WHERE id = sqlc.arg('artist_id')
RETURNING id, monitor_level;

-- The standing "want what is missing" on an artist. Set, every missing track of
-- every monitored release is wanted, now and as tracklists arrive; the artist
-- refresh saves the release groups and queues one release refresh each, and
-- those land one at a time behind MusicBrainz's rate limit, so a single press
-- reaches only the releases whose tracks have already arrived.
--
-- Setting it twice keeps the first time, because the standing want has not
-- stopped and started; it is the same one. Clearing it stops future wanting and
-- touches no want that exists, for the reason changing the monitor level does
-- not: those were asked for.
-- name: SetArtistWantMissing :one
UPDATE artists
SET want_missing_since = CASE
        WHEN sqlc.arg('wanted')::boolean THEN COALESCE(want_missing_since, now())
        ELSE NULL
    END,
    updated_at = now()
WHERE id = sqlc.arg('artist_id')
RETURNING id, want_missing_since;

-- Every track of one release that a standing want reaches, with the release
-- context each would be wanted from. It is ArtistTracksForWanting narrowed to
-- the release whose tracklist has just arrived, and it answers with no rows at
-- all unless the artist has a standing want and their monitor level counts this
-- release — so the caller has one thing to walk and no rule of its own to
-- apply.
--
-- The monitor level is read by the CASE ArtistTracksForWanting reads it by,
-- which is what makes the two halves of the standing want agree: the press on
-- the artist's page and the tracklist arriving an hour later want the same
-- releases. At 'owned' both want nothing, because there is nothing there that
-- is not already held.
-- name: StandingWantTracks :many
SELECT
    albums.id AS album_id,
    albums.title AS album_title,
    albums.musicbrainz_release_group_id,
    artists.name AS artist_name,
    tracks.title,
    tracks.duration_ms,
    tracks.isrc,
    tracks.musicbrainz_recording_id,
    (track_mappings.id IS NOT NULL)::boolean AS owned,
    (held.library_file_id IS NOT NULL)::boolean AS held_elsewhere
FROM albums
JOIN artists ON artists.id = albums.artist_id
JOIN tracks ON tracks.album_id = albums.id
LEFT JOIN track_mappings
    ON track_mappings.track_id = tracks.id
   AND EXISTS (
       SELECT 1 FROM library_files
       WHERE library_files.id = track_mappings.library_file_id
         AND library_files.missing_at IS NULL
   )
-- The same recording proven on a file this release never mapped onto; see
-- ArtistTracksForWanting, which reads it the same way and for the same reason.
LEFT JOIN LATERAL (
    SELECT library_files.id AS library_file_id
    FROM library_file_identities
    JOIN library_files
        ON library_files.id = library_file_identities.library_file_id
    WHERE library_file_identities.musicbrainz_recording_id
            = tracks.musicbrainz_recording_id
      AND library_files.missing_at IS NULL
    UNION ALL
    SELECT library_files.id
    FROM tracks AS carrying
    JOIN track_mappings ON track_mappings.track_id = carrying.id
    JOIN library_files ON library_files.id = track_mappings.library_file_id
    WHERE carrying.musicbrainz_recording_id = tracks.musicbrainz_recording_id
      AND library_files.missing_at IS NULL
    LIMIT 1
) held ON true
WHERE albums.id = $1
  AND artists.want_missing_since IS NOT NULL
  AND CASE artists.monitor_level
          WHEN 'everything' THEN true
          WHEN 'main' THEN albums.album_type IN ('album', 'ep', 'single')
              AND NOT (albums.secondary_types
                  && '{live,compilation,remix,dj-mix,mixtape/street,demo,broadcast}'::text[])
          WHEN 'albums_eps' THEN albums.album_type IN ('album', 'ep')
              AND NOT (albums.secondary_types
                  && '{live,compilation,remix,dj-mix,mixtape/street,demo,broadcast}'::text[])
          ELSE false
      END
ORDER BY tracks.disc_number, tracks.track_number NULLS LAST, tracks.id;

-- name: QueueArtistRefresh :one
WITH existing_artist AS (
    SELECT artists.id
    FROM artists
    WHERE artists.id = sqlc.arg('artist_id')
),
active_job AS (
    SELECT jobs.id, jobs.status, jobs.created_at
    FROM jobs
    JOIN existing_artist ON jobs.payload->>'artistId' = existing_artist.id::text
    WHERE jobs.kind = 'refresh_artist_metadata'
      AND jobs.status IN ('queued', 'running')
    ORDER BY jobs.created_at DESC
    LIMIT 1
),
created_job AS (
    INSERT INTO jobs (kind, payload)
    SELECT 'refresh_artist_metadata', jsonb_build_object('artistId', id)
    FROM existing_artist
    WHERE NOT EXISTS (SELECT 1 FROM active_job)
    RETURNING id, status, created_at
)
SELECT id, status, created_at FROM active_job
UNION ALL
SELECT id, status, created_at FROM created_job
LIMIT 1;

-- The cached picture of an artist. Display and never evidence.
-- name: ArtistImage :one
SELECT image, content_type, source
FROM artist_images
WHERE artist_id = $1;

-- name: SaveArtistImage :exec
INSERT INTO artist_images (artist_id, image, content_type, source, fetched_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (artist_id) DO UPDATE
SET image = EXCLUDED.image,
    content_type = EXCLUDED.content_type,
    source = EXCLUDED.source,
    fetched_at = EXCLUDED.fetched_at;

-- Artists there is a picture to look for. Only those MusicBrainz knows, because
-- the picture is keyed on that identifier and there is nothing to ask about
-- without it.
--
-- That is everyone nobody has looked for a picture of yet, and everyone whose
-- row says nobody had one but says it about a chain that has since changed. A
-- row with no image is the answer "asked, and there is none", which answers the
-- question that was asked at the time and stops answering it when the chain
-- doing the asking gains a rung — an artist written off before that would
-- otherwise be skipped forever and the new rung would picture nobody. The
-- caller passes the day the chain last changed (coverart.chainChanged), so
-- adding a rung is a date to bump rather than a migration deleting absences by
-- hand, which is what adding the last one took.
--
-- Those never asked about come first, so a collection's worth of old absences
-- being asked again cannot hold up an artist nobody has ever looked for at all;
-- among the rest the stalest goes first. A pass records what it finds either
-- way, absence included, which writes a fetched_at on the near side of the date
-- above — so a batch of re-asks is answered once and not seen again until the
-- chain changes again.
-- name: ArtistsMissingImage :many
SELECT artists.id, artists.musicbrainz_id
FROM artists
LEFT JOIN artist_images ON artist_images.artist_id = artists.id
WHERE artists.musicbrainz_id IS NOT NULL
  AND (
    artist_images.artist_id IS NULL
    OR (artist_images.image IS NULL AND artist_images.fetched_at < sqlc.arg(chain_changed))
  )
ORDER BY artist_images.fetched_at ASC NULLS FIRST, artists.created_at
LIMIT sqlc.arg(batch);

-- name: ArtistsMissingBiography :many
-- The artists nobody has asked an encyclopaedia about yet.
--
-- Only artists with a MusicBrainz identifier, because the chain starts there:
-- MusicBrainz says which Wikidata entity this is, the entity says which
-- Wikipedia article, and the article is where the words come from. Nobody is
-- ever asked about an artist by name.
--
-- biography_fetched_at is set whatever the answer was, absence included, so an
-- artist asked about once is never in this list again. The oldest question
-- first, then the oldest artist, so a run through the catalogue is the same
-- run every time.
SELECT artists.id, artists.musicbrainz_id
FROM artists
WHERE artists.musicbrainz_id IS NOT NULL
  AND artists.biography_fetched_at IS NULL
ORDER BY artists.created_at
LIMIT sqlc.arg(batch);

-- name: SaveArtistBiography :exec
-- Records what the encyclopaedia said about one artist, including that it had
-- never heard of them: an empty biography with a time beside it is the answer
-- "asked, and there is no article", which is what stops the sweep asking again.
-- An outage calls nothing here at all.
UPDATE artists
SET biography = nullif(sqlc.arg(biography)::text, ''),
    biography_source_url = nullif(sqlc.arg(source_url)::text, ''),
    biography_fetched_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(artist_id);

-- name: BackfillGenres :one
-- Asks for the genres nobody has asked MusicBrainz about yet.
--
-- It asks MusicBrainz nothing itself. It queues the ordinary refreshes, which
-- are the passes that already visit the entity and read the community's votes
-- off the responses they were fetching anyway. So a catalogue that predates
-- genres fills at the refresh's own pace, one request a second, with no request
-- that would not have been made by somebody pressing Refresh.
--
-- An artist without genres is asked for by the artist refresh. An album without
-- them is asked for by its own release refresh, and that is the part that used
-- to be wrong: the artist refresh only writes the release groups MusicBrainz
-- credits to that artist, so an album credited to somebody else — a remix, a
-- collaboration filed under the other name — was never reached by it and kept
-- its NULL forever. The sweep read that NULL as "the artist still needs asking",
-- queued the artist again ten minutes later, and each of those refreshes queued
-- a release refresh for all 185 of the artist's release groups. Two such albums
-- produced 1,494 jobs an hour, indefinitely.
--
-- An entity already queued, or being refreshed now, is left alone: that pass
-- will genre it, and a second job would only ask MusicBrainz the same question
-- twice.
--
-- One whose refresh failed within the last day is left alone too, and for a
-- different reason: a batch is ordered oldest-first, so an entity whose
-- MusicBrainz identifier no longer resolves would otherwise be re-queued, fail,
-- and be re-queued again every pass forever, filling the batch with the same
-- permanent failure and starving everything behind it. A day is long enough
-- that a transient failure gets retried the same day, and short enough that a
-- real answer is not put off for good.
--
-- A pass queues at most one batch of each kind. Reports how many were waiting
-- and how many were queued, so the sweep knows whether to come back.
WITH waiting_artists AS (
    SELECT artists.id
    FROM artists
    LEFT JOIN LATERAL (
        SELECT jobs.completed_at
        FROM jobs
        WHERE jobs.kind = 'refresh_artist_metadata'
          AND jobs.payload->>'artistId' = artists.id::text
          AND jobs.status = 'failed'
        ORDER BY jobs.completed_at DESC
        LIMIT 1
    ) last_failure ON true
    WHERE artists.musicbrainz_id IS NOT NULL
      AND artists.genres IS NULL
      AND (
        last_failure.completed_at IS NULL
        OR last_failure.completed_at < now() - interval '1 day'
      )
    ORDER BY artists.created_at
    LIMIT sqlc.arg(batch)
),
waiting_albums AS (
    SELECT albums.id
    FROM albums
    LEFT JOIN LATERAL (
        SELECT jobs.completed_at
        FROM jobs
        WHERE jobs.kind = 'refresh_album_metadata'
          AND jobs.payload->>'albumId' = albums.id::text
          AND jobs.status = 'failed'
        ORDER BY jobs.completed_at DESC
        LIMIT 1
    ) last_failure ON true
    WHERE albums.musicbrainz_release_group_id IS NOT NULL
      AND albums.genres IS NULL
      AND (
        last_failure.completed_at IS NULL
        OR last_failure.completed_at < now() - interval '1 day'
      )
    ORDER BY albums.created_at
    LIMIT sqlc.arg(batch)
),
queued_artists AS (
    INSERT INTO jobs (kind, payload)
    SELECT 'refresh_artist_metadata', jsonb_build_object('artistId', waiting_artists.id)
    FROM waiting_artists
    WHERE NOT EXISTS (
        SELECT 1 FROM jobs
        WHERE jobs.kind = 'refresh_artist_metadata'
          AND jobs.payload->>'artistId' = waiting_artists.id::text
          AND jobs.status IN ('queued', 'running')
    )
    RETURNING 1 AS queued_one
),
queued_albums AS (
    INSERT INTO jobs (kind, payload)
    SELECT 'refresh_album_metadata', jsonb_build_object('albumId', waiting_albums.id::text)
    FROM waiting_albums
    ON CONFLICT (kind, (payload->>'albumId'))
        WHERE kind = 'refresh_album_metadata' AND status IN ('queued', 'running')
    DO NOTHING
    RETURNING 1 AS queued_one
)
SELECT
    ((SELECT count(*) FROM waiting_artists)
        + (SELECT count(*) FROM waiting_albums))::bigint AS waiting_count,
    ((SELECT count(*) FROM queued_artists)
        + (SELECT count(*) FROM queued_albums))::bigint AS queued_count;

-- name: CatalogueGenres :many
-- Every genre the catalogue holds for an artist, in alphabetical order. It is
-- the list the artists browser offers to filter by, so it names only genres
-- that would find somebody.
SELECT DISTINCT unnest(artists.genres)::text AS genre
FROM artists
WHERE artists.genres IS NOT NULL
ORDER BY genre;

-- Every track of every release this artist has, with the release context each
-- one would be wanted from. It is the discography-wide version of what
-- ListAlbumTracks answers for one release, and it is one query rather than two
-- per release because a discography is hundreds of them.
--
-- Owned and held elsewhere are the two ways the library can already hold a
-- track, and either one is a reason not to want it: a mapping onto a present
-- file, or a present file proven to be that recording without being mapped onto
-- this release at all. Both are read because the acquisition loop settles a want
-- against either (OwnedFileForRecording), so wanting on the mapping alone makes
-- wants the loop answers with "In your library." a minute later.
--
-- Only monitored releases are walked: "want everything missing" follows the
-- artist's monitor level, always, and at 'owned' there is nothing new to want
-- at all. An unmonitored release can still be wanted by hand from its own
-- page — a manual want always wins — this is only what the discography-wide
-- press reaches.
-- name: ArtistTracksForWanting :many
SELECT
    albums.id AS album_id,
    albums.title AS album_title,
    albums.musicbrainz_release_group_id,
    artists.name AS artist_name,
    tracks.title,
    tracks.duration_ms,
    tracks.isrc,
    tracks.musicbrainz_recording_id,
    (track_mappings.id IS NOT NULL)::boolean AS owned,
    (held.library_file_id IS NOT NULL)::boolean AS held_elsewhere
FROM albums
JOIN artists ON artists.id = albums.artist_id
JOIN tracks ON tracks.album_id = albums.id
LEFT JOIN track_mappings
    ON track_mappings.track_id = tracks.id
   AND EXISTS (
       SELECT 1 FROM library_files
       WHERE library_files.id = track_mappings.library_file_id
         AND library_files.missing_at IS NULL
   )
-- The same recording proven on a file this release never mapped onto: a
-- resolved identity naming it, or a mapping onto another release's track that
-- carries it. Aliasing the inner tracks table leaves the outer one's recording
-- id in scope, which is what both halves compare against.
LEFT JOIN LATERAL (
    SELECT library_files.id AS library_file_id
    FROM library_file_identities
    JOIN library_files
        ON library_files.id = library_file_identities.library_file_id
    WHERE library_file_identities.musicbrainz_recording_id
            = tracks.musicbrainz_recording_id
      AND library_files.missing_at IS NULL
    UNION ALL
    SELECT library_files.id
    FROM tracks AS carrying
    JOIN track_mappings ON track_mappings.track_id = carrying.id
    JOIN library_files ON library_files.id = track_mappings.library_file_id
    WHERE carrying.musicbrainz_recording_id = tracks.musicbrainz_recording_id
      AND library_files.missing_at IS NULL
    LIMIT 1
) held ON true
WHERE albums.artist_id = $1
  AND CASE artists.monitor_level
          WHEN 'everything' THEN true
          WHEN 'main' THEN albums.album_type IN ('album', 'ep', 'single')
              AND NOT (albums.secondary_types
                  && '{live,compilation,remix,dj-mix,mixtape/street,demo,broadcast}'::text[])
          WHEN 'albums_eps' THEN albums.album_type IN ('album', 'ep')
              AND NOT (albums.secondary_types
                  && '{live,compilation,remix,dj-mix,mixtape/street,demo,broadcast}'::text[])
          ELSE false
      END
ORDER BY albums.release_date NULLS LAST, albums.id,
         tracks.disc_number, tracks.track_number NULLS LAST, tracks.id;

-- Followed artists whose catalogue nobody has looked at lately, oldest first.
-- This is what the follow feed's clock asks for: a release published after the
-- follow only reaches the catalogue if somebody asks MusicBrainz again, and the
-- whole point of following is that nobody has to.
--
-- Only artists MusicBrainz knows, because the refresh is keyed on that
-- identifier and there is nothing to ask about without it. A catalogue-only
-- artist is skipped entirely: holding an artist is a statement of fact about
-- the library, not a request to keep them complete.
-- name: StaleFollowedArtists :many
SELECT artists.id
FROM artists
WHERE artists.followed_at IS NOT NULL
  AND artists.musicbrainz_id IS NOT NULL
  AND (artists.last_refreshed_at IS NULL OR artists.last_refreshed_at < sqlc.arg('refreshed_before'))
ORDER BY artists.last_refreshed_at NULLS FIRST, artists.id
LIMIT sqlc.arg('row_limit');

-- What the follow feed wants: tracks on releases published after the follow, by
-- artists the user follows, that the library does not hold and nobody has
-- already asked for.
--
-- Published after the follow is the whole rule. Following an artist says "keep
-- them complete from here"; it is not a press of "want the discography", which
-- exists separately and reaches backwards on purpose. A release with no date is
-- not wanted either — an absent date is silence, and reading it as "new" would
-- make following an artist drag in every undated thing MusicBrainz holds
-- against them.
--
-- The monitor level filters this exactly as it filters the discography press,
-- and at 'owned' the feed wants nothing at all. An unmonitored release is still
-- browsable and can still be wanted by hand; this is only what arrives without
-- anybody pressing anything.
--
-- A track with no recording is skipped rather than wanted. Creation is
-- idempotent on the recording ID, so a want without one is always new and every
-- pass would make another.
-- name: FollowFeedTracks :many
SELECT
    albums.id AS album_id,
    albums.title AS album_title,
    albums.musicbrainz_release_group_id,
    artists.name AS artist_name,
    tracks.title,
    tracks.duration_ms,
    tracks.isrc,
    tracks.musicbrainz_recording_id
FROM albums
JOIN artists ON artists.id = albums.artist_id
JOIN tracks ON tracks.album_id = albums.id
WHERE artists.followed_at IS NOT NULL
  AND albums.release_date IS NOT NULL
  AND albums.release_date > artists.followed_at::date
  AND tracks.musicbrainz_recording_id IS NOT NULL
  AND CASE artists.monitor_level
          WHEN 'everything' THEN true
          WHEN 'main' THEN albums.album_type IN ('album', 'ep', 'single')
              AND NOT (albums.secondary_types
                  && '{live,compilation,remix,dj-mix,mixtape/street,demo,broadcast}'::text[])
          WHEN 'albums_eps' THEN albums.album_type IN ('album', 'ep')
              AND NOT (albums.secondary_types
                  && '{live,compilation,remix,dj-mix,mixtape/street,demo,broadcast}'::text[])
          ELSE false
      END
  -- Either way the library can already hold it. A mapping onto a present file —
  -- including one onto this very track, which is the same query with carrying
  -- being tracks itself — or a present file proven to be that recording and
  -- never mapped onto this release at all. Both are read because the loop
  -- settles a want against either, so wanting on the mapping alone makes wants
  -- the loop answers with "In your library." a minute later.
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
  -- A recording already asked for is not asked for again, and that is also what
  -- keeps a refused one refused: a not_wanted target holds its recording, so the
  -- feed finds it and passes over rather than re-opening the decision. Only a
  -- superseded row is exempt, because the survivor it points at carries the same
  -- recording and is matched here instead.
  AND NOT EXISTS (
      SELECT 1
      FROM acquisition_targets
      WHERE acquisition_targets.musicbrainz_recording_id = tracks.musicbrainz_recording_id
        AND acquisition_targets.status <> 'superseded'
  )
ORDER BY albums.release_date, albums.id,
         tracks.disc_number, tracks.track_number NULLS LAST, tracks.id
LIMIT sqlc.arg('row_limit');
