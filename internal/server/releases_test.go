package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

// The three presses on the Releases browser's selection bar. What is pinned here
// is that a selection is a list of identifiers the server checks — never a
// filter it runs again — and that every answer is per release, because a
// selection of six is six outcomes and not one.

func selectionBody(ids ...uuid.UUID) string {
	body, _ := json.Marshal(releaseSelection{ReleaseIDs: ids})
	return string(body)
}

func postSelection(handler http.Handler, path, body string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return response
}

func TestRematchingASelectionQueuesOnePassPerRelease(t *testing.T) {
	first, second := uuid.New(), uuid.New()
	store := &fakeStore{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := postSelection(handler, "/api/v1/releases/rematch", selectionBody(first, second))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var answer rematchedReleasesResponse
	if err := json.Unmarshal(response.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if len(answer.Releases) != 2 {
		t.Fatalf("answered about %d releases; want one row each", len(answer.Releases))
	}
	for _, release := range answer.Releases {
		if !release.Queued {
			t.Fatalf("release %s was not queued", release.ReleaseID)
		}
	}
	if len(store.refreshQueuedFor) != 2 {
		t.Fatalf("asked the store about %d releases", len(store.refreshQueuedFor))
	}
}

// Pressing again while a pass is running says so rather than doubling the work.
func TestARematchOfAReleaseAlreadyRunningSaysSo(t *testing.T) {
	running := uuid.New()
	store := &fakeStore{albumRefreshAlreadyQueued: map[uuid.UUID]bool{running: true}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := postSelection(handler, "/api/v1/releases/rematch", selectionBody(running))

	var answer rematchedReleasesResponse
	if err := json.Unmarshal(response.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if len(answer.Releases) != 1 || answer.Releases[0].Queued {
		t.Fatalf("answer = %+v; want one release reported as not queued", answer.Releases)
	}
}

// A release the catalogue cannot ask MusicBrainz about is reported apart from
// one whose pass is already running: they read the same and are not the same.
func TestARematchOfAnUnaskableReleaseIsReportedApart(t *testing.T) {
	gone := uuid.New()
	store := &fakeStore{unaskableAlbums: map[uuid.UUID]bool{gone: true}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := postSelection(handler, "/api/v1/releases/rematch", selectionBody(gone))

	var answer rematchedReleasesResponse
	if err := json.Unmarshal(response.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if len(answer.Unaskable) != 1 || answer.Unaskable[0] != gone {
		t.Fatalf("unaskable = %v; want the release that could not be asked about", answer.Unaskable)
	}
	if len(answer.Releases) != 0 {
		t.Fatalf("releases = %+v; want none reported as queued", answer.Releases)
	}
}

// The same release ticked twice is one release. Two refresh passes for one
// release is work nobody asked for.
func TestASelectionCountsEachReleaseOnce(t *testing.T) {
	once := uuid.New()
	store := &fakeStore{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	postSelection(handler, "/api/v1/releases/rematch", selectionBody(once, once, once))

	if len(store.refreshQueuedFor) != 1 {
		t.Fatalf("asked the store %d times about one release", len(store.refreshQueuedFor))
	}
}

func TestAnEmptySelectionIsRefused(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := postSelection(handler, "/api/v1/releases/rematch", `{"releaseIds":[]}`)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// A selection is a list the server checks, so it has to have an end.
func TestASelectionPastTheCapIsRefused(t *testing.T) {
	ids := make([]uuid.UUID, selectionCap+1)
	for index := range ids {
		ids[index] = uuid.New()
	}
	store := &fakeStore{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := postSelection(handler, "/api/v1/releases/rematch", selectionBody(ids...))

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(store.refreshQueuedFor) != 0 {
		t.Fatal("a selection past the cap reached the store")
	}
}

func TestRetryingASelectionAnswersPerRelease(t *testing.T) {
	withFailures, without := uuid.New(), uuid.New()
	failed := []uuid.UUID{uuid.New(), uuid.New()}
	store := &queueStore{
		fakeStore:       &fakeStore{},
		failedByRelease: map[uuid.UUID][]uuid.UUID{withFailures: failed},
	}
	handler := queueHandler(store)

	response := postSelection(handler, "/api/v1/releases/retry", selectionBody(withFailures, without))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var answer retriedReleasesResponse
	if err := json.Unmarshal(response.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if len(answer.Releases) != 2 {
		t.Fatalf("answered about %d releases; want one row each", len(answer.Releases))
	}
	if answer.Releases[0].Retried != 2 {
		t.Fatalf("retried %d jobs for the release that had two", answer.Releases[0].Retried)
	}
	// A release with nothing that failed is still an answer, not an omission.
	if answer.Releases[1].Retried != 0 || answer.Releases[1].ReleaseID != without {
		t.Fatalf("second row = %+v; want the release with nothing to retry", answer.Releases[1])
	}
}

// Only the jobs the selected releases own are put back. The retry is about a
// selection, not about the whole queue.
func TestRetryingASelectionOnlyTouchesItsOwnJobs(t *testing.T) {
	mine := uuid.New()
	failed := []uuid.UUID{uuid.New()}
	store := &queueStore{
		fakeStore:       &fakeStore{},
		failedByRelease: map[uuid.UUID][]uuid.UUID{mine: failed},
	}
	handler := queueHandler(store)

	postSelection(handler, "/api/v1/releases/retry", selectionBody(mine))

	if len(store.askedAboutReleases) != 1 || store.askedAboutReleases[0] != mine {
		t.Fatalf("asked about %v; want only the selected release", store.askedAboutReleases)
	}
	if len(store.requeuedIDs) != 1 || store.requeuedIDs[0] != failed[0] {
		t.Fatalf("requeued %v; want the selected release's failed job", store.requeuedIDs)
	}
}

func TestIgnoringASelectionRecordsTheSameTrackScopedDecisions(t *testing.T) {
	albumID := uuid.New()
	recordingID := uuid.NullUUID{UUID: uuid.New(), Valid: true}
	store := &fakeStore{
		album: db.GetAlbumRow{ID: albumID, Title: "A Release", ArtistName: "An Artist"},
		tracks: []db.ListAlbumTracksRow{
			{ID: uuid.New(), Title: "One", MusicbrainzRecordingID: recordingID},
		},
	}
	wants := &fakeAcquisitionTargets{target: db.AcquisitionTargetRow{ID: uuid.New()}, created: true}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(wants))

	response := postSelection(handler, "/api/v1/releases/ignore", selectionBody(albumID))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var answer ignoredReleasesResponse
	if err := json.Unmarshal(response.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if len(answer.Releases) != 1 || answer.Releases[0].Dismissed != 1 {
		t.Fatalf("answer = %+v; want one release with one dismissal", answer.Releases)
	}
	if wants.stopped == uuid.Nil {
		t.Fatal("no want was stopped for the release that was ignored")
	}
}

// Owned music is untouched by ignoring a release, exactly as it is when one
// release is ignored on its own page.
func TestIgnoringASelectionLeavesOwnedMusicAlone(t *testing.T) {
	albumID := uuid.New()
	store := &fakeStore{
		album: db.GetAlbumRow{ID: albumID, Title: "A Release", ArtistName: "An Artist"},
		tracks: []db.ListAlbumTracksRow{
			{ID: uuid.New(), Title: "Owned", Owned: true},
		},
	}
	wants := &fakeAcquisitionTargets{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(wants))

	postSelection(handler, "/api/v1/releases/ignore", selectionBody(albumID))

	if wants.stopped != uuid.Nil {
		t.Fatal("a want was stopped for a release the library fully owns")
	}
}
