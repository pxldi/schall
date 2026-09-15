package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/soundcloud"
	"github.com/rs/zerolog"
)

type fakeSoundCloud struct {
	track     soundcloud.Track
	err       error
	adoptedID uuid.UUID
	askedURL  string
	confirmed soundcloud.Naming
}

func (fake *fakeSoundCloud) Preview(_ context.Context, permalink string) (soundcloud.Track, error) {
	fake.askedURL = permalink
	return fake.track, fake.err
}

func (fake *fakeSoundCloud) Adopt(
	_ context.Context, fileID uuid.UUID, permalink string, confirmed soundcloud.Naming,
) (soundcloud.Adopted, error) {
	fake.adoptedID = fileID
	fake.askedURL = permalink
	fake.confirmed = confirmed
	if fake.err != nil {
		return soundcloud.Adopted{}, fake.err
	}
	return soundcloud.Adopted{
		Track:      fake.track,
		Naming:     confirmed,
		TrackTitle: fake.track.TrackTitle(),
		ArtistID:   uuid.New(),
		Summary:    "Recorded by hand as a SoundCloud track.",
		Pictured:   true,
	}, nil
}

func anAboEdit() soundcloud.Track {
	return soundcloud.Track{
		ID:          "293",
		ExternalID:  "soundcloud:293",
		Title:       "PinkPantheress - Illegal (Abo Edit) by Abo",
		Uploader:    "Abo",
		UploaderURL: "https://soundcloud.com/abo",
		ArtworkURL:  "https://i1.sndcdn.com/artworks-abc-t500x500.jpg",
		Permalink:   "https://soundcloud.com/abo/illegal",
	}
}

func soundCloudAPI(tracks SoundCloudTracks) http.Handler {
	return NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSoundCloud(tracks))
}

// The person pastes a link and is shown what came back, before anything is
// decided. The whole title is carried as it arrived; the title the file will
// take is carried beside it.
func TestLookingUpATrackShowsWhatSoundCloudSaid(t *testing.T) {
	tracks := &fakeSoundCloud{track: anAboEdit()}
	response := httptest.NewRecorder()
	soundCloudAPI(tracks).ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/soundcloud/track?url=https%3A%2F%2Fsoundcloud.com%2Fabo%2Fillegal", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload soundCloudTrackResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Title != "PinkPantheress - Illegal (Abo Edit) by Abo" {
		t.Fatalf("title = %q, want the whole string oEmbed sent", payload.Title)
	}
	if payload.TrackTitle != "PinkPantheress - Illegal (Abo Edit)" {
		t.Fatalf("track title = %q", payload.TrackTitle)
	}
	if payload.Uploader != "Abo" || payload.ExternalID != "soundcloud:293" {
		t.Fatalf("payload = %+v", payload)
	}
	if tracks.askedURL != "https://soundcloud.com/abo/illegal" {
		t.Fatalf("asked about %q", tracks.askedURL)
	}
}

func TestNamingAFileFromSoundCloudSendsTheAddressAndTheFile(t *testing.T) {
	tracks := &fakeSoundCloud{track: anAboEdit()}
	fileID := uuid.New()

	response := httptest.NewRecorder()
	soundCloudAPI(tracks).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/library/files/"+fileID.String()+"/soundcloud",
		strings.NewReader(`{"url":"https://soundcloud.com/abo/illegal"}`)))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if tracks.adoptedID != fileID {
		t.Fatalf("named the file %s, want %s", tracks.adoptedID, fileID)
	}
	if tracks.askedURL != "https://soundcloud.com/abo/illegal" {
		t.Fatalf("asked about %q", tracks.askedURL)
	}
	var payload soundCloudAdoptResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Track.TrackTitle != "PinkPantheress - Illegal (Abo Edit)" || !payload.Pictured {
		t.Fatalf("payload = %+v", payload)
	}
}

// Both refusals a person can cause say what happened and what to do. Neither
// is a dead end, and neither is a retry.
func TestALinkThatIsNotATrackAndATrackThatIsGoneAreDifferentAnswers(t *testing.T) {
	cases := []struct {
		err    error
		status int
		title  string
	}{
		{soundcloud.ErrNotATrack, http.StatusBadRequest, "That link is not a SoundCloud track"},
		{soundcloud.ErrTrackGone, http.StatusNotFound, "SoundCloud no longer has that track"},
	}
	for _, testCase := range cases {
		response := httptest.NewRecorder()
		soundCloudAPI(&fakeSoundCloud{err: testCase.err}).ServeHTTP(response,
			httptest.NewRequest(http.MethodGet, "/api/v1/soundcloud/track?url=x", nil))

		if response.Code != testCase.status {
			t.Fatalf("status = %d, want %d; body = %s", response.Code, testCase.status, response.Body)
		}
		var problem struct {
			Title   string   `json:"title"`
			Details []string `json:"details"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
			t.Fatal(err)
		}
		if problem.Title != testCase.title {
			t.Errorf("title = %q, want %q", problem.Title, testCase.title)
		}
		if len(problem.Details) != 1 || problem.Details[0] == "" {
			t.Errorf("a refusal with nothing to do about it: %+v", problem.Details)
		}
	}
}

// An installation with no SoundCloud client says so rather than half-working.
func TestTheSoundCloudRoutesReportThemselvesUnavailable(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/soundcloud/track?url=x", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", response.Code, response.Body)
	}
}

// "source" is a state the library list can be narrowed to: it is how somebody
// finds the tracks Schall holds by address rather than by recording.
func TestTheLibraryListCanBeNarrowedToTracksHeldByAddress(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	for query, want := range map[string]int{
		"resolution=source":   http.StatusOK,
		"resolution=probably": http.StatusUnprocessableEntity,
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(
			http.MethodGet, "/api/v1/library/files?"+query, nil))
		if response.Code != want {
			t.Errorf("%s = %d, want %d; body = %s", query, response.Code, want, response.Body)
		}
	}
}
