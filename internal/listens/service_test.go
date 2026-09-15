package listens

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/rs/zerolog"

	"github.com/pxldi/schall/internal/db"
)

type fakeStore struct {
	settings db.ListenBrainzSettingsRow
	bounds   db.ListenBounds
	inserted []db.ListenInsert
}

func (store *fakeStore) ListenBrainzSettings(context.Context) (db.ListenBrainzSettingsRow, error) {
	return store.settings, nil
}

func (store *fakeStore) ListenBounds(context.Context) (db.ListenBounds, error) {
	return store.bounds, nil
}

func (store *fakeStore) InsertListens(_ context.Context, listens []db.ListenInsert) (int64, error) {
	store.inserted = append(store.inserted, listens...)
	return int64(len(listens)), nil
}

func enabled() db.ListenBrainzSettingsRow {
	return db.ListenBrainzSettingsRow{Username: "listener", Enabled: true, BaseURL: "http://unused", LabsURL: "http://unused"}
}

// page writes n listens ending at `last`, one second apart, newest first.
func page(last time.Time, n int) string {
	var entries []string
	for i := 0; i < n; i++ {
		at := last.Add(-time.Duration(i) * time.Second).Unix()
		entries = append(entries, fmt.Sprintf(
			`{"listened_at":%d,"track_metadata":{"artist_name":"A","track_name":"T%d","release_name":"R"}}`, at, i))
	}
	return `{"payload":{"count":` + fmt.Sprint(n) + `,"listens":[` + strings.Join(entries, ",") + `]}}`
}

func TestSyncReadsForwardFromTheNewestListen(t *testing.T) {
	newest := time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC)
	var asked []string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		asked = append(asked, request.URL.RawQuery)
		_, _ = response.Write([]byte(page(newest.Add(time.Hour), 3)))
	}))
	defer server.Close()

	store := &fakeStore{settings: enabled(), bounds: db.ListenBounds{
		Count:  10,
		Newest: pgtype.Timestamptz{Time: newest, Valid: true},
	}}
	service := NewService(store, "schall-test/0.0", zerolog.Nop()).WithEndpoint(server.URL, nil, nil)

	result, err := service.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if len(asked) != 1 || !strings.Contains(asked[0], "min_ts="+fmt.Sprint(newest.Unix())) {
		t.Fatalf("asked %v, want one page after the newest listen", asked)
	}
	if strings.Contains(asked[0], "max_ts") {
		t.Errorf("a forward read must not send max_ts: %s", asked[0])
	}
	if result.Fetched != 3 || result.Stored != 3 || result.More {
		t.Errorf("result = %+v", result)
	}
	if len(store.inserted) != 3 || store.inserted[0].TrackName != "T0" {
		t.Errorf("inserted %+v", store.inserted)
	}
}

func TestSyncBackfillsFromNowWhenNothingIsStored(t *testing.T) {
	now := time.Date(2026, time.September, 6, 0, 0, 0, 0, time.UTC)
	var asked []string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		asked = append(asked, request.URL.RawQuery)
		// A full page, then a short one, is the whole history.
		if len(asked) == 1 {
			_, _ = response.Write([]byte(page(now.Add(-time.Minute), pageSize)))
			return
		}
		_, _ = response.Write([]byte(page(now.Add(-2*time.Hour), 2)))
	}))
	defer server.Close()

	store := &fakeStore{settings: enabled()}
	service := NewService(store, "schall-test/0.0", zerolog.Nop()).
		WithEndpoint(server.URL, nil, func() time.Time { return now })

	result, err := service.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if len(asked) != 2 {
		t.Fatalf("asked %d pages, want 2: %v", len(asked), asked)
	}
	if !strings.Contains(asked[0], "max_ts="+fmt.Sprint(now.Unix())) {
		t.Errorf("first page must start at now: %s", asked[0])
	}
	oldestOfFirstPage := now.Add(-time.Minute).Add(-time.Duration(pageSize-1) * time.Second).Unix()
	if !strings.Contains(asked[1], "max_ts="+fmt.Sprint(oldestOfFirstPage)) {
		t.Errorf("second page must continue below the first: %s", asked[1])
	}
	if result.Fetched != pageSize+2 || result.More {
		t.Errorf("result = %+v", result)
	}
}

func TestSyncRefusesADisabledAccount(t *testing.T) {
	settings := enabled()
	settings.Enabled = false
	service := NewService(&fakeStore{settings: settings}, "schall-test/0.0", zerolog.Nop())
	if _, err := service.Sync(context.Background()); err != ErrDisabled {
		t.Fatalf("Sync() error = %v, want ErrDisabled", err)
	}
}

func TestSyncReadsOnBelowTheOldestListenUntilTheFloor(t *testing.T) {
	now := time.Date(2026, time.September, 6, 0, 0, 0, 0, time.UTC)
	oldest := now.Add(-30 * 24 * time.Hour)
	var asked []string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		asked = append(asked, request.URL.RawQuery)
		if strings.Contains(request.URL.RawQuery, "min_ts") {
			_, _ = response.Write([]byte(`{"payload":{"count":0,"listens":[]}}`))
			return
		}
		_, _ = response.Write([]byte(page(oldest.Add(-time.Hour), 2)))
	}))
	defer server.Close()

	store := &fakeStore{settings: enabled(), bounds: db.ListenBounds{
		Count:  10,
		Oldest: pgtype.Timestamptz{Time: oldest, Valid: true},
		Newest: pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true},
	}}
	service := NewService(store, "schall-test/0.0", zerolog.Nop()).
		WithEndpoint(server.URL, nil, func() time.Time { return now })

	result, err := service.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if len(asked) != 2 || !strings.Contains(asked[1], "max_ts="+fmt.Sprint(oldest.Unix())) {
		t.Fatalf("asked %v, want a forward page and then one below the oldest listen", asked)
	}
	if result.Fetched != 2 || len(store.inserted) != 2 {
		t.Errorf("result = %+v, inserted %d", result, len(store.inserted))
	}
}
