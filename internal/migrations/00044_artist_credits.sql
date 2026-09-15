-- +goose Up
-- A credit is a list, not a sentence.
--
-- "Porter Robinson, Ninajirachi" written into one string is one artist called
-- that: neither real artist is reachable from it, and a music server given it
-- files a third artist nobody has heard of beside the two. MusicBrainz already
-- holds the credit as a list — a name, a join phrase and an artist per entry —
-- and the catalogue kept the joined printing of it and threw the list away.
--
-- These tables are that list, and nothing more. They record what MusicBrainz
-- says; they decide nothing. No part of matching, identity or acquisition
-- reads them, and a credit is never split anywhere but where MusicBrainz
-- already split it.

-- The artists credited on one track, in the order MusicBrainz credits them.
--
-- The order is not decoration. A tag write pairs the names against the
-- identifiers by position (setCredits, internal/tagging/tags.go), so a credit
-- read back out of order hands one artist's identifier to another's name.
--
-- musicbrainz_artist_id is not a reference to artists(id), on purpose. A guest
-- credited on one track is not an artist the library follows or holds, and
-- creating a row in artists for one would put a name in the user's artist list
-- as a side effect of somebody else's release — and then the
-- artists_catalogue_only_explained constraint would demand a sentence saying
-- why it is held, which nobody could write truthfully. Where the artist is
-- held, the join is artists.musicbrainz_id = musicbrainz_artist_id and it finds
-- them; a guest who is not held is a name and an identifier, which is all a tag
-- needs and all this claims.
CREATE TABLE track_artist_credits (
    track_id UUID NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
    -- Where in the credit this artist stands, from 1. Nothing references one
    -- credit row, so the track and the place in its list are its whole
    -- identity and it needs no surrogate key of its own.
    position INTEGER NOT NULL,
    -- The name as this release credits them, which is not always the artist's
    -- own name: MusicBrainz records "P!nk" on the sleeve and "Pink" as the
    -- artist. The sleeve is what a music server shows, so the sleeve is what
    -- is written into the file.
    --
    -- Blank is allowed and is not checked against. An entry MusicBrainz prints
    -- with neither a credited name nor an artist name is rare and is still an
    -- entry: its place has to be taken, or the next artist's identifier lands
    -- against the wrong name and the list stops reproducing the credit the
    -- provider printed.
    credited_name TEXT NOT NULL,
    -- The artist MusicBrainz says that name is. Absent where the credit names
    -- somebody MusicBrainz has no entity for, which is silence rather than a
    -- reason to drop the name.
    musicbrainz_artist_id UUID,
    -- What MusicBrainz prints between this credit and the next — ", ", " feat.
    -- ", " & ". Empty on the last one, and empty is a value here rather than a
    -- missing row: joining the list back up has to reproduce the string the
    -- provider prints, character for character.
    join_phrase TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (track_id, position),
    CONSTRAINT track_artist_credits_position_positive CHECK (position > 0)
);

-- The artists credited on one album, in the same shape and for the same
-- reason. It hangs off albums rather than off release_editions because albums
-- is what the rest of Schall refers to — the folder is filed under it, the
-- download names it, the UI groups by it — and release_editions is a 1:1
-- satellite that would only add a join. The credit itself is read off the
-- representative edition, which is the edition Schall files and tags; where no
-- edition has been chosen there is no credit, which is silence.
--
-- albums.artist_id is untouched and stays what it is: the one artist the album
-- is filed under. This is the credit, which is a different claim.
CREATE TABLE album_artist_credits (
    album_id UUID NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    credited_name TEXT NOT NULL,
    musicbrainz_artist_id UUID,
    join_phrase TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (album_id, position),
    CONSTRAINT album_artist_credits_position_positive CHECK (position > 0)
);

-- Finding every track a MusicBrainz artist is credited on, which is what an
-- artist page showing guest appearances asks. Partial, because a credit
-- without an identifier answers no such question.
CREATE INDEX track_artist_credits_artist_idx
    ON track_artist_credits (musicbrainz_artist_id)
    WHERE musicbrainz_artist_id IS NOT NULL;

CREATE INDEX album_artist_credits_artist_idx
    ON album_artist_credits (musicbrainz_artist_id)
    WHERE musicbrainz_artist_id IS NOT NULL;

-- Every album already in the catalogue was fetched before the credit was kept,
-- so its list is empty and only a refetch fills it. refresh_album_metadata is
-- already the job that asks the provider for a release and hands the answer to
-- SaveReleaseCatalogue, and the answer now carries the credits, so backfilling
-- is running that job once per album rather than a job kind of its own.
--
-- Staggered, because the worker claims by (run_after, created_at): a thousand
-- rows queued at one instant would put every scan, import and acquisition
-- behind them. Two seconds apart is slower than the client's own one request
-- per second, which leaves the queue room to claim everything else in between
-- and keeps the provider from ever seeing a burst.
--
-- Albums with no release group are skipped: there is nothing to ask about, and
-- a job that can only fail three times is noise rather than a backfill. The
-- ON CONFLICT target is the partial unique index from 00002, so an album whose
-- refresh is already queued or running is not queued a second time.
INSERT INTO jobs (kind, payload, run_after)
SELECT 'refresh_album_metadata',
       jsonb_build_object('albumId', albums.id::text),
       now() + (row_number() OVER (ORDER BY albums.created_at) * interval '2 seconds')
FROM albums
WHERE albums.musicbrainz_release_group_id IS NOT NULL
ON CONFLICT (kind, (payload->>'albumId'))
    WHERE kind = 'refresh_album_metadata' AND status IN ('queued', 'running')
DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS album_artist_credits;
DROP TABLE IF EXISTS track_artist_credits;
-- The queued refreshes are left alone. A queued refresh_album_metadata is
-- ordinary work that Schall enqueues on its own account, and there is nothing
-- in the row saying which ones this migration added; deleting them by kind
-- would cancel refreshes nobody asked it to cancel. Running one without the
-- tables costs a request and writes the catalogue it always wrote.
