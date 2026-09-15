-- +goose Up
-- A label is a company that publishes music. MusicBrainz holds one as an entity
-- of its own, with a stable identifier, and every release it published names it.
--
-- Schall already follows artists: a follow says "keep this artist's releases
-- complete", and the follow feed asks MusicBrainz what they have put out since
-- and wants the tracks the library does not hold. Some collections are gathered
-- by label instead — a small imprint whose every record is worth hearing,
-- whoever made it — and until now the only way to cover one was to follow each
-- of its artists by hand, which cannot cover the ones it has not signed yet.
--
-- These two tables are the same statement of intent, about a label. Nothing is
-- guessed from a name: a label is held by its MusicBrainz identifier, and the
-- releases it published are read from MusicBrainz by that identifier.
CREATE TABLE labels (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    musicbrainz_id UUID NOT NULL UNIQUE,
    name TEXT NOT NULL,
    -- What MusicBrainz calls the label and where it says it is. Both are
    -- display, and both are how a person tells two labels of the same name
    -- apart when they choose one to follow. Empty means MusicBrainz said
    -- nothing, which is silence and never a claim.
    label_type TEXT NOT NULL DEFAULT '',
    country TEXT NOT NULL DEFAULT '',
    disambiguation TEXT NOT NULL DEFAULT '',
    -- The same four levels an artist has, deciding what counts as one of the
    -- label's releases. It filters counting and wanting, never what is fetched:
    -- a refresh still records every release group the label published.
    monitor_level TEXT NOT NULL DEFAULT 'main',
    -- Following is a decision with a date, exactly as it is for an artist, and
    -- the feed wants only what was published after it. Unfollowing sets it back
    -- to NULL and keeps the row.
    followed_at TIMESTAMPTZ,
    last_refreshed_at TIMESTAMPTZ,
    -- Where the next pass over this label's releases starts. MusicBrainz pages
    -- a browse, a large label has thousands of releases, and one pass reads a
    -- bounded number of pages so a follow list is never one long run against a
    -- rate limit shared with interactive search. The position lives on the row
    -- rather than in a job payload, because a job that fails takes its payload
    -- with it and the walk would start again from nothing.
    releases_offset INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT labels_name_not_blank CHECK (btrim(name) <> ''),
    CONSTRAINT labels_monitor_level_valid
        CHECK (monitor_level IN ('everything', 'main', 'albums_eps', 'owned'))
);

CREATE INDEX labels_followed_idx ON labels (followed_at) WHERE followed_at IS NOT NULL;

-- Which releases a label published. The release itself is an ordinary album
-- row, ingested the way any other release group is and filed under the artist
-- MusicBrainz credits it to, held rather than followed. This table is only the
-- fact that the label published it, so a release can belong to a label and to
-- an artist at once without either owning the other.
CREATE TABLE label_releases (
    label_id UUID NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
    album_id UUID NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (label_id, album_id)
);

CREATE INDEX label_releases_album_idx ON label_releases (album_id);

-- One active walk per label, in the shape download_requests_open_target_idx
-- set: a sweep and a "Refresh now" press racing each other has to find the
-- one already queued or running rather than start a second walk that stomps
-- the first one's releases_offset.
CREATE UNIQUE INDEX label_refresh_jobs_active_idx
    ON jobs ((payload->>'labelId'))
    WHERE kind = 'refresh_label_metadata' AND status IN ('queued', 'running');

-- A want records where the asking came from, and the answer now has one more
-- form. A track wanted because a label the user follows published the release
-- it is on came from the label, not from its artist — the user may not follow
-- that artist at all, and may never have heard of them.
ALTER TABLE acquisition_targets
    DROP CONSTRAINT IF EXISTS acquisition_targets_origin_valid,
    ADD CONSTRAINT acquisition_targets_origin_valid
        CHECK (origin IN ('manual', 'playlist', 'follow_feed', 'recommendation', 'label_feed'));

-- +goose Down
-- The wants themselves are kept and re-attributed by hand, exactly as the
-- recommendation origin was: a want is music somebody asked for, and rolling a
-- schema back is not a reason to forget it.
UPDATE acquisition_targets SET origin = 'manual' WHERE origin = 'label_feed';

DROP INDEX IF EXISTS label_refresh_jobs_active_idx;

ALTER TABLE acquisition_targets
    DROP CONSTRAINT IF EXISTS acquisition_targets_origin_valid,
    ADD CONSTRAINT acquisition_targets_origin_valid
        CHECK (origin IN ('manual', 'playlist', 'follow_feed', 'recommendation'));

DROP TABLE IF EXISTS label_releases;
DROP TABLE IF EXISTS labels;
