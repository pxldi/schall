-- +goose Up

-- A genre is what the MusicBrainz community voted this artist or this release
-- group is. Schall already holds both MusicBrainz identifiers, so the votes are
-- asked for by identifier during the refreshes that visit those entities
-- anyway, and no name is ever sent to anybody.
--
-- NULL and the empty array say different things, and the difference is what
-- makes the backfill stop. NULL is "nobody has asked yet". The empty array is
-- "asked, and MusicBrainz holds no votes" — an answer, so the row is never
-- asked about again. Absence is silence, not a reason to ask a second service.
--
-- Genres are display. Nothing that matches, grades or resolves a file reads
-- them.
ALTER TABLE artists ADD COLUMN genres TEXT[];
ALTER TABLE albums ADD COLUMN genres TEXT[];

-- A biography is a few lines from the Wikipedia article about this artist,
-- reached from the artist's MusicBrainz identifier through the Wikidata entity
-- MusicBrainz links — the same chain the artist picture follows, one hop
-- further for words instead of an image.
--
-- biography_source_url is the article the words came from. Wikipedia text is CC
-- BY-SA, so the page it is shown on must name Wikipedia and link the article;
-- storing the address is what lets it.
--
-- biography_fetched_at is when the chain last answered about this artist, and
-- it is set whatever the answer was. A row with a time and no text is "asked,
-- and no encyclopaedia has heard of them", which stops the sweep asking again.
-- A row still NULL was never asked. An outage writes nothing at all, because an
-- outage is not an absence.
ALTER TABLE artists ADD COLUMN biography TEXT;
ALTER TABLE artists ADD COLUMN biography_source_url TEXT;
ALTER TABLE artists ADD COLUMN biography_fetched_at TIMESTAMPTZ;

-- One genre backfill at a time, enforced by the table rather than hoped for,
-- exactly as the picture sweep and the lyrics sweep are.
--
-- The backfill asks nobody anything itself. It finds the artists whose genres
-- nobody has asked MusicBrainz for, and queues the ordinary artist refresh for
-- them; that refresh is what talks to MusicBrainz, one request per second like
-- every other. Two backfills running would queue the same refreshes twice, and
-- the point of the pass is that the catalogue fills once and quietly.
CREATE UNIQUE INDEX jobs_genre_sweep_idx
    ON jobs (kind)
    WHERE kind = 'sweep_genres' AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX IF EXISTS jobs_genre_sweep_idx;
ALTER TABLE artists DROP COLUMN biography_fetched_at;
ALTER TABLE artists DROP COLUMN biography_source_url;
ALTER TABLE artists DROP COLUMN biography;
ALTER TABLE albums DROP COLUMN genres;
ALTER TABLE artists DROP COLUMN genres;
