-- The releases list is not here. It is scoped, searched, filtered, ordered and
-- paged by what the browser was asked for, and the ORDER BY has to stay an
-- expression an index can walk, which is a clause sqlc cannot build. It lives
-- in internal/db/catalogue.go, next to ListLibraryFiles for the same reason.

-- name: GetAlbum :one
SELECT
    albums.id,
    albums.artist_id,
    artists.name AS artist_name,
    (artists.followed_at IS NOT NULL)::boolean AS artist_followed,
    albums.musicbrainz_release_group_id,
    albums.title,
    albums.release_date,
    albums.album_type,
    -- What MusicBrainz's community voted this release group is, most voted
    -- first. Empty both when nobody has asked yet and when nobody voted, which
    -- the page shows the same way: no chips.
    COALESCE(albums.genres, '{}')::text[] AS genres,
    COALESCE(metadata_sources.metadata->>'first-release-date', '')::text AS first_release_date,
    release_editions.musicbrainz_release_id,
    release_editions.status AS edition_status,
    release_editions.country AS edition_country,
    release_editions.barcode,
    release_editions.release_date AS edition_release_date,
    COALESCE(release_editions.media_count, 0)::integer AS media_count,
    COALESCE(release_editions.track_count, 0)::integer AS track_count,
    COALESCE(release_editions.selected_automatically, true)::boolean AS selected_automatically,
    COALESCE(release_editions.selection_reason, '')::text AS selection_reason,
    -- Which archive the cover came from, so an interface can say when a picture
    -- was found by name rather than by an identifier. Empty until one is cached.
    COALESCE(release_cover_art.source, '')::text AS cover_source,
    -- Whether the tracks of this release have been fetched from MusicBrainz yet.
    -- 'completed' the moment an edition was chosen, because the edition is where
    -- the tracks come from; 'pending' when no job has been made for it at all.
    -- The releases list reports the same five words for a row, and a release
    -- page that has no tracks needs them for the same reason: a release nobody
    -- is fetching tracks for will never get any, and one that is being fetched
    -- will, so only the second is worth waiting on.
    CASE
        WHEN release_editions.id IS NOT NULL THEN 'completed'
        WHEN refresh.running THEN 'running'
        WHEN refresh.queued THEN 'queued'
        WHEN refresh.failed THEN 'failed'
        ELSE 'pending'
    END::text AS track_refresh_status
FROM albums
JOIN artists ON artists.id = albums.artist_id
LEFT JOIN release_cover_art ON release_cover_art.album_id = albums.id
LEFT JOIN metadata_sources
    ON metadata_sources.entity_type = 'album'
   AND metadata_sources.entity_id = albums.id
   AND metadata_sources.provider = 'musicbrainz'
LEFT JOIN release_editions ON release_editions.album_id = albums.id
LEFT JOIN LATERAL (
    -- The refresh jobs for this release, asked as three questions rather than
    -- read as rows: a release refreshed more than once has one job row per
    -- attempt, and what is wanted is the state of the set. The releases list
    -- asks the same three with bool_or inside the grouping it already has; a
    -- single release has no grouping to put them in, so they are asked beside
    -- the row instead.
    SELECT bool_or(jobs.status = 'running') AS running,
           bool_or(jobs.status = 'queued') AS queued,
           bool_or(jobs.status = 'failed') AS failed
    FROM jobs
    WHERE jobs.kind = 'refresh_album_metadata'
      AND jobs.payload->>'albumId' = albums.id::text
) refresh ON true
WHERE albums.id = $1;

-- Owned is the mapping this release has onto a file, which is what a row's match
-- details describe. Held elsewhere is the other way the library can already hold
-- the music, and it is why a release the library can play in full is not
-- reported as missing a track: the same recording sitting on a single, an EP and
-- a compilation gets one track row apiece, a file maps onto exactly one of them,
-- and the other two would read as gaps forever. Both are proof the acquisition
-- loop settles a want against (OwnedFileForRecording), so counting and wanting
-- read them together — a want it would settle on sight is a want nobody should
-- have made.
-- name: ListAlbumTracks :many
SELECT
    tracks.id,
    tracks.musicbrainz_recording_id,
    tracks.title,
    tracks.disc_number,
    tracks.track_number,
    tracks.duration_ms,
    tracks.isrc,
    (track_mappings.id IS NOT NULL)::boolean AS owned,
    (held.library_file_id IS NOT NULL)::boolean AS held_elsewhere,
    track_mappings.method AS match_method,
    track_mappings.confidence AS match_confidence,
    coalesce(track_mappings.is_manual, false)::boolean AS match_manual,
    track_mappings.library_file_id,
    -- The want that governs this track, so the page can offer the toggle that
    -- fits: none, being pursued, or dismissed. At most one target exists per
    -- recording outside 'superseded' (acquisition_targets_recording_idx), so
    -- this join cannot fan a track out.
    want.id AS want_id,
    want.status AS want_status
FROM tracks
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
LEFT JOIN acquisition_targets AS want
    ON tracks.musicbrainz_recording_id IS NOT NULL
   AND want.musicbrainz_recording_id = tracks.musicbrainz_recording_id
   AND want.status <> 'superseded'
WHERE tracks.album_id = $1
ORDER BY tracks.disc_number, tracks.track_number NULLS LAST, tracks.id;

-- One track with the release context a want for it would carry, plus the two
-- ways the library can already hold it. It is ListAlbumTracks for exactly one
-- row, fetched when a single track is wanted or dismissed from a release page.
-- name: GetTrackForWanting :one
SELECT
    tracks.id,
    tracks.album_id,
    albums.title AS album_title,
    albums.musicbrainz_release_group_id,
    artists.name AS artist_name,
    tracks.title,
    tracks.duration_ms,
    tracks.isrc,
    tracks.musicbrainz_recording_id,
    (track_mappings.id IS NOT NULL)::boolean AS owned,
    (held.library_file_id IS NOT NULL)::boolean AS held_elsewhere
FROM tracks
JOIN albums ON albums.id = tracks.album_id
JOIN artists ON artists.id = albums.artist_id
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
WHERE tracks.id = $1;

-- The cached picture of a release. It is display and never evidence: no grader
-- reads it, and a cover cannot say what a file is.
-- name: ReleaseCoverArt :one
SELECT image, content_type, source
FROM release_cover_art
WHERE album_id = $1;

-- A row with no image is the answer "asked, and there is none", which is what
-- stops the archive being asked again on every page view.
-- name: SaveReleaseCoverArt :exec
INSERT INTO release_cover_art (album_id, image, content_type, source, fetched_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (album_id) DO UPDATE
SET image = EXCLUDED.image,
    content_type = EXCLUDED.content_type,
    source = EXCLUDED.source,
    fetched_at = EXCLUDED.fetched_at;

-- Takes back a cover a person set by hand, and only that.
--
-- The release goes back to being one nobody has answered about, so the sweep
-- asks the archives again on its next pass. It is written to touch a row whose
-- source is 'user' and no other: a cover an archive gave is a cached answer,
-- and throwing it away here would empty the cache one release at a time for a
-- question nobody asked.
-- name: DeleteUserReleaseCoverArt :execrows
DELETE FROM release_cover_art
WHERE album_id = $1 AND source = 'user';

-- The releases nobody has looked for a picture of yet. A release with a row —
-- picture or recorded absence — is answered from the cache and never asked about
-- again.
--
-- One kind of row is not an answer. A row names the archive that answered and
-- holds the bytes it sent; a row that names an archive and holds no bytes says
-- two things that cannot both be true, and it is what a release looks like after
-- a fetch that found nothing was written down under the archive's name instead of
-- under 'none'. Left alone that release is never asked about and never pictured.
-- So it is asked once more, and the answer it gets — a picture, or an absence
-- recorded as one — is what stops it being asked a third time. A recorded absence
-- ('none', and the empty name older rows used) is left alone.
--
-- So is a cover the person supplied. 'user' is not an archive that answered; it
-- is somebody having looked at the record and said this is the picture, and
-- asking again would be asking a question that has been answered. Such a row
-- always carries bytes and would drop out on those grounds alone — the name is
-- here so the rule is the rule rather than a consequence.
-- name: ReleasesMissingCoverArt :many
SELECT
    albums.id,
    albums.title,
    artists.name AS artist_name,
    albums.musicbrainz_release_group_id,
    release_editions.musicbrainz_release_id
FROM albums
JOIN artists ON artists.id = albums.artist_id
LEFT JOIN release_editions ON release_editions.album_id = albums.id
LEFT JOIN release_cover_art ON release_cover_art.album_id = albums.id
WHERE release_cover_art.album_id IS NULL
   OR (coalesce(octet_length(release_cover_art.image), 0) = 0
       AND release_cover_art.source NOT IN ('', 'none', 'user'))
ORDER BY albums.created_at
LIMIT $1;

-- Where on disc a release lives, for the releases a picture is already held for.
--
-- Schall caches every cover it fetches in release_cover_art, which is what its
-- own pages draw from. Navidrome — the playback server that reads the same music
-- from the same disc — cannot see a Postgres table, and looks for an image file
-- beside the music instead. This is the list of releases that have both halves
-- of what writing that file needs: a picture in the cache, and music on disc to
-- write it beside.
--
-- A folder rather than a file, and one row per folder: a release on two discs is
-- filed in two folders as often as one, and each of them wants its own cover. The
-- folder is the file's path with the last component cut off, which is the same
-- thing the importer built the path from.
--
-- Only mapped, present files, for the same reason ReleaseMappedFilePaths reads
-- only mapped ones: a track mapping is a match this project already proved, so
-- the folder is this release's folder rather than whatever a scan found nearby.
--
-- Recorded absences drop out on their own. A release the archive answered "no
-- artwork" about has a row with no image, and this asks for rows that carry
-- bytes — so nothing here re-asks about a release that was already answered.
--
-- The source comes along because it decides where the picture to write is
-- fetched from. Only the Cover Art Archive holds a full-size copy of what it
-- gave; a cover that came from iTunes or out of one of the release's own files
-- exists nowhere but this cache, and asking the archive for it would ask about a
-- picture it never had.
-- name: ReleaseFoldersToPicture :many
SELECT DISTINCT
    albums.id,
    albums.musicbrainz_release_group_id,
    release_editions.musicbrainz_release_id,
    release_cover_art.source,
    regexp_replace(library_files.path, '/[^/]*$', '') AS folder
FROM albums
JOIN release_cover_art ON release_cover_art.album_id = albums.id
LEFT JOIN release_editions ON release_editions.album_id = albums.id
JOIN tracks ON tracks.album_id = albums.id
JOIN track_mappings ON track_mappings.track_id = tracks.id
JOIN library_files
    ON library_files.id = track_mappings.library_file_id
   AND library_files.missing_at IS NULL
WHERE octet_length(release_cover_art.image) > 0
ORDER BY albums.id, folder;

-- Where on disc an artist's music lives, for the artists a picture is held for.
--
-- The same shape as ReleaseFoldersToPicture and for the same reason, asked one
-- level up. Navidrome looks for an artist's picture in the artist's own folder,
-- so the caller works that folder out from the release folders under it — which
-- is a decision about the library's layout and belongs in Go rather than here.
-- name: ArtistFoldersToPicture :many
SELECT DISTINCT
    artists.id,
    regexp_replace(library_files.path, '/[^/]*$', '') AS folder
FROM artists
JOIN artist_images ON artist_images.artist_id = artists.id
JOIN albums ON albums.artist_id = artists.id
JOIN tracks ON tracks.album_id = albums.id
JOIN track_mappings ON track_mappings.track_id = tracks.id
JOIN library_files
    ON library_files.id = track_mappings.library_file_id
   AND library_files.missing_at IS NULL
WHERE artist_images.image IS NOT NULL
ORDER BY artists.id, folder;

-- The local files of a release, for the picture they may be carrying. Only
-- mapped files: a track mapping is a match this project already proved, so the
-- picture inside one of those files is a picture of this release and not of
-- whatever happened to be sitting in a folder.
-- name: ReleaseMappedFilePaths :many
SELECT library_files.path
FROM track_mappings
JOIN tracks ON tracks.id = track_mappings.track_id
JOIN library_files ON library_files.id = track_mappings.library_file_id
WHERE tracks.album_id = $1
ORDER BY tracks.disc_number, tracks.track_number
LIMIT $2;

-- The music on disc, with the catalogue's own account of what each file is, for
-- writing the words of the song beside it.
--
-- Lyrics come from LRCLIB, which is asked by artist, title, album and length and
-- answers only when the length agrees within two seconds. Every one of those
-- four has to be the catalogue's account rather than the file's own tags: the
-- tags are whatever a peer typed, and a misspelt artist finds either nothing or
-- somebody else's song.
--
-- Only mapped, present files, because a mapping is a match this project already
-- proved. A file with no mapping is a file nobody has said what it is, and there
-- is nothing truthful to ask about it.
--
-- A track with no length is skipped here rather than asked about without one:
-- LRCLIB falls back to matching on the names alone when the duration is missing,
-- and that is the fuzzy agreement this codebase refuses.
--
-- Paged by the file's own id, which is the cursor a sweep carries from one pass
-- to the next. There is no table recording what was asked — the .lrc file beside
-- the music is the record — so a pass has to be able to say where it stopped.
-- name: TracksToLyric :many
SELECT
    library_files.id AS library_file_id,
    library_files.path,
    tracks.title,
    artists.name AS artist_name,
    albums.title AS album_title,
    tracks.duration_ms
FROM track_mappings
JOIN tracks ON tracks.id = track_mappings.track_id
JOIN albums ON albums.id = tracks.album_id
JOIN artists ON artists.id = albums.artist_id
JOIN library_files
    ON library_files.id = track_mappings.library_file_id
   AND library_files.missing_at IS NULL
WHERE tracks.duration_ms IS NOT NULL
  AND tracks.duration_ms > 0
  AND library_files.id > sqlc.arg(after)
ORDER BY library_files.id
LIMIT sqlc.arg(batch);

-- One file, described exactly as a page of them is, for the moment it is
-- imported. Same selection, named a different way: a file the library has lost
-- or that nothing has matched selects nothing here exactly as it does above.
-- name: TrackToLyric :one
SELECT
    library_files.id AS library_file_id,
    library_files.path,
    tracks.title,
    artists.name AS artist_name,
    albums.title AS album_title,
    tracks.duration_ms
FROM track_mappings
JOIN tracks ON tracks.id = track_mappings.track_id
JOIN albums ON albums.id = tracks.album_id
JOIN artists ON artists.id = albums.artist_id
JOIN library_files
    ON library_files.id = track_mappings.library_file_id
   AND library_files.missing_at IS NULL
WHERE tracks.duration_ms IS NOT NULL
  AND tracks.duration_ms > 0
  AND library_files.id = sqlc.arg(library_file_id)
LIMIT 1;
