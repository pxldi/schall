-- +goose Up
-- Feedback is an append-only record of a taste signal. It copies the candidate
-- context because recommendation candidates belong to replaceable snapshots.
CREATE TABLE recommendation_feedback (
    id UUID NOT NULL PRIMARY KEY DEFAULT gen_random_uuid(),
    source TEXT NOT NULL,
    musicbrainz_recording_id UUID NOT NULL,
    musicbrainz_release_group_id UUID NOT NULL,
    musicbrainz_artist_ids UUID[] NOT NULL,
    signal TEXT NOT NULL,
    reason_codes TEXT[] NOT NULL,
    reason_context JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT recommendation_feedback_source_present
        CHECK (btrim(source) <> ''),
    CONSTRAINT recommendation_feedback_signal_valid
        CHECK (signal IN ('more_like_this', 'less_like_this')),
    CONSTRAINT recommendation_feedback_artists_present
        CHECK (cardinality(musicbrainz_artist_ids) > 0),
    CONSTRAINT recommendation_feedback_artists_identified
        CHECK (array_position(musicbrainz_artist_ids, NULL) IS NULL),
    CONSTRAINT recommendation_feedback_reasons_present
        CHECK (cardinality(reason_codes) > 0),
    CONSTRAINT recommendation_feedback_reasons_identified
        CHECK (array_position(reason_codes, NULL) IS NULL
               AND array_position(reason_codes, '') IS NULL),
    CONSTRAINT recommendation_feedback_reason_context_object
        CHECK (jsonb_typeof(reason_context) = 'object')
);

CREATE INDEX recommendation_feedback_source_created_idx
    ON recommendation_feedback (source, created_at DESC);

CREATE INDEX recommendation_feedback_source_recording_idx
    ON recommendation_feedback (source, musicbrainz_recording_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS recommendation_feedback;
