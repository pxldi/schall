package musicbrainz

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// The artist lookup asks for genres and reads them back most voted first. The
// response below is the shape MusicBrainz sends for /artist/<id>?inc=genres.
func TestArtistCatalogueReadsGenresMostVotedFirst(t *testing.T) {
	var artistInclude, browseInclude string
	client := testClient(t, func(response http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/artist/") {
			artistInclude = request.URL.Query().Get("inc")
			_, _ = response.Write([]byte(`{
				"id": "a74b1b7f-71a5-4011-9441-d0b5e4122711",
				"name": "Radiohead",
				"sort-name": "Radiohead",
				"genres": [
					{"id": "1", "name": "art rock", "count": 9},
					{"id": "2", "name": "alternative rock", "count": 21},
					{"id": "3", "name": "seen live", "count": 0},
					{"id": "4", "name": "electronic", "count": 9}
				]
			}`))
			return
		}
		browseInclude = request.URL.Query().Get("inc")
		_, _ = response.Write([]byte(`{
			"release-group-count": 1,
			"release-groups": [
				{"id": "b1392450-e666-3926-a536-22c65f834433", "title": "OK Computer",
				 "first-release-date": "1997-05-21", "primary-type": "Album",
				 "genres": [
					{"name": "alternative rock", "count": 14},
					{"name": "art rock", "count": 6}
				 ]}
			]
		}`))
	})

	catalogue, err := client.ArtistCatalogue(context.Background(),
		mustUUID("a74b1b7f-71a5-4011-9441-d0b5e4122711"))
	if err != nil {
		t.Fatalf("ArtistCatalogue() error = %v", err)
	}
	if artistInclude != "genres" {
		t.Errorf("artist inc = %q, want genres", artistInclude)
	}
	if browseInclude != "genres" {
		t.Errorf("release-group browse inc = %q, want genres", browseInclude)
	}
	// Most voted first, ties by name, and the genre nobody votes for any more is
	// not an opinion to print.
	want := []string{"alternative rock", "art rock", "electronic"}
	if got := catalogue.Artist.Genres; !sameStrings(got, want) {
		t.Errorf("artist genres = %v, want %v", got, want)
	}
	if len(catalogue.ReleaseGroups) != 1 {
		t.Fatalf("release groups = %d, want 1", len(catalogue.ReleaseGroups))
	}
	wantGroup := []string{"alternative rock", "art rock"}
	if got := catalogue.ReleaseGroups[0].Genres; !sameStrings(got, wantGroup) {
		t.Errorf("release group genres = %v, want %v", got, wantGroup)
	}
}

// An artist MusicBrainz holds no votes for comes back with an empty list rather
// than nothing at all. That is the answer "asked, and there are none", and the
// store keeps it apart from "nobody has asked".
func TestArtistCatalogueGenresAreEmptyWhenNobodyVoted(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/artist/") {
			_, _ = response.Write([]byte(
				`{"id": "a74b1b7f-71a5-4011-9441-d0b5e4122711",
				  "name": "Marram", "sort-name": "Marram"}`))
			return
		}
		_, _ = response.Write([]byte(`{"release-group-count": 0, "release-groups": []}`))
	})

	catalogue, err := client.ArtistCatalogue(context.Background(),
		mustUUID("a74b1b7f-71a5-4011-9441-d0b5e4122711"))
	if err != nil {
		t.Fatalf("ArtistCatalogue() error = %v", err)
	}
	if catalogue.Artist.Genres == nil {
		t.Error("genres = nil, want an empty list")
	}
	if len(catalogue.Artist.Genres) != 0 {
		t.Errorf("genres = %v, want none", catalogue.Artist.Genres)
	}
}

// The release-group lookup, which is the other pass that visits an entity
// MusicBrainz holds votes for.
func TestReleaseGroupReadsGenres(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{
			"id": "b1392450-e666-3926-a536-22c65f834433",
			"title": "OK Computer",
			"genres": [
				{"name": "art rock", "count": 6},
				{"name": "alternative rock", "count": 14}
			],
			"artist-credit": [
				{"name": "Radiohead", "joinphrase": "",
				 "artist": {"id": "a74b1b7f-71a5-4011-9441-d0b5e4122711",
				            "name": "Radiohead", "sort-name": "Radiohead"}}
			]
		}`))
	})

	detail, err := client.ReleaseGroup(context.Background(),
		mustUUID("b1392450-e666-3926-a536-22c65f834433"))
	if err != nil {
		t.Fatalf("ReleaseGroup() error = %v", err)
	}
	want := []string{"alternative rock", "art rock"}
	if got := detail.ReleaseGroup.Genres; !sameStrings(got, want) {
		t.Errorf("genres = %v, want %v", got, want)
	}
}

// Only a handful of genres are kept. Anybody can add one, so the long tail of a
// single vote each is where the misspellings and the private jokes live.
func TestTopGenresKeepsOnlyAHandful(t *testing.T) {
	items := []genreItem{
		{Name: "one", Count: 10}, {Name: "two", Count: 9}, {Name: "three", Count: 8},
		{Name: "four", Count: 7}, {Name: "five", Count: 6}, {Name: "six", Count: 5},
	}
	got := topGenres(items)
	want := []string{"one", "two", "three", "four", "five"}
	if !sameStrings(got, want) {
		t.Errorf("topGenres() = %v, want %v", got, want)
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
