package db

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
)

// seedSearchable fills every kind the one search reaches with the same set of
// names, so each assertion below can be made of all five at once. Three of the
// names carry a character an ILIKE pattern would otherwise read as an
// instruction, and each has a decoy beside it — what an unescaped pattern would
// have answered with as well.
func seedSearchable(t *testing.T, ctx context.Context, pool *pgxpool.Pool, names []string) {
	t.Helper()

	for position, name := range names {
		artistID, albumID := uuid.New(), uuid.New()
		if _, err := pool.Exec(ctx, `
			WITH artist AS (
			    INSERT INTO artists (id, name, sort_name) VALUES ($1, $3, $3)
			), album AS (
			    INSERT INTO albums (id, artist_id, title) VALUES ($2, $1, $3)
			), track AS (
			    INSERT INTO tracks (id, album_id, title, disc_number, track_number)
			    VALUES ($4, $2, $3, 1, 1)
			), file AS (
			    INSERT INTO library_files (id, path, size_bytes, modified_at, title_tag)
			    VALUES ($5, $6, 1, now(), $3)
			)
			INSERT INTO playlists (id, source, name) VALUES ($7, 'manual', $3)
		`, artistID, albumID, name, uuid.New(), uuid.New(),
			"/music/"+uuid.New().String()+".flac", uuid.New()); err != nil {
			t.Fatalf("seed %q (position %d): %v", name, position, err)
		}
	}
}

func searchTestPool(t *testing.T) (context.Context, *pgxpool.Pool, *Queries) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	pool := dbtest.Setup(t)
	return ctx, pool, New(pool)
}

// The escaping rule the project pins exists because a search box is not a
// pattern language. A cross-entity search is exactly where that rule gets
// copied, so every kind it reaches is held to it here: a query of "%" answers
// with the one name that contains a per cent sign, not with the collection.
func TestSearchMatchesWildcardCharactersLiterally(t *testing.T) {
	ctx, pool, queries := searchTestPool(t)
	seedSearchable(t, ctx, pool, []string{
		"50% Chance", "5000 Volts", "A_B Sound", "AXB Sound", `A\_B Tape`, `A\XB Sound`,
	})

	for _, testCase := range []struct {
		name, query string
		want        []string
	}{
		{"per cent sign", "50%", []string{"50% Chance"}},
		// A per cent sign on its own is the whole point: as a pattern it means
		// everything, and as a query it means the one name that has one in it.
		{"per cent sign alone", "%", []string{"50% Chance"}},
		{"underscore", "a_b", []string{"A_B Sound"}},
		// An underscore on its own means any one character as a pattern, and
		// the two names spelt with one as a query.
		{"underscore alone", "_", []string{`A\_B Tape`, "A_B Sound"}},
		{"the escape character itself", `\_`, []string{`A\_B Tape`}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			results, err := queries.Search(ctx, SearchParams{Query: testCase.query, Limit: 5})
			if err != nil {
				t.Fatalf("Search(%q) error = %v", testCase.query, err)
			}
			for kind, got := range searchedNames(results) {
				slices.Sort(got)
				if !slices.Equal(got, testCase.want) {
					t.Errorf("Search(%q) %s = %v, want %v",
						testCase.query, kind, got, testCase.want)
				}
			}
		})
	}
}

// An empty box is not a request for the whole collection. Nothing answers it,
// and nothing is scanned to find that out.
func TestSearchAnswersAnEmptyQueryWithNothing(t *testing.T) {
	ctx, pool, queries := searchTestPool(t)
	seedSearchable(t, ctx, pool, []string{"Portishead", "Massive Attack"})

	for _, query := range []string{"", "   ", "\t\n"} {
		results, err := queries.Search(ctx, SearchParams{Query: query, Limit: 5})
		if err != nil {
			t.Fatalf("Search(%q) error = %v", query, err)
		}
		for kind, got := range searchedNames(results) {
			if len(got) != 0 {
				t.Errorf("Search(%q) %s = %v, want nothing", query, kind, got)
			}
		}
	}
}

// The limit binds each kind on its own, so one crowded kind cannot take the
// room another needs.
func TestSearchBoundsEveryKindOnItsOwn(t *testing.T) {
	ctx, pool, queries := searchTestPool(t)
	seedSearchable(t, ctx, pool, []string{
		"Dummy One", "Dummy Two", "Dummy Three", "Dummy Four",
	})

	results, err := queries.Search(ctx, SearchParams{Query: "dummy", Limit: 2})
	if err != nil {
		t.Fatalf("Search error = %v", err)
	}
	for kind, got := range searchedNames(results) {
		if len(got) != 2 {
			t.Errorf("Search with limit 2 answered %d %s, want 2", len(got), kind)
		}
	}
}

// A search reaches every kind Schall holds, and each result carries what the
// page owning it needs to be linked to.
func TestSearchAnswersUnderEveryHeading(t *testing.T) {
	ctx, pool, queries := searchTestPool(t)

	artistID, albumID, trackID := uuid.New(), uuid.New(), uuid.New()
	fileID, playlistID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		WITH artist AS (
		    INSERT INTO artists (id, name, sort_name)
		    VALUES ($1, 'Portishead', 'Portishead')
		), album AS (
		    INSERT INTO albums (id, artist_id, title, release_date)
		    VALUES ($2, $1, 'Dummy', DATE '1994-08-22')
		), track AS (
		    INSERT INTO tracks (id, album_id, title, disc_number, track_number)
		    VALUES ($3, $2, 'Sour Times', 1, 2)
		), file AS (
		    INSERT INTO library_files (
		        id, path, size_bytes, modified_at, artist_tag, album_tag, title_tag
		    )
		    VALUES ($4, '/music/Portishead/Dummy/02 Sour Times.flac', 1, now(),
		            'Portishead', 'Dummy', 'Sour Times')
		)
		INSERT INTO playlists (id, source, name, owner_name, track_count)
		VALUES ($5, 'manual', 'Portishead on repeat', 'leo', 12)
	`, artistID, albumID, trackID, fileID, playlistID); err != nil {
		t.Fatal(err)
	}

	results, err := queries.Search(ctx, SearchParams{Query: "portishead", Limit: 5})
	if err != nil {
		t.Fatalf("Search error = %v", err)
	}

	if len(results.Artists) != 1 || results.Artists[0].ID != artistID {
		t.Errorf("artists = %v, want the seeded artist", results.Artists)
	}
	// A release answers to its artist's name as well as its own title, which is
	// how the releases browser is searched, and it arrives described the way
	// that browser describes it — its artist, its date, and how much of it is
	// owned, the last two maintained on the release itself.
	if len(results.Releases) != 1 || results.Releases[0].ID != albumID ||
		results.Releases[0].ArtistName != "Portishead" ||
		!results.Releases[0].ReleaseDate.Valid ||
		results.Releases[0].TrackCount != 1 ||
		results.Releases[0].OwnedTrackCount != 0 {
		t.Errorf("releases = %v, want the seeded release described", results.Releases)
	}
	// A catalogue track has no page of its own, so it must arrive naming the
	// release it sits on.
	if len(results.Tracks) != 1 || results.Tracks[0].ID != trackID ||
		results.Tracks[0].AlbumID != albumID {
		t.Errorf("tracks = %v, want the seeded track on its release", results.Tracks)
	}
	if len(results.Files) != 1 || results.Files[0].ID != fileID ||
		results.Files[0].Missing {
		t.Errorf("files = %v, want the seeded file, present", results.Files)
	}
	if len(results.Playlists) != 1 || results.Playlists[0].ID != playlistID ||
		results.Playlists[0].OwnerName != "leo" {
		t.Errorf("playlists = %v, want the seeded playlist", results.Playlists)
	}
}

// The total counts what answered, not what came back. It is the number "All 4
// in Artists" is written from, and a list already cut down to two cannot say it.
func TestSearchCountsWhatAnsweredPastTheLimit(t *testing.T) {
	ctx, pool, queries := searchTestPool(t)
	seedSearchable(t, ctx, pool, []string{
		"Dummy One", "Dummy Two", "Dummy Three", "Dummy Four",
	})

	results, err := queries.Search(ctx, SearchParams{Query: "dummy", Limit: 2})
	if err != nil {
		t.Fatalf("Search error = %v", err)
	}

	want := SearchTotals{Artists: 4, Releases: 4, Tracks: 4, Files: 4, Playlists: 4}
	if results.Totals != want {
		t.Errorf("totals = %#v, want %#v", results.Totals, want)
	}
}

// Nothing answered is a total of none, said rather than left out.
func TestSearchCountsNothingWhenNothingAnswered(t *testing.T) {
	ctx, pool, queries := searchTestPool(t)
	seedSearchable(t, ctx, pool, []string{"Portishead"})

	results, err := queries.Search(ctx, SearchParams{Query: "nothing here", Limit: 5})
	if err != nil {
		t.Fatalf("Search error = %v", err)
	}

	if results.Totals != (SearchTotals{}) {
		t.Errorf("totals = %#v, want nothing counted", results.Totals)
	}
}

// What an artist row is read by: the files the library still has of them, and
// the releases those files sit on. Two of the three files below are the
// artist's, one is somebody else's, and one of the two went missing — so the
// numbers are one file on one release, not three on two.
func TestSearchCountsWhatTheLibraryHoldsOfAnArtist(t *testing.T) {
	ctx, pool, queries := searchTestPool(t)

	if _, err := pool.Exec(ctx, `
		WITH artist AS (
		    INSERT INTO artists (id, name, sort_name)
		    VALUES ('11111111-1111-1111-1111-111111111111', 'Portishead', 'Portishead')
		), album AS (
		    INSERT INTO albums (id, artist_id, title)
		    VALUES ('22222222-2222-2222-2222-222222222222',
		            '11111111-1111-1111-1111-111111111111', 'Dummy')
		), tracks AS (
		    INSERT INTO tracks (id, album_id, title, disc_number, track_number)
		    VALUES ('33333333-3333-3333-3333-333333333333',
		            '22222222-2222-2222-2222-222222222222', 'Sour Times', 1, 1),
		           ('44444444-4444-4444-4444-444444444444',
		            '22222222-2222-2222-2222-222222222222', 'Roads', 1, 2)
		), files AS (
		    INSERT INTO library_files (id, path, size_bytes, modified_at, missing_at)
		    VALUES ('55555555-5555-5555-5555-555555555555', '/music/a.flac', 1, now(), NULL),
		           ('66666666-6666-6666-6666-666666666666', '/music/b.flac', 1, now(), now())
		)
		INSERT INTO track_mappings (track_id, library_file_id, method)
		VALUES ('33333333-3333-3333-3333-333333333333',
		        '55555555-5555-5555-5555-555555555555', 'tags'),
		       ('44444444-4444-4444-4444-444444444444',
		        '66666666-6666-6666-6666-666666666666', 'tags')
	`); err != nil {
		t.Fatal(err)
	}

	results, err := queries.Search(ctx, SearchParams{Query: "portishead", Limit: 5})
	if err != nil {
		t.Fatalf("Search error = %v", err)
	}

	if len(results.Artists) != 1 {
		t.Fatalf("artists = %v, want the seeded artist", results.Artists)
	}
	if results.Artists[0].FileCount != 1 || results.Artists[0].ReleaseCount != 1 {
		t.Errorf("artist held %d files on %d releases, want 1 on 1",
			results.Artists[0].FileCount, results.Artists[0].ReleaseCount)
	}
}

// A catalogue track says whether the library holds the recording, and it reads
// that the way the rest of Schall does: a file proven to be the recording counts
// even though it is mapped onto no track at all.
func TestSearchCallsATrackOwnedOnAnIdentifiedFileAlone(t *testing.T) {
	ctx, pool, queries := searchTestPool(t)

	recordingID := uuid.New()
	if _, err := pool.Exec(ctx, `
		WITH artist AS (
		    INSERT INTO artists (id, name, sort_name)
		    VALUES ('11111111-1111-1111-1111-111111111111', 'Portishead', 'Portishead')
		), album AS (
		    INSERT INTO albums (id, artist_id, title)
		    VALUES ('22222222-2222-2222-2222-222222222222',
		            '11111111-1111-1111-1111-111111111111', 'Dummy')
		), track AS (
		    INSERT INTO tracks (id, album_id, title, disc_number, track_number,
		                        musicbrainz_recording_id, duration_ms)
		    VALUES ('33333333-3333-3333-3333-333333333333',
		            '22222222-2222-2222-2222-222222222222', 'Sour Times', 1, 1, $1, 251000)
		), file AS (
		    INSERT INTO library_files (id, path, size_bytes, modified_at)
		    VALUES ('55555555-5555-5555-5555-555555555555', '/music/a.flac', 1, now())
		)
		INSERT INTO library_file_identities (library_file_id, kind, method,
		                                     musicbrainz_recording_id)
		VALUES ('55555555-5555-5555-5555-555555555555', 'external', 'recording_id', $1)
	`, recordingID); err != nil {
		t.Fatal(err)
	}

	results, err := queries.Search(ctx, SearchParams{Query: "sour times", Limit: 5})
	if err != nil {
		t.Fatalf("Search error = %v", err)
	}

	if len(results.Tracks) != 1 {
		t.Fatalf("tracks = %v, want the seeded track", results.Tracks)
	}
	if !results.Tracks[0].Owned {
		t.Error("track owned = false, want the identified file to count")
	}
	if !results.Tracks[0].DurationMs.Valid || results.Tracks[0].DurationMs.Int32 != 251000 {
		t.Errorf("track duration = %v, want 251000ms", results.Tracks[0].DurationMs)
	}
}

// A track nothing in the library answers to is not owned, whatever else agrees.
func TestSearchCallsATrackNothingHoldsUnowned(t *testing.T) {
	ctx, pool, queries := searchTestPool(t)
	seedSearchable(t, ctx, pool, []string{"Sour Times"})

	results, err := queries.Search(ctx, SearchParams{Query: "sour times", Limit: 5})
	if err != nil {
		t.Fatalf("Search error = %v", err)
	}

	if len(results.Tracks) != 1 {
		t.Fatalf("tracks = %v, want the seeded track", results.Tracks)
	}
	if results.Tracks[0].Owned {
		t.Error("track owned = true, want a track nothing holds to say so")
	}
}

// An answer with no room in it is a caller that has not decided how much it
// wants, not a request for everything.
func TestSearchRefusesAnUnboundedAnswer(t *testing.T) {
	ctx, _, queries := searchTestPool(t)

	if _, err := queries.Search(ctx, SearchParams{Query: "portishead", Limit: 0}); err == nil {
		t.Fatal("Search with no limit = no error, want a refusal")
	}
}

// searchedNames is what every kind answered with, by the one field all five
// seeded kinds carry the query in.
func searchedNames(results SearchResults) map[string][]string {
	named := map[string][]string{
		"artists": {}, "releases": {}, "tracks": {}, "files": {}, "playlists": {},
	}
	for _, row := range results.Artists {
		named["artists"] = append(named["artists"], row.Name)
	}
	for _, row := range results.Releases {
		named["releases"] = append(named["releases"], row.Title)
	}
	for _, row := range results.Tracks {
		named["tracks"] = append(named["tracks"], row.Title)
	}
	for _, row := range results.Files {
		named["files"] = append(named["files"], row.TitleTag.String)
	}
	for _, row := range results.Playlists {
		named["playlists"] = append(named["playlists"], row.Name)
	}
	return named
}
