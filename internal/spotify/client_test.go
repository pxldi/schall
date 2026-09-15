package spotify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient(Options{
		APIURL:       server.URL,
		AccountsURL:  server.URL,
		ClientID:     "id",
		ClientSecret: "secret",
		HTTPClient:   server.Client(),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func trackRow(id, title, artist string, durationMS int, isrc string) map[string]any {
	return map[string]any{
		"is_local": false,
		"item": map[string]any{
			"id": id, "name": title, "duration_ms": durationMS, "type": "track",
			"external_ids": map[string]any{"isrc": isrc},
			"artists":      []any{map[string]any{"name": artist}},
			"album":        map[string]any{"name": title},
		},
	}
}

func TestNewClientRejectsIncompleteSettings(t *testing.T) {
	for _, options := range []Options{
		{ClientSecret: "secret"},
		{ClientID: "id"},
		{ClientID: "id", ClientSecret: "secret", APIURL: "://broken"},
	} {
		if _, err := NewClient(options); err == nil {
			t.Fatalf("NewClient(%+v) accepted incomplete settings", options)
		}
	}
}

// The playlist arrives in the shape the wire serves today: the first page
// rides on the playlist object under "items", rows carry the track under
// "item", and later pages come from /items.
func TestPlaylistReadsTheNewWireShape(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/playlists/abc123", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("playlist request carried authorization %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":        "musik",
			"description": "d",
			"snapshot_id": "rev1",
			"owner":       map[string]any{"display_name": "Poldi"},
			"items": map[string]any{
				"total": 3,
				"items": []any{
					trackRow("t1", "Motorola", "$NOT", 145319, "QM42K1858315"),
					map[string]any{"is_local": true, "item": map[string]any{
						"id": "", "name": "local file", "type": "track",
					}},
				},
			},
		})
	})
	mux.HandleFunc("/playlists/abc123/items", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("offset"); got != "2" {
			t.Fatalf("second page asked for offset %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"total": 3,
			"items": []any{trackRow("t3", "HDMI", "BONES", 139000, "ca5kr1567859")},
		})
	})

	playlist, err := testClient(t, mux).Playlist(context.Background(), "token", "abc123")
	if err != nil {
		t.Fatalf("Playlist: %v", err)
	}
	if playlist.Name != "musik" || playlist.SnapshotID != "rev1" || playlist.OwnerName != "Poldi" {
		t.Fatalf("playlist metadata read wrong: %+v", playlist)
	}
	if len(playlist.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(playlist.Entries))
	}
	if playlist.Entries[0].TrackID != "t1" || playlist.Entries[0].ISRC != "QM42K1858315" {
		t.Fatalf("first entry read wrong: %+v", playlist.Entries[0])
	}
	if !playlist.Entries[1].Unusable {
		t.Fatal("the local row should be unusable, not dropped or usable")
	}
	if playlist.Entries[2].ISRC != "CA5KR1567859" {
		t.Fatalf("ISRC should be uppercased, got %q", playlist.Entries[2].ISRC)
	}
}

func TestPlaylistObeysRetryAfter(t *testing.T) {
	var calls int
	mux := http.NewServeMux()
	mux.HandleFunc("/playlists/abc123", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": "musik", "snapshot_id": "rev1",
			"items": map[string]any{"total": 0, "items": []any{}},
		})
	})
	if _, err := testClient(t, mux).Playlist(context.Background(), "token", "abc123"); err != nil {
		t.Fatalf("Playlist after 429: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected one retry, saw %d calls", calls)
	}
}

func TestRevokedAuthorizationIsItsOwnError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	client := testClient(t, mux)
	if _, err := client.Playlist(context.Background(), "token", "abc123"); err != ErrUnauthorized {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
	if _, err := client.Refresh(context.Background(), "stale"); err != ErrUnauthorized {
		t.Fatalf("refresh: expected ErrUnauthorized, got %v", err)
	}
	if _, err := client.Me(context.Background(), "token"); err != ErrUnauthorized {
		t.Fatalf("profile: expected ErrUnauthorized, got %v", err)
	}
}

func TestExchangeSpeaksBasicAuth(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/token", func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "id" || pass != "secret" {
			t.Fatalf("token request carried wrong credentials %q %q", user, pass)
		}
		if got := r.FormValue("grant_type"); got != "authorization_code" {
			t.Fatalf("grant_type = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at", "refresh_token": "rt", "expires_in": 3600,
		})
	})
	token, err := testClient(t, mux).Exchange(context.Background(), "code", "http://cb")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if token.AccessToken != "at" || token.RefreshToken != "rt" {
		t.Fatalf("token read wrong: %+v", token)
	}
}

// A client built without one of its own still bounds how long it waits, or a
// Spotify that never answers would hold an ingestion job for as long as the
// process lives.
func TestAClientBuiltWithoutAnHTTPClientStillBoundsItsWaiting(t *testing.T) {
	client, err := NewClient(Options{ClientID: "id", ClientSecret: "secret"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if client.httpClient.Timeout <= 0 {
		t.Fatal("the client waits forever for an answer")
	}
}

// The authorization link asks for the scopes ingestion needs and carries the
// CSRF token the callback has to echo back.
func TestTheAuthorizationLinkAsksForWhatIngestionNeeds(t *testing.T) {
	mux := http.NewServeMux()
	address := testClient(t, mux).AuthorizeURL("http://schall.test/callback", "a-state")

	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatalf("parse the authorization URL: %v", err)
	}
	query := parsed.Query()
	for key, want := range map[string]string{
		"client_id":     "id",
		"response_type": "code",
		"redirect_uri":  "http://schall.test/callback",
		"scope":         Scopes,
		"state":         "a-state",
	} {
		if got := query.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// A connection nobody ever finished has no refresh token, and there is nothing
// to ask Spotify about. The account has to be connected again, never retried.
func TestARefreshWithNoStoredTokenNeverReachesSpotify(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) {
		t.Error("Spotify was asked to refresh nothing")
	})

	if _, err := testClient(t, mux).Refresh(context.Background(), "  "); err != ErrUnauthorized {
		t.Fatalf("error = %v, want ErrUnauthorized", err)
	}
}

// The accounts service answering with something that is not a token response is
// a failure of the exchange, not an empty token quietly stored.
func TestATokenResponseThatCannotBeReadIsReported(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/token", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>an outage page</html>"))
	})

	_, err := testClient(t, mux).Exchange(context.Background(), "code", "http://cb")

	if err == nil || !strings.Contains(err.Error(), "decode Spotify token response") {
		t.Fatalf("error = %v, want the unreadable answer reported", err)
	}
}

func TestATokenResponseHoldingNoAccessTokenIsRefused(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/token", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"expires_in": 3600})
	})

	_, err := testClient(t, mux).Exchange(context.Background(), "code", "http://cb")

	if err == nil || !strings.Contains(err.Error(), "held no access token") {
		t.Fatalf("error = %v, want the empty token refused", err)
	}
}

// A status that is neither success nor rejected credentials is Spotify having a
// bad afternoon, and it has to read as one so nobody disconnects the account
// over it.
func TestATokenRequestSpotifyBrokeOnIsNotAnAuthorizationFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/token", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	_, err := testClient(t, mux).Exchange(context.Background(), "code", "http://cb")

	if errors.Is(err, ErrUnauthorized) {
		t.Fatalf("error = %v, want an outage not read as revoked credentials", err)
	}
	if err == nil || err.Error() != "Spotify returned HTTP 500" {
		t.Fatalf("error = %v, want the status reported", err)
	}
}

func TestAnAccountsServiceThatCouldNotBeReachedIsReported(t *testing.T) {
	server := httptest.NewServer(http.NewServeMux())
	address := server.URL
	server.Close()
	client, err := NewClient(Options{
		APIURL: address, AccountsURL: address, ClientID: "id", ClientSecret: "secret",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if _, err := client.Exchange(context.Background(), "code", "http://cb"); err == nil ||
		!strings.Contains(err.Error(), "request Spotify token") {
		t.Fatalf("error = %v, want the unreachable service reported", err)
	}
}

func TestMeReportsWhoseAuthorizationThisIs(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/me", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Errorf("profile request carried authorization %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "poldi", "display_name": "Poldi"})
	})

	account, err := testClient(t, mux).Me(context.Background(), "token")
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if account.ID != "poldi" || account.DisplayName != "Poldi" {
		t.Fatalf("account = %+v", account)
	}
}

// An authorization that names nobody is not an account somebody can be shown as
// connected.
func TestAProfileNamingNobodyIsRefused(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/me", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"display_name": "Poldi"})
	})

	if _, err := testClient(t, mux).Me(context.Background(), "token"); err == nil ||
		!strings.Contains(err.Error(), "held no account") {
		t.Fatalf("error = %v, want the profile without an account refused", err)
	}
}

// The idempotency check asks for the two fields it reads and nothing else, so a
// playlist of ten thousand rows costs one small response.
func TestPlaylistRevisionAsksOnlyForWhatTheIdempotencyCheckReads(t *testing.T) {
	var fields string
	mux := http.NewServeMux()
	mux.HandleFunc("/playlists/abc123", func(w http.ResponseWriter, r *http.Request) {
		fields = r.URL.Query().Get("fields")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": "musik", "snapshot_id": "rev1",
			"items": map[string]any{"total": 400, "items": []any{}},
		})
	})

	name, snapshot, err := testClient(t, mux).
		PlaylistRevision(context.Background(), "token", "abc123")
	if err != nil {
		t.Fatalf("PlaylistRevision: %v", err)
	}
	if name != "musik" || snapshot != "rev1" {
		t.Fatalf("name = %q, snapshot = %q", name, snapshot)
	}
	if fields != "name,snapshot_id" {
		t.Fatalf("fields = %q, want only the two the check reads", fields)
	}
}

// A playlist that was deleted or made private is a fact about the playlist, not
// a failure of Schall, so it has its own error to be told apart.
func TestAPlaylistSpotifyHasNoRecordOfIsItsOwnError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	if _, _, err := testClient(t, mux).
		PlaylistRevision(context.Background(), "token", "abc123"); err != ErrNotFound {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

// A playlist object with no name is not one Schall can file, and reading on
// would build a playlist under an empty title.
func TestAPlaylistWithNoNameIsRefused(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/playlists/abc123", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"snapshot_id": "rev1",
			"items":       map[string]any{"total": 0, "items": []any{}},
		})
	})

	if _, err := testClient(t, mux).Playlist(context.Background(), "token", "abc123"); err == nil ||
		!strings.Contains(err.Error(), "held no name") {
		t.Fatalf("error = %v, want the nameless playlist refused", err)
	}
}

// Spotify's total and Spotify's pages disagree often enough that paging on the
// total alone loops forever. A page that came back empty ends the read.
func TestAPageThatCameBackEmptyEndsTheRead(t *testing.T) {
	var pages int
	mux := http.NewServeMux()
	mux.HandleFunc("/playlists/abc123", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": "musik", "snapshot_id": "rev1",
			"items": map[string]any{
				"total": 500,
				"items": []any{trackRow("t1", "Motorola", "$NOT", 145319, "QM42K1858315")},
			},
		})
	})
	mux.HandleFunc("/playlists/abc123/items", func(w http.ResponseWriter, _ *http.Request) {
		pages++
		_ = json.NewEncoder(w).Encode(map[string]any{"total": 500, "items": []any{}})
	})

	playlist, err := testClient(t, mux).Playlist(context.Background(), "token", "abc123")
	if err != nil {
		t.Fatalf("Playlist: %v", err)
	}
	if pages != 1 {
		t.Fatalf("asked for %d pages, want the read to stop at the empty one", pages)
	}
	if len(playlist.Entries) != 1 {
		t.Fatalf("entries = %d, want the one row there was", len(playlist.Entries))
	}
}

// Half a playlist is not a playlist. A page that failed has to stop the read
// rather than hand back the rows that happened to arrive first.
func TestAPageThatFailedStopsTheReadRatherThanTruncatingIt(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/playlists/abc123", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": "musik", "snapshot_id": "rev1",
			"items": map[string]any{
				"total": 3,
				"items": []any{trackRow("t1", "Motorola", "$NOT", 145319, "QM42K1858315")},
			},
		})
	})
	mux.HandleFunc("/playlists/abc123/items", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	if _, err := testClient(t, mux).Playlist(context.Background(), "token", "abc123"); err == nil {
		t.Fatal("a playlist read that lost a page was reported as complete")
	}
}

// A body that is not the JSON it claims to be is a failure, never an empty
// playlist that would look like every track having been removed.
func TestAnAnswerThatIsNotJSONIsReported(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/playlists/abc123", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>a proxy page</html>"))
	})

	_, err := testClient(t, mux).Playlist(context.Background(), "token", "abc123")

	if err == nil || !strings.Contains(err.Error(), "decode Spotify response") {
		t.Fatalf("error = %v, want the unreadable answer reported", err)
	}
}

// A status with no meaning here is reported as the status it was, so nobody
// disconnects an account over an outage.
func TestAStatusWithNoMeaningHereIsReportedAsItself(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})

	_, err := testClient(t, mux).Playlist(context.Background(), "token", "abc123")

	if errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want an outage kept apart from an answer about the playlist", err)
	}
	if err == nil || err.Error() != "Spotify returned HTTP 502" {
		t.Fatalf("error = %v, want the status reported", err)
	}
}

func TestSpotifyThatCouldNotBeReachedIsReported(t *testing.T) {
	server := httptest.NewServer(http.NewServeMux())
	address := server.URL
	server.Close()
	client, err := NewClient(Options{
		APIURL: address, AccountsURL: address, ClientID: "id", ClientSecret: "secret",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if _, err := client.Playlist(context.Background(), "token", "abc123"); err == nil ||
		!strings.Contains(err.Error(), "request Spotify") {
		t.Fatalf("error = %v, want the unreachable service reported", err)
	}
}

// Obeying a Retry-After is waiting, and a caller that has stopped waiting is not
// kept in it.
func TestACallerThatGaveUpDuringABackoffIsNotHeldInIt(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := testClient(t, mux).Playlist(ctx, "token", "abc123")

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want the caller's deadline honoured", err)
	}
}

// A Spotify that never stops asking to slow down has to end in an error rather
// than in a loop, or the job holding it never finishes.
func TestSpotifyThatKeepsAskingToSlowDownEventuallyGivesUp(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	})

	_, err := testClient(t, mux).Playlist(context.Background(), "token", "abc123")

	if err == nil || !strings.Contains(err.Error(), "kept asking to slow down") {
		t.Fatalf("error = %v, want the client to give up rather than loop", err)
	}
}

// A row that is not a track Schall can fetch is reported as unusable rather than
// dropped, so an ingestion can say what it skipped instead of silently shrinking
// the playlist.
func TestARowThatIsNotAFetchableTrackIsUnusableRatherThanDropped(t *testing.T) {
	rows := []any{
		map[string]any{"is_local": false, "item": nil},
		map[string]any{"is_local": false, "item": map[string]any{
			"id": "e1", "name": "an episode", "type": "episode",
		}},
		map[string]any{"is_local": false, "item": map[string]any{
			"id": "l1", "name": "a local file", "type": "track", "is_local": true,
		}},
		map[string]any{"is_local": false, "item": map[string]any{
			"id": "", "name": "no identifier", "type": "track",
		}},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/playlists/abc123", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": "musik", "snapshot_id": "rev1",
			"items": map[string]any{"total": len(rows), "items": rows},
		})
	})

	playlist, err := testClient(t, mux).Playlist(context.Background(), "token", "abc123")
	if err != nil {
		t.Fatalf("Playlist: %v", err)
	}
	if len(playlist.Entries) != len(rows) {
		t.Fatalf("entries = %d, want every row kept", len(playlist.Entries))
	}
	for index, entry := range playlist.Entries {
		if !entry.Unusable {
			t.Errorf("entry %d = %+v, want it reported as unusable", index, entry)
		}
	}
}

// Spotify lists a track's artists in credit order and the first is the primary
// credit, which is what a search is built from.
func TestATracksArtistsKeepSpotifysOrder(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/playlists/abc123", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": "musik", "snapshot_id": "rev1",
			"items": map[string]any{"total": 1, "items": []any{map[string]any{
				"is_local": false,
				"item": map[string]any{
					"id": "t1", "name": "Bad and Boujee", "type": "track",
					"duration_ms": 343150,
					"album":       map[string]any{"name": "Culture"},
					"artists": []any{
						map[string]any{"name": "Migos"},
						map[string]any{"name": "  "},
						map[string]any{"name": "Lil Uzi Vert"},
					},
				},
			}}},
		})
	})

	playlist, err := testClient(t, mux).Playlist(context.Background(), "token", "abc123")
	if err != nil {
		t.Fatalf("Playlist: %v", err)
	}
	got := playlist.Entries[0].Artists
	if len(got) != 2 || got[0] != "Migos" || got[1] != "Lil Uzi Vert" {
		t.Fatalf("artists = %v, want Spotify's order with the nameless credit left out", got)
	}
}

func TestPlaylistID(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"https://open.spotify.com/playlist/7zTzKBnIXnetASx3wWxwcp?si=x", "7zTzKBnIXnetASx3wWxwcp", true},
		{"spotify:playlist:7zTzKBnIXnetASx3wWxwcp", "7zTzKBnIXnetASx3wWxwcp", true},
		{"7zTzKBnIXnetASx3wWxwcp", "7zTzKBnIXnetASx3wWxwcp", true},
		{"https://open.spotify.com/album/xyz", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, err := PlaylistID(tc.in)
		if tc.ok && (err != nil || got != tc.want) {
			t.Fatalf("PlaylistID(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
		if !tc.ok && err == nil {
			t.Fatalf("PlaylistID(%q) accepted a non-playlist", tc.in)
		}
	}
}
