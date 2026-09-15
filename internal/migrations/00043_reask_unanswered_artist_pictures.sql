-- +goose Up
-- Forget that nobody could picture these artists, because the question has
-- changed since they were asked.
--
-- A row with a NULL image is the answer "asked, and there is none", and it
-- exists so a nameless face is not asked about on every page view. That is the
-- right thing to record right up until the chain doing the asking grows a rung.
-- It now reads the Bandcamp or SoundCloud page MusicBrainz links, which is the
-- only rung most of a real collection has — five of thirty-three artists on the
-- first deployment carried bandcamp and soundcloud relations and none of the
-- three kinds the chain knew about. Every one of them already had an absence on
-- record, so the sweep would have skipped all five forever and the new rung
-- would have pictured nobody.
--
-- Only absences are deleted. A row holding an actual picture is the answer to a
-- question that has not changed, and nothing here re-fetches one.
DELETE FROM artist_images WHERE image IS NULL;

-- +goose Down
-- There is nothing to restore. What is deleted above is a cached absence, and
-- the sweep derives it again the next time it runs — slower, never wrong.
SELECT 1;
