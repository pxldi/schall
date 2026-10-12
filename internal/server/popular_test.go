package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/rs/zerolog"

	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/listenbrainz"
	"github.com/pxldi/schall/internal/popular"
)

type popularStore struct {
	*fakeStore
	asked []uuid.UUID
	rows  []db.PopularTrackRow
}

func (store *popularStore) PopularTracks(_ context.Context, _ uuid.UUID, recordings []uuid.UUID) ([]db.PopularTrackRow, error) {
	store.asked = recordings
	return store.rows, nil
}

type fakePopular struct {
	recordings []listenbrainz.PopularRecording
	err        error
}

func (reader fakePopular) ForArtist(context.Context, string) ([]listenbrainz.PopularRecording, error) {
	return reader.recordings, reader.err
}

func readPopular(t *testing.T, store Store, reader PopularReader) popularResponse {
	t.Helper()
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithPopular(reader))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/artists/"+uuid.NewString()+"/popular", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body popularResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestPopularSaysWhatIsMissingWithoutAToken(t *testing.T) {
	store := &popularStore{fakeStore: &fakeStore{artist: db.GetArtistRow{
		MusicbrainzID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
	}}}
	body := readPopular(t, store, fakePopular{err: popular.ErrNoToken})
	if len(body.Items) != 0 || body.Unavailable == "" {
		t.Fatalf("body = %+v, want no items and a reason", body)
	}
}

// The list keeps ListenBrainz's order and takes what Schall knows about each
// recording from the catalogue, keyed by recording ID.
func TestPopularJoinsTheCatalogueByRecording(t *testing.T) {
	humble, luther := uuid.New(), uuid.New()
	trackID := uuid.New()
	store := &popularStore{
		fakeStore: &fakeStore{artist: db.GetArtistRow{MusicbrainzID: uuid.NullUUID{UUID: uuid.New(), Valid: true}}},
		rows: []db.PopularTrackRow{
			{RecordingID: humble, TrackID: uuid.NullUUID{UUID: trackID, Valid: true}, Held: true},
			{RecordingID: luther, WantStatus: pgtype.Text{String: "pending", Valid: true}},
		},
	}
	body := readPopular(t, store, fakePopular{recordings: []listenbrainz.PopularRecording{
		{RecordingMBID: humble.String(), Name: "HUMBLE.", ListenCount: 900},
		{RecordingMBID: "not an id", Name: "Dropped"},
		{RecordingMBID: luther.String(), Name: "luther", ListenCount: 800},
	}})
	if len(store.asked) != 2 || store.asked[0] != humble || store.asked[1] != luther {
		t.Fatalf("asked the catalogue for %v", store.asked)
	}
	if len(body.Items) != 2 || body.Items[0].Title != "HUMBLE." || !body.Items[0].Held ||
		body.Items[0].TrackID == nil || *body.Items[0].TrackID != trackID {
		t.Fatalf("items = %+v", body.Items)
	}
	if body.Items[1].Title != "luther" || body.Items[1].WantStatus == nil || *body.Items[1].WantStatus != "pending" {
		t.Fatalf("second = %+v", body.Items[1])
	}
}
