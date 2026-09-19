package db

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
)

func TestCatalogueQueries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID, albumID, releaseGroupID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name)
		VALUES ($1, $2, 'Portishead', 'Portishead')
	`, artistID, uuid.New()); err != nil {
		t.Fatalf("seed artist: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, musicbrainz_release_group_id, title, album_type)
		VALUES ($1, $2, $3, 'Dummy', 'album')
	`, albumID, artistID, releaseGroupID); err != nil {
		t.Fatalf("seed album: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO metadata_sources (entity_type, entity_id, provider, external_id, metadata)
		VALUES ('album', $1, 'musicbrainz', $2, '{"first-release-date":"1994-08"}')
	`, albumID, releaseGroupID); err != nil {
		t.Fatalf("seed album metadata: %v", err)
	}

	queries := New(pool)
	artist, err := queries.GetArtist(ctx, artistID)
	if err != nil {
		t.Fatalf("GetArtist() error = %v", err)
	}
	if artist.Name != "Portishead" || artist.AlbumCount != 1 || artist.RefreshStatus != "pending" {
		t.Errorf("artist = %#v", artist)
	}

	albums, err := queries.ListAlbums(ctx, ListAlbumsParams{
		ArtistID: uuid.NullUUID{UUID: artistID, Valid: true},
	})
	if err != nil {
		t.Fatalf("ListAlbums() error = %v", err)
	}
	if len(albums.Items) != 1 || albums.Items[0].ID != albumID || albums.Items[0].FirstReleaseDate != "1994-08" {
		t.Errorf("albums = %#v", albums)
	}

	firstJob, err := queries.QueueArtistRefresh(ctx, artistID)
	if err != nil {
		t.Fatalf("QueueArtistRefresh() error = %v", err)
	}
	secondJob, err := queries.QueueArtistRefresh(ctx, artistID)
	if err != nil {
		t.Fatalf("second QueueArtistRefresh() error = %v", err)
	}
	if firstJob.ID != secondJob.ID || firstJob.Status != "queued" {
		t.Errorf("refresh jobs = %#v, %#v; want same queued job", firstJob, secondJob)
	}
}

// Following an artist Schall already holds is a promotion, not a conflict: the
// artist is in the catalogue because the library contains their music, and the
// user is now asking for their releases to be kept complete. It also queues the
// full refresh, because until now only the one release behind an owned file had
// been fetched.
func TestFollowingPromotesAHeldArtist(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	heldID, musicBrainzID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name, followed_at, catalogue_summary)
		VALUES ($1, $2, 'Sewerslvt', 'Sewerslvt', NULL, 'In the catalogue because your library holds their music.')
	`, heldID, musicBrainzID); err != nil {
		t.Fatalf("seed held artist: %v", err)
	}

	queries := New(pool)

	// The list shows both, and says which is which.
	listed, err := queries.ListArtists(ctx, ListArtistsParams{Limit: 50})
	if err != nil {
		t.Fatalf("ListArtists() error = %v", err)
	}
	if len(listed) != 1 || listed[0].FollowedAt.Valid || listed[0].CatalogueSummary == "" {
		t.Fatalf("listed = %#v, want one held artist that explains itself", listed)
	}

	followed, err := queries.FollowArtistByID(ctx, heldID)
	if err != nil {
		t.Fatalf("FollowArtistByID() error = %v", err)
	}
	if !followed.FollowedAt.Valid {
		t.Errorf("followed_at = %v, want a followed artist", followed.FollowedAt)
	}

	var summary *string
	if err := pool.QueryRow(ctx, `
		SELECT catalogue_summary FROM artists WHERE id = $1
	`, heldID).Scan(&summary); err != nil {
		t.Fatal(err)
	}
	if summary != nil {
		t.Errorf("catalogue_summary = %q, want it cleared once the artist is followed", *summary)
	}

	var refreshes int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'refresh_artist_metadata' AND payload->>'artistId' = $1::text
	`, heldID).Scan(&refreshes); err != nil {
		t.Fatal(err)
	}
	if refreshes != 1 {
		t.Errorf("refreshes queued = %d, want the full discography fetched", refreshes)
	}

	// Following again has nothing to promote.
	if _, err := queries.FollowArtistByID(ctx, heldID); err == nil {
		t.Error("FollowArtistByID() on a followed artist returned a row, want no rows")
	}

	// The same promotion happens when the user follows from a search result,
	// which is the path that used to report a conflict the user could not act on.
	if _, err := pool.Exec(ctx, `
		UPDATE artists SET followed_at = NULL, catalogue_summary = 'held again' WHERE id = $1
	`, heldID); err != nil {
		t.Fatal(err)
	}
	promoted, err := queries.CreateArtist(ctx, CreateArtistParams{
		MusicbrainzID: uuid.NullUUID{UUID: musicBrainzID, Valid: true},
		Name:          "Sewerslvt",
		SortName:      "Sewerslvt",
	})
	if err != nil {
		t.Fatalf("CreateArtist() on a held artist error = %v", err)
	}
	if promoted.ID != heldID || !promoted.FollowedAt.Valid {
		t.Errorf("promoted = %#v, want the held artist followed rather than a second row", promoted)
	}
}

// seedArtistRoster creates the two halves of the artists list — the ones the
// user chose to follow and the ones the library holds on their behalf — so the
// paging tests have something with a defined order to page through.
func seedArtistRoster(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	roster := []struct {
		name, sortName string
		followed       bool
	}{
		{"Aphex Twin", "Aphex Twin", true},
		{"Portishead", "Portishead", true},
		{"Boards of Canada", "Boards of Canada", false},
		{"Sewerslvt", "Sewerslvt", false},
		{"The Knife", "Knife, The", false},
	}
	for _, entry := range roster {
		// A held artist has to explain itself; the schema insists on it.
		if _, err := pool.Exec(ctx, `
			INSERT INTO artists (id, musicbrainz_id, name, sort_name, followed_at, catalogue_summary)
			VALUES (
				$1, $2, $3, $4,
				CASE WHEN $5::boolean THEN now() ELSE NULL END,
				CASE WHEN $5::boolean THEN NULL ELSE 'Kept because your library holds their music.' END
			)
		`, uuid.New(), uuid.New(), entry.name, entry.sortName, entry.followed); err != nil {
			t.Fatalf("seed artist %s: %v", entry.name, err)
		}
	}
}

func listedArtistNames(rows []ListArtistsRow) []string {
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.Name)
	}
	return names
}

// A page boundary must not disturb the order the page promises: one run
// through the filing names, whether or not an artist is followed.
//
// Followed artists used to come first, which made the list start at A, run to
// the end of the followed ones, and then start at A again — under a control
// that said "Name". Being followed now breaks a tie and nothing more, and the
// scope picker is where "only the ones I follow" is asked for.
func TestListArtistsPagesInSortOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedArtistRoster(t, ctx, pool)
	queries := New(pool)

	var paged []string
	for offset := int32(0); offset < 6; offset += 2 {
		page, err := queries.ListArtists(ctx, ListArtistsParams{Limit: 2, Offset: offset})
		if err != nil {
			t.Fatalf("ListArtists(offset=%d) error = %v", offset, err)
		}
		paged = append(paged, listedArtistNames(page)...)
	}

	// The filing name is what is sorted, so The Knife is under K and stands
	// between Boards of Canada and Portishead.
	want := []string{"Aphex Twin", "Boards of Canada", "The Knife", "Portishead", "Sewerslvt"}
	if strings.Join(paged, "|") != strings.Join(want, "|") {
		t.Errorf("paged = %v, want %v", paged, want)
	}
}

// A row says whether a picture of the artist is cached. A recorded absence is
// a row with no image, and counts as no picture.
func TestListArtistsSayWhetherAPictureIsCached(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedArtistRoster(t, ctx, pool)
	queries := New(pool)

	if _, err := pool.Exec(ctx, `
		INSERT INTO artist_images (artist_id, image, content_type, source)
		SELECT id, '\x89504e47'::bytea, 'image/png', 'fanart' FROM artists WHERE name = 'Portishead'
	`); err != nil {
		t.Fatalf("seed picture: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO artist_images (artist_id, image, content_type, source)
		SELECT id, NULL, '', 'none' FROM artists WHERE name = 'Aphex Twin'
	`); err != nil {
		t.Fatalf("seed recorded absence: %v", err)
	}

	rows, err := queries.ListArtists(ctx, ListArtistsParams{})
	if err != nil {
		t.Fatalf("ListArtists() error = %v", err)
	}

	pictured := map[string]bool{}
	for _, row := range rows {
		pictured[row.Name] = row.HasImage
	}
	if !pictured["Portishead"] || pictured["Aphex Twin"] || pictured["The Knife"] {
		t.Errorf("pictured = %v, want only Portishead", pictured)
	}
}

// The list is read by a person, and a person reads A to Z once. PostgreSQL
// compares text by byte in this database, and every capital letter has a lower
// byte than every small one, so an unfolded sort put AZALEA before American
// Football and left nthng below Yung Lean at the very end of the list.
func TestListArtistsSortsWithoutRegardToCase(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	for _, name := range []string{"nthng", "AZALEA", "American Football"} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO artists (id, musicbrainz_id, name, sort_name, followed_at)
			VALUES ($1, $2, $3, $3, now())
		`, uuid.New(), uuid.New(), name); err != nil {
			t.Fatalf("seed artist %s: %v", name, err)
		}
	}

	queries := New(pool)
	listed, err := queries.ListArtists(ctx, ListArtistsParams{Limit: 50})
	if err != nil {
		t.Fatalf("ListArtists() error = %v", err)
	}

	want := []string{"American Football", "AZALEA", "nthng"}
	if got := listedArtistNames(listed); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("listed = %v, want %v", got, want)
	}
}

func TestListArtistsHeldScopeExcludesFollowedArtists(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedArtistRoster(t, ctx, pool)
	queries := New(pool)

	page, err := queries.ListArtists(ctx, ListArtistsParams{Scope: "held", Limit: 50})
	if err != nil {
		t.Fatalf("ListArtists() error = %v", err)
	}

	for _, row := range page {
		if row.FollowedAt.Valid {
			t.Errorf("held scope returned followed artist %s", row.Name)
		}
	}
	if len(page) != 3 {
		t.Errorf("held artists = %v, want 3", listedArtistNames(page))
	}
}

func TestListArtistsFollowedScopeExcludesHeldArtists(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedArtistRoster(t, ctx, pool)
	queries := New(pool)

	page, err := queries.ListArtists(ctx, ListArtistsParams{Scope: "followed", Limit: 50})
	if err != nil {
		t.Fatalf("ListArtists() error = %v", err)
	}

	for _, row := range page {
		if !row.FollowedAt.Valid {
			t.Errorf("followed scope returned held artist %s", row.Name)
		}
	}
	if len(page) != 2 {
		t.Errorf("followed artists = %v, want 2", listedArtistNames(page))
	}
}

// Searching has to reach the whole list rather than the page in front of the
// user, and the sort name is part of how an artist is spelled.
func TestListArtistsSearchMatchesTheSortName(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedArtistRoster(t, ctx, pool)
	queries := New(pool)

	page, err := queries.ListArtists(ctx, ListArtistsParams{Query: "knife, the", Limit: 50})
	if err != nil {
		t.Fatalf("ListArtists() error = %v", err)
	}

	if len(page) != 1 || page[0].Name != "The Knife" {
		t.Errorf("search results = %v, want just The Knife", listedArtistNames(page))
	}
}

// The totals are what tell the user the list continues past the page in front
// of them, so they count the whole list and both of its halves.
func TestArtistListTotalsCountPastTheEndOfThePage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedArtistRoster(t, ctx, pool)
	queries := New(pool)

	totals, err := queries.ArtistListTotals(ctx, ArtistListTotalsParams{})
	if err != nil {
		t.Fatalf("ArtistListTotals() error = %v", err)
	}
	if totals.FollowedCount != 2 || totals.HeldCount != 3 {
		t.Errorf("totals = %#v, want 2 followed and 3 held", totals)
	}
}

func TestArtistListTotalsNarrowToTheSearch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedArtistRoster(t, ctx, pool)
	queries := New(pool)

	totals, err := queries.ArtistListTotals(ctx, ArtistListTotalsParams{Query: "knife"})
	if err != nil {
		t.Fatalf("ArtistListTotals() error = %v", err)
	}
	if totals.FollowedCount != 0 || totals.HeldCount != 1 {
		t.Errorf("totals = %#v, want only the searched artist counted", totals)
	}
}

// Every list search builds an ILIKE pattern out of what the user typed, and
// the tests for that live together so the next one written stays honest about
// it: a query is text, not a pattern, and the characters that mean something
// inside a pattern have to be escaped before they get there.
//
// wildcardSearchSubjects are the names those tests search among. Each case's
// row is seeded next to the row a pattern would have dragged in beside it:
// once for escaping nothing at all, and once — `A\XB Sound` — for escaping the
// two wildcards but not the escape character that has to carry them.
var wildcardSearchSubjects = []string{
	"50% Chance", "5000 Volts", "A_B Sound", "AXB Sound", `A\_B Tape`, `A\XB Sound`,
}

var wildcardSearchCases = []struct {
	name, query, want string
}{
	{"per cent sign", "50%", "50% Chance"},
	{"underscore", "a_b", "A_B Sound"},
	{"the escape character itself", `\_`, `A\_B Tape`},
}

func seedWildcardArtists(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	for _, subject := range wildcardSearchSubjects {
		if _, err := pool.Exec(ctx, `
			INSERT INTO artists (id, musicbrainz_id, name, sort_name, followed_at)
			VALUES ($1, $2, $3, $3, now())
		`, uuid.New(), uuid.New(), subject); err != nil {
			t.Fatalf("seed artist %s: %v", subject, err)
		}
	}
}

func TestListArtistsSearchMatchesWildcardCharactersLiterally(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedWildcardArtists(t, ctx, pool)
	queries := New(pool)

	for _, testCase := range wildcardSearchCases {
		t.Run(testCase.name, func(t *testing.T) {
			page, err := queries.ListArtists(ctx, ListArtistsParams{Query: testCase.query, Limit: 50})
			if err != nil {
				t.Fatalf("ListArtists(%q) error = %v", testCase.query, err)
			}
			if len(page) != 1 || page[0].Name != testCase.want {
				t.Errorf("search for %q = %v, want just %q",
					testCase.query, listedArtistNames(page), testCase.want)
			}
		})
	}
}

func TestArtistListTotalsCountWildcardCharactersLiterally(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedWildcardArtists(t, ctx, pool)
	queries := New(pool)

	for _, testCase := range wildcardSearchCases {
		t.Run(testCase.name, func(t *testing.T) {
			totals, err := queries.ArtistListTotals(ctx, ArtistListTotalsParams{Query: testCase.query})
			if err != nil {
				t.Fatalf("ArtistListTotals(%q) error = %v", testCase.query, err)
			}
			if totals.FollowedCount != 1 {
				t.Errorf("totals for %q = %#v, want the one matching artist counted",
					testCase.query, totals)
			}
		})
	}
}

func TestListLibraryFilesSearchMatchesWildcardCharactersLiterally(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	for _, subject := range wildcardSearchSubjects {
		seedLibraryFile(t, ctx, pool, "/music/"+subject+".flac")
	}
	queries := New(pool)

	for _, testCase := range wildcardSearchCases {
		t.Run(testCase.name, func(t *testing.T) {
			// The count and the page are separate statements, and a search
			// that reached only one of them would report a total the page
			// cannot show.
			page, err := queries.ListLibraryFiles(ctx, ListLibraryFilesParams{
				Query: testCase.query, Limit: 50,
			})
			if err != nil {
				t.Fatalf("ListLibraryFiles(%q) error = %v", testCase.query, err)
			}
			want := "/music/" + testCase.want + ".flac"
			if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Path != want {
				t.Errorf("search for %q = %v (total %d), want just %q",
					testCase.query, listedFilePaths(page.Items), page.Total, want)
			}
		})
	}
}

func TestListAlbumsSearchMatchesWildcardCharactersLiterally(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedWildcardReleases(t, ctx, pool)
	queries := New(pool)

	for _, testCase := range wildcardSearchCases {
		t.Run(testCase.name, func(t *testing.T) {
			// The count and the page are separate statements sharing one
			// filter, and a search that reached only one of them would report
			// a total the page cannot show.
			page, err := queries.ListAlbums(ctx, ListAlbumsParams{
				Query: testCase.query, Limit: 50,
			})
			if err != nil {
				t.Fatalf("ListAlbums(%q) error = %v", testCase.query, err)
			}
			if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Title != testCase.want {
				t.Errorf("search for %q = %v (total %d), want just %q",
					testCase.query, listedReleaseTitles(page), page.Total, testCase.want)
			}
		})
	}
}

// seedWildcardReleases puts the wildcard subjects on one artist's shelf, as
// release titles. The artist is named after none of them so that the two halves
// of the searched expression cannot cover for each other.
func seedWildcardReleases(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	artistID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name, followed_at)
		VALUES ($1, $2, 'Pattern Test', 'Pattern Test', now())
	`, artistID, uuid.New()); err != nil {
		t.Fatalf("seed artist: %v", err)
	}
	for _, subject := range wildcardSearchSubjects {
		if _, err := pool.Exec(ctx, `
			INSERT INTO albums (id, artist_id, musicbrainz_release_group_id, title, release_date, album_type)
			VALUES ($1, $2, $3, $4, '2020-01-01'::date, 'album')
		`, uuid.New(), artistID, uuid.New(), subject); err != nil {
			t.Fatalf("seed album %s: %v", subject, err)
		}
	}
}

func listedFilePaths(rows []LibraryFileRow) []string {
	paths := make([]string, 0, len(rows))
	for _, row := range rows {
		paths = append(paths, row.Path)
	}
	return paths
}

// An artist being refreshed on some other page is still being refreshed, so
// the count that decides whether the page keeps polling ignores the search.
func TestArtistListTotalsCountRefreshesOutsideTheSearch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedArtistRoster(t, ctx, pool)
	queries := New(pool)

	var artistID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT id FROM artists WHERE name = 'Aphex Twin'
	`).Scan(&artistID); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.QueueArtistRefresh(ctx, artistID); err != nil {
		t.Fatalf("QueueArtistRefresh() error = %v", err)
	}

	totals, err := queries.ArtistListTotals(ctx, ArtistListTotalsParams{Query: "knife"})
	if err != nil {
		t.Fatalf("ArtistListTotals() error = %v", err)
	}
	if totals.RefreshingCount != 1 {
		t.Errorf("refreshing = %d, want the queued refresh counted", totals.RefreshingCount)
	}
}

// seedReleaseShelf builds a catalogue holding one release of every shape the
// browser can filter by, across a followed artist and a held one, so the
// filters can be checked against releases whose shape is known rather than
// against the counts the same query computes.
func seedReleaseShelf(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	shelf := []struct {
		artist      string
		followed    bool
		title       string
		releaseDate string
		// tracks is one entry per track: "owned", "missing" for a mapping to a
		// file that has gone from disk, or "absent" for no mapping at all.
		tracks  []string
		edition bool
		job     string
	}{
		{"Portishead", true, "Dummy", "1994-08-22", []string{"absent", "absent"}, true, ""},
		{"Portishead", true, "Third", "2008-04-27", nil, false, ""},
		{"Boards of Canada", true, "Music Has the Right to Children", "1998-04-20",
			[]string{"owned", "missing"}, true, ""},
		{"Boards of Canada", true, "Geogaddi", "2002-02-18", []string{"owned", "owned"}, true, ""},
		{"Sewerslvt", false, "Draining Love Story", "2020-06-05", nil, false, "failed"},
		{"Sewerslvt", false, "We Had Good Times Together", "2021-03-19",
			[]string{"owned", "owned"}, true, ""},
	}

	artists := map[string]uuid.UUID{}
	for index, entry := range shelf {
		artistID, ok := artists[entry.artist]
		if !ok {
			artistID = uuid.New()
			artists[entry.artist] = artistID
			if _, err := pool.Exec(ctx, `
				INSERT INTO artists (id, musicbrainz_id, name, sort_name, followed_at, catalogue_summary)
				VALUES (
					$1, $2, $3, $3,
					CASE WHEN $4::boolean THEN now() ELSE NULL END,
					CASE WHEN $4::boolean THEN NULL ELSE 'Kept because your library holds their music.' END
				)
			`, artistID, uuid.New(), entry.artist, entry.followed); err != nil {
				t.Fatalf("seed artist %s: %v", entry.artist, err)
			}
		}

		albumID := uuid.New()
		if _, err := pool.Exec(ctx, `
			INSERT INTO albums (id, artist_id, musicbrainz_release_group_id, title, release_date, album_type)
			VALUES ($1, $2, $3, $4, $5::date, 'album')
		`, albumID, artistID, uuid.New(), entry.title, entry.releaseDate); err != nil {
			t.Fatalf("seed album %s: %v", entry.title, err)
		}
		if entry.edition {
			if _, err := pool.Exec(ctx, `
				INSERT INTO release_editions (album_id, musicbrainz_release_id, title, selection_reason)
				VALUES ($1, $2, $3, 'only release')
			`, albumID, uuid.New(), entry.title); err != nil {
				t.Fatalf("seed edition %s: %v", entry.title, err)
			}
		}
		if entry.job != "" {
			if _, err := pool.Exec(ctx, `
				INSERT INTO jobs (kind, status, payload)
				VALUES ('refresh_album_metadata', $1, jsonb_build_object('albumId', $2::uuid))
			`, entry.job, albumID); err != nil {
				t.Fatalf("seed refresh job %s: %v", entry.title, err)
			}
		}
		for position, ownership := range entry.tracks {
			trackID := uuid.New()
			if _, err := pool.Exec(ctx, `
				INSERT INTO tracks (id, album_id, title, disc_number, track_number)
				VALUES ($1, $2, $3, 1, $4)
			`, trackID, albumID, entry.title+" track", position+1); err != nil {
				t.Fatalf("seed track %s: %v", entry.title, err)
			}
			if ownership == "absent" {
				continue
			}
			fileID := seedLibraryFile(t, ctx, pool,
				fmt.Sprintf("/music/%d-%d-%s.flac", index, position, ownership))
			if ownership == "missing" {
				if _, err := pool.Exec(ctx,
					`UPDATE library_files SET missing_at = now() WHERE id = $1`, fileID); err != nil {
					t.Fatalf("mark file missing: %v", err)
				}
			}
			if _, err := pool.Exec(ctx, `
				INSERT INTO track_mappings (track_id, library_file_id, method)
				VALUES ($1, $2, 'exact')
			`, trackID, fileID); err != nil {
				t.Fatalf("seed mapping %s: %v", entry.title, err)
			}
		}
	}
}

func listedReleaseTitles(page AlbumPage) []string {
	titles := make([]string, 0, len(page.Items))
	for _, row := range page.Items {
		titles = append(titles, row.Title)
	}
	return titles
}

func listReleases(t *testing.T, ctx context.Context, pool *pgxpool.Pool, params ListAlbumsParams) AlbumPage {
	t.Helper()

	page, err := New(pool).ListAlbums(ctx, params)
	if err != nil {
		t.Fatalf("ListAlbums(%+v) error = %v", params, err)
	}
	return page
}

// A page boundary must not disturb the order the page promises, which is what
// makes the walk past the end of one page the rest of the same list.
func TestListAlbumsPagesInReleaseOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedReleaseShelf(t, ctx, pool)

	var paged []string
	for offset := int32(0); offset < 6; offset += 2 {
		page := listReleases(t, ctx, pool, ListAlbumsParams{Sort: "year", Limit: 2, Offset: offset})
		paged = append(paged, listedReleaseTitles(page)...)
	}

	want := []string{
		"Dummy", "Music Has the Right to Children", "Geogaddi", "Third",
		"Draining Love Story", "We Had Good Times Together",
	}
	if strings.Join(paged, "|") != strings.Join(want, "|") {
		t.Errorf("paged = %v, want %v", paged, want)
	}
}

// A row says whether a picture of the release is cached. A recorded absence
// is a row with no image, and counts as no cover: the list would otherwise ask
// for a picture the archive already said does not exist.
func TestListAlbumsSayWhetherACoverIsCached(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedReleaseShelf(t, ctx, pool)

	if _, err := pool.Exec(ctx, `
		INSERT INTO release_cover_art (album_id, image, content_type, source)
		SELECT id, '\x89504e47'::bytea, 'image/png', 'caa' FROM albums WHERE title = 'Dummy'
	`); err != nil {
		t.Fatalf("seed cover: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO release_cover_art (album_id, image, content_type, source)
		SELECT id, NULL, '', 'none' FROM albums WHERE title = 'Third'
	`); err != nil {
		t.Fatalf("seed recorded absence: %v", err)
	}

	page := listReleases(t, ctx, pool, ListAlbumsParams{Sort: "artist", Limit: 10})

	pictured := map[string]bool{}
	for _, row := range page.Items {
		pictured[row.Title] = row.HasCover
	}
	if !pictured["Dummy"] || pictured["Third"] || pictured["Geogaddi"] {
		t.Errorf("pictured = %v, want only Dummy", pictured)
	}
}

func TestListAlbumsOrdersByArtistBeforeYear(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedReleaseShelf(t, ctx, pool)

	page := listReleases(t, ctx, pool, ListAlbumsParams{Sort: "artist", Limit: 10})

	want := []string{
		"Music Has the Right to Children", "Geogaddi", "Dummy", "Third",
		"Draining Love Story", "We Had Good Times Together",
	}
	if got := listedReleaseTitles(page); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("titles = %v, want %v", got, want)
	}
}

func TestListAlbumsReversesTheOrderOnRequest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedReleaseShelf(t, ctx, pool)

	page := listReleases(t, ctx, pool, ListAlbumsParams{Sort: "year", Descending: true, Limit: 2})

	want := []string{"We Had Good Times Together", "Draining Love Story"}
	if got := listedReleaseTitles(page); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("titles = %v, want %v", got, want)
	}
}

// Following an artist is a claim about their whole discography; owning a track
// is a claim about one release. The library scope is both of them, which is why
// it reaches a held artist's release and not the rest of their catalogue.
func TestListAlbumsLibraryScopeReachesOwnedReleasesOfHeldArtists(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedReleaseShelf(t, ctx, pool)

	page := listReleases(t, ctx, pool, ListAlbumsParams{Scope: "library", Sort: "year", Limit: 10})

	want := []string{
		"Dummy", "Music Has the Right to Children", "Geogaddi", "Third",
		"We Had Good Times Together",
	}
	if got := listedReleaseTitles(page); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("titles = %v, want %v", got, want)
	}
}

func TestListAlbumsFollowedScopeExcludesHeldArtists(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedReleaseShelf(t, ctx, pool)

	page := listReleases(t, ctx, pool, ListAlbumsParams{Scope: "followed", Sort: "year", Limit: 10})

	for _, row := range page.Items {
		if !row.ArtistFollowed {
			t.Errorf("held release %q in the followed scope", row.Title)
		}
	}
	if len(page.Items) != 4 {
		t.Errorf("titles = %v, want the four followed releases", listedReleaseTitles(page))
	}
}

// Searching has to reach the whole list rather than the page in front of the
// user, and an artist's name is how most people look for their releases.
func TestListAlbumsSearchMatchesTheArtistName(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedReleaseShelf(t, ctx, pool)

	page := listReleases(t, ctx, pool, ListAlbumsParams{Query: "portis", Sort: "year", Limit: 10})

	want := []string{"Dummy", "Third"}
	if got := listedReleaseTitles(page); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("titles = %v, want %v", got, want)
	}
}

func TestListAlbumsSearchMatchesTheReleaseTitle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedReleaseShelf(t, ctx, pool)

	page := listReleases(t, ctx, pool, ListAlbumsParams{Query: "geogaddi", Limit: 10})

	if got := listedReleaseTitles(page); strings.Join(got, "|") != "Geogaddi" {
		t.Errorf("titles = %v, want Geogaddi", got)
	}
}

// The filters have to name the same release the table names. They are written
// as existence tests so a page stays bounded, while the columns beside them are
// counted — two different pieces of SQL that must agree about every release.
func TestListAlbumsStatusFiltersAgreeWithTheCountedColumns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedReleaseShelf(t, ctx, pool)

	cases := []struct {
		status string
		want   string
	}{
		{"owned", "Geogaddi|We Had Good Times Together"},
		{"partial", "Music Has the Right to Children"},
		{"missing", "Dummy"},
		{"untracked", "Third"},
		{"failed", "Draining Love Story"},
	}
	for _, testCase := range cases {
		page := listReleases(t, ctx, pool,
			ListAlbumsParams{Status: testCase.status, Sort: "year", Limit: 10})
		if got := strings.Join(listedReleaseTitles(page), "|"); got != testCase.want {
			t.Errorf("status %q = %v, want %v", testCase.status, got, testCase.want)
		}
	}
}

// A mapping to a file that has gone from disk is kept, because the decision
// behind it is still good, but the release is no longer owned outright.
func TestListAlbumsDoesNotCountTracksWhoseFileIsMissing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedReleaseShelf(t, ctx, pool)

	page := listReleases(t, ctx, pool, ListAlbumsParams{Query: "Music Has the Right", Limit: 10})

	if len(page.Items) != 1 {
		t.Fatalf("titles = %v, want one release", listedReleaseTitles(page))
	}
	if page.Items[0].TrackCount != 2 || page.Items[0].OwnedTrackCount != 1 {
		t.Errorf("owned = %d of %d, want 1 of 2",
			page.Items[0].OwnedTrackCount, page.Items[0].TrackCount)
	}
}

// The totals are what tell the user the list continues past the page in front
// of them, and what a status filter is holding back.
func TestListAlbumsCountsPastTheEndOfThePage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedReleaseShelf(t, ctx, pool)

	page := listReleases(t, ctx, pool,
		ListAlbumsParams{Scope: "library", Status: "owned", Sort: "year", Limit: 1})

	if len(page.Items) != 1 {
		t.Fatalf("items = %d, want the one that fits", len(page.Items))
	}
	if page.Total != 2 {
		t.Errorf("total = %d, want the two owned releases", page.Total)
	}
	if page.ScopeTotal != 5 {
		t.Errorf("scope total = %d, want the five the library scope reaches", page.ScopeTotal)
	}
}

// Asking for no limit asks for the whole list, which the views that total the
// catalogue rather than browse it still need.
func TestListAlbumsWithoutALimitReturnsEveryRelease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedReleaseShelf(t, ctx, pool)

	page := listReleases(t, ctx, pool, ListAlbumsParams{})

	if len(page.Items) != 6 || page.Total != 6 {
		t.Errorf("items = %d, total = %d, want all six releases", len(page.Items), page.Total)
	}
}

// An unpaged list still has to say how much its status filter is holding back.
// The rows it returns cannot answer that: they are the ones that passed it.
func TestListAlbumsCountsTheScopeBehindAnUnpagedStatusFilter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedReleaseShelf(t, ctx, pool)

	page := listReleases(t, ctx, pool, ListAlbumsParams{Scope: "library", Status: "owned"})

	if page.Total != 2 {
		t.Errorf("total = %d, want the two owned releases", page.Total)
	}
	if page.ScopeTotal != 5 {
		t.Errorf("scope total = %d, want the five the library scope reaches", page.ScopeTotal)
	}
}

// "owned" used to be the example of an ordering this refused, because sorting
// by how complete a release is meant counting every track of every release
// first. A release carries that number now, so the refusal is only for orders
// nobody named.
func TestListAlbumsRefusesAnOrderingItDoesNotKnow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	if _, err := New(pool).ListAlbums(ctx, ListAlbumsParams{Sort: "loudness", Limit: 10}); err == nil {
		t.Error("ListAlbums(sort=loudness) error = nil, want a refusal")
	}
}

// Least complete first, which is what somebody looking for what to fetch next
// is asking for. A release with nothing to count has no completeness and comes
// before the ones that have.
func TestListAlbumsOrdersByHowCompleteAReleaseIs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedReleaseShelf(t, ctx, pool)

	page := listReleases(t, ctx, pool, ListAlbumsParams{Scope: "followed", Sort: "owned", Limit: 10})

	want := "Third|Dummy|Music Has the Right to Children|Geogaddi"
	if got := strings.Join(listedReleaseTitles(page), "|"); got != want {
		t.Errorf("least complete first = %v, want %v", got, want)
	}
}

// Most complete first is the same order read backwards, which is what asking
// for it descending has to mean.
func TestListAlbumsOrdersByCompletenessBackwards(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedReleaseShelf(t, ctx, pool)

	page := listReleases(t, ctx, pool,
		ListAlbumsParams{Scope: "followed", Sort: "owned", Descending: true, Limit: 10})

	want := "Geogaddi|Music Has the Right to Children|Dummy|Third"
	if got := strings.Join(listedReleaseTitles(page), "|"); got != want {
		t.Errorf("most complete first = %v, want %v", got, want)
	}
}

// The browser's six chips each say how many releases they hold, and they are
// all answered by the pass that counts the scope.
func TestListAlbumsCountsEveryStatusInTheOnePass(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedReleaseShelf(t, ctx, pool)

	page := listReleases(t, ctx, pool, ListAlbumsParams{Status: "owned", Sort: "year", Limit: 10})

	want := map[string]int64{"owned": 2, "partial": 1, "missing": 1, "untracked": 1, "failed": 1}
	for status, count := range want {
		if page.StatusTotals[status] != count {
			t.Errorf("%s = %d, want %d", status, page.StatusTotals[status], count)
		}
	}
	if page.ScopeTotal != 6 {
		t.Errorf("scope total = %d, want all six releases", page.ScopeTotal)
	}
}

// countedRelease reads one release back through the list the browser uses, so
// what is asserted is what the page would show.
func countedRelease(t *testing.T, ctx context.Context, pool *pgxpool.Pool, title string) AlbumRow {
	t.Helper()

	page := listReleases(t, ctx, pool, ListAlbumsParams{Query: title, Limit: 10})
	if len(page.Items) != 1 {
		t.Fatalf("searching %q listed %v, want one release", title, listedReleaseTitles(page))
	}
	return page.Items[0]
}

func trackOnRelease(t *testing.T, ctx context.Context, pool *pgxpool.Pool, title string, position int) uuid.UUID {
	t.Helper()

	var trackID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT tracks.id FROM tracks
		JOIN albums ON albums.id = tracks.album_id
		WHERE albums.title = $1 AND tracks.track_number = $2
	`, title, position).Scan(&trackID); err != nil {
		t.Fatalf("read track %d of %s: %v", position, title, err)
	}
	return trackID
}

func TestAMappingWrittenMakesTheReleaseCountThatTrackOwned(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedReleaseShelf(t, ctx, pool)

	if _, err := pool.Exec(ctx, `
		INSERT INTO track_mappings (track_id, library_file_id, method) VALUES ($1, $2, 'exact')
	`, trackOnRelease(t, ctx, pool, "Dummy", 1),
		seedLibraryFile(t, ctx, pool, "/music/dummy-1.flac")); err != nil {
		t.Fatalf("write mapping: %v", err)
	}

	if owned := countedRelease(t, ctx, pool, "Dummy").OwnedTrackCount; owned != 1 {
		t.Errorf("owned = %d, want the one track just mapped", owned)
	}
}

func TestAMappingWithdrawnStopsTheReleaseCountingThatTrack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedReleaseShelf(t, ctx, pool)

	if _, err := pool.Exec(ctx, `
		DELETE FROM track_mappings WHERE track_id = $1
	`, trackOnRelease(t, ctx, pool, "Geogaddi", 1)); err != nil {
		t.Fatalf("withdraw mapping: %v", err)
	}

	if owned := countedRelease(t, ctx, pool, "Geogaddi").OwnedTrackCount; owned != 1 {
		t.Errorf("owned = %d, want the one mapping left", owned)
	}
}

// A mapping that moves has to move both counts: the release that lost it counts
// one fewer, and the release that gained it counts one more.
func TestAMappingMovedToAnotherReleaseMovesBothCounts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedReleaseShelf(t, ctx, pool)

	if _, err := pool.Exec(ctx, `
		UPDATE track_mappings SET track_id = $1 WHERE track_id = $2
	`, trackOnRelease(t, ctx, pool, "Dummy", 1),
		trackOnRelease(t, ctx, pool, "Geogaddi", 1)); err != nil {
		t.Fatalf("move mapping: %v", err)
	}

	if owned := countedRelease(t, ctx, pool, "Geogaddi").OwnedTrackCount; owned != 1 {
		t.Errorf("Geogaddi owned = %d, want one fewer", owned)
	}
	if owned := countedRelease(t, ctx, pool, "Dummy").OwnedTrackCount; owned != 1 {
		t.Errorf("Dummy owned = %d, want the mapping it gained", owned)
	}
}

// A file that has gone from disk is not ownership today, and the release says
// so as soon as the scanner writes that it is gone.
func TestAFileGoneFromDiskLeavesTheOwnedCount(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedReleaseShelf(t, ctx, pool)

	if _, err := pool.Exec(ctx, `
		UPDATE library_files SET missing_at = now()
		WHERE id = (SELECT library_file_id FROM track_mappings WHERE track_id = $1)
	`, trackOnRelease(t, ctx, pool, "Geogaddi", 1)); err != nil {
		t.Fatalf("mark file missing: %v", err)
	}

	if owned := countedRelease(t, ctx, pool, "Geogaddi").OwnedTrackCount; owned != 1 {
		t.Errorf("owned = %d, want the one file still on disk", owned)
	}
}

// A scan that finds everything where it left it writes every file it saw,
// missing_at among them. What this pins is that the counts sit still through
// that: a trigger that read a written row as a file that had gone would take
// every owned track off every release the scan walked past. It cannot pin the
// early return beside that check, which saves work rather than changing an
// answer — only the clock can show that one missing.
func TestAScanThatChangesNothingLeavesTheCountsAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedReleaseShelf(t, ctx, pool)

	before := countedRelease(t, ctx, pool, "Geogaddi")
	var updated int64
	if err := pool.QueryRow(ctx, `
		WITH touched AS (
			UPDATE library_files SET scanned_at = now(), missing_at = missing_at RETURNING 1
		)
		SELECT count(*) FROM touched
	`).Scan(&updated); err != nil {
		t.Fatalf("touch every file: %v", err)
	}
	if updated == 0 {
		t.Fatal("the scan touched no files, so this proves nothing")
	}

	if after := countedRelease(t, ctx, pool, "Geogaddi"); after.OwnedTrackCount != before.OwnedTrackCount {
		t.Errorf("owned = %d, want the %d it was before the scan",
			after.OwnedTrackCount, before.OwnedTrackCount)
	}
}

// Deleting a library file takes its mappings with it, and no Go call site
// writes that deletion. The counts still have to follow it.
func TestAFileDeletedLeavesTheOwnedCount(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedReleaseShelf(t, ctx, pool)

	if _, err := pool.Exec(ctx, `
		DELETE FROM library_files
		WHERE id = (SELECT library_file_id FROM track_mappings WHERE track_id = $1)
	`, trackOnRelease(t, ctx, pool, "Geogaddi", 1)); err != nil {
		t.Fatalf("delete library file: %v", err)
	}

	if owned := countedRelease(t, ctx, pool, "Geogaddi").OwnedTrackCount; owned != 1 {
		t.Errorf("owned = %d, want the one mapping the deletion left", owned)
	}
}

func TestATrackAddedToAReleaseIsCounted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedReleaseShelf(t, ctx, pool)

	if _, err := pool.Exec(ctx, `
		INSERT INTO tracks (album_id, title, disc_number, track_number)
		SELECT id, 'Dummy track 3', 1, 3 FROM albums WHERE title = 'Dummy'
	`); err != nil {
		t.Fatalf("add track: %v", err)
	}

	if tracks := countedRelease(t, ctx, pool, "Dummy").TrackCount; tracks != 3 {
		t.Errorf("tracks = %d, want the three it now lists", tracks)
	}
}

func TestATrackRemovedFromAReleaseStopsBeingCounted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedReleaseShelf(t, ctx, pool)

	if _, err := pool.Exec(ctx,
		`DELETE FROM tracks WHERE id = $1`, trackOnRelease(t, ctx, pool, "Dummy", 2)); err != nil {
		t.Fatalf("remove track: %v", err)
	}

	if tracks := countedRelease(t, ctx, pool, "Dummy").TrackCount; tracks != 1 {
		t.Errorf("tracks = %d, want the one it still lists", tracks)
	}
}

// Deciding not to want a recording takes it out of what is still to get, so a
// release short of nothing else stops being partial.
func TestADismissedTrackLeavesWhatIsStillToGet(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedRecordingOnTwoReleases(t, ctx, pool)

	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets
			(origin, entry_artist, entry_title, musicbrainz_recording_id, status, not_wanted_at)
		SELECT 'manual', 'Cynthoni', tracks.title, tracks.musicbrainz_recording_id, 'not_wanted', now()
		FROM tracks WHERE tracks.title = 'Even Further Down the Drain'
	`); err != nil {
		t.Fatalf("dismiss recording: %v", err)
	}

	compilation := countedRelease(t, ctx, pool, "Pt. 1 & 2")
	if compilation.DismissedTrackCount != 1 {
		t.Errorf("dismissed = %d, want the one recording just dismissed",
			compilation.DismissedTrackCount)
	}
	owned := listReleases(t, ctx, pool, ListAlbumsParams{Status: "owned", Sort: "year", Limit: 10})
	if got := strings.Join(listedReleaseTitles(owned), "|"); got != "Pt. 1|Pt. 1 & 2" {
		t.Errorf("owned = %v, want both, the compilation short of nothing it wants", got)
	}
}

// Taking a dismissal back puts the track back among the ones still to get,
// which is the half of the decision the acquisition loop reopens
// (PursueAcquisitionTargetAgain).
func TestAWantNoLongerDismissedCountsAsStillToGetAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedRecordingOnTwoReleases(t, ctx, pool)

	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets
			(origin, entry_artist, entry_title, musicbrainz_recording_id, status, not_wanted_at)
		SELECT 'manual', 'Cynthoni', tracks.title, tracks.musicbrainz_recording_id, 'not_wanted', now()
		FROM tracks WHERE tracks.title = 'Even Further Down the Drain'
	`); err != nil {
		t.Fatalf("dismiss recording: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET status = 'pending', not_wanted_at = NULL
		WHERE status = 'not_wanted'
	`); err != nil {
		t.Fatalf("want it again: %v", err)
	}

	if dismissed := countedRelease(t, ctx, pool, "Pt. 1 & 2").DismissedTrackCount; dismissed != 0 {
		t.Errorf("dismissed = %d, want nothing dismissed once it is wanted again", dismissed)
	}
	partial := listReleases(t, ctx, pool, ListAlbumsParams{Status: "partial", Sort: "year", Limit: 10})
	if got := strings.Join(listedReleaseTitles(partial), "|"); got != "Pt. 1 & 2" {
		t.Errorf("partial = %v, want the compilation short of it again", got)
	}
}

// countsThatDisagree lists every release whose stored counts differ from the
// counts derived from the tables they come from. It is a second reading of the
// same rule on purpose: the columns are maintained by trigger and nothing
// recomputes them on the way out, so a trigger that did not fire would be
// invisible to every other assertion here.
func countsThatDisagree(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []string {
	t.Helper()

	rows, err := pool.Query(ctx, `
		SELECT albums.title
		FROM albums
		CROSS JOIN LATERAL (
			SELECT count(tracks.id)::integer AS track_count,
			       count(tracks.id) FILTER (WHERE decided.owned)::integer AS owned,
			       count(tracks.id) FILTER (
			           WHERE NOT decided.owned AND decided.dismissed
			       )::integer AS dismissed
			FROM tracks
			LEFT JOIN LATERAL (
				SELECT (EXISTS (
					SELECT 1 FROM track_mappings
					JOIN library_files ON library_files.id = track_mappings.library_file_id
					WHERE track_mappings.track_id = tracks.id AND library_files.missing_at IS NULL
				) OR EXISTS (
					SELECT 1 FROM library_file_identities
					JOIN library_files ON library_files.id = library_file_identities.library_file_id
					WHERE library_file_identities.musicbrainz_recording_id = tracks.musicbrainz_recording_id
					  AND library_files.missing_at IS NULL
				) OR EXISTS (
					SELECT 1 FROM tracks AS carrying
					JOIN track_mappings ON track_mappings.track_id = carrying.id
					JOIN library_files ON library_files.id = track_mappings.library_file_id
					WHERE carrying.musicbrainz_recording_id = tracks.musicbrainz_recording_id
					  AND library_files.missing_at IS NULL
				)) AS owned,
				EXISTS (
					SELECT 1 FROM acquisition_targets
					WHERE tracks.musicbrainz_recording_id IS NOT NULL
					  AND acquisition_targets.musicbrainz_recording_id = tracks.musicbrainz_recording_id
					  AND acquisition_targets.status = 'not_wanted'
				) AS dismissed
			) AS decided ON true
			WHERE tracks.album_id = albums.id
		) AS derived
		WHERE (albums.track_count, albums.owned_track_count, albums.dismissed_track_count)
		      IS DISTINCT FROM (derived.track_count, derived.owned, derived.dismissed)
		ORDER BY albums.title
	`)
	if err != nil {
		t.Fatalf("compare stored counts with derived ones: %v", err)
	}
	disagreeing, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("compare stored counts with derived ones: %v", err)
	}
	return disagreeing
}

// Every way a count can move, in one transaction each, checked against the
// tables rather than against the number the last assertion expected.
func TestTheStoredCountsSayWhatTheTablesSay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedReleaseShelf(t, ctx, pool)
	seedRecordingOnTwoReleases(t, ctx, pool)

	statements := []string{
		`INSERT INTO library_files (id, path, size_bytes, modified_at)
		 VALUES ('11111111-1111-1111-1111-111111111111', '/music/late.flac', 4096, now())`,
		`INSERT INTO library_file_identities (library_file_id, kind, method, musicbrainz_recording_id)
		 SELECT '11111111-1111-1111-1111-111111111111', 'external', 'acoustid', musicbrainz_recording_id
		 FROM tracks WHERE title = 'Even Further Down the Drain'`,
		`UPDATE library_files SET missing_at = now()
		 WHERE id = '11111111-1111-1111-1111-111111111111'`,
		`UPDATE library_files SET missing_at = NULL
		 WHERE id = '11111111-1111-1111-1111-111111111111'`,
		`DELETE FROM library_file_identities
		 WHERE library_file_id = '11111111-1111-1111-1111-111111111111'`,
		`INSERT INTO acquisition_targets
			(id, origin, entry_artist, entry_title, musicbrainz_recording_id, status, not_wanted_at)
		 SELECT '22222222-2222-2222-2222-222222222222', 'manual', 'Cynthoni', title,
		        musicbrainz_recording_id, 'not_wanted', now()
		 FROM tracks WHERE title = 'Even Further Down the Drain'`,
		`UPDATE acquisition_targets SET status = 'pending', not_wanted_at = NULL
		 WHERE id = '22222222-2222-2222-2222-222222222222'`,
		`UPDATE acquisition_targets SET status = 'not_wanted', not_wanted_at = now()
		 WHERE id = '22222222-2222-2222-2222-222222222222'`,
		`DELETE FROM acquisition_targets WHERE id = '22222222-2222-2222-2222-222222222222'`,
		`UPDATE tracks SET musicbrainz_recording_id = NULL WHERE title = 'Flickering in the Gloom'`,
		`DELETE FROM library_files WHERE path = '/music/flickering.flac'`,
		`DELETE FROM albums WHERE title = 'Pt. 1'`,
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("run %q: %v", statement, err)
		}
		if disagreeing := countsThatDisagree(t, ctx, pool); len(disagreeing) > 0 {
			t.Fatalf("after %q the counts disagree with the tables for %v", statement, disagreeing)
		}
	}
}

// The counts are only true while every writer that can move them is one the
// triggers see. A backfill is how a database that drifted anyway is repaired,
// so it has to find the drift and say how much there was.
func TestTheBackfillRepairsCountsThatDrifted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedReleaseShelf(t, ctx, pool)

	if _, err := pool.Exec(ctx, `ALTER TABLE track_mappings DISABLE TRIGGER USER`); err != nil {
		t.Fatalf("silence the triggers: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO track_mappings (track_id, library_file_id, method) VALUES ($1, $2, 'exact')
	`, trackOnRelease(t, ctx, pool, "Dummy", 1),
		seedLibraryFile(t, ctx, pool, "/music/behind-the-triggers.flac")); err != nil {
		t.Fatalf("write mapping behind the triggers: %v", err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE track_mappings ENABLE TRIGGER USER`); err != nil {
		t.Fatalf("restore the triggers: %v", err)
	}

	if owned := countedRelease(t, ctx, pool, "Dummy").OwnedTrackCount; owned != 0 {
		t.Fatalf("owned = %d before the backfill, so nothing drifted and this proves nothing", owned)
	}
	if disagreeing := countsThatDisagree(t, ctx, pool); len(disagreeing) != 1 {
		t.Fatalf("drifted releases = %v, want the one written behind the triggers", disagreeing)
	}

	var corrected int64
	if err := pool.QueryRow(ctx, `SELECT album_counts_recompute_all()`).Scan(&corrected); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if corrected != 1 {
		t.Errorf("corrected = %d, want the one release that drifted", corrected)
	}
	if owned := countedRelease(t, ctx, pool, "Dummy").OwnedTrackCount; owned != 1 {
		t.Errorf("owned = %d after the backfill, want the mapping it missed", owned)
	}
	if disagreeing := countsThatDisagree(t, ctx, pool); len(disagreeing) > 0 {
		t.Errorf("still disagreeing after the backfill: %v", disagreeing)
	}
}

// Running it against a database nothing is wrong with has to be safe and has to
// say so, because that is how an operator finds out there was no drift.
func TestTheBackfillCorrectsNothingWhenNothingDrifted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedReleaseShelf(t, ctx, pool)

	var corrected int64
	if err := pool.QueryRow(ctx, `SELECT album_counts_recompute_all()`).Scan(&corrected); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if corrected != 0 {
		t.Errorf("corrected = %d, want nothing to correct", corrected)
	}
}

// seedFollowedArtist creates a followed artist with one album and one track,
// which is the smallest catalogue an unfollow has to make a decision about.
func seedFollowedArtist(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (artistID, albumID, trackID, recordingID uuid.UUID) {
	t.Helper()

	artistID, albumID, trackID, recordingID = uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		-- The level is stated rather than defaulted. New follows watch main
		-- releases since 00099, and these tests count what 'everything' counts.
		INSERT INTO artists (id, musicbrainz_id, name, sort_name, followed_at, monitor_level)
		VALUES ($1, $2, 'Boards of Canada', 'Boards of Canada', now(), 'everything')
	`, artistID, uuid.New()); err != nil {
		t.Fatalf("seed artist: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, musicbrainz_release_group_id, title, album_type)
		VALUES ($1, $2, $3, 'Music Has the Right to Children', 'album')
	`, albumID, artistID, uuid.New()); err != nil {
		t.Fatalf("seed album: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO tracks (id, album_id, musicbrainz_recording_id, title, disc_number, track_number)
		VALUES ($1, $2, $3, 'Roygbiv', 1, 8)
	`, trackID, albumID, recordingID); err != nil {
		t.Fatalf("seed track: %v", err)
	}
	return artistID, albumID, trackID, recordingID
}

func seedLibraryFile(t *testing.T, ctx context.Context, pool *pgxpool.Pool, path string) uuid.UUID {
	t.Helper()

	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, $2, 4096, now())
	`, fileID, path); err != nil {
		t.Fatalf("seed library file: %v", err)
	}
	return fileID
}

// Unfollowing withdraws the intent to keep an artist complete. It must not
// withdraw the fact that the library holds their music, so an artist with an
// owned file is demoted to held, keeps every release including the ones the
// library does not reach, and keeps the mapping the cascade would have taken.
func TestUnfollowingDemotesAnArtistWhoseMusicIsOwned(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID, _, trackID, _ := seedFollowedArtist(t, ctx, pool)
	fileID := seedLibraryFile(t, ctx, pool, "/music/boards/roygbiv.flac")
	if _, err := pool.Exec(ctx, `
		INSERT INTO track_mappings (track_id, library_file_id, method, is_manual)
		VALUES ($1, $2, 'manual', true)
	`, trackID, fileID); err != nil {
		t.Fatalf("seed mapping: %v", err)
	}

	// A release the library does not reach at all. Keeping it is the point:
	// pruning would discard edition choices and buy nothing, because missing
	// counts are already scoped to followed artists.
	unreachedID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, musicbrainz_release_group_id, title, album_type)
		VALUES ($1, $2, $3, 'Geogaddi', 'album')
	`, unreachedID, artistID, uuid.New()); err != nil {
		t.Fatalf("seed unreached album: %v", err)
	}

	// A queued full-discography refresh is no longer wanted once nobody is
	// tracking the artist for completeness.
	queries := New(pool)
	if _, err := queries.QueueArtistRefresh(ctx, artistID); err != nil {
		t.Fatalf("QueueArtistRefresh() error = %v", err)
	}

	result, err := queries.UnfollowArtist(ctx, artistID)
	if err != nil {
		t.Fatalf("UnfollowArtist() error = %v", err)
	}
	if !result.Held.Bool {
		t.Fatalf("held = %v, want an artist whose music is owned to be kept", result.Held)
	}
	if result.MappedTrackCount != 1 {
		t.Errorf("mapped_track_count = %d, want 1", result.MappedTrackCount)
	}
	// One owned track is "1 of their tracks", not "1 of their trackss".
	if want := "Unfollowed on"; !strings.HasPrefix(result.CatalogueSummary, want) {
		t.Errorf("catalogue_summary = %q, want it to start with %q", result.CatalogueSummary, want)
	}
	if !strings.Contains(result.CatalogueSummary, "1 of their tracks.") {
		t.Errorf("catalogue_summary = %q, want it to count the owned track", result.CatalogueSummary)
	}

	var followedAt *time.Time
	var storedSummary string
	if err := pool.QueryRow(ctx, `
		SELECT followed_at, coalesce(catalogue_summary, '') FROM artists WHERE id = $1
	`, artistID).Scan(&followedAt, &storedSummary); err != nil {
		t.Fatalf("artist was deleted, want it demoted: %v", err)
	}
	if followedAt != nil || storedSummary != result.CatalogueSummary {
		t.Errorf("followed_at = %v, summary = %q; want a held artist that explains itself", followedAt, storedSummary)
	}

	var albums, mappings int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM albums WHERE artist_id = $1),
			(SELECT count(*) FROM track_mappings WHERE library_file_id = $2)
	`, artistID, fileID).Scan(&albums, &mappings); err != nil {
		t.Fatal(err)
	}
	if albums != 2 {
		t.Errorf("albums = %d, want both releases kept including the unreached one", albums)
	}
	if mappings != 1 {
		t.Errorf("mappings = %d, want the manual mapping to survive unfollowing", mappings)
	}

	var cancelled string
	if err := pool.QueryRow(ctx, `
		SELECT status FROM jobs
		WHERE kind = 'refresh_artist_metadata' AND payload->>'artistId' = $1::text
	`, artistID).Scan(&cancelled); err != nil {
		t.Fatal(err)
	}
	if cancelled != "cancelled" {
		t.Errorf("refresh job = %q, want it cancelled once nobody tracks the artist", cancelled)
	}

	// A held artist has no intent left to withdraw.
	if _, err := queries.UnfollowArtist(ctx, artistID); err == nil {
		t.Error("UnfollowArtist() on a held artist returned a row, want no rows")
	}
}

// An artist the library reaches in no way is released outright. That delete is
// only safe because there was nothing for the cascade to take, which is exactly
// what the reach checks established.
func TestUnfollowingReleasesAnArtistTheLibraryDoesNotReach(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID, _, _, _ := seedFollowedArtist(t, ctx, pool)

	queries := New(pool)
	result, err := queries.UnfollowArtist(ctx, artistID)
	if err != nil {
		t.Fatalf("UnfollowArtist() error = %v", err)
	}
	if result.Held.Bool {
		t.Fatalf("held = %v, want an unreached artist released", result.Held)
	}
	if result.CatalogueSummary != "" {
		t.Errorf("catalogue_summary = %q, want none for an artist who is gone", result.CatalogueSummary)
	}

	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM artists WHERE id = $1`, artistID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Errorf("artists = %d, want the unreached artist removed", remaining)
	}
}

// A file proven to be the artist's music but not yet mapped is still reach: the
// catalogue is what is late, not the evidence. Releasing the artist here would
// throw away a release the very next matching run is about to use.
func TestUnfollowingKeepsAnArtistWithAnIdentifiedFile(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID, _, _, recordingID := seedFollowedArtist(t, ctx, pool)
	fileID := seedLibraryFile(t, ctx, pool, "/music/boards/unmatched.flac")
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (library_file_id, kind, musicbrainz_recording_id, method)
		VALUES ($1, 'external', $2, 'recording-id')
	`, fileID, recordingID); err != nil {
		t.Fatalf("seed identity: %v", err)
	}

	result, err := New(pool).UnfollowArtist(ctx, artistID)
	if err != nil {
		t.Fatalf("UnfollowArtist() error = %v", err)
	}
	if !result.Held.Bool {
		t.Fatalf("held = %v, want an artist with an identified file kept", result.Held)
	}
	if !strings.Contains(result.CatalogueSummary, "identified as their music") {
		t.Errorf("catalogue_summary = %q, want it to name the resolved file", result.CatalogueSummary)
	}
}

// download_requests cascades from albums, so releasing an artist mid-acquisition
// would take the request, its transfers, and its import history with it. Asking
// for the music is a statement that it is wanted.
func TestUnfollowingKeepsAnArtistWithAnOpenDownload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID, albumID, _, _ := seedFollowedArtist(t, ctx, pool)
	if _, err := pool.Exec(ctx, `
		INSERT INTO download_requests (
			album_id, provider, source_username, source_directory,
			file_count, expected_track_count, total_size_bytes, format, score
		)
		VALUES ($1, 'slskd', 'peer', '@@music\Boards', 12, 12, 500000000, 'flac', 0.9)
	`, albumID); err != nil {
		t.Fatalf("seed download request: %v", err)
	}

	result, err := New(pool).UnfollowArtist(ctx, artistID)
	if err != nil {
		t.Fatalf("UnfollowArtist() error = %v", err)
	}
	if !result.Held.Bool {
		t.Fatalf("held = %v, want an artist with music still arriving kept", result.Held)
	}
	if !strings.Contains(result.CatalogueSummary, "still in progress") {
		t.Errorf("catalogue_summary = %q, want it to name the download", result.CatalogueSummary)
	}

	var requests int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM download_requests WHERE album_id = $1
	`, albumID).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Errorf("download requests = %d, want the open request to survive unfollowing", requests)
	}
}

// A cancelled request is not music that is still wanted, so it is not reach.
func TestUnfollowingIgnoresACancelledDownload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID, albumID, _, _ := seedFollowedArtist(t, ctx, pool)
	if _, err := pool.Exec(ctx, `
		INSERT INTO download_requests (
			album_id, provider, source_username, source_directory,
			file_count, expected_track_count, total_size_bytes, format, score,
			status, cancelled_at
		)
		VALUES ($1, 'slskd', 'peer', '@@music\Boards', 12, 12, 500000000, 'flac', 0.9,
			'cancelled', now())
	`, albumID); err != nil {
		t.Fatalf("seed cancelled request: %v", err)
	}

	result, err := New(pool).UnfollowArtist(ctx, artistID)
	if err != nil {
		t.Fatalf("UnfollowArtist() error = %v", err)
	}
	if result.Held.Bool {
		t.Errorf("held = %v, want a cancelled request not to count as reach", result.Held)
	}
}

// Wanting a discography walks every release of the artist and reports what the
// library already holds. What no fake can answer is which mappings count, so
// this is the query's own test: a file the scanner has since stopped finding is
// not ownership, and the track it was mapped to is still missing music.
func TestArtistTracksForWantingReadsEveryReleaseAndWhatIsOwned(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name)
		VALUES ($1, $2, 'Portishead', 'Portishead')
	`, artistID, uuid.New()); err != nil {
		t.Fatalf("seed artist: %v", err)
	}
	seedWantableRelease(t, ctx, pool, artistID, "Dummy", "1994-08-22", []string{
		"Mysterons", "Sour Times", "Strangers",
	})
	seedWantableRelease(t, ctx, pool, artistID, "Portishead", "1997-09-29", []string{"Cowboys"})

	// One track is held, one is mapped to a file the scanner no longer finds.
	if _, err := pool.Exec(ctx, `
		INSERT INTO track_mappings (track_id, library_file_id, method)
		SELECT tracks.id, $1, 'exact' FROM tracks WHERE tracks.title = 'Sour Times'
	`, seedLibraryFile(t, ctx, pool, "/music/sour-times.flac")); err != nil {
		t.Fatalf("seed owned mapping: %v", err)
	}
	goneID := seedLibraryFile(t, ctx, pool, "/music/strangers.flac")
	if _, err := pool.Exec(ctx,
		`UPDATE library_files SET missing_at = now() WHERE id = $1`, goneID); err != nil {
		t.Fatalf("mark file missing: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO track_mappings (track_id, library_file_id, method)
		SELECT tracks.id, $1, 'exact' FROM tracks WHERE tracks.title = 'Strangers'
	`, goneID); err != nil {
		t.Fatalf("seed missing mapping: %v", err)
	}

	rows, err := New(pool).ArtistTracksForWanting(ctx, artistID)
	if err != nil {
		t.Fatalf("ArtistTracksForWanting() error = %v", err)
	}
	owned := make(map[string]bool, len(rows))
	albums := make(map[string]string, len(rows))
	for _, row := range rows {
		owned[row.Title] = row.Owned
		albums[row.Title] = row.AlbumTitle
	}
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want every track of both releases", len(rows))
	}
	if !owned["Sour Times"] {
		t.Error("a track mapped to a file the library holds is not owned")
	}
	if owned["Strangers"] {
		t.Error("a track mapped to a file that has gone missing counts as owned")
	}
	if albums["Cowboys"] != "Portishead" {
		t.Errorf("Cowboys album = %q, want the release it sits on", albums["Cowboys"])
	}
}

// The other way the library already holds a track: a file proven to be that
// recording, never mapped onto this release at all. The acquisition loop settles
// a want against exactly that (OwnedFileForRecording), so a walk that cannot see
// it makes wants the loop answers with "In your library." a minute later.
func TestArtistTracksForWantingSeesAFileProvenToBeTheRecording(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name)
		VALUES ($1, $2, 'Portishead', 'Portishead')
	`, artistID, uuid.New()); err != nil {
		t.Fatalf("seed artist: %v", err)
	}
	seedWantableRelease(t, ctx, pool, artistID, "Dummy", "1994-08-22",
		[]string{"Mysterons", "Sour Times"})

	var recordingID uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT musicbrainz_recording_id FROM tracks WHERE title = 'Mysterons'`,
	).Scan(&recordingID); err != nil {
		t.Fatalf("read the recording: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, method, confidence, summary
		)
		VALUES ($1, 'external', $2, 'fingerprint', 1, 'Identified by its audio.')
	`, seedLibraryFile(t, ctx, pool, "/music/mysterons.flac"), recordingID); err != nil {
		t.Fatalf("seed identity: %v", err)
	}

	rows, err := New(pool).ArtistTracksForWanting(ctx, artistID)
	if err != nil {
		t.Fatalf("ArtistTracksForWanting() error = %v", err)
	}
	identified := make(map[string]bool, len(rows))
	for _, row := range rows {
		identified[row.Title] = row.HeldElsewhere
	}
	if !identified["Mysterons"] {
		t.Error("a file proven to be the recording does not count as holding it")
	}
	if identified["Sour Times"] {
		t.Error("a track nothing was proven for counts as held")
	}
}

func seedWantableRelease(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	artistID uuid.UUID, title, releaseDate string, tracks []string,
) {
	t.Helper()

	albumID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, musicbrainz_release_group_id, title, release_date, album_type)
		VALUES ($1, $2, $3, $4, $5::date, 'album')
	`, albumID, artistID, uuid.New(), title, releaseDate); err != nil {
		t.Fatalf("seed album %s: %v", title, err)
	}
	for position, name := range tracks {
		if _, err := pool.Exec(ctx, `
			INSERT INTO tracks (id, album_id, musicbrainz_recording_id, title, disc_number, track_number)
			VALUES ($1, $2, $3, $4, 1, $5)
		`, uuid.New(), albumID, uuid.New(), name, position+1); err != nil {
			t.Fatalf("seed track %s: %v", name, err)
		}
	}
}

// seedArtistWithCatalogue gives one artist a release of trackCount tracks, of
// which ownedCount are mapped onto files the library still has. It is the shape
// every completeness assertion below needs: a discography to compare against and
// a library that reaches part of it.
func seedArtistWithCatalogue(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	name string, trackCount, ownedCount int,
) (artistID, albumID uuid.UUID) {
	t.Helper()

	artistID, albumID = uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name, followed_at)
		VALUES ($1, $2, $3, $3, now())
	`, artistID, uuid.New(), name); err != nil {
		t.Fatalf("seed artist %s: %v", name, err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, musicbrainz_release_group_id, title, album_type)
		VALUES ($1, $2, $3, $4, 'album')
	`, albumID, artistID, uuid.New(), name+" LP"); err != nil {
		t.Fatalf("seed album for %s: %v", name, err)
	}
	for position := range trackCount {
		trackID := uuid.New()
		if _, err := pool.Exec(ctx, `
			INSERT INTO tracks (id, album_id, musicbrainz_recording_id, title, disc_number, track_number)
			VALUES ($1, $2, $3, $4, 1, $5)
		`, trackID, albumID, uuid.New(), fmt.Sprintf("%s track %d", name, position+1), position+1); err != nil {
			t.Fatalf("seed track for %s: %v", name, err)
		}
		if position >= ownedCount {
			continue
		}
		fileID := seedLibraryFile(t, ctx, pool, fmt.Sprintf("/music/%s/%d.flac", name, position+1))
		if _, err := pool.Exec(ctx, `
			INSERT INTO track_mappings (track_id, library_file_id, method, is_manual)
			VALUES ($1, $2, 'manual', true)
		`, trackID, fileID); err != nil {
			t.Fatalf("seed mapping for %s: %v", name, err)
		}
	}
	return artistID, albumID
}

func listedArtist(t *testing.T, rows []ListArtistsRow, name string) ListArtistsRow {
	t.Helper()
	for _, row := range rows {
		if row.Name == name {
			return row
		}
	}
	t.Fatalf("artist %q is not in %v", name, listedArtistNames(rows))
	return ListArtistsRow{}
}

// A release the library reaches most of is not a release the library has. The
// card that said otherwise would report a discography as complete while a track
// of it was still missing, which is the one thing the number is asked for.
func TestListArtistsCountsAReleaseOwnedOnlyWhenEveryTrackIs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedArtistWithCatalogue(t, ctx, pool, "Objekt", 4, 3)
	queries := New(pool)

	page, err := queries.ListArtists(ctx, ListArtistsParams{Limit: 50})
	if err != nil {
		t.Fatalf("ListArtists() error = %v", err)
	}

	artist := listedArtist(t, page, "Objekt")
	if artist.ReleaseCount != 1 || artist.OwnedReleaseCount != 0 {
		t.Errorf("release counts = %d owned of %d, want 0 of 1",
			artist.OwnedReleaseCount, artist.ReleaseCount)
	}
	if artist.OwnedTrackCount != 3 || artist.TrackCount != 4 {
		t.Errorf("track counts = %d owned of %d, want 3 of 4",
			artist.OwnedTrackCount, artist.TrackCount)
	}
}

// A file that went missing leaves its mapping behind. Counting it would call a
// release complete when nothing can play it.
func TestListArtistsDoesNotCountATrackWhoseFileWentMissing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedArtistWithCatalogue(t, ctx, pool, "Yaeji", 2, 2)
	if _, err := pool.Exec(ctx, `
		UPDATE library_files SET missing_at = now() WHERE path = '/music/Yaeji/2.flac'
	`); err != nil {
		t.Fatalf("mark file missing: %v", err)
	}
	queries := New(pool)

	page, err := queries.ListArtists(ctx, ListArtistsParams{Limit: 50})
	if err != nil {
		t.Fatalf("ListArtists() error = %v", err)
	}

	artist := listedArtist(t, page, "Yaeji")
	if artist.OwnedReleaseCount != 0 {
		t.Errorf("owned releases = %d, want none while a track's file is missing",
			artist.OwnedReleaseCount)
	}
	if artist.OwnedTrackCount != 1 {
		t.Errorf("owned tracks = %d, want 1 of 2", artist.OwnedTrackCount)
	}
}

// Sorting by how little is owned has to happen over the whole list, or the page
// would only be ordering whichever artists it happened to hold.
func TestListArtistsOrdersTheLeastCompleteFirst(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedArtistWithCatalogue(t, ctx, pool, "Caribou", 4, 4)
	seedArtistWithCatalogue(t, ctx, pool, "Burial", 4, 1)
	seedArtistWithCatalogue(t, ctx, pool, "Autechre", 4, 2)
	queries := New(pool)

	page, err := queries.ListArtists(ctx, ListArtistsParams{Sort: "least-complete", Limit: 50})
	if err != nil {
		t.Fatalf("ListArtists() error = %v", err)
	}

	want := []string{"Burial", "Autechre", "Caribou"}
	if got := listedArtistNames(page); !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// The attention view exists to be pointed at. A want whose copy nothing could
// identify is held for a person to listen to, and the artist it belongs to is
// the one place somebody would look for it.
func TestListArtistsNarrowsToArtistsWhoseCopyIsWaitingForAnEar(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedArtistWithCatalogue(t, ctx, pool, "Burial", 2, 2)
	_, albumID := seedArtistWithCatalogue(t, ctx, pool, "Objekt", 2, 2)

	targetID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets
			(id, origin, origin_album_id, entry_artist, entry_title,
			 musicbrainz_recording_id, resolution_method, resolved_at, status)
		VALUES ($1, 'playlist', $2, 'Objekt', 'Ganzfeld', $3, 'isrc', now(), 'pending')
	`, targetID, albumID, uuid.New()); err != nil {
		t.Fatalf("seed acquisition target: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_target_files
			(acquisition_target_id, provider, source_username, remote_path, verdict)
		VALUES ($1, 'slskd', 'peer', 'music\Ganzfeld.flac', 'held')
	`, targetID); err != nil {
		t.Fatalf("seed held copy: %v", err)
	}
	queries := New(pool)

	page, err := queries.ListArtists(ctx, ListArtistsParams{Completeness: "attention", Limit: 50})
	if err != nil {
		t.Fatalf("ListArtists() error = %v", err)
	}

	if got := listedArtistNames(page); !slices.Equal(got, []string{"Objekt"}) {
		t.Errorf("attention view = %v, want only Objekt", got)
	}
	if count := listedArtist(t, page, "Objekt").ReviewCount; count != 1 {
		t.Errorf("review count = %d, want 1", count)
	}
}

// An artist nothing has been fetched for cannot be called complete or
// incomplete: the catalogue has nothing to compare the library against. Letting
// them fall into "complete" would report an empty answer as a finished one.
func TestListArtistsLeavesAnArtistWithNoDiscographyOutOfBothCompletenessViews(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedArtistWithCatalogue(t, ctx, pool, "Objekt", 2, 2)
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name, followed_at)
		VALUES ($1, $2, 'Lorenzo Senni', 'Senni, Lorenzo', now())
	`, uuid.New(), uuid.New()); err != nil {
		t.Fatalf("seed unscanned artist: %v", err)
	}
	queries := New(pool)

	for _, view := range []string{"complete", "incomplete"} {
		page, err := queries.ListArtists(ctx, ListArtistsParams{Completeness: view, Limit: 50})
		if err != nil {
			t.Fatalf("ListArtists(%s) error = %v", view, err)
		}
		if slices.Contains(listedArtistNames(page), "Lorenzo Senni") {
			t.Errorf("%s view = %v, want an artist with no discography left out",
				view, listedArtistNames(page))
		}
	}

	page, err := queries.ListArtists(ctx, ListArtistsParams{Completeness: "attention", Limit: 50})
	if err != nil {
		t.Fatalf("ListArtists(attention) error = %v", err)
	}
	if !slices.Contains(listedArtistNames(page), "Lorenzo Senni") {
		t.Errorf("attention view = %v, want the unscanned artist in it", listedArtistNames(page))
	}
}

// The tab counts label the choices the user is picking between, so a count that
// moved when a choice was made would be describing the answer rather than the
// question.
func TestArtistListTotalsCountEveryCompletenessViewAtOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedArtistWithCatalogue(t, ctx, pool, "Caribou", 4, 4)
	seedArtistWithCatalogue(t, ctx, pool, "Burial", 4, 1)
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name, followed_at)
		VALUES ($1, $2, 'Lorenzo Senni', 'Senni, Lorenzo', now())
	`, uuid.New(), uuid.New()); err != nil {
		t.Fatalf("seed unscanned artist: %v", err)
	}
	queries := New(pool)

	totals, err := queries.ArtistListTotals(ctx, ArtistListTotalsParams{})
	if err != nil {
		t.Fatalf("ArtistListTotals() error = %v", err)
	}

	if totals.AllCount != 3 || totals.CompleteCount != 1 ||
		totals.IncompleteCount != 1 || totals.AttentionCount != 1 {
		t.Errorf("counts = %d all, %d complete, %d incomplete, %d attention; want 3/1/1/1",
			totals.AllCount, totals.CompleteCount, totals.IncompleteCount, totals.AttentionCount)
	}
	if totals.ReleaseTotal != 2 || totals.OwnedReleaseTotal != 1 {
		t.Errorf("release totals = %d owned of %d, want 1 of 2",
			totals.OwnedReleaseTotal, totals.ReleaseTotal)
	}
}

// seedDismissal records the user's decision not to want a recording, in the
// shape StopPursuing leaves behind.
func seedDismissal(t *testing.T, ctx context.Context, pool *pgxpool.Pool, recordingID uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO acquisition_targets (
			origin, entry_title, musicbrainz_recording_id,
			resolution_method, resolved_at, status, summary, not_wanted_at
		)
		VALUES ('manual', 'dismissed', $1, 'manual', now(), 'not_wanted',
		        'This recording is not wanted.', now())
	`, recordingID); err != nil {
		t.Fatalf("seed dismissal: %v", err)
	}
}

// A dismissed track leaves the completion denominator instead of counting as
// missing, and a release whose every track is dismissed leaves the release
// counts entirely — a deliberate decision is not a gap.
func TestArtistCompletenessExcludesDismissedTracks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID, albumID, trackID, _ := seedFollowedArtist(t, ctx, pool)
	fileID := seedLibraryFile(t, ctx, pool, "/music/boards/roygbiv.flac")
	if _, err := pool.Exec(ctx, `
		INSERT INTO track_mappings (track_id, library_file_id, method, is_manual)
		VALUES ($1, $2, 'manual', true)
	`, trackID, fileID); err != nil {
		t.Fatalf("seed mapping: %v", err)
	}
	// A second track on the same release, unowned and dismissed.
	dismissedRecording := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO tracks (id, album_id, musicbrainz_recording_id, title, disc_number, track_number)
		VALUES ($1, $2, $3, 'Aquarius', 1, 9)
	`, uuid.New(), albumID, dismissedRecording); err != nil {
		t.Fatalf("seed dismissed track: %v", err)
	}
	seedDismissal(t, ctx, pool, dismissedRecording)
	// A release whose every track is dismissed.
	dumpID, dumpRecording := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, musicbrainz_release_group_id, title, album_type)
		VALUES ($1, $2, $3, 'Old Tunes', 'album')
	`, dumpID, artistID, uuid.New()); err != nil {
		t.Fatalf("seed dismissed album: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO tracks (id, album_id, musicbrainz_recording_id, title, disc_number, track_number)
		VALUES ($1, $2, $3, 'Nova Scotia Robots', 1, 1)
	`, uuid.New(), dumpID, dumpRecording); err != nil {
		t.Fatalf("seed dismissed album track: %v", err)
	}
	seedDismissal(t, ctx, pool, dumpRecording)

	queries := New(pool)
	artists, err := queries.ListArtists(ctx, ListArtistsParams{Limit: 10})
	if err != nil {
		t.Fatalf("ListArtists() error = %v", err)
	}
	index := slices.IndexFunc(artists, func(row ListArtistsRow) bool { return row.ID == artistID })
	if index < 0 {
		t.Fatalf("artist %s not listed", artistID)
	}
	row := artists[index]
	// One release counts — the fully dismissed one is out — and it is complete:
	// one owned track over a denominator the dismissal left.
	if row.ReleaseCount != 1 || row.OwnedReleaseCount != 1 {
		t.Errorf("releases = %d owned %d, want 1 owned of 1 counted", row.ReleaseCount, row.OwnedReleaseCount)
	}
	if row.TrackCount != 1 || row.OwnedTrackCount != 1 {
		t.Errorf("tracks = %d owned %d, want 1 owned of 1 counted", row.TrackCount, row.OwnedTrackCount)
	}

	albums, err := queries.ListAlbums(ctx, ListAlbumsParams{
		ArtistID: uuid.NullUUID{UUID: artistID, Valid: true},
	})
	if err != nil {
		t.Fatalf("ListAlbums() error = %v", err)
	}
	for _, album := range albums.Items {
		switch album.ID {
		case albumID:
			if album.TrackCount != 2 || album.OwnedTrackCount != 1 || album.DismissedTrackCount != 1 {
				t.Errorf("album counts = %d/%d/%d, want 2 tracks, 1 owned, 1 dismissed",
					album.TrackCount, album.OwnedTrackCount, album.DismissedTrackCount)
			}
		case dumpID:
			if album.DismissedTrackCount != 1 || album.OwnedTrackCount != 0 {
				t.Errorf("dismissed album counts = %+v", album)
			}
		}
	}

	// The release with an owned track and a dismissed gap reads owned; the
	// fully dismissed one is neither owned nor missing — it was decided about,
	// not lost.
	owned, err := queries.ListAlbums(ctx, ListAlbumsParams{
		ArtistID: uuid.NullUUID{UUID: artistID, Valid: true}, Status: "owned",
	})
	if err != nil {
		t.Fatalf("ListAlbums(owned) error = %v", err)
	}
	if len(owned.Items) != 1 || owned.Items[0].ID != albumID {
		t.Errorf("owned = %+v, want exactly the release whose gap was dismissed", owned.Items)
	}
	missing, err := queries.ListAlbums(ctx, ListAlbumsParams{
		ArtistID: uuid.NullUUID{UUID: artistID, Valid: true}, Status: "missing",
	})
	if err != nil {
		t.Fatalf("ListAlbums(missing) error = %v", err)
	}
	if len(missing.Items) != 0 {
		t.Errorf("missing = %+v, want none", missing.Items)
	}
}

// The tracks of a release carry the want that governs them, so a page can
// offer the toggle that fits each row.
func TestListAlbumTracksCarriesTheGoverningWant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	_, albumID, _, recordingID := seedFollowedArtist(t, ctx, pool)
	seedDismissal(t, ctx, pool, recordingID)

	tracks, err := New(pool).ListAlbumTracks(ctx, albumID)
	if err != nil {
		t.Fatalf("ListAlbumTracks() error = %v", err)
	}
	if len(tracks) != 1 {
		t.Fatalf("tracks = %d, want 1", len(tracks))
	}
	if !tracks[0].WantID.Valid || tracks[0].WantStatus.String != "not_wanted" {
		t.Errorf("want = %v %q, want the dismissal carried on the track",
			tracks[0].WantID, tracks[0].WantStatus.String)
	}
}

// The monitor level decides what counts as missing and what a discography-wide
// want reaches — never what is stored or shown. A live dump stops being
// "missing" under 'main', and 'owned' counts only what the library maps.
func TestMonitorLevelScopesCompletenessAndWanting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID, albumID, trackID, _ := seedFollowedArtist(t, ctx, pool)
	fileID := seedLibraryFile(t, ctx, pool, "/music/boards/roygbiv.flac")
	if _, err := pool.Exec(ctx, `
		INSERT INTO track_mappings (track_id, library_file_id, method, is_manual)
		VALUES ($1, $2, 'manual', true)
	`, trackID, fileID); err != nil {
		t.Fatalf("seed mapping: %v", err)
	}
	// A live dump MusicBrainz types as an album with the 'live' secondary type,
	// with a track the library does not hold.
	liveID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO albums (id, artist_id, musicbrainz_release_group_id, title, album_type, secondary_types)
		VALUES ($1, $2, $3, 'Live at ATP', 'album', '{live}')
	`, liveID, artistID, uuid.New()); err != nil {
		t.Fatalf("seed live album: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO tracks (id, album_id, musicbrainz_recording_id, title, disc_number, track_number)
		VALUES ($1, $2, $3, 'Roygbiv (live)', 1, 1)
	`, uuid.New(), liveID, uuid.New()); err != nil {
		t.Fatalf("seed live track: %v", err)
	}

	queries := New(pool)
	listed := func() ListArtistsRow {
		t.Helper()
		artists, err := queries.ListArtists(ctx, ListArtistsParams{Limit: 10})
		if err != nil {
			t.Fatalf("ListArtists() error = %v", err)
		}
		index := slices.IndexFunc(artists, func(row ListArtistsRow) bool { return row.ID == artistID })
		if index < 0 {
			t.Fatalf("artist %s not listed", artistID)
		}
		return artists[index]
	}

	// Everything: both releases count and the live dump is missing.
	row := listed()
	if row.ReleaseCount != 2 || row.OwnedReleaseCount != 1 || row.TrackCount != 2 {
		t.Errorf("everything: releases %d/%d tracks %d, want 1 of 2 owned over 2 tracks",
			row.OwnedReleaseCount, row.ReleaseCount, row.TrackCount)
	}

	// Main releases: the live dump leaves the counts and the walk.
	if _, err := queries.SetArtistMonitorLevel(ctx, SetArtistMonitorLevelParams{
		ArtistID: artistID, MonitorLevel: "main",
	}); err != nil {
		t.Fatalf("SetArtistMonitorLevel() error = %v", err)
	}
	row = listed()
	if row.ReleaseCount != 1 || row.OwnedReleaseCount != 1 || row.TrackCount != 1 || row.OwnedTrackCount != 1 {
		t.Errorf("main: releases %d/%d tracks %d/%d, want a complete artist of one release",
			row.OwnedReleaseCount, row.ReleaseCount, row.OwnedTrackCount, row.TrackCount)
	}
	wantable, err := queries.ArtistTracksForWanting(ctx, artistID)
	if err != nil {
		t.Fatalf("ArtistTracksForWanting() error = %v", err)
	}
	for _, track := range wantable {
		if track.AlbumID == liveID {
			t.Errorf("wanting reached the live dump: %+v", track)
		}
	}

	// Owned only: completeness is over what the library maps, and the
	// discography-wide walk reaches nothing at all.
	if _, err := queries.SetArtistMonitorLevel(ctx, SetArtistMonitorLevelParams{
		ArtistID: artistID, MonitorLevel: "owned",
	}); err != nil {
		t.Fatalf("SetArtistMonitorLevel() error = %v", err)
	}
	row = listed()
	if row.ReleaseCount != 1 || row.OwnedReleaseCount != 1 || row.TrackCount != 1 || row.OwnedTrackCount != 1 {
		t.Errorf("owned: releases %d/%d tracks %d/%d, want only the mapped release counted",
			row.OwnedReleaseCount, row.ReleaseCount, row.OwnedTrackCount, row.TrackCount)
	}
	wantable, err = queries.ArtistTracksForWanting(ctx, artistID)
	if err != nil {
		t.Fatalf("ArtistTracksForWanting() error = %v", err)
	}
	if len(wantable) != 0 {
		t.Errorf("owned: wanting reached %d tracks, want none", len(wantable))
	}

	// The releases list keeps showing the live dump; it is merely unmonitored.
	albums, err := queries.ListAlbums(ctx, ListAlbumsParams{
		ArtistID: uuid.NullUUID{UUID: artistID, Valid: true},
	})
	if err != nil {
		t.Fatalf("ListAlbums() error = %v", err)
	}
	if len(albums.Items) != 2 {
		t.Fatalf("albums = %d, want the full discography shown", len(albums.Items))
	}
	for _, album := range albums.Items {
		if album.ID == liveID && album.Monitored {
			t.Errorf("live dump reads monitored at level owned")
		}
		if album.ID == albumID && !album.Monitored {
			t.Errorf("owned release should stay monitored at level owned")
		}
	}
}

// The same recording on two releases, held by one file. MusicBrainz gives that
// recording a track row on the EP and another on the compilation that collects
// it, a file maps onto exactly one of the two, and the other would read as a
// gap forever — which is the reason the counts below ask what the library
// holds rather than what this release mapped.
func seedRecordingOnTwoReleases(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool,
) (artistID, compilationID uuid.UUID) {
	t.Helper()

	artistID, compilationID = uuid.New(), uuid.New()
	epID := uuid.New()
	shared, only := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name, followed_at)
		VALUES ($1, $2, 'Cynthoni', 'Cynthoni', now())
	`, artistID, uuid.New()); err != nil {
		t.Fatalf("seed artist: %v", err)
	}
	for _, release := range []struct {
		id     uuid.UUID
		title  string
		kind   string
		date   string
		tracks []uuid.UUID
		titles []string
	}{
		{epID, "Pt. 1", "ep", "2021-02-01",
			[]uuid.UUID{shared}, []string{"Flickering in the Gloom"}},
		{compilationID, "Pt. 1 & 2", "album", "2021-09-01",
			[]uuid.UUID{shared, only},
			[]string{"Flickering in the Gloom", "Even Further Down the Drain"}},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO albums (id, artist_id, musicbrainz_release_group_id, title, release_date, album_type)
			VALUES ($1, $2, $3, $4, $5::date, $6)
		`, release.id, artistID, uuid.New(), release.title, release.date, release.kind); err != nil {
			t.Fatalf("seed album %s: %v", release.title, err)
		}
		for position, recordingID := range release.tracks {
			if _, err := pool.Exec(ctx, `
				INSERT INTO tracks (id, album_id, musicbrainz_recording_id, title, disc_number, track_number)
				VALUES ($1, $2, $3, $4, 1, $5)
			`, uuid.New(), release.id, recordingID,
				release.titles[position], position+1); err != nil {
				t.Fatalf("seed track %s: %v", release.titles[position], err)
			}
		}
	}

	// The one file, filed under the EP the way the importer found it.
	var epTrackID uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT id FROM tracks WHERE album_id = $1`, epID,
	).Scan(&epTrackID); err != nil {
		t.Fatalf("read the EP track: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO track_mappings (track_id, library_file_id, method)
		VALUES ($1, $2, 'verified_download')
	`, epTrackID, seedLibraryFile(t, ctx, pool, "/music/flickering.flac")); err != nil {
		t.Fatalf("seed mapping: %v", err)
	}
	return artistID, compilationID
}

// A release counts what the library can play, not what it mapped here. The
// compilation never mapped the track it shares with the EP, and the file
// proving that recording is one the acquisition loop settles a want against
// (OwnedFileForRecording), so calling it missing would ask strangers for music
// that is on the disk.
func TestAReleaseCountsATrackWhoseFileIsFiledUnderAnotherRelease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	_, compilationID := seedRecordingOnTwoReleases(t, ctx, pool)

	page := listReleases(t, ctx, pool, ListAlbumsParams{Query: "Pt. 1 & 2", Limit: 10})

	if len(page.Items) != 1 {
		t.Fatalf("titles = %v, want the compilation", listedReleaseTitles(page))
	}
	if page.Items[0].ID != compilationID {
		t.Fatalf("release = %s, want the compilation", page.Items[0].ID)
	}
	if page.Items[0].OwnedTrackCount != 1 || page.Items[0].TrackCount != 2 {
		t.Errorf("owned = %d of %d, want 1 of 2",
			page.Items[0].OwnedTrackCount, page.Items[0].TrackCount)
	}
}

// The releases browser sorts by the same arithmetic it displays, so a release
// holding one shared track and one it is genuinely short of is partial.
func TestAReleaseHoldingOnlyTheSharedTrackIsPartial(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedRecordingOnTwoReleases(t, ctx, pool)

	partial := listReleases(t, ctx, pool,
		ListAlbumsParams{Status: "partial", Sort: "year", Limit: 10})

	if got := strings.Join(listedReleaseTitles(partial), "|"); got != "Pt. 1 & 2" {
		t.Errorf("partial = %v, want the compilation", got)
	}
}

// The discography percentage is the same question asked over every release, and
// a track owned on the EP is owned when the compilation lists it too.
func TestArtistCompletenessCountsATrackFiledUnderAnotherRelease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedRecordingOnTwoReleases(t, ctx, pool)

	rows, err := New(pool).ListArtists(ctx, ListArtistsParams{Limit: 10})
	if err != nil {
		t.Fatalf("ListArtists() error = %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("artists = %d, want the one seeded", len(rows))
	}
	if rows[0].OwnedTrackCount != 2 || rows[0].TrackCount != 3 {
		t.Errorf("owned = %d of %d tracks, want 2 of 3",
			rows[0].OwnedTrackCount, rows[0].TrackCount)
	}
	if rows[0].OwnedReleaseCount != 1 || rows[0].ReleaseCount != 2 {
		t.Errorf("owned = %d of %d releases, want the EP alone",
			rows[0].OwnedReleaseCount, rows[0].ReleaseCount)
	}
}

// The release page asks per track, and it has to be able to tell the tick it
// draws from the gap it offers to fill.
func TestListAlbumTracksMarksTheTrackHeldUnderAnotherRelease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	_, compilationID := seedRecordingOnTwoReleases(t, ctx, pool)

	rows, err := New(pool).ListAlbumTracks(ctx, compilationID)
	if err != nil {
		t.Fatalf("ListAlbumTracks() error = %v", err)
	}
	held := make(map[string]bool, len(rows))
	owned := make(map[string]bool, len(rows))
	for _, row := range rows {
		held[row.Title] = row.HeldElsewhere
		owned[row.Title] = row.Owned
	}
	if !held["Flickering in the Gloom"] {
		t.Error("the track whose file is filed under the EP does not read as held")
	}
	if owned["Flickering in the Gloom"] {
		t.Error("a track this release never mapped reads as mapped onto it")
	}
	if held["Even Further Down the Drain"] {
		t.Error("a track nothing was proven for reads as held")
	}
}

// The dashboard's wishlist count is the same question as the Wishlist tab, and
// the number the user reads first. Only wants still being looked for belong in
// it, whether or not a catalogue track is missing.
func TestTheDashboardCountsOpenWants(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedRecordingOnTwoReleases(t, ctx, pool)
	queries := New(pool)
	openWant, _, err := queries.CreateAcquisitionTarget(ctx, wantParams("Open want", uuid.New()))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() open want error = %v", err)
	}
	acquiredWant, _, err := queries.CreateAcquisitionTarget(ctx, wantParams("Acquired want", uuid.New()))
	if err != nil {
		t.Fatalf("CreateAcquisitionTarget() acquired want error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET status = 'acquired', acquired_at = now(), next_attempt_at = NULL WHERE id = $1
	`, acquiredWant); err != nil {
		t.Fatalf("mark want acquired: %v", err)
	}

	summary, err := queries.DashboardSummary(ctx)
	if err != nil {
		t.Fatalf("DashboardSummary() error = %v", err)
	}
	if summary.OpenWantCount != 1 {
		t.Errorf("open wants = %d, want 1 (open want %s)",
			summary.OpenWantCount, openWant)
	}
}

// The other proof of the same fact: a file nobody mapped anywhere, whose
// resolved identity names the recording. It is what the acquisition loop reads
// (OwnedFileForRecording), so the pages read it too.
func TestAReleaseCountsATrackAnIdentityNamesWithoutAnyMapping(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedRecordingOnTwoReleases(t, ctx, pool)

	var recordingID uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT musicbrainz_recording_id FROM tracks WHERE title = 'Even Further Down the Drain'`,
	).Scan(&recordingID); err != nil {
		t.Fatalf("read the recording: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, method, confidence, summary
		)
		VALUES ($1, 'external', $2, 'fingerprint', 1, 'Identified by its audio.')
	`, seedLibraryFile(t, ctx, pool, "/music/drain.flac"), recordingID); err != nil {
		t.Fatalf("seed identity: %v", err)
	}

	page := listReleases(t, ctx, pool, ListAlbumsParams{Query: "Pt. 1 & 2", Limit: 10})

	if len(page.Items) != 1 {
		t.Fatalf("titles = %v, want the compilation", listedReleaseTitles(page))
	}
	if page.Items[0].OwnedTrackCount != 2 {
		t.Errorf("owned = %d of 2, want both", page.Items[0].OwnedTrackCount)
	}
}

// A file that has gone from disk proves nothing today, wherever it is filed.
func TestATrackHeldOnlyByAMissingFileIsNotCounted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	_, compilationID := seedRecordingOnTwoReleases(t, ctx, pool)

	if _, err := pool.Exec(ctx,
		`UPDATE library_files SET missing_at = now()`); err != nil {
		t.Fatalf("mark the file missing: %v", err)
	}

	rows, err := New(pool).ListAlbumTracks(ctx, compilationID)
	if err != nil {
		t.Fatalf("ListAlbumTracks() error = %v", err)
	}
	for _, row := range rows {
		if row.HeldElsewhere {
			t.Errorf("%q reads as held by a file that is gone", row.Title)
		}
	}
}

// mapTheFileOntoBothReleases gives the one seeded file the second mapping
// migration 00033 allows: the same recording, answered on the compilation as
// well as on the EP the importer filed it under.
func mapTheFileOntoBothReleases(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	if _, err := pool.Exec(ctx, `
		INSERT INTO track_mappings (track_id, library_file_id, method)
		SELECT elsewhere.id, mapped.library_file_id, 'verified_download'
		FROM track_mappings AS mapped
		JOIN tracks AS carrying ON carrying.id = mapped.track_id
		JOIN tracks AS elsewhere
		    ON elsewhere.musicbrainz_recording_id = carrying.musicbrainz_recording_id
		   AND elsewhere.id <> carrying.id
	`); err != nil {
		t.Fatalf("map the file onto the other release: %v", err)
	}
}

// The library list is a list of files. A file answers a track on every release
// that carries its recording, so joining the mappings would repeat it once per
// release — against a total that only ever counted files, and under a browser
// that keys its rows by file id.
func TestTheLibraryListsAFileOnceHoweverManyTracksItAnswers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedRecordingOnTwoReleases(t, ctx, pool)
	mapTheFileOntoBothReleases(t, ctx, pool)

	page, err := New(pool).ListLibraryFiles(ctx, ListLibraryFilesParams{Limit: 50})
	if err != nil {
		t.Fatalf("ListLibraryFiles() error = %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("list = %v (total %d), want the one file listed once",
			listedFilePaths(page.Items), page.Total)
	}
}

// Which of a file's mappings describes its row is not arbitrary: a decision
// somebody made by hand is the one the row has to carry, or the only affordance
// for taking it back disappears behind an automatic mapping made later.
func TestALibraryRowIsDescribedByTheMappingMadeByHand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	seedRecordingOnTwoReleases(t, ctx, pool)
	mapTheFileOntoBothReleases(t, ctx, pool)

	if _, err := pool.Exec(ctx, `
		UPDATE track_mappings
		SET method = 'manual', is_manual = true, created_at = now()
		WHERE id = (SELECT id FROM track_mappings ORDER BY created_at DESC LIMIT 1)
	`); err != nil {
		t.Fatalf("decide one mapping by hand: %v", err)
	}

	page, err := New(pool).ListLibraryFiles(ctx, ListLibraryFilesParams{Limit: 50})
	if err != nil {
		t.Fatalf("ListLibraryFiles() error = %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("list = %v, want the one file listed once", listedFilePaths(page.Items))
	}
	if !page.Items[0].MappingManual {
		t.Errorf("row reads as matched by %q, want the manual mapping describing it",
			page.Items[0].MappingMethod.String)
	}
}

// The release page opens with one release and what is known about it, including
// the edition chosen for it, so it reads as one thing rather than a title with
// facts arriving separately.
func TestOneReleaseIsReadWithTheEditionChosenForIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	_, albumID, _, _ := seedFollowedArtist(t, ctx, pool)
	if _, err := pool.Exec(ctx, `
		INSERT INTO release_editions (
			album_id, musicbrainz_release_id, title, status, country, media_count, track_count,
			selected_automatically, selection_reason
		)
		VALUES ($1, $2, 'Music Has the Right to Children', 'Official', 'GB', 1, 10, true,
		        'the only official release')
	`, albumID, uuid.New()); err != nil {
		t.Fatalf("seed the edition: %v", err)
	}

	release, err := New(pool).GetAlbum(ctx, albumID)
	if err != nil {
		t.Fatalf("GetAlbum() error = %v", err)
	}
	if release.Title != "Music Has the Right to Children" || release.ArtistName != "Boards of Canada" {
		t.Fatalf("release = %#v, want the one that was asked for", release)
	}
	if !release.MusicbrainzReleaseID.Valid || release.TrackCount != 10 {
		t.Fatalf("edition = %#v, want the one chosen for it", release)
	}
	if release.SelectionReason != "the only official release" {
		t.Fatalf("selection reason = %q, want why this edition was chosen",
			release.SelectionReason)
	}
}

// A release nobody has cached a picture for says so, rather than reporting an
// archive it was never asked about.
func TestAReleaseWithNoCachedPictureNamesNoArchive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	_, albumID, _, _ := seedFollowedArtist(t, ctx, pool)

	release, err := New(pool).GetAlbum(ctx, albumID)
	if err != nil {
		t.Fatalf("GetAlbum() error = %v", err)
	}
	if release.CoverSource != "" {
		t.Fatalf("cover source = %q, want nothing claimed about a picture nobody fetched",
			release.CoverSource)
	}
}

// One track carries the release context a want for it would be filed under, and
// both ways the library can already hold it, so pressing want on a release page
// asks one question rather than four.
func TestOneTrackIsReadWithWhatAWantForItWouldCarry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	_, albumID, trackID, recordingID := seedFollowedArtist(t, ctx, pool)

	track, err := New(pool).GetTrackForWanting(ctx, trackID)
	if err != nil {
		t.Fatalf("GetTrackForWanting() error = %v", err)
	}
	if track.AlbumID != albumID || track.MusicbrainzRecordingID.UUID != recordingID {
		t.Fatalf("track = %#v, want the release and recording it would be wanted as", track)
	}
	if track.ArtistName != "Boards of Canada" || track.Title != "Roygbiv" {
		t.Fatalf("track = %#v, want the words a search would use", track)
	}
	if track.Owned || track.HeldElsewhere {
		t.Fatalf("track = %#v, want a track the library does not hold", track)
	}
}

// A recording proven on a file this release never mapped onto is still music the
// library holds, and the press has to say so rather than offering to fetch it
// again.
func TestATrackHeldUnderAnotherReleaseSaysSoWhenItIsWanted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	_, _, trackID, recordingID := seedFollowedArtist(t, ctx, pool)
	fileID := seedLibraryFile(t, ctx, pool, "/music/elsewhere.flac")
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, method, summary
		)
		VALUES ($1, 'external', $2, 'recording-id', 'proven by identifier')
	`, fileID, recordingID); err != nil {
		t.Fatalf("seed the identity: %v", err)
	}

	track, err := New(pool).GetTrackForWanting(ctx, trackID)
	if err != nil {
		t.Fatalf("GetTrackForWanting() error = %v", err)
	}
	if !track.HeldElsewhere {
		t.Fatalf("track = %#v, want the copy filed under another release counted", track)
	}
}

// The cached picture is read back exactly as it was stored, because it is served
// to a browser rather than interpreted.
func TestACachedReleasePictureIsReadBackAsItWasStored(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	_, albumID, _, _ := seedFollowedArtist(t, ctx, pool)
	queries := New(pool)

	if err := queries.SaveReleaseCoverArt(ctx, SaveReleaseCoverArtParams{
		AlbumID: albumID, Image: []byte("\x89PNG-ish"), ContentType: "image/png",
		Source: "coverartarchive",
	}); err != nil {
		t.Fatalf("SaveReleaseCoverArt() error = %v", err)
	}

	cover, err := queries.ReleaseCoverArt(ctx, albumID)
	if err != nil {
		t.Fatalf("ReleaseCoverArt() error = %v", err)
	}
	if string(cover.Image) != "\x89PNG-ish" || cover.ContentType != "image/png" {
		t.Fatalf("cover = %q of type %q, want the bytes that were stored",
			cover.Image, cover.ContentType)
	}
	if cover.Source != "coverartarchive" {
		t.Fatalf("source = %q, want the archive it came from", cover.Source)
	}
}

// A release with a row — picture or recorded absence — is answered from the cache
// and never asked about again, which is what stops the archive being asked on
// every page view.
func TestAReleaseWithARecordedAnswerIsNotAskedAboutAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	_, albumID, _, _ := seedFollowedArtist(t, ctx, pool)
	queries := New(pool)

	missing, err := queries.ReleasesMissingCoverArt(ctx, 10)
	if err != nil {
		t.Fatalf("ReleasesMissingCoverArt() error = %v", err)
	}
	if len(missing) != 1 || missing[0].ID != albumID {
		t.Fatalf("missing = %+v, want the release nobody has looked for a picture of", missing)
	}

	// An empty image is the answer "asked, and there is none".
	if err := queries.SaveReleaseCoverArt(ctx, SaveReleaseCoverArtParams{
		AlbumID: albumID, Image: nil, ContentType: "", Source: "",
	}); err != nil {
		t.Fatalf("SaveReleaseCoverArt() error = %v", err)
	}

	after, err := queries.ReleasesMissingCoverArt(ctx, 10)
	if err != nil {
		t.Fatalf("ReleasesMissingCoverArt() error = %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("missing = %+v, want a recorded absence to count as an answer", after)
	}
}

// A row that names an archive and holds no picture is not an answer. It is what
// a release looks like after a fetch that found nothing was written down under
// the archive's name: nothing looks for a cover for that release again, and
// there is no cover to show. It is asked about once more.
func TestAReleaseNamingAnArchiveWithNoPictureIsAskedAboutAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	_, albumID, _, _ := seedFollowedArtist(t, ctx, pool)
	queries := New(pool)

	if _, err := pool.Exec(ctx, `
		INSERT INTO release_cover_art (album_id, image, content_type, source)
		VALUES ($1, NULL, '', 'coverartarchive')
	`, albumID); err != nil {
		t.Fatalf("seed the row: %v", err)
	}

	missing, err := queries.ReleasesMissingCoverArt(ctx, 10)
	if err != nil {
		t.Fatalf("ReleasesMissingCoverArt() error = %v", err)
	}
	if len(missing) != 1 || missing[0].ID != albumID {
		t.Fatalf("missing = %+v, want the release nothing would ever picture", missing)
	}

	// And the answer it gets, whichever it is, is what stops it being asked a
	// third time.
	if err := queries.SaveReleaseCoverArt(ctx, SaveReleaseCoverArtParams{
		AlbumID: albumID, Image: nil, ContentType: "", Source: "none",
	}); err != nil {
		t.Fatalf("SaveReleaseCoverArt() error = %v", err)
	}
	answered, err := queries.ReleasesMissingCoverArt(ctx, 10)
	if err != nil {
		t.Fatalf("ReleasesMissingCoverArt() error = %v", err)
	}
	if len(answered) != 0 {
		t.Fatalf("missing = %+v, want the release asked once and then left alone", answered)
	}
}

// A cover the person supplied is permanent. 'user' is not an archive that
// answered — it is somebody having looked at the record and said this is the
// picture — so the sweep passes over it, and nothing asks an archive about that
// release again until they take it back.
//
// The row is written with no bytes on purpose. A supplied cover always carries
// bytes and would drop out of this list on those grounds alone; the test is
// that the name alone is enough, so the rule holds however the row got there.
func TestACoverThePersonSuppliedIsNeverAskedAboutAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	_, albumID, _, _ := seedFollowedArtist(t, ctx, pool)
	queries := New(pool)

	if _, err := pool.Exec(ctx, `
		INSERT INTO release_cover_art (album_id, image, content_type, source)
		VALUES ($1, ''::bytea, 'image/png', 'user')
	`, albumID); err != nil {
		t.Fatalf("seed the row: %v", err)
	}

	missing, err := queries.ReleasesMissingCoverArt(ctx, 10)
	if err != nil {
		t.Fatalf("ReleasesMissingCoverArt() error = %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("missing = %+v, want a supplied cover left alone", missing)
	}
}

// The picture a person supplies is kept as it was given, under the name that
// makes it permanent, and it replaces whatever an archive had answered before.
func TestASuppliedCoverReplacesTheOneAnArchiveGave(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	_, albumID, _, _ := seedFollowedArtist(t, ctx, pool)
	queries := New(pool)

	if err := queries.SaveReleaseCoverArt(ctx, SaveReleaseCoverArtParams{
		AlbumID: albumID, Image: []byte("the archive's picture"),
		ContentType: "image/jpeg", Source: "coverartarchive",
	}); err != nil {
		t.Fatalf("SaveReleaseCoverArt() error = %v", err)
	}
	if err := queries.SaveReleaseCoverArt(ctx, SaveReleaseCoverArtParams{
		AlbumID: albumID, Image: []byte("the picture somebody chose"),
		ContentType: "image/png", Source: "user",
	}); err != nil {
		t.Fatalf("SaveReleaseCoverArt() error = %v", err)
	}

	cover, err := queries.ReleaseCoverArt(ctx, albumID)
	if err != nil {
		t.Fatalf("ReleaseCoverArt() error = %v", err)
	}
	if cover.Source != "user" || string(cover.Image) != "the picture somebody chose" {
		t.Fatalf("cover = %+v, want the picture somebody chose", cover)
	}
}

// Taking a supplied cover back leaves the release with no row, which is what a
// release nobody has looked for a picture of looks like — so the sweep asks the
// archives about it again on its next pass.
func TestTakingBackASuppliedCoverLeavesTheReleaseToBeAskedAbout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	_, albumID, _, _ := seedFollowedArtist(t, ctx, pool)
	queries := New(pool)

	if err := queries.SaveReleaseCoverArt(ctx, SaveReleaseCoverArtParams{
		AlbumID: albumID, Image: []byte("the picture somebody chose"),
		ContentType: "image/png", Source: "user",
	}); err != nil {
		t.Fatalf("SaveReleaseCoverArt() error = %v", err)
	}

	removed, err := queries.DeleteUserReleaseCoverArt(ctx, albumID)
	if err != nil {
		t.Fatalf("DeleteUserReleaseCoverArt() error = %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed %d rows, want 1", removed)
	}
	missing, err := queries.ReleasesMissingCoverArt(ctx, 10)
	if err != nil {
		t.Fatalf("ReleasesMissingCoverArt() error = %v", err)
	}
	if len(missing) != 1 || missing[0].ID != albumID {
		t.Fatalf("missing = %+v, want the release asked about again", missing)
	}
}

// A cover an archive gave is a cached answer. Removing it here would empty the
// cache one release at a time for a question nobody asked, so it is untouched.
func TestTakingBackACoverLeavesAnArchivesAnswerAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	_, albumID, _, _ := seedFollowedArtist(t, ctx, pool)
	queries := New(pool)

	if err := queries.SaveReleaseCoverArt(ctx, SaveReleaseCoverArtParams{
		AlbumID: albumID, Image: []byte("the archive's picture"),
		ContentType: "image/jpeg", Source: "coverartarchive",
	}); err != nil {
		t.Fatalf("SaveReleaseCoverArt() error = %v", err)
	}

	removed, err := queries.DeleteUserReleaseCoverArt(ctx, albumID)
	if err != nil {
		t.Fatalf("DeleteUserReleaseCoverArt() error = %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed %d rows, want the archive's answer left alone", removed)
	}
	cover, err := queries.ReleaseCoverArt(ctx, albumID)
	if err != nil {
		t.Fatalf("ReleaseCoverArt() error = %v", err)
	}
	if cover.Source != "coverartarchive" {
		t.Fatalf("source = %q, want the archive's answer still cached", cover.Source)
	}
}

// Only mapped files are read for the picture they may be carrying: a mapping is
// a match this project already proved, so the picture inside one of those files
// is a picture of this release rather than of whatever sat in a folder.
func TestOnlyTheProvenFilesOfAReleaseAreReadForItsPicture(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	_, albumID, trackID, _ := seedFollowedArtist(t, ctx, pool)
	mapped := seedLibraryFile(t, ctx, pool, "/music/boc/08 Roygbiv.flac")
	seedLibraryFile(t, ctx, pool, "/music/boc/cover-only.flac")
	if _, err := pool.Exec(ctx, `
		INSERT INTO track_mappings (track_id, library_file_id, method, is_manual)
		VALUES ($1, $2, 'manual', true)
	`, trackID, mapped); err != nil {
		t.Fatalf("seed the mapping: %v", err)
	}

	paths, err := New(pool).ReleaseMappedFilePaths(ctx, ReleaseMappedFilePathsParams{
		AlbumID: albumID, Limit: 10,
	})
	if err != nil {
		t.Fatalf("ReleaseMappedFilePaths() error = %v", err)
	}
	if len(paths) != 1 || paths[0] != "/music/boc/08 Roygbiv.flac" {
		t.Fatalf("paths = %v, want only the file proven to be of this release", paths)
	}
}

// The cached artist picture is read back as it was stored, for the same reason a
// release cover is: it is served, never interpreted.
func TestACachedArtistPictureIsReadBackAsItWasStored(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	artistID, _, _, _ := seedFollowedArtist(t, ctx, pool)
	queries := New(pool)

	if err := queries.SaveArtistImage(ctx, SaveArtistImageParams{
		ArtistID: artistID, Image: []byte("jpeg-ish"), ContentType: "image/jpeg",
		Source: "wikidata",
	}); err != nil {
		t.Fatalf("SaveArtistImage() error = %v", err)
	}

	image, err := queries.ArtistImage(ctx, artistID)
	if err != nil {
		t.Fatalf("ArtistImage() error = %v", err)
	}
	if string(image.Image) != "jpeg-ish" || image.Source != "wikidata" {
		t.Fatalf("image = %q from %q, want the bytes that were stored",
			image.Image, image.Source)
	}
}

// Only artists MusicBrainz knows are asked about, because the picture is keyed on
// that identifier and there is nothing to ask with without it.
func TestAnArtistMusicBrainzDoesNotKnowIsNotAskedAboutForAPicture(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	known, _, _, _ := seedFollowedArtist(t, ctx, pool)
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (name, sort_name, followed_at, catalogue_summary)
		VALUES ('Unknown', 'Unknown', NULL, 'held because the library has their music')
	`); err != nil {
		t.Fatalf("seed the unidentified artist: %v", err)
	}

	missing, err := New(pool).ArtistsMissingImage(ctx, missingImages(time.Now(), 10))
	if err != nil {
		t.Fatalf("ArtistsMissingImage() error = %v", err)
	}
	if len(missing) != 1 || missing[0].ID != known {
		t.Fatalf("missing = %+v, want only the artist there is something to ask about",
			missing)
	}
}

// missingImages asks for the artists there is a picture to look for, as of a
// chain that last changed at the given moment.
func missingImages(chainChanged time.Time, batch int32) ArtistsMissingImageParams {
	return ArtistsMissingImageParams{
		ChainChanged: pgtype.Timestamptz{Time: chainChanged, Valid: true},
		Batch:        batch,
	}
}

// recordAbsence writes the answer "asked, and nobody had a picture", as of the
// given moment. The sweep writes one with now(); a test needs one older than the
// chain, and the moment it was recorded is the whole question here.
func recordAbsence(t *testing.T, ctx context.Context, pool *pgxpool.Pool, artistID uuid.UUID, when time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artist_images (artist_id, image, content_type, source, fetched_at)
		VALUES ($1, NULL, '', 'none', $2)
		ON CONFLICT (artist_id) DO UPDATE SET fetched_at = EXCLUDED.fetched_at
	`, artistID, when); err != nil {
		t.Fatalf("record the absence: %v", err)
	}
}

// An artist whose picture is already cached is not asked about again.
func TestAnArtistWithACachedPictureIsNotAskedAboutAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	artistID, _, _, _ := seedFollowedArtist(t, ctx, pool)
	queries := New(pool)
	if err := queries.SaveArtistImage(ctx, SaveArtistImageParams{
		ArtistID: artistID, Image: []byte("jpeg-ish"), ContentType: "image/jpeg",
		Source: "wikidata",
	}); err != nil {
		t.Fatalf("SaveArtistImage() error = %v", err)
	}

	missing, err := queries.ArtistsMissingImage(ctx, missingImages(time.Now(), 10))
	if err != nil {
		t.Fatalf("ArtistsMissingImage() error = %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("missing = %+v, want a picture to count as an answer", missing)
	}
}

// An absence recorded since the chain last changed is an answer to the question
// being asked now, so the artist is left alone.
func TestAnArtistWrittenOffSinceTheChainChangedIsNotAskedAboutAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	artistID, _, _, _ := seedFollowedArtist(t, ctx, pool)
	queries := New(pool)
	if err := queries.SaveArtistImage(ctx, SaveArtistImageParams{
		ArtistID: artistID, Image: nil, ContentType: "", Source: "none",
	}); err != nil {
		t.Fatalf("SaveArtistImage() error = %v", err)
	}

	missing, err := queries.ArtistsMissingImage(
		ctx, missingImages(time.Now().Add(-time.Hour), 10))
	if err != nil {
		t.Fatalf("ArtistsMissingImage() error = %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("missing = %+v, want a recorded absence to count as an answer", missing)
	}
}

// An absence recorded before the chain changed answers a question nobody is
// asking any more: it says the rungs of the day had no picture, and there is a
// rung now that never looked. So the artist is asked about again.
func TestAnArtistWrittenOffBeforeTheChainChangedIsAskedAboutAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	artistID, _, _, _ := seedFollowedArtist(t, ctx, pool)
	recordAbsence(t, ctx, pool, artistID, time.Now().Add(-48*time.Hour))

	missing, err := New(pool).ArtistsMissingImage(
		ctx, missingImages(time.Now().Add(-time.Hour), 10))
	if err != nil {
		t.Fatalf("ArtistsMissingImage() error = %v", err)
	}
	if len(missing) != 1 || missing[0].ID != artistID {
		t.Fatalf("missing = %+v, want the artist the new rung has never looked for", missing)
	}
}

// Asking again and finding nothing again records the absence afresh, so the
// batch is answered once rather than every pass finding the same artists and
// asking every service about them again for as long as the process lives.
func TestAnArtistAskedAboutAgainIsNotAskedAThirdTime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	artistID, _, _, _ := seedFollowedArtist(t, ctx, pool)
	recordAbsence(t, ctx, pool, artistID, time.Now().Add(-48*time.Hour))
	queries := New(pool)

	if err := queries.SaveArtistImage(ctx, SaveArtistImageParams{
		ArtistID: artistID, Image: nil, ContentType: "", Source: "none",
	}); err != nil {
		t.Fatalf("SaveArtistImage() error = %v", err)
	}

	missing, err := queries.ArtistsMissingImage(
		ctx, missingImages(time.Now().Add(-time.Hour), 10))
	if err != nil {
		t.Fatalf("ArtistsMissingImage() error = %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("missing = %+v, want the answer that was just written to count", missing)
	}
}

// A collection's worth of old absences being asked again must not hold up an
// artist nobody has ever looked for: a batch is small and the never-asked go
// first.
func TestAnArtistNobodyHasAskedAboutComesBeforeOneWrittenOffLongAgo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	writtenOff, neverAsked := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name, created_at)
		VALUES ($1, $2, 'Written off', 'Written off', now() - interval '2 days'),
		       ($3, $4, 'Never asked', 'Never asked', now())
	`, writtenOff, uuid.New(), neverAsked, uuid.New()); err != nil {
		t.Fatalf("seed the artists: %v", err)
	}
	recordAbsence(t, ctx, pool, writtenOff, time.Now().Add(-48*time.Hour))

	missing, err := New(pool).ArtistsMissingImage(
		ctx, missingImages(time.Now().Add(-time.Hour), 1))
	if err != nil {
		t.Fatalf("ArtistsMissingImage() error = %v", err)
	}
	if len(missing) != 1 || missing[0].ID != neverAsked {
		t.Fatalf("missing = %+v, want the artist nobody has looked for asked about first",
			missing)
	}
}

// A followed artist nobody has asked MusicBrainz about lately is what the feed's
// clock reads: a release published after the follow only reaches the catalogue if
// somebody asks again, and the whole point of following is that nobody has to.
func TestAFollowedArtistNobodyAskedAboutTodayIsStale(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	artistID, _, _, _ := seedFollowedArtist(t, ctx, pool)

	stale, err := New(pool).StaleFollowedArtists(ctx, StaleFollowedArtistsParams{
		RefreshedBefore: pgtype.Timestamptz{Time: time.Now().Add(-24 * time.Hour), Valid: true},
		RowLimit:        10,
	})
	if err != nil {
		t.Fatalf("StaleFollowedArtists() error = %v", err)
	}
	if len(stale) != 1 || stale[0] != artistID {
		t.Fatalf("stale = %v, want the followed artist nobody has asked about (%s)",
			stale, artistID)
	}
}

// An artist asked about an hour ago is left alone. MusicBrainz answers one
// request a second on a limiter shared with the interactive search, so re-asking
// on every pass would spend that budget learning nothing.
func TestAnArtistAskedAboutRecentlyIsNotStale(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	artistID, _, _, _ := seedFollowedArtist(t, ctx, pool)
	if _, err := pool.Exec(ctx, `
		UPDATE artists SET last_refreshed_at = now() WHERE id = $1
	`, artistID); err != nil {
		t.Fatal(err)
	}

	stale, err := New(pool).StaleFollowedArtists(ctx, StaleFollowedArtistsParams{
		RefreshedBefore: pgtype.Timestamptz{Time: time.Now().Add(-24 * time.Hour), Valid: true},
		RowLimit:        10,
	})
	if err != nil {
		t.Fatalf("StaleFollowedArtists() error = %v", err)
	}
	if len(stale) != 0 {
		t.Fatalf("stale = %v, want the limiter left alone", stale)
	}
}

// An artist in the catalogue because the library holds their music is never
// asked about: holding is a statement of fact, not a request to keep anybody
// complete.
func TestAHeldArtistIsNeverAskedAboutOnAClock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (musicbrainz_id, name, sort_name, followed_at, catalogue_summary)
		VALUES ($1, 'Held', 'Held', NULL, 'held because the library has their music')
	`, uuid.New()); err != nil {
		t.Fatalf("seed the held artist: %v", err)
	}

	stale, err := New(pool).StaleFollowedArtists(ctx, StaleFollowedArtistsParams{
		RefreshedBefore: pgtype.Timestamptz{Time: time.Now().Add(-24 * time.Hour), Valid: true},
		RowLimit:        10,
	})
	if err != nil {
		t.Fatalf("StaleFollowedArtists() error = %v", err)
	}
	if len(stale) != 0 {
		t.Fatalf("stale = %v, want a held artist left out of the follow clock", stale)
	}
}

// An artist nobody followed has to say why they are in a list the user never
// chose, or an unexplained row reads as a bug.
func TestAnArtistNobodyFollowedMustExplainWhyTheyAreHere(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (musicbrainz_id, name, sort_name, followed_at)
		VALUES ($1, 'Unexplained', 'Unexplained', NULL)
	`, uuid.New()); err == nil {
		t.Fatal("an artist nobody followed was recorded with nothing to explain them")
	}
}

// A blank sentence is no sentence: whitespace would satisfy a NOT NULL and still
// leave the row unexplained.
func TestAWhitespaceExplanationDoesNotExplainAnArtist(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (musicbrainz_id, name, sort_name, followed_at, catalogue_summary)
		VALUES ($1, 'Blank', 'Blank', NULL, '   ')
	`, uuid.New()); err == nil {
		t.Fatal("whitespace was accepted as the reason an artist is in the catalogue")
	}
}

// The follow feed's own read: a track on a release published after the follow, by
// somebody the user follows, that the library does not hold and nobody has asked
// for.
func TestTheFeedReadOffersATrackReleasedAfterTheFollow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	artistID := seedArtistFollowedAMonthAgo(t, ctx, pool)
	seedWantableRelease(t, ctx, pool, artistID, "Tomorrow's Harvest",
		time.Now().AddDate(0, 0, -1).Format("2006-01-02"), []string{"Gemini"})

	tracks, err := New(pool).FollowFeedTracks(ctx, 100)
	if err != nil {
		t.Fatalf("FollowFeedTracks() error = %v", err)
	}
	if len(tracks) != 1 || tracks[0].Title != "Gemini" {
		t.Fatalf("tracks = %+v, want the one published after the follow", tracks)
	}
	if tracks[0].ArtistName != "Boards of Canada" || !tracks[0].MusicbrainzReleaseGroupID.Valid {
		t.Fatalf("track = %+v, want the release context a want would carry", tracks[0])
	}
}

// A recording somebody already asked for is not asked for again, which is also
// what keeps a refused one refused: the not_wanted target holds the recording,
// so the feed finds it and passes over.
func TestTheFeedReadPassesOverARecordingAlreadyAskedFor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	artistID := seedArtistFollowedAMonthAgo(t, ctx, pool)
	seedWantableRelease(t, ctx, pool, artistID, "Tomorrow's Harvest",
		time.Now().AddDate(0, 0, -1).Format("2006-01-02"), []string{"Gemini"})

	var recordingID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT musicbrainz_recording_id FROM tracks WHERE title = 'Gemini'
	`).Scan(&recordingID); err != nil {
		t.Fatal(err)
	}
	queries := New(pool)
	id, _, err := queries.CreateAcquisitionTarget(ctx, wantParams("Gemini", recordingID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.StopPursuingAcquisitionTarget(ctx, id, "no longer wanted"); err != nil {
		t.Fatal(err)
	}

	tracks, err := queries.FollowFeedTracks(ctx, 100)
	if err != nil {
		t.Fatalf("FollowFeedTracks() error = %v", err)
	}
	if len(tracks) != 0 {
		t.Fatalf("tracks = %+v, want the refusal left standing", tracks)
	}
}

// Scope is a fixed vocabulary, and anything else is refused rather than quietly
// ignored: a browser asking for a scope nothing knows would otherwise be shown
// the whole catalogue and read it as the answer to its question.
func TestListAlbumsRefusesAScopeItDoesNotKnow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	if _, err := New(pool).ListAlbums(ctx, ListAlbumsParams{Scope: "wanted"}); err == nil {
		t.Fatal("a scope the list does not know was accepted")
	}
}

// The same for status: the shapes the browser shows are enumerated here, and one
// it has never heard of is a question this list cannot answer.
func TestListAlbumsRefusesAStatusItDoesNotKnow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	if _, err := New(pool).ListAlbums(ctx, ListAlbumsParams{Status: "nearly"}); err == nil {
		t.Fatal("a status the list does not know was accepted")
	}
}

func seedArtistFollowedAMonthAgo(t *testing.T, ctx context.Context, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()

	artistID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name, followed_at, monitor_level)
		VALUES ($1, $2, 'Boards of Canada', 'Boards of Canada', now() - interval '30 days',
		        'everything')
	`, artistID, uuid.New()); err != nil {
		t.Fatalf("seed artist: %v", err)
	}
	return artistID
}

// An artist's page states how much of a discography the library holds while
// asking for that discography one page at a time, so the fraction is counted
// here rather than over whatever page arrived. albumStatusFilters answers a
// neighbouring question and cannot stand in for it; the tests below are the
// places the two part company.

func TestDiscographyCountsOnlyMonitoredReleases(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	// At 'albums_eps' a single is browsable but is not part of complete, so it
	// is neither owned nor missing however few of its tracks the library holds.
	artistID := seedDiscographyArtist(t, ctx, pool, "albums_eps",
		seedRelease{albumType: "album", tracks: 2, owned: 2},
		seedRelease{albumType: "single", tracks: 1},
	)

	counts, err := New(pool).ArtistDiscography(ctx, artistID)
	if err != nil {
		t.Fatalf("ArtistDiscography() error = %v", err)
	}
	want := ArtistDiscography{Releases: 2, Owned: 1, Missing: 0, Dismissed: 0, Counted: 1}
	if counts != want {
		t.Errorf("counts = %#v, want %#v", counts, want)
	}
}

func TestDiscographyLeavesADismissedReleaseOutOfTheFraction(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	// Every gap decided about is not a gap. The release stays on the page and
	// leaves both halves of the fraction, so 1 of 1 rather than 1 of 2.
	artistID := seedDiscographyArtist(t, ctx, pool, "everything",
		seedRelease{albumType: "album", tracks: 2, owned: 2},
		seedRelease{albumType: "album", tracks: 3, dismissed: 3},
	)

	counts, err := New(pool).ArtistDiscography(ctx, artistID)
	if err != nil {
		t.Fatalf("ArtistDiscography() error = %v", err)
	}
	want := ArtistDiscography{Releases: 2, Owned: 1, Missing: 0, Dismissed: 1, Counted: 1}
	if counts != want {
		t.Errorf("counts = %#v, want %#v", counts, want)
	}
}

func TestDiscographyCountsAReachedReleaseCompleteWhenOnlyOwnedIsWatched(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	// At 'owned' the denominator is what the library maps, so a release it
	// reaches is complete however many tracks the release group lists — and one
	// it reaches nothing of is not monitored at all. The browser's status
	// filters would call the first of these partial.
	artistID := seedDiscographyArtist(t, ctx, pool, "owned",
		seedRelease{albumType: "album", tracks: 10, owned: 4},
		seedRelease{albumType: "album", tracks: 5},
	)

	counts, err := New(pool).ArtistDiscography(ctx, artistID)
	if err != nil {
		t.Fatalf("ArtistDiscography() error = %v", err)
	}
	want := ArtistDiscography{Releases: 2, Owned: 1, Missing: 0, Dismissed: 0, Counted: 1}
	if counts != want {
		t.Errorf("counts = %#v, want %#v", counts, want)
	}
}

func TestDiscographyCountsAPartlyHeldReleaseAsMissing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	// What "want everything missing" would want: a release with anything left
	// to get, whether it holds some of its tracks or none of them.
	artistID := seedDiscographyArtist(t, ctx, pool, "everything",
		seedRelease{albumType: "album", tracks: 3, owned: 1},
		seedRelease{albumType: "album", tracks: 4},
	)

	counts, err := New(pool).ArtistDiscography(ctx, artistID)
	if err != nil {
		t.Fatalf("ArtistDiscography() error = %v", err)
	}
	want := ArtistDiscography{Releases: 2, Owned: 0, Missing: 2, Dismissed: 0, Counted: 2}
	if counts != want {
		t.Errorf("counts = %#v, want %#v", counts, want)
	}
}

func TestDiscographyCountsNothingForAnArtistWithNoReleases(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID := seedDiscographyArtist(t, ctx, pool, "everything")

	counts, err := New(pool).ArtistDiscography(ctx, artistID)
	if err != nil {
		t.Fatalf("ArtistDiscography() error = %v", err)
	}
	if counts != (ArtistDiscography{}) {
		t.Errorf("counts = %#v, want every count zero", counts)
	}
}

// Narrowing to missing and counting what is missing have to be the one rule, or
// a chip offers a release under a heading the header does not count it in. These
// pin the two together on the cases where the status filters would part company
// with the fraction.

func TestNarrowingToMissingSkipsAnUnmonitoredRelease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	// A single at 'albums_eps' has tracks the library does not hold, so the
	// status filters call it missing. It is not part of complete, so the header
	// does not, and neither does the chip that says "Missing".
	artistID := seedDiscographyArtist(t, ctx, pool, "albums_eps",
		seedRelease{albumType: "single", tracks: 1},
	)

	page, err := New(pool).ListAlbums(ctx, ListAlbumsParams{
		ArtistID:     uuid.NullUUID{UUID: artistID, Valid: true},
		Completeness: "missing",
		Limit:        48,
	})
	if err != nil {
		t.Fatalf("ListAlbums() error = %v", err)
	}
	if len(page.Items) != 0 || page.Total != 0 {
		t.Errorf("missing = %d releases, total %d; want none", len(page.Items), page.Total)
	}
}

func TestNarrowingToMissingSkipsAReleaseWhoseEveryGapWasDismissed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID := seedDiscographyArtist(t, ctx, pool, "everything",
		seedRelease{albumType: "album", tracks: 3, dismissed: 3},
	)

	page, err := New(pool).ListAlbums(ctx, ListAlbumsParams{
		ArtistID:     uuid.NullUUID{UUID: artistID, Valid: true},
		Completeness: "missing",
		Limit:        48,
	})
	if err != nil {
		t.Fatalf("ListAlbums() error = %v", err)
	}
	if len(page.Items) != 0 {
		t.Errorf("missing = %d releases, want none", len(page.Items))
	}
}

func TestNarrowingToMissingHoldsBothTheEmptyAndThePartlyHeld(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID := seedDiscographyArtist(t, ctx, pool, "everything",
		seedRelease{albumType: "album", tracks: 3, owned: 1},
		seedRelease{albumType: "album", tracks: 4},
		seedRelease{albumType: "album", tracks: 2, owned: 2},
	)

	page, err := New(pool).ListAlbums(ctx, ListAlbumsParams{
		ArtistID:     uuid.NullUUID{UUID: artistID, Valid: true},
		Completeness: "missing",
		Limit:        48,
	})
	if err != nil {
		t.Fatalf("ListAlbums() error = %v", err)
	}
	if len(page.Items) != 2 {
		t.Errorf("missing = %d releases, want 2", len(page.Items))
	}
}

// The chips' counts are the header's numbers, so what one narrows to has to be
// as many releases as the other counted. This is that promise, on the artist
// whose monitor level makes the two rules most likely to drift apart.
func TestNarrowingMatchesTheCountItIsLabelledWith(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	artistID := seedDiscographyArtist(t, ctx, pool, "owned",
		seedRelease{albumType: "album", tracks: 10, owned: 4},
		seedRelease{albumType: "album", tracks: 5},
		seedRelease{albumType: "single", tracks: 1, owned: 1},
	)
	queries := New(pool)

	counts, err := queries.ArtistDiscography(ctx, artistID)
	if err != nil {
		t.Fatalf("ArtistDiscography() error = %v", err)
	}
	for shape, want := range map[string]int64{"owned": counts.Owned, "missing": counts.Missing} {
		page, err := queries.ListAlbums(ctx, ListAlbumsParams{
			ArtistID:     uuid.NullUUID{UUID: artistID, Valid: true},
			Completeness: shape,
			Limit:        48,
		})
		if err != nil {
			t.Fatalf("ListAlbums(%s) error = %v", shape, err)
		}
		if int64(len(page.Items)) != want || page.Total != want {
			t.Errorf("%s narrowed to %d releases (total %d), but the header counts %d",
				shape, len(page.Items), page.Total, want)
		}
	}
}

func TestListAlbumsRejectsACompletenessItDoesNotKnow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	if _, err := New(pool).ListAlbums(ctx, ListAlbumsParams{Completeness: "nearly"}); err == nil {
		t.Fatal("a completeness the list does not know was accepted")
	}
}

// seedRelease is a release said as the three counts a release carries, which is
// all the fraction reads. The triggers that maintain them are not what these
// tests are about.
type seedRelease struct {
	albumType string
	tracks    int
	owned     int
	dismissed int
}

func seedDiscographyArtist(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, monitorLevel string,
	releases ...seedRelease,
) uuid.UUID {
	t.Helper()

	artistID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name, followed_at, monitor_level)
		VALUES ($1, $2, 'Autechre', 'Autechre', now(), $3)
	`, artistID, uuid.New(), monitorLevel); err != nil {
		t.Fatalf("seed artist: %v", err)
	}
	for index, release := range releases {
		if _, err := pool.Exec(ctx, `
			INSERT INTO albums (id, artist_id, musicbrainz_release_group_id, title, album_type,
			                    track_count, owned_track_count, dismissed_track_count)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`, uuid.New(), artistID, uuid.New(), fmt.Sprintf("Release %d", index),
			release.albumType, release.tracks, release.owned, release.dismissed); err != nil {
			t.Fatalf("seed release %d: %v", index, err)
		}
	}
	return artistID
}

// The name is the only thing a listing gives, so the answer is only useful when
// exactly one artist answers to it.
func TestANameOneArtistCarriesReadsBackThatArtistsIdentifier(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	musicbrainzID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name)
		VALUES ($1, $2, 'umbraid', 'umbraid')
	`, uuid.New(), musicbrainzID); err != nil {
		t.Fatalf("seed artist: %v", err)
	}

	identified, err := queries.ArtistMusicBrainzIDsByName(ctx, []string{"umbraid"})
	if err != nil {
		t.Fatalf("ArtistMusicBrainzIDsByName() error = %v", err)
	}
	if identified["umbraid"] != musicbrainzID {
		t.Errorf("identifier for umbraid = %v, want %v", identified["umbraid"], musicbrainzID)
	}
}

// Two artists of one name is a question about which was meant, and nothing here
// answers a question by picking one of the answers.
func TestANameTwoArtistsShareIdentifiesNeitherOfThem(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	for range 2 {
		if _, err := pool.Exec(ctx, `
			INSERT INTO artists (id, musicbrainz_id, name, sort_name)
			VALUES ($1, $2, 'Nova', 'Nova')
		`, uuid.New(), uuid.New()); err != nil {
			t.Fatalf("seed artist: %v", err)
		}
	}

	identified, err := queries.ArtistMusicBrainzIDsByName(ctx, []string{"Nova"})
	if err != nil {
		t.Fatalf("ArtistMusicBrainzIDsByName() error = %v", err)
	}
	if id, found := identified["Nova"]; found {
		t.Errorf("identifier for Nova = %v, want none while two artists carry the name", id)
	}
}

// An artist the library made a row for without ever reaching MusicBrainz has
// nothing to seed, and that is silence rather than an answer.
func TestAnArtistWithoutAMusicBrainzIdentifierIdentifiesNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, name, sort_name) VALUES ($1, 'Ket Robinson', 'Robinson, Ket')
	`, uuid.New()); err != nil {
		t.Fatalf("seed artist: %v", err)
	}

	identified, err := queries.ArtistMusicBrainzIDsByName(ctx, []string{"Ket Robinson"})
	if err != nil {
		t.Fatalf("ArtistMusicBrainzIDsByName() error = %v", err)
	}
	if id, found := identified["Ket Robinson"]; found {
		t.Errorf("identifier for Ket Robinson = %v, want none", id)
	}
}

// The names differ from the credits only in case, which is a difference this
// comparison keeps: an exact agreement is the only one worth acting on.
func TestANameThatDiffersOnlyInCaseIdentifiesNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)

	if _, err := pool.Exec(ctx, `
		INSERT INTO artists (id, musicbrainz_id, name, sort_name)
		VALUES ($1, $2, 'umbraid', 'umbraid')
	`, uuid.New(), uuid.New()); err != nil {
		t.Fatalf("seed artist: %v", err)
	}

	identified, err := queries.ArtistMusicBrainzIDsByName(ctx, []string{"Umbraid"})
	if err != nil {
		t.Fatalf("ArtistMusicBrainzIDsByName() error = %v", err)
	}
	if id, found := identified["Umbraid"]; found {
		t.Errorf("identifier for Umbraid = %v, want none", id)
	}
}
