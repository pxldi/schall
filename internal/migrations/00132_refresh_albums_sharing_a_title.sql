-- +goose Up
-- Refresh once every album that shares its artist, title and type with another
-- album. A refresh folds an album whose release group MusicBrainz merged into
-- another (#128), but nothing refreshes an album that already has its tracks.
-- There were 295 such albums live, 4 pairs of them merged at MusicBrainz. The
-- same grouping read live took 10 ms. Staggered as in 00129.
INSERT INTO jobs (kind, payload, run_after)
SELECT 'refresh_album_metadata',
       jsonb_build_object('albumId', albums.id::text),
       now() + (row_number() OVER (ORDER BY albums.created_at) * interval '2 seconds')
FROM albums
WHERE albums.musicbrainz_release_group_id IS NOT NULL
  AND EXISTS (
      SELECT 1 FROM albums AS twin
      WHERE twin.artist_id = albums.artist_id
        AND twin.id <> albums.id
        AND twin.album_type = albums.album_type
        AND lower(btrim(twin.title)) = lower(btrim(albums.title))
  )
ON CONFLICT (kind, (payload->>'albumId'))
    WHERE kind = 'refresh_album_metadata' AND status IN ('queued', 'running')
DO NOTHING;

-- +goose Down
SELECT 1;
