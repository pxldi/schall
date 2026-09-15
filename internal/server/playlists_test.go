package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/acquisition"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/navidrome"
	"github.com/pxldi/schall/internal/playlists"
	"github.com/rs/zerolog"
)

type fakePlaylists struct {
	err       error
	connected bool
	code      string
	// rows and entries are what the reading endpoints answer with.
	rows    []db.PlaylistRow
	row     db.PlaylistRow
	entries []db.PlaylistEntryRow
	// followed is the last link Follow was asked about, so a refusal can be
	// checked against the service never having been reached at all.
	followed string
	// wanted is the last playlist/entry pair WantEntry was asked about.
	wanted struct {
		playlistID uuid.UUID
		entryID    uuid.UUID
		called     bool
	}
	// importedFile is what the last file import was given: the file's name,
	// the name for the list, and the bytes.
	importedFile struct {
		filename string
		listName string
		content  string
		called   bool
	}
}

func (fake *fakePlaylists) List(context.Context) ([]db.PlaylistRow, error) {
	return fake.rows, fake.err
}
func (fake *fakePlaylists) Get(context.Context, uuid.UUID) (db.PlaylistRow, []db.PlaylistEntryRow, error) {
	return fake.row, fake.entries, fake.err
}
func (fake *fakePlaylists) Follow(_ context.Context, link string) (db.PlaylistRow, error) {
	fake.followed = link
	return db.PlaylistRow{Name: link, Source: "spotify"}, fake.err
}
func (fake *fakePlaylists) QueueImport(context.Context, uuid.UUID) (db.PlaylistRow, error) {
	return db.PlaylistRow{Source: "spotify"}, fake.err
}
func (fake *fakePlaylists) WantEntry(_ context.Context, playlistID, entryID uuid.UUID) error {
	fake.wanted.playlistID = playlistID
	fake.wanted.entryID = entryID
	fake.wanted.called = true
	return fake.err
}
func (fake *fakePlaylists) ImportFile(
	_ context.Context, filename, name string, content []byte,
) (db.PlaylistRow, error) {
	fake.importedFile.filename = filename
	fake.importedFile.listName = name
	fake.importedFile.content = string(content)
	fake.importedFile.called = true
	if fake.err != nil {
		return db.PlaylistRow{}, fake.err
	}
	return db.PlaylistRow{Name: name, Source: "file"}, nil
}
func (fake *fakePlaylists) Delete(context.Context, uuid.UUID) error { return fake.err }
func (fake *fakePlaylists) Connect(_ context.Context, code, _ string) (db.SpotifySettingsRow, error) {
	fake.connected, fake.code = true, code
	return db.SpotifySettingsRow{}, fake.err
}
func (fake *fakePlaylists) AuthorizeURL(_ context.Context, redirectURI, state string) (string, error) {
	return "https://accounts.spotify.example/authorize?" + url.Values{
		"redirect_uri": {redirectURI}, "state": {state},
	}.Encode(), fake.err
}

// spotifyStore is the fakeStore with Spotify settings on top, so NewAPI's
// interface assertion finds them.
type spotifyStore struct {
	*fakeStore
	row db.SpotifySettingsRow
	// rowErr is how the settings lookup answers, so a store with nothing stored
	// and a store that could not be read are different tests.
	rowErr  error
	saveErr error
}

func (store *spotifyStore) SpotifySettings(context.Context) (db.SpotifySettingsRow, error) {
	return store.row, store.rowErr
}

func (store *spotifyStore) SaveSpotifySettings(_ context.Context, clientID, clientSecret string) (db.SpotifySettingsRow, error) {
	if store.saveErr != nil {
		return db.SpotifySettingsRow{}, store.saveErr
	}
	store.row.ClientID = clientID
	if clientSecret != "" {
		store.row.ClientSecret = clientSecret
	}
	return store.row, nil
}

func playlistHandler(service PlaylistService, store *spotifyStore) http.Handler {
	return NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithPlaylists(service))
}

// The settings response says whether secrets exist and never what they are.
func TestSpotifySettingsNeverEchoSecrets(t *testing.T) {
	store := &spotifyStore{fakeStore: &fakeStore{}, row: db.SpotifySettingsRow{
		ClientID:     "client-id",
		ClientSecret: "the-secret-value",
		RefreshToken: pgtype.Text{String: "the-refresh-token", Valid: true},
		AccountName:  pgtype.Text{String: "Poldi", Valid: true},
	}}
	handler := playlistHandler(&fakePlaylists{}, store)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/settings/spotify", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	body := response.Body.String()
	if strings.Contains(body, "the-secret-value") || strings.Contains(body, "the-refresh-token") {
		t.Fatalf("settings response leaked a secret: %s", body)
	}
	var decoded struct {
		ClientSecretSet bool   `json:"clientSecretSet"`
		Connected       bool   `json:"connected"`
		RedirectURI     string `json:"redirectUri"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.ClientSecretSet || !decoded.Connected {
		t.Fatalf("response = %s", body)
	}
	if !strings.HasSuffix(decoded.RedirectURI, "/api/v1/spotify/callback") {
		t.Fatalf("redirect URI = %q", decoded.RedirectURI)
	}
}

// The callback insists on the state it issued: an unknown state connects
// nothing, the issued one connects and both end at the settings page.
func TestSpotifyCallbackInsistsOnItsState(t *testing.T) {
	service := &fakePlaylists{}
	handler := playlistHandler(service, &spotifyStore{fakeStore: &fakeStore{}})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/spotify/callback?code=abc&state=forged", nil))
	if response.Code != http.StatusFound || service.connected {
		t.Fatalf("forged state: status = %d, connected = %v", response.Code, service.connected)
	}
	if location := response.Header().Get("Location"); !strings.Contains(location, "state-mismatch") {
		t.Fatalf("forged state redirected to %q", location)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/settings/spotify/authorize", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("authorize: status = %d body = %s", response.Code, response.Body.String())
	}
	var issued struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	authorizeURL, err := url.Parse(issued.URL)
	if err != nil {
		t.Fatal(err)
	}
	state := authorizeURL.Query().Get("state")
	if state == "" {
		t.Fatalf("authorize URL carries no state: %s", issued.URL)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/spotify/callback?code=abc&state="+state, nil))
	if !service.connected || service.code != "abc" {
		t.Fatal("the issued state did not connect")
	}
	if location := response.Header().Get("Location"); !strings.Contains(location, "connected") {
		t.Fatalf("connected callback redirected to %q", location)
	}

	// A state is redeemed once.
	service.connected = false
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/spotify/callback?code=abc&state="+state, nil))
	if service.connected {
		t.Fatal("a redeemed state connected a second time")
	}
}

func TestFollowingWithoutAnAccountIsAConflict(t *testing.T) {
	handler := playlistHandler(&fakePlaylists{err: playlists.ErrNotConnected}, &spotifyStore{fakeStore: &fakeStore{}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/playlists",
		strings.NewReader(`{"url":"https://open.spotify.com/playlist/abc"}`)))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.Code)
	}
}

func TestFollowingWithoutCredentialsIsAConflict(t *testing.T) {
	handler := playlistHandler(&fakePlaylists{err: playlists.ErrNotConfigured}, &spotifyStore{fakeStore: &fakeStore{}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/playlists",
		strings.NewReader(`{"url":"https://open.spotify.com/playlist/abc"}`)))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.Code)
	}
}

// A link that is not a playlist is the user's to correct, and the reason has to
// reach them rather than becoming an internal error.
func TestFollowingALinkThatIsNotAPlaylistSaysWhy(t *testing.T) {
	handler := playlistHandler(
		&fakePlaylists{err: errors.New("that link is not a Spotify playlist")},
		&spotifyStore{fakeStore: &fakeStore{}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/playlists",
		strings.NewReader(`{"url":"https://example.invalid/nothing"}`)))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", response.Code)
	}
	if !strings.Contains(response.Body.String(), "not a Spotify playlist") {
		t.Fatalf("body = %s, want the reason carried through", response.Body)
	}
}

func TestAFollowedPlaylistIsReturnedAsCreated(t *testing.T) {
	handler := playlistHandler(&fakePlaylists{}, &spotifyStore{fakeStore: &fakeStore{}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/playlists",
		strings.NewReader(`{"url":"https://open.spotify.com/playlist/abc"}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// The followed lists are what the playlists page is, and each one carries how
// much of it the library already holds.
func TestFollowedPlaylistsAreListedWithWhatIsOwned(t *testing.T) {
	service := &fakePlaylists{rows: []db.PlaylistRow{{
		ID: uuid.New(), Source: "spotify", Name: "Dinner", EntryCount: 12, OwnedCount: 5,
	}}}
	handler := playlistHandler(service, &spotifyStore{fakeStore: &fakeStore{}})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/playlists", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload struct {
		Items []playlistResponse `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 || payload.Items[0].OwnedCount != 5 || payload.Items[0].EntryCount != 12 {
		t.Fatalf("items = %+v", payload.Items)
	}
}

func TestListingPlaylistsReportsAStoreThatFailed(t *testing.T) {
	handler := playlistHandler(
		&fakePlaylists{err: errors.New("the database is unreachable")},
		&spotifyStore{fakeStore: &fakeStore{}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/playlists", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// A playlist's page is its entries in order, each saying whether the library
// already holds it and what is being pursued for it if not.
func TestAPlaylistIsReadBackWithItsEntries(t *testing.T) {
	fileID, targetID := uuid.New(), uuid.New()
	service := &fakePlaylists{
		row: db.PlaylistRow{ID: uuid.New(), Source: "spotify", Name: "Dinner"},
		entries: []db.PlaylistEntryRow{{
			ID: uuid.New(), Position: 1, EntryArtist: "Portishead", EntryTitle: "Roads",
			EntryDurationMS: pgtype.Int4{Int32: 305000, Valid: true},
			OwnedFileID:     uuid.NullUUID{UUID: fileID, Valid: true},
			AnsweringFileID: uuid.NullUUID{UUID: fileID, Valid: true},
		}, {
			ID: uuid.New(), Position: 2, EntryArtist: "Portishead", EntryTitle: "Wandering Star",
			TargetID:     uuid.NullUUID{UUID: targetID, Valid: true},
			TargetStatus: pgtype.Text{String: "pending", Valid: true},
		}},
	}
	handler := playlistHandler(service, &spotifyStore{fakeStore: &fakeStore{}})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/playlists/"+uuid.NewString(), nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload struct {
		Entries []playlistEntryResponse `json:"entries"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Entries) != 2 {
		t.Fatalf("entries = %+v", payload.Entries)
	}
	if payload.Entries[0].OwnedFileID == nil || *payload.Entries[0].OwnedFileID != fileID {
		t.Errorf("first entry = %+v, want the owned file named", payload.Entries[0])
	}
	if payload.Entries[1].TargetID == nil || *payload.Entries[1].TargetID != targetID {
		t.Errorf("second entry = %+v, want the want named", payload.Entries[1])
	}
}

// Music Schall went and fetched is music the user has. The stored ownership
// record is only written by an import, so an entry acquired since the last one
// has none — and reading that record instead of the answer would tell somebody
// they do not own a song sitting in their library.
func TestAnEntryAcquiredSinceTheLastImportIsReturnedAsOwned(t *testing.T) {
	fileID := uuid.New()
	service := &fakePlaylists{
		row: db.PlaylistRow{ID: uuid.New(), Source: "spotify", Name: "Dinner"},
		entries: []db.PlaylistEntryRow{{
			ID: uuid.New(), Position: 1, EntryArtist: "Portishead", EntryTitle: "Roads",
			AnsweringFileID: uuid.NullUUID{UUID: fileID, Valid: true},
			TargetID:        uuid.NullUUID{UUID: uuid.New(), Valid: true},
			TargetStatus:    pgtype.Text{String: "acquired", Valid: true},
		}},
	}
	handler := playlistHandler(service, &spotifyStore{fakeStore: &fakeStore{}})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/playlists/"+uuid.NewString(), nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload struct {
		Entries []playlistEntryResponse `json:"entries"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Entries) != 1 {
		t.Fatalf("entries = %+v", payload.Entries)
	}
	if payload.Entries[0].OwnedFileID == nil || *payload.Entries[0].OwnedFileID != fileID {
		t.Errorf("entry = %+v, want the acquired file named as owned", payload.Entries[0])
	}
}

// An entry MusicBrainz holds no recording for cannot be acquired however often
// it is asked about, and the release editor is the one action that changes that.
func TestAnEntryNothingWasFoundForIsOfferedTheReleaseEditor(t *testing.T) {
	entries := readEntries(t, unfoundEntry(), &fakeStore{})

	seed := entries[0].MusicBrainzSeed
	if seed == nil {
		t.Fatalf("entry = %+v, want a seeded release editor", entries[0])
	}
	if seed.URL != acquisition.ReleaseEditorURL {
		t.Errorf("seed url = %q, want %q", seed.URL, acquisition.ReleaseEditorURL)
	}
	if got := seeded(seed, "mediums.0.track.0.name"); got != "France 98" {
		t.Errorf("seeded track title = %q, want %q", got, "France 98")
	}
}

// An entry that has not been asked about yet has lost nothing, and offering to
// add a release for it would answer a question nobody has asked.
func TestAnEntryWaitingToBeResolvedIsOfferedNoReleaseEditor(t *testing.T) {
	entry := unfoundEntry()
	entry.TargetSummary = pgtype.Text{String: acquisition.UnresolvedSummary, Valid: true}

	if seed := readEntries(t, entry, &fakeStore{})[0].MusicBrainzSeed; seed != nil {
		t.Errorf("seed = %+v, want none while the entry is still being asked about", seed)
	}
}

// An artist the catalogue already holds is named by identifier, so the editor
// opens knowing who this is rather than guessing at a name.
func TestASeededEditorNamesAnArtistTheCatalogueKnows(t *testing.T) {
	artistID := uuid.New()
	store := &fakeStore{artistIDs: map[string]uuid.UUID{"umbraid": artistID}}

	seed := readEntries(t, unfoundEntry(), store)[0].MusicBrainzSeed
	if seed == nil {
		t.Fatal("entry has no seeded release editor")
	}
	if got := seeded(seed, "artist_credit.names.0.mbid"); got != artistID.String() {
		t.Errorf("seeded artist mbid = %q, want %q", got, artistID)
	}
}

// Only the entries at a dead end are offered a form, so only their artists are
// worth a question to the catalogue.
func TestOnlyTheEntriesNothingWasFoundForAreLookedUpByArtist(t *testing.T) {
	waiting := unfoundEntry()
	waiting.EntryArtist = "Ket Robinson"
	waiting.TargetSummary = pgtype.Text{String: acquisition.UnresolvedSummary, Valid: true}
	store := &fakeStore{}

	readEntries(t, unfoundEntry(), store, waiting)

	for _, name := range store.artistIDsAsked {
		if name == "Ket Robinson" {
			t.Errorf("asked about %q, want only the artists of entries at a dead end", name)
		}
	}
}

// The form is an extra. A catalogue that cannot answer which artist this is
// leaves the editor searching for a name, which is where it would have been
// anyway, and the list still reads back.
func TestASeededEditorSurvivesAnArtistLookupThatFails(t *testing.T) {
	store := &fakeStore{artistIDsErr: errors.New("no")}

	seed := readEntries(t, unfoundEntry(), store)[0].MusicBrainzSeed
	if seed == nil {
		t.Fatal("entry has no seeded release editor")
	}
	if got := seeded(seed, "artist_credit.names.0.artist.name"); got != "umbraid" {
		t.Errorf("seeded artist name = %q, want %q", got, "umbraid")
	}
}

// MusicBrainz editors are people, and metadata arriving without an account of
// where it came from is rude.
func TestASeededEditNoteNamesTheListTheMetadataCameFrom(t *testing.T) {
	seed := readEntries(t, unfoundEntry(), &fakeStore{})[0].MusicBrainzSeed
	if seed == nil {
		t.Fatal("entry has no seeded release editor")
	}
	if note := seeded(seed, "edit_note"); !strings.Contains(note, "a Spotify playlist") {
		t.Errorf("edit note = %q, want it to name the Spotify playlist it came from", note)
	}
}

// One entry MusicBrainz was asked about and had no recording for. The sentence
// is spelled out rather than borrowed from the acquisition package, which keeps
// it to itself: it is what the database holds for such an entry, and a change to
// it that these tests did not notice would be a change to who gets offered the
// release editor.
func unfoundEntry() db.PlaylistEntryRow {
	return db.PlaylistEntryRow{
		ID: uuid.New(), Position: 1, EntryArtist: "umbraid", EntryTitle: "France 98",
		EntryAlbum:      "France 98",
		EntryDurationMS: pgtype.Int4{Int32: 372000, Valid: true},
		TargetID:        uuid.NullUUID{UUID: uuid.New(), Valid: true},
		TargetStatus:    pgtype.Text{String: "unresolved", Valid: true},
		TargetSummary: pgtype.Text{
			String: "No recording MusicBrainz knows of matches this entry yet." +
				" It will be asked about again.",
			Valid: true,
		},
	}
}

func readEntries(
	t *testing.T, entry db.PlaylistEntryRow, store *fakeStore, rest ...db.PlaylistEntryRow,
) []playlistEntryResponse {
	t.Helper()
	service := &fakePlaylists{
		row:     db.PlaylistRow{ID: uuid.New(), Source: "spotify", Name: "Techno"},
		entries: append([]db.PlaylistEntryRow{entry}, rest...),
	}
	handler := playlistHandler(service, &spotifyStore{fakeStore: store})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/playlists/"+uuid.NewString(), nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload struct {
		Entries []playlistEntryResponse `json:"entries"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Entries) != len(rest)+1 {
		t.Fatalf("entries = %+v", payload.Entries)
	}
	return payload.Entries
}

func seeded(seed *musicbrainzSeedResponse, name string) string {
	for _, field := range seed.Fields {
		if field.Name == name {
			return field.Value
		}
	}
	return ""
}

func TestAPlaylistNobodyFollowsIsNotFound(t *testing.T) {
	handler := playlistHandler(&fakePlaylists{err: pgx.ErrNoRows}, &spotifyStore{fakeStore: &fakeStore{}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/playlists/"+uuid.NewString(), nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestReadingAPlaylistReportsAStoreThatFailed(t *testing.T) {
	handler := playlistHandler(
		&fakePlaylists{err: errors.New("the database is unreachable")},
		&spotifyStore{fakeStore: &fakeStore{}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/playlists/"+uuid.NewString(), nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestAnImportIsQueuedForAFollowedPlaylist(t *testing.T) {
	handler := playlistHandler(&fakePlaylists{}, &spotifyStore{fakeStore: &fakeStore{}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/playlists/"+uuid.NewString()+"/import", nil))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestImportingAPlaylistNobodyFollowsIsNotFound(t *testing.T) {
	handler := playlistHandler(&fakePlaylists{err: pgx.ErrNoRows}, &spotifyStore{fakeStore: &fakeStore{}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/playlists/"+uuid.NewString()+"/import", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

// Spotify is the only source an import knows how to read, so a list from
// anywhere else is refused rather than half-imported.
func TestOnlyASpotifyPlaylistCanBeImported(t *testing.T) {
	handler := playlistHandler(
		&fakePlaylists{err: playlists.ErrNotSpotify}, &spotifyStore{fakeStore: &fakeStore{}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/playlists/"+uuid.NewString()+"/import", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.Code)
	}
}

func TestQueueingAPlaylistImportReportsAStoreThatFailed(t *testing.T) {
	handler := playlistHandler(
		&fakePlaylists{err: errors.New("the database is unreachable")},
		&spotifyStore{fakeStore: &fakeStore{}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/playlists/"+uuid.NewString()+"/import", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestWantingAPlaylistEntryCreatesItsTarget(t *testing.T) {
	fake := &fakePlaylists{}
	handler := playlistHandler(fake, &spotifyStore{fakeStore: &fakeStore{}})
	playlistID, entryID := uuid.New(), uuid.New()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/playlists/"+playlistID.String()+"/entries/"+entryID.String()+"/wanted", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if !fake.wanted.called || fake.wanted.playlistID != playlistID || fake.wanted.entryID != entryID {
		t.Fatalf("WantEntry called with (%v, %v), want (%v, %v)",
			fake.wanted.playlistID, fake.wanted.entryID, playlistID, entryID)
	}
}

func TestWantingAnEntryThatDoesNotExistIsNotFound(t *testing.T) {
	handler := playlistHandler(
		&fakePlaylists{err: playlists.ErrEntryNotFound}, &spotifyStore{fakeStore: &fakeStore{}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/playlists/"+uuid.NewString()+"/entries/"+uuid.NewString()+"/wanted", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestUnfollowingAPlaylistAnswersWithNoContent(t *testing.T) {
	handler := playlistHandler(&fakePlaylists{}, &spotifyStore{fakeStore: &fakeStore{}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete,
		"/api/v1/playlists/"+uuid.NewString(), nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestUnfollowingAPlaylistNobodyFollowsIsNotFound(t *testing.T) {
	handler := playlistHandler(&fakePlaylists{err: pgx.ErrNoRows}, &spotifyStore{fakeStore: &fakeStore{}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete,
		"/api/v1/playlists/"+uuid.NewString(), nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestUnfollowingAPlaylistReportsAStoreThatFailed(t *testing.T) {
	handler := playlistHandler(
		&fakePlaylists{err: errors.New("the database is unreachable")},
		&spotifyStore{fakeStore: &fakeStore{}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete,
		"/api/v1/playlists/"+uuid.NewString(), nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// A playlist id that is not a UUID names nothing, and saying so is the caller's
// mistake rather than a missing playlist.
func TestAMalformedPlaylistIDIsRefused(t *testing.T) {
	handler := playlistHandler(&fakePlaylists{}, &spotifyStore{fakeStore: &fakeStore{}})
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/playlists/not-a-uuid", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/playlists/not-a-uuid/import", nil),
		httptest.NewRequest(http.MethodDelete, "/api/v1/playlists/not-a-uuid", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/playlists/not-a-uuid/entries/"+uuid.NewString()+"/wanted", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/playlists/"+uuid.NewString()+"/entries/not-a-uuid/wanted", nil),
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Errorf("%s %s: status = %d, want 400", request.Method, request.URL.Path, response.Code)
		}
	}
}

// Without playlist ingestion wired up the routes say so, rather than half
// working against settings nothing reads.
func TestThePlaylistRoutesReportBeingUnavailable(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/playlists", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/playlists", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodGet, "/api/v1/playlists/"+uuid.NewString(), nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/playlists/"+uuid.NewString()+"/import", nil),
		httptest.NewRequest(http.MethodDelete, "/api/v1/playlists/"+uuid.NewString(), nil),
		httptest.NewRequest(http.MethodPost,
			"/api/v1/playlists/"+uuid.NewString()+"/entries/"+uuid.NewString()+"/wanted", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/settings/spotify", nil),
		httptest.NewRequest(http.MethodPut, "/api/v1/settings/spotify", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodPost, "/api/v1/settings/spotify/authorize", nil),
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: status = %d, want 503", request.Method, request.URL.Path, response.Code)
		}
	}
}

// The callback is a browser navigation rather than an API call, so with nothing
// configured it says so in plain text rather than redirecting somewhere.
func TestTheSpotifyCallbackReportsBeingUnavailable(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/spotify/callback", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
}

// Nothing stored yet is not a failure: the settings page has to open before
// anybody can enter credentials on it.
func TestSpotifySettingsAreReadableBeforeAnythingIsStored(t *testing.T) {
	handler := playlistHandler(&fakePlaylists{},
		&spotifyStore{fakeStore: &fakeStore{}, rowErr: pgx.ErrNoRows})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/settings/spotify", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload spotifySettingsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Configured || payload.ConnectionStatus != "unknown" {
		t.Fatalf("payload = %+v, want nothing configured", payload)
	}
}

func TestReadingSpotifySettingsReportsAStoreThatFailed(t *testing.T) {
	handler := playlistHandler(&fakePlaylists{},
		&spotifyStore{fakeStore: &fakeStore{}, rowErr: errors.New("the database is unreachable")})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/settings/spotify", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestSpotifySettingsRequireAClientID(t *testing.T) {
	handler := playlistHandler(&fakePlaylists{}, &spotifyStore{fakeStore: &fakeStore{}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/settings/spotify",
		strings.NewReader(`{"clientId":"  ","clientSecret":"secret"}`)))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", response.Code)
	}
}

// An omitted secret keeps the stored one, which only works if one is stored.
func TestSpotifySettingsRequireASecretTheFirstTime(t *testing.T) {
	handler := playlistHandler(&fakePlaylists{},
		&spotifyStore{fakeStore: &fakeStore{}, rowErr: pgx.ErrNoRows})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/settings/spotify",
		strings.NewReader(`{"clientId":"client-id"}`)))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", response.Code)
	}
}

// Once a secret is stored, saving without one leaves it alone rather than
// clearing it — which is what makes the settings form safe to submit twice.
func TestSavingSpotifySettingsWithoutASecretKeepsTheStoredOne(t *testing.T) {
	store := &spotifyStore{fakeStore: &fakeStore{}, row: db.SpotifySettingsRow{
		ClientID: "old-client", ClientSecret: "the-secret-value",
	}}
	handler := playlistHandler(&fakePlaylists{}, store)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/settings/spotify",
		strings.NewReader(`{"clientId":"new-client"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.row.ClientID != "new-client" || store.row.ClientSecret != "the-secret-value" {
		t.Fatalf("stored = %+v, want the secret kept", store.row)
	}
}

func TestSavingSpotifySettingsReportsAStoreThatFailed(t *testing.T) {
	handler := playlistHandler(&fakePlaylists{},
		&spotifyStore{fakeStore: &fakeStore{}, saveErr: errors.New("the database is unreachable")})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/settings/spotify",
		strings.NewReader(`{"clientId":"client-id","clientSecret":"secret"}`)))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// Authorization cannot be prepared before the credentials it would be sent
// with, and that is the operator's next step rather than a server fault.
func TestAuthorizingWithoutCredentialsIsAConflict(t *testing.T) {
	handler := playlistHandler(
		&fakePlaylists{err: playlists.ErrNotConfigured}, &spotifyStore{fakeStore: &fakeStore{}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/settings/spotify/authorize", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.Code)
	}
}

func TestPreparingAuthorizationReportsAProviderThatFailed(t *testing.T) {
	handler := playlistHandler(
		&fakePlaylists{err: errors.New("spotify did not respond")}, &spotifyStore{fakeStore: &fakeStore{}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/settings/spotify/authorize", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// A user who refused on Spotify's consent page comes back with an error and no
// code, and lands on the settings page saying so rather than on a blank screen.
func TestARefusedSpotifyApprovalEndsAtTheSettingsPage(t *testing.T) {
	service := &fakePlaylists{}
	handler := playlistHandler(service, &spotifyStore{fakeStore: &fakeStore{}})
	state := issuedSpotifyState(t, handler)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/spotify/callback?error=access_denied&state="+state, nil))
	if response.Code != http.StatusFound || service.connected {
		t.Fatalf("status = %d connected = %v", response.Code, service.connected)
	}
	if location := response.Header().Get("Location"); !strings.Contains(location, "denied") {
		t.Fatalf("redirected to %q", location)
	}
}

// A code Spotify would not exchange leaves the account unconnected, and the
// settings page has to say that rather than claiming success.
func TestAnExchangeThatFailedEndsAtTheSettingsPage(t *testing.T) {
	service := &fakePlaylists{}
	handler := playlistHandler(service, &spotifyStore{fakeStore: &fakeStore{}})
	state := issuedSpotifyState(t, handler)
	service.err = errors.New("spotify refused the authorization code")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/spotify/callback?code=abc&state="+state, nil))
	if location := response.Header().Get("Location"); !strings.Contains(location, "failed") {
		t.Fatalf("redirected to %q", location)
	}
}

// issuedSpotifyState asks for an authorization URL and returns the state the
// server will insist on seeing again.
func issuedSpotifyState(t *testing.T, handler http.Handler) string {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/settings/spotify/authorize", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("authorize: status = %d; body = %s", response.Code, response.Body)
	}
	var issued struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	authorizeURL, err := url.Parse(issued.URL)
	if err != nil {
		t.Fatal(err)
	}
	state := authorizeURL.Query().Get("state")
	if state == "" {
		t.Fatalf("authorize URL carries no state: %s", issued.URL)
	}
	return state
}

// A callback arriving over a proxy that terminated TLS must name the address
// the browser used, or the redirect Spotify was registered against is not the
// one Schall asks it to come back to.
func TestTheRedirectURIFollowsTheAddressTheBrowserUsed(t *testing.T) {
	handler := playlistHandler(&fakePlaylists{}, &spotifyStore{fakeStore: &fakeStore{}})

	request := httptest.NewRequest(http.MethodGet, "/api/v1/settings/spotify", nil)
	request.Host = "schall.example"
	request.Header.Set("X-Forwarded-Proto", "https")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	var payload spotifySettingsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.RedirectURI != "https://schall.example/api/v1/spotify/callback" {
		t.Fatalf("redirect URI = %q", payload.RedirectURI)
	}
}

// A body nothing can read is not settings to store, and saying so is half the
// answer. These two handlers used to write nothing at all, which net/http
// finishes as an empty 200 — the page reads that as saved, invalidates its
// query, and shows the credentials it already had as though they were the ones
// it just sent. Nothing was written either way; what changed is that the
// caller now hears it.
func TestSavingSpotifySettingsStoresNothingFromABodyThatIsNotJSON(t *testing.T) {
	store := &spotifyStore{fakeStore: &fakeStore{}}
	handler := playlistHandler(&fakePlaylists{}, store)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/settings/spotify",
		strings.NewReader(`not json`)))
	if store.row.ClientID != "" {
		t.Fatalf("stored = %+v, want nothing written", store.row)
	}
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

// A state outliving a restart would mean the approval took longer than anybody
// sits on a consent page, so the ones that have aged out are dropped as new
// ones are issued rather than kept for a browser that is never coming back.
func TestAnExpiredAuthorizationStateIsForgotten(t *testing.T) {
	states := &oauthStates{issued: map[string]time.Time{
		"stale": time.Now().Add(-oauthStateLifetime - time.Minute),
	}}

	if _, err := states.issue(); err != nil {
		t.Fatalf("issue() error = %v", err)
	}
	if states.redeem("stale") {
		t.Fatal("a state older than the consent page's lifetime was still redeemed")
	}
}

// A body that cannot be read is refused out loud. Following nothing is only
// half of it: an empty 200 is a success as far as the browser is concerned, and
// the page would go on to invalidate its queries and show the list unchanged
// with no sign anything went wrong.
func TestFollowingAPlaylistFollowsNothingFromABodyThatIsNotJSON(t *testing.T) {
	service := &fakePlaylists{}
	handler := playlistHandler(service, &spotifyStore{fakeStore: &fakeStore{}})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/playlists",
		strings.NewReader(`not json`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
	if service.followed != "" {
		t.Fatalf("followed = %q, want nothing followed", service.followed)
	}
}

// navidromeSyncStore is the fakeStore with the sync queue on top, so NewAPI's
// interface assertion finds it.
type navidromeSyncStore struct {
	*spotifyStore
	syncedPlaylist uuid.UUID
	swept          bool
	err            error
}

func (store *navidromeSyncStore) QueueNavidromePlaylistSync(_ context.Context, playlistID uuid.UUID) error {
	if store.err != nil {
		return store.err
	}
	store.syncedPlaylist = playlistID
	return nil
}

func (store *navidromeSyncStore) QueueNavidromePlaylistSweep(context.Context, time.Time) error {
	if store.err != nil {
		return store.err
	}
	store.swept = true
	return nil
}

func navidromeSyncHandler(store *navidromeSyncStore) http.Handler {
	return NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithPlaylists(&fakePlaylists{}))
}

// Queued, not done: a pass talks to the player, and nobody waits on that
// through a request.
func TestSyncingOnePlaylistQueuesThePassAndSaysSo(t *testing.T) {
	store := &navidromeSyncStore{spotifyStore: &spotifyStore{fakeStore: &fakeStore{}}}
	playlistID := uuid.New()

	response := httptest.NewRecorder()
	navidromeSyncHandler(store).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/playlists/"+playlistID.String()+"/navidrome-sync", nil))

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", response.Code)
	}
	if store.syncedPlaylist != playlistID {
		t.Fatalf("queued = %s, want the playlist that was asked for", store.syncedPlaylist)
	}
}

func TestSyncingAPlaylistThatDoesNotExistIsNotFound(t *testing.T) {
	store := &navidromeSyncStore{
		spotifyStore: &spotifyStore{fakeStore: &fakeStore{}}, err: pgx.ErrNoRows,
	}

	response := httptest.NewRecorder()
	navidromeSyncHandler(store).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/playlists/"+uuid.New().String()+"/navidrome-sync", nil))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestSyncingAPlaylistNeedsAPlaylistID(t *testing.T) {
	store := &navidromeSyncStore{spotifyStore: &spotifyStore{fakeStore: &fakeStore{}}}

	response := httptest.NewRecorder()
	navidromeSyncHandler(store).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/playlists/not-a-uuid/navidrome-sync", nil))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

func TestSyncingWithThePlayerQueuesTheSweep(t *testing.T) {
	store := &navidromeSyncStore{spotifyStore: &spotifyStore{fakeStore: &fakeStore{}}}

	response := httptest.NewRecorder()
	navidromeSyncHandler(store).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/navidrome/sync", nil))

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", response.Code)
	}
	if !store.swept {
		t.Fatal("the sweep was not queued")
	}
}

// A store that cannot queue anything says so rather than answering 202 for a
// pass that will never run.
func TestASyncNobodyCouldQueueIsReported(t *testing.T) {
	store := &navidromeSyncStore{
		spotifyStore: &spotifyStore{fakeStore: &fakeStore{}},
		err:          errors.New("the database is not answering"),
	}

	response := httptest.NewRecorder()
	navidromeSyncHandler(store).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/navidrome/sync", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want the failure reported", response.Code)
	}
}

// An installation whose store predates sync answers that it is unavailable
// rather than half-working.
func TestSyncingIsUnavailableWithoutTheQueue(t *testing.T) {
	handler := NewAPI(&spotifyStore{fakeStore: &fakeStore{}}, fakeDatabase{},
		&fakeArtistSearcher{}, zerolog.Nop(), WithPlaylists(&fakePlaylists{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/navidrome/sync", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
}

// fakePairing answers for the player without one being there.
type fakePairing struct {
	report navidrome.PairingReport
	err    error
}

func (fake *fakePairing) PlaylistPairing(
	context.Context, uuid.UUID,
) (navidrome.PairingReport, error) {
	return fake.report, fake.err
}

func pairingHandler(pairing PlayerPairing) http.Handler {
	return NewAPI(&spotifyStore{fakeStore: &fakeStore{}}, fakeDatabase{},
		&fakeArtistSearcher{}, zerolog.Nop(),
		WithPlaylists(&fakePlaylists{}), WithPlayerPairing(pairing))
}

// Somebody looking at a playlist showing eight of thirty-eight tracks in the
// player needs the files, not the number: which ones, and what was asked about
// them. Reading it out of the database or the player by hand is the state this
// replaces.
func TestThePairingReportNamesTheFilesThePlayerDidNotAnswerFor(t *testing.T) {
	handler := pairingHandler(&fakePairing{report: navidrome.PairingReport{
		Configured: true, EntryCount: 38, AcquiredCount: 29, Checked: 29, PairedCount: 8,
		Unpaired: []navidrome.UnpairedTrack{{
			Position:    9,
			LibraryPath: "/music/Raär/one.flac",
			RemotePath:  "/music/Raär/one.flac",
			Reason:      navidrome.MissNoCandidates,
			Attempts:    []navidrome.LookupAttempt{{Term: "036 - Raär - one", Candidates: 0}},
		}},
	}})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/playlists/"+uuid.NewString()+"/navidrome-pairing", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", response.Code, response.Body.String())
	}
	var body struct {
		EntryCount    int64 `json:"entryCount"`
		AcquiredCount int   `json:"acquiredCount"`
		PairedCount   int   `json:"pairedCount"`
		Unpaired      []struct {
			Path        string   `json:"path"`
			PathEscaped string   `json:"pathEscaped"`
			Reason      string   `json:"reason"`
			Searched    []string `json:"searched"`
		} `json:"unpaired"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.EntryCount != 38 || body.AcquiredCount != 29 || body.PairedCount != 8 {
		t.Errorf("body = %+v, want all three counts", body)
	}
	if len(body.Unpaired) != 1 || body.Unpaired[0].Reason != "no_candidates" {
		t.Fatalf("unpaired = %+v, want the file and why", body.Unpaired)
	}
	if body.Unpaired[0].Searched[0] != "036 - Raär - one" {
		t.Errorf("searched = %v, want what the player was asked", body.Unpaired[0].Searched)
	}
	if !strings.Contains(body.Unpaired[0].PathEscaped, `\u00e4`) {
		t.Errorf("escaped path = %q, want a form a difference survives in",
			body.Unpaired[0].PathEscaped)
	}
}

// The player is asked inside the request, so a player that will not answer is
// the request's failure and not a 200 describing nothing.
func TestAPairingReportThePlayerWouldNotAnswerIsReported(t *testing.T) {
	handler := pairingHandler(&fakePairing{err: errors.New("navidrome did not respond in time")})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/playlists/"+uuid.NewString()+"/navidrome-pairing", nil))

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want the player's failure reported", response.Code)
	}
}

func TestAPairingReportForAPlaylistNobodyHoldsIsNotFound(t *testing.T) {
	handler := pairingHandler(&fakePairing{err: pgx.ErrNoRows})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/playlists/"+uuid.NewString()+"/navidrome-pairing", nil))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestThePairingReportIsUnavailableWithoutASyncer(t *testing.T) {
	handler := NewAPI(&spotifyStore{fakeStore: &fakeStore{}}, fakeDatabase{},
		&fakeArtistSearcher{}, zerolog.Nop(), WithPlaylists(&fakePlaylists{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/playlists/"+uuid.NewString()+"/navidrome-pairing", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
}

// playlistFileRequest builds the multipart request the browser sends: the file
// itself, and the name the person confirmed.
func playlistFileRequest(target, filename, content, name string) *http.Request {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if name != "" {
		_ = writer.WriteField("name", name)
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		panic(err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		panic(err)
	}
	_ = writer.Close()
	request := httptest.NewRequest(http.MethodPost, target, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

// The preview reads the file and creates nothing.
func TestPlaylistFilePreviewReadsTheFileAndImportsNothing(t *testing.T) {
	service := &fakePlaylists{}
	handler := playlistHandler(service, &spotifyStore{fakeStore: &fakeStore{}, rowErr: pgx.ErrNoRows})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, playlistFileRequest("/api/v1/playlists/file/preview",
		"Evening.csv", "Track Name,Artist Name(s),ISRC\nXtal,Aphex Twin,GBAAA8500001\n", ""))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", response.Code, response.Body.String())
	}
	var decoded struct {
		Name     string   `json:"name"`
		Format   string   `json:"format"`
		Columns  []string `json:"columns"`
		RowCount int      `json:"rowCount"`
		Rows     []struct {
			Title  string `json:"title"`
			Artist string `json:"artist"`
			ISRC   string `json:"isrc"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Name != "Evening" || decoded.Format != "csv" || decoded.RowCount != 1 {
		t.Fatalf("preview = %s", response.Body.String())
	}
	if strings.Join(decoded.Columns, ",") != "title,artist,isrc" {
		t.Fatalf("columns = %v", decoded.Columns)
	}
	if decoded.Rows[0].Title != "Xtal" || decoded.Rows[0].ISRC != "GBAAA8500001" {
		t.Fatalf("row = %+v", decoded.Rows[0])
	}
	if service.importedFile.called {
		t.Fatal("the preview imported the file")
	}
}

// A file nothing can be read out of is refused, and says what would work.
func TestPlaylistFilePreviewRefusesAFileItCannotRead(t *testing.T) {
	handler := playlistHandler(&fakePlaylists{}, &spotifyStore{fakeStore: &fakeStore{}, rowErr: pgx.ErrNoRows})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, playlistFileRequest("/api/v1/playlists/file/preview",
		"notes.csv", "a,b,c\n1,2,3\n", ""))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "cannot be read as a playlist") {
		t.Fatalf("body = %s", response.Body.String())
	}
}

// The import is given the file's own bytes and the confirmed name.
func TestImportPlaylistFileSendsTheFileToTheService(t *testing.T) {
	service := &fakePlaylists{}
	handler := playlistHandler(service, &spotifyStore{fakeStore: &fakeStore{}, rowErr: pgx.ErrNoRows})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, playlistFileRequest("/api/v1/playlists/file",
		"Evening.m3u", "#EXTM3U\n#EXTINF:10,A - B\n/music/a.flac\n", "Evening songs"))
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", response.Code, response.Body.String())
	}
	if service.importedFile.filename != "Evening.m3u" {
		t.Fatalf("filename = %q", service.importedFile.filename)
	}
	if service.importedFile.listName != "Evening songs" {
		t.Fatalf("list name = %q", service.importedFile.listName)
	}
	if !strings.Contains(service.importedFile.content, "#EXTINF:10,A - B") {
		t.Fatalf("content = %q", service.importedFile.content)
	}
	var decoded struct {
		Name   string `json:"name"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Name != "Evening songs" || decoded.Source != "file" {
		t.Fatalf("body = %s", response.Body.String())
	}
}

// A request with no file at all is told to choose one.
func TestImportPlaylistFileNeedsAFile(t *testing.T) {
	service := &fakePlaylists{}
	handler := playlistHandler(service, &spotifyStore{fakeStore: &fakeStore{}, rowErr: pgx.ErrNoRows})

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("name", "Evening")
	_ = writer.Close()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/playlists/file", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body %s", response.Code, response.Body.String())
	}
	if service.importedFile.called {
		t.Fatal("a request with no file reached the service")
	}
}
