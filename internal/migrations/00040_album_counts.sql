-- +goose Up
-- How much of a release the library already holds is asked far more often than
-- it changes, and until now it was answered by reading tracks, mappings,
-- identities and files every time it was asked. Page a release it is bounded
-- work; count how many releases are "partial" and it is the whole catalogue,
-- because deciding that asks about every release rather than about the page.
-- 00022 measured what that costs: 83ms to list a page, 1.20s to count it.
--
-- So a release now carries the three numbers itself, and they are maintained
-- rather than recomputed.
--
--   track_count            tracks on the release
--   owned_track_count      of those, the ones the library can play today
--   dismissed_track_count  of the rest, the ones the user said not to want
--
-- The three are what every shape the browser filters by is made of — owned is
-- owned_track_count = track_count - dismissed, partial has some of each,
-- missing has none owned, untracked has no tracks — so the filter becomes
-- arithmetic on the release's own row and the count becomes one pass over
-- albums. It also gives the six status chips live counts and lets the list be
-- ordered by how complete a release is, neither of which paging could afford.
--
-- These are display counts. They say how much of a release is owned and
-- nothing else; nothing that matches, grades or admits a file may read them.
-- Ownership is still proved where it was always proved, one track at a time.
--
-- Maintained by trigger rather than by the code that writes, deliberately.
-- Ownership is not a property of the release's own rows: it moves when a
-- mapping is written, when a file goes missing or comes back, when a file is
-- identified, when a track's recording changes, and when a want is dismissed —
-- five tables, written from five packages, three of them ones this repo asks
-- not to be edited casually. Two of the five reach the counts only by cascade:
-- deleting a library file deletes its mappings, and no Go call site sees that
-- happen. A number that has to be true cannot be kept true by asking sixteen
-- call sites to remember it, and remembering it is exactly what a new call site
-- would not do. The cost is paid by writers: every statement touching those
-- five tables now also recomputes the releases it could have moved. Measured
-- below.
--
-- Statement level, with transition tables, because the one bulk writer here is
-- the scanner: a library that did not mount marks every file missing in one
-- UPDATE, and a row-level trigger would answer that with one recompute per
-- file. Statement level answers it with one recompute per release.
--
-- Drift is repairable without a deploy. album_counts_recompute_all() rebuilds
-- every release's counts from the tables they are derived from and returns how
-- many rows were wrong, so on a live database
--
--     SELECT album_counts_recompute_all();
--
-- both reports drift and fixes it, and answers 0 when there is none.
--
-- Measured against a synthetic catalogue of 60k releases across 20k artists,
-- with 539k tracks, 276k mappings, 382k library files, 40k identities and 145k
-- jobs — the shape 00022 was measured against, a little heavier. Best of three
-- on a machine with other work on it. Before is the same query deriving the
-- counts the way it did until now; the browser's own scope, followed or owned:
--
--   count, status "partial"         2.64s -> 82ms
--   count, all six shapes at once      -- -> 96ms    (was one shape, or none)
--   first page, status "partial"    3.2ms -> 0.21ms
--   first page, year order          631ms -> 0.08ms
--   first page, completeness order     -- -> 23ms
--
-- and what the writers pay for it, triggers off against triggers on:
--
--   one mapping written            0.09ms -> 0.7ms
--   10k files marked missing         340ms -> 4.6s
--   a scan writing 10k unchanged     239ms -> 240ms
--   backfill all 60k releases           -- -> 8.3s
--
-- The scanner's two numbers are the ones to watch, and they are the reason
-- these triggers are statement level and compare both sides. A scan that finds
-- everything where it left it writes every file it saw and moves no count, and
-- costs nothing extra. A scan that finds 10k files gone is a library that did
-- not mount; it happens once, and 4.6s is what it costs to have every release
-- that leant on those files tell the truth immediately afterwards. A row-level
-- trigger would have answered the same statement 10k times over.
ALTER TABLE albums
    ADD COLUMN track_count integer NOT NULL DEFAULT 0,
    ADD COLUMN owned_track_count integer NOT NULL DEFAULT 0,
    ADD COLUMN dismissed_track_count integer NOT NULL DEFAULT 0;

-- Drift that breaks the arithmetic should fail the write that caused it rather
-- than become a wrong number on a page. A track is counted dismissed only when
-- it is not owned, so the two never overlap and neither can outgrow the whole.
ALTER TABLE albums
    ADD CONSTRAINT albums_counts_within_track_count CHECK (
        track_count >= 0
        AND owned_track_count >= 0
        AND dismissed_track_count >= 0
        AND owned_track_count + dismissed_track_count <= track_count
    );

-- The rule, in one place. A track is owned while a file that is still on disk
-- proves it: mapped to this track, or identified as the recording this track
-- carries, or mapped to another track carrying the same recording — the same
-- three proofs the acquisition loop settles a want against. A mapping to a file
-- that has gone missing is kept, because the decision behind it is still good,
-- but it is not ownership today.
--
-- Dismissed is the user having said they do not want the recording. It is not
-- owned and not missing either; it leaves the denominator.
--
-- The rows are locked in id order before they are written, because two
-- transactions that both move counts will often overlap: one file can be the
-- reason five releases change. Ordering makes one call safe against another —
-- which covers the statement that moves thousands at once, where the overlap is
-- near certain.
--
-- It does not make a transaction safe against another transaction. A write that
-- moves a mapping from one release to another is a DELETE and then an INSERT,
-- two statements, and the second one's releases are not known while the first
-- one's locks are being taken; two of those crossing in opposite directions can
-- deadlock where before this migration they never touched albums at all. What
-- that costs is the losing statement — a manual match or a scan that has to be
-- retried — and never a wrong count: the loser rolls back whole. Making it
-- impossible means one lock for all counting, taken before any release is, and
-- that is a queue in front of every write in the application to save a rare
-- retry.
--
-- force_generic_plan is here because this runs once a write and almost always
-- for one or two releases, where planning it costs an order of magnitude more
-- than running it: 2.7ms a release replanned against 0.3ms on the cached plan,
-- and one mapping written 3.3ms against 0.7ms. The price is the other end —
-- the plan cached for one release is asked to serve ten thousand when a whole
-- library goes missing, and that case is about a quarter slower for it. Writing
-- one mapping is what happens constantly; ten thousand files vanishing is what
-- happens when a disk does not mount.
-- +goose StatementBegin
CREATE FUNCTION album_counts_recompute(album_ids uuid[]) RETURNS bigint
LANGUAGE plpgsql
SET plan_cache_mode = 'force_generic_plan'
AS $$
DECLARE
    corrected bigint;
BEGIN
    PERFORM 1 FROM albums WHERE albums.id = ANY (album_ids) ORDER BY albums.id FOR NO KEY UPDATE;

    UPDATE albums
       SET track_count = counted.track_count,
           owned_track_count = counted.owned_track_count,
           dismissed_track_count = counted.dismissed_track_count
      FROM (
            SELECT counted_album.id,
                   count(tracks.id)::integer AS track_count,
                   count(tracks.id) FILTER (WHERE decided.owned)::integer AS owned_track_count,
                   count(tracks.id) FILTER (
                       WHERE NOT decided.owned AND decided.dismissed
                   )::integer AS dismissed_track_count
              FROM albums AS counted_album
              LEFT JOIN tracks ON tracks.album_id = counted_album.id
              LEFT JOIN LATERAL (
                    SELECT
                        (EXISTS (
                            SELECT 1 FROM track_mappings
                            JOIN library_files
                              ON library_files.id = track_mappings.library_file_id
                             AND library_files.missing_at IS NULL
                            WHERE track_mappings.track_id = tracks.id
                        ) OR EXISTS (
                            SELECT 1 FROM library_file_identities
                            JOIN library_files
                              ON library_files.id = library_file_identities.library_file_id
                             AND library_files.missing_at IS NULL
                            WHERE library_file_identities.musicbrainz_recording_id
                                    = tracks.musicbrainz_recording_id
                        ) OR EXISTS (
                            SELECT 1 FROM tracks AS carrying
                            JOIN track_mappings
                              ON track_mappings.track_id = carrying.id
                            JOIN library_files
                              ON library_files.id = track_mappings.library_file_id
                             AND library_files.missing_at IS NULL
                            WHERE carrying.musicbrainz_recording_id
                                    = tracks.musicbrainz_recording_id
                        )) AS owned,
                        EXISTS (
                            SELECT 1 FROM acquisition_targets
                            WHERE tracks.musicbrainz_recording_id IS NOT NULL
                              AND acquisition_targets.musicbrainz_recording_id
                                    = tracks.musicbrainz_recording_id
                              AND acquisition_targets.status = 'not_wanted'
                        ) AS dismissed
              ) AS decided ON true
             WHERE counted_album.id = ANY (album_ids)
             GROUP BY counted_album.id
           ) AS counted
     WHERE albums.id = counted.id
       AND (albums.track_count, albums.owned_track_count, albums.dismissed_track_count)
           IS DISTINCT FROM
           (counted.track_count, counted.owned_track_count, counted.dismissed_track_count);

    GET DIAGNOSTICS corrected = ROW_COUNT;
    RETURN corrected;
END;
$$;
-- +goose StatementEnd

-- What a change touches, said the same way by every trigger below: the releases
-- it names outright, plus every release holding a track for a recording it
-- names. The second half is there because one file answers a recording wherever
-- that recording is listed — the single, the album and the compilation — so a
-- mapping written against one release moves the counts of the others.
-- +goose StatementBegin
CREATE FUNCTION album_counts_recompute_touched(album_ids uuid[], recording_ids uuid[])
RETURNS bigint LANGUAGE plpgsql
SET plan_cache_mode = 'force_generic_plan'
AS $$
BEGIN
    RETURN album_counts_recompute(ARRAY(
        SELECT unnest(album_ids)
        UNION
        SELECT tracks.album_id FROM tracks
        WHERE tracks.musicbrainz_recording_id = ANY (recording_ids)
    ));
END;
$$;
-- +goose StatementEnd

-- The repair. Recomputes every release from the tables the counts derive from
-- and answers with the number it had to correct.
-- +goose StatementBegin
CREATE FUNCTION album_counts_recompute_all() RETURNS bigint
LANGUAGE plpgsql AS $$
BEGIN
    RETURN album_counts_recompute(ARRAY(SELECT albums.id FROM albums ORDER BY albums.id));
END;
$$;
-- +goose StatementEnd

-- Tracks arriving, leaving or changing the recording they carry.
-- +goose StatementBegin
CREATE FUNCTION album_counts_from_tracks() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        PERFORM album_counts_recompute_touched(
            ARRAY(SELECT DISTINCT before.album_id FROM before),
            ARRAY(SELECT DISTINCT before.musicbrainz_recording_id FROM before
                   WHERE before.musicbrainz_recording_id IS NOT NULL));
    END IF;
    IF TG_OP <> 'DELETE' THEN
        PERFORM album_counts_recompute_touched(
            ARRAY(SELECT DISTINCT after.album_id FROM after),
            ARRAY(SELECT DISTINCT after.musicbrainz_recording_id FROM after
                   WHERE after.musicbrainz_recording_id IS NOT NULL));
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER album_counts_tracks_insert AFTER INSERT ON tracks
    REFERENCING NEW TABLE AS after
    FOR EACH STATEMENT EXECUTE FUNCTION album_counts_from_tracks();
CREATE TRIGGER album_counts_tracks_update AFTER UPDATE ON tracks
    REFERENCING OLD TABLE AS before NEW TABLE AS after
    FOR EACH STATEMENT EXECUTE FUNCTION album_counts_from_tracks();
CREATE TRIGGER album_counts_tracks_delete AFTER DELETE ON tracks
    REFERENCING OLD TABLE AS before
    FOR EACH STATEMENT EXECUTE FUNCTION album_counts_from_tracks();

-- Mappings written, moved or withdrawn — including the ones that go when a
-- library file or a track is deleted, which no Go call site writes itself.
-- +goose StatementBegin
CREATE FUNCTION album_counts_from_track_mappings() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        PERFORM album_counts_recompute_touched(
            ARRAY(SELECT DISTINCT tracks.album_id FROM before
                    JOIN tracks ON tracks.id = before.track_id),
            ARRAY(SELECT DISTINCT tracks.musicbrainz_recording_id FROM before
                    JOIN tracks ON tracks.id = before.track_id
                   WHERE tracks.musicbrainz_recording_id IS NOT NULL));
    END IF;
    IF TG_OP <> 'DELETE' THEN
        PERFORM album_counts_recompute_touched(
            ARRAY(SELECT DISTINCT tracks.album_id FROM after
                    JOIN tracks ON tracks.id = after.track_id),
            ARRAY(SELECT DISTINCT tracks.musicbrainz_recording_id FROM after
                    JOIN tracks ON tracks.id = after.track_id
                   WHERE tracks.musicbrainz_recording_id IS NOT NULL));
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER album_counts_track_mappings_insert AFTER INSERT ON track_mappings
    REFERENCING NEW TABLE AS after
    FOR EACH STATEMENT EXECUTE FUNCTION album_counts_from_track_mappings();
CREATE TRIGGER album_counts_track_mappings_update AFTER UPDATE ON track_mappings
    REFERENCING OLD TABLE AS before NEW TABLE AS after
    FOR EACH STATEMENT EXECUTE FUNCTION album_counts_from_track_mappings();
CREATE TRIGGER album_counts_track_mappings_delete AFTER DELETE ON track_mappings
    REFERENCING OLD TABLE AS before
    FOR EACH STATEMENT EXECUTE FUNCTION album_counts_from_track_mappings();

-- A file going missing or coming back. Only that: the scanner writes every file
-- it still sees on every scan and clears missing_at while it is there, so a
-- trigger that read every written row would walk the whole catalogue each time
-- the library is walked. What matters is whether the file is on disk, not when
-- it was last seen, so the two sides are compared and the no-ops dropped before
-- anything else runs. The comparison is what a column list on the trigger would
-- have done more cheaply, but a trigger with a column list cannot have
-- transition tables, and seeing the whole statement at once is worth more here
-- than being woken less often.
--
-- The comparison is what saves the walk: without it every file the scanner
-- wrote would be a file to chase releases for. The early return after it saves
-- two empty index lookups and a call, which is smaller than it looks — and
-- neither guard can move a count, because album_counts_recompute writes only
-- what it finds changed. Both are cost, and only the clock can show either
-- missing.
--
-- Insert needs no trigger: a file nothing points at yet cannot own anything.
-- Delete needs none either — it cascades into mappings and identities, and
-- those triggers answer for it.
-- +goose StatementBegin
CREATE FUNCTION album_counts_from_library_files() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    moved uuid[];
BEGIN
    moved := ARRAY(
        SELECT after.id FROM after
        JOIN before ON before.id = after.id
        WHERE (before.missing_at IS NULL) <> (after.missing_at IS NULL)
    );
    IF cardinality(moved) = 0 THEN
        RETURN NULL;
    END IF;

    PERFORM album_counts_recompute_touched(
        ARRAY(SELECT DISTINCT tracks.album_id FROM track_mappings
                JOIN tracks ON tracks.id = track_mappings.track_id
               WHERE track_mappings.library_file_id = ANY (moved)),
        ARRAY(SELECT DISTINCT tracks.musicbrainz_recording_id FROM track_mappings
                JOIN tracks ON tracks.id = track_mappings.track_id
               WHERE track_mappings.library_file_id = ANY (moved)
                 AND tracks.musicbrainz_recording_id IS NOT NULL
               UNION
              SELECT DISTINCT library_file_identities.musicbrainz_recording_id
                FROM library_file_identities
               WHERE library_file_identities.library_file_id = ANY (moved)
                 AND library_file_identities.musicbrainz_recording_id IS NOT NULL));
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER album_counts_library_files_update AFTER UPDATE ON library_files
    REFERENCING OLD TABLE AS before NEW TABLE AS after
    FOR EACH STATEMENT EXECUTE FUNCTION album_counts_from_library_files();

-- A file being identified as a recording, or that identification being taken
-- back. It owns the recording wherever the recording is listed, so there is no
-- release to name outright — only recordings.
-- +goose StatementBegin
CREATE FUNCTION album_counts_from_library_file_identities() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        PERFORM album_counts_recompute_touched('{}'::uuid[],
            ARRAY(SELECT DISTINCT before.musicbrainz_recording_id FROM before
                   WHERE before.musicbrainz_recording_id IS NOT NULL));
    END IF;
    IF TG_OP <> 'DELETE' THEN
        PERFORM album_counts_recompute_touched('{}'::uuid[],
            ARRAY(SELECT DISTINCT after.musicbrainz_recording_id FROM after
                   WHERE after.musicbrainz_recording_id IS NOT NULL));
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER album_counts_identities_insert AFTER INSERT ON library_file_identities
    REFERENCING NEW TABLE AS after
    FOR EACH STATEMENT EXECUTE FUNCTION album_counts_from_library_file_identities();
CREATE TRIGGER album_counts_identities_update AFTER UPDATE ON library_file_identities
    REFERENCING OLD TABLE AS before NEW TABLE AS after
    FOR EACH STATEMENT EXECUTE FUNCTION album_counts_from_library_file_identities();
CREATE TRIGGER album_counts_identities_delete AFTER DELETE ON library_file_identities
    REFERENCING OLD TABLE AS before
    FOR EACH STATEMENT EXECUTE FUNCTION album_counts_from_library_file_identities();

-- A want being dismissed, or un-dismissed. Only not_wanted moves a count, and
-- both sides are read so that a target leaving that status moves it back; every
-- other status change here finds no recording to answer for and stops.
-- +goose StatementBegin
CREATE FUNCTION album_counts_from_acquisition_targets() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        PERFORM album_counts_recompute_touched('{}'::uuid[],
            ARRAY(SELECT DISTINCT before.musicbrainz_recording_id FROM before
                   WHERE before.musicbrainz_recording_id IS NOT NULL
                     AND before.status = 'not_wanted'));
    END IF;
    IF TG_OP <> 'DELETE' THEN
        PERFORM album_counts_recompute_touched('{}'::uuid[],
            ARRAY(SELECT DISTINCT after.musicbrainz_recording_id FROM after
                   WHERE after.musicbrainz_recording_id IS NOT NULL
                     AND after.status = 'not_wanted'));
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER album_counts_acquisition_targets_insert AFTER INSERT ON acquisition_targets
    REFERENCING NEW TABLE AS after
    FOR EACH STATEMENT EXECUTE FUNCTION album_counts_from_acquisition_targets();
CREATE TRIGGER album_counts_acquisition_targets_update AFTER UPDATE ON acquisition_targets
    REFERENCING OLD TABLE AS before NEW TABLE AS after
    FOR EACH STATEMENT EXECUTE FUNCTION album_counts_from_acquisition_targets();
CREATE TRIGGER album_counts_acquisition_targets_delete AFTER DELETE ON acquisition_targets
    REFERENCING OLD TABLE AS before
    FOR EACH STATEMENT EXECUTE FUNCTION album_counts_from_acquisition_targets();

-- The columns start at zero, which is only true for a catalogue with no tracks
-- in it. Fill them from what is already there.
SELECT album_counts_recompute_all();

-- +goose Down
DROP TRIGGER IF EXISTS album_counts_acquisition_targets_delete ON acquisition_targets;
DROP TRIGGER IF EXISTS album_counts_acquisition_targets_update ON acquisition_targets;
DROP TRIGGER IF EXISTS album_counts_acquisition_targets_insert ON acquisition_targets;
DROP TRIGGER IF EXISTS album_counts_identities_delete ON library_file_identities;
DROP TRIGGER IF EXISTS album_counts_identities_update ON library_file_identities;
DROP TRIGGER IF EXISTS album_counts_identities_insert ON library_file_identities;
DROP TRIGGER IF EXISTS album_counts_library_files_update ON library_files;
DROP TRIGGER IF EXISTS album_counts_track_mappings_delete ON track_mappings;
DROP TRIGGER IF EXISTS album_counts_track_mappings_update ON track_mappings;
DROP TRIGGER IF EXISTS album_counts_track_mappings_insert ON track_mappings;
DROP TRIGGER IF EXISTS album_counts_tracks_delete ON tracks;
DROP TRIGGER IF EXISTS album_counts_tracks_update ON tracks;
DROP TRIGGER IF EXISTS album_counts_tracks_insert ON tracks;
DROP FUNCTION IF EXISTS album_counts_from_acquisition_targets();
DROP FUNCTION IF EXISTS album_counts_from_library_file_identities();
DROP FUNCTION IF EXISTS album_counts_from_library_files();
DROP FUNCTION IF EXISTS album_counts_from_track_mappings();
DROP FUNCTION IF EXISTS album_counts_from_tracks();
DROP FUNCTION IF EXISTS album_counts_recompute_all();
DROP FUNCTION IF EXISTS album_counts_recompute_touched(uuid[], uuid[]);
DROP FUNCTION IF EXISTS album_counts_recompute(uuid[]);
ALTER TABLE albums
    DROP CONSTRAINT IF EXISTS albums_counts_within_track_count,
    DROP COLUMN IF EXISTS dismissed_track_count,
    DROP COLUMN IF EXISTS owned_track_count,
    DROP COLUMN IF EXISTS track_count;
