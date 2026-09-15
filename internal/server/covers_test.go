package server

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/coverart"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

// picture is a real image, because everything on this path checks that the
// bytes decode. A file that merely says image/jpeg is refused, which is the
// point of several of the tests below.
func picture(t *testing.T) []byte {
	t.Helper()
	drawing := image.NewRGBA(image.Rect(0, 0, 8, 8))
	drawing.Set(0, 0, color.RGBA{R: 255, A: 255})
	var bytesOut bytes.Buffer
	if err := png.Encode(&bytesOut, drawing); err != nil {
		t.Fatalf("encode a test picture: %v", err)
	}
	return bytesOut.Bytes()
}

// coverUpload is the request a browser makes when somebody chooses a file.
func coverUpload(t *testing.T, albumID uuid.UUID, name string, image []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("image", name)
	if err != nil {
		t.Fatalf("build the upload: %v", err)
	}
	if _, err := part.Write(image); err != nil {
		t.Fatalf("write the upload: %v", err)
	}
	if err := form.Close(); err != nil {
		t.Fatalf("close the upload: %v", err)
	}
	request := httptest.NewRequest(
		http.MethodPut, "/api/v1/albums/"+albumID.String()+"/cover", &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	return request
}

// fakePictures is what fetches the picture at an address somebody typed.
type fakePictures struct {
	artwork coverart.Artwork
	err     error
	asked   []string
}

func (pictures *fakePictures) FromURL(
	_ context.Context, address string,
) (coverart.Artwork, error) {
	pictures.asked = append(pictures.asked, address)
	return pictures.artwork, pictures.err
}

// A release with no cover anywhere is an ordinary thing — the archives answer
// about the release they were asked about, and some records have never been
// photographed by anybody who uploaded it. The person has the sleeve, so they
// hand it over, and what is written down says they did.
func TestSettingACoverFromAFileKeepsItAsTheUsersOwn(t *testing.T) {
	store := &fakeStore{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	albumID := uuid.New()
	image := picture(t)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, coverUpload(t, albumID, "sleeve.png", image))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	if len(store.savedCovers) != 1 {
		t.Fatalf("saved %d covers, want 1", len(store.savedCovers))
	}
	saved := store.savedCovers[0]
	if saved.AlbumID != albumID {
		t.Fatalf("saved for album %s, want %s", saved.AlbumID, albumID)
	}
	if saved.Source != coverart.NameUser {
		t.Fatalf("source = %q, want %q", saved.Source, coverart.NameUser)
	}
	if !bytes.Equal(saved.Image, image) {
		t.Fatalf("the bytes written are not the bytes sent")
	}
	// The type comes from the bytes, never from the name of the file: the
	// extension written beside the music is chosen from it.
	if saved.ContentType != "image/png" {
		t.Fatalf("content type = %q, want image/png", saved.ContentType)
	}
}

// The phone app seeks by asking for a slice of the cover, and the answer has to
// carry only that slice and say so with a 206 and a Content-Range.
func TestACachedCoverAnswersARangeRequest(t *testing.T) {
	store := &fakeStore{cachedCover: db.ReleaseCoverArtRow{
		Image: []byte("0123456789 more bytes"), ContentType: "image/jpeg", Source: coverart.Name,
	}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/albums/"+uuid.New().String()+"/cover", nil)
	request.Header.Set("Range", "bytes=0-9")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", response.Code)
	}
	if response.Body.String() != "0123456789" {
		t.Fatalf("body = %q, want the first ten bytes", response.Body.String())
	}
	if got := response.Header().Get("Content-Range"); got != "bytes 0-9/21" {
		t.Errorf("content-range = %q", got)
	}
}

// A phone that already holds this cover sends back the ETag it was given, and
// gets a 304 rather than downloading the picture again.
func TestACachedCoverAnswersIfNoneMatchWithNotModified(t *testing.T) {
	store := &fakeStore{cachedCover: db.ReleaseCoverArtRow{
		Image: []byte("a picture"), ContentType: "image/jpeg", Source: coverart.Name,
	}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	albumID := uuid.New().String()

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(
		http.MethodGet, "/api/v1/albums/"+albumID+"/cover", nil))
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on the first response")
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/albums/"+albumID+"/cover", nil)
	request.Header.Set("If-None-Match", etag)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", response.Code)
	}
	if response.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty on a 304", response.Body.String())
	}
}

// A full request without either header still gets the whole picture, exactly as
// before this route learned Range and If-None-Match.
func TestACachedCoverWithNoConditionalHeadersIsServedInFull(t *testing.T) {
	store := &fakeStore{cachedCover: db.ReleaseCoverArtRow{
		Image: []byte("a picture"), ContentType: "image/jpeg", Source: coverart.Name,
	}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/albums/"+uuid.New().String()+"/cover", nil))

	if response.Code != http.StatusOK || response.Body.String() != "a picture" {
		t.Fatalf("status = %d body = %q, want the whole picture", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Errorf("content type = %q", got)
	}
}

func TestACachedCoverMissIsCachedByTheBrowser(t *testing.T) {
	store := &fakeStore{cachedCoverErr: pgx.ErrNoRows}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithCoverArt(&fakeArchive{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/albums/"+uuid.NewString()+"/cover?cached=1", nil))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
	if got := response.Header().Get("Cache-Control"); got != "public, max-age=3600" {
		t.Fatalf("cache-control = %q, want one-day cache", got)
	}
}

func TestAnUpstreamCoverErrorIsNotCachedByTheBrowser(t *testing.T) {
	store := &fakeStore{cachedCoverErr: pgx.ErrNoRows}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithCoverArt(&fakeArchive{err: errors.New("archive unavailable")}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/albums/"+uuid.NewString()+"/cover", nil))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
	if got := response.Header().Get("Cache-Control"); got != "" {
		t.Fatalf("cache-control = %q, want no cache header", got)
	}
}

// The name of the file says nothing. A PNG called sleeve.jpg is a PNG, and it
// is stored as one, because a picture with the wrong extension is a picture
// some players will not open.
func TestSettingACoverReadsTheTypeFromTheBytes(t *testing.T) {
	store := &fakeStore{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, coverUpload(t, uuid.New(), "sleeve.jpg", picture(t)))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	if store.savedCovers[0].ContentType != "image/png" {
		t.Fatalf("content type = %q, want image/png", store.savedCovers[0].ContentType)
	}
}

// A page saved as .jpg is the failure this guards against. Left unchecked it
// becomes a row saying the release has its picture, holding bytes nothing can
// open, that no sweep will ever replace.
func TestAFileThatIsNotAPictureIsRefusedAndNothingIsWritten(t *testing.T) {
	store := &fakeStore{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response,
		coverUpload(t, uuid.New(), "sleeve.jpg", []byte("<html>not a picture</html>")))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
	if len(store.savedCovers) != 0 {
		t.Fatalf("a refused cover wrote %d rows, want 0", len(store.savedCovers))
	}
}

// The bound is the archive's bound. A file past it is refused as it arrives,
// and the release is left exactly as it was.
func TestAnImageOverTheBoundIsRefused(t *testing.T) {
	store := &fakeStore{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	oversize := bytes.Repeat([]byte("x"), coverart.MaxImageBytes+(2<<20))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, coverUpload(t, uuid.New(), "huge.png", oversize))

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", response.Code, response.Body.String())
	}
	if len(store.savedCovers) != 0 {
		t.Fatalf("a refused cover wrote %d rows, want 0", len(store.savedCovers))
	}
}

// The other way in: an address, which Schall fetches itself. The browser never
// loads the picture, so the same size bound and the same reading of what came
// back apply as they do to the archive.
func TestSettingACoverFromAnAddressFetchesItAndKeepsIt(t *testing.T) {
	image := picture(t)
	archive := httptest.NewServer(http.HandlerFunc(
		func(response http.ResponseWriter, _ *http.Request) {
			response.Header().Set("Content-Type", "image/png")
			_, _ = response.Write(image)
		}))
	defer archive.Close()

	store := &fakeStore{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithCoverPictures(coverart.NewClient(coverart.Options{
			BaseURL: archive.URL, AllowLocalPictureAddresses: true,
		})))
	albumID := uuid.New()

	request := httptest.NewRequest(http.MethodPut,
		"/api/v1/albums/"+albumID.String()+"/cover",
		strings.NewReader(`{"url":"`+archive.URL+`/sleeve.png"}`))
	request.Header.Set("Content-Type", "application/json")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	if len(store.savedCovers) != 1 {
		t.Fatalf("saved %d covers, want 1", len(store.savedCovers))
	}
	if store.savedCovers[0].Source != coverart.NameUser {
		t.Fatalf("source = %q, want %q", store.savedCovers[0].Source, coverart.NameUser)
	}
	if !bytes.Equal(store.savedCovers[0].Image, image) {
		t.Fatalf("the bytes written are not the bytes fetched")
	}
}

// An address answering with a page is the same failure as a file that is not a
// picture, and it ends the same way: refused, and nothing written down.
func TestAnAddressThatAnswersWithAPageIsRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(response http.ResponseWriter, _ *http.Request) {
			response.Header().Set("Content-Type", "text/html")
			_, _ = response.Write([]byte("<html>not a picture</html>"))
		}))
	defer server.Close()

	store := &fakeStore{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithCoverPictures(coverart.NewClient(coverart.Options{AllowLocalPictureAddresses: true})))

	request := httptest.NewRequest(http.MethodPut,
		"/api/v1/albums/"+uuid.NewString()+"/cover",
		strings.NewReader(`{"url":"`+server.URL+`/page"}`))
	request.Header.Set("Content-Type", "application/json")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", response.Code, response.Body.String())
	}
	if len(store.savedCovers) != 0 {
		t.Fatalf("a refused cover wrote %d rows, want 0", len(store.savedCovers))
	}
}

// Only http and https are fetched at all. Anything else is refused, and the
// release is left as it was.
func TestACoverAddressThatIsNotAWebAddressIsRefused(t *testing.T) {
	store := &fakeStore{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithCoverPictures(coverart.NewClient(coverart.Options{})))

	request := httptest.NewRequest(http.MethodPut,
		"/api/v1/albums/"+uuid.NewString()+"/cover",
		strings.NewReader(`{"url":"file:///etc/passwd"}`))
	request.Header.Set("Content-Type", "application/json")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
	if len(store.savedCovers) != 0 {
		t.Fatalf("a refused cover wrote %d rows, want 0", len(store.savedCovers))
	}
}

// The bound is the bound wherever the picture came from. Reading up to it and
// stopping would keep half a picture as somebody's permanent cover.
func TestAPictureAtAnAddressOverTheBoundIsRefused(t *testing.T) {
	oversize := bytes.Repeat([]byte("x"), coverart.MaxImageBytes+1024)
	server := httptest.NewServer(http.HandlerFunc(
		func(response http.ResponseWriter, _ *http.Request) {
			response.Header().Set("Content-Type", "image/png")
			_, _ = response.Write(oversize)
		}))
	defer server.Close()

	store := &fakeStore{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithCoverPictures(coverart.NewClient(coverart.Options{
			AllowLocalPictureAddresses: true,
		})))

	request := httptest.NewRequest(http.MethodPut,
		"/api/v1/albums/"+uuid.NewString()+"/cover",
		strings.NewReader(`{"url":"`+server.URL+`/huge.png"}`))
	request.Header.Set("Content-Type", "application/json")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", response.Code, response.Body.String())
	}
	if len(store.savedCovers) != 0 {
		t.Fatalf("a refused cover wrote %d rows, want 0", len(store.savedCovers))
	}
}

// An address Schall refuses to reach reads exactly like an address with no
// picture at it. Any difference between the two answers — a status, a word, a
// pause — is a report on which machines and ports exist on the network Schall
// runs inside, and a person asking one address after another would have a map
// of it.
func TestARefusedAddressAnswersTheSameAsAnAddressWithNoImage(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(
		func(response http.ResponseWriter, _ *http.Request) {
			response.Header().Set("Content-Type", "text/html")
			_, _ = response.Write([]byte("<html>not a picture</html>"))
		}))
	defer page.Close()

	answer := func(pictures CoverPictureFetcher, url string) (int, string) {
		handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{},
			zerolog.Nop(), WithCoverPictures(pictures))
		request := httptest.NewRequest(http.MethodPut,
			"/api/v1/albums/"+uuid.NewString()+"/cover",
			strings.NewReader(`{"url":"`+url+`"}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code, response.Body.String()
	}

	// The same address twice: once through a client that may reach this
	// machine, once through the guarded one every installation runs.
	openStatus, openBody := answer(coverart.NewClient(coverart.Options{
		AllowLocalPictureAddresses: true,
	}), page.URL+"/page")
	guardedStatus, guardedBody := answer(
		coverart.NewClient(coverart.Options{}), page.URL+"/page")

	if guardedStatus != openStatus || guardedBody != openBody {
		t.Fatalf("a refused address answered %d %s, want the same as %d %s",
			guardedStatus, guardedBody, openStatus, openBody)
	}
}

// A server that could not be reached is not the person's mistake, and the
// answer says so rather than blaming what they typed.
func TestAnAddressThatCouldNotBeReachedReportsTheAddress(t *testing.T) {
	store := &fakeStore{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithCoverPictures(&fakePictures{err: errors.New("the host is unreachable")}))

	request := httptest.NewRequest(http.MethodPut,
		"/api/v1/albums/"+uuid.NewString()+"/cover",
		strings.NewReader(`{"url":"https://example.invalid/sleeve.png"}`))
	request.Header.Set("Content-Type", "application/json")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", response.Code)
	}
	if len(store.savedCovers) != 0 {
		t.Fatalf("a refused cover wrote %d rows, want 0", len(store.savedCovers))
	}
}

// Taking a cover back leaves the release for the archives to be asked about
// again, which is what the sweep does with a release that has no row.
func TestRemovingACoverAsksTheStoreToTakeBackTheUsersOwn(t *testing.T) {
	store := &fakeStore{removedCoverRows: 1}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	albumID := uuid.New()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodDelete, "/api/v1/albums/"+albumID.String()+"/cover", nil))

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", response.Code)
	}
	if len(store.removedCovers) != 1 || store.removedCovers[0] != albumID {
		t.Fatalf("removed %v, want one row for %s", store.removedCovers, albumID)
	}
}

// A release with no cover of its own to take back is not a failure: the
// outcome asked for is the outcome, and pressing twice costs nothing.
func TestRemovingACoverThatIsNotThereIsNotAFailure(t *testing.T) {
	store := &fakeStore{removedCoverRows: 0}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodDelete, "/api/v1/albums/"+uuid.NewString()+"/cover", nil))

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", response.Code)
	}
}

// The archive is asked about a release the catalogue holds, and so is this: a
// cover for a release that is not there is a cover for nothing.
func TestSettingACoverForAReleaseThatIsNotThereIsNotFound(t *testing.T) {
	store := &fakeStore{err: pgx.ErrNoRows}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, coverUpload(t, uuid.New(), "sleeve.png", picture(t)))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
	if len(store.savedCovers) != 0 {
		t.Fatalf("a refused cover wrote %d rows, want 0", len(store.savedCovers))
	}
}
