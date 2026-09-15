-- The seeded collection. Everything here is invented: five artists nobody has
-- released anything under, and MusicBrainz identifiers that resolve to nothing.
-- That is deliberate. A fixture built from real identifiers would be a fixture
-- the first background refresh rewrote, and a browser test would then be
-- asserting against whatever MusicBrainz said this morning.
--
-- Identifiers are fixed rather than generated, so a test can name a release by
-- its id and a failure screenshot taken today matches one taken last month.
-- Times are relative to now(), because a UI that says "3 hours ago" would drift
-- into "8 months ago" if they were not.
--
-- What the collection is arranged to show, artist by artist:
--
--   Aurora Lake        followed, every release complete
--   Vela Nine          followed, incomplete, with a transfer under way
--   Kestrel Grove      followed, complete, but a want is waiting for an ear and
--                      its last metadata refresh failed
--   Marram             held rather than followed -- the library has their music
--                      and nobody asked for it
--   Petrichor Machine  followed this morning, discography never fetched
--
-- and, on the files, one of each thing that can stop and ask: a file whose
-- identity nothing could settle, one that fits two catalogue tracks, one the
-- library holds twice, one that is owned but external to nothing, and one whose
-- evidence disagrees with itself.
--
-- No artist here is both followed and known to MusicBrainz. That pair is the one
-- standing instruction in the schema to go and ask a provider something, and the
-- section below says why the fixture never writes it.

-- Where the music is. One root, scanned, so the setup guide stands down.
INSERT INTO library_roots (id, path, enabled, last_scanned_at, created_at)
VALUES (
    '10000000-0000-4000-8000-000000000001', '/music', true,
    now() - interval '3 hours', now() - interval '40 days'
);

-- Nobody the fixture follows carries a MusicBrainz identifier, and that is what
-- keeps the seeded database still rather than still for a while.
--
-- The follow feed asks MusicBrainz about every followed artist it has an
-- identifier for and nobody has looked at for a day (StaleFollowedArtists,
-- feedRefreshIdle in internal/acquisition/feed.go). A fixture answering that
-- with a recent last_refreshed_at only answers it today: the day is measured
-- against now() and a seeded row is not measured again, so the three artists
-- that used to carry identifiers here fell due the morning after whoever seeded
-- them went home. Three invented identifiers then became three outbound
-- requests, three failed refreshes and a changed badge, on a database whose
-- whole promise is that it says what the last seed wrote.
--
-- No last_refreshed_at can outrun a window measured from now(), so the fixture
-- does not qualify for that window at all: the identifier lives on the held
-- artist, who the feed does not ask about, and following here is a follow of an
-- artist the catalogue knows by name. Both are states Schall reaches on its own
-- -- an artist the scanner raised from tags and somebody then followed has
-- exactly this shape.
--
-- last_refreshed_at stays on the followed rows because the artist page prints
-- it, and a date nothing acts on is a date rather than a due date.
INSERT INTO artists (
    id, musicbrainz_id, name, sort_name, followed_at, last_refreshed_at,
    catalogue_summary, monitor_level, created_at
) VALUES
    (
        'a0000000-0000-4000-8000-000000000001', NULL,
        'Aurora Lake', 'Lake, Aurora', now() - interval '40 days',
        now() - interval '4 hours', NULL, 'everything', now() - interval '40 days'
    ),
    (
        'a0000000-0000-4000-8000-000000000002', NULL,
        'Vela Nine', 'Vela Nine', now() - interval '31 days',
        now() - interval '6 hours', NULL, 'everything', now() - interval '31 days'
    ),
    -- Refreshed five hours ago and asked again forty minutes ago, which is the
    -- attempt that failed.
    (
        'a0000000-0000-4000-8000-000000000003', NULL,
        'Kestrel Grove', 'Grove, Kestrel', now() - interval '18 days',
        now() - interval '5 hours', NULL, 'everything', now() - interval '18 days'
    ),
    -- Held, not followed: the library contains their music and nobody asked for
    -- it. An artist with no followed_at must say why they are listed.
    --
    -- The one MusicBrainz identifier in the fixture, and it is here because
    -- holding an artist asks nothing of a provider: the feed's sweep reads
    -- followed artists only, so this identifier is a fact on a row rather than a
    -- standing question.
    (
        'a0000000-0000-4000-8000-000000000004', 'a1000000-0000-4000-8000-000000000004',
        'Marram', 'Marram', NULL, now() - interval '6 days',
        'Held because the library contains their music.', 'everything',
        now() - interval '12 days'
    ),
    -- Followed this morning and nothing fetched since, so there is no
    -- discography to show yet and the card says so rather than calling them
    -- complete.
    (
        'a0000000-0000-4000-8000-000000000005', NULL,
        'Petrichor Machine', 'Petrichor Machine', now() - interval '2 hours',
        NULL, NULL, 'everything', now() - interval '2 hours'
    );

INSERT INTO albums (
    id, artist_id, musicbrainz_release_group_id, preferred_musicbrainz_release_id,
    title, release_date, album_type, secondary_types, track_count,
    owned_track_count, dismissed_track_count, created_at
) VALUES
    (
        'b0000000-0000-4000-8000-000000000001', 'a0000000-0000-4000-8000-000000000001',
        'f0000000-0000-4000-8000-000000000001', 'f1000000-0000-4000-8000-000000000001',
        'Glass Harbour', '2024-03-15', 'album', '{}', 3, 3, 0, now() - interval '40 days'
    ),
    (
        'b0000000-0000-4000-8000-000000000002', 'a0000000-0000-4000-8000-000000000001',
        'f0000000-0000-4000-8000-000000000002', 'f1000000-0000-4000-8000-000000000002',
        'Tideline', '2022-06-01', 'ep', '{}', 2, 2, 0, now() - interval '40 days'
    ),
    (
        'b0000000-0000-4000-8000-000000000003', 'a0000000-0000-4000-8000-000000000002',
        'f0000000-0000-4000-8000-000000000003', 'f1000000-0000-4000-8000-000000000003',
        'Signal Fires', '2023-09-08', 'album', '{}', 4, 2, 0, now() - interval '31 days'
    ),
    (
        'b0000000-0000-4000-8000-000000000004', 'a0000000-0000-4000-8000-000000000002',
        'f0000000-0000-4000-8000-000000000004', 'f1000000-0000-4000-8000-000000000004',
        'Nine Lanterns', '2021-01-20', 'album', '{}', 3, 0, 0, now() - interval '31 days'
    ),
    (
        'b0000000-0000-4000-8000-000000000005', 'a0000000-0000-4000-8000-000000000003',
        'f0000000-0000-4000-8000-000000000005', 'f1000000-0000-4000-8000-000000000005',
        'Harrow Bell', '2025-02-14', 'album', '{}', 3, 3, 0, now() - interval '18 days'
    ),
    (
        'b0000000-0000-4000-8000-000000000006', 'a0000000-0000-4000-8000-000000000004',
        'f0000000-0000-4000-8000-000000000006', 'f1000000-0000-4000-8000-000000000006',
        'Saltmarsh', '2020-11-11', 'album', '{}', 2, 2, 0, now() - interval '12 days'
    );

-- Every artist and every release has been asked about, and MusicBrainz's
-- community had voted for nothing. That is what the empty list says; NULL would
-- say "nobody has asked yet", which is a question the genre backfill would
-- answer by queueing a refresh of the one artist here who carries a MusicBrainz
-- identifier (BackfillGenres in queries/artists.sql). The fixture is arranged
-- not to qualify rather than to qualify later, like every other stillness here.
--
-- Empty rather than invented genres for a second reason: a browser test reads
-- what the page says, and a chip nobody asked for is one more thing on the page
-- that the seed would have to keep true.
UPDATE artists SET genres = '{}';
UPDATE albums SET genres = '{}';

-- And every artist has been asked about by an encyclopaedia, which had never
-- heard of them. A time with no words beside it is that answer, and it is what
-- stops the picture sweep listing them for a biography every pass
-- (ArtistsMissingBiography). None of these artists is a real one, so having no
-- article is also the truth.
UPDATE artists SET biography_fetched_at = created_at;

-- One credited artist per release, taken from the artist the release belongs to,
-- identifier included where there is one. Only Marram carries one, which is what
-- a credit looks like for everybody the catalogue knows by name alone.
INSERT INTO album_artist_credits (album_id, position, credited_name, musicbrainz_artist_id)
SELECT albums.id, 1, artists.name, artists.musicbrainz_id
FROM albums JOIN artists ON artists.id = albums.artist_id;

-- The edition each release was ingested from, and why that one. The release page
-- states this rationale, so a seeded release has to carry it or the page reads
-- as though the choice was never made.
INSERT INTO release_editions (
    album_id, musicbrainz_release_id, title, status, country, release_date,
    media_count, track_count, selected_automatically, selection_reason
) VALUES
    (
        'b0000000-0000-4000-8000-000000000001', 'f1000000-0000-4000-8000-000000000001',
        'Glass Harbour', 'Official', 'XW', '2024-03-15', 1, 3, true,
        'earliest official digital release'
    ),
    (
        'b0000000-0000-4000-8000-000000000002', 'f1000000-0000-4000-8000-000000000002',
        'Tideline', 'Official', 'GB', '2022-06-01', 1, 2, true,
        'only official release'
    ),
    (
        'b0000000-0000-4000-8000-000000000003', 'f1000000-0000-4000-8000-000000000003',
        'Signal Fires', 'Official', 'XW', '2023-09-08', 1, 4, true,
        'earliest official digital release'
    ),
    (
        'b0000000-0000-4000-8000-000000000004', 'f1000000-0000-4000-8000-000000000004',
        'Nine Lanterns', 'Official', 'DE', '2021-01-20', 1, 3, true,
        'earliest official release'
    ),
    (
        'b0000000-0000-4000-8000-000000000005', 'f1000000-0000-4000-8000-000000000005',
        'Harrow Bell', 'Official', 'XW', '2025-02-14', 1, 3, false,
        'chosen by hand: the digital edition matches the library'
    ),
    (
        'b0000000-0000-4000-8000-000000000006', 'f1000000-0000-4000-8000-000000000006',
        'Saltmarsh', 'Official', 'NL', '2020-11-11', 1, 2, true,
        'only official release'
    );

INSERT INTO tracks (
    id, album_id, musicbrainz_recording_id, title, disc_number, track_number,
    duration_ms, isrc
) VALUES
    -- Glass Harbour
    ('c0000000-0000-4000-8000-000000000001', 'b0000000-0000-4000-8000-000000000001',
     'e0000000-0000-4000-8000-000000000001', 'Low Tide Signal', 1, 1, 241000, 'XX00A2400001'),
    ('c0000000-0000-4000-8000-000000000002', 'b0000000-0000-4000-8000-000000000001',
     'e0000000-0000-4000-8000-000000000002', 'Harbour Lights', 1, 2, 197500, 'XX00A2400002'),
    ('c0000000-0000-4000-8000-000000000003', 'b0000000-0000-4000-8000-000000000001',
     'e0000000-0000-4000-8000-000000000003', 'Glass', 1, 3, 356000, 'XX00A2400003'),
    -- Tideline
    ('c0000000-0000-4000-8000-000000000004', 'b0000000-0000-4000-8000-000000000002',
     'e0000000-0000-4000-8000-000000000004', 'Shoreward', 1, 1, 208000, 'XX00A2200001'),
    ('c0000000-0000-4000-8000-000000000005', 'b0000000-0000-4000-8000-000000000002',
     'e0000000-0000-4000-8000-000000000005', 'Tideline', 1, 2, 274000, 'XX00A2200002'),
    -- Signal Fires: the first two are owned, the last two are what makes Vela
    -- Nine incomplete.
    ('c0000000-0000-4000-8000-000000000006', 'b0000000-0000-4000-8000-000000000003',
     'e0000000-0000-4000-8000-000000000006', 'Beacon One', 1, 1, 189000, 'XX00V2300001'),
    ('c0000000-0000-4000-8000-000000000007', 'b0000000-0000-4000-8000-000000000003',
     'e0000000-0000-4000-8000-000000000007', 'Beacon Two', 1, 2, 221000, 'XX00V2300002'),
    ('c0000000-0000-4000-8000-000000000008', 'b0000000-0000-4000-8000-000000000003',
     'e0000000-0000-4000-8000-000000000008', 'Watchfire', 1, 3, 302000, 'XX00V2300003'),
    ('c0000000-0000-4000-8000-000000000009', 'b0000000-0000-4000-8000-000000000003',
     'e0000000-0000-4000-8000-000000000009', 'Smoke Column', 1, 4, 264000, 'XX00V2300004'),
    -- Nine Lanterns: owned by nothing at all.
    ('c0000000-0000-4000-8000-00000000000a', 'b0000000-0000-4000-8000-000000000004',
     'e0000000-0000-4000-8000-00000000000a', 'First Lantern', 1, 1, 233000, NULL),
    ('c0000000-0000-4000-8000-00000000000b', 'b0000000-0000-4000-8000-000000000004',
     'e0000000-0000-4000-8000-00000000000b', 'Ninth Lantern', 1, 2, 311000, NULL),
    ('c0000000-0000-4000-8000-00000000000c', 'b0000000-0000-4000-8000-000000000004',
     'e0000000-0000-4000-8000-00000000000c', 'Lantern Coda', 1, 3, 148000, NULL),
    -- Harrow Bell
    ('c0000000-0000-4000-8000-00000000000d', 'b0000000-0000-4000-8000-000000000005',
     'e0000000-0000-4000-8000-00000000000d', 'Harrow', 1, 1, 279000, 'XX00K2500001'),
    ('c0000000-0000-4000-8000-00000000000e', 'b0000000-0000-4000-8000-000000000005',
     'e0000000-0000-4000-8000-00000000000e', 'Bell Tower', 1, 2, 195000, 'XX00K2500002'),
    ('c0000000-0000-4000-8000-00000000000f', 'b0000000-0000-4000-8000-000000000005',
     'e0000000-0000-4000-8000-00000000000f', 'Evensong', 1, 3, 402000, 'XX00K2500003'),
    -- Saltmarsh
    ('c0000000-0000-4000-8000-000000000010', 'b0000000-0000-4000-8000-000000000006',
     'e0000000-0000-4000-8000-000000000010', 'Saltmarsh', 1, 1, 226000, NULL),
    ('c0000000-0000-4000-8000-000000000011', 'b0000000-0000-4000-8000-000000000006',
     'e0000000-0000-4000-8000-000000000011', 'Sea Lavender', 1, 2, 318000, NULL);

INSERT INTO library_files (
    id, library_root_id, path, size_bytes, modified_at, artist_tag, album_tag,
    title_tag, disc_number, track_number, duration_ms, musicbrainz_recording_id,
    isrc, match_status, match_candidate_count, resolution_status,
    resolution_summary, resolution_attempted_at, duplicate_kept_at,
    bit_rate_kbps, sample_rate_hz, channels, scanned_at, created_at
) VALUES
    -- Glass Harbour, complete on disc.
    ('d0000000-0000-4000-8000-000000000001', '10000000-0000-4000-8000-000000000001',
     '/music/Aurora Lake/2024 - Glass Harbour/01 Low Tide Signal.flac', 41238912,
     now() - interval '39 days', 'Aurora Lake', 'Glass Harbour', 'Low Tide Signal',
     1, 1, 241000, 'e0000000-0000-4000-8000-000000000001', 'XX00A2400001',
     'matched', 1, 'resolved', 'recording identifier agreed', now() - interval '39 days',
     NULL, 1008, 44100, 2, now() - interval '3 hours', now() - interval '39 days'),
    ('d0000000-0000-4000-8000-000000000002', '10000000-0000-4000-8000-000000000001',
     '/music/Aurora Lake/2024 - Glass Harbour/02 Harbour Lights.flac', 33889344,
     now() - interval '39 days', 'Aurora Lake', 'Glass Harbour', 'Harbour Lights',
     1, 2, 197500, 'e0000000-0000-4000-8000-000000000002', 'XX00A2400002',
     'matched', 1, 'resolved', 'recording identifier agreed', now() - interval '39 days',
     NULL, 1013, 44100, 2, now() - interval '3 hours', now() - interval '39 days'),
    ('d0000000-0000-4000-8000-000000000003', '10000000-0000-4000-8000-000000000001',
     '/music/Aurora Lake/2024 - Glass Harbour/03 Glass.flac', 60293120,
     now() - interval '39 days', 'Aurora Lake', 'Glass Harbour', 'Glass',
     1, 3, 356000, 'e0000000-0000-4000-8000-000000000003', 'XX00A2400003',
     'matched', 1, 'resolved', 'recording identifier agreed', now() - interval '39 days',
     NULL, 1002, 44100, 2, now() - interval '3 hours', now() - interval '39 days'),
    -- Tideline
    ('d0000000-0000-4000-8000-000000000004', '10000000-0000-4000-8000-000000000001',
     '/music/Aurora Lake/2022 - Tideline/01 Shoreward.mp3', 8331264,
     now() - interval '38 days', 'Aurora Lake', 'Tideline', 'Shoreward',
     1, 1, 208000, 'e0000000-0000-4000-8000-000000000004', 'XX00A2200001',
     'matched', 1, 'resolved', 'ISRC agreed', now() - interval '38 days',
     NULL, 320, 44100, 2, now() - interval '3 hours', now() - interval '38 days'),
    ('d0000000-0000-4000-8000-000000000005', '10000000-0000-4000-8000-000000000001',
     '/music/Aurora Lake/2022 - Tideline/02 Tideline.mp3', 10967040,
     now() - interval '38 days', 'Aurora Lake', 'Tideline', 'Tideline',
     1, 2, 274000, 'e0000000-0000-4000-8000-000000000005', 'XX00A2200002',
     'matched', 1, 'resolved', 'ISRC agreed', now() - interval '38 days',
     NULL, 320, 44100, 2, now() - interval '3 hours', now() - interval '38 days'),
    -- Signal Fires, half of it.
    ('d0000000-0000-4000-8000-000000000006', '10000000-0000-4000-8000-000000000001',
     '/music/Vela Nine/2023 - Signal Fires/01 Beacon One.flac', 31981568,
     now() - interval '20 days', 'Vela Nine', 'Signal Fires', 'Beacon One',
     1, 1, 189000, 'e0000000-0000-4000-8000-000000000006', 'XX00V2300001',
     'matched', 1, 'resolved', 'recording identifier agreed', now() - interval '20 days',
     NULL, 1015, 44100, 2, now() - interval '3 hours', now() - interval '20 days'),
    ('d0000000-0000-4000-8000-000000000007', '10000000-0000-4000-8000-000000000001',
     '/music/Vela Nine/2023 - Signal Fires/02 Beacon Two.flac', 37421056,
     now() - interval '20 days', 'Vela Nine', 'Signal Fires', 'Beacon Two',
     1, 2, 221000, 'e0000000-0000-4000-8000-000000000007', 'XX00V2300002',
     'matched', 1, 'resolved', 'recording identifier agreed', now() - interval '20 days',
     NULL, 1011, 44100, 2, now() - interval '3 hours', now() - interval '20 days'),
    -- Harrow Bell, complete, and the third one was matched by hand.
    ('d0000000-0000-4000-8000-000000000008', '10000000-0000-4000-8000-000000000001',
     '/music/Kestrel Grove/2025 - Harrow Bell/01 Harrow.flac', 47251456,
     now() - interval '17 days', 'Kestrel Grove', 'Harrow Bell', 'Harrow',
     1, 1, 279000, 'e0000000-0000-4000-8000-00000000000d', 'XX00K2500001',
     'matched', 1, 'resolved', 'recording identifier agreed', now() - interval '17 days',
     NULL, 1007, 44100, 2, now() - interval '3 hours', now() - interval '17 days'),
    ('d0000000-0000-4000-8000-000000000009', '10000000-0000-4000-8000-000000000001',
     '/music/Kestrel Grove/2025 - Harrow Bell/02 Bell Tower.flac', 33013760,
     now() - interval '17 days', 'Kestrel Grove', 'Harrow Bell', 'Bell Tower',
     1, 2, 195000, 'e0000000-0000-4000-8000-00000000000e', 'XX00K2500002',
     'matched', 1, 'resolved', 'recording identifier agreed', now() - interval '17 days',
     NULL, 1009, 44100, 2, now() - interval '3 hours', now() - interval '17 days'),
    -- No recording identifier and no ISRC on this one: it is here to be the file
    -- a person matched by hand, which is the mapping nothing may ever overwrite.
    ('d0000000-0000-4000-8000-00000000000a', '10000000-0000-4000-8000-000000000001',
     '/music/Kestrel Grove/2025 - Harrow Bell/03 Evensong.flac', 68091904,
     now() - interval '17 days', 'Kestrel Grove', 'Harrow Bell', 'Evensong',
     1, 3, 402000, NULL, NULL,
     'matched', 1, 'resolved', 'matched by hand', now() - interval '16 days',
     NULL, 1004, 44100, 2, now() - interval '3 hours', now() - interval '17 days'),
    -- Saltmarsh: the music that holds Marram in the list.
    ('d0000000-0000-4000-8000-00000000000b', '10000000-0000-4000-8000-000000000001',
     '/music/Marram/2020 - Saltmarsh/01 Saltmarsh.flac', 38273024,
     now() - interval '12 days', 'Marram', 'Saltmarsh', 'Saltmarsh',
     1, 1, 226000, 'e0000000-0000-4000-8000-000000000010', NULL,
     'matched', 1, 'resolved', 'artist, release and position agreed',
     now() - interval '12 days', NULL, 1006, 44100, 2,
     now() - interval '3 hours', now() - interval '12 days'),
    ('d0000000-0000-4000-8000-00000000000c', '10000000-0000-4000-8000-000000000001',
     '/music/Marram/2020 - Saltmarsh/02 Sea Lavender.flac', 53870592,
     now() - interval '12 days', 'Marram', 'Saltmarsh', 'Sea Lavender',
     1, 2, 318000, 'e0000000-0000-4000-8000-000000000011', NULL,
     'matched', 1, 'resolved', 'artist, release and position agreed',
     now() - interval '12 days', NULL, 1005, 44100, 2,
     now() - interval '3 hours', now() - interval '12 days'),

    -- The five that stopped and asked. None of them is 'pending' or 'failed' on
    -- resolution: those two are what startup re-queues, and a seeded library
    -- that queued provider work on every boot would answer a browser test with
    -- whatever the network did.

    -- Which recording is this file? Two candidates, neither conclusive.
    ('d0000000-0000-4000-8000-00000000000d', '10000000-0000-4000-8000-000000000001',
     '/music/Unsorted/13 unknown take.flac', 44695552,
     now() - interval '5 days', 'Vela Nine', NULL, 'Watchfire (take 3)',
     NULL, NULL, 305000, NULL, NULL,
     'unmatched', 0, 'needs_review', 'two recordings fit and nothing separates them',
     now() - interval '5 days', NULL, 1010, 44100, 2,
     now() - interval '3 hours', now() - interval '5 days'),
    -- Which catalogue track does this file sit in? The release holds the same
    -- piece twice and the tags cannot say which.
    ('d0000000-0000-4000-8000-00000000000e', '10000000-0000-4000-8000-000000000001',
     '/music/Aurora Lake/2024 - Glass Harbour/bonus - Glass (reprise).flac', 29622272,
     now() - interval '9 days', 'Aurora Lake', 'Glass Harbour', 'Glass',
     1, NULL, 174000, NULL, NULL,
     'ambiguous', 2, 'resolved', 'the release holds this title twice',
     now() - interval '9 days', NULL, 1001, 44100, 2,
     now() - interval '3 hours', now() - interval '9 days'),
    -- Which to keep: the mapped copy is d...0001.
    ('d0000000-0000-4000-8000-00000000000f', '10000000-0000-4000-8000-000000000001',
     '/music/Aurora Lake/Glass Harbour (web rip)/01 Low Tide Signal.mp3', 9646080,
     now() - interval '4 days', 'Aurora Lake', 'Glass Harbour', 'Low Tide Signal',
     1, 1, 241000, 'e0000000-0000-4000-8000-000000000001', 'XX00A2400001',
     'duplicate', 1, 'resolved', 'the library already holds this recording',
     now() - interval '4 days', NULL, 320, 44100, 2,
     now() - interval '3 hours', now() - interval '4 days'),
    -- Owned music with no external identity at all. Not missing: local only.
    ('d0000000-0000-4000-8000-000000000010', '10000000-0000-4000-8000-000000000001',
     '/music/Unsorted/rehearsal room, second night.flac', 91234304,
     now() - interval '25 days', 'Marram', NULL, 'Rehearsal room, second night',
     NULL, NULL, 542000, NULL, NULL,
     'unmatched', 0, 'local_only', 'no provider holds this recording',
     now() - interval '25 days', NULL, 1013, 44100, 2,
     now() - interval '3 hours', now() - interval '25 days'),
    -- Evidence that disagrees with itself: the tag names one recording and the
    -- audio answers to another.
    ('d0000000-0000-4000-8000-000000000011', '10000000-0000-4000-8000-000000000001',
     '/music/Unsorted/beacon (mislabelled).mp3', 8912896,
     now() - interval '2 days', 'Vela Nine', 'Signal Fires', 'Beacon Two',
     1, 2, 189000, 'e0000000-0000-4000-8000-000000000007', NULL,
     'unmatched', 0, 'conflict', 'the tag and the audio name different recordings',
     now() - interval '2 days', NULL, 320, 44100, 2,
     now() - interval '3 hours', now() - interval '2 days');

-- Every seeded file has already been measured for loudness, so the pass that
-- measures the library finds nothing to do.
--
-- The paths above name no real audio, so a pass that did find these files would
-- decode nothing, learn nothing, and write down that it had asked -- which is
-- the same end state as this, reached by making the browser tests wait for it.
-- The gain columns stay null on purpose: none of these files was measured by
-- anybody, so none of them has a number, and a fixture that invented one would
-- have the tagger writing a loudness nothing measured.
UPDATE library_files SET loudness_measured_at = now() - interval '3 hours';

-- Every seeded file has already been listened to.
--
-- The sweep that measures audio decodes a few seconds of every file whose
-- spectrum has never been read, and none of these files exists on disc: a
-- browser test would start a decoder per row and get nothing back. Stamping the
-- rows as looked at is what leaves that sweep nothing to do.
--
-- Nothing is claimed about what was found. The cutoff is the top of what 44.1
-- kHz can hold, which is what audio that was never squeezed measures, and no
-- file is suspected of anything: a fixture that accused one of its own copies
-- would put a chip on a page the browser tests read.
UPDATE library_files
SET spectral_cutoff_hz = 22000,
    transcode_suspected = false,
    spectrum_read_at = scanned_at;

-- One file, one track, and the reason. The eleventh is manual, which is the
-- decision nothing may ever overwrite.
INSERT INTO track_mappings (track_id, library_file_id, method, confidence, is_manual)
VALUES
    ('c0000000-0000-4000-8000-000000000001', 'd0000000-0000-4000-8000-000000000001', 'musicbrainz_recording_id', 1, false),
    ('c0000000-0000-4000-8000-000000000002', 'd0000000-0000-4000-8000-000000000002', 'musicbrainz_recording_id', 1, false),
    ('c0000000-0000-4000-8000-000000000003', 'd0000000-0000-4000-8000-000000000003', 'musicbrainz_recording_id', 1, false),
    ('c0000000-0000-4000-8000-000000000004', 'd0000000-0000-4000-8000-000000000004', 'isrc', 1, false),
    ('c0000000-0000-4000-8000-000000000005', 'd0000000-0000-4000-8000-000000000005', 'isrc', 1, false),
    ('c0000000-0000-4000-8000-000000000006', 'd0000000-0000-4000-8000-000000000006', 'musicbrainz_recording_id', 1, false),
    ('c0000000-0000-4000-8000-000000000007', 'd0000000-0000-4000-8000-000000000007', 'musicbrainz_recording_id', 1, false),
    ('c0000000-0000-4000-8000-00000000000d', 'd0000000-0000-4000-8000-000000000008', 'musicbrainz_recording_id', 1, false),
    ('c0000000-0000-4000-8000-00000000000e', 'd0000000-0000-4000-8000-000000000009', 'musicbrainz_recording_id', 1, false),
    ('c0000000-0000-4000-8000-00000000000f', 'd0000000-0000-4000-8000-00000000000a', 'manual', NULL, true),
    ('c0000000-0000-4000-8000-000000000010', 'd0000000-0000-4000-8000-00000000000b', 'tags', 0.95, false),
    ('c0000000-0000-4000-8000-000000000011', 'd0000000-0000-4000-8000-00000000000c', 'tags', 0.95, false);

INSERT INTO library_file_identities (
    library_file_id, kind, musicbrainz_recording_id, musicbrainz_release_group_id,
    artist_name, release_title, track_title, duration_ms, isrc, method, confidence,
    is_manual, summary, evidence
) VALUES
    ('d0000000-0000-4000-8000-000000000001', 'external', 'e0000000-0000-4000-8000-000000000001',
     'f0000000-0000-4000-8000-000000000001', 'Aurora Lake', 'Glass Harbour', 'Low Tide Signal',
     241000, 'XX00A2400001', 'musicbrainz_recording_id', 1, false,
     'the file carries the recording identifier', '["recording identifier agreed"]'),
    ('d0000000-0000-4000-8000-000000000002', 'external', 'e0000000-0000-4000-8000-000000000002',
     'f0000000-0000-4000-8000-000000000001', 'Aurora Lake', 'Glass Harbour', 'Harbour Lights',
     197500, 'XX00A2400002', 'musicbrainz_recording_id', 1, false,
     'the file carries the recording identifier', '["recording identifier agreed"]'),
    ('d0000000-0000-4000-8000-000000000003', 'external', 'e0000000-0000-4000-8000-000000000003',
     'f0000000-0000-4000-8000-000000000001', 'Aurora Lake', 'Glass Harbour', 'Glass',
     356000, 'XX00A2400003', 'musicbrainz_recording_id', 1, false,
     'the file carries the recording identifier', '["recording identifier agreed"]'),
    ('d0000000-0000-4000-8000-000000000004', 'external', 'e0000000-0000-4000-8000-000000000004',
     'f0000000-0000-4000-8000-000000000002', 'Aurora Lake', 'Tideline', 'Shoreward',
     208000, 'XX00A2200001', 'isrc', 1, false,
     'the ISRC agreed and nothing contradicted it', '["ISRC agreed"]'),
    ('d0000000-0000-4000-8000-000000000005', 'external', 'e0000000-0000-4000-8000-000000000005',
     'f0000000-0000-4000-8000-000000000002', 'Aurora Lake', 'Tideline', 'Tideline',
     274000, 'XX00A2200002', 'isrc', 1, false,
     'the ISRC agreed and nothing contradicted it', '["ISRC agreed"]'),
    ('d0000000-0000-4000-8000-000000000006', 'external', 'e0000000-0000-4000-8000-000000000006',
     'f0000000-0000-4000-8000-000000000003', 'Vela Nine', 'Signal Fires', 'Beacon One',
     189000, 'XX00V2300001', 'musicbrainz_recording_id', 1, false,
     'the file carries the recording identifier', '["recording identifier agreed"]'),
    ('d0000000-0000-4000-8000-000000000007', 'external', 'e0000000-0000-4000-8000-000000000007',
     'f0000000-0000-4000-8000-000000000003', 'Vela Nine', 'Signal Fires', 'Beacon Two',
     221000, 'XX00V2300002', 'musicbrainz_recording_id', 1, false,
     'the file carries the recording identifier', '["recording identifier agreed"]'),
    ('d0000000-0000-4000-8000-000000000008', 'external', 'e0000000-0000-4000-8000-00000000000d',
     'f0000000-0000-4000-8000-000000000005', 'Kestrel Grove', 'Harrow Bell', 'Harrow',
     279000, 'XX00K2500001', 'musicbrainz_recording_id', 1, false,
     'the file carries the recording identifier', '["recording identifier agreed"]'),
    ('d0000000-0000-4000-8000-000000000009', 'external', 'e0000000-0000-4000-8000-00000000000e',
     'f0000000-0000-4000-8000-000000000005', 'Kestrel Grove', 'Harrow Bell', 'Bell Tower',
     195000, 'XX00K2500002', 'musicbrainz_recording_id', 1, false,
     'the file carries the recording identifier', '["recording identifier agreed"]'),
    -- Local only: owned music with no external identity, which is not the same
    -- as music the library is missing.
    ('d0000000-0000-4000-8000-000000000010', 'local_only', NULL, NULL,
     'Marram', NULL, 'Rehearsal room, second night', 542000, NULL, 'no_candidate', NULL, false,
     'no provider holds a recording this could be', '[]');

-- The two recordings the unidentified file could be, which is the whole of what
-- makes it a question rather than an answer.
INSERT INTO library_file_candidates (
    library_file_id, musicbrainz_recording_id, musicbrainz_release_group_id,
    artist_name, release_title, track_title, duration_ms, isrc, rank, agrees,
    differs, summary
) VALUES
    ('d0000000-0000-4000-8000-00000000000d', 'e0000000-0000-4000-8000-000000000008',
     'f0000000-0000-4000-8000-000000000003', 'Vela Nine', 'Signal Fires', 'Watchfire',
     302000, 'XX00V2300003', 1, '{artist,title}', '{duration}',
     'the artist and title agree; the duration is 3s out'),
    ('d0000000-0000-4000-8000-00000000000d', 'e0000000-0000-4000-8000-00000000001a',
     'f0000000-0000-4000-8000-000000000003', 'Vela Nine', 'Signal Fires (live)', 'Watchfire',
     307000, NULL, 2, '{artist,title}', '{release}',
     'the same title on a live release nothing rules out');

-- Wants waiting on a person. Both are 'awaiting_review', which is the one status
-- the sweeper leaves alone: a want with a next_attempt_at would be picked up on
-- the next tick and the queue would not stay still long enough to be tested.
INSERT INTO acquisition_targets (
    id, origin, origin_album_id, entry_artist, entry_title, entry_album,
    entry_duration_ms, entry_isrc, musicbrainz_recording_id,
    musicbrainz_release_group_id, resolution_method, resolved_at, status,
    review_reason, summary, attempts, last_attempt_at, created_at, updated_at
) VALUES
    -- Copies fetched for a want that no listener could identify. They are held
    -- rather than imported, which is the refusal the review queue exists for.
    (
        '20000000-0000-4000-8000-000000000001', 'follow_feed',
        'b0000000-0000-4000-8000-000000000005', 'Kestrel Grove', 'Evensong (single edit)',
        'Harrow Bell', 238000, NULL, 'e0000000-0000-4000-8000-00000000001b',
        'f0000000-0000-4000-8000-000000000005', 'recording_id', now() - interval '6 days',
        'awaiting_review', 'candidate',
        'Two copies arrived and neither could be identified as this recording.',
        2, now() - interval '2 days', now() - interval '6 days', now() - interval '2 days'
    ),
    -- A playlist entry that fits more than one recording. Nothing may pick for
    -- the user, so it waits.
    (
        '20000000-0000-4000-8000-000000000002', 'playlist', NULL,
        'Vela Nine', 'Watchfire', 'Signal Fires', 305000, NULL, NULL, NULL,
        NULL, NULL, 'awaiting_review', 'resolution',
        'Two recordings fit this entry and nothing separates them.',
        1, now() - interval '3 days', now() - interval '3 days', now() - interval '3 days'
    );

-- Wants nobody has to answer anything about: one still being looked for, one
-- somebody stopped looking for. That is the state a want spends most of its life
-- in — no question to answer, no transfer under way — and it is what the Wanted
-- tab on the Downloads screen shows.
--
-- Neither carries a next_attempt_at. A want with one is due, and a due want is
-- picked up by the sweeper on its first tick, which would send a search out
-- while a browser test was reading the page. The recording IDs are this
-- fixture's own and name nothing in the catalogue and nothing in the stored
-- recommendations, so no suppression rule and no completeness figure moves
-- because these two rows exist.
INSERT INTO acquisition_targets (
    id, origin, entry_artist, entry_title, entry_album, entry_duration_ms,
    musicbrainz_recording_id, resolution_method, resolved_at, status, summary,
    attempts, last_attempt_at, not_wanted_at, created_at, updated_at
) VALUES
    (
        '20000000-0000-4000-8000-000000000003', 'manual',
        'Marram', 'Salt Marsh', 'Fathom', 244000,
        'e0000000-0000-4000-8000-00000000002a', 'recording_id', now() - interval '5 days',
        'pending', 'Nobody is offering this recording yet.',
        3, now() - interval '5 hours', NULL,
        now() - interval '5 days', now() - interval '5 hours'
    ),
    (
        '20000000-0000-4000-8000-000000000004', 'playlist',
        'Aurora Lake', 'Beacon Hill', 'Glass Harbour', 198000,
        'e0000000-0000-4000-8000-00000000002b', 'recording_id', now() - interval '9 days',
        'not_wanted', 'You stopped looking for this recording.',
        1, now() - interval '8 days', now() - interval '7 days',
        now() - interval '9 days', now() - interval '7 days'
    );

INSERT INTO acquisition_target_files (
    id, acquisition_target_id, provider, source_username, remote_path, file_name,
    size_bytes, verdict, decided_by, summary, evidence, decided_at, created_at
) VALUES
    (
        '21000000-0000-4000-8000-000000000001', '20000000-0000-4000-8000-000000000001',
        'slskd', 'harbourmaster',
        '@@shared\Kestrel Grove\Harrow Bell (singles)\Evensong (single edit).flac',
        'Evensong (single edit).flac', 39845888, 'held', 'schall',
        'nothing identified the audio, so it was held rather than imported',
        '{"listened": true, "identified": false}',
        now() - interval '2 days', now() - interval '2 days'
    ),
    (
        '21000000-0000-4000-8000-000000000002', '20000000-0000-4000-8000-000000000001',
        'slskd', 'nightporter',
        '@@shared\music\kestrel grove\evensong.mp3', 'evensong.mp3', 9218048,
        'held', 'schall',
        'the tags agree and the audio answers to nothing',
        '{"listened": true, "identified": false}',
        now() - interval '2 days', now() - interval '2 days'
    );

INSERT INTO acquisition_target_candidates (
    acquisition_target_id, musicbrainz_recording_id, musicbrainz_release_group_id,
    artist_name, release_title, track_title, duration_ms, isrc, rank, agrees,
    differs, summary
) VALUES
    (
        '20000000-0000-4000-8000-000000000002', 'e0000000-0000-4000-8000-000000000008',
        'f0000000-0000-4000-8000-000000000003', 'Vela Nine', 'Signal Fires', 'Watchfire',
        302000, 'XX00V2300003', 1, '{artist,title}', '{duration}',
        'the studio recording, 3s shorter than the entry'
    ),
    (
        '20000000-0000-4000-8000-000000000002', 'e0000000-0000-4000-8000-00000000001a',
        'f0000000-0000-4000-8000-000000000003', 'Vela Nine', 'Signal Fires (live)', 'Watchfire',
        307000, NULL, 2, '{artist,title,duration}', '{}',
        'a live recording whose duration is 2s out'
    );

INSERT INTO acquisition_target_attempts (acquisition_target_id, attempt, outcome, detail, recorded_at)
VALUES
    ('20000000-0000-4000-8000-000000000001', 1, 'none', 'no source offered this recording', now() - interval '5 days'),
    ('20000000-0000-4000-8000-000000000001', 2, 'inconclusive', 'two copies arrived and neither could be identified', now() - interval '2 days'),
    ('20000000-0000-4000-8000-000000000002', 1, 'inconclusive', 'two recordings fit the entry', now() - interval '3 days');

-- A want list with a source, and the entries it is made of: two the library
-- already holds, two it does not.
INSERT INTO playlists (
    id, source, source_id, source_revision, name, description, owner_name,
    track_count, imported_at, created_at, updated_at
) VALUES (
    '40000000-0000-4000-8000-000000000001', 'manual', NULL, NULL, 'Late night drive',
    'Four things to test a queue with.', 'you', 4, now() - interval '3 days',
    now() - interval '3 days', now() - interval '3 days'
);

INSERT INTO playlist_entries (
    id, playlist_id, position, source_track_id, entry_artist, entry_title,
    entry_album, entry_duration_ms, entry_isrc, owned_library_file_id
) VALUES
    ('41000000-0000-4000-8000-000000000001', '40000000-0000-4000-8000-000000000001', 1,
     NULL, 'Aurora Lake', 'Glass', 'Glass Harbour', 356000, 'XX00A2400003',
     'd0000000-0000-4000-8000-000000000003'),
    ('41000000-0000-4000-8000-000000000002', '40000000-0000-4000-8000-000000000001', 2,
     NULL, 'Marram', 'Sea Lavender', 'Saltmarsh', 318000, NULL,
     'd0000000-0000-4000-8000-00000000000c'),
    ('41000000-0000-4000-8000-000000000003', '40000000-0000-4000-8000-000000000001', 3,
     NULL, 'Vela Nine', 'Watchfire', 'Signal Fires', 305000, NULL, NULL),
    ('41000000-0000-4000-8000-000000000004', '40000000-0000-4000-8000-000000000001', 4,
     NULL, 'Vela Nine', 'Smoke Column', 'Signal Fires', 264000, 'XX00V2300004', NULL);

INSERT INTO playlist_entry_targets (playlist_entry_id, acquisition_target_id)
VALUES ('41000000-0000-4000-8000-000000000003', '20000000-0000-4000-8000-000000000002');

-- One stored answer from the recommendation source, so the Recommended view has
-- a list to draw.
--
-- The source itself is deliberately not configured: listenbrainz_settings stays
-- empty, because startup queues a sweep for an enabled account and a sweep would
-- replace every row below with whatever ListenBrainz said this morning. A stored
-- snapshot with no account is the state an installation is in after somebody
-- disconnects one, and nothing acts on it.
--
-- Nothing here is measured against a clock. The rows say what they say at any
-- date the fixture is read at: three suggestions the five rules allow, and one
-- they hold back because the library already holds that recording -- it is
-- 'Low Tide Signal', which a present file is mapped to. No impression row is
-- seeded, because the fifth rule's window is the one thing here that would
-- expire.
INSERT INTO recommendation_snapshots (
    source, source_snapshot_id, source_snapshot_at, status, detail, fetched_at
) VALUES (
    'listenbrainz', 'seed-model-1', now() - interval '2 hours', 'complete', '',
    now() - interval '2 hours'
);

INSERT INTO recommendation_candidates (
    source, musicbrainz_recording_id, musicbrainz_release_group_id,
    musicbrainz_artist_ids, recording_title, release_title, artist_name, rank,
    source_score, reason_codes, reason_context, fetched_at
) VALUES
    ('listenbrainz', 'e2000000-0000-4000-8000-000000000001',
     'b2000000-0000-4000-8000-000000000001',
     '{a2000000-0000-4000-8000-000000000001}', 'Weather Report', 'Long Reach',
     'Halyard', 1, 0.91, '{cf_raw}', '{"model": "seed-model-1"}',
     now() - interval '2 hours'),
    ('listenbrainz', 'e2000000-0000-4000-8000-000000000002',
     'b2000000-0000-4000-8000-000000000002',
     '{a2000000-0000-4000-8000-000000000002}', 'Cold Sun', 'Field Notes',
     'Cassiopeia Fields', 2, 0.84, '{similar_artist}', '{"model": "seed-model-1"}',
     now() - interval '2 hours'),
    -- The library holds this recording, so the first rule keeps it off the list
    -- and the count above the list says why.
    ('listenbrainz', 'e0000000-0000-4000-8000-000000000001',
     'b2000000-0000-4000-8000-000000000003',
     '{a2000000-0000-4000-8000-000000000003}', 'Low Tide Signal', 'Glass Harbour',
     'Aurora Lake', 3, 0.80, '{cf_raw}', '{"model": "seed-model-1"}',
     now() - interval '2 hours'),
    ('listenbrainz', 'e2000000-0000-4000-8000-000000000004',
     'b2000000-0000-4000-8000-000000000004',
     '{a2000000-0000-4000-8000-000000000004}', 'Slack Water', 'Estuary',
     'Pilot Cutter', 4, 0.77, '{similar_recording}', '{"model": "seed-model-1"}',
     now() - interval '2 hours');

-- One folder asked for and moving, which is what puts a transfer on the
-- Downloads badge and "downloading" on the Vela Nine card.
INSERT INTO download_requests (
    id, album_id, provider, source_username, source_directory, status, file_count,
    expected_track_count, total_size_bytes, format, average_bit_rate, score,
    reasons, files, requested_at, started_at, import_status
) VALUES (
    '30000000-0000-4000-8000-000000000001', 'b0000000-0000-4000-8000-000000000003',
    'slskd', 'harbourmaster', '@@shared\Vela Nine\Signal Fires [FLAC]', 'started',
    4, 4, 152043520, 'flac', 1011, 0.94,
    '{"every track accounted for","lossless","one folder"}',
    '[
        {"path": "@@shared\\Vela Nine\\Signal Fires [FLAC]\\01 Beacon One.flac",
         "name": "01 Beacon One.flac", "extension": ".flac", "sizeBytes": 31981568},
        {"path": "@@shared\\Vela Nine\\Signal Fires [FLAC]\\02 Beacon Two.flac",
         "name": "02 Beacon Two.flac", "extension": ".flac", "sizeBytes": 37421056},
        {"path": "@@shared\\Vela Nine\\Signal Fires [FLAC]\\03 Watchfire.flac",
         "name": "03 Watchfire.flac", "extension": ".flac", "sizeBytes": 44695552},
        {"path": "@@shared\\Vela Nine\\Signal Fires [FLAC]\\04 Smoke Column.flac",
         "name": "04 Smoke Column.flac", "extension": ".flac", "sizeBytes": 37945344}
     ]',
    now() - interval '20 minutes', now() - interval '18 minutes', 'pending'
);

INSERT INTO downloads (
    id, album_id, download_request_id, provider, provider_download_id, status,
    source_username, remote_path, size_bytes, transferred_bytes, queued_at,
    started_at, attempt
) VALUES (
    '31000000-0000-4000-8000-000000000001', 'b0000000-0000-4000-8000-000000000003',
    '30000000-0000-4000-8000-000000000001', 'slskd', 'seed-transfer-1', 'downloading',
    'harbourmaster', '@@shared\Vela Nine\Signal Fires [FLAC]\03 Watchfire.flac',
    44695552, 18874368, now() - interval '20 minutes', now() - interval '18 minutes', 1
);

-- One search run that found something, so the sources view has a page to open.
INSERT INTO source_searches (
    run_id, album_id, status, query, candidates, requested_at, searched_at
) VALUES (
    '32000000-0000-4000-8000-000000000001', 'b0000000-0000-4000-8000-000000000003',
    'found', 'Vela Nine Signal Fires',
    '[
        {
            "provider": "slskd", "username": "harbourmaster",
            "directory": "@@shared\\Vela Nine\\Signal Fires [FLAC]",
            "totalSizeBytes": 152043520, "format": "flac", "averageBitRate": 1011,
            "freeUploadSlot": true, "queueLength": 0, "uploadSpeed": 1450000,
            "score": 0.94,
            "match": {"expected": 4, "confirmed": 4, "partial": 0, "absent": 0},
            "reasons": ["every track accounted for", "lossless", "one folder"],
            "files": [
                {"path": "@@shared\\Vela Nine\\Signal Fires [FLAC]\\01 Beacon One.flac",
                 "name": "01 Beacon One.flac", "extension": ".flac",
                 "sizeBytes": 31981568, "bitRate": 1015, "durationSeconds": 189},
                {"path": "@@shared\\Vela Nine\\Signal Fires [FLAC]\\02 Beacon Two.flac",
                 "name": "02 Beacon Two.flac", "extension": ".flac",
                 "sizeBytes": 37421056, "bitRate": 1011, "durationSeconds": 221},
                {"path": "@@shared\\Vela Nine\\Signal Fires [FLAC]\\03 Watchfire.flac",
                 "name": "03 Watchfire.flac", "extension": ".flac",
                 "sizeBytes": 44695552, "bitRate": 1009, "durationSeconds": 302},
                {"path": "@@shared\\Vela Nine\\Signal Fires [FLAC]\\04 Smoke Column.flac",
                 "name": "04 Smoke Column.flac", "extension": ".flac",
                 "sizeBytes": 37945344, "bitRate": 1013, "durationSeconds": 264}
            ]
        },
        {
            "provider": "slskd", "username": "nightporter",
            "directory": "@@shared\\music\\vela nine\\signal fires",
            "totalSizeBytes": 41287680, "format": "mp3", "averageBitRate": 320,
            "freeUploadSlot": false, "queueLength": 6, "uploadSpeed": 210000,
            "score": 0.61,
            "match": {"expected": 4, "confirmed": 2, "partial": 1, "absent": 1},
            "reasons": ["every track accounted for", "lossy"],
            "files": [
                {"path": "@@shared\\music\\vela nine\\signal fires\\01 beacon one.mp3",
                 "name": "01 beacon one.mp3", "extension": ".mp3",
                 "sizeBytes": 7561216, "bitRate": 320, "durationSeconds": 189},
                {"path": "@@shared\\music\\vela nine\\signal fires\\02 beacon two.mp3",
                 "name": "02 beacon two.mp3", "extension": ".mp3",
                 "sizeBytes": 8847360, "bitRate": 320, "durationSeconds": 221},
                {"path": "@@shared\\music\\vela nine\\signal fires\\03 watchfire.mp3",
                 "name": "03 watchfire.mp3", "extension": ".mp3",
                 "sizeBytes": 12083200, "bitRate": 320, "durationSeconds": 302},
                {"path": "@@shared\\music\\vela nine\\signal fires\\04 smoke column.mp3",
                 "name": "04 smoke column.mp3", "extension": ".mp3",
                 "sizeBytes": 10567680, "bitRate": 320, "durationSeconds": 264}
            ]
        }
     ]',
    now() - interval '25 minutes', now() - interval '24 minutes'
);

-- A refresh that spent every attempt. It is what puts "failed" on the Kestrel
-- Grove card and a count on the Settings badge, and it is a row rather than a
-- queued job so nothing re-runs it while a test is reading it.
--
-- The error is the one that artist's refresh actually ends in. Kestrel Grove is
-- followed by name and MusicBrainz has never been asked who they are, so the
-- refresh stops at the identifier it has nothing to ask with (processArtist,
-- internal/jobs/worker.go) -- which is also what a reader gets by pressing
-- Refresh on this card, so the banner and the button agree.
INSERT INTO jobs (kind, status, payload, attempts, max_attempts, run_after, started_at, completed_at, error_message, created_at)
VALUES
    (
        'refresh_artist_metadata', 'failed',
        '{"artistId": "a0000000-0000-4000-8000-000000000003"}', 3, 3,
        now() - interval '40 minutes', now() - interval '40 minutes',
        now() - interval '38 minutes',
        'load artist: artist has no MusicBrainz identity', now() - interval '40 minutes'
    ),
    (
        'scan_library', 'completed', '{}', 1, 3,
        now() - interval '3 hours', now() - interval '3 hours', now() - interval '3 hours',
        NULL, now() - interval '3 hours'
    );

-- Configured and reachable, so the sidebar reads "connected" rather than "not
-- set up". Nothing here can be dialled: 5030 on loopback is where slskd would
-- be, and a seeded database is not running one.
INSERT INTO slskd_settings (base_url, api_key, enabled, search_timeout_seconds, connection_status, last_checked_at)
VALUES ('http://127.0.0.1:5030', 'seed-api-key', true, 20, 'ok', now() - interval '11 minutes');

INSERT INTO import_settings (source_retention, acoustid_api_key, acoustid_enabled)
VALUES ('keep', '', false);

-- Pictures for every artist and every release, drawn rather than fetched. They
-- exist for two reasons: a grid of covers is what the pages actually look like
-- in use, and the sweep that fills this table picks rows that are absent, so
-- seeding them is also what stops a seeded database from calling an archive on
-- every boot.
INSERT INTO release_cover_art (album_id, image, content_type, source, fetched_at)
SELECT
    albums.id,
    convert_to(
        '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 320 320">'
        || '<rect width="320" height="320" fill="' || shade.background || '"/>'
        || '<circle cx="160" cy="132" r="62" fill="none" stroke="' || shade.ink
        || '" stroke-width="6" opacity="0.55"/>'
        || '<rect x="0" y="236" width="320" height="84" fill="#00000055"/>'
        || '<text x="20" y="270" fill="' || shade.ink
        || '" font-family="Helvetica, Arial, sans-serif" font-size="24">'
        || albums.title || '</text>'
        || '<text x="20" y="298" fill="' || shade.ink
        || '" font-family="Helvetica, Arial, sans-serif" font-size="15" opacity="0.75">'
        || artists.name || '</text></svg>',
        'UTF8'
    ),
    'image/svg+xml',
    'embedded',
    now() - interval '3 hours'
FROM albums
JOIN artists ON artists.id = albums.artist_id
JOIN LATERAL (
    SELECT
        -- A colour per title, chosen the same way every time this runs. md5 for
        -- the choosing rather than hashtext, which is an internal Postgres
        -- function with no promise to keep answering the same thing.
        (ARRAY['#1f4b57', '#4a2f5c', '#5c3a20', '#204a2f', '#4a2030', '#2a3560'])[
            (get_byte(decode(md5(albums.title), 'hex'), 0) % 6) + 1
        ] AS background,
        '#f2ece4'::text AS ink
) AS shade ON true;

INSERT INTO artist_images (artist_id, image, content_type, source, fetched_at)
SELECT
    artists.id,
    convert_to(
        '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 320 320">'
        || '<rect width="320" height="320" fill="#17171a"/>'
        || '<circle cx="160" cy="160" r="104" fill="'
        || (ARRAY['#2f5c68', '#5c3a20', '#3b2f5c', '#2a4a33', '#5c2a3a'])[
               (get_byte(decode(md5(artists.name), 'hex'), 0) % 5) + 1
           ]
        || '"/>'
        || '<text x="160" y="176" fill="#f2ece4" text-anchor="middle"'
        || ' font-family="Helvetica, Arial, sans-serif" font-size="64">'
        || upper(left(artists.name, 1)) || '</text></svg>',
        'UTF8'
    ),
    'image/svg+xml',
    'musicbrainz',
    now() - interval '3 hours'
FROM artists;
