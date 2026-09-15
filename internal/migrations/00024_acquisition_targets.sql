-- +goose Up
-- Nothing in Schall has ever said "we want this recording". A download request
-- is a folder somebody already chose, and the Missing view is a computation
-- over followed artists — neither survives the question being asked about a
-- song nobody follows an artist for, and neither is something a retry can be
-- attached to.
--
-- A target is the want itself. The recording ID is an answer written into it
-- rather than the row's identity: that is what lets a wrong resolution be
-- corrected from the review queue without losing the fact that the music is
-- wanted, and it is why the entry evidence below is kept verbatim instead of
-- being replaced by whatever was concluded from it.
CREATE TABLE acquisition_targets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Why this target exists. Provenance, not behaviour: nothing branches on
    -- it, but a target nobody remembers asking for has to be able to say where
    -- it came from. A playlist reference joins this when playlists exist.
    origin TEXT NOT NULL,
    origin_album_id UUID REFERENCES albums(id) ON DELETE SET NULL,

    -- The entry as whoever asked for it wrote it, and the only input a later
    -- re-resolution has. A blank string is silence, exactly as an absent tag
    -- is; it is never read as agreement with anything.
    entry_artist TEXT NOT NULL DEFAULT '',
    entry_title TEXT NOT NULL DEFAULT '',
    entry_album TEXT NOT NULL DEFAULT '',
    entry_duration_ms INTEGER,
    entry_isrc TEXT,

    -- The answer. musicbrainz_release_group_id is the release context the
    -- resolution ran through, kept because it is the first rule of the
    -- release-choice order at import time. It is stored as a MusicBrainz
    -- identifier rather than as a catalogue row, because the release may not be
    -- in the catalogue when the target is created and a want must not wait for
    -- somebody to follow an artist.
    musicbrainz_recording_id UUID,
    musicbrainz_release_group_id UUID,
    resolution_method TEXT,
    resolved_at TIMESTAMPTZ,

    status TEXT NOT NULL DEFAULT 'unresolved',
    -- Which question the review queue is holding. 'resolution' is "which
    -- recording is this entry", 'candidate' is "is this file that recording".
    -- They are different questions with different answers and must never
    -- render as one.
    review_reason TEXT,
    -- summary says in one sentence why the target is in the state it is in, in
    -- every state, because a status nobody can explain is a status nobody
    -- trusts. last_error is only ever about Schall failing to ask — never about
    -- the music — exactly as resolution_summary and resolution_error are kept
    -- apart on library_files.
    summary TEXT NOT NULL DEFAULT '',
    last_error TEXT,

    -- The requeue. next_attempt_at is the schedule of record and lives here
    -- rather than in a queued job: a want outlives any single attempt, the job
    -- table is never purged, and stopping a target has to be one update rather
    -- than a hunt through jobs. NULL means nothing is scheduled — the target is
    -- in flight, waiting on a person, or finished.
    attempts INTEGER NOT NULL DEFAULT 0,
    last_attempt_at TIMESTAMPTZ,
    next_attempt_at TIMESTAMPTZ,

    acquired_library_file_id UUID REFERENCES library_files(id) ON DELETE SET NULL,
    acquired_at TIMESTAMPTZ,
    not_wanted_at TIMESTAMPTZ,
    -- Two entries that resolve to one recording are one want. The loser keeps
    -- its own origin and points at the survivor, so whatever asked for it can
    -- follow the trail rather than finding a row that silently vanished.
    superseded_by_id UUID REFERENCES acquisition_targets(id) ON DELETE RESTRICT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT acquisition_targets_status_valid CHECK (status IN (
        'unresolved', 'pending', 'searching', 'awaiting_review',
        'acquired', 'not_wanted', 'superseded'
    )),
    -- There is deliberately no 'failed'. An exhausted search is a target that
    -- is still wanted and has not been found yet: peers come and go, and the
    -- file may be there tomorrow. Every failure returns to 'pending' or
    -- 'unresolved' carrying a reason and a next attempt.
    CONSTRAINT acquisition_targets_origin_valid
        CHECK (origin IN ('manual', 'playlist', 'follow_feed')),
    -- A target with nothing to resolve and nothing to search for is not a
    -- target, and what it wants is never guessed at from the rest of the row.
    CONSTRAINT acquisition_targets_entry_identifiable
        CHECK (btrim(entry_title) <> '' OR entry_isrc IS NOT NULL),
    -- The state and the answer can never disagree about whether the entry has
    -- been resolved, in either direction.
    CONSTRAINT acquisition_targets_identified_when_actionable
        CHECK (status NOT IN ('pending', 'searching', 'acquired')
               OR musicbrainz_recording_id IS NOT NULL),
    CONSTRAINT acquisition_targets_unresolved_unidentified
        CHECK (status <> 'unresolved' OR musicbrainz_recording_id IS NULL),
    CONSTRAINT acquisition_targets_review_reason_valid
        CHECK (review_reason IS NULL OR review_reason IN ('resolution', 'candidate')),
    CONSTRAINT acquisition_targets_review_consistent
        CHECK ((status = 'awaiting_review') = (review_reason IS NOT NULL)),
    -- Asking whether a file is the target recording presupposes one.
    CONSTRAINT acquisition_targets_candidate_review_identified
        CHECK (review_reason <> 'candidate' OR musicbrainz_recording_id IS NOT NULL),
    CONSTRAINT acquisition_targets_acquired_consistent
        CHECK ((status = 'acquired') = (acquired_at IS NOT NULL)),
    CONSTRAINT acquisition_targets_not_wanted_consistent
        CHECK ((status = 'not_wanted') = (not_wanted_at IS NOT NULL)),
    CONSTRAINT acquisition_targets_superseded_consistent
        CHECK ((status = 'superseded') = (superseded_by_id IS NOT NULL)),
    -- Only a target waiting for its next chance carries a schedule. One in
    -- flight, in review, or finished would be a second opinion about what
    -- happens to it next.
    CONSTRAINT acquisition_targets_schedule_consistent
        CHECK (next_attempt_at IS NULL OR status IN ('unresolved', 'pending')),
    CONSTRAINT acquisition_targets_attempts_nonnegative CHECK (attempts >= 0),
    CONSTRAINT acquisition_targets_duration_nonnegative
        CHECK (entry_duration_ms IS NULL OR entry_duration_ms >= 0),
    -- A target cannot supersede itself.
    CONSTRAINT acquisition_targets_superseded_elsewhere
        CHECK (superseded_by_id IS NULL OR superseded_by_id <> id)
);

-- One live target per recording. This is what makes a re-imported playlist
-- idempotent, and it is also what makes "not wanted" stick: a target somebody
-- stopped pursuing keeps its recording, so re-importing the playlist that
-- created it cannot quietly resurrect it. Only a superseded row is exempt,
-- because it holds the same recording as the survivor it points at.
CREATE UNIQUE INDEX acquisition_targets_recording_idx
    ON acquisition_targets (musicbrainz_recording_id)
    WHERE musicbrainz_recording_id IS NOT NULL AND status <> 'superseded';

-- The targets whose turn it is. Everything else is invisible to the sweeper,
-- which is the point: the queue is a walk over what is due rather than a scan
-- of every want ever recorded.
CREATE INDEX acquisition_targets_due_idx
    ON acquisition_targets (next_attempt_at, created_at)
    WHERE status IN ('unresolved', 'pending');

CREATE INDEX acquisition_targets_status_idx
    ON acquisition_targets (status, created_at DESC);

-- What each attempt at a target came to, kept beside the target rather than on
-- it, in the shape download_transfer_attempts already uses for files. The
-- target row stays the current state, which is what the rest of the system
-- reads; this is append-only and is never consulted to decide anything.
--
-- 'none' and 'failed' are separate for the reason source_searches keeps them
-- separate: nobody having it and nobody having managed to look are different
-- answers, and only one of them says anything about the music.
CREATE TABLE acquisition_target_attempts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    acquisition_target_id UUID NOT NULL
        REFERENCES acquisition_targets(id) ON DELETE CASCADE,
    -- Which attempt this was, matching acquisition_targets.attempts at the time
    -- it settled, so a row can be read on its own rather than by counting.
    attempt INTEGER NOT NULL,
    outcome TEXT NOT NULL,
    -- detail is whatever words the thing that failed used, kept rather than
    -- paraphrased: a provider's own message is the only account of the failure
    -- anybody has.
    detail TEXT,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT acquisition_target_attempts_outcome_valid CHECK (outcome IN (
        'resolved', 'inconclusive', 'none', 'rejected', 'acquired', 'failed'
    )),
    CONSTRAINT acquisition_target_attempts_attempt_positive CHECK (attempt >= 1)
);

CREATE INDEX acquisition_target_attempts_target_idx
    ON acquisition_target_attempts (acquisition_target_id, recorded_at);

-- A single-file acquisition is still a download request, and it has to be able
-- to say which want it was serving. Nullable, because every request recorded
-- until now was a whole release somebody chose by hand, and those answer to
-- nothing but themselves.
ALTER TABLE download_requests
    ADD COLUMN acquisition_target_id UUID
        REFERENCES acquisition_targets(id) ON DELETE SET NULL;

CREATE INDEX download_requests_acquisition_target_idx
    ON download_requests (acquisition_target_id)
    WHERE acquisition_target_id IS NOT NULL;

-- One sweeper, enforced by the table rather than hoped for, exactly as the
-- transfer poller is. It turns targets that are due into per-target jobs; it
-- never does the work itself.
CREATE UNIQUE INDEX jobs_acquisition_sweep_idx
    ON jobs (kind)
    WHERE kind = 'sweep_acquisition_targets' AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX IF EXISTS jobs_acquisition_sweep_idx;

DROP INDEX IF EXISTS download_requests_acquisition_target_idx;
ALTER TABLE download_requests DROP COLUMN IF EXISTS acquisition_target_id;

DROP TABLE IF EXISTS acquisition_target_attempts;
DROP TABLE IF EXISTS acquisition_targets;
