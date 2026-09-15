package navidrome

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// asked is one request the way the server saw it: which method it was, and the
// parameters wherever they arrived. A mutating call puts them in the body, so a
// test that only read the query string would report an empty request.
type asked struct {
	method     string
	parameters url.Values
	body       url.Values
}

// recording answers like a Subsonic server and keeps every request, body
// included.
func recording(t *testing.T, body string) (*httptest.Server, *[]asked) {
	t.Helper()
	var requests []asked
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		requests = append(requests, asked{
			method:     r.Method,
			parameters: r.Form,
			body:       r.PostForm,
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func namedClient(t *testing.T, baseURL, name string) *Client {
	t.Helper()
	client, err := NewClient(Options{
		BaseURL: baseURL, Username: "schall", Password: "hunter2", ClientName: name,
	})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	return client
}

const okPlaylists = `{"subsonic-response":{"status":"ok","version":"1.16.1","playlists":{"playlist":[` +
	`{"id":"pl-1","name":"Evening","owner":"leo","songCount":2,"comment":"pushed by schall"}]}}}`

const okPlaylist = `{"subsonic-response":{"status":"ok","version":"1.16.1","playlist":` +
	`{"id":"pl-1","name":"Evening","owner":"leo","songCount":2,"entry":[` +
	`{"id":"song-1","title":"First","path":"/music/Artist/Album/01 First.flac"},` +
	`{"id":"song-2","title":"Second","path":"/music/Artist/Album/02 Second.flac"}]}}}`

const okSong = `{"subsonic-response":{"status":"ok","version":"1.16.1","song":` +
	`{"id":"song-1","title":"First","path":"/music/Artist/Album/01 First.flac"}}}`

const notFound = `{"subsonic-response":{"status":"failed","version":"1.16.1",` +
	`"error":{"code":70,"message":"The requested data was not found"}}}`

// ADR 0010: Subsonic.DefaultReportRealPath is copied onto a player row the
// first time a client calls under a name and never re-read, and a row already
// exists under "schall" from the rescan trigger. Sync only gets real paths by
// arriving as somebody else, so the name reaching the wire is the contract.
func TestSyncIntroducesItselfUnderItsOwnName(t *testing.T) {
	server, requests := recording(t, okPlaylists)

	if _, err := namedClient(t, server.URL, SyncClientName).Playlists(context.Background()); err != nil {
		t.Fatalf("list playlists: %v", err)
	}

	if got := (*requests)[0].parameters.Get("c"); got != "schall-sync" {
		t.Errorf("client name = %q, want schall-sync", got)
	}
}

// Everything that only asks for a rescan keeps the identity it has always had,
// so an unnamed client must not quietly become a new player row.
func TestAClientThatWasNotNamedIsStillSchall(t *testing.T) {
	server, requests := recording(t, okPlaylists)

	if _, err := namedClient(t, server.URL, "").Playlists(context.Background()); err != nil {
		t.Fatalf("list playlists: %v", err)
	}

	if got := (*requests)[0].parameters.Get("c"); got != "schall" {
		t.Errorf("client name = %q, want schall", got)
	}
}

func TestListingPlaylistsReportsWhatTheServerHolds(t *testing.T) {
	server, _ := recording(t, okPlaylists)

	playlists, err := namedClient(t, server.URL, SyncClientName).Playlists(context.Background())

	if err != nil {
		t.Fatalf("list playlists: %v", err)
	}
	if len(playlists) != 1 {
		t.Fatalf("playlists = %d, want one", len(playlists))
	}
	want := Playlist{ID: "pl-1", Name: "Evening", Owner: "leo", SongCount: 2, Comment: "pushed by schall"}
	if !reflect.DeepEqual(playlists[0], want) {
		t.Errorf("playlist = %+v, want %+v", playlists[0], want)
	}
}

// A server holding no playlists omits the container altogether, and that is an
// account with nothing in it rather than an answer that could not be read.
func TestAServerHoldingNoPlaylistsListsNoneRatherThanFailing(t *testing.T) {
	server, _ := recording(t, `{"subsonic-response":{"status":"ok","version":"1.16.1"}}`)

	playlists, err := namedClient(t, server.URL, SyncClientName).Playlists(context.Background())

	if err != nil {
		t.Fatalf("list playlists: %v", err)
	}
	if len(playlists) != 0 {
		t.Errorf("playlists = %+v, want none", playlists)
	}
}

func TestAPlaylistCarriesItsEntriesInTheOrderTheServerListedThem(t *testing.T) {
	server, _ := recording(t, okPlaylist)

	playlist, err := namedClient(t, server.URL, SyncClientName).
		Playlist(context.Background(), "pl-1")

	if err != nil {
		t.Fatalf("read playlist: %v", err)
	}
	want := []Song{
		{ID: "song-1", Title: "First", Path: "/music/Artist/Album/01 First.flac"},
		{ID: "song-2", Title: "Second", Path: "/music/Artist/Album/02 Second.flac"},
	}
	if !slices.Equal(playlist.Entries, want) {
		t.Errorf("entries = %+v, want %+v", playlist.Entries, want)
	}
}

// A playlist Schall has created and not yet pushed anything into comes back
// with no entries at all, and that is an empty playlist rather than a failure.
func TestAnEmptyPlaylistHasNoEntriesRatherThanAnError(t *testing.T) {
	server, _ := recording(t, `{"subsonic-response":{"status":"ok","version":"1.16.1","playlist":`+
		`{"id":"pl-9","name":"Nothing yet","owner":"leo","songCount":0}}}`)

	playlist, err := namedClient(t, server.URL, SyncClientName).
		Playlist(context.Background(), "pl-9")

	if err != nil {
		t.Fatalf("read playlist: %v", err)
	}
	if len(playlist.Entries) != 0 {
		t.Errorf("entries = %+v, want none", playlist.Entries)
	}
}

func TestAPlaylistTheServerAnsweredNothingForIsReportedAsNotFound(t *testing.T) {
	server, _ := recording(t, `{"subsonic-response":{"status":"ok","version":"1.16.1"}}`)

	_, err := namedClient(t, server.URL, SyncClientName).Playlist(context.Background(), "pl-1")

	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want the playlist reported as missing", err)
	}
}

func TestCreatingAPlaylistReportsTheIdItWasGiven(t *testing.T) {
	server, _ := recording(t, `{"subsonic-response":{"status":"ok","version":"1.16.1","playlist":`+
		`{"id":"pl-new","name":"Evening","owner":"leo","songCount":2}}}`)

	id, err := namedClient(t, server.URL, SyncClientName).
		CreatePlaylist(context.Background(), "Evening", []string{"song-1", "song-2"})

	if err != nil {
		t.Fatalf("create playlist: %v", err)
	}
	if id != "pl-new" {
		t.Errorf("id = %q, want pl-new", id)
	}
}

// A playlist of a few hundred tracks is a few hundred songId parameters, which
// is a URL long enough to be cut off somewhere between here and the server.
func TestCreatingAPlaylistSendsItsSongsInTheBody(t *testing.T) {
	server, requests := recording(t, `{"subsonic-response":{"status":"ok","version":"1.16.1",`+
		`"playlist":{"id":"pl-new","name":"Evening"}}}`)

	if _, err := namedClient(t, server.URL, SyncClientName).
		CreatePlaylist(context.Background(), "Evening", []string{"song-1", "song-2"}); err != nil {
		t.Fatalf("create playlist: %v", err)
	}

	request := (*requests)[0]
	if request.method != http.MethodPost {
		t.Errorf("method = %s, want POST", request.method)
	}
	if got := request.body["songId"]; !slices.Equal(got, []string{"song-1", "song-2"}) {
		t.Errorf("songId = %v, want both songs in the body", got)
	}
}

// The id is what the snapshot of the pushed state is keyed by, so a playlist
// that exists somewhere nobody can name is nothing to write down.
func TestAPlaylistCreatedWithoutAnIdIsReported(t *testing.T) {
	server, _ := recording(t, `{"subsonic-response":{"status":"ok","version":"1.16.1"}}`)

	_, err := namedClient(t, server.URL, SyncClientName).
		CreatePlaylist(context.Background(), "Evening", nil)

	if err == nil || !strings.Contains(err.Error(), "what its id is") {
		t.Errorf("error = %v, want the missing id reported", err)
	}
}

func TestUpdatingAPlaylistAddsSongsById(t *testing.T) {
	server, requests := recording(t, `{"subsonic-response":{"status":"ok","version":"1.16.1"}}`)

	if err := namedClient(t, server.URL, SyncClientName).UpdatePlaylist(context.Background(),
		PlaylistUpdate{PlaylistID: "pl-1", AddSongIDs: []string{"song-3", "song-4"}}); err != nil {
		t.Fatalf("update playlist: %v", err)
	}

	request := (*requests)[0]
	if request.method != http.MethodPost {
		t.Errorf("method = %s, want POST", request.method)
	}
	if got := request.body["songIdToAdd"]; !slices.Equal(got, []string{"song-3", "song-4"}) {
		t.Errorf("songIdToAdd = %v, want both songs in the body", got)
	}
}

// Removals are positions the server reads against one listing, so they go over
// as they were read and are not renumbered for each other on the way.
func TestUpdatingAPlaylistRemovesSongsByThePositionsItWasGiven(t *testing.T) {
	server, requests := recording(t, `{"subsonic-response":{"status":"ok","version":"1.16.1"}}`)

	if err := namedClient(t, server.URL, SyncClientName).UpdatePlaylist(context.Background(),
		PlaylistUpdate{PlaylistID: "pl-1", RemoveIndexes: []int{0, 1}}); err != nil {
		t.Fatalf("update playlist: %v", err)
	}

	if got := (*requests)[0].body["songIndexToRemove"]; !slices.Equal(got, []string{"0", "1"}) {
		t.Errorf("songIndexToRemove = %v, want the positions as they were read", got)
	}
}

func TestUpdatingAPlaylistRenamesItWhenAskedTo(t *testing.T) {
	server, requests := recording(t, `{"subsonic-response":{"status":"ok","version":"1.16.1"}}`)

	if err := namedClient(t, server.URL, SyncClientName).UpdatePlaylist(context.Background(),
		PlaylistUpdate{PlaylistID: "pl-1", Name: "Late evening", Comment: "kept by schall"}); err != nil {
		t.Fatalf("update playlist: %v", err)
	}

	request := (*requests)[0]
	if got := request.body.Get("name"); got != "Late evening" {
		t.Errorf("name = %q, want the new name", got)
	}
	if got := request.body.Get("comment"); got != "kept by schall" {
		t.Errorf("comment = %q, want the new comment", got)
	}
}

// An empty field is "leave this alone", so an update that only adds songs must
// not arrive carrying a blank name that would erase the playlist's.
func TestAnUpdateThatSaysNothingAboutTheNameLeavesItAlone(t *testing.T) {
	server, requests := recording(t, `{"subsonic-response":{"status":"ok","version":"1.16.1"}}`)

	if err := namedClient(t, server.URL, SyncClientName).UpdatePlaylist(context.Background(),
		PlaylistUpdate{PlaylistID: "pl-1", AddSongIDs: []string{"song-3"}}); err != nil {
		t.Fatalf("update playlist: %v", err)
	}

	if _, sent := (*requests)[0].body["name"]; sent {
		t.Error("an update that said nothing about the name sent one anyway")
	}
}

func TestAnUpdateWithoutAPlaylistIsRefusedBeforeItIsSent(t *testing.T) {
	server, requests := recording(t, `{"subsonic-response":{"status":"ok","version":"1.16.1"}}`)

	err := namedClient(t, server.URL, SyncClientName).UpdatePlaylist(context.Background(),
		PlaylistUpdate{AddSongIDs: []string{"song-3"}})

	if err == nil || !strings.Contains(err.Error(), "which playlist") {
		t.Fatalf("error = %v, want the missing playlist reported", err)
	}
	if len(*requests) != 0 {
		t.Errorf("requests = %d, want the call never made", len(*requests))
	}
}

func TestASongReportsThePathTheServerGaveIt(t *testing.T) {
	server, _ := recording(t, okSong)

	song, err := namedClient(t, server.URL, SyncClientName).Song(context.Background(), "song-1")

	if err != nil {
		t.Fatalf("read song: %v", err)
	}
	want := Song{ID: "song-1", Title: "First", Path: "/music/Artist/Album/01 First.flac"}
	if song != want {
		t.Errorf("song = %+v, want %+v", song, want)
	}
}

// ADR 0010 reads an id that stopped resolving as an identity that broke, which
// is never a track the user removed — so this one refusal has to be tellable
// from every other reason a request failed.
func TestAnIdThatNoLongerResolvesIsReportedAsNotFound(t *testing.T) {
	server, _ := recording(t, notFound)

	_, err := namedClient(t, server.URL, SyncClientName).Song(context.Background(), "song-gone")

	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want it to read as not found", err)
	}
}

// Only code 70 means "it is not there". A refusal for any other reason is a
// fault to report, and reading it as an absence would let a misconfigured
// account look like a library that lost the track.
func TestARefusalForAnyOtherReasonDoesNotReadAsNotFound(t *testing.T) {
	server, _ := recording(t, `{"subsonic-response":{"status":"failed","version":"1.16.1",`+
		`"error":{"code":50,"message":"Forbidden"}}}`)

	_, err := namedClient(t, server.URL, SyncClientName).Song(context.Background(), "song-1")

	if err == nil {
		t.Fatal("a refusal was reported as a song")
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want a refusal that does not read as an absence", err)
	}
}

// getSong answering ok with nothing in it is not a song, and reading the zero
// value back would pair a playlist entry with a file that has no path at all.
func TestASongTheServerAnsweredNothingForIsReportedAsNotFound(t *testing.T) {
	server, _ := recording(t, `{"subsonic-response":{"status":"ok","version":"1.16.1"}}`)

	_, err := namedClient(t, server.URL, SyncClientName).Song(context.Background(), "song-1")

	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want the missing song reported as not found", err)
	}
}

const searchHit = `{"subsonic-response":{"status":"ok","version":"1.16.1","searchResult3":{"song":[` +
	`{"id":"song-1","title":"First","path":"/music/Artist/Album/01 First.flac"}]}}}`

func TestFindingASongAcceptsThePathItAskedFor(t *testing.T) {
	server, _ := recording(t, searchHit)

	match, err := namedClient(t, server.URL, SyncClientName).
		FindSongByPath(context.Background(), SongLookup{Path: "/music/Artist/Album/01 First.flac"})

	if err != nil {
		t.Fatalf("find song: %v", err)
	}
	if match.Song.ID != "song-1" {
		t.Errorf("song = %+v, want the song at that exact path", match.Song)
	}
}

// The whole reason the search exists is to pair a library file with an id, and
// two albums with a track of the same name are the ordinary case rather than an
// exotic one. A file name is not a file.
func TestASongWhoseNameMatchesButWhoseFolderDoesNotIsRefused(t *testing.T) {
	server, _ := recording(t, `{"subsonic-response":{"status":"ok","version":"1.16.1","searchResult3":`+
		`{"song":[{"id":"song-9","title":"First","path":"/music/Artist/Other Album/01 First.flac"}]}}}`)

	_, err := namedClient(t, server.URL, SyncClientName).
		FindSongByPath(context.Background(), SongLookup{Path: "/music/Artist/Album/01 First.flac"})

	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want the near miss refused rather than taken", err)
	}
}

// A file system that distinguishes two paths by their case is a file system
// where two files can differ by it, so equal-but-for-case is a different file.
func TestAPathDifferingOnlyInCaseIsRefused(t *testing.T) {
	server, _ := recording(t, `{"subsonic-response":{"status":"ok","version":"1.16.1","searchResult3":`+
		`{"song":[{"id":"song-9","title":"First","path":"/music/artist/album/01 first.flac"}]}}}`)

	_, err := namedClient(t, server.URL, SyncClientName).
		FindSongByPath(context.Background(), SongLookup{Path: "/music/Artist/Album/01 First.flac"})

	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want the case difference refused", err)
	}
}

// A path that is a prefix of the one asked for is a different file, however
// close it looks.
func TestAPathThatOnlyStartsTheSameIsRefused(t *testing.T) {
	server, _ := recording(t, `{"subsonic-response":{"status":"ok","version":"1.16.1","searchResult3":`+
		`{"song":[{"id":"song-9","title":"First","path":"/music/Artist/Album/01 First.flac.bak"}]}}}`)

	_, err := namedClient(t, server.URL, SyncClientName).
		FindSongByPath(context.Background(), SongLookup{Path: "/music/Artist/Album/01 First.flac"})

	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want the longer path refused", err)
	}
}

func TestASearchThatFoundNothingIsRefused(t *testing.T) {
	server, _ := recording(t, `{"subsonic-response":{"status":"ok","version":"1.16.1","searchResult3":{}}}`)

	_, err := namedClient(t, server.URL, SyncClientName).
		FindSongByPath(context.Background(), SongLookup{Path: "/music/Artist/Album/01 First.flac"})

	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want nothing found reported as not found", err)
	}
}

// Two songs reporting the same path is not a pair to choose between; there is
// no answer here that is not a guess.
func TestTwoSongsAtTheSamePathAreRefusedRatherThanPickedBetween(t *testing.T) {
	server, _ := recording(t, `{"subsonic-response":{"status":"ok","version":"1.16.1","searchResult3":`+
		`{"song":[{"id":"song-1","path":"/music/Artist/Album/01 First.flac"},`+
		`{"id":"song-2","path":"/music/Artist/Album/01 First.flac"}]}}}`)

	_, err := namedClient(t, server.URL, SyncClientName).
		FindSongByPath(context.Background(), SongLookup{Path: "/music/Artist/Album/01 First.flac"})

	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want the ambiguity refused", err)
	}
}

// Asking about no path at all is a caller mistake, and the answer is still
// nothing rather than whatever the first search result happened to be.
func TestFindingASongWithNoPathIsRefusedWithoutAsking(t *testing.T) {
	server, requests := recording(t, searchHit)

	_, err := namedClient(t, server.URL, SyncClientName).FindSongByPath(context.Background(), SongLookup{Path: "  "})

	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want the empty path refused", err)
	}
	if len(*requests) != 0 {
		t.Errorf("requests = %d, want no search made", len(*requests))
	}
}

// Artists and albums cannot carry a path, so asking for them would only be more
// for the server to do and more for this to read past.
func TestASearchAsksOnlyForSongs(t *testing.T) {
	server, requests := recording(t, searchHit)

	if _, err := namedClient(t, server.URL, SyncClientName).
		FindSongByPath(context.Background(), SongLookup{Path: "/music/Artist/Album/01 First.flac"}); err != nil {
		t.Fatalf("find song: %v", err)
	}

	query := (*requests)[0].parameters
	if query.Get("artistCount") != "0" || query.Get("albumCount") != "0" {
		t.Errorf("counts = artists %q, albums %q, want neither asked for",
			query.Get("artistCount"), query.Get("albumCount"))
	}
}

// Navidrome indexes tags rather than paths, so the file name without its
// extension is the part of a path a search can be made of. It only has to be a
// net wide enough to catch the file; what the file is stays the path's job.
func TestASearchAsksAboutTheFileNameWithoutItsExtension(t *testing.T) {
	server, requests := recording(t, searchHit)

	if _, err := namedClient(t, server.URL, SyncClientName).
		FindSongByPath(context.Background(), SongLookup{Path: "/music/Artist/Album/01 First.flac"}); err != nil {
		t.Fatalf("find song: %v", err)
	}

	if got := (*requests)[0].parameters.Get("query"); got != "01 First" {
		t.Errorf("query = %q, want the file name without its extension", got)
	}
}

// answering serves one body per request, keeping the last for everything after
// it. A lookup may ask more than once, and what the second answer is is the
// whole behaviour in a couple of these.
func answering(t *testing.T, bodies ...string) (*httptest.Server, *[]asked) {
	t.Helper()
	var requests []asked
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		requests = append(requests, asked{method: r.Method, parameters: r.Form, body: r.PostForm})
		body := bodies[len(bodies)-1]
		if len(requests) <= len(bodies) {
			body = bodies[len(requests)-1]
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

const emptySearch = `{"subsonic-response":{"status":"ok","version":"1.16.1","searchResult3":{}}}`

// The file name is a poor question to ask a server that indexes tags. A name
// Schall composed carries bookkeeping no tag will ever hold — the position a
// track had in the run that acquired it, for one — and a server that wants
// every word of a query matched answers nothing at all for it. The words Schall
// read from the file itself are what the server filed it under.
func TestASearchAsksAboutTheWordsSchallHoldsForTheFile(t *testing.T) {
	server, requests := recording(t, searchHit)

	if _, err := namedClient(t, server.URL, SyncClientName).FindSongByPath(
		context.Background(), SongLookup{
			Path:   "/music/Artist/Album/01 First.flac",
			Artist: "Raär",
			Title:  "Sometimes I Hear Sirens",
		}); err != nil {
		t.Fatalf("find song: %v", err)
	}

	if got := (*requests)[0].parameters.Get("query"); got != "Raär Sometimes I Hear Sirens" {
		t.Errorf("query = %q, want the artist and title Schall holds", got)
	}
}

// A file whose tags the server does not answer for is still asked about by
// name: the tags are a better question, not the only one.
func TestAFileTheTagsDidNotFindIsAskedAboutByNameAsWell(t *testing.T) {
	server, requests := answering(t, emptySearch, searchHit)

	match, err := namedClient(t, server.URL, SyncClientName).FindSongByPath(
		context.Background(), SongLookup{
			Path: "/music/Artist/Album/01 First.flac", Artist: "Artist", Title: "First",
		})

	if err != nil {
		t.Fatalf("find song: %v", err)
	}
	if match.Song.ID != "song-1" {
		t.Fatalf("song = %+v, want the song the second search found", match.Song)
	}
	if len(*requests) != 2 || (*requests)[1].parameters.Get("query") != "01 First" {
		t.Errorf("requests = %d, second query %q, want the file name asked after the tags",
			len(*requests), (*requests)[1].parameters.Get("query"))
	}
}

// The two ways a lookup comes back empty are the two competing explanations for
// a file the player will not answer for, and only the client can tell them
// apart. A search that reached nothing is the first.
func TestASearchThatReachedNothingSaysThatIsWhatHappened(t *testing.T) {
	server, _ := recording(t, emptySearch)

	_, err := namedClient(t, server.URL, SyncClientName).
		FindSongByPath(context.Background(), SongLookup{Path: "/music/Artist/Album/01 First.flac"})

	var miss *LookupMiss
	if !errors.As(err, &miss) {
		t.Fatalf("error = %v, want a described miss", err)
	}
	if miss.Reason != MissNoCandidates {
		t.Errorf("reason = %q, want %q", miss.Reason, MissNoCandidates)
	}
}

// And a search that reached other files is the second: the term found music,
// none of it this file.
func TestASearchThatFoundOtherFilesSaysThatIsWhatHappened(t *testing.T) {
	server, _ := recording(t, `{"subsonic-response":{"status":"ok","version":"1.16.1","searchResult3":`+
		`{"song":[{"id":"song-9","title":"First","path":"/music/Artist/Other Album/01 First.flac"}]}}}`)

	_, err := namedClient(t, server.URL, SyncClientName).
		FindSongByPath(context.Background(), SongLookup{Path: "/music/Artist/Album/01 First.flac"})

	var miss *LookupMiss
	if !errors.As(err, &miss) {
		t.Fatalf("error = %v, want a described miss", err)
	}
	if miss.Reason != MissNoPathMatch {
		t.Errorf("reason = %q, want %q", miss.Reason, MissNoPathMatch)
	}
	if miss.Candidates() != 1 {
		t.Errorf("candidates = %d, want the song the search did return counted", miss.Candidates())
	}
}

// A refusal carries what the server offered instead, because the count on its
// own cannot say whether the path map describes the wrong mount or the search
// simply found other music.
func TestARefusalCarriesThePathsTheServerOfferedInstead(t *testing.T) {
	server, _ := recording(t, `{"subsonic-response":{"status":"ok","version":"1.16.1","searchResult3":`+
		`{"song":[{"id":"song-9","title":"First","path":"/mnt/music/Artist/Album/01 First.flac"}]}}}`)

	_, err := namedClient(t, server.URL, SyncClientName).
		FindSongByPath(context.Background(), SongLookup{Path: "/music/Artist/Album/01 First.flac"})

	var miss *LookupMiss
	if !errors.As(err, &miss) {
		t.Fatalf("error = %v, want a described miss", err)
	}
	if len(miss.Attempts) != 1 || len(miss.Attempts[0].Evidence) != 1 ||
		!strings.Contains(miss.Attempts[0].Evidence[0], "/mnt/music/Artist/Album/01 First.flac") {
		t.Errorf("attempts = %+v, want the path the server offered", miss.Attempts)
	}
}

// Two names can be the same characters in two Unicode normal forms, print
// identically, and still not be equal. A refusal that showed them as the same
// text would send whoever reads it looking for a difference the log has hidden.
func TestARefusalTellsApartTwoPathsThatPrintTheSame(t *testing.T) {
	composed := "/music/Ra\u00e4r/01 First.flac"
	decomposed := "/music/Raa\u0308r/01 First.flac"
	server, _ := recording(t, `{"subsonic-response":{"status":"ok","version":"1.16.1","searchResult3":`+
		`{"song":[{"id":"song-9","title":"First","path":"`+decomposed+`"}]}}}`)

	_, err := namedClient(t, server.URL, SyncClientName).
		FindSongByPath(context.Background(), SongLookup{Path: composed})

	var miss *LookupMiss
	if !errors.As(err, &miss) {
		t.Fatalf("error = %v, want a described miss", err)
	}
	offered := miss.Attempts[0].Evidence[0]
	if !strings.Contains(offered, `\u0308`) || !strings.Contains(miss.Error(), `\u00e4`) {
		t.Errorf("miss = %s offering %s, want each form escaped for what it is", miss.Error(), offered)
	}
}

// An ambiguity is an answer. Asking again under another term until one of them
// agrees would be this client shopping for a search that says what it wants.
func TestTwoSongsAtTheSamePathStopTheSearchThere(t *testing.T) {
	server, requests := answering(t, `{"subsonic-response":{"status":"ok","version":"1.16.1","searchResult3":`+
		`{"song":[{"id":"song-1","path":"/music/Artist/Album/01 First.flac"},`+
		`{"id":"song-2","path":"/music/Artist/Album/01 First.flac"}]}}}`, searchHit)

	_, err := namedClient(t, server.URL, SyncClientName).FindSongByPath(
		context.Background(), SongLookup{
			Path: "/music/Artist/Album/01 First.flac", Artist: "Artist", Title: "First",
		})

	var miss *LookupMiss
	if !errors.As(err, &miss) {
		t.Fatalf("error = %v, want a described miss", err)
	}
	if miss.Reason != MissPathRepeated {
		t.Errorf("reason = %q, want %q", miss.Reason, MissPathRepeated)
	}
	if len(*requests) != 1 {
		t.Errorf("requests = %d, want the ambiguity to end the lookup", len(*requests))
	}
}

// A search the server refused is a fault, not an absence: reporting it as
// not-found would tell the sync pass the file is gone from a library it never
// managed to look at.
func TestASearchTheServerRefusedIsNotReportedAsAnAbsence(t *testing.T) {
	server, _ := recording(t, `{"subsonic-response":{"status":"failed","version":"1.16.1",`+
		`"error":{"code":40,"message":"Wrong username or password"}}}`)

	_, err := namedClient(t, server.URL, SyncClientName).
		FindSongByPath(context.Background(), SongLookup{Path: "/music/Artist/Album/01 First.flac"})

	if err == nil {
		t.Fatal("a refused search was reported as a song")
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want a refusal rather than an absence", err)
	}
}

// The mutating calls go over a different code path from the reads, so the
// failures they can meet on the way have to be reported the same way.
func TestAMutatingCallThatCouldNotReachThePlayerSaysSo(t *testing.T) {
	server, _ := recording(t, `{"subsonic-response":{"status":"ok","version":"1.16.1"}}`)
	address := server.URL
	server.Close()

	err := namedClient(t, address, SyncClientName).UpdatePlaylist(context.Background(),
		PlaylistUpdate{PlaylistID: "pl-1", AddSongIDs: []string{"song-3"}})

	if err == nil || !strings.Contains(err.Error(), "could not reach navidrome") {
		t.Errorf("error = %v, want the unreachable player reported", err)
	}
}

// A mutating call is signed like every other one, and the credentials travel
// with the parameters into the body.
func TestAMutatingCallIsSignedInTheBodyItSends(t *testing.T) {
	server, requests := recording(t, `{"subsonic-response":{"status":"ok","version":"1.16.1"}}`)

	if err := namedClient(t, server.URL, SyncClientName).UpdatePlaylist(context.Background(),
		PlaylistUpdate{PlaylistID: "pl-1", AddSongIDs: []string{"song-3"}}); err != nil {
		t.Fatalf("update playlist: %v", err)
	}

	body := (*requests)[0].body
	for _, parameter := range []string{"u", "t", "s", "v", "c"} {
		if body.Get(parameter) == "" {
			t.Errorf("%s was not sent in the body", parameter)
		}
	}
}

// Listing playlists is the first thing sync does, so a server that refused it
// has to be reported rather than read as an account holding none.
func TestListingPlaylistsReportsARefusalRatherThanAnEmptyLibrary(t *testing.T) {
	server, _ := recording(t, `{"subsonic-response":{"status":"failed","version":"1.16.1",`+
		`"error":{"code":40,"message":"Wrong username or password"}}}`)

	_, err := namedClient(t, server.URL, SyncClientName).Playlists(context.Background())

	if err == nil || !strings.Contains(err.Error(), "username and password") {
		t.Errorf("error = %v, want the refusal reported", err)
	}
}

func TestReadingOnePlaylistReportsARefusal(t *testing.T) {
	server, _ := recording(t, `{"subsonic-response":{"status":"failed","version":"1.16.1",`+
		`"error":{"code":50,"message":"Forbidden"}}}`)

	_, err := namedClient(t, server.URL, SyncClientName).Playlist(context.Background(), "pl-1")

	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("error = %v, want the refusal reported", err)
	}
}

func TestCreatingAPlaylistReportsARefusal(t *testing.T) {
	server, _ := recording(t, `{"subsonic-response":{"status":"failed","version":"1.16.1",`+
		`"error":{"code":50,"message":"Forbidden"}}}`)

	_, err := namedClient(t, server.URL, SyncClientName).
		CreatePlaylist(context.Background(), "Evening", []string{"song-1"})

	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("error = %v, want the refusal reported", err)
	}
}

// A path that names a directory and no file has no file name to search on, and
// there is nothing to ask the server about.
func TestAPathWithNoFileNameIsRefusedWithoutAsking(t *testing.T) {
	server, requests := recording(t, searchHit)

	_, err := namedClient(t, server.URL, SyncClientName).FindSongByPath(context.Background(), SongLookup{Path: "/"})

	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want the pathless request refused", err)
	}
	if len(*requests) != 0 {
		t.Errorf("requests = %d, want no search made", len(*requests))
	}
}
