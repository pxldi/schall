-- +goose Up
-- A dismissal is a permanent recommendation decision at exactly the level the
-- user chose. The MBID deliberately has no catalogue foreign key: an external
-- recommendation can be dismissed without importing it into the catalogue.
CREATE TABLE recommendation_dismissals (
    subject_type TEXT NOT NULL,
    musicbrainz_id UUID NOT NULL,
    dismissed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (subject_type, musicbrainz_id),
    CONSTRAINT recommendation_dismissals_subject_valid
        CHECK (subject_type IN ('recording', 'release_group', 'artist'))
);

-- Impression fatigue is bounded state, not a render log. Policy constants and
-- the atomic transition live in the recommendation service so retuning them does
-- not require a migration.
CREATE TABLE recommendation_impressions (
    musicbrainz_recording_id UUID PRIMARY KEY,
    ignored_impressions INTEGER NOT NULL DEFAULT 0,
    last_shown_at TIMESTAMPTZ NOT NULL,
    suppressed_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT recommendation_impressions_count_nonnegative
        CHECK (ignored_impressions >= 0),
    CONSTRAINT recommendation_impressions_window_ordered
        CHECK (suppressed_until IS NULL OR suppressed_until > last_shown_at)
);

-- One row is the current usable answer from one source. A failed fetch does not
-- touch it; a complete or explained-partial fetch upserts it in the same
-- transaction that replaces its candidates.
CREATE TABLE recommendation_snapshots (
    source TEXT PRIMARY KEY,
    source_snapshot_id TEXT,
    source_snapshot_at TIMESTAMPTZ,
    status TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT '',
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT recommendation_snapshots_source_present
        CHECK (btrim(source) <> ''),
    CONSTRAINT recommendation_snapshots_status_valid
        CHECK (status IN ('complete', 'partial')),
    CONSTRAINT recommendation_snapshots_partial_explained
        CHECK (status <> 'partial' OR btrim(detail) <> ''),
    CONSTRAINT recommendation_snapshots_source_id_present
        CHECK (source_snapshot_id IS NULL OR btrim(source_snapshot_id) <> '')
);

-- Candidates are provider answers, not local catalogue entities. Every
-- suppression comparison uses the expanded MusicBrainz IDs; display names never
-- decide eligibility. Artist credits stay inline because they share the
-- candidate's replacement lifecycle and have no independent identity.
CREATE TABLE recommendation_candidates (
    source TEXT NOT NULL
        REFERENCES recommendation_snapshots(source) ON DELETE CASCADE,
    musicbrainz_recording_id UUID NOT NULL,
    musicbrainz_release_group_id UUID NOT NULL,
    musicbrainz_artist_ids UUID[] NOT NULL,
    recording_title TEXT NOT NULL,
    release_title TEXT NOT NULL DEFAULT '',
    artist_name TEXT NOT NULL,
    rank INTEGER NOT NULL,
    source_score DOUBLE PRECISION,
    reason_codes TEXT[] NOT NULL,
    reason_context JSONB NOT NULL DEFAULT '{}'::jsonb,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (source, musicbrainz_recording_id),
    UNIQUE (source, rank),
    CONSTRAINT recommendation_candidates_recording_title_present
        CHECK (btrim(recording_title) <> ''),
    CONSTRAINT recommendation_candidates_artist_name_present
        CHECK (btrim(artist_name) <> ''),
    CONSTRAINT recommendation_candidates_rank_positive CHECK (rank > 0),
    CONSTRAINT recommendation_candidates_artists_present
        CHECK (cardinality(musicbrainz_artist_ids) > 0),
    CONSTRAINT recommendation_candidates_artists_identified
        CHECK (array_position(musicbrainz_artist_ids, NULL) IS NULL),
    CONSTRAINT recommendation_candidates_reasons_present
        CHECK (cardinality(reason_codes) > 0),
    CONSTRAINT recommendation_candidates_reasons_identified
        CHECK (array_position(reason_codes, NULL) IS NULL
               AND array_position(reason_codes, '') IS NULL),
    CONSTRAINT recommendation_candidates_reason_context_object
        CHECK (jsonb_typeof(reason_context) = 'object')
);

-- +goose Down
DROP TABLE IF EXISTS recommendation_candidates;
DROP TABLE IF EXISTS recommendation_snapshots;
DROP TABLE IF EXISTS recommendation_impressions;
DROP TABLE IF EXISTS recommendation_dismissals;
