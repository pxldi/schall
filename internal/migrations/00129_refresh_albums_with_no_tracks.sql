-- +goose Up
-- Refresh every album with no tracks once. The edition choice used to take a
-- MusicBrainz release with no tracks entered (Yeat's ADL has 15 editions, 9 of
-- them empty) and every later refresh read the same empty one again. The
-- refresh now passes over empty editions, and a release group with none left
-- completes as "MusicBrainz lists no tracks". There were 16 such albums live,
-- so this queues 16 jobs, staggered as in 00123.
INSERT INTO jobs (kind, payload, run_after)
SELECT 'refresh_album_metadata',
       jsonb_build_object('albumId', albums.id::text),
       now() + (row_number() OVER (ORDER BY albums.created_at) * interval '2 seconds')
FROM albums
WHERE albums.musicbrainz_release_group_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM tracks WHERE tracks.album_id = albums.id)
ON CONFLICT (kind, (payload->>'albumId'))
    WHERE kind = 'refresh_album_metadata' AND status IN ('queued', 'running')
DO NOTHING;

-- +goose Down
SELECT 1;
