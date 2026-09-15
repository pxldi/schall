package playlists

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/events"
	"github.com/pxldi/schall/internal/spotify"
	"github.com/rs/zerolog"
)

// fakeSpotify serves the wire shape Spotify serves today, with a mutable
// track list so a test can change the remote playlist between imports.
type fakeSpotify struct {
	name     string
	snapshot string
	tracks   []map[string]any
	requests int

	// pageSize splits the rows the way Spotify does: the playlist object
	// carries the first page and the rest come from /items. Zero serves the
	// whole list in the playlist object.
	pageSize int

	// account is what /me answers; nil is an account with a display name.
	account map[string]any

	// rotatedRefresh is the refresh token a refresh grant hands back instead
	// of the stored one; empty means Spotify kept the old one. Its opposite,
	// exchangeRefresh, is what an authorization_code grant returns, and empty
	// there means Spotify returned none at all.
	rotatedRefresh  string
	exchangeRefresh string

	// The refusals. Each is a status code answered instead of the payload:
	// tokenStatus, meStatus and playlistStatus for every such request,
	// fullReadStatus only for the read of the rows so the revision check still
	// works, and tokenFailAfter to start refusing once that many tokens have
	// been served.
	tokenStatus    int
	meStatus       int
	playlistStatus int
	fullReadStatus int
	tokenFailAfter int
	tokenRequests  int
}

func track(id, artist, title, album string, durationMS int, isrc string) map[string]any {
	return map[string]any{
		"is_local": false,
		"item": map[string]any{
			"id": id, "name": title, "duration_ms": durationMS, "type": "track",
			"external_ids": map[string]any{"isrc": isrc},
			"artists":      []any{map[string]any{"name": artist}},
			"album":        map[string]any{"name": album},
		},
	}
}

func (fake *fakeSpotify) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	// The accounts service is fussy about the grant, and so is this: a caller
	// reaching for the wrong one is answered 400 rather than quietly handed a
	// token, so an import that stopped refreshing would fail here.
	mux.HandleFunc("POST /api/token", func(w http.ResponseWriter, r *http.Request) {
		fake.tokenRequests++
		if fake.tokenStatus != 0 {
			w.WriteHeader(fake.tokenStatus)
			return
		}
		if fake.tokenFailAfter > 0 && fake.tokenRequests > fake.tokenFailAfter {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		reply := map[string]any{"access_token": "access", "expires_in": 3600}
		switch r.FormValue("grant_type") {
		case "refresh_token":
			if r.FormValue("refresh_token") == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if fake.rotatedRefresh != "" {
				reply["refresh_token"] = fake.rotatedRefresh
			}
		case "authorization_code":
			if r.FormValue("code") == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if fake.exchangeRefresh != "" {
				reply["refresh_token"] = fake.exchangeRefresh
			}
		default:
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(reply)
	})
	mux.HandleFunc("GET /me", func(w http.ResponseWriter, r *http.Request) {
		if fake.meStatus != 0 {
			w.WriteHeader(fake.meStatus)
			return
		}
		account := fake.account
		if account == nil {
			account = map[string]any{"id": "poldi", "display_name": "Poldi"}
		}
		_ = json.NewEncoder(w).Encode(account)
	})
	// The documented /tracks subresource answers 403 on the live API, which is
	// why the rows are read from the playlist object and /items. Refusing it
	// here the same way keeps a regression from passing quietly.
	mux.HandleFunc("GET /playlists/{id}/tracks", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	mux.HandleFunc("GET /playlists/{id}/items", func(w http.ResponseWriter, r *http.Request) {
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		_ = json.NewEncoder(w).Encode(fake.page(offset))
	})
	mux.HandleFunc("GET /playlists/{id}", func(w http.ResponseWriter, r *http.Request) {
		fake.requests++
		status := fake.playlistStatus
		if status == 0 && r.URL.Query().Get("fields") == "" {
			status = fake.fullReadStatus
		}
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": fake.name, "description": "d", "snapshot_id": fake.snapshot,
			"owner": map[string]any{"display_name": "Poldi"},
			"items": fake.page(0),
		})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// page serves the rows from offset, honouring pageSize so a test can make a
// list long enough to need a second request.
func (fake *fakeSpotify) page(offset int) map[string]any {
	size := fake.pageSize
	if size <= 0 {
		size = len(fake.tracks)
	}
	end := min(offset+size, len(fake.tracks))
	rows := []map[string]any{}
	if offset < end {
		rows = fake.tracks[offset:end]
	}
	return map[string]any{"total": len(fake.tracks), "items": rows}
}

// newTestService is the ordinary case: credentials stored and an account
// connected.
func newTestService(t *testing.T, pool *pgxpool.Pool, fake *fakeSpotify) *Service {
	t.Helper()
	service := newUnconnectedService(t, pool, fake)
	if _, err := db.New(pool).SaveSpotifyAuthorization(context.Background(), "refresh", "Poldi"); err != nil {
		t.Fatal(err)
	}
	return service
}

// newUnconnectedService has application credentials but no authorization: the
// state between entering a client ID and finishing the OAuth round-trip.
func newUnconnectedService(t *testing.T, pool *pgxpool.Pool, fake *fakeSpotify) *Service {
	t.Helper()
	queries := db.New(pool)
	if _, err := queries.SaveSpotifySettings(context.Background(), "client", "secret"); err != nil {
		t.Fatal(err)
	}
	return newUnconfiguredService(t, pool, fake)
}

// newUnconfiguredService has no stored credentials at all.
func newUnconfiguredService(t *testing.T, pool *pgxpool.Pool, fake *fakeSpotify) *Service {
	t.Helper()
	server := fake.server(t)
	return NewService(db.New(pool), zerolog.Nop()).
		WithEndpoints(server.URL, server.URL, server.Client())
}

func TestImportCreatesOneWantPerUnownedEntry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)

	fake := &fakeSpotify{name: "musik", snapshot: "rev1", tracks: []map[string]any{
		track("t1", "$NOT", "Motorola", "Motorola", 145319, "QM42K1858315"),
		track("t2", "BONES", "HDMI", "Rotten", 139000, "CA5KR1567859"),
		// The same track twice is one entry and one want.
		track("t1", "$NOT", "Motorola", "Motorola", 145319, "QM42K1858315"),
		// A local file has nothing to reconcile by and is skipped.
		{"is_local": true, "item": map[string]any{"id": "", "type": "track", "name": "home recording"}},
	}}
	service := newTestService(t, pool, fake)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "remote1", "musik")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Import(ctx, playlist.ID); err != nil {
		t.Fatalf("Import() error = %v", err)
	}

	entries, err := queries.PlaylistEntries(ctx, playlist.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2 (duplicate collapsed, local skipped)", len(entries))
	}
	for _, entry := range entries {
		if !entry.TargetID.Valid {
			t.Fatalf("entry %q has no want", entry.EntryTitle)
		}
		if entry.TargetStatus.String != "unresolved" {
			t.Fatalf("entry %q target status = %q, want unresolved", entry.EntryTitle, entry.TargetStatus.String)
		}
	}

	stored, err := queries.Playlist(ctx, playlist.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SourceRevision.String != "rev1" || !stored.ImportedAt.Valid || stored.TrackCount != 2 {
		t.Fatalf("stored playlist = %#v", stored)
	}

	// New wants are unresolved and only a sweep resolves them; an import that
	// created some must have asked for one.
	var sweeps int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM jobs WHERE kind = 'sweep_acquisition_targets' AND status = 'queued'`,
	).Scan(&sweeps); err != nil {
		t.Fatal(err)
	}
	if sweeps != 1 {
		t.Fatalf("queued sweeps = %d, want 1", sweeps)
	}
}

// A re-import reconciles; it does not repeat. Running it twice over the same
// list leaves one entry and one want, and a run that finds a track gone prunes
// the entry without withdrawing the want — removing a track from a playlist is
// not an instruction to stop wanting it.
//
// This used to assert that the second run made exactly one request, which
// pinned a short-circuit on the unchanged revision. That assertion was
// removed rather than relaxed: the behaviour it described was the defect. What
// is idempotent about a re-import is what it leaves behind, not what it
// refuses to ask.
func TestReimportingReconcilesRatherThanRepeating(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)

	fake := &fakeSpotify{name: "musik", snapshot: "rev1", tracks: []map[string]any{
		track("t1", "$NOT", "Motorola", "Motorola", 145319, "QM42K1858315"),
	}}
	service := newTestService(t, pool, fake)
	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "remote1", "musik")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Import(ctx, playlist.ID); err != nil {
		t.Fatal(err)
	}
	// Same revision, run again: one entry, one want, nothing duplicated.
	if err := service.Import(ctx, playlist.ID); err != nil {
		t.Fatal(err)
	}
	entriesAgain, err := queries.PlaylistEntries(ctx, playlist.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entriesAgain) != 1 {
		t.Fatalf("entries after a second import = %d, want 1", len(entriesAgain))
	}
	var targets int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM acquisition_targets`).Scan(&targets); err != nil {
		t.Fatal(err)
	}
	if targets != 1 {
		t.Fatalf("targets = %d, want 1", targets)
	}

	// A changed revision that dropped the track prunes the entry and leaves
	// the want standing: removing a track is not "stop wanting this".
	fake.snapshot = "rev2"
	fake.tracks = []map[string]any{
		track("t2", "BONES", "HDMI", "Rotten", 139000, "CA5KR1567859"),
	}
	if err := service.Import(ctx, playlist.ID); err != nil {
		t.Fatal(err)
	}
	entries, err := queries.PlaylistEntries(ctx, playlist.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].EntryTitle != "HDMI" {
		t.Fatalf("entries after removal = %#v", entries)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM acquisition_targets`).Scan(&targets); err != nil {
		t.Fatal(err)
	}
	if targets != 2 {
		t.Fatalf("targets after removal = %d, want both wants kept", targets)
	}

	// A re-appearing track is a fresh entry — its join row went with the old
	// one — so it asks again. The new want and the old resolve to the same
	// recording and merge by supersession (0006); asking twice costs requests,
	// never a duplicate acquisition.
	fake.snapshot = "rev3"
	fake.tracks = append(fake.tracks, track("t1", "$NOT", "Motorola", "Motorola", 145319, "QM42K1858315"))
	if err := service.Import(ctx, playlist.ID); err != nil {
		t.Fatal(err)
	}
	entries, err = queries.PlaylistEntries(ctx, playlist.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries after return = %d, want 2", len(entries))
	}
	for _, entry := range entries {
		if !entry.TargetID.Valid {
			t.Fatalf("entry %q has no want after return", entry.EntryTitle)
		}
	}
}

func TestImportRecordsOwnershipByISRCAndCreatesNothingForIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)

	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, '/music/motorola.flac', 1024, now())
	`, fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, isrc, method, summary
		)
		VALUES ($1, 'external', $2, 'QM42K1858315', 'isrc', 'proven by identifier')
	`, fileID, uuid.New()); err != nil {
		t.Fatal(err)
	}

	fake := &fakeSpotify{name: "musik", snapshot: "rev1", tracks: []map[string]any{
		track("t1", "$NOT", "Motorola", "Motorola", 145319, "QM42K1858315"),
	}}
	service := newTestService(t, pool, fake)
	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "remote1", "musik")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Import(ctx, playlist.ID); err != nil {
		t.Fatal(err)
	}

	entries, err := queries.PlaylistEntries(ctx, playlist.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if !entries[0].OwnedFileID.Valid || entries[0].OwnedFileID.UUID != fileID {
		t.Fatalf("entry owned file = %#v, want %s", entries[0].OwnedFileID, fileID)
	}
	if entries[0].TargetID.Valid {
		t.Fatal("an owned entry must create no want")
	}
}

// The playlist not having changed says nothing about the library. Ownership is
// re-asked every import — a file that arrived since the last one answers its
// entry — so a re-import over an unchanged list is exactly how a copy acquired
// in between gets attached to the entry that wanted it.
//
// Re-import is also the only lever there is: nothing queues an import except a
// person following a list or pressing the button.
func TestReimportingAnUnchangedPlaylistAnswersAnEntryWithACopyAcquiredSince(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)

	fake := &fakeSpotify{name: "musik", snapshot: "rev1", tracks: []map[string]any{
		track("t1", "$NOT", "Motorola", "Motorola", 145319, "QM42K1858315"),
	}}
	service := newTestService(t, pool, fake)
	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "remote1", "musik")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Import(ctx, playlist.ID); err != nil {
		t.Fatal(err)
	}
	entries, err := queries.PlaylistEntries(ctx, playlist.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].OwnedFileID.Valid {
		t.Fatalf("entries = %#v, want one entry nothing owns yet", entries)
	}

	// The copy arrives, proven by identifier, exactly as the acquisition loop
	// would leave it. Spotify's list is untouched.
	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, '/music/motorola.flac', 1024, now())
	`, fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, isrc, method, summary
		)
		VALUES ($1, 'external', $2, 'QM42K1858315', 'isrc', 'proven by identifier')
	`, fileID, uuid.New()); err != nil {
		t.Fatal(err)
	}

	if err := service.Import(ctx, playlist.ID); err != nil {
		t.Fatal(err)
	}
	entries, err = queries.PlaylistEntries(ctx, playlist.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if !entries[0].OwnedFileID.Valid || entries[0].OwnedFileID.UUID != fileID {
		t.Fatalf("entry owned file = %#v, want the copy that arrived since", entries[0].OwnedFileID)
	}
}

// An import is the moment a list's contents changed, so it is the moment the
// player should be told. Queued rather than done inside the import: a pass
// talks to Navidrome, and an import that waited on that conversation would fail
// for a reason that has nothing to do with the music.
func TestAnImportQueuesAPassOverTheListForThePlayer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)

	fake := &fakeSpotify{name: "musik", snapshot: "rev1", tracks: []map[string]any{
		track("t1", "$NOT", "Motorola", "Motorola", 145319, "QM42K1858315"),
	}}
	service := newTestService(t, pool, fake)
	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "remote1", "musik")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Import(ctx, playlist.ID); err != nil {
		t.Fatal(err)
	}

	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'sync_navidrome_playlists'
		  AND payload ->> 'playlistId' = $1
	`, playlist.ID.String()).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("queued player passes = %d, want 1", queued)
	}
}

func TestFollowRecordsTheListAndQueuesItsFirstImport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	service := newTestService(t, pool, &fakeSpotify{name: "musik", snapshot: "rev1"})
	playlist, err := service.Follow(ctx, "https://open.spotify.com/playlist/remote1?si=abc")
	if err != nil {
		t.Fatalf("Follow() error = %v", err)
	}
	if playlist.Source != "spotify" || playlist.SourceID.String != "remote1" || playlist.Name != "musik" {
		t.Fatalf("followed playlist = %#v", playlist)
	}

	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'import_playlist' AND status = 'queued'
		  AND payload ->> 'playlistId' = $1
	`, playlist.ID.String()).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("queued imports = %d, want 1", queued)
	}
}

func TestFollowRefusesALinkThatDoesNotNameAPlaylist(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	service := newTestService(t, pool, &fakeSpotify{name: "musik", snapshot: "rev1"})
	if _, err := service.Follow(ctx, "https://open.spotify.com/album/an-album"); err == nil {
		t.Fatal("Follow(an album link) = no error, want a refusal")
	}
	var stored int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM playlists`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Fatalf("playlists = %d, want nothing recorded", stored)
	}
}

// Spotify allows an untitled list. A want list still has to be nameable, and
// its own identifier is the one name that cannot be wrong.
func TestFollowNamesTheListByItsIDWhenSpotifyGivesItNone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	service := newTestService(t, pool, &fakeSpotify{snapshot: "rev1"})
	playlist, err := service.Follow(ctx, "remote1")
	if err != nil {
		t.Fatalf("Follow() error = %v", err)
	}
	if playlist.Name != "remote1" {
		t.Fatalf("playlist name = %q, want the playlist ID", playlist.Name)
	}
}

// Pressing follow on a list that is already followed is a request for it to be
// up to date, not a mistake to be refused.
func TestFollowingAFollowedListAnswersWithTheSameList(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	service := newTestService(t, pool, &fakeSpotify{name: "musik", snapshot: "rev1"})
	first, err := service.Follow(ctx, "remote1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Follow(ctx, "remote1")
	if err != nil {
		t.Fatalf("following twice = %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("second follow = %s, want the list already followed %s", second.ID, first.ID)
	}
	var stored int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM playlists`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 1 {
		t.Fatalf("playlists = %d, want 1", stored)
	}
}

func TestFollowingAListTellsTheListeners(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	hub := events.NewHub()
	notices, stop := hub.Subscribe()
	defer stop()
	service := newTestService(t, pool, &fakeSpotify{name: "musik", snapshot: "rev1"}).WithEvents(hub)

	if _, err := service.Follow(ctx, "remote1"); err != nil {
		t.Fatal(err)
	}
	if !awaitNotice(t, notices, events.TopicPlaylists) {
		t.Fatal("following a list published no playlists notice")
	}
}

// One live import per list: asking again while one is waiting is answered by
// the import that already exists.
func TestQueueImportAsksForOneImportOfAFollowedList(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)

	service := newTestService(t, pool, &fakeSpotify{name: "musik", snapshot: "rev1"})
	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "remote1", "musik")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := service.QueueImport(ctx, playlist.ID); err != nil {
			t.Fatalf("QueueImport() error = %v", err)
		}
	}
	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE kind = 'import_playlist' AND status = 'queued'
	`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("queued imports = %d, want 1", queued)
	}
}

func TestOperationsOnAListThatIsNotStoredAreRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	service := newTestService(t, pool, &fakeSpotify{name: "musik", snapshot: "rev1"})
	missing := uuid.New()
	for _, testCase := range []struct {
		name string
		call func() error
	}{
		{"queue an import", func() error { _, err := service.QueueImport(ctx, missing); return err }},
		{"read it", func() error { _, _, err := service.Get(ctx, missing); return err }},
		{"delete it", func() error { return service.Delete(ctx, missing) }},
		{"import it", func() error { return service.Import(ctx, missing) }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if err := testCase.call(); !errors.Is(err, pgx.ErrNoRows) {
				t.Errorf("error = %v, want pgx.ErrNoRows", err)
			}
		})
	}
}

// A list with no remote source has nothing to import from, and asking Spotify
// about it would be asking about a list Spotify has never heard of.
func TestAListWithNoRemoteSourceCannotBeImported(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	var playlistID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO playlists (source, name) VALUES ('manual', 'mine') RETURNING id
	`).Scan(&playlistID); err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, pool, &fakeSpotify{name: "musik", snapshot: "rev1"})
	for _, testCase := range []struct {
		name string
		call func() error
	}{
		{"queue an import", func() error { _, err := service.QueueImport(ctx, playlistID); return err }},
		{"import it", func() error { return service.Import(ctx, playlistID) }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if err := testCase.call(); !errors.Is(err, ErrNotSpotify) {
				t.Errorf("error = %v, want ErrNotSpotify", err)
			}
		})
	}
}

func TestSpotifyOperationsRefuseUntilCredentialsAreStored(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	playlist, err := db.New(pool).UpsertSpotifyPlaylist(ctx, "remote1", "musik")
	if err != nil {
		t.Fatal(err)
	}
	service := newUnconfiguredService(t, pool, &fakeSpotify{name: "musik", snapshot: "rev1"})
	for _, testCase := range []struct {
		name string
		call func() error
	}{
		{"build the authorize URL", func() error {
			_, err := service.AuthorizeURL(ctx, "https://schall/callback", "state")
			return err
		}},
		{"finish the round-trip", func() error {
			_, err := service.Connect(ctx, "code", "https://schall/callback")
			return err
		}},
		{"follow a list", func() error { _, err := service.Follow(ctx, "remote1"); return err }},
		{"import a list", func() error { return service.Import(ctx, playlist.ID) }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if err := testCase.call(); !errors.Is(err, ErrNotConfigured) {
				t.Errorf("error = %v, want ErrNotConfigured", err)
			}
		})
	}
}

// A request that was abandoned says nothing about whether Spotify is set up,
// and reporting it as "not configured" would send the user off to re-enter
// credentials they already have.
func TestACancelledRequestIsNotReadAsUnconfigured(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	service := newTestService(t, pool, &fakeSpotify{name: "musik", snapshot: "rev1"})
	cancelled, stop := context.WithCancel(ctx)
	stop()
	_, err := service.AuthorizeURL(cancelled, "https://schall/callback", "state")
	if err == nil || errors.Is(err, ErrNotConfigured) {
		t.Fatalf("AuthorizeURL(cancelled) = %v, want a failure that is not ErrNotConfigured", err)
	}
}

// Credentials are not an authorization. Nothing that has to speak to Spotify
// on the user's behalf may proceed until an account has approved it.
func TestSpotifyOperationsRefuseWithoutAConnectedAccount(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	playlist, err := db.New(pool).UpsertSpotifyPlaylist(ctx, "remote1", "musik")
	if err != nil {
		t.Fatal(err)
	}
	service := newUnconnectedService(t, pool, &fakeSpotify{name: "musik", snapshot: "rev1"})
	for _, testCase := range []struct {
		name string
		call func() error
	}{
		{"follow a list", func() error { _, err := service.Follow(ctx, "remote1"); return err }},
		{"import a list", func() error { return service.Import(ctx, playlist.ID) }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if err := testCase.call(); !errors.Is(err, ErrNotConnected) {
				t.Errorf("error = %v, want ErrNotConnected", err)
			}
		})
	}
}

// Following is a promise to keep a list up to date, and a list Spotify has
// never heard of is one nothing could ever be imported from.
func TestFollowingAListSpotifyDoesNotHaveRecordsNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	service := newTestService(t, pool, &fakeSpotify{
		name: "musik", snapshot: "rev1", playlistStatus: http.StatusNotFound,
	})
	if _, err := service.Follow(ctx, "remote1"); !errors.Is(err, spotify.ErrNotFound) {
		t.Fatalf("Follow() error = %v, want spotify.ErrNotFound", err)
	}
	var stored int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM playlists`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Fatalf("playlists = %d, want nothing recorded", stored)
	}
}

// An authorization Spotify no longer accepts is repaired by connecting the
// account again, never by retrying, so the settings page has to be able to say
// so.
func TestARejectedAuthorizationIsRecordedOnTheSettings(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "remote1", "musik")
	if err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, pool, &fakeSpotify{
		name: "musik", snapshot: "rev1", tokenStatus: http.StatusUnauthorized,
	})
	if err := service.Import(ctx, playlist.ID); !errors.Is(err, spotify.ErrUnauthorized) {
		t.Fatalf("Import() error = %v, want spotify.ErrUnauthorized", err)
	}
	settings, err := queries.SpotifySettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.ConnectionStatus != "failed" ||
		!strings.Contains(settings.ConnectionError.String, "connect the account again") {
		t.Fatalf("settings = status %q, error %q", settings.ConnectionStatus, settings.ConnectionError.String)
	}
}

// Spotify being unwell is not the same statement as Spotify refusing the
// authorization, and only the second one is worth telling the user to act on.
func TestASpotifyOutageIsNotRecordedAsADeadAuthorization(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "remote1", "musik")
	if err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, pool, &fakeSpotify{
		name: "musik", snapshot: "rev1", tokenStatus: http.StatusInternalServerError,
	})
	if err := service.Import(ctx, playlist.ID); err == nil {
		t.Fatal("Import() = no error, want the outage reported")
	}
	settings, err := queries.SpotifySettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.ConnectionStatus == "failed" {
		t.Fatalf("connection status = %q, want the stored authorization left alone", settings.ConnectionStatus)
	}
}

// Spotify may hand back a new refresh token on any refresh. Keeping the old
// one would work until the moment it did not, and the account would then have
// to be connected again for no reason anybody could see.
func TestARotatedRefreshTokenReplacesTheStoredOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)

	service := newTestService(t, pool, &fakeSpotify{
		name: "musik", snapshot: "rev1", rotatedRefresh: "rotated",
	})
	if _, err := service.Follow(ctx, "remote1"); err != nil {
		t.Fatal(err)
	}
	settings, err := queries.SpotifySettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.RefreshToken.String != "rotated" {
		t.Fatalf("stored refresh token = %q, want the rotated one", settings.RefreshToken.String)
	}
}

func TestImportRecordsWhySpotifyStoppedIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "remote1", "musik")
	if err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, pool, &fakeSpotify{
		name: "musik", snapshot: "rev1", playlistStatus: http.StatusInternalServerError,
	})
	if err := service.Import(ctx, playlist.ID); err == nil {
		t.Fatal("Import() = no error, want the failure reported")
	}
	settings, err := queries.SpotifySettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.ConnectionStatus != "failed" ||
		!strings.Contains(settings.ConnectionError.String, "read Spotify playlist revision") {
		t.Fatalf("settings = status %q, error %q", settings.ConnectionStatus, settings.ConnectionError.String)
	}
}

// The revision is the record of work already done. An import that could not
// read the rows must not leave one behind, or the next run would believe the
// list was already reconciled.
func TestAnImportThatCannotReadTheRowsStoresNoRevision(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)

	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "remote1", "musik")
	if err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, pool, &fakeSpotify{
		name: "musik", snapshot: "rev1", fullReadStatus: http.StatusInternalServerError,
	})
	if err := service.Import(ctx, playlist.ID); err == nil {
		t.Fatal("Import() = no error, want the failure reported")
	}
	stored, err := queries.Playlist(ctx, playlist.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SourceRevision.Valid || stored.ImportedAt.Valid {
		t.Fatalf("stored playlist = revision %#v, imported at %#v", stored.SourceRevision, stored.ImportedAt)
	}
}

// The settings page says whether the stored authorization works, so an import
// that got through has to clear whatever the last one recorded.
func TestASuccessfulImportClearsAnEarlierFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)

	fake := &fakeSpotify{name: "musik", snapshot: "rev1", tracks: []map[string]any{
		track("t1", "$NOT", "Motorola", "Motorola", 145319, "QM42K1858315"),
	}}
	service := newTestService(t, pool, fake)
	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "remote1", "musik")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.RecordSpotifyConnection(ctx, "failed", "Spotify was unwell"); err != nil {
		t.Fatal(err)
	}
	if err := service.Import(ctx, playlist.ID); err != nil {
		t.Fatal(err)
	}
	settings, err := queries.SpotifySettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.ConnectionStatus != "ok" || settings.ConnectionError.Valid {
		t.Fatalf("settings = status %q, error %q", settings.ConnectionStatus, settings.ConnectionError.String)
	}
}

// A row with neither a title nor an ISRC gives a resolver nothing to ask
// about, so it is counted as skipped rather than stored as an entry nobody
// could ever answer.
func TestImportSkipsARowWithNeitherTitleNorISRC(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)

	fake := &fakeSpotify{name: "musik", snapshot: "rev1", tracks: []map[string]any{
		track("t1", "$NOT", "Motorola", "Motorola", 145319, "QM42K1858315"),
		track("t2", "", "", "", 0, ""),
	}}
	service := newTestService(t, pool, fake)
	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "remote1", "musik")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Import(ctx, playlist.ID); err != nil {
		t.Fatal(err)
	}
	entries, err := queries.PlaylistEntries(ctx, playlist.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].SourceTrackID.String != "t1" {
		t.Fatalf("entries = %#v, want only the row that names something", entries)
	}
}

// A want list is an ordered thing, and a row that could not be used must not
// leave a hole in the numbering behind it.
func TestImportNumbersEntriesInPlaylistOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)

	fake := &fakeSpotify{name: "musik", snapshot: "rev1", tracks: []map[string]any{
		track("t1", "$NOT", "Motorola", "Motorola", 145319, "QM42K1858315"),
		{"is_local": true, "item": map[string]any{"id": "", "type": "track", "name": "home recording"}},
		track("t2", "BONES", "HDMI", "Rotten", 139000, "CA5KR1567859"),
	}}
	service := newTestService(t, pool, fake)
	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "remote1", "musik")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Import(ctx, playlist.ID); err != nil {
		t.Fatal(err)
	}
	entries, err := queries.PlaylistEntries(ctx, playlist.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	if entries[0].SourceTrackID.String != "t1" || entries[0].Position != 1 {
		t.Fatalf("first entry = %q at %d", entries[0].SourceTrackID.String, entries[0].Position)
	}
	if entries[1].SourceTrackID.String != "t2" || entries[1].Position != 2 {
		t.Fatalf("second entry = %q at %d", entries[1].SourceTrackID.String, entries[1].Position)
	}
}

// Spotify serves the first page of rows inside the playlist object and the
// rest from /items — the documented /tracks subresource answers 403. A list
// longer than one page is the ordinary case, and every row of it is a want.
func TestImportReadsEveryPageOfALongList(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)

	fake := &fakeSpotify{name: "musik", snapshot: "rev1", pageSize: 2, tracks: []map[string]any{
		track("t1", "$NOT", "Motorola", "Motorola", 145319, "QM42K1858315"),
		track("t2", "BONES", "HDMI", "Rotten", 139000, "CA5KR1567859"),
		track("t3", "Xavier Wulf", "Blue Tuesday", "Sniper Gang", 168000, "QZES71982361"),
	}}
	service := newTestService(t, pool, fake)
	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "remote1", "musik")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Import(ctx, playlist.ID); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	entries, err := queries.PlaylistEntries(ctx, playlist.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want every row of both pages", len(entries))
	}
}

// Ownership is an identifier question and is re-asked every import: a file
// that went missing stops answering its entry, and the entry becomes a want.
func TestAFileThatWentMissingUnanswersItsEntry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)

	fileID := seedIdentifiedFile(t, ctx, pool, "/music/motorola.flac", "QM42K1858315")
	fake := &fakeSpotify{name: "musik", snapshot: "rev1", tracks: []map[string]any{
		track("t1", "$NOT", "Motorola", "Motorola", 145319, "QM42K1858315"),
	}}
	service := newTestService(t, pool, fake)
	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "remote1", "musik")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Import(ctx, playlist.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE library_files SET missing_at = now() WHERE id = $1
	`, fileID); err != nil {
		t.Fatal(err)
	}
	fake.snapshot = "rev2"
	if err := service.Import(ctx, playlist.ID); err != nil {
		t.Fatal(err)
	}
	entries, err := queries.PlaylistEntries(ctx, playlist.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].OwnedFileID.Valid {
		t.Fatalf("entry is still answered by %s", entries[0].OwnedFileID.UUID)
	}
	if !entries[0].TargetID.Valid {
		t.Fatal("an entry nothing answers must have a want")
	}
}

// Only a sweep resolves a new want, so an import that made none has nothing to
// wake the sweeper for.
func TestAnImportThatCreatedNoWantAsksForNoSweep(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)

	seedIdentifiedFile(t, ctx, pool, "/music/motorola.flac", "QM42K1858315")
	fake := &fakeSpotify{name: "musik", snapshot: "rev1", tracks: []map[string]any{
		track("t1", "$NOT", "Motorola", "Motorola", 145319, "QM42K1858315"),
	}}
	service := newTestService(t, pool, fake)
	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "remote1", "musik")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Import(ctx, playlist.ID); err != nil {
		t.Fatal(err)
	}
	var sweeps int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM jobs WHERE kind = 'sweep_acquisition_targets'`,
	).Scan(&sweeps); err != nil {
		t.Fatal(err)
	}
	if sweeps != 0 {
		t.Fatalf("queued sweeps = %d, want none", sweeps)
	}
}

func TestAnImportThatCreatedWantsTellsTheAcquisitionListeners(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)

	hub := events.NewHub()
	notices, stop := hub.Subscribe()
	defer stop()
	fake := &fakeSpotify{name: "musik", snapshot: "rev1", tracks: []map[string]any{
		track("t1", "$NOT", "Motorola", "Motorola", 145319, "QM42K1858315"),
	}}
	service := newTestService(t, pool, fake).WithEvents(hub)
	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "remote1", "musik")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Import(ctx, playlist.ID); err != nil {
		t.Fatal(err)
	}
	if !awaitNotice(t, notices, events.TopicAcquisitions) {
		t.Fatal("an import that made wants published no acquisitions notice")
	}
}

func TestConnectStoresTheAuthorizationAndWhoseItIs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	service := newUnconnectedService(t, pool, &fakeSpotify{
		name: "musik", snapshot: "rev1", exchangeRefresh: "granted",
	})
	settings, err := service.Connect(ctx, "code", "https://schall/callback")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if settings.RefreshToken.String != "granted" || settings.AccountName.String != "Poldi" {
		t.Fatalf("settings = %#v", settings)
	}
}

// A Spotify account need not have a display name, and a settings page showing
// a blank one would be saying nobody is connected.
func TestConnectNamesTheAccountByItsIDWhenItHasNoDisplayName(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	service := newUnconnectedService(t, pool, &fakeSpotify{
		name: "musik", snapshot: "rev1", exchangeRefresh: "granted",
		account: map[string]any{"id": "31poldi", "display_name": "  "},
	})
	settings, err := service.Connect(ctx, "code", "https://schall/callback")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if settings.AccountName.String != "31poldi" {
		t.Fatalf("account name = %q, want the account ID", settings.AccountName.String)
	}
}

// A half-finished round-trip is not an authorization. Storing one would leave
// the settings page claiming an account is connected that cannot be refreshed.
func TestAFailedConnectStoresNoAuthorization(t *testing.T) {
	for _, testCase := range []struct {
		name string
		fake *fakeSpotify
	}{
		{"the code is rejected", &fakeSpotify{tokenStatus: http.StatusBadRequest}},
		{"no refresh token comes back", &fakeSpotify{}},
		{"the profile cannot be read", &fakeSpotify{
			exchangeRefresh: "granted", meStatus: http.StatusInternalServerError,
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			pool := dbtest.Setup(t)

			service := newUnconnectedService(t, pool, testCase.fake)
			if _, err := service.Connect(ctx, "code", "https://schall/callback"); err == nil {
				t.Fatal("Connect() = no error, want a refusal")
			}
			settings, err := db.New(pool).SpotifySettings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if settings.RefreshToken.Valid {
				t.Fatalf("stored refresh token = %q, want none", settings.RefreshToken.String)
			}
		})
	}
}

// The authorize URL is the only thing between the user and the consent screen:
// it has to carry who is asking, what for, and the token the callback must
// echo back.
func TestAuthorizeURLCarriesTheScopesAndState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	service := newTestService(t, pool, &fakeSpotify{name: "musik", snapshot: "rev1"})
	address, err := service.AuthorizeURL(ctx, "https://schall/callback", "csrf-token")
	if err != nil {
		t.Fatalf("AuthorizeURL() error = %v", err)
	}
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatalf("parse %q: %v", address, err)
	}
	query := parsed.Query()
	if query.Get("client_id") != "client" || query.Get("state") != "csrf-token" ||
		query.Get("scope") != spotify.Scopes || query.Get("response_type") != "code" {
		t.Fatalf("authorize URL query = %#v", query)
	}
}

func TestListReturnsEveryFollowedList(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)

	for _, sourceID := range []string{"remote1", "remote2"} {
		if _, err := queries.UpsertSpotifyPlaylist(ctx, sourceID, "musik"); err != nil {
			t.Fatal(err)
		}
	}
	service := newTestService(t, pool, &fakeSpotify{name: "musik", snapshot: "rev1"})
	lists, err := service.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(lists) != 2 {
		t.Fatalf("lists = %d, want 2", len(lists))
	}
}

func TestGetReturnsTheListWithItsEntries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)

	fake := &fakeSpotify{name: "musik", snapshot: "rev1", tracks: []map[string]any{
		track("t1", "$NOT", "Motorola", "Motorola", 145319, "QM42K1858315"),
	}}
	service := newTestService(t, pool, fake)
	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "remote1", "musik")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Import(ctx, playlist.ID); err != nil {
		t.Fatal(err)
	}
	got, entries, err := service.Get(ctx, playlist.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.ID != playlist.ID {
		t.Fatalf("list = %s, want %s", got.ID, playlist.ID)
	}
	if len(entries) != 1 || entries[0].EntryTitle != "Motorola" {
		t.Fatalf("entries = %#v", entries)
	}
}

// Unfollowing a list is not the statement "stop wanting this music", so the
// wants it made outlive it.
// WantEntry is the door for an entry an import never asks about on its own —
// today, every entry on a list adopted as a file that a crash or an older
// build left with no target at all.
func TestWantEntryCreatesATargetForAnEntryWithNone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)
	service := NewService(queries, zerolog.Nop())

	playlist, err := queries.UpsertFilePlaylist(ctx, "list.csv", "list")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := queries.UpsertPlaylistEntry(ctx, db.UpsertPlaylistEntryParams{
		PlaylistID: playlist.ID, Position: 1, SourceTrackID: "t1",
		EntryArtist: "A", EntryTitle: "Song",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := service.WantEntry(ctx, playlist.ID, entry.ID); err != nil {
		t.Fatalf("WantEntry() error = %v", err)
	}

	updated, err := queries.PlaylistEntry(ctx, entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.TargetID.Valid {
		t.Fatalf("entry = %#v, want a target", updated)
	}

	// Asking again is the same want, not a second one.
	if err := service.WantEntry(ctx, playlist.ID, entry.ID); err != nil {
		t.Fatalf("second WantEntry() error = %v", err)
	}
	var targets int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM acquisition_targets`).Scan(&targets); err != nil {
		t.Fatal(err)
	}
	if targets != 1 {
		t.Fatalf("targets = %d, want 1", targets)
	}
}

func TestWantEntryLeavesAnOwnedEntryAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)
	service := NewService(queries, zerolog.Nop())

	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, '/music/song.flac', 1024, now())
	`, fileID); err != nil {
		t.Fatal(err)
	}
	playlist, err := queries.UpsertFilePlaylist(ctx, "list.csv", "list")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := queries.UpsertPlaylistEntry(ctx, db.UpsertPlaylistEntryParams{
		PlaylistID: playlist.ID, Position: 1, SourceTrackID: "t1",
		EntryArtist: "A", EntryTitle: "Song",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := queries.MarkPlaylistEntryOwned(
		ctx, entry.ID, uuid.NullUUID{UUID: fileID, Valid: true},
	); err != nil {
		t.Fatal(err)
	}

	if err := service.WantEntry(ctx, playlist.ID, entry.ID); err != nil {
		t.Fatalf("WantEntry() error = %v", err)
	}

	var targets int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM acquisition_targets`).Scan(&targets); err != nil {
		t.Fatal(err)
	}
	if targets != 0 {
		t.Fatalf("targets = %d, want none for music already held", targets)
	}
}

func TestWantingAnEntryOnTheWrongPlaylistIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)
	service := NewService(queries, zerolog.Nop())

	playlist, err := queries.UpsertFilePlaylist(ctx, "list.csv", "list")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := queries.UpsertPlaylistEntry(ctx, db.UpsertPlaylistEntryParams{
		PlaylistID: playlist.ID, Position: 1, SourceTrackID: "t1",
		EntryArtist: "A", EntryTitle: "Song",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := service.WantEntry(ctx, uuid.New(), entry.ID); !errors.Is(err, ErrEntryNotFound) {
		t.Fatalf("err = %v, want ErrEntryNotFound", err)
	}
}

func TestDeleteForgetsTheListButKeepsItsWants(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := db.New(pool)

	fake := &fakeSpotify{name: "musik", snapshot: "rev1", tracks: []map[string]any{
		track("t1", "$NOT", "Motorola", "Motorola", 145319, "QM42K1858315"),
	}}
	service := newTestService(t, pool, fake)
	playlist, err := queries.UpsertSpotifyPlaylist(ctx, "remote1", "musik")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Import(ctx, playlist.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ctx, playlist.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := queries.Playlist(ctx, playlist.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("reading the deleted list = %v, want pgx.ErrNoRows", err)
	}
	var targets int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM acquisition_targets`).Scan(&targets); err != nil {
		t.Fatal(err)
	}
	if targets != 1 {
		t.Fatalf("targets = %d, want the want left standing", targets)
	}
}

// seedIdentifiedFile puts a file in the library that an ISRC provably names.
func seedIdentifiedFile(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, path, isrc string,
) uuid.UUID {
	t.Helper()
	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, $2, 1024, now())
	`, fileID, path); err != nil {
		t.Fatalf("seed file %s: %v", path, err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, isrc, method, summary
		)
		VALUES ($1, 'external', $2, $3, 'isrc', 'proven by identifier')
	`, fileID, uuid.New(), isrc); err != nil {
		t.Fatalf("seed identity for %s: %v", path, err)
	}
	return fileID
}

// awaitNotice reports whether the topic arrives before the hub goes quiet.
func awaitNotice(t *testing.T, notices <-chan events.Topic, want events.Topic) bool {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case topic := <-notices:
			if topic == want {
				return true
			}
		case <-deadline:
			return false
		}
	}
}
