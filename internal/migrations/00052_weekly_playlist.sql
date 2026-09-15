-- +goose Up
-- The weekly playlist, and the lease that is the only licence to delete music
-- (ADR 0021).
--
-- Schall recommends recordings today and stops there: a suggestion becomes a
-- want only when a person presses "Want it". The weekly playlist is the step
-- after that. Once a week, unattended, Schall picks a few recommended songs,
-- obtains them the ordinary way, and puts them on a playlist the user's player
-- can see. A song nobody kept is deleted again, so the collection does not grow
-- by twenty songs a week forever.
--
-- Deleting music is the one thing Schall does that cannot be undone, so the
-- authority to do it is written down rather than inferred. A file may be deleted
-- automatically only while a lease row says so, and a lease exists only because
-- one weekly run obtained one file for one want. Nothing else in the product can
-- write one.
--
-- Every table here exists because one fact in this feature has a lifetime no
-- existing table has: a run is a bounded event, a lease crosses runs and outlives
-- its file, a keep read is evidence about one signal in one run, and an unkept
-- recording keeps its own clock long after both.

-- A playlist is a want list with a source (ADR 0008). The weekly list is a local
-- list: unlike a Spotify or Navidrome list it names no remote object, so it joins
-- 'manual' on the source_id exception.
ALTER TABLE playlists
    DROP CONSTRAINT playlists_source_valid,
    ADD CONSTRAINT playlists_source_valid
        CHECK (source IN ('spotify', 'manual', 'navidrome', 'weekly')),
    DROP CONSTRAINT playlists_source_id_matches_source,
    ADD CONSTRAINT playlists_source_id_matches_source
        CHECK ((source IN ('manual', 'weekly')) = (source_id IS NULL));

-- "The weekly playlist" is one object that is rewritten every week, not a new
-- object each week. The runs below are what carry the history.
CREATE UNIQUE INDEX playlists_weekly_singleton_idx
    ON playlists ((source = 'weekly'))
    WHERE source = 'weekly';

-- A want that was obtained and then removed keeps the stamp saying it arrived.
-- `acquisition_targets` is the store of recordings Schall is trying to obtain
-- (00024). Its old constraint was a biconditional — the status is 'acquired'
-- exactly when acquired_at is set — so moving an obtained target on to
-- 'not_wanted' would have forced the removal to erase the fact that the song was
-- ever here. The two halves that were meant are kept apart: 'acquired' still
-- requires the stamp, and the stamp may now remain on a target that has been
-- settled.
ALTER TABLE acquisition_targets
    DROP CONSTRAINT acquisition_targets_acquired_consistent,
    ADD CONSTRAINT acquisition_targets_acquired_stamped
        CHECK (status <> 'acquired' OR acquired_at IS NOT NULL),
    ADD CONSTRAINT acquisition_targets_acquired_stamp_kept
        CHECK (acquired_at IS NULL
               OR status IN ('acquired', 'not_wanted', 'superseded'));

-- Whether the weekly playlist runs at all, how big it is, and whether it may
-- remove anything. It ships switched off: a feature that deletes music starts
-- when a person turns it on and not when a container restarts. Mode is here
-- rather than in code so that switching removals off is one press on a settings
-- screen, which is the control somebody reaches for when something looks wrong.
CREATE TABLE weekly_playlist_settings (
    singleton BOOLEAN PRIMARY KEY DEFAULT true,
    enabled BOOLEAN NOT NULL DEFAULT false,
    songs_per_week INTEGER NOT NULL DEFAULT 20,
    mode TEXT NOT NULL DEFAULT 'remove',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT weekly_playlist_settings_single_row CHECK (singleton),
    CONSTRAINT weekly_playlist_settings_mode_valid
        CHECK (mode IN ('report', 'remove')),
    -- An upper bound as well as a lower one. Every song is a search against
    -- Soulseek, which bans a client that searches quickly, and a week's list
    -- nobody can listen through is a week of deletions.
    CONSTRAINT weekly_playlist_settings_size_sane
        CHECK (songs_per_week BETWEEN 1 AND 50)
);

-- One refresh, from the moment it starts to the account it leaves behind. Mode
-- is copied onto the run because the report a person reads a month later has to
-- say what the rules were that week, not what they are now. Status 'partial' is
-- the ordinary outcome of a week when Soulseek was slow, not a failure.
CREATE TABLE weekly_playlist_runs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    playlist_id UUID REFERENCES playlists(id) ON DELETE SET NULL,
    mode TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'running',
    detail TEXT NOT NULL DEFAULT '',
    chosen_count INTEGER NOT NULL DEFAULT 0,
    wanted_count INTEGER NOT NULL DEFAULT 0,
    arrived_count INTEGER NOT NULL DEFAULT 0,
    kept_count INTEGER NOT NULL DEFAULT 0,
    extended_count INTEGER NOT NULL DEFAULT 0,
    leaving_count INTEGER NOT NULL DEFAULT 0,
    removed_count INTEGER NOT NULL DEFAULT 0,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    CONSTRAINT weekly_playlist_runs_mode_valid
        CHECK (mode IN ('report', 'remove')),
    CONSTRAINT weekly_playlist_runs_status_valid
        CHECK (status IN ('running', 'complete', 'partial', 'failed')),
    CONSTRAINT weekly_playlist_runs_partial_explained
        CHECK (status NOT IN ('partial', 'failed') OR btrim(detail) <> ''),
    CONSTRAINT weekly_playlist_runs_finished_when_settled
        CHECK ((status = 'running') = (finished_at IS NULL)),
    CONSTRAINT weekly_playlist_runs_counts_nonnegative
        CHECK (chosen_count >= 0 AND wanted_count >= 0 AND arrived_count >= 0
               AND kept_count >= 0 AND extended_count >= 0
               AND leaving_count >= 0 AND removed_count >= 0)
);

-- One run at a time, in the shape the acquisition sweep's job index already
-- uses.
CREATE UNIQUE INDEX weekly_playlist_runs_live_idx
    ON weekly_playlist_runs ((status = 'running'))
    WHERE status = 'running';

CREATE INDEX weekly_playlist_runs_recent_idx
    ON weekly_playlist_runs (started_at DESC);

-- The lease: this file was obtained to fill the weekly playlist and may be
-- deleted until it earns a keep. It is the only thing in Schall that makes a
-- file eligible for automatic deletion, and run_id and acquisition_target_id are
-- NOT NULL because that authority comes from the run that granted it and the
-- want it filled. A file that arrived any other way — a scan, an import, a want
-- the user made — carries no lease and can never be reached from here.
--
-- The row outlives the file on purpose. library_file_id is set to NULL when the
-- audio goes, and library_path is copied here when the lease is granted, so a run
-- can always say afterwards what it removed.
CREATE TABLE library_file_leases (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    library_file_id UUID REFERENCES library_files(id) ON DELETE SET NULL,
    library_path TEXT NOT NULL,
    musicbrainz_recording_id UUID NOT NULL,
    run_id UUID NOT NULL
        REFERENCES weekly_playlist_runs(id) ON DELETE RESTRICT,
    acquisition_target_id UUID NOT NULL
        REFERENCES acquisition_targets(id) ON DELETE RESTRICT,
    state TEXT NOT NULL DEFAULT 'held',
    granted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    extensions INTEGER NOT NULL DEFAULT 0,
    extended_reason TEXT NOT NULL DEFAULT '',
    -- The grace week. A run that finds no keep sets these and deletes nothing;
    -- the run whose date has arrived reads again and carries it out.
    removes_at TIMESTAMPTZ,
    leaving_run_id UUID REFERENCES weekly_playlist_runs(id) ON DELETE SET NULL,
    -- Which signal kept it, and the run that settled it.
    kept_by TEXT,
    released_at TIMESTAMPTZ,
    removed_at TIMESTAMPTZ,
    settled_run_id UUID REFERENCES weekly_playlist_runs(id) ON DELETE SET NULL,
    -- held     — on trial: obtained this week, nobody has judged it yet
    -- leaving  — no keep was found, and the next refresh carries it out
    -- kept     — a signal said keep; the file is the user's like any other
    -- removed  — Schall deleted it
    -- gone     — the file left by another hand before the lease was judged, so
    --            there is nothing to delete and nothing was decided
    CONSTRAINT library_file_leases_state_valid
        CHECK (state IN ('held', 'leaving', 'kept', 'removed', 'gone')),
    CONSTRAINT library_file_leases_path_present
        CHECK (btrim(library_path) <> ''),
    CONSTRAINT library_file_leases_expiry_after_grant
        CHECK (expires_at > granted_at),
    CONSTRAINT library_file_leases_extensions_nonnegative
        CHECK (extensions >= 0),
    -- A leaving lease says when and who decided it. A lease that went back to
    -- held because the second read failed clears both again.
    CONSTRAINT library_file_leases_leaving_dated
        CHECK (state <> 'leaving'
               OR (removes_at IS NOT NULL AND leaving_run_id IS NOT NULL)),
    CONSTRAINT library_file_leases_departure_only_after_notice
        CHECK (removes_at IS NULL
               OR state IN ('leaving', 'kept', 'removed')),
    -- Nothing is removed without having been announced first.
    CONSTRAINT library_file_leases_removal_announced
        CHECK (state <> 'removed' OR removes_at IS NOT NULL),
    -- A kept lease names the signal that kept it; nothing else does.
    CONSTRAINT library_file_leases_kept_evidenced
        CHECK ((state = 'kept')
               = (kept_by IS NOT NULL AND btrim(kept_by) <> '')),
    -- Two ways a lease ends without a deletion: the song earned a keep, or the
    -- file left by another hand. Both are stamped when they happen.
    CONSTRAINT library_file_leases_release_stamped
        CHECK ((state IN ('kept', 'gone')) = (released_at IS NOT NULL)),
    CONSTRAINT library_file_leases_removed_stamped
        CHECK ((state = 'removed') = (removed_at IS NOT NULL)),
    -- A lease is only gone once its file is, which is what makes it different
    -- from a removal: nothing was deleted here and nothing was decided.
    CONSTRAINT library_file_leases_gone_has_no_file
        CHECK (state <> 'gone' OR library_file_id IS NULL),
    -- Deliberately no constraint requiring a live lease to point at a file. A
    -- person may delete a leased file by hand from the duplicates screen, and a
    -- constraint here would refuse their delete. A held lease whose file has gone
    -- is settled by the next refresh as removed-by-someone-else, with nothing to
    -- delete and no want to settle.
    --
    -- A run may only be named on a settled lease, and a removal always has one. A
    -- keep names none when the user pressed Keep on a Tuesday.
    CONSTRAINT library_file_leases_settled_run_scoped
        CHECK (settled_run_id IS NULL OR state IN ('kept', 'removed', 'gone')),
    CONSTRAINT library_file_leases_removal_has_a_run
        CHECK (state <> 'removed' OR settled_run_id IS NOT NULL),
    CONSTRAINT library_file_leases_gone_has_a_run
        CHECK (state <> 'gone' OR settled_run_id IS NOT NULL)
);

-- One file carries at most one live lease. A kept or removed lease is history and
-- blocks nothing.
CREATE UNIQUE INDEX library_file_leases_live_idx
    ON library_file_leases (library_file_id)
    WHERE state IN ('held', 'leaving') AND library_file_id IS NOT NULL;

-- What a refresh asks for twice: which leases are due to be judged, and which
-- announced departures have arrived.
CREATE INDEX library_file_leases_due_idx
    ON library_file_leases (expires_at)
    WHERE state = 'held';

CREATE INDEX library_file_leases_leaving_idx
    ON library_file_leases (removes_at)
    WHERE state = 'leaving';

CREATE INDEX library_file_leases_run_idx ON library_file_leases (run_id);

CREATE INDEX library_file_leases_target_idx
    ON library_file_leases (acquisition_target_id);

-- What was read about a leased file, what it said, and when. The three outcomes
-- are the point: 'absent' may allow a removal and 'unreadable' never may, so a
-- client that collapses them is the one bug this feature must not have. Navidrome
-- being unreachable on a Sunday must not read as "the user kept nothing this
-- week".
CREATE TABLE library_file_keep_reads (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    lease_id UUID NOT NULL
        REFERENCES library_file_leases(id) ON DELETE CASCADE,
    -- Every signal Schall reads is read by a run. The user's own Keep is not read
    -- at all: they press it on a Tuesday, and it names no run.
    run_id UUID REFERENCES weekly_playlist_runs(id) ON DELETE CASCADE,
    signal TEXT NOT NULL,
    outcome TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT '',
    read_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Two signals, and each one only ever keeps. A star is the plainest statement
    -- of "I liked this" the user already makes in their player; the Keep button is
    -- the answer to "I never star anything" and the one signal that can never be
    -- unreadable. A third signal is a one-line migration, not a redesign.
    CONSTRAINT library_file_keep_reads_signal_valid
        CHECK (signal IN ('navidrome_star', 'schall_keep')),
    CONSTRAINT library_file_keep_reads_outcome_valid
        CHECK (outcome IN ('kept', 'absent', 'unreadable')),
    -- A read that failed says why. A run report that says "could not read"
    -- without saying what happened is something a person can only press retry on.
    CONSTRAINT library_file_keep_reads_failure_explained
        CHECK (outcome <> 'unreadable' OR btrim(detail) <> ''),
    -- A pressed Keep is not a read. It exists only when it happened, so it is
    -- always a keep, and it names no run because a person presses it on a
    -- Tuesday. Every other signal is read by a run and names it.
    CONSTRAINT library_file_keep_reads_pressed_keep_is_not_a_read
        CHECK ((signal = 'schall_keep')
               = (run_id IS NULL AND outcome = 'kept')),
    -- And the other way round: a signal Schall goes out and reads always names
    -- the run that read it. Without this an ordinary "not starred" answer could
    -- be written with no run against it, and the run report — which is the whole
    -- account of why a song was removed — would not show it.
    CONSTRAINT library_file_keep_reads_read_names_its_run
        CHECK (signal = 'schall_keep' OR run_id IS NOT NULL),
    -- One answer per signal per lease per run.
    UNIQUE (lease_id, run_id, signal)
);

CREATE INDEX library_file_keep_reads_run_idx
    ON library_file_keep_reads (run_id);

CREATE INDEX library_file_keep_reads_lease_idx
    ON library_file_keep_reads (lease_id, read_at DESC);

-- Acquired, delivered, listened past and not kept. Stronger than an ignored
-- impression, weaker than a dismissal, and its own store because it is its own
-- statement: the user never said "not interested". Folding it into
-- recommendation_dismissals would turn one indifferent week into a permanent
-- refusal. The window is a named value in the service, not a default here, so
-- retuning it needs no migration.
CREATE TABLE recommendation_unkept (
    musicbrainz_recording_id UUID PRIMARY KEY,
    unkept_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    suppressed_until TIMESTAMPTZ NOT NULL,
    occurrences INTEGER NOT NULL DEFAULT 1,
    run_id UUID REFERENCES weekly_playlist_runs(id) ON DELETE SET NULL,
    CONSTRAINT recommendation_unkept_window_ordered
        CHECK (suppressed_until > unkept_at),
    CONSTRAINT recommendation_unkept_occurrences_positive
        CHECK (occurrences >= 1)
);

-- One weekly refresh queued or running at a time, in the shape 00048 gave the
-- recommendation sweep.
CREATE UNIQUE INDEX jobs_weekly_playlist_refresh_idx
    ON jobs (kind)
    WHERE kind = 'refresh_weekly_playlist' AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX IF EXISTS jobs_weekly_playlist_refresh_idx;
DROP TABLE IF EXISTS recommendation_unkept;
DROP TABLE IF EXISTS library_file_keep_reads;
DROP TABLE IF EXISTS library_file_leases;
DROP INDEX IF EXISTS weekly_playlist_runs_recent_idx;
DROP INDEX IF EXISTS weekly_playlist_runs_live_idx;
DROP TABLE IF EXISTS weekly_playlist_runs;
DROP TABLE IF EXISTS weekly_playlist_settings;
DROP INDEX IF EXISTS playlists_weekly_singleton_idx;
DELETE FROM playlists WHERE source = 'weekly';
UPDATE acquisition_targets SET acquired_at = NULL WHERE status <> 'acquired';
ALTER TABLE acquisition_targets
    DROP CONSTRAINT acquisition_targets_acquired_stamp_kept,
    DROP CONSTRAINT acquisition_targets_acquired_stamped,
    ADD CONSTRAINT acquisition_targets_acquired_consistent
        CHECK ((status = 'acquired') = (acquired_at IS NOT NULL));
ALTER TABLE playlists
    DROP CONSTRAINT playlists_source_id_matches_source,
    ADD CONSTRAINT playlists_source_id_matches_source
        CHECK ((source = 'manual') = (source_id IS NULL)),
    DROP CONSTRAINT playlists_source_valid,
    ADD CONSTRAINT playlists_source_valid
        CHECK (source IN ('spotify', 'manual', 'navidrome'));
