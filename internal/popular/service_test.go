package popular

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/rs/zerolog"

	"github.com/pxldi/schall/internal/db"
)

type fakeStore struct {
	row db.ListenBrainzSettingsRow
	err error
}

func (store fakeStore) ListenBrainzSettings(context.Context) (db.ListenBrainzSettingsRow, error) {
	return store.row, store.err
}

const artist = "381086ea-f511-4aba-bdf9-71c753dc5077"

func TestNoStoredTokenIsSaidRatherThanAsked(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	t.Cleanup(server.Close)

	for _, store := range []fakeStore{{err: pgx.ErrNoRows}, {row: db.ListenBrainzSettingsRow{Username: "listener"}}} {
		service := NewService(store, "schall-test/0.0", zerolog.Nop()).WithEndpoint(server.URL, nil)
		if _, err := service.ForArtist(context.Background(), artist); !errors.Is(err, ErrNoToken) {
			t.Fatalf("ForArtist() error = %v, want ErrNoToken", err)
		}
	}
	if calls != 0 {
		t.Fatalf("ListenBrainz was asked %d times without a token", calls)
	}
}

func TestOneArtistIsAskedOnceADay(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls++
		if request.Header.Get("Authorization") != "Token secret" {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		_, _ = response.Write([]byte(`[{"recording_mbid": "b1a9c0e9-d987-4042-ae91-78d6a3267d69", "recording_name": "HUMBLE."}]`))
	}))
	t.Cleanup(server.Close)

	now := time.Date(2026, 10, 12, 20, 0, 0, 0, time.UTC)
	store := fakeStore{row: db.ListenBrainzSettingsRow{UserToken: pgtype.Text{String: "secret", Valid: true}}}
	service := NewService(store, "schall-test/0.0", zerolog.Nop()).WithEndpoint(server.URL, func() time.Time { return now })

	for range 2 {
		recordings, err := service.ForArtist(context.Background(), artist)
		if err != nil || len(recordings) != 1 {
			t.Fatalf("ForArtist() = %v, %v", recordings, err)
		}
	}
	if calls != 1 {
		t.Fatalf("asked %d times within a day, want 1", calls)
	}
	now = now.Add(25 * time.Hour)
	if _, err := service.ForArtist(context.Background(), artist); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("asked %d times after a day, want 2", calls)
	}
}
