package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

type fakePlayerStore struct {
	*fakeStore
	settings   db.NavidromeSettingsRow
	configured bool
	saved      db.SaveNavidromeSettingsParams
	recorded   string
	// Each of these breaks one query on its own, so a store with nothing stored
	// and one that could not be read are different tests.
	settingsErr error
	saveErr     error
	recordErr   error
}

func (store *fakePlayerStore) NavidromeSettings(context.Context) (db.NavidromeSettingsRow, error) {
	if store.settingsErr != nil {
		return db.NavidromeSettingsRow{}, store.settingsErr
	}
	if !store.configured {
		return db.NavidromeSettingsRow{}, pgx.ErrNoRows
	}
	return store.settings, nil
}

func (store *fakePlayerStore) SaveNavidromeSettings(
	_ context.Context, params db.SaveNavidromeSettingsParams,
) (db.NavidromeSettingsRow, error) {
	if store.saveErr != nil {
		return db.NavidromeSettingsRow{}, store.saveErr
	}
	store.saved = params
	password := params.Password
	if password == "" {
		password = store.settings.Password
	}
	store.configured = true
	store.settings = db.NavidromeSettingsRow{
		BaseURL: params.BaseURL, Username: params.Username, Password: password,
		Enabled: params.Enabled, ConnectionStatus: "unknown",
	}
	return store.settings, nil
}

func (store *fakePlayerStore) RecordNavidromeConnection(
	_ context.Context, status, detail, failure string,
) (db.NavidromeSettingsRow, error) {
	if store.recordErr != nil {
		return db.NavidromeSettingsRow{}, store.recordErr
	}
	store.recorded = status
	store.settings.ConnectionStatus = status
	store.settings.ConnectionDetail = pgtype.Text{String: detail, Valid: detail != ""}
	store.settings.ConnectionError = pgtype.Text{String: failure, Valid: failure != ""}
	return store.settings, nil
}

// fakePlaybackNotifier answers the way the real notifier does when it is
// asked to tell the player right now: it is what the "Tell Navidrome now"
// route calls, kept separate from fakePlayerStore so a test can break the
// telling without breaking the settings underneath it.
type fakePlaybackNotifier struct {
	told int
	err  error
	// onNotice runs after a successful notice, standing in for the real
	// notifier writing last_notified_at down.
	onNotice func()
}

func (notifier *fakePlaybackNotifier) NoticeLibraryChange(context.Context) error {
	notifier.told++
	if notifier.err != nil {
		return notifier.err
	}
	if notifier.onNotice != nil {
		notifier.onNotice()
	}
	return nil
}

func configuredPlayer(baseURL string) db.NavidromeSettingsRow {
	return db.NavidromeSettingsRow{
		BaseURL: baseURL, Username: "schall", Password: "player-password",
		Enabled: true, ConnectionStatus: "unknown",
	}
}

func TestThePlayerPasswordIsNeverReturnedToThePage(t *testing.T) {
	store := &fakePlayerStore{
		fakeStore: &fakeStore{}, configured: true,
		settings: configuredPlayer("http://navidrome:4533"),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/settings/navidrome", nil))

	if strings.Contains(response.Body.String(), "player-password") {
		t.Fatalf("response leaked the password: %s", response.Body)
	}
	var payload navidromeSettingsResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Configured || !payload.PasswordSet || payload.Username != "schall" {
		t.Fatalf("payload = %#v", payload)
	}
}

// The page is never shown the password, so an empty one means "leave it alone"
// rather than "erase it".
func TestSavingThePlayerWithAnEmptyPasswordLeavesTheStoredOneAlone(t *testing.T) {
	store := &fakePlayerStore{
		fakeStore: &fakeStore{}, configured: true,
		settings: configuredPlayer("http://navidrome:4533"),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPut, "/api/v1/settings/navidrome",
		strings.NewReader(`{"baseUrl":"http://navidrome:4533/","username":"schall","password":"","enabled":false}`)))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.saved.Password != "" {
		t.Fatalf("saved password = %q, want the stored one untouched", store.saved.Password)
	}
	if store.saved.BaseURL != "http://navidrome:4533" {
		t.Errorf("saved base URL = %q, want the trailing slash trimmed", store.saved.BaseURL)
	}
}

func TestAFirstPlayerWithoutAPasswordIsRefused(t *testing.T) {
	store := &fakePlayerStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPut, "/api/v1/settings/navidrome",
		strings.NewReader(`{"baseUrl":"http://navidrome:4533","username":"schall","password":"","enabled":true}`)))

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestSavingThePlayerValidatesTheAddressAndAccount(t *testing.T) {
	store := &fakePlayerStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	for name, body := range map[string]string{
		"missing base URL": `{"baseUrl":"","username":"schall","password":"p","enabled":true}`,
		"relative URL":     `{"baseUrl":"navidrome:4533","username":"schall","password":"p","enabled":true}`,
		"wrong scheme":     `{"baseUrl":"ftp://navidrome","username":"schall","password":"p","enabled":true}`,
		"missing username": `{"baseUrl":"http://navidrome:4533","username":"","password":"p","enabled":true}`,
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(
			http.MethodPut, "/api/v1/settings/navidrome", strings.NewReader(body)))
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status = %d; body = %s", name, response.Code, response.Body)
		}
	}
}

func TestTestingAPlayerNobodyConfiguredIsRefused(t *testing.T) {
	store := &fakePlayerStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/settings/navidrome/test", nil))

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// A check the player refused is an answer, not a broken request: the page has
// to be told what went wrong and the stored status has to remember it.
func TestACheckThePlayerRefusedIsRecordedAsFailed(t *testing.T) {
	player := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"subsonic-response":{"status":"failed","version":"1.16.1",` +
			`"error":{"code":40,"message":"Wrong username or password"}}}`))
	}))
	defer player.Close()

	store := &fakePlayerStore{
		fakeStore: &fakeStore{}, configured: true, settings: configuredPlayer(player.URL),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/settings/navidrome/test", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.recorded != "failed" {
		t.Errorf("recorded status = %q, want the refusal remembered", store.recorded)
	}
	var payload navidromeSettingsResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.ConnectionError == nil || !strings.Contains(*payload.ConnectionError, "username and password") {
		t.Errorf("connection error = %v, want it to name the credentials", payload.ConnectionError)
	}
}

func TestACheckThePlayerAnsweredIsRecordedAsConnected(t *testing.T) {
	player := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"subsonic-response":{"status":"ok","version":"1.16.1",` +
			`"type":"navidrome","serverVersion":"0.63.2"}}`))
	}))
	defer player.Close()

	store := &fakePlayerStore{
		fakeStore: &fakeStore{}, configured: true, settings: configuredPlayer(player.URL),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/settings/navidrome/test", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.recorded != "ok" {
		t.Errorf("recorded status = %q, want the check remembered as connected", store.recorded)
	}
}

// Without the player settings queries there is nowhere to read or write them,
// and the routes say so rather than reporting nothing configured.
func TestThePlayerRoutesReportBeingUnavailable(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/settings/navidrome", nil),
		httptest.NewRequest(http.MethodPut, "/api/v1/settings/navidrome", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodPost, "/api/v1/settings/navidrome/test", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/settings/navidrome/rescan", nil),
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: status = %d, want 503", request.Method, request.URL.Path, response.Code)
		}
	}
}

// The settings page has to open before anybody can enter a player on it.
func TestPlayerSettingsAreReadableBeforeAnythingIsStored(t *testing.T) {
	store := &fakePlayerStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/settings/navidrome", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload navidromeSettingsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Configured || payload.ConnectionStatus != "unknown" {
		t.Fatalf("payload = %+v, want nothing configured", payload)
	}
}

func TestReadingPlayerSettingsReportsAStoreThatFailed(t *testing.T) {
	store := &fakePlayerStore{
		fakeStore: &fakeStore{}, settingsErr: errors.New("the database is unreachable"),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/settings/navidrome", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestSavingPlayerSettingsRejectsABodyThatIsNotJSON(t *testing.T) {
	store := &fakePlayerStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/settings/navidrome",
		strings.NewReader(`not json`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

// Every bound on the stored player is refused rather than corrected: an address
// nothing can be reached at is worth saying out loud.
func TestPlayerSettingsRefuseWhatCannotBeStored(t *testing.T) {
	store := &fakePlayerStore{
		fakeStore: &fakeStore{}, configured: true, settings: configuredPlayer("http://navidrome:4533"),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	for name, body := range map[string]string{
		"not a URL":     `{"baseUrl":"h ttp://navidrome","username":"schall"}`,
		"no host":       `{"baseUrl":"http://","username":"schall"}`,
		"long base URL": `{"baseUrl":"http://` + strings.Repeat("h", 500) + `","username":"schall"}`,
		"long username": `{"baseUrl":"http://navidrome:4533","username":"` + strings.Repeat("u", 201) + `"}`,
		"long password": `{"baseUrl":"http://navidrome:4533","username":"schall","password":"` +
			strings.Repeat("p", 501) + `"}`,
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut,
			"/api/v1/settings/navidrome", strings.NewReader(body)))
		if response.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, want 422; body = %s", name, response.Code, response.Body)
		}
	}
}

// A stored password is only kept if there is one to keep, and a lookup that
// failed is not the same as nothing stored.
func TestKeepingAStoredPasswordReportsAStoreThatFailed(t *testing.T) {
	store := &fakePlayerStore{
		fakeStore: &fakeStore{}, settingsErr: errors.New("the database is unreachable"),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/settings/navidrome",
		strings.NewReader(`{"baseUrl":"http://navidrome:4533","username":"schall"}`)))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestSavingPlayerSettingsReportsAStoreThatFailed(t *testing.T) {
	store := &fakePlayerStore{
		fakeStore: &fakeStore{}, saveErr: errors.New("the database is unreachable"),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/settings/navidrome",
		strings.NewReader(`{"baseUrl":"http://navidrome:4533","username":"schall","password":"p"}`)))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestTestingThePlayerReportsAStoreThatFailed(t *testing.T) {
	store := &fakePlayerStore{
		fakeStore: &fakeStore{}, settingsErr: errors.New("the database is unreachable"),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/settings/navidrome/test", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// The stored status is what the settings page shows between checks, so a check
// whose outcome could not be recorded is reported rather than answered with a
// status nothing wrote.
func TestATestedPlayerConnectionReportsAStoreThatFailed(t *testing.T) {
	store := &fakePlayerStore{
		fakeStore: &fakeStore{}, configured: true,
		settings:  configuredPlayer("http://navidrome:4533"),
		recordErr: errors.New("the database is unreachable"),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/settings/navidrome/test", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// A stored address the client cannot be built for is a configuration to fix
// rather than a server fault.
func TestTestingAPlayerWithAnUnusableAddressIsRefused(t *testing.T) {
	store := &fakePlayerStore{
		fakeStore: &fakeStore{}, configured: true, settings: configuredPlayer(""),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/settings/navidrome/test", nil))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", response.Code, response.Body)
	}
}

// Pressing "Tell Navidrome now" runs the notice right away and answers with
// the moment it landed, rather than the thirty-second wait a queued job would
// leave the button unable to show.
func TestRescanTellsThePlayerNowAndReportsWhenItWasTold(t *testing.T) {
	store := &fakePlayerStore{
		fakeStore: &fakeStore{}, configured: true,
		settings: configuredPlayer("http://navidrome:4533"),
	}
	told := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	playback := &fakePlaybackNotifier{}
	playback.onNotice = func() {
		store.settings.LastNotifiedAt = pgtype.Timestamptz{Time: told, Valid: true}
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithPlaybackNotifier(playback))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/settings/navidrome/rescan", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if playback.told != 1 {
		t.Fatalf("told = %d, want the player told once", playback.told)
	}
	var payload navidromeSettingsResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.LastNotifiedAt == nil || !payload.LastNotifiedAt.Equal(told) {
		t.Fatalf("last notified at = %v, want %v", payload.LastNotifiedAt, told)
	}
}

// Nothing is configured to tell, so the press is refused rather than asking a
// client built from an empty address.
func TestRescanIsRefusedWhenNavidromeIsNotConfigured(t *testing.T) {
	store := &fakePlayerStore{fakeStore: &fakeStore{}}
	playback := &fakePlaybackNotifier{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithPlaybackNotifier(playback))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/settings/navidrome/rescan", nil))

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if playback.told != 0 {
		t.Fatalf("told = %d, want the player left alone", playback.told)
	}
}

// Switched off means nobody wants the player told, and pressing the button
// must not turn that back on by itself.
func TestRescanIsRefusedWhenNavidromeIsSwitchedOff(t *testing.T) {
	settings := configuredPlayer("http://navidrome:4533")
	settings.Enabled = false
	store := &fakePlayerStore{fakeStore: &fakeStore{}, configured: true, settings: settings}
	playback := &fakePlaybackNotifier{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithPlaybackNotifier(playback))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/settings/navidrome/rescan", nil))

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if playback.told != 0 {
		t.Fatalf("told = %d, want the player left alone", playback.told)
	}
}

// A press is a request the person is waiting on, so a player that could not be
// reached is reported rather than answered as though nothing happened.
func TestRescanReportsWhenThePlayerCouldNotBeToldAbout(t *testing.T) {
	store := &fakePlayerStore{
		fakeStore: &fakeStore{}, configured: true,
		settings: configuredPlayer("http://navidrome:4533"),
	}
	playback := &fakePlaybackNotifier{err: errors.New("navidrome did not respond in time")}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithPlaybackNotifier(playback))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/settings/navidrome/rescan", nil))

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// Without a playback notifier registered there is nothing this route can call,
// and it says so rather than answering as though the player were told.
func TestRescanRouteReportsBeingUnavailableWithoutAPlaybackNotifier(t *testing.T) {
	store := &fakePlayerStore{
		fakeStore: &fakeStore{}, configured: true,
		settings: configuredPlayer("http://navidrome:4533"),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/settings/navidrome/rescan", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}
