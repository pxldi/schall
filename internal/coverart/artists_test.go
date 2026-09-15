package coverart

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/musicbrainz"
)

// fakeRelations stands in for MusicBrainz saying which pictures are of an artist.
type fakeRelations struct {
	urls []musicbrainz.URLRelation
	err  error
}

func (relations fakeRelations) ArtistURLRelations(
	context.Context, uuid.UUID,
) ([]musicbrainz.URLRelation, error) {
	return relations.urls, relations.err
}

// MusicBrainz names the page a picture lives on, not the picture. Commons is
// asked for the file behind the page.
func TestAnArtistIsPicturedByTheRelationMusicBrainzHolds(t *testing.T) {
	var asked string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		asked = request.URL.Path
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write([]byte("a face"))
	}))
	t.Cleanup(server.Close)

	artists := NewArtists(
		fakeRelations{urls: []musicbrainz.URLRelation{
			{Type: "image", Resource: "https://commons.wikimedia.org/wiki/File:Portishead.jpg"},
		}},
		NewClient(Options{BaseURL: server.URL, HTTPClient: server.Client()}),
	)
	// The Commons address is fixed, so the test points the fetch at its own
	// server by rewriting the host through the client's transport.
	artists.images.httpClient = &http.Client{Transport: rewriteHost{to: server.Listener.Addr().String()}}

	artwork, err := artists.Picture(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Picture() error = %v", err)
	}
	if string(artwork.Image) != "a face" || artwork.Source != NameCommons {
		t.Fatalf("artwork = %+v, want the picture, labelled with where it came from", artwork)
	}
	if !strings.Contains(asked, "Special:FilePath/Portishead.jpg") {
		t.Fatalf("asked %q, want the file behind the page", asked)
	}
}

// rewriteHost sends every request to the test server whatever it was addressed
// to, which is how a fixed third-party address is exercised at all.
type rewriteHost struct{ to string }

func (transport rewriteHost) RoundTrip(request *http.Request) (*http.Response, error) {
	request.URL.Scheme, request.URL.Host = "http", transport.to
	return http.DefaultTransport.RoundTrip(request)
}

// A relation pointing at something that is not a Commons file is left alone.
// Fetching an arbitrary address because a database mentioned it is a different
// thing from asking a known service for a file.
func TestARelationThatIsNotACommonsFileIsNotFetched(t *testing.T) {
	asked := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		asked = true
	}))
	t.Cleanup(server.Close)

	artists := NewArtists(
		fakeRelations{urls: []musicbrainz.URLRelation{
			{Type: "image", Resource: "https://example.test/some/picture.jpg"},
		}},
		NewClient(Options{BaseURL: server.URL}),
	)
	artists.images.httpClient = &http.Client{Transport: rewriteHost{to: server.Listener.Addr().String()}}

	if _, err := artists.Picture(context.Background(), uuid.New()); !errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want the relation left alone", err)
	}
	if asked {
		t.Fatal("an address that is not a known service's file was fetched")
	}
}

// An artist MusicBrainz names no picture of has none, which the caller writes
// down and stops asking about.
func TestAnArtistWithNoPictureIsAnAbsence(t *testing.T) {
	artists := NewArtists(fakeRelations{}, NewClient(Options{}))

	if _, err := artists.Picture(context.Background(), uuid.New()); !errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want the absence reported as such", err)
	}
}

// MusicBrainz rarely names a picture outright. It names the artist's Wikidata
// entity, and the picture is that entity's own image statement — two statements
// about one identifier, with nothing guessed between them.
func TestAnArtistIsPicturedThroughTheirWikidataEntity(t *testing.T) {
	var asked []string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		asked = append(asked, request.URL.Path)
		if strings.HasSuffix(request.URL.Path, ".json") {
			_, _ = response.Write([]byte(
				`{"entities":{"Q189991":{"claims":{"P18":[{"mainsnak":{"datavalue":{"value":"Gorillaz.jpg"}}}]}}}}`))
			return
		}
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write([]byte("a face"))
	}))
	t.Cleanup(server.Close)

	artists := NewArtists(
		fakeRelations{urls: []musicbrainz.URLRelation{
			{Type: "wikidata", Resource: "https://www.wikidata.org/wiki/Q189991"},
		}},
		NewClient(Options{}),
	)
	artists.images.httpClient = &http.Client{Transport: rewriteHost{to: server.Listener.Addr().String()}}

	artwork, err := artists.Picture(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Picture() error = %v", err)
	}
	if string(artwork.Image) != "a face" || artwork.Source != NameCommons {
		t.Fatalf("artwork = %+v, want the picture Wikidata named", artwork)
	}
	if len(asked) != 2 || !strings.Contains(asked[1], "Special:FilePath/Gorillaz.jpg") {
		t.Fatalf("asked %v, want the entity and then the file it named", asked)
	}
}

// A link to somewhere that is not one of the two services followed is not
// followed, whatever a database says about it.
func TestALinkThatIsNotWikidataOrCommonsIsNotFollowed(t *testing.T) {
	asked := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		asked = true
	}))
	t.Cleanup(server.Close)

	artists := NewArtists(
		fakeRelations{urls: []musicbrainz.URLRelation{
			{Type: "wikidata", Resource: "https://example.test/wiki/Q1"},
			{Type: "official homepage", Resource: "https://example.test/"},
		}},
		NewClient(Options{}),
	)
	artists.images.httpClient = &http.Client{Transport: rewriteHost{to: server.Listener.Addr().String()}}

	if _, err := artists.Picture(context.Background(), uuid.New()); !errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want the links left alone", err)
	}
	if asked {
		t.Fatal("an address outside the services followed was fetched")
	}
}

// Wikimedia refuses a request that does not say who is asking, with a 403 that
// reads like a permissions problem and is really a politeness one. Every request
// from here says.
func TestEveryPictureRequestSaysWhoIsAsking(t *testing.T) {
	var agents []string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		agents = append(agents, request.Header.Get("User-Agent"))
		if strings.HasSuffix(request.URL.Path, ".json") {
			_, _ = response.Write([]byte(
				`{"entities":{"Q1":{"claims":{"P18":[{"mainsnak":{"datavalue":{"value":"A.jpg"}}}]}}}}`))
			return
		}
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write([]byte("a face"))
	}))
	t.Cleanup(server.Close)

	artists := NewArtists(
		fakeRelations{urls: []musicbrainz.URLRelation{
			{Type: "wikidata", Resource: "https://www.wikidata.org/wiki/Q1"},
		}},
		NewClient(Options{UserAgent: "Schall/0.1.0 (https://example.test)"}),
	)
	artists.images.httpClient = &http.Client{Transport: rewriteHost{to: server.Listener.Addr().String()}}

	if _, err := artists.Picture(context.Background(), uuid.New()); err != nil {
		t.Fatalf("Picture() error = %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("made %d requests, want the entity and the file", len(agents))
	}
	for _, agent := range agents {
		if !strings.HasPrefix(agent, "Schall/") {
			t.Fatalf("user agent = %q, want every request to say who is asking", agent)
		}
	}
}

// An entity carries claims of every kind and their values are strings, objects
// and numbers by turns. Reading them all as one shape fails on the first
// statement that is not an image.
func TestAnEntityWithClaimsOfEveryShapeIsStillRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, ".json") {
			_, _ = response.Write([]byte(`{"entities":{"Q1":{"claims":{
				"P569":[{"mainsnak":{"datavalue":{"value":{"time":"+1998-00-00T00:00:00Z"}}}}],
				"P18":[{"mainsnak":{"datavalue":{"value":"A.jpg"}}}],
				"P2044":[{"mainsnak":{"datavalue":{"value":{"amount":"+12"}}}}]
			}}}}`))
			return
		}
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write([]byte("a face"))
	}))
	t.Cleanup(server.Close)

	artists := NewArtists(
		fakeRelations{urls: []musicbrainz.URLRelation{
			{Type: "wikidata", Resource: "https://www.wikidata.org/wiki/Q1"},
		}},
		NewClient(Options{}),
	)
	artists.images.httpClient = &http.Client{Transport: rewriteHost{to: server.Listener.Addr().String()}}

	artwork, err := artists.Picture(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Picture() error = %v", err)
	}
	if string(artwork.Image) != "a face" {
		t.Fatalf("artwork = %+v, want the image statement read past the others", artwork)
	}
}

// The encyclopaedia route only pictures artists an encyclopaedia has heard of.
// MusicBrainz links an underground producer's streaming page all the same, and
// that page has a picture of them.
func TestAnArtistWithNoEncyclopaediaEntryIsPicturedByTheirStreamingPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/artist/") {
			_, _ = response.Write([]byte(
				`{"id":63560202,"picture_big":"https://cdn.test/images/artist/abc/500x500.jpg"}`))
			return
		}
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write([]byte("a face"))
	}))
	t.Cleanup(server.Close)

	artists := NewArtists(
		fakeRelations{urls: []musicbrainz.URLRelation{
			{Type: "free streaming", Resource: "https://www.deezer.com/artist/63560202"},
		}},
		NewClient(Options{}),
	)
	artists.images.httpClient = &http.Client{Transport: rewriteHost{to: server.Listener.Addr().String()}}

	artwork, err := artists.Picture(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Picture() error = %v", err)
	}
	if string(artwork.Image) != "a face" || artwork.Source != NameDeezer {
		t.Fatalf("artwork = %+v, want the picture the streaming page shows", artwork)
	}
}

// A streaming page with no picture of the artist says so by naming a picture with
// no picture in it, and serves a grey silhouette at that address rather than
// refusing. A silhouette cached forever is worse than no face at all.
func TestAStreamingPageWithNoPictureIsAnAbsence(t *testing.T) {
	fetched := false
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/artist/") {
			_, _ = response.Write([]byte(
				`{"id":1,"picture_big":"https://cdn.test/images/artist//500x500.jpg"}`))
			return
		}
		fetched = true
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write([]byte("a silhouette"))
	}))
	t.Cleanup(server.Close)

	artists := NewArtists(
		fakeRelations{urls: []musicbrainz.URLRelation{
			{Type: "free streaming", Resource: "https://www.deezer.com/artist/1"},
		}},
		NewClient(Options{}),
	)
	artists.images.httpClient = &http.Client{Transport: rewriteHost{to: server.Listener.Addr().String()}}

	if _, err := artists.Picture(context.Background(), uuid.New()); !errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want the absence reported as such", err)
	}
	if fetched {
		t.Fatal("the placeholder was fetched and would have been cached as a face")
	}
}

// The encyclopaedia is asked first: it says who somebody is, where a streaming
// service says what its page shows.
func TestTheEncyclopaediaIsPreferredToTheStreamingPage(t *testing.T) {
	var asked []string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		asked = append(asked, request.URL.Path)
		if strings.HasSuffix(request.URL.Path, ".json") {
			_, _ = response.Write([]byte(
				`{"entities":{"Q1":{"claims":{"P18":[{"mainsnak":{"datavalue":{"value":"A.jpg"}}}]}}}}`))
			return
		}
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write([]byte("a face"))
	}))
	t.Cleanup(server.Close)

	artists := NewArtists(
		fakeRelations{urls: []musicbrainz.URLRelation{
			{Type: "free streaming", Resource: "https://www.deezer.com/artist/1"},
			{Type: "wikidata", Resource: "https://www.wikidata.org/wiki/Q1"},
		}},
		NewClient(Options{}),
	)
	artists.images.httpClient = &http.Client{Transport: rewriteHost{to: server.Listener.Addr().String()}}

	artwork, err := artists.Picture(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Picture() error = %v", err)
	}
	if artwork.Source != NameCommons {
		t.Fatalf("source = %q, want the encyclopaedia asked first", artwork.Source)
	}
	for _, path := range asked {
		if strings.HasPrefix(path, "/artist/") {
			t.Fatal("the streaming page was asked although the encyclopaedia answered")
		}
	}
}

// picturedBy builds an Artists whose every third-party address goes to one test
// server, for the relations given.
func picturedBy(
	t *testing.T, relations []musicbrainz.URLRelation, handler http.HandlerFunc,
) *Artists {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	artists := NewArtists(fakeRelations{urls: relations}, NewClient(Options{}))
	artists.images.httpClient = &http.Client{Transport: rewriteHost{to: server.Listener.Addr().String()}}
	return artists
}

// An artist nothing has resolved yet has no identifier to ask by, and Schall
// does not search for one.
func TestAnArtistWithNoIdentifierIsNotSearchedFor(t *testing.T) {
	artists := picturedBy(t, nil, func(http.ResponseWriter, *http.Request) {
		t.Error("a service was asked about an artist with no identifier")
	})

	if _, err := artists.Picture(context.Background(), uuid.Nil); !errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want the absence reported as such", err)
	}
}

// The chain starts at what MusicBrainz says the artist is linked to. A provider
// that would not say is a failure, never an artist with no picture.
func TestAProviderThatWouldNotSayWhatAnArtistIsLinkedToIsReported(t *testing.T) {
	artists := NewArtists(
		fakeRelations{err: errors.New("MusicBrainz is not answering")},
		NewClient(Options{}),
	)

	_, err := artists.Picture(context.Background(), uuid.New())

	if err == nil || errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want a failure that is not an absence", err)
	}
}

// A Commons file page MusicBrainz named that Commons then would not serve is an
// outage, and writing it down as "this artist has no picture" would be permanent.
func TestACommonsFileThatCouldNotBeFetchedIsNotAnAbsence(t *testing.T) {
	artists := picturedBy(t, []musicbrainz.URLRelation{
		{Type: "image", Resource: "https://commons.wikimedia.org/wiki/File:Portishead.jpg"},
	}, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusInternalServerError)
	})

	_, err := artists.Picture(context.Background(), uuid.New())

	if err == nil || errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want a failure that is not an absence", err)
	}
}

// The same for the encyclopaedia: an entity nobody could read says nothing about
// whether the artist has a picture.
func TestAWikidataEntityThatCouldNotBeReadIsNotAnAbsence(t *testing.T) {
	for name, respond := range map[string]http.HandlerFunc{
		"the service refused": func(response http.ResponseWriter, _ *http.Request) {
			response.WriteHeader(http.StatusInternalServerError)
		},
		"the entity was not JSON": func(response http.ResponseWriter, _ *http.Request) {
			_, _ = response.Write([]byte("<html>an outage page</html>"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			artists := picturedBy(t, []musicbrainz.URLRelation{
				{Type: "wikidata", Resource: "https://www.wikidata.org/wiki/Q1"},
			}, respond)

			_, err := artists.Picture(context.Background(), uuid.New())

			if err == nil || errors.Is(err, ErrNoArtwork) {
				t.Fatalf("error = %v, want a failure that is not an absence", err)
			}
		})
	}
}

// The encyclopaedia only pictures artists it has heard of, which is not most of
// a collection like this one. An entity it cannot picture is passed over and the
// streaming page is asked instead.
func TestAnEntityTheEncyclopaediaCannotPictureFallsThroughToTheStreamingPage(t *testing.T) {
	for name, entity := range map[string]string{
		"no such entity":                         "",
		"no picture statement":                   `{"entities":{"Q1":{"claims":{}}}}`,
		"a picture statement that names nothing": `{"entities":{"Q1":{"claims":{"P18":[{"mainsnak":{"datavalue":{"value":"  "}}}]}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			artists := picturedBy(t, []musicbrainz.URLRelation{
				{Type: "wikidata", Resource: "https://www.wikidata.org/wiki/Q1"},
				{Type: "free streaming", Resource: "https://www.deezer.com/artist/1"},
			}, func(response http.ResponseWriter, request *http.Request) {
				switch {
				case strings.HasSuffix(request.URL.Path, ".json"):
					if entity == "" {
						response.WriteHeader(http.StatusNotFound)
						return
					}
					_, _ = response.Write([]byte(entity))
				case strings.HasPrefix(request.URL.Path, "/artist/"):
					_, _ = response.Write([]byte(
						`{"id":1,"picture_big":"https://cdn.test/a/500x500.jpg"}`))
				default:
					response.Header().Set("Content-Type", "image/jpeg")
					_, _ = response.Write([]byte("a face"))
				}
			})

			artwork, err := artists.Picture(context.Background(), uuid.New())
			if err != nil {
				t.Fatalf("Picture() error = %v", err)
			}
			if artwork.Source != NameDeezer {
				t.Fatalf("source = %q, want the streaming page asked instead", artwork.Source)
			}
		})
	}
}

// The picture Wikidata named that Commons then had no copy of is not a picture,
// so it is passed over rather than leaving the artist with a failure.
func TestAPictureCommonsHasNoCopyOfIsPassedOver(t *testing.T) {
	artists := picturedBy(t, []musicbrainz.URLRelation{
		{Type: "wikidata", Resource: "https://www.wikidata.org/wiki/Q1"},
	}, func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, ".json") {
			_, _ = response.Write([]byte(
				`{"entities":{"Q1":{"claims":{"P18":[{"mainsnak":{"datavalue":{"value":"A.jpg"}}}]}}}}`))
			return
		}
		response.WriteHeader(http.StatusNotFound)
	})

	if _, err := artists.Picture(context.Background(), uuid.New()); !errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want the absence reported as such", err)
	}
}

// A picture Commons would not serve at all is an outage, and it stops the chain
// rather than being written down as an artist with no face.
func TestAPictureCommonsWouldNotServeIsNotAnAbsence(t *testing.T) {
	artists := picturedBy(t, []musicbrainz.URLRelation{
		{Type: "wikidata", Resource: "https://www.wikidata.org/wiki/Q1"},
	}, func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, ".json") {
			_, _ = response.Write([]byte(
				`{"entities":{"Q1":{"claims":{"P18":[{"mainsnak":{"datavalue":{"value":"A.jpg"}}}]}}}}`))
			return
		}
		response.WriteHeader(http.StatusInternalServerError)
	})

	_, err := artists.Picture(context.Background(), uuid.New())

	if err == nil || errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want a failure that is not an absence", err)
	}
}

// A streaming page nobody could read says nothing about whether the artist has a
// picture there.
func TestAStreamingPageThatCouldNotBeReadIsNotAnAbsence(t *testing.T) {
	for name, respond := range map[string]http.HandlerFunc{
		"the service refused": func(response http.ResponseWriter, _ *http.Request) {
			response.WriteHeader(http.StatusInternalServerError)
		},
		"the page was not JSON": func(response http.ResponseWriter, _ *http.Request) {
			_, _ = response.Write([]byte("<html>an outage page</html>"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			artists := picturedBy(t, []musicbrainz.URLRelation{
				{Type: "free streaming", Resource: "https://www.deezer.com/artist/1"},
			}, respond)

			_, err := artists.Picture(context.Background(), uuid.New())

			if err == nil || errors.Is(err, ErrNoArtwork) {
				t.Fatalf("error = %v, want a failure that is not an absence", err)
			}
		})
	}
}

// A streaming service that is not there at all says nothing about the artist
// either.
func TestAStreamingServiceThatCouldNotBeReachedIsNotAnAbsence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	address := server.Listener.Addr().String()
	server.Close()
	artists := NewArtists(fakeRelations{urls: []musicbrainz.URLRelation{
		{Type: "free streaming", Resource: "https://www.deezer.com/artist/1"},
	}}, NewClient(Options{}))
	artists.images.httpClient = &http.Client{Transport: rewriteHost{to: address}}

	_, err := artists.Picture(context.Background(), uuid.New())

	if err == nil || errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want a failure that is not an absence", err)
	}
}

// A streaming service that has no such artist, or says so in the body of a 200,
// is an absence rather than a failure.
func TestAStreamingServiceWithNoSuchArtistIsAnAbsence(t *testing.T) {
	for name, respond := range map[string]http.HandlerFunc{
		"no such page": func(response http.ResponseWriter, _ *http.Request) {
			response.WriteHeader(http.StatusNotFound)
		},
		"an error in the body of a 200": func(response http.ResponseWriter, _ *http.Request) {
			_, _ = response.Write([]byte(`{"error":{"type":"DataException"}}`))
		},
		"a page naming no picture": func(response http.ResponseWriter, _ *http.Request) {
			_, _ = response.Write([]byte(`{"id":1,"picture_big":"  "}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			artists := picturedBy(t, []musicbrainz.URLRelation{
				{Type: "free streaming", Resource: "https://www.deezer.com/artist/1"},
			}, respond)

			if _, err := artists.Picture(context.Background(), uuid.New()); !errors.Is(err, ErrNoArtwork) {
				t.Fatalf("error = %v, want the absence reported as such", err)
			}
		})
	}
}

// The picture the streaming page named that the image host would not serve is an
// outage, and it stops the chain rather than becoming an absence.
func TestAStreamingPicturesHostThatWouldNotServeItIsNotAnAbsence(t *testing.T) {
	artists := picturedBy(t, []musicbrainz.URLRelation{
		{Type: "free streaming", Resource: "https://www.deezer.com/artist/1"},
	}, func(response http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/artist/") {
			_, _ = response.Write([]byte(`{"id":1,"picture_big":"https://cdn.test/images/artist/abc/500x500.jpg"}`))
			return
		}
		response.WriteHeader(http.StatusInternalServerError)
	})

	_, err := artists.Picture(context.Background(), uuid.New())

	if err == nil || errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want a failure that is not an absence", err)
	}
}

// A picture the image host has no copy of is an absence, and the chain runs out
// rather than failing.
func TestAStreamingPictureNobodyHasACopyOfIsAnAbsence(t *testing.T) {
	artists := picturedBy(t, []musicbrainz.URLRelation{
		{Type: "free streaming", Resource: "https://www.deezer.com/artist/1"},
	}, func(response http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/artist/") {
			_, _ = response.Write([]byte(`{"id":1,"picture_big":"https://cdn.test/images/artist/abc/500x500.jpg"}`))
			return
		}
		response.WriteHeader(http.StatusNotFound)
	})

	if _, err := artists.Picture(context.Background(), uuid.New()); !errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want the absence reported as such", err)
	}
}

// Fetching an arbitrary address because a database mentioned it is a different
// thing from asking a known service for a file. Only addresses that are one of
// the services followed, in the shape that service uses, are followed at all.
func TestOnlyAddressesInTheShapeOfAKnownServiceAreFollowed(t *testing.T) {
	asked := false
	artists := picturedBy(t, []musicbrainz.URLRelation{
		// Not a Commons file page.
		{Type: "image", Resource: "https://commons.wikimedia.org/wiki/Category:Portishead"},
		// Wikidata addresses that name no entity.
		{Type: "wikidata", Resource: "https://www.wikidata.org/wiki/Portishead"},
		{Type: "wikidata", Resource: "https://www.wikidata.org/wiki/Q"},
		{Type: "wikidata", Resource: "https://www.wikidata.org/wiki/Q12b4"},
		// Streaming addresses that name no artist.
		{Type: "free streaming", Resource: "https://www.deezer.com/album/1"},
		{Type: "free streaming", Resource: "https://www.deezer.com/artist"},
		{Type: "free streaming", Resource: "https://www.deezer.com/en/artist/"},
		{Type: "free streaming", Resource: "https://www.deezer.com/artist/abc"},
		// Apple addresses that name no artist: one release, the store itself, and
		// an artist segment with no number under it.
		{Type: "streaming", Resource: "https://music.apple.com/us/album/anaesthesia/1665306505"},
		{Type: "streaming", Resource: "https://music.apple.com/"},
		{Type: "streaming", Resource: "https://music.apple.com/us/artist/"},
		{Type: "purchase for download", Resource: "https://itunes.apple.com/jp/artist/idazalea"},
		// Pages that are one release or one track rather than the artist, and the
		// sites themselves rather than anybody's page on them.
		{Type: "bandcamp", Resource: "https://indecorum.bandcamp.com/album/anaesthesia"},
		{Type: "bandcamp", Resource: "https://bandcamp.com/"},
		{Type: "bandcamp", Resource: "https://www.bandcamp.com/"},
		{Type: "soundcloud", Resource: "https://soundcloud.com/indecorum1989/sets/demos"},
		{Type: "soundcloud", Resource: "https://soundcloud.com/"},
	}, func(http.ResponseWriter, *http.Request) {
		asked = true
	})

	if _, err := artists.Picture(context.Background(), uuid.New()); !errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want every address left alone", err)
	}
	if asked {
		t.Fatal("an address that is not a known service's file was fetched")
	}
}

// A streaming page under a language prefix is the same page.
func TestAStreamingPageUnderALanguagePrefixIsStillFollowed(t *testing.T) {
	artists := picturedBy(t, []musicbrainz.URLRelation{
		{Type: "free streaming", Resource: "https://www.deezer.com/en/artist/63560202"},
	}, func(response http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/artist/") {
			_, _ = response.Write([]byte(`{"id":63560202,"picture_big":"https://cdn.test/a/500x500.jpg"}`))
			return
		}
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write([]byte("a face"))
	})

	artwork, err := artists.Picture(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Picture() error = %v", err)
	}
	if artwork.Source != NameDeezer {
		t.Fatalf("artwork = %+v, want the picture the streaming page shows", artwork)
	}
}

// An entity carries claims whose values are strings, objects and numbers by
// turns. A picture statement that is not a file name is passed over rather than
// failing the read.
func TestAPictureStatementThatIsNotAFileNameIsPassedOver(t *testing.T) {
	artists := picturedBy(t, []musicbrainz.URLRelation{
		{Type: "wikidata", Resource: "https://www.wikidata.org/wiki/Q1"},
	}, func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, ".json") {
			_, _ = response.Write([]byte(`{"entities":{"Q1":{"claims":{"P18":[
				{"mainsnak":{"datavalue":{"value":{"id":"Q42"}}}},
				{"mainsnak":{"datavalue":{"value":"A.jpg"}}}
			]}}}}`))
			return
		}
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write([]byte("a face"))
	})

	artwork, err := artists.Picture(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Picture() error = %v", err)
	}
	if string(artwork.Image) != "a face" {
		t.Fatalf("artwork = %+v, want the statement that names a file read", artwork)
	}
}

// Commons holds photographs at whatever was uploaded, and a name needs a
// thumbnail beside it rather than six megabytes.
func TestAnArtistPictureIsAskedForAtAReadableSize(t *testing.T) {
	var asked string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, ".json") {
			_, _ = response.Write([]byte(
				`{"entities":{"Q1":{"claims":{"P18":[{"mainsnak":{"datavalue":{"value":"A.jpg"}}}]}}}}`))
			return
		}
		asked = request.URL.String()
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write([]byte("a face"))
	}))
	t.Cleanup(server.Close)

	artists := NewArtists(
		fakeRelations{urls: []musicbrainz.URLRelation{
			{Type: "wikidata", Resource: "https://www.wikidata.org/wiki/Q1"},
		}},
		NewClient(Options{}),
	)
	artists.images.httpClient = &http.Client{Transport: rewriteHost{to: server.Listener.Addr().String()}}

	if _, err := artists.Picture(context.Background(), uuid.New()); err != nil {
		t.Fatalf("Picture() error = %v", err)
	}
	if !strings.Contains(asked, "width=") {
		t.Fatalf("asked %q, want a rendition rather than the original", asked)
	}
}

// Deezer builds a picture address out of a hash of the image, and an artist it
// has no picture of gets the hash of nothing. The address is a real one and
// serves a grey silhouette, so recognising it is the only thing between a
// silhouette and a face on the page forever.
func TestAStreamingPictureBuiltFromNoImageIsAnAbsence(t *testing.T) {
	// Built the way Deezer builds it rather than copied, so the constant behind
	// the guard is checked against the hash itself.
	blank := fmt.Sprintf(
		"https://cdn-images.dzcdn.net/images/artist/%x/500x500-000000-80-0-0.jpg", md5.Sum(nil))
	fetched := false
	artists := picturedBy(t, []musicbrainz.URLRelation{
		{Type: "free streaming", Resource: "https://www.deezer.com/artist/1"},
	}, func(response http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/artist/") {
			_, _ = fmt.Fprintf(response, `{"id":1,"picture_big":%q}`, blank)
			return
		}
		fetched = true
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write([]byte("a silhouette"))
	})

	if _, err := artists.Picture(context.Background(), uuid.New()); !errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want the absence reported as such", err)
	}
	if fetched {
		t.Fatal("the placeholder was fetched and would have been cached as a face")
	}
}

// A streaming service has a page only for artists it carries. The ones it never
// carried put their music on Bandcamp, which MusicBrainz links, and that page
// shows a picture of them.
func TestAnArtistNoStreamingServiceCarriesIsPicturedByTheirBandcampPage(t *testing.T) {
	artists := picturedBy(t, []musicbrainz.URLRelation{
		{Type: "bandcamp", Resource: "https://indecorum.bandcamp.com/"},
	}, func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/" {
			_, _ = response.Write([]byte(
				`<html><head><meta property="og:image" content="https://f4.bcbits.com/img/0036237913_23.jpg" >`))
			return
		}
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write([]byte("a face"))
	})

	artwork, err := artists.Picture(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Picture() error = %v", err)
	}
	if string(artwork.Image) != "a face" || artwork.Source != NameBandcamp {
		t.Fatalf("artwork = %+v, want the picture the artist's own page declares", artwork)
	}
}

// The same for the other page these artists keep.
func TestAnArtistIsPicturedByTheirSoundCloudPage(t *testing.T) {
	artists := picturedBy(t, []musicbrainz.URLRelation{
		{Type: "soundcloud", Resource: "https://soundcloud.com/indecorum1989"},
	}, func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/indecorum1989" {
			_, _ = response.Write([]byte(
				`<html><head><meta property="og:image" content="https://i1.sndcdn.com/avatars-t500x500.jpg">`))
			return
		}
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write([]byte("a face"))
	})

	artwork, err := artists.Picture(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Picture() error = %v", err)
	}
	if string(artwork.Image) != "a face" || artwork.Source != NameSoundCloud {
		t.Fatalf("artwork = %+v, want the picture the artist's own page declares", artwork)
	}
}

// A page says more about its picture than where it is: SoundCloud writes the
// picture's width and height beside it. Only the tag that names the picture is
// the picture.
func TestATagAboutThePictureIsNotReadAsThePicture(t *testing.T) {
	artists := picturedBy(t, []musicbrainz.URLRelation{
		{Type: "soundcloud", Resource: "https://soundcloud.com/indecorum1989"},
	}, func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/indecorum1989" {
			_, _ = response.Write([]byte(`<html><head>
				<meta property="og:image:width" content="500">
				<meta property="og:image" content="https://i1.sndcdn.com/avatars-t500x500.jpg">`))
			return
		}
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write([]byte("a face"))
	})

	artwork, err := artists.Picture(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Picture() error = %v", err)
	}
	if string(artwork.Image) != "a face" {
		t.Fatalf("artwork = %+v, want the tag that names the picture read", artwork)
	}
}

// The address a page declares is written for a browser, which reads an ampersand
// written as an escape as an ampersand.
func TestAPictureAddressWrittenWithAnEscapeIsReadAsTheAddressItIs(t *testing.T) {
	var asked string
	artists := picturedBy(t, []musicbrainz.URLRelation{
		{Type: "bandcamp", Resource: "https://indecorum.bandcamp.com/"},
	}, func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/" {
			_, _ = response.Write([]byte(
				`<meta property="og:image" content="https://f4.bcbits.com/img/a.jpg?w=500&amp;h=500">`))
			return
		}
		asked = request.URL.String()
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write([]byte("a face"))
	})

	if _, err := artists.Picture(context.Background(), uuid.New()); err != nil {
		t.Fatalf("Picture() error = %v", err)
	}
	if asked != "/img/a.jpg?w=500&h=500" {
		t.Fatalf("asked %q, want the address the escape stands for", asked)
	}
}

// The artist's own page is believed about which picture is of them, because it is
// their page. It is not believed about where Schall should go: a picture the
// service is not serving itself is not fetched at all.
func TestAPictureAPageDeclaresElsewhereIsNotFetched(t *testing.T) {
	for name, declared := range map[string]string{
		"another site entirely": "https://example.test/whatever.jpg",
		"a relative address":    "/img/a.jpg",
		"not an address at all": "data:image/png;base64,AAAA",
	} {
		t.Run(name, func(t *testing.T) {
			fetched := false
			artists := picturedBy(t, []musicbrainz.URLRelation{
				{Type: "bandcamp", Resource: "https://indecorum.bandcamp.com/"},
			}, func(response http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/" {
					_, _ = fmt.Fprintf(response, `<meta property="og:image" content=%q>`, declared)
					return
				}
				fetched = true
				response.Header().Set("Content-Type", "image/jpeg")
				_, _ = response.Write([]byte("something else"))
			})

			if _, err := artists.Picture(context.Background(), uuid.New()); !errors.Is(err, ErrNoArtwork) {
				t.Fatalf("error = %v, want the declared address left alone", err)
			}
			if fetched {
				t.Fatal("an address the service was not serving was fetched")
			}
		})
	}
}

// A page that declares no picture is an artist with no picture there, which the
// caller writes down and stops asking about.
func TestAPageThatDeclaresNoPictureIsAnAbsence(t *testing.T) {
	artists := picturedBy(t, []musicbrainz.URLRelation{
		{Type: "bandcamp", Resource: "https://indecorum.bandcamp.com/"},
	}, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`<html><head><title>Indecorum</title></head></html>`))
	})

	if _, err := artists.Picture(context.Background(), uuid.New()); !errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want the absence reported as such", err)
	}
}

// A page nobody could read says nothing about whether the artist has a picture on
// it, and writing that down as "no picture" would be permanent.
func TestAPageThatCouldNotBeReadIsNotAnAbsence(t *testing.T) {
	artists := picturedBy(t, []musicbrainz.URLRelation{
		{Type: "bandcamp", Resource: "https://indecorum.bandcamp.com/"},
	}, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusInternalServerError)
	})

	_, err := artists.Picture(context.Background(), uuid.New())

	if err == nil || errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want a failure that is not an absence", err)
	}
}

// A page that is gone is an artist with no picture there rather than an outage.
func TestAPageThatIsGoneIsAnAbsence(t *testing.T) {
	artists := picturedBy(t, []musicbrainz.URLRelation{
		{Type: "bandcamp", Resource: "https://indecorum.bandcamp.com/"},
	}, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNotFound)
	})

	if _, err := artists.Picture(context.Background(), uuid.New()); !errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want the absence reported as such", err)
	}
}

// A service that will not serve whoever is asking has answered, and the answer is
// that this page cannot say. Bandcamp does exactly that to a datacentre address —
// the deployed instance got 403 from every page a home connection got 200 from —
// and a failure there stopped the chain before SoundCloud, which does answer, was
// ever reached.
func TestAPageThatRefusesUsLetsTheNextPageAnswer(t *testing.T) {
	artists := picturedBy(t, []musicbrainz.URLRelation{
		{Type: "bandcamp", Resource: "https://indecorum.bandcamp.com/"},
		{Type: "soundcloud", Resource: "https://soundcloud.com/indecorum1989"},
	}, func(response http.ResponseWriter, request *http.Request) {
		if request.Host == "indecorum.bandcamp.com" {
			response.WriteHeader(http.StatusForbidden)
			return
		}
		if request.URL.Path == "/indecorum1989" {
			_, _ = response.Write([]byte(
				`<html><head><meta property="og:image" content="https://i1.sndcdn.com/avatars-t500x500.jpg">`))
			return
		}
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write([]byte("a face"))
	})

	artwork, err := artists.Picture(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Picture() error = %v", err)
	}
	if string(artwork.Image) != "a face" || artwork.Source != NameSoundCloud {
		t.Fatalf("artwork = %+v, want the page that would answer asked", artwork)
	}
}

// And where every page refuses, the artist has no picture rather than the sweep
// having a failure to retry: the refusal will read the same however often it is
// asked, so recording it is what stops it being asked again.
func TestAnArtistNoPageWillServeUsIsAnAbsence(t *testing.T) {
	artists := picturedBy(t, []musicbrainz.URLRelation{
		{Type: "bandcamp", Resource: "https://indecorum.bandcamp.com/"},
	}, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusForbidden)
	})

	if _, err := artists.Picture(context.Background(), uuid.New()); !errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want the refusal recorded as no picture", err)
	}
}

// Being told to slow down is the one refusal that is about us rather than about
// the artist, so it stays a failure: an absence recorded here would make our own
// haste permanent.
func TestAPageThatAsksUsToSlowDownIsNotAnAbsence(t *testing.T) {
	artists := picturedBy(t, []musicbrainz.URLRelation{
		{Type: "bandcamp", Resource: "https://indecorum.bandcamp.com/"},
	}, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusTooManyRequests)
	})

	_, err := artists.Picture(context.Background(), uuid.New())

	if err == nil || errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want a failure that is not an absence", err)
	}
}

// The streaming service is asked before the artist's own page: it answers with a
// picture of the artist, where a page answers with whatever it shows.
func TestTheStreamingPageIsPreferredToTheArtistsOwnPage(t *testing.T) {
	artists := picturedBy(t, []musicbrainz.URLRelation{
		{Type: "bandcamp", Resource: "https://indecorum.bandcamp.com/"},
		{Type: "free streaming", Resource: "https://www.deezer.com/artist/1"},
	}, func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/":
			t.Error("the artist's own page was asked although the streaming service answered")
		case strings.HasPrefix(request.URL.Path, "/artist/"):
			_, _ = response.Write([]byte(`{"id":1,"picture_big":"https://cdn.test/a/500x500.jpg"}`))
		default:
			response.Header().Set("Content-Type", "image/jpeg")
			_, _ = response.Write([]byte("a face"))
		}
	})

	artwork, err := artists.Picture(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Picture() error = %v", err)
	}
	if artwork.Source != NameDeezer {
		t.Fatalf("source = %q, want the streaming service asked first", artwork.Source)
	}
}

// The other streaming service MusicBrainz links by identifier. An artist no
// encyclopaedia has heard of and Deezer never carried still has a page here, and
// that page declares a picture of them.
func TestAnArtistOnlyAppleCarriesIsPicturedByTheirAppleMusicPage(t *testing.T) {
	artists := picturedBy(t, []musicbrainz.URLRelation{
		{Type: "streaming", Resource: "https://music.apple.com/us/artist/1665306505"},
	}, func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/us/artist/1665306505" {
			_, _ = response.Write([]byte(
				`<html><head><meta property="og:image" content="https://is1-ssl.mzstatic.com/image/thumb/AMCArtistImages126/v4/c0/63/a0/file_cropped.png/1200x630cw.png">`))
			return
		}
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write([]byte("a face"))
	})

	artwork, err := artists.Picture(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Picture() error = %v", err)
	}
	if string(artwork.Image) != "a face" || artwork.Source != NameAppleMusic {
		t.Fatalf("artwork = %+v, want the picture the Apple page declares", artwork)
	}
}

// MusicBrainz holds some of these links on the store's older host, filed under
// buying the music rather than streaming it. It is the same artist number written
// the way that store wrote them.
func TestAnArtistLinkedOnTheOlderAppleStoreIsStillPictured(t *testing.T) {
	asked := ""
	artists := picturedBy(t, []musicbrainz.URLRelation{
		{Type: "purchase for download", Resource: "https://itunes.apple.com/jp/artist/id1113388221"},
	}, func(response http.ResponseWriter, request *http.Request) {
		if strings.Contains(request.URL.Path, "/artist/") {
			asked = request.URL.Path
			_, _ = response.Write([]byte(
				`<html><head><meta property="og:image" content="https://is1-ssl.mzstatic.com/image/thumb/abc/1200x630cw.png">`))
			return
		}
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write([]byte("a face"))
	})

	artwork, err := artists.Picture(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Picture() error = %v", err)
	}
	if string(artwork.Image) != "a face" {
		t.Fatalf("artwork = %+v, want the older link followed to a picture", artwork)
	}
	if asked != "/jp/artist/1113388221" {
		t.Fatalf("asked %q, want the artist that number names in the store they were linked in", asked)
	}
}

// What an Apple page declares is the wide crop it wants shown beside a link. A
// face beside a name is square, and the size is written into the address.
func TestAnApplePictureIsAskedForAsASquare(t *testing.T) {
	asked := ""
	artists := picturedBy(t, []musicbrainz.URLRelation{
		{Type: "streaming", Resource: "https://music.apple.com/us/artist/1665306505"},
	}, func(response http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/us/artist/") {
			_, _ = response.Write([]byte(
				`<meta property="og:image" content="https://is1-ssl.mzstatic.com/image/thumb/abc/file_cropped.png/1200x630cw.png">`))
			return
		}
		asked = request.URL.Path
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write([]byte("a face"))
	})

	if _, err := artists.Picture(context.Background(), uuid.New()); err != nil {
		t.Fatalf("Picture() error = %v", err)
	}
	if asked != "/image/thumb/abc/file_cropped.png/600x600bb.jpg" {
		t.Fatalf("asked %q, want the same picture at the shape a face is shown in", asked)
	}
}

// And an address that does not carry a size is fetched exactly as declared:
// rewriting one Apple has not shown us it serves would be fetching something
// nobody said was there.
func TestAnApplePictureWithNoSizeInItsAddressIsFetchedAsDeclared(t *testing.T) {
	asked := ""
	artists := picturedBy(t, []musicbrainz.URLRelation{
		{Type: "streaming", Resource: "https://music.apple.com/us/artist/1665306505"},
	}, func(response http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/us/artist/") {
			_, _ = response.Write([]byte(
				`<meta property="og:image" content="https://is1-ssl.mzstatic.com/image/thumb/abc/source">`))
			return
		}
		asked = request.URL.Path
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write([]byte("a face"))
	})

	if _, err := artists.Picture(context.Background(), uuid.New()); err != nil {
		t.Fatalf("Picture() error = %v", err)
	}
	if asked != "/image/thumb/abc/source" {
		t.Fatalf("asked %q, want the address the page declared", asked)
	}
}

// Apple has a page per storefront and MusicBrainz keeps every one it is told
// about, so the same artist arrives two or three times over. Those are one page
// declaring one picture, and asking again once it has answered is only rudeness.
func TestTheSameArtistInEveryStorefrontIsAskedAboutOnce(t *testing.T) {
	asked := []string{}
	artists := picturedBy(t, []musicbrainz.URLRelation{
		{Type: "streaming", Resource: "https://music.apple.com/us/artist/1665306505"},
		{Type: "streaming", Resource: "https://music.apple.com/au/artist/1665306505"},
	}, func(response http.ResponseWriter, request *http.Request) {
		asked = append(asked, request.URL.Path)
		_, _ = response.Write([]byte(`<html><head><title>a page declaring nothing</title>`))
	})

	if _, err := artists.Picture(context.Background(), uuid.New()); !errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want the absence reported as such", err)
	}
	if len(asked) != 1 {
		t.Fatalf("asked %v, want one page for the artist linked twice", asked)
	}
}

// The streaming service's page is asked before the page the artist keeps for
// themself: it declares an artist image, where a Bandcamp page declares whatever
// that artist last put out.
func TestTheAppleMusicPageIsPreferredToTheArtistsOwnPage(t *testing.T) {
	artists := picturedBy(t, []musicbrainz.URLRelation{
		{Type: "bandcamp", Resource: "https://indecorum.bandcamp.com/"},
		{Type: "streaming", Resource: "https://music.apple.com/us/artist/1665306505"},
	}, func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/":
			t.Error("the artist's own page was asked although Apple answered")
		case strings.HasPrefix(request.URL.Path, "/us/artist/"):
			_, _ = response.Write([]byte(
				`<meta property="og:image" content="https://is1-ssl.mzstatic.com/image/thumb/abc/1200x630cw.png">`))
		default:
			response.Header().Set("Content-Type", "image/jpeg")
			_, _ = response.Write([]byte("a face"))
		}
	})

	artwork, err := artists.Picture(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Picture() error = %v", err)
	}
	if artwork.Source != NameAppleMusic {
		t.Fatalf("source = %q, want the streaming service asked first", artwork.Source)
	}
}

// Apple may yet answer this cluster the way Bandcamp does, refusing the request
// rather than the artist. That is an answer about us and not about them, so the
// chain carries on to the page below rather than stopping.
func TestAnAppleMusicPageThatRefusesUsLetsTheNextPageAnswer(t *testing.T) {
	artists := picturedBy(t, []musicbrainz.URLRelation{
		{Type: "streaming", Resource: "https://music.apple.com/us/artist/1665306505"},
		{Type: "soundcloud", Resource: "https://soundcloud.com/indecorum1989"},
	}, func(response http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasPrefix(request.URL.Path, "/us/artist/"):
			response.WriteHeader(http.StatusForbidden)
		case request.URL.Path == "/indecorum1989":
			_, _ = response.Write([]byte(
				`<html><head><meta property="og:image" content="https://i1.sndcdn.com/avatars-t500x500.jpg">`))
		default:
			response.Header().Set("Content-Type", "image/jpeg")
			_, _ = response.Write([]byte("a face"))
		}
	})

	artwork, err := artists.Picture(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Picture() error = %v", err)
	}
	if artwork.Source != NameSoundCloud {
		t.Fatalf("source = %q, want the page that would answer asked", artwork.Source)
	}
}
