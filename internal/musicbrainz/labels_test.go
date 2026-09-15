package musicbrainz

import (
	"context"
	"net/http"
	"testing"
)

func TestSearchLabelsReadsCandidates(t *testing.T) {
	var gotPath, gotQuery, gotLimit string
	client := testClient(t, func(response http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.Path
		gotQuery = request.URL.Query().Get("query")
		gotLimit = request.URL.Query().Get("limit")
		_, _ = response.Write([]byte(`{
			"count": 2,
			"labels": [
				{"id": "46f0f4cd-8aab-4b33-b698-f459faf64190",
				 "name": "Warp Records", "type": "Original Production",
				 "country": "GB", "score": 100,
				 "area": {"name": "United Kingdom"},
				 "disambiguation": "UK electronic label"},
				{"id": "8e9d9b21-1f0f-4a1f-bf7d-1c6c88b8f18d",
				 "name": "Warp", "type": "Imprint", "country": "US", "score": 71,
				 "area": {"name": "United States"}},
				{"id": "not-a-uuid", "name": "Warped", "score": 40}
			]
		}`))
	})

	labels, err := client.SearchLabels(context.Background(), "warp", 10)
	if err != nil {
		t.Fatalf("SearchLabels() error = %v", err)
	}
	if gotPath != "/label/" {
		t.Errorf("path = %q, want /label/", gotPath)
	}
	if gotQuery != "warp" || gotLimit != "10" {
		t.Errorf("query = %q, limit = %q", gotQuery, gotLimit)
	}
	// The third candidate names no label Schall could hold, so it is dropped
	// rather than kept under an ID nothing can be looked up by.
	if len(labels) != 2 {
		t.Fatalf("labels = %d, want 2", len(labels))
	}
	first := labels[0]
	if first.ID != mustUUID("46f0f4cd-8aab-4b33-b698-f459faf64190") {
		t.Errorf("id = %s", first.ID)
	}
	if first.Name != "Warp Records" || first.Type != "Original Production" ||
		first.Country != "GB" || first.Area != "United Kingdom" ||
		first.Disambiguation != "UK electronic label" || first.Score != 100 {
		t.Errorf("label = %#v", first)
	}
}

func TestSearchLabelsRefusesAnEmptyQuery(t *testing.T) {
	client := testClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("MusicBrainz was asked about nothing")
	})
	if _, err := client.SearchLabels(context.Background(), "   ", 10); err == nil {
		t.Fatal("SearchLabels() error = nil, want a refusal")
	}
}

// One album pressed twice is two releases of one release group. The page
// reports both numbers: the releases it read, which is what the next offset is
// counted in, and the release groups that came out of them.
func TestLabelReleasesReducesPressingsToReleaseGroups(t *testing.T) {
	var gotPath, gotLabel, gotInclude, gotOffset string
	client := testClient(t, func(response http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.Path
		gotLabel = request.URL.Query().Get("label")
		gotInclude = request.URL.Query().Get("inc")
		gotOffset = request.URL.Query().Get("offset")
		_, _ = response.Write([]byte(`{
			"release-count": 3,
			"release-offset": 100,
			"releases": [
				{"id": "1a4bd2f1-1c9c-4a5a-8d8e-1c8d9b0a1f11",
				 "title": "Untrue", "date": "2007-11-05", "status": "Official",
				 "artist-credit": [
					{"name": "Burial", "joinphrase": "",
					 "artist": {"id": "1a2b3c4d-1111-2222-3333-444455556666",
					            "name": "Burial", "sort-name": "Burial"}}
				 ],
				 "release-group": {"id": "aa11bb22-cc33-dd44-ee55-ff6677889900",
				                   "title": "Untrue", "first-release-date": "2007-11-05",
				                   "primary-type": "Album", "secondary-types": []}},
				{"id": "2b5ce302-2d0d-4b6b-9e9f-2d9e0c1b2f22",
				 "title": "Untrue", "date": "2008-01-01", "status": "Official",
				 "artist-credit": [
					{"name": "Burial", "joinphrase": "",
					 "artist": {"id": "1a2b3c4d-1111-2222-3333-444455556666",
					            "name": "Burial", "sort-name": "Burial"}}
				 ],
				 "release-group": {"id": "aa11bb22-cc33-dd44-ee55-ff6677889900",
				                   "title": "Untrue", "first-release-date": "2007-11-05",
				                   "primary-type": "Album", "secondary-types": []}},
				{"id": "3c6df413-3e1e-4c7c-af0a-3eaf1d2c3f33",
				 "title": "Kindred", "date": "2012-02-13", "status": "Official",
				 "artist-credit": [],
				 "release-group": {"id": "bb22cc33-dd44-ee55-ff66-778899001122",
				                   "title": "Kindred", "primary-type": "EP",
				                   "secondary-types": []}}
			]
		}`))
	})

	page, err := client.LabelReleases(context.Background(),
		mustUUID("46f0f4cd-8aab-4b33-b698-f459faf64190"), 100, 100)
	if err != nil {
		t.Fatalf("LabelReleases() error = %v", err)
	}
	if gotPath != "/release" || gotLabel != "46f0f4cd-8aab-4b33-b698-f459faf64190" {
		t.Errorf("path = %q, label = %q", gotPath, gotLabel)
	}
	if gotInclude != "release-groups+artist-credits" {
		t.Errorf("inc = %q", gotInclude)
	}
	if gotOffset != "100" {
		t.Errorf("offset = %q, want 100", gotOffset)
	}
	if page.Total != 3 {
		t.Errorf("total = %d, want 3", page.Total)
	}
	if page.Read != 3 {
		t.Errorf("read = %d, want the three releases the page carried", page.Read)
	}
	// Two pressings of Untrue are one release group; Kindred credits nobody
	// Schall could file it under, so it is left out rather than invented.
	if len(page.ReleaseGroups) != 1 {
		t.Fatalf("release groups = %d, want 1", len(page.ReleaseGroups))
	}
	detail := page.ReleaseGroups[0]
	if detail.ReleaseGroup.ID != mustUUID("aa11bb22-cc33-dd44-ee55-ff6677889900") ||
		detail.ReleaseGroup.Title != "Untrue" ||
		detail.ReleaseGroup.FirstReleaseDate != "2007-11-05" ||
		detail.ReleaseGroup.PrimaryType != "Album" {
		t.Errorf("release group = %#v", detail.ReleaseGroup)
	}
	if detail.Artist.Name != "Burial" || detail.Credit != "Burial" {
		t.Errorf("artist = %#v, credit = %q", detail.Artist, detail.Credit)
	}
	if len(detail.ReleaseGroup.Metadata) == 0 {
		t.Error("metadata is empty, want the provider response")
	}
}

func TestLabelReleasesRefusesAnUnusableRequest(t *testing.T) {
	client := testClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("MusicBrainz was asked an unusable question")
	})
	if _, err := client.LabelReleases(context.Background(), mustUUID(
		"46f0f4cd-8aab-4b33-b698-f459faf64190"), -1, 100); err == nil {
		t.Fatal("LabelReleases() error = nil, want a refusal of a negative offset")
	}
	if _, err := client.LabelReleases(context.Background(), mustUUID(
		"46f0f4cd-8aab-4b33-b698-f459faf64190"), 0, 500); err == nil {
		t.Fatal("LabelReleases() error = nil, want a refusal of an oversized page")
	}
}
