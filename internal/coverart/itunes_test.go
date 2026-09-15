package coverart

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// itunesServer answers a search with whatever the test puts in albums, and
// serves any artwork URL it handed out. The albums are set after the server
// exists, because their artwork URLs have to point back at it.
func itunesServer(t *testing.T) (*httptest.Server, *[]itunesAlbum) {
	t.Helper()
	albums := &[]itunesAlbum{}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/artwork") {
			response.Header().Set("Content-Type", "image/jpeg")
			_, _ = response.Write([]byte("a picture of " + request.URL.Query().Get("of")))
			return
		}
		_ = json.NewEncoder(response).Encode(itunesSearchResponse{Results: *albums})
	}))
	t.Cleanup(server.Close)
	return server, albums
}

func artworkURL(server *httptest.Server, of string) string {
	return server.URL + "/artwork/100x100bb.jpg?of=" + of
}

// Punctuation and case differ between catalogues for the same album, and a
// person reading the two names would call them the same.
func TestAnAlbumNamedTheSameIsPicturedByName(t *testing.T) {
	server, albums := itunesServer(t)
	*albums = []itunesAlbum{{
		ArtistName: "Portishead", CollectionName: "Dummy",
		ArtworkURL100: artworkURL(server, "dummy"),
	}}

	artwork, err := NewITunesClient(Options{BaseURL: server.URL}).
		Cover(context.Background(), "portishead", "dummy!")
	if err != nil {
		t.Fatalf("Cover() error = %v", err)
	}
	if artwork.Source != NameITunes || len(artwork.Image) == 0 {
		t.Fatalf("artwork = %+v, want a picture labelled with where it came from", artwork)
	}
}

// A near miss is a different album, and choosing between those is exactly what a
// text search cannot do.
func TestAnAlbumNamedAlmostTheSameIsNotPictured(t *testing.T) {
	server, albums := itunesServer(t)
	*albums = []itunesAlbum{{
		ArtistName: "Portishead", CollectionName: "Dummy (Deluxe Edition)",
		ArtworkURL100: artworkURL(server, "deluxe"),
	}}

	_, err := NewITunesClient(Options{BaseURL: server.URL}).
		Cover(context.Background(), "Portishead", "Dummy")
	if !errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want a near miss refused", err)
	}
}

// Two albums of the same name by the same artist picturing different things is
// an ambiguity, and a cover is cheap to go without.
func TestTwoAlbumsAgreeingOnTheNameArePicturedByNeither(t *testing.T) {
	server, albums := itunesServer(t)
	*albums = []itunesAlbum{
		{ArtistName: "Weezer", CollectionName: "Weezer", ArtworkURL100: artworkURL(server, "blue")},
		{ArtistName: "Weezer", CollectionName: "Weezer", ArtworkURL100: artworkURL(server, "green")},
	}

	_, err := NewITunesClient(Options{BaseURL: server.URL}).
		Cover(context.Background(), "Weezer", "Weezer")
	if !errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want the ambiguity refused rather than guessed at", err)
	}
}

// A client built with no address of its own still knows where the service is.
func TestAnITunesClientBuiltWithNoAddressStillHasOne(t *testing.T) {
	if got := NewITunesClient(Options{}).baseURL; got != defaultITunesBaseURL {
		t.Fatalf("base URL = %q, want the default of %q", got, defaultITunesBaseURL)
	}
}

// An album that agrees on the names but pictures nothing is not an answer, and
// it must not stand in the way of one that does.
func TestAnAlbumPicturingNothingIsPassedOver(t *testing.T) {
	server, albums := itunesServer(t)
	*albums = []itunesAlbum{
		{ArtistName: "Portishead", CollectionName: "Dummy", ArtworkURL100: "  "},
		{ArtistName: "Portishead", CollectionName: "Dummy", ArtworkURL100: artworkURL(server, "dummy")},
	}

	artwork, err := NewITunesClient(Options{BaseURL: server.URL}).
		Cover(context.Background(), "Portishead", "Dummy")
	if err != nil {
		t.Fatalf("Cover() error = %v", err)
	}
	if len(artwork.Image) == 0 {
		t.Fatal("the album that does picture something was not used")
	}
}

// A search nobody could run says nothing about whether this release has a cover,
// so it is a failure rather than an absence a caller would write down forever.
func TestASearchThatCouldNotBeRunIsNotAnAbsence(t *testing.T) {
	for name, respond := range map[string]http.HandlerFunc{
		"the service refused": func(response http.ResponseWriter, _ *http.Request) {
			response.WriteHeader(http.StatusInternalServerError)
		},
		"the answer was not JSON": func(response http.ResponseWriter, _ *http.Request) {
			_, _ = response.Write([]byte("<html>an outage page</html>"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(respond)
			t.Cleanup(server.Close)

			_, err := NewITunesClient(Options{BaseURL: server.URL}).
				Cover(context.Background(), "Portishead", "Dummy")

			if err == nil || errors.Is(err, ErrNoArtwork) {
				t.Fatalf("error = %v, want a failure that is not an absence", err)
			}
		})
	}
}

func TestAServiceThatCouldNotBeReachedIsNotAnAbsence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	address := server.URL
	server.Close()

	_, err := NewITunesClient(Options{BaseURL: address}).
		Cover(context.Background(), "Portishead", "Dummy")

	if err == nil || errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want a failure that is not an absence", err)
	}
}

// The search hands back an address for the artwork, and what happens at that
// address is its own question: no copy there is an absence, a refusal is not.
func TestArtworkTheServiceNamedButDidNotServe(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		respond   http.HandlerFunc
		isAbsence bool
	}{
		{"no copy at the address it named", func(response http.ResponseWriter, _ *http.Request) {
			response.WriteHeader(http.StatusNotFound)
		}, true},
		{"something that is not a picture", func(response http.ResponseWriter, _ *http.Request) {
			response.Header().Set("Content-Type", "text/html")
			_, _ = response.Write([]byte("<html>gone</html>"))
		}, true},
		{"the service refused", func(response http.ResponseWriter, _ *http.Request) {
			response.WriteHeader(http.StatusInternalServerError)
		}, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(
				func(response http.ResponseWriter, request *http.Request) {
					if strings.HasPrefix(request.URL.Path, "/artwork") {
						testCase.respond(response, request)
						return
					}
					_ = json.NewEncoder(response).Encode(itunesSearchResponse{
						Results: []itunesAlbum{{
							ArtistName: "Portishead", CollectionName: "Dummy",
							ArtworkURL100: server.URL + "/artwork/100x100bb.jpg",
						}},
					})
				}))
			t.Cleanup(server.Close)

			_, err := NewITunesClient(Options{BaseURL: server.URL}).
				Cover(context.Background(), "Portishead", "Dummy")

			if testCase.isAbsence && !errors.Is(err, ErrNoArtwork) {
				t.Fatalf("error = %v, want the absence reported as such", err)
			}
			if !testCase.isAbsence && (err == nil || errors.Is(err, ErrNoArtwork)) {
				t.Fatalf("error = %v, want a failure that is not an absence", err)
			}
		})
	}
}

// The artwork address comes from the service, not from Schall. One nobody could
// ask, or nobody could reach, is a failure rather than a release with no cover.
func TestAnArtworkAddressTheServiceGaveThatNobodyCanAskIsReported(t *testing.T) {
	unreachable := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	gone := unreachable.URL
	unreachable.Close()

	for name, artwork := range map[string]string{
		"an address nobody could build a request from": "http://itunes.test/\x7f/100x100bb.jpg",
		"an address nobody could reach":                gone + "/100x100bb.jpg",
	} {
		t.Run(name, func(t *testing.T) {
			server, albums := itunesServer(t)
			*albums = []itunesAlbum{{
				ArtistName: "Portishead", CollectionName: "Dummy", ArtworkURL100: artwork,
			}}

			_, err := NewITunesClient(Options{BaseURL: server.URL}).
				Cover(context.Background(), "Portishead", "Dummy")

			if err == nil || errors.Is(err, ErrNoArtwork) {
				t.Fatalf("error = %v, want a failure that is not an absence", err)
			}
		})
	}
}

// Artwork that stopped halfway is not a small picture; it is no picture, and
// storing what arrived would cache a broken image forever.
func TestArtworkThatStoppedHalfwayIsNotStored(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(
		func(response http.ResponseWriter, request *http.Request) {
			if strings.HasPrefix(request.URL.Path, "/artwork") {
				response.Header().Set("Content-Type", "image/jpeg")
				response.Header().Set("Content-Length", "4096")
				_, _ = response.Write([]byte("the first bytes"))
				if flusher, ok := response.(http.Flusher); ok {
					flusher.Flush()
				}
				if hijacker, ok := response.(http.Hijacker); ok {
					if connection, _, err := hijacker.Hijack(); err == nil {
						_ = connection.Close()
					}
				}
				return
			}
			_ = json.NewEncoder(response).Encode(itunesSearchResponse{
				Results: []itunesAlbum{{
					ArtistName: "Portishead", CollectionName: "Dummy",
					ArtworkURL100: server.URL + "/artwork/100x100bb.jpg",
				}},
			})
		}))
	t.Cleanup(server.Close)

	_, err := NewITunesClient(Options{BaseURL: server.URL}).
		Cover(context.Background(), "Portishead", "Dummy")

	if err == nil || errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want half a picture reported rather than stored", err)
	}
}

// An address nobody could turn into a request is a misconfiguration, and it has
// to be reported rather than read as a release with no cover.
func TestAnITunesAddressNobodyCouldAskIsReported(t *testing.T) {
	_, err := NewITunesClient(Options{BaseURL: "http://itunes.test/\x7f"}).
		Cover(context.Background(), "Portishead", "Dummy")

	if err == nil || errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want the unusable address reported", err)
	}
}

// A release with nothing to search by is not searched for.
func TestAReleaseWithNoNameIsNotSearchedFor(t *testing.T) {
	asked := false
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		asked = true
		_ = json.NewEncoder(response).Encode(itunesSearchResponse{})
	}))
	t.Cleanup(server.Close)

	_, err := NewITunesClient(Options{BaseURL: server.URL}).Cover(context.Background(), "", "Dummy")
	if !errors.Is(err, ErrNoArtwork) || asked {
		t.Fatalf("error = %v asked = %v, want nothing asked about nothing", err, asked)
	}
}
