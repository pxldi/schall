package navidrome

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

// keepsStore answers for the database. A reading only ever asks it one thing:
// which player to talk to, and whether there is one.
type keepsStore struct {
	settings db.NavidromeSettingsRow
	err      error
}

func (store keepsStore) NavidromeSettings(context.Context) (db.NavidromeSettingsRow, error) {
	return store.settings, store.err
}

// keptSong is one song the stand-in player holds: where it says the file is,
// the words it has indexed for it, and whether the account starred it.
type keptSong struct {
	id      string
	path    string
	artist  string
	title   string
	starred bool
}

// indexed reports whether the player would offer this song for a query.
//
// Navidrome answers a search out of the tags it indexed, so the stand-in does
// the same: the artist and title it holds, or the file name for a song with no
// tags. It never answers out of the path, which is what makes a search a way of
// getting candidates rather than a way of finding a file.
func (song keptSong) indexed(query string) bool {
	if words := strings.TrimSpace(song.artist + " " + song.title); words != "" {
		return words == query
	}
	return fileNameTerm(song.path) == query
}

// keepsPlayer answers like Navidrome for a fixed set of songs. search3 offers
// the songs whose indexed words the query names; getSong describes one song by
// id and puts the starred time on it only when there is one.
//
// An id it does not hold is refused with Subsonic code 70, the way Navidrome
// refuses an id that stopped resolving.
func keepsPlayer(t *testing.T, songs ...keptSong) (*httptest.Server, *[]url.Values) {
	t.Helper()
	var asked []url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Query())
		switch {
		case strings.HasSuffix(r.URL.Path, "/search3"):
			var found []map[string]any
			for _, song := range songs {
				if !song.indexed(r.URL.Query().Get("query")) {
					continue
				}
				found = append(found, map[string]any{
					"id": song.id, "title": filepath.Base(song.path), "path": song.path,
				})
			}
			writeSubsonic(w, map[string]any{"searchResult3": map[string]any{"song": found}})
		case strings.HasSuffix(r.URL.Path, "/getSong"):
			for _, song := range songs {
				if song.id != r.URL.Query().Get("id") {
					continue
				}
				described := map[string]any{
					"id": song.id, "title": filepath.Base(song.path), "path": song.path,
				}
				if song.starred {
					described["starred"] = "2026-08-01T10:00:00Z"
				}
				writeSubsonic(w, map[string]any{"song": described})
				return
			}
			writeSubsonicError(w, codeNotFound, "Song not found")
		default:
			writeSubsonic(w, nil)
		}
	}))
	t.Cleanup(server.Close)
	return server, &asked
}

func writeSubsonic(w http.ResponseWriter, body map[string]any) {
	payload := map[string]any{"status": "ok", "version": protocolVersion}
	for key, value := range body {
		payload[key] = value
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"subsonic-response": payload})
}

func writeSubsonicError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"subsonic-response": map[string]any{
		"status":  "failed",
		"version": protocolVersion,
		"error":   map[string]any{"code": code, "message": message},
	}})
}

// keepsFor builds a reading against a stand-in player, with the mount mapping
// the deployment uses written as it is configured.
func keepsFor(t *testing.T, baseURL, pairs string) *Keeps {
	t.Helper()
	pathMap, err := ParsePathMap(pairs)
	if err != nil {
		t.Fatalf("ParsePathMap() error = %v", err)
	}
	settings := configuredPlayer()
	settings.BaseURL = baseURL
	return NewKeeps(keepsStore{settings: settings}, pathMap, zerolog.Nop())
}

// onePath is the file most of these tests ask about. oneFile is the question
// Schall puts about it — the path it wrote it to and the tags it read from it —
// and oneSong is the song the player holds for it, filed under the same words
// at the same path.
const onePath = "/music/artist/one.flac"

func oneFile() SongKeep {
	return SongKeep{Path: onePath, Artist: "Artist", Title: "One"}
}

func oneSong(starred bool) keptSong {
	return keptSong{id: "song-1", path: onePath, artist: "Artist", title: "One", starred: starred}
}

func TestASongTheUserStarredIsPresent(t *testing.T) {
	server, _ := keepsPlayer(t, oneSong(true))

	stars, err := keepsFor(t, server.URL, "").Stars(context.Background(), []SongKeep{oneFile()})

	if err != nil {
		t.Fatalf("Stars() error = %v", err)
	}
	if got := stars[onePath].Reading; got != Present {
		t.Errorf("reading = %s, want present", got)
	}
}

func TestASongThePlayerHoldsAndNobodyStarredIsAbsent(t *testing.T) {
	server, _ := keepsPlayer(t, oneSong(false))

	stars, err := keepsFor(t, server.URL, "").Stars(context.Background(), []SongKeep{oneFile()})

	if err != nil {
		t.Fatalf("Stars() error = %v", err)
	}
	star := stars[onePath]
	if star.Reading != Absent {
		t.Errorf("reading = %s, want absent", star.Reading)
	}
	if star.Detail != "" {
		t.Errorf("detail = %q, want none on a real answer", star.Detail)
	}
}

// The reading nothing may be deleted on. A song Navidrome has not indexed was
// never shown to the user, so they could not have starred it, and calling that
// absent would delete music somebody just acquired.
func TestASongThePlayerDoesNotHoldIsUnreadableRatherThanAbsent(t *testing.T) {
	server, _ := keepsPlayer(t, oneSong(false))

	stars, err := keepsFor(t, server.URL, "").Stars(context.Background(), []SongKeep{
		{Path: "/music/artist/two.flac", Artist: "Artist", Title: "Two"},
	})

	if err != nil {
		t.Fatalf("Stars() error = %v", err)
	}
	star := stars["/music/artist/two.flac"]
	if star.Reading != Unreadable {
		t.Fatalf("reading = %s, want unreadable", star.Reading)
	}
	if !strings.Contains(star.Detail, "does not have a song") {
		t.Errorf("detail = %q, want it to say the player has no such song", star.Detail)
	}
	if !strings.Contains(star.Detail, "looked at the library") {
		t.Errorf("detail = %q, want it to name the likeliest reason", star.Detail)
	}
}

// Navidrome indexes tags and not paths, so the tags Schall read from the file
// are how the player is asked which song it is.
func TestAFileIsFoundByTheTagsSchallHoldsForIt(t *testing.T) {
	server, asked := keepsPlayer(t, keptSong{
		id: "song-1", path: onePath, artist: "Artist", title: "One", starred: true,
	})

	stars, err := keepsFor(t, server.URL, "").Stars(context.Background(), []SongKeep{oneFile()})

	if err != nil {
		t.Fatalf("Stars() error = %v", err)
	}
	if got := stars[onePath].Reading; got != Present {
		t.Fatalf("reading = %s, want present", got)
	}
	if got := (*asked)[0].Get("query"); got != "Artist One" {
		t.Errorf("query = %q, want the tags Schall holds for the file", got)
	}
}

// The whole rule at this boundary. The tags name the music, and the player has
// another copy of the same music somewhere else — a star on that copy says
// nothing about this file, so the answer is that nobody could be asked about
// this one.
func TestAFileWhoseTagsFindAnotherCopyIsUnreadableRatherThanAbsent(t *testing.T) {
	server, _ := keepsPlayer(t, keptSong{
		id: "song-9", path: "/music/other/one.flac", artist: "Artist", title: "One", starred: true,
	})

	stars, err := keepsFor(t, server.URL, "").Stars(context.Background(), []SongKeep{oneFile()})

	if err != nil {
		t.Fatalf("Stars() error = %v", err)
	}
	star := stars[onePath]
	if star.Reading != Unreadable {
		t.Fatalf("reading = %s, want unreadable", star.Reading)
	}
	if !strings.Contains(star.Detail, "a different file") {
		t.Errorf("detail = %q, want it to say the player answered with another file", star.Detail)
	}
}

// A file with no tags to ask with is still asked about, by its name, which is
// what the lookup falls back to.
func TestAFileWithNoTagsIsAskedAboutByItsName(t *testing.T) {
	server, asked := keepsPlayer(t, keptSong{id: "song-1", path: onePath, starred: true})

	stars, err := keepsFor(t, server.URL, "").Stars(
		context.Background(), []SongKeep{{Path: onePath}})

	if err != nil {
		t.Fatalf("Stars() error = %v", err)
	}
	if got := stars[onePath].Reading; got != Present {
		t.Fatalf("reading = %s, want present", got)
	}
	if got := (*asked)[0].Get("query"); got != "one" {
		t.Errorf("query = %q, want the file name without its extension", got)
	}
}

// The file name is a poor question — Schall composes names that carry
// bookkeeping no tag holds — so it often finds nothing. Finding nothing is
// still not the user declining to keep the file.
func TestAFileWithNoTagsThatItsNameDoesNotFindIsUnreadableRatherThanAbsent(t *testing.T) {
	server, _ := keepsPlayer(t, keptSong{
		id: "song-1", path: onePath, artist: "Artist", title: "One", starred: true,
	})

	stars, err := keepsFor(t, server.URL, "").Stars(
		context.Background(), []SongKeep{{Path: onePath}})

	if err != nil {
		t.Fatalf("Stars() error = %v", err)
	}
	star := stars[onePath]
	if star.Reading != Unreadable {
		t.Fatalf("reading = %s, want unreadable", star.Reading)
	}
	if star.Detail == "" {
		t.Error("a file nobody could ask about was recorded without saying why")
	}
}

// A song found by path whose id then resolves to nothing is the same absence
// arriving one call later, and is read the same way.
func TestASongWhoseIdStoppedResolvingIsUnreadable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/search3") {
			writeSubsonic(w, map[string]any{"searchResult3": map[string]any{"song": []map[string]any{
				{"id": "song-1", "title": "one", "path": onePath},
			}}})
			return
		}
		writeSubsonicError(w, codeNotFound, "Song not found")
	}))
	t.Cleanup(server.Close)

	stars, err := keepsFor(t, server.URL, "").Stars(context.Background(), []SongKeep{oneFile()})

	if err != nil {
		t.Fatalf("Stars() error = %v", err)
	}
	star := stars[onePath]
	if star.Reading != Unreadable {
		t.Fatalf("reading = %s, want unreadable", star.Reading)
	}
	if !strings.Contains(star.Detail, "song-1") {
		t.Errorf("detail = %q, want it to name the song that stopped resolving", star.Detail)
	}
}

func TestASongThePlayerRefusedToDescribeIsUnreadable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSubsonicError(w, codeNotAllowed, "Forbidden")
	}))
	t.Cleanup(server.Close)

	stars, err := keepsFor(t, server.URL, "").Stars(context.Background(), []SongKeep{oneFile()})

	if err != nil {
		t.Fatalf("Stars() error = %v", err)
	}
	star := stars[onePath]
	if star.Reading != Unreadable {
		t.Fatalf("reading = %s, want unreadable", star.Reading)
	}
	if star.Detail == "" {
		t.Error("a refusal was recorded without saying anything about itself")
	}
}

func TestASongThePlayerAnsweredWithAnHTTPErrorIsUnreadable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/search3") {
			writeSubsonic(w, map[string]any{"searchResult3": map[string]any{"song": []map[string]any{
				{"id": "song-1", "title": "one", "path": onePath},
			}}})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	stars, err := keepsFor(t, server.URL, "").Stars(context.Background(), []SongKeep{oneFile()})

	if err != nil {
		t.Fatalf("Stars() error = %v", err)
	}
	if got := stars[onePath].Reading; got != Unreadable {
		t.Errorf("reading = %s, want unreadable", got)
	}
}

// An installation with no player has not said that nobody keeps this music. It
// has said nothing, and a map of absences would be a fabricated answer.
func TestAReadingWithNoPlayerConfiguredIsAnError(t *testing.T) {
	keeps := NewKeeps(keepsStore{err: pgx.ErrNoRows}, PathMap{}, zerolog.Nop())

	stars, err := keeps.Stars(context.Background(), []SongKeep{oneFile()})

	if !errors.Is(err, ErrNoPlayer) {
		t.Fatalf("Stars() error = %v, want ErrNoPlayer", err)
	}
	if len(stars) != 0 {
		t.Errorf("stars = %v, want nothing at all", stars)
	}
}

func TestAReadingAgainstAPlayerSwitchedOffIsAnError(t *testing.T) {
	settings := configuredPlayer()
	settings.Enabled = false

	stars, err := NewKeeps(keepsStore{settings: settings}, PathMap{}, zerolog.Nop()).
		Stars(context.Background(), []SongKeep{oneFile()})

	if !errors.Is(err, ErrNoPlayer) {
		t.Fatalf("Stars() error = %v, want ErrNoPlayer", err)
	}
	if len(stars) != 0 {
		t.Errorf("stars = %v, want nothing at all", stars)
	}
}

func TestSettingsThatCouldNotBeReadStopTheReading(t *testing.T) {
	keeps := NewKeeps(keepsStore{err: errors.New("the database is down")}, PathMap{}, zerolog.Nop())

	if _, err := keeps.Stars(context.Background(), []SongKeep{oneFile()}); err == nil {
		t.Fatal("a reading was reported against settings nobody could read")
	}
}

// One batch, three fates: the player is asked about every file it was given and
// one file it cannot answer for does not spoil the answers about the others.
func TestOneBatchCarriesAnAnswerForEveryFileItWasGiven(t *testing.T) {
	server, _ := keepsPlayer(t,
		oneSong(true),
		keptSong{id: "song-2", path: "/music/artist/two.flac", artist: "Artist", title: "Two"},
	)

	stars, err := keepsFor(t, server.URL, "").Stars(context.Background(), []SongKeep{
		oneFile(),
		{Path: "/music/artist/two.flac", Artist: "Artist", Title: "Two"},
		{Path: "/music/artist/three.flac", Artist: "Artist", Title: "Three"},
	})

	if err != nil {
		t.Fatalf("Stars() error = %v", err)
	}
	want := map[string]Reading{
		onePath:                    Present,
		"/music/artist/two.flac":   Absent,
		"/music/artist/three.flac": Unreadable,
	}
	if len(stars) != len(want) {
		t.Fatalf("stars = %v, want one answer per file", stars)
	}
	for path, reading := range want {
		if got := stars[path].Reading; got != reading {
			t.Errorf("reading for %s = %s, want %s", path, got, reading)
		}
	}
}

// Schall and Navidrome read the same directory through their own mounts, so the
// path an answer is judged against is the Schall path put in the player's terms.
func TestAStarIsReadThroughTheMountMapping(t *testing.T) {
	server, _ := keepsPlayer(t, keptSong{
		id: "song-1", path: "/music-schall/artist/one.flac",
		artist: "Artist", title: "One", starred: true,
	})

	stars, err := keepsFor(t, server.URL, "/music=/music-schall").Stars(
		context.Background(), []SongKeep{oneFile()})

	if err != nil {
		t.Fatalf("Stars() error = %v", err)
	}
	if got := stars[onePath].Reading; got != Present {
		t.Errorf("reading = %s, want present", got)
	}
}

// The song is found by comparing the path Navidrome reports with the path
// Schall holds, and a player row created under "schall" reports a made-up
// display path forever (docs/decisions/0010).
func TestAReadingIntroducesItselfUnderTheSyncClientName(t *testing.T) {
	server, asked := keepsPlayer(t, oneSong(false))

	if _, err := keepsFor(t, server.URL, "").Stars(
		context.Background(), []SongKeep{oneFile()}); err != nil {
		t.Fatalf("Stars() error = %v", err)
	}

	if len(*asked) == 0 {
		t.Fatal("the player was never asked anything")
	}
	for _, query := range *asked {
		if got := query.Get("c"); got != SyncClientName {
			t.Fatalf("client name = %q, want %q", got, SyncClientName)
		}
	}
}

func TestAFileNamedTwiceIsAskedAboutOnce(t *testing.T) {
	server, asked := keepsPlayer(t, oneSong(false))

	stars, err := keepsFor(t, server.URL, "").Stars(
		context.Background(), []SongKeep{oneFile(), oneFile()})

	if err != nil {
		t.Fatalf("Stars() error = %v", err)
	}
	if len(stars) != 1 {
		t.Fatalf("stars = %v, want one answer", stars)
	}
	if len(*asked) != 2 {
		t.Errorf("requests = %d, want the search and the song read once each", len(*asked))
	}
}

func TestAReadingOfNothingAsksThePlayerNothing(t *testing.T) {
	server, asked := keepsPlayer(t)

	stars, err := keepsFor(t, server.URL, "").Stars(context.Background(), nil)

	if err != nil {
		t.Fatalf("Stars() error = %v", err)
	}
	if len(stars) != 0 || len(*asked) != 0 {
		t.Errorf("stars = %v after %d requests, want an empty answer and no questions",
			stars, len(*asked))
	}
}

func TestACallerThatGaveUpGetsNoAnswersRatherThanAbsences(t *testing.T) {
	server, _ := keepsPlayer(t, oneSong(false))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	stars, err := keepsFor(t, server.URL, "").Stars(ctx, []SongKeep{oneFile()})

	if err == nil {
		t.Fatal("a reading was reported for a caller that gave up")
	}
	if len(stars) != 0 {
		t.Errorf("stars = %v, want nothing at all", stars)
	}
}

// The three readings are told apart in a log line as well as in code, and the
// one that is not an answer says so rather than printing as a number.
func TestEachReadingSaysWhichItIs(t *testing.T) {
	for reading, want := range map[Reading]string{
		Present:     "present",
		Absent:      "absent",
		Unreadable:  "unreadable",
		Reading(99): "unreadable",
	} {
		if got := reading.String(); got != want {
			t.Errorf("Reading(%d).String() = %q, want %q", int(reading), got, want)
		}
	}
}
