package coverart

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/pxldi/schall/internal/wikipedia"
)

// fakeEncyclopaedia stands in for Wikipedia answering about one article title.
type fakeEncyclopaedia struct {
	asked   []string
	summary wikipedia.Summary
	err     error
}

func (encyclopaedia *fakeEncyclopaedia) Summary(
	_ context.Context, title string,
) (wikipedia.Summary, error) {
	encyclopaedia.asked = append(encyclopaedia.asked, title)
	return encyclopaedia.summary, encyclopaedia.err
}

// wikidataServing answers as Wikidata's plain-JSON view of one entity, and
// rewrites the fixed Wikidata address onto the test's own server.
func wikidataServing(t *testing.T, body string) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(
		func(response http.ResponseWriter, _ *http.Request) {
			_, _ = response.Write([]byte(body))
		}))
	t.Cleanup(server.Close)
	client := NewClient(Options{BaseURL: server.URL, HTTPClient: server.Client()})
	client.httpClient = &http.Client{Transport: rewriteHost{to: server.Listener.Addr().String()}}
	return client
}

// The whole chain, one statement at a time: MusicBrainz says which Wikidata
// entity this artist is, the entity says which article is about it, and the
// article is where the words come from. No name is sent anywhere.
func TestABiographyIsReachedThroughTheIdentifiers(t *testing.T) {
	encyclopaedia := &fakeEncyclopaedia{summary: wikipedia.Summary{
		Text: "Talk Talk were an English band formed in 1981.",
		URL:  "https://en.wikipedia.org/wiki/Talk_Talk",
	}}
	biographies := NewBiographies(
		fakeRelations{urls: []musicbrainz.URLRelation{
			{Type: "wikidata", Resource: "https://www.wikidata.org/wiki/Q1160145"},
		}},
		wikidataServing(t, `{"entities": {"Q1160145": {
			"sitelinks": {"enwiki": {"title": "Talk Talk"},
			              "dewiki": {"title": "Talk Talk (Band)"}}}}}`),
		encyclopaedia,
	)

	biography, err := biographies.Of(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Of() error = %v", err)
	}
	if len(encyclopaedia.asked) != 1 || encyclopaedia.asked[0] != "Talk Talk" {
		t.Errorf("asked = %v, want the title the entity named", encyclopaedia.asked)
	}
	if biography.Text != "Talk Talk were an English band formed in 1981." {
		t.Errorf("text = %q", biography.Text)
	}
	// The article is part of the answer, because the licence the words come
	// under requires the page that shows them to link it.
	if biography.SourceURL != "https://en.wikipedia.org/wiki/Talk_Talk" {
		t.Errorf("source = %q", biography.SourceURL)
	}
}

// An artist MusicBrainz links no Wikidata entity for is one no encyclopaedia
// has heard of. Nobody is asked by name to fill the silence.
func TestAnArtistWithNoWikidataEntityHasNoBiography(t *testing.T) {
	encyclopaedia := &fakeEncyclopaedia{}
	biographies := NewBiographies(
		fakeRelations{urls: []musicbrainz.URLRelation{
			{Type: "free streaming", Resource: "https://www.deezer.com/artist/123"},
			{Type: "bandcamp", Resource: "https://marram.bandcamp.com/"},
		}},
		NewClient(Options{}),
		encyclopaedia,
	)

	_, err := biographies.Of(context.Background(), uuid.New())
	if !errors.Is(err, ErrNoBiography) {
		t.Fatalf("Of() error = %v, want ErrNoBiography", err)
	}
	if len(encyclopaedia.asked) != 0 {
		t.Errorf("asked %v, want nobody asked at all", encyclopaedia.asked)
	}
}

// An entity Wikidata holds with no English article is an answer about the
// artist, recorded as one.
func TestAnEntityWithNoEnglishArticleHasNoBiography(t *testing.T) {
	encyclopaedia := &fakeEncyclopaedia{}
	biographies := NewBiographies(
		fakeRelations{urls: []musicbrainz.URLRelation{
			{Type: "wikidata", Resource: "https://www.wikidata.org/wiki/Q42"},
		}},
		wikidataServing(t, `{"entities": {"Q42": {
			"sitelinks": {"dewiki": {"title": "Etwas"}}}}}`),
		encyclopaedia,
	)

	_, err := biographies.Of(context.Background(), uuid.New())
	if !errors.Is(err, ErrNoBiography) {
		t.Fatalf("Of() error = %v, want ErrNoBiography", err)
	}
	if len(encyclopaedia.asked) != 0 {
		t.Errorf("asked %v, want nobody asked at all", encyclopaedia.asked)
	}
}

// Wikipedia having no article under the title the entity named is an absence,
// and it reads as one here.
func TestNoArticleUnderTheTitleIsAnAbsence(t *testing.T) {
	biographies := NewBiographies(
		fakeRelations{urls: []musicbrainz.URLRelation{
			{Type: "wikidata", Resource: "https://www.wikidata.org/wiki/Q42"},
		}},
		wikidataServing(t, `{"entities": {"Q42": {
			"sitelinks": {"enwiki": {"title": "Somebody"}}}}}`),
		&fakeEncyclopaedia{err: wikipedia.ErrNoArticle},
	)

	if _, err := biographies.Of(context.Background(), uuid.New()); !errors.Is(err, ErrNoBiography) {
		t.Fatalf("Of() error = %v, want ErrNoBiography", err)
	}
}

// Wikipedia failing is not an absence. It comes back as a failure so the sweep
// writes nothing down and asks again later.
func TestAnOutageIsNotAnAbsence(t *testing.T) {
	failure := errors.New("Wikipedia returned 503 Service Unavailable")
	biographies := NewBiographies(
		fakeRelations{urls: []musicbrainz.URLRelation{
			{Type: "wikidata", Resource: "https://www.wikidata.org/wiki/Q42"},
		}},
		wikidataServing(t, `{"entities": {"Q42": {
			"sitelinks": {"enwiki": {"title": "Somebody"}}}}}`),
		&fakeEncyclopaedia{err: failure},
	)

	_, err := biographies.Of(context.Background(), uuid.New())
	if err == nil || errors.Is(err, ErrNoBiography) {
		t.Fatalf("Of() error = %v, want the failure reported", err)
	}
}

// MusicBrainz itself failing stops the chain, for the same reason.
func TestAMetadataFailureStopsTheChain(t *testing.T) {
	failure := errors.New("MusicBrainz returned 503 Service Unavailable")
	biographies := NewBiographies(
		fakeRelations{err: failure}, NewClient(Options{}), &fakeEncyclopaedia{},
	)

	_, err := biographies.Of(context.Background(), uuid.New())
	if !errors.Is(err, failure) {
		t.Fatalf("Of() error = %v, want the failure reported", err)
	}
}
