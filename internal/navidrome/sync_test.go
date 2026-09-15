package navidrome

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

// writtenSnapshot is one call to ReplaceNavidromePlaylistSnapshot, kept whole so
// a test can assert on what a pass claims Navidrome was told.
type writtenSnapshot struct {
	header db.NavidromePlaylistSnapshotParams
	tracks []db.NavidromePlaylistSnapshotTrackParams
}

type repairedTrack struct {
	playlistID uuid.UUID
	position   int32
	songID     string
}

// syncStore answers for the database. Everything a pass writes is remembered
// rather than applied, except the two writes a later step reads back: an
// appended entry becomes part of the acquired subset, exactly as setting
// owned_library_file_id does.
type syncStore struct {
	settings    db.NavidromeSettingsRow
	settingsErr error

	playlist       db.PlaylistRow
	lists          []db.PlaylistRow
	acquired       []db.AcquiredPlaylistTrackRow
	snapshot       db.NavidromePlaylistSnapshotRow
	snapshotErr    error
	snapshotTracks []db.NavidromePlaylistSnapshotTrackRow
	byRemoteID     map[string]db.NavidromePlaylistSnapshotRow
	filesByPath    map[string]db.LibraryFileRefRow
	filesByID      map[uuid.UUID]string
	adopted        db.PlaylistRow

	deleteErr error
	repairErr error
	upsertErr error
	writeErr  error
	fileErr   error
	queueErr  error

	deletedEntries  []uuid.UUID
	appended        []db.AppendPlaylistEntryParams
	repairs         []repairedTrack
	writes          []writtenSnapshot
	snapshotDropped bool
	upserted        []string
	queued          []uuid.UUID
}

func (store *syncStore) NavidromeSettings(context.Context) (db.NavidromeSettingsRow, error) {
	return store.settings, store.settingsErr
}

func (store *syncStore) Playlist(context.Context, uuid.UUID) (db.PlaylistRow, error) {
	return store.playlist, nil
}

func (store *syncStore) Playlists(context.Context) ([]db.PlaylistRow, error) {
	return store.lists, nil
}

func (store *syncStore) AcquiredPlaylistTracks(
	context.Context, uuid.UUID,
) ([]db.AcquiredPlaylistTrackRow, error) {
	return store.acquired, nil
}

func (store *syncStore) AppendPlaylistEntry(
	_ context.Context, params db.AppendPlaylistEntryParams,
) (db.PlaylistEntryRow, error) {
	store.appended = append(store.appended, params)
	entry := db.PlaylistEntryRow{
		ID:         uuid.New(),
		PlaylistID: params.PlaylistID,
		Position:   int32(len(store.acquired) + 1),
		EntryTitle: params.EntryTitle,
	}
	store.acquired = append(store.acquired, db.AcquiredPlaylistTrackRow{
		EntryID:       entry.ID,
		Position:      entry.Position,
		LibraryFileID: params.LibraryFileID,
		LibraryPath:   store.filesByID[params.LibraryFileID],
	})
	return entry, nil
}

func (store *syncStore) DeletePlaylistEntry(_ context.Context, entryID uuid.UUID) error {
	if store.deleteErr != nil {
		return store.deleteErr
	}
	store.deletedEntries = append(store.deletedEntries, entryID)
	remaining := store.acquired[:0]
	for _, track := range store.acquired {
		if track.EntryID != entryID {
			remaining = append(remaining, track)
		}
	}
	store.acquired = remaining
	return nil
}

func (store *syncStore) UpsertNavidromePlaylist(
	_ context.Context, sourceID, name string,
) (db.PlaylistRow, error) {
	if store.upsertErr != nil {
		return db.PlaylistRow{}, store.upsertErr
	}
	store.upserted = append(store.upserted, sourceID)
	store.adopted.Name = name
	return store.adopted, nil
}

func (store *syncStore) LibraryFileByPath(_ context.Context, path string) (db.LibraryFileRefRow, error) {
	if store.fileErr != nil {
		return db.LibraryFileRefRow{}, store.fileErr
	}
	file, found := store.filesByPath[path]
	if !found {
		return db.LibraryFileRefRow{}, pgx.ErrNoRows
	}
	return file, nil
}

func (store *syncStore) NavidromePlaylistSnapshot(
	context.Context, uuid.UUID,
) (db.NavidromePlaylistSnapshotRow, error) {
	return store.snapshot, store.snapshotErr
}

func (store *syncStore) NavidromePlaylistSnapshotTracks(
	context.Context, uuid.UUID,
) ([]db.NavidromePlaylistSnapshotTrackRow, error) {
	return store.snapshotTracks, nil
}

func (store *syncStore) SnapshotByNavidromeID(
	_ context.Context, navidromeID string,
) (db.NavidromePlaylistSnapshotRow, error) {
	snapshot, found := store.byRemoteID[navidromeID]
	if !found {
		return db.NavidromePlaylistSnapshotRow{}, pgx.ErrNoRows
	}
	return snapshot, nil
}

func (store *syncStore) ReplaceNavidromePlaylistSnapshot(
	_ context.Context,
	header db.NavidromePlaylistSnapshotParams,
	tracks []db.NavidromePlaylistSnapshotTrackParams,
) error {
	if store.writeErr != nil {
		return store.writeErr
	}
	store.writes = append(store.writes, writtenSnapshot{header: header, tracks: tracks})
	return nil
}

func (store *syncStore) RepairNavidromeSnapshotTrack(
	_ context.Context, playlistID uuid.UUID, position int32, songID string,
) error {
	if store.repairErr != nil {
		return store.repairErr
	}
	store.repairs = append(store.repairs, repairedTrack{
		playlistID: playlistID, position: position, songID: songID,
	})
	return nil
}

func (store *syncStore) DeleteNavidromePlaylistSnapshot(context.Context, uuid.UUID) error {
	store.snapshotDropped = true
	return nil
}

func (store *syncStore) QueueNavidromePlaylistSync(_ context.Context, playlistID uuid.UUID) error {
	if store.queueErr != nil {
		return store.queueErr
	}
	store.queued = append(store.queued, playlistID)
	return nil
}

// syncPlayer answers for Navidrome. A song it does not hold is ErrNotFound,
// which is the answer the pass reasons from and never an error.
type syncPlayer struct {
	lists   []Playlist
	details map[string]Playlist
	songs   map[string]Song
	byPath  map[string]Song
	// misses answers for a path the player does not hold with a described
	// refusal rather than a bare one, so a test can watch what the client said
	// about a failed search travel out to whoever has to act on it.
	misses map[string]*LookupMiss
	// lookups is every question put to the player, in order.
	lookups []SongLookup
	// deadlineAfter makes the player stop answering once this many files have
	// been asked about, the way one too slow for a request's budget does.
	deadlineAfter int

	listsErr     error
	findErr      error
	songErr      error
	createErr    error
	createdName  string
	createdSongs []string
	createdID    string
	updates      []PlaylistUpdate
}

func (player *syncPlayer) Playlists(context.Context) ([]Playlist, error) {
	return player.lists, player.listsErr
}

func (player *syncPlayer) Playlist(_ context.Context, id string) (Playlist, error) {
	playlist, found := player.details[id]
	if !found {
		return Playlist{}, ErrNotFound
	}
	return playlist, nil
}

func (player *syncPlayer) CreatePlaylist(
	_ context.Context, name string, songIDs []string,
) (string, error) {
	if player.createErr != nil {
		return "", player.createErr
	}
	player.createdName = name
	player.createdSongs = songIDs
	if player.createdID == "" {
		player.createdID = "nd-new"
	}
	return player.createdID, nil
}

func (player *syncPlayer) UpdatePlaylist(_ context.Context, update PlaylistUpdate) error {
	player.updates = append(player.updates, update)
	return nil
}

func (player *syncPlayer) Song(_ context.Context, id string) (Song, error) {
	if player.songErr != nil {
		return Song{}, player.songErr
	}
	song, found := player.songs[id]
	if !found {
		return Song{}, ErrNotFound
	}
	return song, nil
}

func (player *syncPlayer) FindSongByPath(_ context.Context, lookup SongLookup) (SongMatch, error) {
	player.lookups = append(player.lookups, lookup)
	if player.findErr != nil {
		return SongMatch{}, player.findErr
	}
	if player.deadlineAfter > 0 && len(player.lookups) > player.deadlineAfter {
		return SongMatch{}, fmt.Errorf("ask navidrome: %w", context.DeadlineExceeded)
	}
	song, found := player.byPath[lookup.Path]
	if !found {
		if miss, described := player.misses[lookup.Path]; described {
			return SongMatch{}, miss
		}
		return SongMatch{}, &LookupMiss{Path: lookup.Path, Reason: MissNoCandidates}
	}
	return SongMatch{Song: song}, nil
}

func syncerFor(store *syncStore, player *syncPlayer, pairs string, t *testing.T) *Syncer {
	t.Helper()
	pathMap, err := ParsePathMap(pairs)
	if err != nil {
		t.Fatalf("ParsePathMap() error = %v", err)
	}
	syncer := NewSyncer(store, pathMap, zerolog.Nop())
	syncer.newClient = func(Options) (playlistClient, error) { return player, nil }
	return syncer
}

func configuredPlayer() db.NavidromeSettingsRow {
	return db.NavidromeSettingsRow{
		BaseURL: "http://navidrome:4533", Username: "poldi", Password: "hunter2", Enabled: true,
	}
}

// joinedList is a want list with one acquired file, already pushed under one
// song id: the state most of these tests start from.
func joinedList(t *testing.T) (*syncStore, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	playlistID, entryID, fileID := uuid.New(), uuid.New(), uuid.New()
	store := &syncStore{
		settings: configuredPlayer(),
		playlist: db.PlaylistRow{ID: playlistID, Name: "musik", Source: "spotify"},
		acquired: []db.AcquiredPlaylistTrackRow{{
			EntryID: entryID, Position: 1, LibraryFileID: fileID,
			LibraryPath: "/music/artist/one.flac",
		}},
		snapshot: db.NavidromePlaylistSnapshotRow{
			PlaylistID: playlistID, NavidromePlaylistID: "nd-1",
		},
		snapshotTracks: []db.NavidromePlaylistSnapshotTrackRow{{
			PlaylistID: playlistID, Position: 1, NavidromeSongID: "song-1",
			LibraryPath:     "/music/artist/one.flac",
			LibraryFileID:   uuid.NullUUID{UUID: fileID, Valid: true},
			PlaylistEntryID: uuid.NullUUID{UUID: entryID, Valid: true},
		}},
		filesByPath: map[string]db.LibraryFileRefRow{
			"/music/artist/one.flac": {ID: fileID, Path: "/music/artist/one.flac"},
		},
		filesByID: map[uuid.UUID]string{fileID: "/music/artist/one.flac"},
	}
	return store, playlistID, entryID, fileID
}

// An installation that plays its music some other way syncs nothing and is not
// a broken one.
func TestAnInstallationWithNoPlayerSyncsNothing(t *testing.T) {
	store := &syncStore{settingsErr: pgx.ErrNoRows}
	player := &syncPlayer{}

	result, err := syncerFor(store, player, "", t).SyncPlaylist(context.Background(), uuid.New())

	if err != nil {
		t.Fatalf("SyncPlaylist() error = %v", err)
	}
	if result.Configured {
		t.Error("a pass ran against a player nobody configured")
	}
	if len(store.writes) != 0 {
		t.Error("a snapshot was written for a player nobody configured")
	}
}

func TestAPlayerSwitchedOffIsNotSyncedWith(t *testing.T) {
	settings := configuredPlayer()
	settings.Enabled = false
	store := &syncStore{settings: settings}

	result, err := syncerFor(store, &syncPlayer{}, "", t).SyncPlaylist(context.Background(), uuid.New())

	if err != nil {
		t.Fatalf("SyncPlaylist() error = %v", err)
	}
	if result.Configured {
		t.Error("a pass ran against a player that is switched off")
	}
}

func TestASweepAgainstNoPlayerDoesNothing(t *testing.T) {
	store := &syncStore{settingsErr: pgx.ErrNoRows}

	result, err := syncerFor(store, &syncPlayer{}, "", t).Sweep(context.Background())

	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if result.Configured || len(store.queued) != 0 {
		t.Error("a sweep ran against a player nobody configured")
	}
}

// Navidrome copies DefaultReportRealPath onto a player row the first time a
// client calls under a name and never re-reads it, and "schall" was created by
// the rescan trigger before anybody needed real paths (docs/decisions/0010).
func TestSyncIntroducesItselfUnderItsOwnClientName(t *testing.T) {
	var names []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		names = append(names, r.URL.Query().Get("c"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"subsonic-response":{"status":"ok","version":"1.16.1","playlists":{}}}`))
	}))
	t.Cleanup(server.Close)

	settings := configuredPlayer()
	settings.BaseURL = server.URL
	store := &syncStore{settings: settings}

	if _, err := NewSyncer(store, PathMap{}, zerolog.Nop()).Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(names) == 0 {
		t.Fatal("the player was never asked anything")
	}
	for _, name := range names {
		if name != SyncClientName {
			t.Fatalf("client name = %q, want %q", name, SyncClientName)
		}
	}
}

func TestAFirstPushCreatesThePlaylistAndWritesTheSnapshot(t *testing.T) {
	store, playlistID, entryID, fileID := joinedList(t)
	store.snapshot, store.snapshotErr, store.snapshotTracks = db.NavidromePlaylistSnapshotRow{}, pgx.ErrNoRows, nil
	player := &syncPlayer{
		byPath:    map[string]Song{"/music/artist/one.flac": {ID: "song-1", Title: "One"}},
		createdID: "nd-created",
	}

	result, err := syncerFor(store, player, "", t).SyncPlaylist(context.Background(), playlistID)

	if err != nil {
		t.Fatalf("SyncPlaylist() error = %v", err)
	}
	if !result.Created || result.Pushed != 1 {
		t.Fatalf("result = %#v, want the list created with its one acquired track", result)
	}
	if player.createdName != "musik" || len(player.createdSongs) != 1 || player.createdSongs[0] != "song-1" {
		t.Fatalf("created %q with %v", player.createdName, player.createdSongs)
	}
	if len(store.writes) != 1 {
		t.Fatalf("writes = %d, want the one snapshot the push made", len(store.writes))
	}
	written := store.writes[0]
	if written.header.NavidromePlaylistID != "nd-created" || written.header.Adopted {
		t.Fatalf("header = %#v, want the list Schall just created", written.header)
	}
	if len(written.tracks) != 1 {
		t.Fatalf("tracks = %#v, want the one that was pushed", written.tracks)
	}
	track := written.tracks[0]
	if track.Position != 1 || track.NavidromeSongID != "song-1" ||
		track.LibraryPath != "/music/artist/one.flac" ||
		track.LibraryFileID.UUID != fileID || track.PlaylistEntryID.UUID != entryID {
		t.Fatalf("track = %#v, want the id and path it was pushed at", track)
	}
}

// The refusal at the heart of the pass. Every reason it can happen makes the
// comparison meaningless, so nothing is written and no playlist is made.
func TestAnAcquiredSubsetThatPairsNothingRefusesThePass(t *testing.T) {
	store, playlistID, _, _ := joinedList(t)
	store.snapshot, store.snapshotErr, store.snapshotTracks = db.NavidromePlaylistSnapshotRow{}, pgx.ErrNoRows, nil
	player := &syncPlayer{}

	_, err := syncerFor(store, player, "", t).SyncPlaylist(context.Background(), playlistID)

	if !errors.Is(err, ErrNothingPaired) {
		t.Fatalf("error = %v, want the pass refused", err)
	}
	if player.createdName != "" {
		t.Error("a playlist was created for a pass that could pair nothing")
	}
	if len(store.writes) != 0 {
		t.Error("a snapshot was written for a pass that could pair nothing")
	}
}

func TestAPassOverAListNoFileIsHeldForCreatesNothing(t *testing.T) {
	store, playlistID, _, _ := joinedList(t)
	store.acquired = nil
	store.snapshot, store.snapshotErr, store.snapshotTracks = db.NavidromePlaylistSnapshotRow{}, pgx.ErrNoRows, nil
	player := &syncPlayer{}

	result, err := syncerFor(store, player, "", t).SyncPlaylist(context.Background(), playlistID)

	if err != nil {
		t.Fatalf("SyncPlaylist() error = %v", err)
	}
	if result.Created || player.createdName != "" {
		t.Error("an empty playlist was created in navidrome")
	}
}

// docs/decisions/0010: the id still means the file it was pushed for, so the
// track leaving the listing is the user having taken it out.
func TestAnAbsentTrackStillReportingItsPushedPathIsARemoval(t *testing.T) {
	store, playlistID, entryID, _ := joinedList(t)
	player := &syncPlayer{
		details: map[string]Playlist{"nd-1": {ID: "nd-1", Name: "musik"}},
		songs:   map[string]Song{"song-1": {ID: "song-1", Path: "/music/artist/one.flac"}},
		byPath:  map[string]Song{"/music/artist/one.flac": {ID: "song-1"}},
	}

	result, err := syncerFor(store, player, "", t).SyncPlaylist(context.Background(), playlistID)

	if err != nil {
		t.Fatalf("SyncPlaylist() error = %v", err)
	}
	if result.RemovalsActed != 1 {
		t.Fatalf("result = %#v, want the removal acted on", result)
	}
	if len(store.deletedEntries) != 1 || store.deletedEntries[0] != entryID {
		t.Fatalf("deleted = %v, want the entry it was pushed for", store.deletedEntries)
	}
}

func TestAnAbsentTrackReportingADifferentPathIsNotARemoval(t *testing.T) {
	store, playlistID, _, _ := joinedList(t)
	player := &syncPlayer{
		details: map[string]Playlist{"nd-1": {ID: "nd-1", Name: "musik"}},
		songs:   map[string]Song{"song-1": {ID: "song-1", Path: "/music/artist/somebody-else.flac"}},
		byPath:  map[string]Song{"/music/artist/one.flac": {ID: "song-9"}},
	}

	result, err := syncerFor(store, player, "", t).SyncPlaylist(context.Background(), playlistID)

	if err != nil {
		t.Fatalf("SyncPlaylist() error = %v", err)
	}
	if result.RemovalsActed != 0 || len(store.deletedEntries) != 0 {
		t.Fatalf("deleted = %v, want the entry left alone", store.deletedEntries)
	}
}

func TestAnAbsentTrackWhoseIdNoLongerResolvesIsNotARemoval(t *testing.T) {
	store, playlistID, _, _ := joinedList(t)
	player := &syncPlayer{
		details: map[string]Playlist{"nd-1": {ID: "nd-1", Name: "musik"}},
		songs:   map[string]Song{},
		byPath:  map[string]Song{"/music/artist/one.flac": {ID: "song-9"}},
	}

	result, err := syncerFor(store, player, "", t).SyncPlaylist(context.Background(), playlistID)

	if err != nil {
		t.Fatalf("SyncPlaylist() error = %v", err)
	}
	if result.RemovalsActed != 0 || len(store.deletedEntries) != 0 {
		t.Fatalf("deleted = %v, want the entry left alone", store.deletedEntries)
	}
}

// docs/decisions/0012: the file is found at exactly its path, so the snapshot
// is re-pointed at the id it answers to now. The path it was pushed at is the
// evidence and is not rewritten.
func TestABreakIsRepairedFromThePath(t *testing.T) {
	store, playlistID, _, _ := joinedList(t)
	player := &syncPlayer{
		details: map[string]Playlist{"nd-1": {ID: "nd-1", Name: "musik"}},
		songs:   map[string]Song{"song-1": {ID: "song-1", Path: "/music/artist/moved.flac"}},
		byPath:  map[string]Song{"/music/artist/one.flac": {ID: "song-new"}},
	}

	result, err := syncerFor(store, player, "", t).SyncPlaylist(context.Background(), playlistID)

	if err != nil {
		t.Fatalf("SyncPlaylist() error = %v", err)
	}
	if result.BreaksRepaired != 1 {
		t.Fatalf("result = %#v, want the break repaired", result)
	}
	if len(store.repairs) != 1 {
		t.Fatalf("repairs = %v", store.repairs)
	}
	repair := store.repairs[0]
	if repair.playlistID != playlistID || repair.position != 1 || repair.songID != "song-new" {
		t.Fatalf("repair = %#v, want the pushed position re-pointed at the new id", repair)
	}
}

func TestABreakNobodyCanFindIsLeftAloneAndReported(t *testing.T) {
	store, playlistID, _, _ := joinedList(t)
	// A second file the player does know about, so that the pass gets past the
	// refusal and the broken one is the only thing being decided here.
	secondID := uuid.New()
	store.acquired = append(store.acquired, db.AcquiredPlaylistTrackRow{
		EntryID: uuid.New(), Position: 2, LibraryFileID: secondID,
		LibraryPath: "/music/artist/two.flac",
	})
	player := &syncPlayer{
		details: map[string]Playlist{"nd-1": {ID: "nd-1", Name: "musik", Entries: []Song{
			{ID: "song-2", Path: "/music/artist/two.flac"},
		}}},
		songs:  map[string]Song{},
		byPath: map[string]Song{"/music/artist/two.flac": {ID: "song-2"}},
	}

	result, err := syncerFor(store, player, "", t).SyncPlaylist(context.Background(), playlistID)

	if err != nil {
		t.Fatalf("SyncPlaylist() error = %v", err)
	}
	if len(result.BreaksLeft) != 1 || result.BreaksLeft[0] != "/music/artist/one.flac" {
		t.Fatalf("result = %#v, want the break reported", result)
	}
	if len(store.repairs) != 0 || len(store.deletedEntries) != 0 {
		t.Fatal("a break nobody could find was acted on")
	}
}

// ADR 0004's whole point: a want nobody has acquired was never pushed, so
// nothing about the player's listing can be read as a statement about it.
func TestAnUnacquiredWantIsNeverPushedAndNeverRemoved(t *testing.T) {
	store, playlistID, _, fileID := joinedList(t)
	// The list holds two wants and one file: the second entry is acquired by
	// nobody, so it is absent from the acquired subset by construction.
	player := &syncPlayer{
		details: map[string]Playlist{"nd-1": {
			ID: "nd-1", Name: "musik", Entries: []Song{{ID: "song-1", Path: "/music/artist/one.flac"}},
		}},
		songs:  map[string]Song{"song-1": {ID: "song-1", Path: "/music/artist/one.flac"}},
		byPath: map[string]Song{"/music/artist/one.flac": {ID: "song-1"}},
	}

	result, err := syncerFor(store, player, "", t).SyncPlaylist(context.Background(), playlistID)

	if err != nil {
		t.Fatalf("SyncPlaylist() error = %v", err)
	}
	if len(store.deletedEntries) != 0 {
		t.Fatalf("deleted = %v, want nothing removed", store.deletedEntries)
	}
	if len(player.updates) != 0 {
		t.Fatalf("updates = %#v, want the listing left as it is", player.updates)
	}
	if result.Pushed != 1 {
		t.Fatalf("result = %#v, want only the acquired track pushed", result)
	}
	written := store.writes[len(store.writes)-1]
	if len(written.tracks) != 1 || written.tracks[0].LibraryFileID.UUID != fileID {
		t.Fatalf("snapshot = %#v, want only the file that exists", written.tracks)
	}
}

func TestASongAddedInNavidromeBecomesAnEntry(t *testing.T) {
	store, playlistID, _, _ := joinedList(t)
	addedID := uuid.New()
	store.filesByPath["/music/artist/two.flac"] = db.LibraryFileRefRow{
		ID: addedID, Path: "/music/artist/two.flac",
	}
	store.filesByID[addedID] = "/music/artist/two.flac"
	player := &syncPlayer{
		details: map[string]Playlist{"nd-1": {ID: "nd-1", Name: "musik", Entries: []Song{
			{ID: "song-1", Path: "/music/artist/one.flac"},
			{ID: "song-2", Title: "Two", Path: "/music/artist/two.flac"},
		}}},
		songs: map[string]Song{"song-1": {ID: "song-1", Path: "/music/artist/one.flac"}},
		byPath: map[string]Song{
			"/music/artist/one.flac": {ID: "song-1"},
			"/music/artist/two.flac": {ID: "song-2"},
		},
	}

	result, err := syncerFor(store, player, "", t).SyncPlaylist(context.Background(), playlistID)

	if err != nil {
		t.Fatalf("SyncPlaylist() error = %v", err)
	}
	if result.AdditionsAdopted != 1 {
		t.Fatalf("result = %#v, want the addition taken in", result)
	}
	if len(store.appended) != 1 {
		t.Fatalf("appended = %#v", store.appended)
	}
	appended := store.appended[0]
	if appended.PlaylistID != playlistID || appended.LibraryFileID != addedID || appended.EntryTitle != "Two" {
		t.Fatalf("appended = %#v, want the file the user added", appended)
	}
	written := store.writes[len(store.writes)-1]
	if len(written.tracks) != 2 {
		t.Fatalf("snapshot = %#v, want both tracks pushed", written.tracks)
	}
}

func TestASongTheLibraryDoesNotHoldIsReportedAndNotAdopted(t *testing.T) {
	store, playlistID, _, _ := joinedList(t)
	player := &syncPlayer{
		details: map[string]Playlist{"nd-1": {ID: "nd-1", Name: "musik", Entries: []Song{
			{ID: "song-1", Path: "/music/artist/one.flac"},
			{ID: "song-elsewhere", Title: "Elsewhere", Path: "/music-soundcloud/mix.mp3"},
		}}},
		songs:  map[string]Song{"song-1": {ID: "song-1", Path: "/music/artist/one.flac"}},
		byPath: map[string]Song{"/music/artist/one.flac": {ID: "song-1"}},
	}

	result, err := syncerFor(store, player, "", t).SyncPlaylist(context.Background(), playlistID)

	if err != nil {
		t.Fatalf("SyncPlaylist() error = %v", err)
	}
	if len(store.appended) != 0 {
		t.Fatalf("appended = %#v, want nothing invented from a path", store.appended)
	}
	if len(result.UnknownAdditions) != 1 || result.UnknownAdditions[0] != "/music-soundcloud/mix.mp3" {
		t.Fatalf("result = %#v, want the song reported", result)
	}
}

// A song Schall never pushed is not Schall's to take out of the user's
// playlist, whatever the want list says about it.
func TestASongSchallNeverPushedIsLeftInThePlayersPlaylist(t *testing.T) {
	store, playlistID, _, _ := joinedList(t)
	player := &syncPlayer{
		details: map[string]Playlist{"nd-1": {ID: "nd-1", Name: "musik", Entries: []Song{
			{ID: "song-1", Path: "/music/artist/one.flac"},
			{ID: "song-elsewhere", Path: "/music-soundcloud/mix.mp3"},
		}}},
		songs:  map[string]Song{"song-1": {ID: "song-1", Path: "/music/artist/one.flac"}},
		byPath: map[string]Song{"/music/artist/one.flac": {ID: "song-1"}},
	}

	if _, err := syncerFor(store, player, "", t).SyncPlaylist(context.Background(), playlistID); err != nil {
		t.Fatalf("SyncPlaylist() error = %v", err)
	}
	for _, update := range player.updates {
		if len(update.RemoveIndexes) != 0 {
			t.Fatalf("update = %#v, want nothing removed", update)
		}
	}
}

// A want that stopped being wanted is taken out of the listing, by the position
// it holds in the one reading this pass made.
func TestATrackSchallNoLongerWantsIsTakenOutOfTheListing(t *testing.T) {
	store, playlistID, _, _ := joinedList(t)
	store.acquired = nil
	player := &syncPlayer{
		details: map[string]Playlist{"nd-1": {ID: "nd-1", Name: "musik", Entries: []Song{
			{ID: "song-1", Path: "/music/artist/one.flac"},
		}}},
		songs:  map[string]Song{"song-1": {ID: "song-1", Path: "/music/artist/one.flac"}},
		byPath: map[string]Song{"/music/artist/one.flac": {ID: "song-1"}},
	}

	if _, err := syncerFor(store, player, "", t).SyncPlaylist(context.Background(), playlistID); err != nil {
		t.Fatalf("SyncPlaylist() error = %v", err)
	}
	if len(player.updates) != 1 {
		t.Fatalf("updates = %#v, want the one update the pass makes", player.updates)
	}
	if len(player.updates[0].RemoveIndexes) != 1 || player.updates[0].RemoveIndexes[0] != 0 {
		t.Fatalf("update = %#v, want the position it holds in the listing", player.updates[0])
	}
}

func TestAnAcquiredTrackTheListingDoesNotHoldIsAdded(t *testing.T) {
	store, playlistID, _, _ := joinedList(t)
	store.snapshotTracks = nil
	player := &syncPlayer{
		details: map[string]Playlist{"nd-1": {ID: "nd-1", Name: "musik"}},
		byPath:  map[string]Song{"/music/artist/one.flac": {ID: "song-1"}},
	}

	if _, err := syncerFor(store, player, "", t).SyncPlaylist(context.Background(), playlistID); err != nil {
		t.Fatalf("SyncPlaylist() error = %v", err)
	}
	if len(player.updates) != 1 || len(player.updates[0].AddSongIDs) != 1 ||
		player.updates[0].AddSongIDs[0] != "song-1" {
		t.Fatalf("updates = %#v, want the acquired track added", player.updates)
	}
}

// The list was deleted in Navidrome. The snapshot goes with it and nothing is
// made again in this pass; a later one finds a list with no snapshot.
func TestARemotePlaylistThatWasDeletedDropsTheSnapshot(t *testing.T) {
	store, playlistID, _, _ := joinedList(t)
	player := &syncPlayer{
		byPath: map[string]Song{"/music/artist/one.flac": {ID: "song-1"}},
	}

	result, err := syncerFor(store, player, "", t).SyncPlaylist(context.Background(), playlistID)

	if err != nil {
		t.Fatalf("SyncPlaylist() error = %v", err)
	}
	if !result.RemoteDeleted || !store.snapshotDropped {
		t.Fatalf("result = %#v, want the snapshot forgotten", result)
	}
	if player.createdName != "" || len(store.writes) != 0 {
		t.Error("the deleted playlist was made again in the same pass")
	}
}

func TestTheSweepAdoptsAPlaylistTheAccountOwns(t *testing.T) {
	fileID, adoptedID := uuid.New(), uuid.New()
	store := &syncStore{
		settings: configuredPlayer(),
		adopted:  db.PlaylistRow{ID: adoptedID, Source: "navidrome"},
		filesByPath: map[string]db.LibraryFileRefRow{
			"/music/artist/one.flac": {ID: fileID, Path: "/music/artist/one.flac"},
		},
		filesByID: map[uuid.UUID]string{fileID: "/music/artist/one.flac"},
	}
	player := &syncPlayer{
		lists: []Playlist{{ID: "nd-7", Name: "abends", Owner: "poldi"}},
		details: map[string]Playlist{"nd-7": {ID: "nd-7", Name: "abends", Owner: "poldi", Entries: []Song{
			{ID: "song-1", Title: "One", Path: "/music/artist/one.flac"},
			{ID: "song-2", Title: "Two", Path: "/elsewhere/two.flac"},
		}}},
	}

	result, err := syncerFor(store, player, "", t).Sweep(context.Background())

	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(result.Adopted) != 1 || result.Adopted[0] != adoptedID {
		t.Fatalf("result = %#v, want the list adopted", result)
	}
	if len(store.upserted) != 1 || store.upserted[0] != "nd-7" {
		t.Fatalf("upserted = %v, want the remote list recorded as the source", store.upserted)
	}
	if len(store.appended) != 1 || store.appended[0].LibraryFileID != fileID {
		t.Fatalf("appended = %#v, want an entry for the song the library holds", store.appended)
	}
	if len(result.UnknownAdditions) != 1 || result.UnknownAdditions[0] != "/elsewhere/two.flac" {
		t.Fatalf("result = %#v, want the song the library does not hold reported", result)
	}
	if len(store.writes) != 1 || !store.writes[0].header.Adopted {
		t.Fatalf("writes = %#v, want a snapshot marked adopted", store.writes)
	}
}

func TestTheSweepLeavesAnotherAccountsPlaylistAlone(t *testing.T) {
	store := &syncStore{settings: configuredPlayer()}
	player := &syncPlayer{
		lists:   []Playlist{{ID: "nd-8", Name: "theirs", Owner: "somebody-else"}},
		details: map[string]Playlist{"nd-8": {ID: "nd-8", Name: "theirs", Owner: "somebody-else"}},
	}

	result, err := syncerFor(store, player, "", t).Sweep(context.Background())

	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(result.Adopted) != 0 || len(store.upserted) != 0 {
		t.Fatalf("adopted = %v, want another account's list left alone", store.upserted)
	}
}

func TestTheSweepSkipsAListItHasAlreadyJoined(t *testing.T) {
	store := &syncStore{
		settings: configuredPlayer(),
		byRemoteID: map[string]db.NavidromePlaylistSnapshotRow{
			"nd-9": {PlaylistID: uuid.New(), NavidromePlaylistID: "nd-9"},
		},
	}
	player := &syncPlayer{
		lists:   []Playlist{{ID: "nd-9", Name: "joined", Owner: "poldi"}},
		details: map[string]Playlist{"nd-9": {ID: "nd-9", Name: "joined", Owner: "poldi"}},
	}

	result, err := syncerFor(store, player, "", t).Sweep(context.Background())

	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(result.Adopted) != 0 || len(store.upserted) != 0 {
		t.Fatalf("adopted = %v, want a joined list left alone", store.upserted)
	}
}

func TestTheSweepAsksForAPassOverEveryList(t *testing.T) {
	first, second := uuid.New(), uuid.New()
	store := &syncStore{
		settings: configuredPlayer(),
		lists:    []db.PlaylistRow{{ID: first}, {ID: second}},
	}

	result, err := syncerFor(store, &syncPlayer{}, "", t).Sweep(context.Background())

	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if result.Queued != 2 || len(store.queued) != 2 ||
		store.queued[0] != first || store.queued[1] != second {
		t.Fatalf("queued = %v, want a pass per list", store.queued)
	}
}

// Schall and Navidrome read the same directory through different mounts, so the
// path Schall holds is put in the player's terms before it is asked about.
func TestThePlayerIsAskedAboutTheMappedPath(t *testing.T) {
	store, playlistID, _, _ := joinedList(t)
	store.snapshot, store.snapshotErr, store.snapshotTracks = db.NavidromePlaylistSnapshotRow{}, pgx.ErrNoRows, nil
	player := &syncPlayer{
		byPath: map[string]Song{"/music-schall/artist/one.flac": {ID: "song-1"}},
	}

	result, err := syncerFor(store, player, "/music=/music-schall", t).
		SyncPlaylist(context.Background(), playlistID)

	if err != nil {
		t.Fatalf("SyncPlaylist() error = %v", err)
	}
	if result.Pushed != 1 {
		t.Fatalf("result = %#v, want the file paired through the mapping", result)
	}
}

// The snapshot is a record of what Schall pushed, in Schall's own terms: the
// path stored is the one Schall wrote, never the one the player reported.
func TestTheSnapshotRecordsThePathSchallOwns(t *testing.T) {
	store, playlistID, _, _ := joinedList(t)
	store.snapshot, store.snapshotErr, store.snapshotTracks = db.NavidromePlaylistSnapshotRow{}, pgx.ErrNoRows, nil
	player := &syncPlayer{
		byPath: map[string]Song{"/music-schall/artist/one.flac": {ID: "song-1"}},
	}

	if _, err := syncerFor(store, player, "/music=/music-schall", t).
		SyncPlaylist(context.Background(), playlistID); err != nil {
		t.Fatalf("SyncPlaylist() error = %v", err)
	}
	if len(store.writes) != 1 || store.writes[0].tracks[0].LibraryPath != "/music/artist/one.flac" {
		t.Fatalf("snapshot = %#v, want the schall path", store.writes)
	}
}

// The removal check compares what the player reports with the pushed path put
// in the player's terms; without the mapping every track would read as broken.
func TestARemovalIsRecognisedThroughTheMapping(t *testing.T) {
	store, playlistID, entryID, _ := joinedList(t)
	player := &syncPlayer{
		details: map[string]Playlist{"nd-1": {ID: "nd-1", Name: "musik"}},
		songs:   map[string]Song{"song-1": {ID: "song-1", Path: "/music-schall/artist/one.flac"}},
		byPath:  map[string]Song{"/music-schall/artist/one.flac": {ID: "song-1"}},
	}

	result, err := syncerFor(store, player, "/music=/music-schall", t).
		SyncPlaylist(context.Background(), playlistID)

	if err != nil {
		t.Fatalf("SyncPlaylist() error = %v", err)
	}
	if result.RemovalsActed != 1 || len(store.deletedEntries) != 1 || store.deletedEntries[0] != entryID {
		t.Fatalf("result = %#v, deleted = %v", result, store.deletedEntries)
	}
}

// A song the player holds is looked up in the library at the path Schall knows
// it by, which is the mapping run the other way.
func TestAnAdditionIsLookedUpAtTheSchallPath(t *testing.T) {
	store, playlistID, _, _ := joinedList(t)
	addedID := uuid.New()
	store.filesByPath["/music/artist/two.flac"] = db.LibraryFileRefRow{
		ID: addedID, Path: "/music/artist/two.flac",
	}
	store.filesByID[addedID] = "/music/artist/two.flac"
	player := &syncPlayer{
		details: map[string]Playlist{"nd-1": {ID: "nd-1", Name: "musik", Entries: []Song{
			{ID: "song-1", Path: "/music-schall/artist/one.flac"},
			{ID: "song-2", Title: "Two", Path: "/music-schall/artist/two.flac"},
		}}},
		songs: map[string]Song{"song-1": {ID: "song-1", Path: "/music-schall/artist/one.flac"}},
		byPath: map[string]Song{
			"/music-schall/artist/one.flac": {ID: "song-1"},
			"/music-schall/artist/two.flac": {ID: "song-2"},
		},
	}

	result, err := syncerFor(store, player, "/music=/music-schall", t).
		SyncPlaylist(context.Background(), playlistID)

	if err != nil {
		t.Fatalf("SyncPlaylist() error = %v", err)
	}
	if result.AdditionsAdopted != 1 || len(store.appended) != 1 ||
		store.appended[0].LibraryFileID != addedID {
		t.Fatalf("result = %#v, appended = %#v", result, store.appended)
	}
}

// An entry whose title the player has nothing to say about still has to be
// identifiable, so the file names it.
func TestASongWithNoTitleIsNamedAfterItsFile(t *testing.T) {
	store, playlistID, _, _ := joinedList(t)
	addedID := uuid.New()
	store.filesByPath["/music/artist/two.flac"] = db.LibraryFileRefRow{
		ID: addedID, Path: "/music/artist/two.flac",
	}
	store.filesByID[addedID] = "/music/artist/two.flac"
	player := &syncPlayer{
		details: map[string]Playlist{"nd-1": {ID: "nd-1", Name: "musik", Entries: []Song{
			{ID: "song-1", Path: "/music/artist/one.flac"},
			{ID: "song-2", Path: "/music/artist/two.flac"},
		}}},
		songs: map[string]Song{"song-1": {ID: "song-1", Path: "/music/artist/one.flac"}},
		byPath: map[string]Song{
			"/music/artist/one.flac": {ID: "song-1"},
			"/music/artist/two.flac": {ID: "song-2"},
		},
	}

	if _, err := syncerFor(store, player, "", t).SyncPlaylist(context.Background(), playlistID); err != nil {
		t.Fatalf("SyncPlaylist() error = %v", err)
	}
	if len(store.appended) != 1 || store.appended[0].EntryTitle != "two.flac" {
		t.Fatalf("appended = %#v, want the file name", store.appended)
	}
}

// A pushed track whose entry is already gone has nothing left to remove.
func TestARemovalWithNoEntryLeftIsReportedAndNotActedOn(t *testing.T) {
	store, playlistID, _, _ := joinedList(t)
	store.snapshotTracks[0].PlaylistEntryID = uuid.NullUUID{}
	player := &syncPlayer{
		details: map[string]Playlist{"nd-1": {ID: "nd-1", Name: "musik"}},
		songs:   map[string]Song{"song-1": {ID: "song-1", Path: "/music/artist/one.flac"}},
		byPath:  map[string]Song{"/music/artist/one.flac": {ID: "song-1"}},
	}

	result, err := syncerFor(store, player, "", t).SyncPlaylist(context.Background(), playlistID)

	if err != nil {
		t.Fatalf("SyncPlaylist() error = %v", err)
	}
	if result.RemovalsWithoutEntry != 1 || len(store.deletedEntries) != 0 {
		t.Fatalf("result = %#v, deleted = %v", result, store.deletedEntries)
	}
}

// Settings nobody could read are not the same as no player configured: there
// may well be one, and nobody knows.
func TestSettingsThatCouldNotBeReadStopThePass(t *testing.T) {
	store := &syncStore{settingsErr: errors.New("the database is not answering")}

	_, err := syncerFor(store, &syncPlayer{}, "", t).SyncPlaylist(context.Background(), uuid.New())

	if err == nil {
		t.Fatal("settings nobody could read were treated as no player at all")
	}
}

// A player whose stored address is unusable is reported rather than quietly
// counted as synced.
func TestAPlayerWithNoUsableAddressIsReported(t *testing.T) {
	settings := configuredPlayer()
	settings.BaseURL = ""
	store := &syncStore{settings: settings}

	_, err := NewSyncer(store, PathMap{}, zerolog.Nop()).SyncPlaylist(context.Background(), uuid.New())

	if err == nil {
		t.Fatal("a player nobody could address was reported as synced")
	}
}

// A player that could not be asked is a pass that cannot be made, not a pass
// that found nothing: the difference is what stops an outage reading as a
// library nobody holds.
func TestAPlayerThatCouldNotBeAskedStopsThePass(t *testing.T) {
	store, playlistID, _, _ := joinedList(t)
	store.snapshot, store.snapshotErr, store.snapshotTracks = db.NavidromePlaylistSnapshotRow{}, pgx.ErrNoRows, nil
	player := &syncPlayer{findErr: errors.New("navidrome is not ready")}

	_, err := syncerFor(store, player, "", t).SyncPlaylist(context.Background(), playlistID)

	if err == nil {
		t.Fatal("a player that could not be asked was read as holding nothing")
	}
	if len(store.writes) != 0 {
		t.Error("a snapshot was written for a pass that could not ask anything")
	}
}

// An id that could not be asked about is not an id that resolved to nothing,
// and reading it as one would delete an entry on the strength of an outage.
func TestAnIdThatCouldNotBeAskedAboutIsNotARemoval(t *testing.T) {
	store, playlistID, _, _ := joinedList(t)
	player := &syncPlayer{
		details: map[string]Playlist{"nd-1": {ID: "nd-1", Name: "musik"}},
		songErr: errors.New("navidrome is not ready"),
		byPath:  map[string]Song{"/music/artist/one.flac": {ID: "song-1"}},
	}

	_, err := syncerFor(store, player, "", t).SyncPlaylist(context.Background(), playlistID)

	if err == nil {
		t.Fatal("an outage was read as an answer about the id")
	}
	if len(store.deletedEntries) != 0 {
		t.Fatalf("deleted = %v, want nothing removed", store.deletedEntries)
	}
}

// A playlist Navidrome would not make is reported, and no snapshot claims it
// was made.
func TestAPlaylistThePlayerRefusedToMakeIsReported(t *testing.T) {
	store, playlistID, _, _ := joinedList(t)
	store.snapshot, store.snapshotErr, store.snapshotTracks = db.NavidromePlaylistSnapshotRow{}, pgx.ErrNoRows, nil
	player := &syncPlayer{
		byPath:    map[string]Song{"/music/artist/one.flac": {ID: "song-1"}},
		createErr: errors.New("navidrome refused the request"),
	}

	_, err := syncerFor(store, player, "", t).SyncPlaylist(context.Background(), playlistID)

	if err == nil {
		t.Fatal("a playlist that was never made was reported as made")
	}
	if len(store.writes) != 0 {
		t.Error("a snapshot was written for a playlist that does not exist")
	}
}

// The snapshot is the only witness to what the player was told, so a push
// nobody could write down is a failed pass rather than a quiet one.
func TestAPushNobodyCouldWriteDownIsReported(t *testing.T) {
	store, playlistID, _, _ := joinedList(t)
	store.snapshot, store.snapshotErr, store.snapshotTracks = db.NavidromePlaylistSnapshotRow{}, pgx.ErrNoRows, nil
	store.writeErr = errors.New("the database is not answering")
	player := &syncPlayer{byPath: map[string]Song{"/music/artist/one.flac": {ID: "song-1"}}}

	_, err := syncerFor(store, player, "", t).SyncPlaylist(context.Background(), playlistID)

	if err == nil {
		t.Fatal("a push nobody recorded was reported as complete")
	}
}

// An entry the store would not delete is a removal that did not happen.
func TestARemovalTheStoreRefusedIsReported(t *testing.T) {
	store, playlistID, _, _ := joinedList(t)
	store.deleteErr = errors.New("the database is not answering")
	player := &syncPlayer{
		details: map[string]Playlist{"nd-1": {ID: "nd-1", Name: "musik"}},
		songs:   map[string]Song{"song-1": {ID: "song-1", Path: "/music/artist/one.flac"}},
		byPath:  map[string]Song{"/music/artist/one.flac": {ID: "song-1"}},
	}

	_, err := syncerFor(store, player, "", t).SyncPlaylist(context.Background(), playlistID)

	if err == nil {
		t.Fatal("a removal that failed was reported as made")
	}
}

// An entry that was already gone when the delete landed is the same answer as
// one that was never there: nothing left to remove.
func TestARemovalOfAnEntryAlreadyGoneIsNotAFailure(t *testing.T) {
	store, playlistID, _, _ := joinedList(t)
	store.deleteErr = pgx.ErrNoRows
	player := &syncPlayer{
		details: map[string]Playlist{"nd-1": {ID: "nd-1", Name: "musik"}},
		songs:   map[string]Song{"song-1": {ID: "song-1", Path: "/music/artist/one.flac"}},
		byPath:  map[string]Song{"/music/artist/one.flac": {ID: "song-1"}},
	}

	result, err := syncerFor(store, player, "", t).SyncPlaylist(context.Background(), playlistID)

	if err != nil {
		t.Fatalf("SyncPlaylist() error = %v", err)
	}
	if result.RemovalsWithoutEntry != 1 || result.RemovalsActed != 0 {
		t.Fatalf("result = %#v, want nothing left to remove", result)
	}
}

// A library that could not be read is not a library that does not hold the
// song, and adopting nothing on the strength of an outage would lose the
// addition.
func TestALibraryThatCouldNotBeReadStopsTheAdoption(t *testing.T) {
	store, playlistID, _, _ := joinedList(t)
	store.fileErr = errors.New("the database is not answering")
	player := &syncPlayer{
		details: map[string]Playlist{"nd-1": {ID: "nd-1", Name: "musik", Entries: []Song{
			{ID: "song-1", Path: "/music/artist/one.flac"},
			{ID: "song-2", Path: "/music/artist/two.flac"},
		}}},
		songs:  map[string]Song{"song-1": {ID: "song-1", Path: "/music/artist/one.flac"}},
		byPath: map[string]Song{"/music/artist/one.flac": {ID: "song-1"}},
	}

	_, err := syncerFor(store, player, "", t).SyncPlaylist(context.Background(), playlistID)

	if err == nil {
		t.Fatal("an outage was read as a library that holds nothing")
	}
	if len(store.appended) != 0 {
		t.Fatalf("appended = %#v, want nothing added", store.appended)
	}
}

// A sweep that could not ask for a pass says so: a list nobody queued is a list
// nothing ever pushes.
func TestASweepThatCouldNotQueueAPassIsReported(t *testing.T) {
	store := &syncStore{
		settings: configuredPlayer(),
		lists:    []db.PlaylistRow{{ID: uuid.New()}},
		queueErr: errors.New("the database is not answering"),
	}

	_, err := syncerFor(store, &syncPlayer{}, "", t).Sweep(context.Background())

	if err == nil {
		t.Fatal("a sweep that queued nothing was reported as complete")
	}
}

// The snapshot is what a pass compares against. Without it there is nothing to
// compare, and pushing anyway would read the player's listing as the truth.
func TestASnapshotThatCouldNotBeReadStopsThePass(t *testing.T) {
	store, playlistID, _, _ := joinedList(t)
	store.snapshotErr = errors.New("the database is not answering")
	player := &syncPlayer{byPath: map[string]Song{"/music/artist/one.flac": {ID: "song-1"}}}

	_, err := syncerFor(store, player, "", t).SyncPlaylist(context.Background(), playlistID)

	if err == nil {
		t.Fatal("a pass ran without knowing what had been pushed")
	}
	if player.createdName != "" || len(store.writes) != 0 {
		t.Error("a pass that could not read its snapshot wrote one")
	}
}

// A repair that was not recorded has not happened: the next pass would ask the
// old id again and read the same break.
func TestARepairThatCouldNotBeRecordedIsReported(t *testing.T) {
	store, playlistID, _, _ := joinedList(t)
	store.repairErr = errors.New("the database is not answering")
	player := &syncPlayer{
		details: map[string]Playlist{"nd-1": {ID: "nd-1", Name: "musik"}},
		songs:   map[string]Song{"song-1": {ID: "song-1", Path: "/music/artist/moved.flac"}},
		byPath:  map[string]Song{"/music/artist/one.flac": {ID: "song-new"}},
	}

	_, err := syncerFor(store, player, "", t).SyncPlaylist(context.Background(), playlistID)

	if err == nil {
		t.Fatal("a repair that was not recorded was reported as made")
	}
}

// A player that could not be listed is not a player holding no playlists.
func TestASweepAgainstAPlayerThatWouldNotAnswerIsReported(t *testing.T) {
	store := &syncStore{settings: configuredPlayer()}
	player := &syncPlayer{listsErr: errors.New("navidrome is not ready")}

	_, err := syncerFor(store, player, "", t).Sweep(context.Background())

	if err == nil {
		t.Fatal("a player that would not answer was read as holding nothing")
	}
	if len(store.queued) != 0 {
		t.Error("passes were queued for a sweep that never read the player")
	}
}

// A list that could not be recorded is not adopted, and nothing about it is
// written down as though it had been.
func TestAnAdoptionThatCouldNotBeRecordedIsReported(t *testing.T) {
	store := &syncStore{settings: configuredPlayer(), upsertErr: errors.New("the database is not answering")}
	player := &syncPlayer{
		lists:   []Playlist{{ID: "nd-7", Name: "abends", Owner: "poldi"}},
		details: map[string]Playlist{"nd-7": {ID: "nd-7", Name: "abends", Owner: "poldi"}},
	}

	_, err := syncerFor(store, player, "", t).Sweep(context.Background())

	if err == nil {
		t.Fatal("a list nobody recorded was reported as adopted")
	}
	if len(store.writes) != 0 {
		t.Error("a snapshot was written for a list that was never adopted")
	}
}

// A list the player would not describe is not adopted with no tracks: the
// entries are what the adoption is made of.
func TestAnAdoptionOfAListThePlayerWillNotDescribeIsReported(t *testing.T) {
	store := &syncStore{settings: configuredPlayer(), adopted: db.PlaylistRow{ID: uuid.New()}}
	player := &syncPlayer{lists: []Playlist{{ID: "nd-7", Name: "abends", Owner: "poldi"}}}

	_, err := syncerFor(store, player, "", t).Sweep(context.Background())

	if err == nil {
		t.Fatal("a list nobody could read was adopted empty")
	}
	if len(store.writes) != 0 {
		t.Error("a snapshot was written for a list nobody could read")
	}
}

// Navidrome indexes tags, not paths. The words Schall read from the file are
// what it is filed under there, and a file name Schall composed is not — it
// carries the position a track had in the run that acquired it, which no tag
// holds and no search matches.
func TestAPassAsksThePlayerAboutTheWordsSchallHoldsForTheFile(t *testing.T) {
	store, playlistID, _, _ := joinedList(t)
	store.acquired[0].Artist = "Raär"
	store.acquired[0].Title = "Sometimes I Hear Sirens"
	player := &syncPlayer{
		byPath:  map[string]Song{"/music/artist/one.flac": {ID: "song-1"}},
		details: map[string]Playlist{"nd-1": {ID: "nd-1", Entries: []Song{{ID: "song-1"}}}},
		songs:   map[string]Song{"song-1": {ID: "song-1", Path: "/music/artist/one.flac"}},
	}

	if _, err := syncerFor(store, player, "", t).SyncPlaylist(context.Background(), playlistID); err != nil {
		t.Fatalf("SyncPlaylist() error = %v", err)
	}

	if len(player.lookups) == 0 {
		t.Fatal("the player was never asked about the file")
	}
	asked := player.lookups[0]
	if asked.Artist != "Raär" || asked.Title != "Sometimes I Hear Sirens" {
		t.Errorf("lookup = %+v, want the words Schall holds for the file", asked)
	}
}

// The count of unpaired files says something is wrong and nothing about what.
// Which of the causes a deployment has is in the refusal, so the pass carries
// it out rather than counting it.
func TestAnUnpairedFileCarriesWhyThePlayerCouldNotAnswer(t *testing.T) {
	store, playlistID, entryID, _ := joinedList(t)
	store.acquired = append(store.acquired, db.AcquiredPlaylistTrackRow{
		EntryID: uuid.New(), Position: 2, LibraryFileID: uuid.New(),
		LibraryPath: "/music/artist/two.flac",
	})
	player := &syncPlayer{
		byPath:  map[string]Song{"/music/artist/one.flac": {ID: "song-1"}},
		details: map[string]Playlist{"nd-1": {ID: "nd-1", Entries: []Song{{ID: "song-1"}}}},
		songs:   map[string]Song{"song-1": {ID: "song-1", Path: "/music/artist/one.flac"}},
		misses: map[string]*LookupMiss{"/music/artist/two.flac": {
			Path:   "/music/artist/two.flac",
			Reason: MissNoPathMatch,
			Attempts: []LookupAttempt{{
				Term: "two", Candidates: 12, Evidence: []string{`"/other/two.flac" (16 bytes)`},
			}},
		}},
	}

	result, err := syncerFor(store, player, "", t).SyncPlaylist(context.Background(), playlistID)

	if err != nil {
		t.Fatalf("SyncPlaylist() error = %v", err)
	}
	if len(result.Unpaired) != 1 {
		t.Fatalf("unpaired = %+v, want the one file the player would not answer for", result.Unpaired)
	}
	unpaired := result.Unpaired[0]
	if unpaired.Reason != MissNoPathMatch || len(unpaired.Attempts) != 1 {
		t.Errorf("unpaired = %+v, want the reason and the search behind it", unpaired)
	}
	if unpaired.LibraryPath != "/music/artist/two.flac" || unpaired.EntryID == entryID {
		t.Errorf("unpaired = %+v, want the file and entry it was about", unpaired)
	}
}

// An unpaired file carries the path the player was asked about as well as the
// one Schall holds: a map that describes the wrong mount is invisible in the
// Schall path and plain in the asked one.
func TestAnUnpairedFileCarriesThePathThePlayerWasAskedAbout(t *testing.T) {
	store, playlistID := pairingList(t)

	report, err := syncerFor(store, &syncPlayer{}, "/music=/srv/media", t).
		PlaylistPairing(context.Background(), playlistID)

	if err != nil {
		t.Fatalf("PlaylistPairing() error = %v", err)
	}
	if len(report.Unpaired) == 0 {
		t.Fatal("nothing was reported as unpaired")
	}
	unpaired := report.Unpaired[0]
	if unpaired.LibraryPath != "/music/a/one.flac" || unpaired.RemotePath != "/srv/media/a/one.flac" {
		t.Errorf("unpaired = %+v, want both the held path and the asked one", unpaired)
	}
}

// pairingList is a want list with three acquired files in two folders and
// nothing pushed yet: the state a report is worth reading in.
func pairingList(t *testing.T) (*syncStore, uuid.UUID) {
	t.Helper()
	playlistID := uuid.New()
	store := &syncStore{
		settings: configuredPlayer(),
		playlist: db.PlaylistRow{ID: playlistID, Name: "musik", Source: "spotify", EntryCount: 5},
		acquired: []db.AcquiredPlaylistTrackRow{
			{EntryID: uuid.New(), Position: 1, LibraryFileID: uuid.New(), LibraryPath: "/music/a/one.flac"},
			{EntryID: uuid.New(), Position: 2, LibraryFileID: uuid.New(), LibraryPath: "/music/a/two.flac"},
			{EntryID: uuid.New(), Position: 3, LibraryFileID: uuid.New(), LibraryPath: "/music/b/three.flac"},
		},
		snapshotErr: pgx.ErrNoRows,
	}
	return store, playlistID
}

func TestAPairingReportSaysHowMuchOfTheListThePlayerAnsweredFor(t *testing.T) {
	store, playlistID := pairingList(t)
	player := &syncPlayer{byPath: map[string]Song{"/music/a/one.flac": {ID: "song-1"}}}

	report, err := syncerFor(store, player, "", t).PlaylistPairing(context.Background(), playlistID)

	if err != nil {
		t.Fatalf("PlaylistPairing() error = %v", err)
	}
	if report.EntryCount != 5 || report.AcquiredCount != 3 || report.PairedCount != 1 {
		t.Errorf("report = %+v, want five entries, three acquired, one paired", report)
	}
	if report.Checked != 3 {
		t.Errorf("checked = %d, want every acquired file asked about", report.Checked)
	}
	if len(report.Unpaired) != 2 {
		t.Errorf("unpaired = %+v, want the two files the player did not answer for", report.Unpaired)
	}
}

// A push refuses when nothing pairs, because a comparison nothing agrees with
// is not one to act on. A report acts on nothing, and nothing having paired is
// exactly the state somebody reads it to understand.
func TestAPairingReportOfAListNothingPairedForIsAnAnswer(t *testing.T) {
	store, playlistID := pairingList(t)

	report, err := syncerFor(store, &syncPlayer{}, "", t).
		PlaylistPairing(context.Background(), playlistID)

	if err != nil {
		t.Fatalf("PlaylistPairing() error = %v", err)
	}
	if report.PairedCount != 0 || len(report.Unpaired) != 3 {
		t.Errorf("report = %+v, want every file reported as unpaired", report)
	}
}

// It is a reading. Reading it must not push, adopt, remove or record anything.
func TestAPairingReportChangesNothing(t *testing.T) {
	store, playlistID := pairingList(t)
	player := &syncPlayer{byPath: map[string]Song{"/music/a/one.flac": {ID: "song-1"}}}

	if _, err := syncerFor(store, player, "", t).
		PlaylistPairing(context.Background(), playlistID); err != nil {
		t.Fatalf("PlaylistPairing() error = %v", err)
	}

	if len(store.writes) != 0 || len(store.queued) != 0 || len(player.updates) != 0 {
		t.Errorf("a report wrote %d snapshots, queued %d passes and made %d updates",
			len(store.writes), len(store.queued), len(player.updates))
	}
}

// A long list can outrun the budget one request has for it. What was asked is
// still an answer, so the report says how much of the list it got through
// rather than throwing away the part it read.
func TestAPairingReportThatRanOutOfTimeSaysHowFarItGot(t *testing.T) {
	store, playlistID := pairingList(t)
	player := &syncPlayer{
		byPath:        map[string]Song{"/music/a/one.flac": {ID: "song-1"}},
		deadlineAfter: 2,
	}

	report, err := syncerFor(store, player, "", t).PlaylistPairing(context.Background(), playlistID)

	if err != nil {
		t.Fatalf("PlaylistPairing() error = %v", err)
	}
	if report.AcquiredCount != 3 || report.Checked != 2 {
		t.Errorf("report = %+v, want two of three files checked", report)
	}
	if report.PairedCount != 1 || len(report.Unpaired) != 1 {
		t.Errorf("report = %+v, want what was read before the budget ran out", report)
	}
}

// A caller that has given up is not a slow player, and answering it with a
// partial picture would be reporting a state nobody finished looking at.
func TestAPairingReportForACallerThatGaveUpIsAFailure(t *testing.T) {
	store, playlistID := pairingList(t)
	player := &syncPlayer{deadlineAfter: 1}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := syncerFor(store, player, "", t).PlaylistPairing(ctx, playlistID)

	if err == nil {
		t.Fatal("a report was made for a caller that had gone")
	}
}

func TestAPairingReportAgainstNoPlayerSaysThereIsNone(t *testing.T) {
	store, playlistID := pairingList(t)
	store.settingsErr = pgx.ErrNoRows

	report, err := syncerFor(store, &syncPlayer{}, "", t).
		PlaylistPairing(context.Background(), playlistID)

	if err != nil {
		t.Fatalf("PlaylistPairing() error = %v", err)
	}
	if report.Configured {
		t.Error("a report was made against a player nobody configured")
	}
	if report.AcquiredCount != 3 {
		t.Errorf("acquired = %d, want what Schall holds reported without a player", report.AcquiredCount)
	}
}

// Taken from the front, a sample answers a question nobody asked: the front of
// a playlist is one arbitrary corner of it. The point of a few lines out of
// twenty-one is telling one bad folder from a whole list.
func TestASampleOfUnpairedFilesCoversTheFoldersRatherThanTheFirstFew(t *testing.T) {
	var unpaired []UnpairedTrack
	for position := 1; position <= 10; position++ {
		unpaired = append(unpaired, UnpairedTrack{
			Position: int32(position), RemotePath: "/music/first/track.flac", Reason: MissNoCandidates,
		})
	}
	unpaired = append(unpaired, UnpairedTrack{
		Position: 11, RemotePath: "/music/second/track.flac", Reason: MissNoCandidates,
	})

	sample := sampleUnpaired(unpaired, 3)

	if len(sample) != 3 {
		t.Fatalf("sample = %+v, want three", sample)
	}
	if !strings.Contains(sample[1].Path, "/music/second/") {
		t.Errorf("sample = %+v, want the other folder before more of the first", sample)
	}
}

// A refusal that describes nothing is still an unpaired file. Nothing else
// answers ErrNotFound here, but the type does not promise that, and a pass that
// met one has to go on rather than stop on it.
func TestAnUnpairedFileWithNoDescribedReasonIsStillReported(t *testing.T) {
	store, playlistID := pairingList(t)
	player := &syncPlayer{
		byPath: map[string]Song{"/music/a/one.flac": {ID: "song-1"}},
		misses: map[string]*LookupMiss{},
	}

	report, err := syncerFor(store, player, "", t).PlaylistPairing(context.Background(), playlistID)

	if err != nil {
		t.Fatalf("PlaylistPairing() error = %v", err)
	}
	if len(report.Unpaired) != 2 {
		t.Fatalf("unpaired = %+v, want both files reported", report.Unpaired)
	}
}
