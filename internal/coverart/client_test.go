package coverart

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// aPicture is a real one, however small. What a source answers with is only
// taken as a picture when it decodes as one, so a test about which source is
// believed has to hand out pictures rather than words. The width tells two of
// them apart.
func aPicture(width int) []byte {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, width, 1))); err != nil {
		panic(err)
	}
	return encoded.Bytes()
}

func nullable(id string) uuid.NullUUID {
	return uuid.NullUUID{UUID: uuid.MustParse(id), Valid: true}
}

const (
	releaseID = "205ff413-6fbe-48d2-bd64-940cc37f5d75"
	groupID   = "b9ad642e-b012-41c7-b72a-42cf4911f9ff"
)

func TestTheFrontCoverOfAReleaseIsFetchedByItsIdentifier(t *testing.T) {
	var asked string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		asked = request.URL.Path
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write([]byte("a picture"))
	}))
	t.Cleanup(server.Close)

	artwork, err := NewClient(Options{BaseURL: server.URL}).
		Front(context.Background(), nullable(releaseID), nullable(groupID))
	if err != nil {
		t.Fatalf("Front() error = %v", err)
	}
	if string(artwork.Image) != "a picture" || artwork.ContentType != "image/jpeg" {
		t.Fatalf("artwork = %+v, want the picture the archive returned", artwork)
	}
	if asked != "/release/"+releaseID+"/front-500" {
		t.Fatalf("asked %q, want the release Schall already resolved", asked)
	}
}

// The edition somebody settled on often has no picture of its own while the work
// does. It is the same work, so it is not a second guess.
func TestAReleaseWithNoPictureFallsBackToItsReleaseGroup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/release/") {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		response.Header().Set("Content-Type", "image/png")
		_, _ = response.Write([]byte("the work's picture"))
	}))
	t.Cleanup(server.Close)

	artwork, err := NewClient(Options{BaseURL: server.URL}).
		Front(context.Background(), nullable(releaseID), nullable(groupID))
	if err != nil {
		t.Fatalf("Front() error = %v", err)
	}
	if string(artwork.Image) != "the work's picture" {
		t.Fatalf("artwork = %+v, want the release group's picture", artwork)
	}
}

// Nothing having a picture is an answer about the music, which a caller may
// remember and stop asking about.
func TestAReleaseNobodyHasPicturedIsAnAbsence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	_, err := NewClient(Options{BaseURL: server.URL}).
		Front(context.Background(), nullable(releaseID), nullable(groupID))
	if !errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want the absence reported as such", err)
	}
}

// An archive having a bad afternoon is not a release without a cover, and saying
// so would let a caller remember an outage forever.
func TestAnArchiveThatFailedIsNotAnAbsence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	_, err := NewClient(Options{BaseURL: server.URL}).
		Front(context.Background(), nullable(releaseID), nullable(groupID))
	if err == nil || errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want a failure that is not an absence", err)
	}
}

// The archive returns 500 for pictures it holds, intermittently, so one 500 is
// not an answer about the release. Asked again, it serves the picture.
func TestAnArchiveThatFailedOnceIsAskedAgain(t *testing.T) {
	var asked int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		asked++
		if asked < 3 {
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write([]byte("a picture"))
	}))
	t.Cleanup(server.Close)

	client := NewClient(Options{BaseURL: server.URL})
	client.betweenAttempts = 0
	artwork, err := client.Front(context.Background(), nullable(releaseID), nullable(groupID))
	if err != nil {
		t.Fatalf("Front() error = %v, want the picture the third ask returned", err)
	}
	if string(artwork.Image) != "a picture" {
		t.Fatalf("artwork = %+v, want the picture", artwork)
	}
	if asked != 3 {
		t.Fatalf("asked %d times, want 3", asked)
	}
}

// Asking again has a limit. An archive that is genuinely down is reported as a
// failure rather than asked about forever, and the release stays unpictured
// rather than being recorded as having no picture.
func TestAnArchiveThatKeepsFailingIsReportedRatherThanAskedForever(t *testing.T) {
	var asked int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		asked++
		response.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	client := NewClient(Options{BaseURL: server.URL})
	client.betweenAttempts = 0
	_, err := client.Front(context.Background(), nullable(releaseID), nullable(groupID))
	if err == nil || errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want a failure that is not an absence", err)
	}
	// Three attempts on the release, and the release group is never reached:
	// a failure is not the absence that would fall through to it.
	if asked != fetchAttempts {
		t.Fatalf("asked %d times, want %d", asked, fetchAttempts)
	}
}

// A release the archive has no picture of is answered on the first reply. There
// is nothing intermittent about a 404, and asking twice would not make it truer
// — it would triple the cost of the common case for a catalogue whose long tail
// mostly has no cover at all.
func TestAReleaseWithNoPictureIsNotAskedAboutAgain(t *testing.T) {
	var asked int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		asked++
		response.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	client := NewClient(Options{BaseURL: server.URL})
	client.betweenAttempts = 0
	_, err := client.Front(context.Background(), nullable(releaseID), uuid.NullUUID{})
	if !errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want an absence", err)
	}
	if asked != 1 {
		t.Fatalf("asked %d times, want 1", asked)
	}
}

// A release nothing has resolved yet is asked about nowhere: there is no
// identifier to ask by, and Schall does not search for one.
func TestAReleaseWithNoIdentifierIsNotSearchedFor(t *testing.T) {
	asked := false
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		asked = true
		response.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	_, err := NewClient(Options{BaseURL: server.URL}).
		Front(context.Background(), uuid.NullUUID{}, uuid.NullUUID{})
	if !errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want the absence reported as such", err)
	}
	if asked {
		t.Fatal("the archive was asked about a release with no identifier")
	}
}

// Something that is not a picture is not a cover, whatever the archive said it
// was serving.
func TestSomethingTheArchiveServedThatIsNotAPictureIsNotACover(t *testing.T) {
	for name, respond := range map[string]http.HandlerFunc{
		"a page where a picture should be": func(response http.ResponseWriter, _ *http.Request) {
			response.Header().Set("Content-Type", "text/html")
			_, _ = response.Write([]byte("<html>an outage page</html>"))
		},
		"a picture of nothing at all": func(response http.ResponseWriter, _ *http.Request) {
			response.Header().Set("Content-Type", "image/jpeg")
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(respond)
			t.Cleanup(server.Close)

			_, err := NewClient(Options{BaseURL: server.URL}).
				Front(context.Background(), nullable(releaseID), uuid.NullUUID{})

			if !errors.Is(err, ErrNoArtwork) {
				t.Fatalf("error = %v, want the bytes refused as no cover", err)
			}
		})
	}
}

// An archive that could not be reached at all is a failure, and never the answer
// "this release has no cover".
func TestAnArchiveThatCouldNotBeReachedIsNotAnAbsence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	address := server.URL
	server.Close()

	_, err := NewClient(Options{BaseURL: address}).
		Front(context.Background(), nullable(releaseID), uuid.NullUUID{})

	if err == nil || errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want a failure that is not an absence", err)
	}
}

// A picture that stopped halfway is not a small picture; it is no picture, and
// storing what arrived would cache a broken image forever.
func TestAPictureThatStoppedHalfwayIsNotStored(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
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
	}))
	t.Cleanup(server.Close)

	_, err := NewClient(Options{BaseURL: server.URL}).
		Front(context.Background(), nullable(releaseID), uuid.NullUUID{})

	if err == nil || errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want half a picture reported rather than stored", err)
	}
}

// An address nobody could turn into a request is a misconfiguration, and it has
// to be reported rather than read as a release with no cover.
func TestAnArchiveAddressNobodyCouldAskIsReported(t *testing.T) {
	_, err := NewClient(Options{BaseURL: "http://archive.test/\x7f"}).
		Front(context.Background(), nullable(releaseID), uuid.NullUUID{})

	if err == nil || errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want the unusable address reported", err)
	}
}

// fakeByName stands in for a name-keyed archive.
type fakeByName struct {
	artwork Artwork
	asked   int
}

func (archive *fakeByName) Cover(context.Context, string, string) (Artwork, error) {
	archive.asked++
	if len(archive.artwork.Image) == 0 {
		return Artwork{}, ErrNoArtwork
	}
	return archive.artwork, nil
}

// The archive that answers about the very release identity resolution settled on
// is believed first, and nothing is asked by name while it has an answer.
func TestAReleaseThePictureArchiveKnowsIsNotLookedUpByName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write(aPicture(1))
	}))
	t.Cleanup(server.Close)
	byName := &fakeByName{artwork: Artwork{Image: aPicture(2), ContentType: "image/jpeg"}}

	artwork, err := Sources{
		Archive: NewClient(Options{BaseURL: server.URL}), ByName: byName,
	}.Front(context.Background(), Release{
		MusicBrainzReleaseID: nullable(releaseID), Artist: "Portishead", Title: "Dummy",
	})
	if err != nil {
		t.Fatalf("Front() error = %v", err)
	}
	if !bytes.Equal(artwork.Image, aPicture(1)) || byName.asked != 0 {
		t.Fatalf("artwork = %q asked by name %d times, want the identified picture alone",
			artwork.Image, byName.asked)
	}
}

// Only a release nobody has pictured by identifier is looked up by name.
func TestAReleaseNobodyHasPicturedIsLookedUpByName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	byName := &fakeByName{artwork: Artwork{
		Image: aPicture(1), ContentType: "image/jpeg", Source: NameITunes,
	}}

	artwork, err := Sources{
		Archive: NewClient(Options{BaseURL: server.URL}), ByName: byName,
	}.Front(context.Background(), Release{
		MusicBrainzReleaseID: nullable(releaseID), Artist: "Portishead", Title: "Dummy",
	})
	if err != nil {
		t.Fatalf("Front() error = %v", err)
	}
	if artwork.Source != NameITunes {
		t.Fatalf("artwork = %+v, want the name-keyed picture, labelled as one", artwork)
	}
}

// An archive that could not be reached is not an archive with no picture. Falling
// through to the guess would swap a certain answer for one nobody asked for, and
// the swap would be invisible ever after.
func TestAnUnreachableArchiveDoesNotFallThroughToTheGuess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	byName := &fakeByName{artwork: Artwork{Image: []byte("a guess"), ContentType: "image/jpeg"}}

	_, err := Sources{
		Archive: NewClient(Options{BaseURL: server.URL}), ByName: byName,
	}.Front(context.Background(), Release{
		MusicBrainzReleaseID: nullable(releaseID), Artist: "Portishead", Title: "Dummy",
	})
	if err == nil || byName.asked != 0 {
		t.Fatalf("error = %v asked by name %d times, want the failure reported", err, byName.asked)
	}
}

// The release's own files failing to be read is not the same as their carrying
// no picture, so the chain stops rather than falling through to the guess.
func TestALibraryThatCouldNotBeReadDoesNotFallThroughToTheGuess(t *testing.T) {
	byName := &fakeByName{artwork: Artwork{Image: []byte("a guess"), ContentType: "image/jpeg"}}

	_, err := Sources{
		FromLibrary: failingLibrary{},
		ByName:      byName,
	}.Front(context.Background(), Release{AlbumID: uuid.New(), Artist: "Portishead", Title: "Dummy"})

	if err == nil || byName.asked != 0 {
		t.Fatalf("error = %v asked by name %d times, want the failure reported", err, byName.asked)
	}
}

// A release nothing at all is configured to answer about has no picture, which
// is an answer rather than a failure.
func TestAReleaseNoSourceCanAnswerAboutIsAnAbsence(t *testing.T) {
	_, err := Sources{}.Front(context.Background(), Release{Artist: "Portishead", Title: "Dummy"})

	if !errors.Is(err, ErrNoArtwork) {
		t.Fatalf("error = %v, want the absence reported as such", err)
	}
}

type failingLibrary struct{}

func (failingLibrary) Cover(context.Context, uuid.UUID) (Artwork, error) {
	return Artwork{}, errors.New("the database is not answering")
}

// The cache holds either a picture or the fact that there is none, and a row
// that names an archive with no bytes under it is neither. It stops the release
// being asked about again and has nothing to show, which is a release left blank
// for good.
func TestAnArchiveNameWithNoPictureIsWrittenDownAsNoPicture(t *testing.T) {
	for _, artwork := range []Artwork{
		{Source: Name},
		{Image: []byte{}, ContentType: "image/jpeg", Source: Name},
		{Image: []byte("an outage page"), ContentType: "image/jpeg", Source: Name},
	} {
		stored := Saved(artwork)
		if len(stored.Image) != 0 || stored.ContentType != "" || stored.Source != NameNobody {
			t.Fatalf("stored = %+v, want the answer that there is no picture", stored)
		}
	}
}

// A picture is kept exactly as the archive sent it.
func TestAPictureIsWrittenDownAsItArrived(t *testing.T) {
	arrived := Artwork{Image: aPicture(3), ContentType: "image/png", Source: Name}

	stored := Saved(arrived)

	if !bytes.Equal(stored.Image, arrived.Image) || stored.ContentType != "image/png" ||
		stored.Source != Name {
		t.Fatalf("stored = %+v, want the picture as it arrived", stored)
	}
}

// Bytes nobody can open are not a picture, whatever the reply called them. The
// source that sent them has answered nothing, so the next source is asked.
func TestBytesNobodyCanOpenAreNotAnAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write([]byte("<html>an outage page</html>"))
	}))
	t.Cleanup(server.Close)
	byName := &fakeByName{artwork: Artwork{
		Image: aPicture(2), ContentType: "image/jpeg", Source: NameITunes,
	}}

	artwork, err := Sources{
		Archive: NewClient(Options{BaseURL: server.URL}), ByName: byName,
	}.Front(context.Background(), Release{
		MusicBrainzReleaseID: nullable(releaseID), Artist: "Portishead", Title: "Dummy",
	})
	if err != nil {
		t.Fatalf("Front() error = %v", err)
	}
	if artwork.Source != NameITunes {
		t.Fatalf("artwork = %+v, want the source that sent a picture", artwork)
	}
}

// The address of a picture somebody sets by hand is the one address Schall
// fetches that Schall did not choose, and Schall runs inside a network the
// person did not choose either: a container beside the router's admin page and
// a cloud host's metadata service. Every one of those is refused.

func TestAPictureAddressOnThisMachineIsRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(response http.ResponseWriter, _ *http.Request) {
			response.Header().Set("Content-Type", "image/png")
			_, _ = response.Write(aPicture(1))
		}))
	defer server.Close()

	_, err := NewClient(Options{}).FromURL(context.Background(), server.URL+"/sleeve.png")
	if !errors.Is(err, ErrAddressRefused) {
		t.Fatalf("FromURL() error = %v, want the address refused", err)
	}
}

// A redirect is where a public address becomes a private one. The guard sits at
// the dial rather than at the address that was typed, so every hop is checked
// as the number it resolved to and the last one is refused like the first.
func TestAPictureAddressThatRedirectsToThisMachineIsRefused(t *testing.T) {
	picture := httptest.NewServer(http.HandlerFunc(
		func(response http.ResponseWriter, _ *http.Request) {
			response.Header().Set("Content-Type", "image/png")
			_, _ = response.Write(aPicture(1))
		}))
	defer picture.Close()

	redirector := httptest.NewServer(http.HandlerFunc(
		func(response http.ResponseWriter, request *http.Request) {
			http.Redirect(response, request, picture.URL+"/sleeve.png", http.StatusFound)
		}))
	defer redirector.Close()

	artwork, err := NewClient(Options{}).
		FromURL(context.Background(), redirector.URL+"/somewhere")
	if !errors.Is(err, ErrAddressRefused) {
		t.Fatalf("FromURL() error = %v, want the address refused", err)
	}
	if len(artwork.Image) != 0 {
		t.Fatalf("a refused address answered with %d bytes", len(artwork.Image))
	}
}

// A chain that never arrives is not a picture being served.
func TestAPictureAddressThatRedirectsForeverIsRefused(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(
		func(response http.ResponseWriter, request *http.Request) {
			http.Redirect(response, request, server.URL+"/again", http.StatusFound)
		}))
	defer server.Close()

	// Allowed to reach this machine, so what refuses it is the chain itself.
	_, err := NewClient(Options{AllowLocalPictureAddresses: true}).
		FromURL(context.Background(), server.URL+"/start")
	if !errors.Is(err, ErrAddressRefused) {
		t.Fatalf("FromURL() error = %v, want the address refused", err)
	}
}

// A literal private address never needs a lookup to be refused: the guard runs
// at the dial, and a literal address is already the number that would be dialed.
func TestALiteralPrivateAddressIsRefusedBeforeDialing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, address := range []string{
		"http://10.0.0.1/cover.jpg",
		"http://[::1]/cover.jpg",
		"http://169.254.169.254/cover.jpg",
	} {
		_, err := NewClient(Options{}).FromURL(ctx, address)
		if !errors.Is(err, ErrAddressRefused) {
			t.Errorf("FromURL(%q) error = %v, want the address refused", address, err)
		}
	}
}

// A name is not an address until it is looked up, and the guard sits at the
// dial precisely so a name that resolves to this machine is refused the same
// as a literal one would be.
func TestADNSNameThatResolvesToThisMachineIsRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(response http.ResponseWriter, _ *http.Request) {
			response.Header().Set("Content-Type", "image/png")
			_, _ = response.Write(aPicture(1))
		}))
	defer server.Close()
	port := server.Listener.Addr().(*net.TCPAddr).Port

	_, err := NewClient(Options{}).
		FromURL(context.Background(), fmt.Sprintf("http://localhost:%d/sleeve.png", port))
	if !errors.Is(err, ErrAddressRefused) {
		t.Fatalf("FromURL() error = %v, want the address refused", err)
	}
}

func TestOnlyAnInternetAddressIsReachable(t *testing.T) {
	for _, endpoint := range []struct {
		address string
		allowed bool
	}{
		{"127.0.0.1:8080", false},
		{"[::1]:8080", false},
		{"0.0.0.0:80", false},
		{"10.0.0.5:80", false},
		{"172.16.4.1:80", false},
		{"192.168.1.1:80", false},
		{"[fc00::1]:80", false},
		{"169.254.169.254:80", false},
		{"[fe80::1]:80", false},
		{"100.64.0.1:80", false},
		{"224.0.0.1:80", false},
		{"93.184.216.34:80", true},
		{"[2606:2800:220:1:248:1893:25c8:1946]:80", true},
	} {
		if reachable(endpoint.address) != endpoint.allowed {
			t.Errorf("reachable(%q) = %v, want %v",
				endpoint.address, !endpoint.allowed, endpoint.allowed)
		}
	}
}

// A picture past the bound is refused rather than cut short. Half a picture
// stored as somebody's permanent cover is worse than no picture: it decodes,
// so nothing ever replaces it.
func TestAPictureAtAnAddressOverTheBoundIsRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(response http.ResponseWriter, _ *http.Request) {
			response.Header().Set("Content-Type", "image/png")
			_, _ = response.Write(bytes.Repeat([]byte("x"), maxImageBytes+1024))
		}))
	defer server.Close()

	_, err := NewClient(Options{AllowLocalPictureAddresses: true}).
		FromURL(context.Background(), server.URL+"/huge.png")
	if !errors.Is(err, ErrPictureTooLarge) {
		t.Fatalf("FromURL() error = %v, want the picture refused as too large", err)
	}
}
