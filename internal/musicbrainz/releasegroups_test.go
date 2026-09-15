package musicbrainz

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

func TestReleaseGroupReadsTitleAndCreditedArtist(t *testing.T) {
	var gotPath, gotInclude string
	client := testClient(t, func(response http.ResponseWriter, request *http.Request) {
		gotPath, gotInclude = request.URL.Path, request.URL.Query().Get("inc")
		_, _ = response.Write([]byte(`{
			"id": "b1392450-e666-3926-a536-22c65f834433",
			"title": "OK Computer",
			"first-release-date": "1997-05-21",
			"primary-type": "Album",
			"secondary-types": [],
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
	if gotPath != "/release-group/b1392450-e666-3926-a536-22c65f834433" {
		t.Errorf("path = %q", gotPath)
	}
	if gotInclude != "artist-credits+genres" {
		t.Errorf("inc = %q, want artist-credits+genres", gotInclude)
	}
	if detail.ReleaseGroup.Title != "OK Computer" || detail.ReleaseGroup.FirstReleaseDate != "1997-05-21" {
		t.Errorf("release group = %#v", detail.ReleaseGroup)
	}
	if detail.Artist.ID != mustUUID("a74b1b7f-71a5-4011-9441-d0b5e4122711") ||
		detail.Artist.Name != "Radiohead" || detail.Artist.SortName != "Radiohead" {
		t.Errorf("artist = %#v", detail.Artist)
	}
	// The metadata is stored as the provider sent it, and the album views read
	// the first release date back out of it.
	if len(detail.ReleaseGroup.Metadata) == 0 {
		t.Error("metadata is empty, want the provider response")
	}
}

// A release group credited to several artists is filed under the first of
// them, but the credit is kept so a collaboration can be named as one.
func TestReleaseGroupKeepsTheFullCredit(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{
			"id": "3f4d1c0a-2f9e-4a4c-8a2b-1c9d0e5f6a7b",
			"title": "Watch the Throne",
			"artist-credit": [
				{"name": "Jay-Z", "joinphrase": " & ",
				 "artist": {"id": "f82bcf78-5b69-4622-a5ef-73800768d9ac",
				            "name": "Jay-Z", "sort-name": "Jay-Z"}},
				{"name": "Kanye West", "joinphrase": "",
				 "artist": {"id": "164f0d73-1234-4e2c-8743-d77bf2191051",
				            "name": "Kanye West", "sort-name": "West, Kanye"}}
			]
		}`))
	})

	detail, err := client.ReleaseGroup(context.Background(),
		mustUUID("3f4d1c0a-2f9e-4a4c-8a2b-1c9d0e5f6a7b"))
	if err != nil {
		t.Fatalf("ReleaseGroup() error = %v", err)
	}
	if detail.Artist.Name != "Jay-Z" {
		t.Errorf("artist = %q, want the first credit", detail.Artist.Name)
	}
	if detail.Credit != "Jay-Z & Kanye West" {
		t.Errorf("credit = %q", detail.Credit)
	}
}

// The credit is kept as the list it was printed from as well, so both artists
// on a collaboration are reachable and not only the one it is filed under.
func TestReleaseGroupKeepsEveryArtistOfTheCredit(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{
			"id": "3f4d1c0a-2f9e-4a4c-8a2b-1c9d0e5f6a7b",
			"title": "Watch the Throne",
			"artist-credit": [
				{"name": "Jay-Z", "joinphrase": " & ",
				 "artist": {"id": "f82bcf78-5b69-4622-a5ef-73800768d9ac",
				            "name": "Jay-Z", "sort-name": "Jay-Z"}},
				{"name": "Kanye West", "joinphrase": "",
				 "artist": {"id": "164f0d73-1234-4e2c-8743-d77bf2191051",
				            "name": "Kanye West", "sort-name": "West, Kanye"}}
			]
		}`))
	})

	detail, err := client.ReleaseGroup(context.Background(),
		mustUUID("3f4d1c0a-2f9e-4a4c-8a2b-1c9d0e5f6a7b"))
	if err != nil {
		t.Fatalf("ReleaseGroup() error = %v", err)
	}
	want := []Credit{
		{Name: "Jay-Z", ArtistID: mustUUID("f82bcf78-5b69-4622-a5ef-73800768d9ac"),
			JoinPhrase: " & ", ArtistName: "Jay-Z"},
		{Name: "Kanye West", ArtistID: mustUUID("164f0d73-1234-4e2c-8743-d77bf2191051"),
			ArtistName: "Kanye West"},
	}
	if len(detail.Credits) != len(want) {
		t.Fatalf("credits = %#v, want both artists", detail.Credits)
	}
	for index, credit := range want {
		if !reflect.DeepEqual(detail.Credits[index], credit) {
			t.Errorf("credit %d = %#v, want %#v", index, detail.Credits[index], credit)
		}
	}
}

// A release group Schall cannot attribute cannot be filed. Inventing an artist
// for it would put music in the catalogue under a name nobody proved.
func TestReleaseGroupWithoutACreditedArtistIsNotFound(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{
			"id": "3f4d1c0a-2f9e-4a4c-8a2b-1c9d0e5f6a7b",
			"title": "Untitled",
			"artist-credit": [{"name": "Unknown", "joinphrase": "", "artist": {"name": "Unknown"}}]
		}`))
	})

	_, err := client.ReleaseGroup(context.Background(),
		mustUUID("3f4d1c0a-2f9e-4a4c-8a2b-1c9d0e5f6a7b"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestReleaseGroupRefusesALookupOnNoIDAtAll(t *testing.T) {
	client := testClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("the provider was asked about no release group at all")
	})

	if _, err := client.ReleaseGroup(context.Background(), uuid.Nil); err == nil {
		t.Fatal("the provider was asked anyway")
	}
}

// A provider that broke is not a release group nobody has heard of, and reading
// it as one would have a caller give up on music that is there.
func TestAReleaseGroupLookupThatFailedIsNotAnAbsence(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusInternalServerError)
	})

	_, err := client.ReleaseGroup(context.Background(),
		mustUUID("3f4d1c0a-2f9e-4a4c-8a2b-1c9d0e5f6a7b"))

	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want a failure that is not an absence", err)
	}
}

// A release group with no title is one nobody could find in their own
// catalogue, so it is no release group at all.
func TestAReleaseGroupWithNoTitleIsNotFound(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{
			"id": "3f4d1c0a-2f9e-4a4c-8a2b-1c9d0e5f6a7b", "title": "   ",
			"artist-credit": [{"name": "Radiohead", "joinphrase": "",
			                   "artist": {"id": "a74b1b7f-71a5-4011-9441-d0b5e4122711",
			                              "name": "Radiohead", "sort-name": "Radiohead"}}]
		}`))
	})

	_, err := client.ReleaseGroup(context.Background(),
		mustUUID("3f4d1c0a-2f9e-4a4c-8a2b-1c9d0e5f6a7b"))

	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

// A credit that identifies itself but names nobody cannot hold a release either,
// so the next credit that does is the one taken.
func TestACreditThatNamesNobodyIsPassedOver(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{
			"id": "3f4d1c0a-2f9e-4a4c-8a2b-1c9d0e5f6a7b", "title": "Kid A",
			"artist-credit": [
				{"name": "", "joinphrase": "",
				 "artist": {"id": "00000000-0000-4000-8000-000000000000", "name": "  "}},
				{"name": "Radiohead", "joinphrase": "",
				 "artist": {"id": "a74b1b7f-71a5-4011-9441-d0b5e4122711", "name": "Radiohead"}}
			]
		}`))
	})

	detail, err := client.ReleaseGroup(context.Background(),
		mustUUID("3f4d1c0a-2f9e-4a4c-8a2b-1c9d0e5f6a7b"))
	if err != nil {
		t.Fatalf("ReleaseGroup() error = %v", err)
	}
	if detail.Artist.ID != mustUUID("a74b1b7f-71a5-4011-9441-d0b5e4122711") {
		t.Errorf("artist = %#v, want the credit that names somebody", detail.Artist)
	}
	// An artist the provider gave no sort name for is still sorted under the name
	// it does have, rather than sorted under nothing.
	if detail.Artist.SortName != "Radiohead" {
		t.Errorf("sort name = %q, want the artist's own name to stand in", detail.Artist.SortName)
	}
}

func TestReleaseGroupNotFound(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNotFound)
	})

	_, err := client.ReleaseGroup(context.Background(),
		mustUUID("3f4d1c0a-2f9e-4a4c-8a2b-1c9d0e5f6a7b"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}
