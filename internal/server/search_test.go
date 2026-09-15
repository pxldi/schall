package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

// The repository is what the route is wired to at startup, through a type
// assertion that would otherwise fail silently and leave search reporting
// itself unavailable in production while every test here passed.
var _ SearchStore = (*db.Queries)(nil)

// searchingStore is a store that can also answer the one question. It is kept
// apart from fakeStore rather than folded into it because SearchStore is
// optional on the store, and a fake that always implements it leaves no way to
// ask what happens to an installation without one.
type searchingStore struct {
	*fakeStore
	results db.SearchResults
	err     error
	asked   []db.SearchParams
}

func (store *searchingStore) Search(_ context.Context, params db.SearchParams) (db.SearchResults, error) {
	store.asked = append(store.asked, params)
	if store.err != nil {
		return db.SearchResults{}, store.err
	}
	return store.results, nil
}

func searchResponseFor(t *testing.T, store Store, url string, want int) searchResponse {
	t.Helper()
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, url, nil))
	if recorder.Code != want {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, want, recorder.Body)
	}
	var body searchResponse
	if recorder.Code == http.StatusOK {
		if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
	}
	return body
}

func TestSearchGroupsWhatItFindsByKind(t *testing.T) {
	artistID, albumID, trackID := uuid.New(), uuid.New(), uuid.New()
	fileID, playlistID := uuid.New(), uuid.New()
	store := &searchingStore{fakeStore: &fakeStore{}, results: db.SearchResults{
		Artists:  []db.SearchArtistsRow{{ID: artistID, Name: "Portishead", SortName: "Portishead", Followed: true}},
		Releases: []db.SearchReleasesRow{{ID: albumID, Title: "Dummy", ArtistID: artistID, ArtistName: "Portishead"}},
		Tracks: []db.SearchCatalogueTracksRow{{
			ID: trackID, Title: "Sour Times", AlbumID: albumID, AlbumTitle: "Dummy",
			ArtistID: artistID, ArtistName: "Portishead", DiscNumber: 1,
		}},
		Files:     []db.SearchLibraryFilesRow{{ID: fileID, Path: "/music/sour times.flac", MatchStatus: "matched"}},
		Playlists: []db.SearchPlaylistsRow{{ID: playlistID, Name: "Trip hop", Source: "manual"}},
	}}

	body := searchResponseFor(t, store, "/api/v1/search?q=portishead", http.StatusOK)

	if body.Query != "portishead" {
		t.Errorf("query = %q, want the query as asked", body.Query)
	}
	if len(body.Artists) != 1 || body.Artists[0].ID != artistID {
		t.Errorf("artists = %#v", body.Artists)
	}
	if len(body.Releases) != 1 || body.Releases[0].ArtistID != artistID {
		t.Errorf("releases = %#v", body.Releases)
	}
	// A catalogue track is linked to through the release that holds it, so the
	// release identifier has to survive the response.
	if len(body.Tracks) != 1 || body.Tracks[0].AlbumID != albumID {
		t.Errorf("tracks = %#v", body.Tracks)
	}
	if len(body.Files) != 1 || body.Files[0].ID != fileID {
		t.Errorf("files = %#v", body.Files)
	}
	if len(body.Playlists) != 1 || body.Playlists[0].ID != playlistID {
		t.Errorf("playlists = %#v", body.Playlists)
	}
}

// Five per kind is the front door's whole plan for size: the list pages are
// paged and indexed and stay the workhorse.
func TestSearchAnswersFiveOfEachKindByDefault(t *testing.T) {
	store := &searchingStore{fakeStore: &fakeStore{}}

	body := searchResponseFor(t, store, "/api/v1/search?q=portishead", http.StatusOK)

	if len(store.asked) != 1 || store.asked[0] != (db.SearchParams{Query: "portishead", Limit: 5}) {
		t.Fatalf("asked = %#v, want one search bounded at five", store.asked)
	}
	if body.Limit != 5 {
		t.Errorf("limit = %d, want 5", body.Limit)
	}
}

func TestSearchTakesTheLimitItIsGiven(t *testing.T) {
	store := &searchingStore{fakeStore: &fakeStore{}}

	searchResponseFor(t, store, "/api/v1/search?q=portishead&limit=20", http.StatusOK)

	if len(store.asked) != 1 || store.asked[0].Limit != 20 {
		t.Fatalf("asked = %#v, want the limit as given", store.asked)
	}
}

// The answer to wanting more than a glance is the list page, not a bigger
// glance, so the cap is refused rather than quietly clamped.
func TestSearchRefusesALimitBeyondTheCap(t *testing.T) {
	store := &searchingStore{fakeStore: &fakeStore{}}

	searchResponseFor(t, store, "/api/v1/search?q=portishead&limit=500", http.StatusUnprocessableEntity)

	if len(store.asked) != 0 {
		t.Fatalf("asked = %#v, want nothing asked", store.asked)
	}
}

// An empty box is not a request for the collection. The store here fails on
// every call, so answering at all proves nothing was asked of it.
func TestSearchAnswersAnEmptyQueryWithoutAskingAnything(t *testing.T) {
	store := &searchingStore{fakeStore: &fakeStore{}, err: errors.New("the database was asked")}

	body := searchResponseFor(t, store, "/api/v1/search?q=%20%20", http.StatusOK)

	if len(body.Artists) != 0 || len(body.Releases) != 0 || len(body.Tracks) != 0 ||
		len(body.Files) != 0 || len(body.Playlists) != 0 {
		t.Fatalf("body = %#v, want every group empty", body)
	}
}

// Every group is present even when nothing matched, so a caller lays its
// headings out once rather than telling "no answer" apart from "not searched".
func TestSearchAnswersEveryGroupEvenWhenNothingMatched(t *testing.T) {
	store := &searchingStore{fakeStore: &fakeStore{}}

	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/search?q=nothing", nil))

	var body map[string]json.RawMessage
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, group := range []string{"artists", "releases", "tracks", "files", "playlists"} {
		if string(body[group]) != "[]" {
			t.Errorf("%s = %s, want an empty list", group, body[group])
		}
	}
}

// Without a store that can answer it, search says so rather than half-working.
func TestSearchReportsItselfUnavailableWithoutAStore(t *testing.T) {
	searchResponseFor(t, &fakeStore{}, "/api/v1/search?q=portishead", http.StatusServiceUnavailable)
}

// The whole point of the total: it describes the answer, not the page of it
// that came back, so "All 214 in Files" can be written over a list of five.
func TestSearchSaysHowManyAnsweredBeyondTheOnesItReturned(t *testing.T) {
	store := &searchingStore{fakeStore: &fakeStore{}, results: db.SearchResults{
		Files:  []db.SearchLibraryFilesRow{{ID: uuid.New(), Path: "/music/one.flac"}},
		Totals: db.SearchTotals{Files: 214},
	}}

	body := searchResponseFor(t, store, "/api/v1/search?q=kid", http.StatusOK)

	if body.Totals.Files != 214 {
		t.Errorf("files total = %d, want the 214 that answered", body.Totals.Files)
	}
}

// A kind nobody escapes out of is still counted, so a caller never has to tell
// "none" apart from "not counted".
func TestSearchCountsAKindThatAnsweredWithNothing(t *testing.T) {
	store := &searchingStore{fakeStore: &fakeStore{}}

	body := searchResponseFor(t, store, "/api/v1/search?q=kid", http.StatusOK)

	if body.Totals.Artists != 0 || body.Totals.Playlists != 0 {
		t.Errorf("totals = %#v, want nothing counted", body.Totals)
	}
}

// An empty box asks nothing, so there is nothing to have counted either.
func TestSearchCountsNothingForAnEmptyQuery(t *testing.T) {
	store := &searchingStore{fakeStore: &fakeStore{}, err: errors.New("the database was asked")}

	body := searchResponseFor(t, store, "/api/v1/search?q=%20%20", http.StatusOK)

	if body.Totals != (searchTotals{}) {
		t.Errorf("totals = %#v, want nothing counted", body.Totals)
	}
}

// The figure an artist row is read by is what the library holds of them.
func TestSearchSaysHowManyFilesAnArtistHas(t *testing.T) {
	store := &searchingStore{fakeStore: &fakeStore{}, results: db.SearchResults{
		Artists: []db.SearchArtistsRow{{
			ID: uuid.New(), Name: "Radiohead", SortName: "Radiohead",
			Followed: true, FileCount: 148, ReleaseCount: 9,
		}},
	}}

	body := searchResponseFor(t, store, "/api/v1/search?q=radiohead", http.StatusOK)

	if len(body.Artists) != 1 || body.Artists[0].FileCount != 148 {
		t.Errorf("artists = %#v, want 148 files", body.Artists)
	}
}

func TestSearchSaysHowManyReleasesAnArtistIsHeldOn(t *testing.T) {
	store := &searchingStore{fakeStore: &fakeStore{}, results: db.SearchResults{
		Artists: []db.SearchArtistsRow{{
			ID: uuid.New(), Name: "Radiohead", SortName: "Radiohead",
			Followed: true, FileCount: 148, ReleaseCount: 9,
		}},
	}}

	body := searchResponseFor(t, store, "/api/v1/search?q=radiohead", http.StatusOK)

	if len(body.Artists) != 1 || body.Artists[0].ReleaseCount != 9 {
		t.Errorf("artists = %#v, want 9 releases", body.Artists)
	}
}

func TestSearchSaysHowLongACatalogueTrackIs(t *testing.T) {
	store := &searchingStore{fakeStore: &fakeStore{}, results: db.SearchResults{
		Tracks: []db.SearchCatalogueTracksRow{{
			ID: uuid.New(), Title: "Idioteque", DiscNumber: 1,
			DurationMs: pgtype.Int4{Int32: 309_000, Valid: true},
			AlbumID:    uuid.New(), AlbumTitle: "Kid A", ArtistID: uuid.New(),
			ArtistName: "Radiohead",
		}},
	}}

	body := searchResponseFor(t, store, "/api/v1/search?q=idioteque", http.StatusOK)

	if len(body.Tracks) != 1 || body.Tracks[0].DurationMs == nil ||
		*body.Tracks[0].DurationMs != 309_000 {
		t.Errorf("tracks = %#v, want a duration of 309000ms", body.Tracks)
	}
}

// A track the catalogue has no length for says nothing rather than saying zero,
// which would print as 0:00 beside music that is not silent.
func TestSearchLeavesOutTheLengthOfATrackNobodyKnows(t *testing.T) {
	store := &searchingStore{fakeStore: &fakeStore{}, results: db.SearchResults{
		Tracks: []db.SearchCatalogueTracksRow{{
			ID: uuid.New(), Title: "Untitled", DiscNumber: 1,
			AlbumID: uuid.New(), AlbumTitle: "Bootleg", ArtistID: uuid.New(),
			ArtistName: "Unknown",
		}},
	}}

	body := searchResponseFor(t, store, "/api/v1/search?q=untitled", http.StatusOK)

	if len(body.Tracks) != 1 || body.Tracks[0].DurationMs != nil {
		t.Errorf("tracks = %#v, want no duration at all", body.Tracks)
	}
}

// Whether the library holds the recording is what the row's state mark reads.
// It is display: it says the music is here, never which file is this track.
func TestSearchSaysWhetherTheLibraryHoldsACatalogueTrack(t *testing.T) {
	store := &searchingStore{fakeStore: &fakeStore{}, results: db.SearchResults{
		Tracks: []db.SearchCatalogueTracksRow{{
			ID: uuid.New(), Title: "Idioteque", DiscNumber: 1, Owned: true,
			AlbumID: uuid.New(), AlbumTitle: "Kid A", ArtistID: uuid.New(),
			ArtistName: "Radiohead",
		}},
	}}

	body := searchResponseFor(t, store, "/api/v1/search?q=idioteque", http.StatusOK)

	if len(body.Tracks) != 1 || !body.Tracks[0].Owned {
		t.Errorf("tracks = %#v, want the track owned", body.Tracks)
	}
}

func TestSearchSaysHowBigAFileIs(t *testing.T) {
	store := &searchingStore{fakeStore: &fakeStore{}, results: db.SearchResults{
		Files: []db.SearchLibraryFilesRow{{
			ID: uuid.New(), Path: "/music/kid-a/01.flac",
			SizeBytes: 32_929_382, MatchStatus: "matched",
		}},
	}}

	body := searchResponseFor(t, store, "/api/v1/search?q=kid", http.StatusOK)

	if len(body.Files) != 1 || body.Files[0].SizeBytes != 32_929_382 {
		t.Errorf("files = %#v, want the size the scanner recorded", body.Files)
	}
}
