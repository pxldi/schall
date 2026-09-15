package navidrome

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

type fakeStore struct {
	row      db.NavidromeSettingsRow
	readErr  error
	noticed  bool
	writeErr error
}

func (store *fakeStore) NavidromeSettings(context.Context) (db.NavidromeSettingsRow, error) {
	return store.row, store.readErr
}

func (store *fakeStore) RecordNavidromeNotice(context.Context) error {
	store.noticed = true
	return store.writeErr
}

type fakePlayer struct {
	told bool
	err  error
}

func (player *fakePlayer) Rescan(context.Context) error {
	player.told = true
	return player.err
}

// notifierFor builds a notifier that answers with the given player instead of
// reaching a network.
func notifierFor(store Store, player *fakePlayer) *Notifier {
	notifier := NewNotifier(store, zerolog.Nop())
	notifier.newClient = func(Options) (rescanner, error) { return player, nil }
	return notifier
}

func configured() db.NavidromeSettingsRow {
	return db.NavidromeSettingsRow{
		BaseURL: "http://navidrome:4533", Username: "schall", Password: "hunter2", Enabled: true,
	}
}

// An installation that plays its music some other way is not a broken one.
func TestAnInstallationWithNoPlayerTellsNobodyAndSaysNothingIsWrong(t *testing.T) {
	store := &fakeStore{readErr: pgx.ErrNoRows}
	player := &fakePlayer{}

	if err := notifierFor(store, player).NoticeLibraryChange(context.Background()); err != nil {
		t.Fatalf("notice library change: %v", err)
	}
	if player.told {
		t.Error("a player nobody configured was told")
	}
}

func TestAPlayerSwitchedOffIsNotTold(t *testing.T) {
	row := configured()
	row.Enabled = false
	store := &fakeStore{row: row}
	player := &fakePlayer{}

	if err := notifierFor(store, player).NoticeLibraryChange(context.Background()); err != nil {
		t.Fatalf("notice library change: %v", err)
	}
	if player.told {
		t.Error("a player switched off was told")
	}
}

func TestAConfiguredPlayerIsAskedToLookAgain(t *testing.T) {
	store := &fakeStore{row: configured()}
	player := &fakePlayer{}

	if err := notifierFor(store, player).NoticeLibraryChange(context.Background()); err != nil {
		t.Fatalf("notice library change: %v", err)
	}
	if !player.told {
		t.Error("the configured player was not told")
	}
}

func TestATellingThatLandedIsWrittenDown(t *testing.T) {
	store := &fakeStore{row: configured()}

	if err := notifierFor(store, &fakePlayer{}).NoticeLibraryChange(context.Background()); err != nil {
		t.Fatalf("notice library change: %v", err)
	}
	if !store.noticed {
		t.Error("a telling that landed was not written down")
	}
}

// The settings are the operator's to change while Schall is running, so the
// notifier builds its client from what is stored at the moment of the telling
// rather than from what was there at startup.
func TestTheNotifierTellsThePlayerTheSettingsNameNow(t *testing.T) {
	var asked []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"subsonic-response":{"status":"ok","version":"1.16.1"}}`))
	}))
	t.Cleanup(server.Close)

	row := configured()
	row.BaseURL = server.URL
	store := &fakeStore{row: row}

	if err := NewNotifier(store, zerolog.Nop()).NoticeLibraryChange(context.Background()); err != nil {
		t.Fatalf("notice library change: %v", err)
	}
	if len(asked) != 1 || asked[0] != apiPrefix+"/startScan" {
		t.Fatalf("asked = %v, want the stored player asked to scan", asked)
	}
}

// A player whose stored address is unusable is reported rather than quietly
// counted as told.
func TestAPlayerWithNoUsableAddressIsReportedRatherThanTold(t *testing.T) {
	row := configured()
	row.BaseURL = ""
	store := &fakeStore{row: row}

	err := NewNotifier(store, zerolog.Nop()).NoticeLibraryChange(context.Background())

	if err == nil {
		t.Fatal("a player nobody could address was reported as told")
	}
	if store.noticed {
		t.Error("a telling that never happened was written down")
	}
}

// Settings that could not be read are not the same as no player configured:
// there may well be one, and nobody knows.
func TestSettingsThatCouldNotBeReadAreReported(t *testing.T) {
	store := &fakeStore{readErr: errors.New("the database is not answering")}
	player := &fakePlayer{}

	err := notifierFor(store, player).NoticeLibraryChange(context.Background())

	if err == nil {
		t.Fatal("settings nobody could read were treated as no player at all")
	}
	if player.told {
		t.Error("a player nobody could look up was told")
	}
}

// A telling that landed and could not be written down is reported, because
// last_notified_at is what the next caller reads to know it happened.
func TestATellingThatCouldNotBeWrittenDownIsReported(t *testing.T) {
	store := &fakeStore{row: configured(), writeErr: errors.New("the database is not answering")}

	err := notifierFor(store, &fakePlayer{}).NoticeLibraryChange(context.Background())

	if err == nil {
		t.Fatal("a notice that was not recorded was reported as complete")
	}
}

// last_notified_at means the player accepted a request to look. A failed
// attempt that stamped it would erase the last moment one actually did.
func TestATellingThatFailedIsReportedAndNotWrittenDown(t *testing.T) {
	store := &fakeStore{row: configured()}
	player := &fakePlayer{err: errors.New("navidrome is reachable but not ready")}

	err := notifierFor(store, player).NoticeLibraryChange(context.Background())

	if err == nil {
		t.Fatal("a player that refused was reported as told")
	}
	if store.noticed {
		t.Error("a telling that failed was written down")
	}
}
