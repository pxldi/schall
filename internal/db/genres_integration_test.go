package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
)

// seedGenreArtist puts one artist and one of their releases in the catalogue.
// The artist's genres are NULL, meaning nobody has asked MusicBrainz yet; the
// release's are empty, meaning it was asked and MusicBrainz holds no votes. So
// the artist is the only thing the backfill has left to ask about, and a test
// that wants an unanswered release says so itself.
func seedGenreArtist(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (uuid.UUID, uuid.UUID) {
	t.Helper()
	artistID, albumID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name)
		VALUES ($1, $2, 'Talk Talk', 'Talk Talk')
	`, artistID, uuid.New()); err != nil {
		t.Fatalf("seed artist: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, musicbrainz_release_group_id, title, album_type, genres)
		VALUES ($1, $2, $3, 'Spirit of Eden', 'album', '{}')
	`, albumID, artistID, uuid.New()); err != nil {
		t.Fatalf("seed album: %v", err)
	}
	return artistID, albumID
}

// The genre backfill finds the artists nobody has asked MusicBrainz about and
// queues the ordinary artist refresh for them. It asks MusicBrainz nothing
// itself.
func TestBackfillGenresQueuesARefreshForAnUnaskedArtist(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	artistID, _ := seedGenreArtist(t, ctx, pool)

	result, err := queries.BackfillGenres(ctx, 25)
	if err != nil {
		t.Fatalf("BackfillGenres() error = %v", err)
	}
	if result.WaitingCount != 1 || result.QueuedCount != 1 {
		t.Fatalf("result = %+v, want one artist waiting and one queued", result)
	}
	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'refresh_artist_metadata' AND payload->>'artistId' = $1::text
	`, artistID).Scan(&queued); err != nil {
		t.Fatalf("count queued refreshes: %v", err)
	}
	if queued != 1 {
		t.Fatalf("queued %d refreshes, want one", queued)
	}
}

// An artist already being refreshed is left alone. That pass will write the
// genres, and a second job would ask MusicBrainz the same question twice.
func TestBackfillGenresLeavesAnArtistAlreadyBeingRefreshed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	artistID, _ := seedGenreArtist(t, ctx, pool)

	if _, err := pool.Exec(ctx, `
		INSERT INTO jobs (kind, payload, status)
		VALUES ('refresh_artist_metadata', jsonb_build_object('artistId', $1::text), 'running')
	`, artistID); err != nil {
		t.Fatalf("seed running refresh: %v", err)
	}

	result, err := queries.BackfillGenres(ctx, 25)
	if err != nil {
		t.Fatalf("BackfillGenres() error = %v", err)
	}
	if result.WaitingCount != 1 {
		t.Fatalf("waiting = %d, want the artist still counted as waiting", result.WaitingCount)
	}
	if result.QueuedCount != 0 {
		t.Fatalf("queued = %d, want nothing queued beside the running refresh",
			result.QueuedCount)
	}
}

// An empty list is an answer: MusicBrainz was asked and holds no votes. The
// backfill never lists that artist again. NULL is the only "nobody has asked".
func TestBackfillGenresIgnoresAnArtistAlreadyAnswered(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	seedGenreArtist(t, ctx, pool)

	if _, err := pool.Exec(ctx, `UPDATE artists SET genres = '{}'`); err != nil {
		t.Fatalf("answer the artist: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE albums SET genres = '{}'`); err != nil {
		t.Fatalf("answer the album: %v", err)
	}

	result, err := queries.BackfillGenres(ctx, 25)
	if err != nil {
		t.Fatalf("BackfillGenres() error = %v", err)
	}
	if result.WaitingCount != 0 {
		t.Fatalf("waiting = %d, want nobody left to ask about", result.WaitingCount)
	}
}

// A release with no genres is asked about by its own refresh, not its artist's.
//
// The artist refresh only writes the release groups MusicBrainz credits to that
// artist, so a remix or a collaboration filed under somebody else's name is
// never reached by it. Queueing the artist for such a release left the release
// NULL, the artist listed again on the next pass, and each of those refreshes
// queuing a release refresh for the artist's whole discography — 1,494 jobs an
// hour off two albums, indefinitely.
func TestBackfillGenresQueuesTheReleasesOwnRefreshForItsGenres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	artistID, albumID := seedGenreArtist(t, ctx, pool)

	if _, err := pool.Exec(ctx, `UPDATE artists SET genres = '{"post-rock"}'`); err != nil {
		t.Fatalf("answer the artist: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE albums SET genres = NULL WHERE id = $1`, albumID); err != nil {
		t.Fatalf("unanswer the album: %v", err)
	}

	result, err := queries.BackfillGenres(ctx, 25)
	if err != nil {
		t.Fatalf("BackfillGenres() error = %v", err)
	}
	if result.WaitingCount != 1 || result.QueuedCount != 1 {
		t.Fatalf("result = %+v, want the release waiting and queued", result)
	}

	var albumRefreshes, artistRefreshes int
	if err := pool.QueryRow(ctx, `
		SELECT
		    count(*) FILTER (
		        WHERE kind = 'refresh_album_metadata' AND payload->>'albumId' = $1::text),
		    count(*) FILTER (
		        WHERE kind = 'refresh_artist_metadata' AND payload->>'artistId' = $2::text)
		FROM jobs
	`, albumID, artistID).Scan(&albumRefreshes, &artistRefreshes); err != nil {
		t.Fatalf("count queued refreshes: %v", err)
	}
	if albumRefreshes != 1 {
		t.Fatalf("queued %d release refreshes, want one", albumRefreshes)
	}
	if artistRefreshes != 0 {
		t.Fatalf("queued %d artist refreshes, want none: the artist has been answered", artistRefreshes)
	}
}

// A release already queued for a refresh is left alone, so a sweep that comes
// back before the first one ran does not ask MusicBrainz twice.
func TestBackfillGenresLeavesAReleaseAlreadyQueued(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	_, albumID := seedGenreArtist(t, ctx, pool)

	if _, err := pool.Exec(ctx, `UPDATE artists SET genres = '{"post-rock"}'`); err != nil {
		t.Fatalf("answer the artist: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE albums SET genres = NULL WHERE id = $1`, albumID); err != nil {
		t.Fatalf("unanswer the album: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO jobs (kind, payload, status)
		VALUES ('refresh_album_metadata', jsonb_build_object('albumId', $1::text), 'running')
	`, albumID); err != nil {
		t.Fatalf("seed running refresh: %v", err)
	}

	result, err := queries.BackfillGenres(ctx, 25)
	if err != nil {
		t.Fatalf("BackfillGenres() error = %v", err)
	}
	if result.QueuedCount != 0 {
		t.Fatalf("queued = %d, want the running refresh left to answer", result.QueuedCount)
	}
}

// The artist page reads the genres and the biography back off the row.
func TestGetArtistReadsTheGenresAndTheBiography(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	artistID, _ := seedGenreArtist(t, ctx, pool)

	if _, err := pool.Exec(ctx, `
		UPDATE artists
		SET genres = '{"post-rock","art rock"}',
		    biography = 'Talk Talk were an English band.',
		    biography_source_url = 'https://en.wikipedia.org/wiki/Talk_Talk',
		    biography_fetched_at = now()
	`); err != nil {
		t.Fatalf("write the genres and the biography: %v", err)
	}

	artist, err := queries.GetArtist(ctx, artistID)
	if err != nil {
		t.Fatalf("GetArtist() error = %v", err)
	}
	if len(artist.Genres) != 2 || artist.Genres[0] != "post-rock" {
		t.Errorf("genres = %v, want them in the order they were written", artist.Genres)
	}
	if artist.Biography != "Talk Talk were an English band." {
		t.Errorf("biography = %q", artist.Biography)
	}
	if artist.BiographySourceUrl != "https://en.wikipedia.org/wiki/Talk_Talk" {
		t.Errorf("source = %q", artist.BiographySourceUrl)
	}
}

// An artist nobody has asked about reads back as no genres and no words rather
// than as anything a page has to handle specially.
func TestGetArtistReadsAnUnaskedArtistAsEmpty(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	artistID, _ := seedGenreArtist(t, ctx, pool)

	artist, err := queries.GetArtist(ctx, artistID)
	if err != nil {
		t.Fatalf("GetArtist() error = %v", err)
	}
	if len(artist.Genres) != 0 {
		t.Errorf("genres = %v, want none", artist.Genres)
	}
	if artist.Biography != "" || artist.BiographySourceUrl != "" {
		t.Errorf("biography = %q from %q, want nothing",
			artist.Biography, artist.BiographySourceUrl)
	}
}

// The artists list narrows to one genre, matched whole. A genre nobody is filed
// under finds nobody.
func TestListArtistsNarrowsToOneGenre(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	seedGenreArtist(t, ctx, pool)

	if _, err := pool.Exec(ctx, `UPDATE artists SET genres = '{"post-rock"}'`); err != nil {
		t.Fatalf("write the genres: %v", err)
	}

	listed, err := queries.ListArtists(ctx, ListArtistsParams{Genre: "post-rock", Limit: 10})
	if err != nil {
		t.Fatalf("ListArtists() error = %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed %d, want the artist filed under that genre", len(listed))
	}

	other, err := queries.ListArtists(ctx, ListArtistsParams{Genre: "ambient", Limit: 10})
	if err != nil {
		t.Fatalf("ListArtists() error = %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("listed %d under a genre nobody is filed under, want none", len(other))
	}
}

// The picklist names only genres that would find somebody, once each.
func TestCatalogueGenresNamesEachGenreOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	seedGenreArtist(t, ctx, pool)

	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, name, sort_name, genres)
		VALUES (gen_random_uuid(), 'Bark Psychosis', 'Bark Psychosis', '{"post-rock","ambient"}')
	`); err != nil {
		t.Fatalf("seed a second artist: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE artists SET genres = '{"post-rock"}' WHERE genres IS NULL
	`); err != nil {
		t.Fatalf("write the genres: %v", err)
	}

	genres, err := queries.CatalogueGenres(ctx)
	if err != nil {
		t.Fatalf("CatalogueGenres() error = %v", err)
	}
	if len(genres) != 2 || genres[0] != "ambient" || genres[1] != "post-rock" {
		t.Fatalf("genres = %v, want each named once, alphabetically", genres)
	}
}

// The biography sweep lists the artists nobody has asked an encyclopaedia
// about, and stops listing one the moment an answer is recorded — including the
// answer "there is no article".
func TestArtistsMissingBiographyStopsListingAnAnsweredArtist(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	artistID, _ := seedGenreArtist(t, ctx, pool)

	waiting, err := queries.ArtistsMissingBiography(ctx, 25)
	if err != nil {
		t.Fatalf("ArtistsMissingBiography() error = %v", err)
	}
	if len(waiting) != 1 || waiting[0].ID != artistID {
		t.Fatalf("waiting = %v, want the artist nobody has asked about", waiting)
	}

	if err := queries.SaveArtistBiography(ctx, SaveArtistBiographyParams{
		ArtistID: artistID, Biography: "", SourceUrl: "",
	}); err != nil {
		t.Fatalf("SaveArtistBiography() error = %v", err)
	}

	waiting, err = queries.ArtistsMissingBiography(ctx, 25)
	if err != nil {
		t.Fatalf("ArtistsMissingBiography() error = %v", err)
	}
	if len(waiting) != 0 {
		t.Fatalf("waiting = %v, want an artist recorded as asked to be left alone", waiting)
	}
}

// An artist whose refresh keeps failing does not block the rest of the batch
// forever. Ordered oldest-first, a permanently failing artist would otherwise
// occupy the same slot in every pass — queued, failed, queued again — while an
// artist behind it in the queue is never reached. A failure within the last
// day is left out of the count instead, so the batch moves on to whoever is
// next.
func TestBackfillGenresDoesNotStarveOnAFailingArtist(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	// The failing artist is seeded first, so it is the oldest and would sort
	// to the front of every batch.
	failingID, _ := seedGenreArtist(t, ctx, pool)
	if _, err := pool.Exec(ctx, `
		INSERT INTO jobs (kind, payload, status, completed_at)
		VALUES ('refresh_artist_metadata', jsonb_build_object('artistId', $1::text),
		        'failed', now())
	`, failingID); err != nil {
		t.Fatalf("seed a failed refresh: %v", err)
	}

	var otherArtistID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name)
		VALUES (gen_random_uuid(), $1, 'Bark Psychosis', 'Bark Psychosis')
		RETURNING id
	`, uuid.New()).Scan(&otherArtistID); err != nil {
		t.Fatalf("seed a second artist: %v", err)
	}

	result, err := queries.BackfillGenres(ctx, 1)
	if err != nil {
		t.Fatalf("BackfillGenres() error = %v", err)
	}
	if result.WaitingCount != 1 || result.QueuedCount != 1 {
		t.Fatalf("result = %+v, want the artist behind the failure counted and queued",
			result)
	}
	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'refresh_artist_metadata' AND payload->>'artistId' = $1::text
	`, otherArtistID).Scan(&queued); err != nil {
		t.Fatalf("count queued refreshes: %v", err)
	}
	if queued != 1 {
		t.Fatalf("queued %d refreshes for the artist behind the failure, want one", queued)
	}
	var refailing int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'refresh_artist_metadata' AND payload->>'artistId' = $1::text
		  AND status = 'queued'
	`, failingID).Scan(&refailing); err != nil {
		t.Fatalf("count refreshes queued for the failing artist: %v", err)
	}
	if refailing != 0 {
		t.Fatalf("the artist that failed today was queued again %d times, want none", refailing)
	}
}

// A failure old enough is a failure worth trying again: the day's exclusion
// has passed, and nothing else has answered for the artist since.
func TestBackfillGenresRetriesAnArtistWhoseFailureIsAWeekOld(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	artistID, _ := seedGenreArtist(t, ctx, pool)

	if _, err := pool.Exec(ctx, `
		INSERT INTO jobs (kind, payload, status, completed_at)
		VALUES ('refresh_artist_metadata', jsonb_build_object('artistId', $1::text),
		        'failed', now() - interval '7 days')
	`, artistID); err != nil {
		t.Fatalf("seed an old failed refresh: %v", err)
	}

	result, err := queries.BackfillGenres(ctx, 25)
	if err != nil {
		t.Fatalf("BackfillGenres() error = %v", err)
	}
	if result.WaitingCount != 1 || result.QueuedCount != 1 {
		t.Fatalf("result = %+v, want the artist tried again a week later", result)
	}
}

// An artist with no MusicBrainz identifier is never listed: the chain starts at
// that identifier, and nobody is asked about an artist by name.
func TestArtistsMissingBiographySkipsAnArtistWithNoIdentifier(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, name, sort_name) VALUES (gen_random_uuid(), 'Marram', 'Marram')
	`); err != nil {
		t.Fatalf("seed artist: %v", err)
	}

	waiting, err := queries.ArtistsMissingBiography(ctx, 25)
	if err != nil {
		t.Fatalf("ArtistsMissingBiography() error = %v", err)
	}
	if len(waiting) != 0 {
		t.Fatalf("waiting = %v, want nobody asked about by name", waiting)
	}
}

// Only one genre backfill is ever on the books, which the table enforces.
func TestQueueGenreSweepKeepsOnePassOnTheBooks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	later := time.Now().Add(time.Hour)
	if err := queries.QueueGenreSweep(ctx, later); err != nil {
		t.Fatalf("QueueGenreSweep() error = %v", err)
	}
	if err := queries.QueueGenreSweep(ctx, time.Now()); err != nil {
		t.Fatalf("QueueGenreSweep() error = %v", err)
	}

	var passes int
	var runAfter time.Time
	if err := pool.QueryRow(ctx, `
		SELECT count(*), min(run_after) FROM jobs WHERE kind = 'sweep_genres'
	`).Scan(&passes, &runAfter); err != nil {
		t.Fatalf("count passes: %v", err)
	}
	if passes != 1 {
		t.Fatalf("passes = %d, want one", passes)
	}
	// The earlier of the two times wins, so asking for it sooner brings it
	// forward rather than adding a second pass.
	if !runAfter.Before(later) {
		t.Fatalf("run_after = %s, want the earlier time kept", runAfter)
	}
}

// Startup means "there must be a pass on the books", not "run one now": a pass
// already waiting keeps the time it holds.
func TestEnsureGenreSweepQueuedKeepsAnExistingPass(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	later := time.Now().Add(time.Hour).UTC().Truncate(time.Millisecond)
	if err := queries.QueueGenreSweep(ctx, later); err != nil {
		t.Fatalf("QueueGenreSweep() error = %v", err)
	}
	if err := queries.EnsureGenreSweepQueued(ctx, time.Now()); err != nil {
		t.Fatalf("EnsureGenreSweepQueued() error = %v", err)
	}

	var runAfter time.Time
	if err := pool.QueryRow(ctx, `
		SELECT run_after FROM jobs WHERE kind = 'sweep_genres'
	`).Scan(&runAfter); err != nil {
		t.Fatalf("read the pass: %v", err)
	}
	if !runAfter.UTC().Truncate(time.Millisecond).Equal(later) {
		t.Fatalf("run_after = %s, want the waiting pass left alone at %s", runAfter, later)
	}
}
